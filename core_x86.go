package main

import (
	"encoding/binary"
	"fmt"
)

// x86 emits x86-64 machine code for the core code generator.
type x86 struct {
	buf    []byte
	labels []int
	fixups []fixup
}

type fixup struct {
	at  int // where the field is
	l   label
	off int // added to the label's address
	end int // x86: the rel32 is relative to this position
}

func newX86() *x86 { return &x86{} }

// rax rcx rdx r8, r10 r9, rbx r12, rbp rsp, rdi rsi rdx rcx r8 r9
var x86Regs = [...]byte{0, 1, 2, 8, 10, 9, 3, 12, 5, 4, 7, 6, 2, 1, 8, 9}

const x86Tmp = 11 // r11

func (x *x86) r(r reg) byte { return x86Regs[r] }

func (x *x86) newLabel() label {
	x.labels = append(x.labels, -1)
	return label(len(x.labels) - 1)
}

func (x *x86) bind(l label)   { x.labels[l] = len(x.buf) }
func (x *x86) pos() int       { return len(x.buf) }
func (x *x86) code() []byte   { return x.buf }
func (x *x86) b(bs ...byte)   { x.buf = append(x.buf, bs...) }
func (x *x86) u32(v uint32)   { x.buf = binary.LittleEndian.AppendUint32(x.buf, v) }
func (x *x86) emit(bs []byte) { x.buf = append(x.buf, bs...) }

func (x *x86) align(n int) {
	for len(x.buf)%n != 0 {
		x.buf = append(x.buf, 0xCC)
	}
}

func (x *x86) rel32(l label, off int) {
	x.fixups = append(x.fixups, fixup{at: len(x.buf), l: l, off: off, end: len(x.buf) + 4})
	x.u32(0)
}

func (x *x86) resolve() error {
	for _, f := range x.fixups {
		t := x.labels[f.l]
		if t < 0 {
			return fmt.Errorf("x86: unbound label %d", f.l)
		}
		binary.LittleEndian.PutUint32(x.buf[f.at:], uint32(int32(t+f.off-f.end)))
	}
	return nil
}

func rex(w bool, r, b byte) byte {
	v := byte(0x40)
	if w {
		v |= 8
	}
	return v | (r>>3)<<2 | b>>3
}

// rr emits a 64-bit register-register instruction: op modrm(11, reg, rm).
func (x *x86) rr(op byte, reg, rm byte) {
	x.b(rex(true, reg, rm), op, 0xC0|(reg&7)<<3|rm&7)
}

// mem emits a 64-bit instruction with a [base+disp] operand.
func (x *x86) mem(op []byte, reg, base byte, disp int32) {
	x.b(rex(true, reg, base))
	x.b(op...)
	mod := byte(2)
	switch {
	case disp == 0 && base&7 != 5:
		mod = 0
	case disp >= -128 && disp < 128:
		mod = 1
	}
	x.b(mod<<6 | (reg&7)<<3 | base&7)
	if base&7 == 4 {
		x.b(0x24)
	}
	switch mod {
	case 1:
		x.b(byte(disp))
	case 2:
		x.u32(uint32(disp))
	}
}

func (x *x86) prologue() int {
	x.b(0x55)             // push rbp
	x.b(0x48, 0x89, 0xE5) // mov rbp, rsp
	x.b(0x48, 0x81, 0xEC) // sub rsp, imm32
	h := len(x.buf)
	x.u32(0)
	return h
}

func (x *x86) setFrame(h, n int) { binary.LittleEndian.PutUint32(x.buf[h:], uint32(n)) }
func (x *x86) epilogue()         { x.b(0xC9, 0xC3) } // leave; ret

func (x *x86) tailJump(t reg) {
	x.b(0xC9)
	r := x.r(t)
	x.b(rex(false, 0, r), 0xFF, 0xE0|r&7)
}

func (x *x86) tailJumpLabel(l label) {
	x.b(0xC9, 0xE9)
	x.rel32(l, 0)
}

func (x *x86) start(main, blob label, off int) {
	x.b(0x48, 0x89, 0xE7)       // mov rdi, rsp
	x.b(0x48, 0x8D, 0x35)       // lea rsi, [rip+main]
	x.rel32(main, 0)            //
	x.b(0x48, 0x83, 0xE4, 0xF0) // and rsp, -16
	x.b(0xE8)                   // call rt_start
	x.rel32(blob, off)
	x.b(0x0F, 0x0B) // ud2
}

func (x *x86) load(dst, base reg, off int32)  { x.mem([]byte{0x8B}, x.r(dst), x.r(base), off) }
func (x *x86) store(src, base reg, off int32) { x.mem([]byte{0x89}, x.r(src), x.r(base), off) }

func (x *x86) mov(dst, src reg) {
	if dst != src && x.r(dst) != x.r(src) {
		x.rr(0x89, x.r(src), x.r(dst))
	}
}

func (x *x86) movImm(r byte, v uint64) {
	switch {
	case v == 0:
		if r >= 8 {
			x.b(0x45)
		}
		x.b(0x31, 0xC0|(r&7)<<3|r&7) // xor r32, r32
	case v <= 0xFFFFFFFF:
		if r >= 8 {
			x.b(0x41)
		}
		x.b(0xB8 + r&7)
		x.u32(uint32(v))
	case int64(v) >= -1<<31 && int64(v) < 1<<31:
		x.b(rex(true, 0, r), 0xC7, 0xC0|r&7)
		x.u32(uint32(v))
	default:
		x.b(rex(true, 0, r), 0xB8+r&7)
		x.buf = binary.LittleEndian.AppendUint64(x.buf, v)
	}
}

func (x *x86) imm(dst reg, v uint64) { x.movImm(x.r(dst), v) }

func (x *x86) addImm(dst, src reg, v int32) { x.mem([]byte{0x8D}, x.r(dst), x.r(src), v) } // lea

func (x *x86) addr(dst reg, l label) {
	r := x.r(dst)
	x.b(rex(true, r, 0), 0x8D, 0x05|(r&7)<<3) // lea r, [rip+disp32]
	x.rel32(l, 0)
}

var x86Alu = map[alu]byte{aluAdd: 0x01, aluSub: 0x29, aluAnd: 0x21, aluOr: 0x09, aluXor: 0x31}

func (x *x86) op(o alu, dst, a, b reg) {
	opc, ok := x86Alu[o]
	if !ok {
		panic("x86: shift by register")
	}
	d, s := x.r(dst), x.r(b)
	if d == s && d != x.r(a) {
		x.rr(0x89, s, x86Tmp)
		s = x86Tmp
	}
	x.mov(dst, a)
	x.rr(opc, s, d)
}

func (x *x86) shiftImm(o alu, dst, a reg, n uint8) {
	x.mov(dst, a)
	ext := byte(4)
	if o == aluShr {
		ext = 5
	}
	d := x.r(dst)
	x.b(rex(true, 0, d), 0xC1, 0xC0|ext<<3|d&7, n)
}

// movq xmm, r64 and back
func (x *x86) toX(xmm byte, r reg) {
	g := x.r(r)
	x.b(0x66, rex(true, xmm, g), 0x0F, 0x6E, 0xC0|xmm<<3|g&7)
}

func (x *x86) fromX(r reg, xmm byte) {
	g := x.r(r)
	x.b(0x66, rex(true, xmm, g), 0x0F, 0x7E, 0xC0|xmm<<3|g&7)
}

func (x *x86) fop(o fop, dst, a, b reg) {
	switch o {
	case fAbs:
		x.mov(dst, a)
		x.shiftImm(aluShl, dst, dst, 1)
		x.shiftImm(aluShr, dst, dst, 1)
		return
	case fNeg:
		x.mov(dst, a)
		d := x.r(dst)
		x.b(rex(true, 0, d), 0x0F, 0xBA, 0xF8|d&7, 63) // btc d, 63
		return
	}
	x.toX(0, a)
	x.toX(1, b)
	x.b(0xF2, 0x0F, map[fop]byte{fAdd: 0x58, fSub: 0x5C, fMul: 0x59, fDiv: 0x5E}[o], 0xC1)
	x.fromX(dst, 0)
}

func (x *x86) jmp(l label) {
	x.b(0xE9)
	x.rel32(l, 0)
}

func (x *x86) jcc(cc byte, l label) {
	x.b(0x0F, cc)
	x.rel32(l, 0)
}

var x86Cond = map[cond]byte{cEq: 0x84, cNe: 0x85, cLtU: 0x82, cLeU: 0x86, cGtU: 0x87, cGeU: 0x83, cLt: 0x8C, cGe: 0x8D}

func (x *x86) br(c cond, a, b reg, l label) {
	x.rr(0x39, x.r(b), x.r(a)) // cmp a, b
	x.jcc(x86Cond[c], l)
}

func (x *x86) ucomisd(a, b byte) { x.b(0x66, 0x0F, 0x2E, 0xC0|a<<3|b) }

func (x *x86) fbr(c fcond, a, b reg, l label) {
	x.toX(0, a)
	x.toX(1, b)
	switch c {
	case fGt:
		x.ucomisd(0, 1)
		x.jcc(0x87, l) // ja
	case fGe:
		x.ucomisd(0, 1)
		x.jcc(0x83, l) // jae
	case fLt:
		x.ucomisd(1, 0)
		x.jcc(0x87, l)
	case fLe:
		x.ucomisd(1, 0)
		x.jcc(0x83, l)
	case fEq:
		x.ucomisd(0, 1)
		x.b(0x7A, 6)   // jp over the je
		x.jcc(0x84, l) // je
	}
}

func (x *x86) test(a reg) { x.rr(0x85, x.r(a), x.r(a)) }

func (x *x86) brZero(a reg, l label) {
	x.test(a)
	x.jcc(0x84, l)
}

func (x *x86) brNonZero(a reg, l label) {
	x.test(a)
	x.jcc(0x85, l)
}

// brTagged: every NaN-boxed value is a NaN, and a NaN is unordered with itself.
func (x *x86) brTagged(a reg, l label) {
	x.toX(0, a)
	x.ucomisd(0, 0)
	x.jcc(0x8A, l) // jp
}

func (x *x86) call(l label) {
	x.b(0xE8)
	x.rel32(l, 0)
}

func (x *x86) callOff(l label, off int) {
	x.b(0xE8)
	x.rel32(l, off)
}

func (x *x86) callReg(r reg) {
	g := x.r(r)
	x.b(rex(false, 0, g), 0xFF, 0xD0|g&7)
}
