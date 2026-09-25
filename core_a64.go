package main

import (
	"encoding/binary"
	"fmt"
)

// a64 emits AArch64 machine code for the core code generator.
type a64 struct {
	buf    []byte
	labels []int
	fixups []a64Fixup
}

type a64Fixup struct {
	at   int
	l    label
	off  int
	kind uint8 // 0 b/bl imm26, 1 b.cond/cbz imm19, 2 adrp+add
}

func newA64() *a64 { return &a64{} }

// x0 x1 x2 x3, x9 x10, x19 x20, x29 sp, x0..x5
var a64Regs = [...]uint32{0, 1, 2, 3, 9, 10, 19, 20, 29, 31, 0, 1, 2, 3, 4, 5}

const (
	a64Tmp  = 16
	a64Tmp2 = 17
	a64SP   = 31
)

func (a *a64) r(r reg) uint32 { return a64Regs[r] }

func (a *a64) newLabel() label {
	a.labels = append(a.labels, -1)
	return label(len(a.labels) - 1)
}

func (a *a64) bindAt(l label, p int) { a.labels[l] = p }
func (a *a64) bind(l label)          { a.labels[l] = len(a.buf) }
func (a *a64) pos() int              { return len(a.buf) }
func (a *a64) code() []byte          { return a.buf }
func (a *a64) emit(bs []byte)        { a.buf = append(a.buf, bs...) }
func (a *a64) i(w uint32)            { a.buf = binary.LittleEndian.AppendUint32(a.buf, w) }

func (a *a64) align(n int) {
	for len(a.buf)%n != 0 {
		a.buf = append(a.buf, 0)
	}
}

func (a *a64) ref(kind uint8, l label, off int) {
	a.fixups = append(a.fixups, a64Fixup{at: len(a.buf), l: l, off: off, kind: kind})
}

func (a *a64) resolve() error {
	le := binary.LittleEndian
	for _, f := range a.fixups {
		t := a.labels[f.l]
		if t < 0 {
			return fmt.Errorf("arm64: unbound label %d", f.l)
		}
		t += f.off
		d := t - f.at
		w := le.Uint32(a.buf[f.at:])
		switch f.kind {
		case 0:
			if d < -1<<27 || d >= 1<<27 {
				return fmt.Errorf("arm64: branch out of range")
			}
			w |= uint32(d>>2) & 0x3FFFFFF
		case 1:
			if d < -1<<20 || d >= 1<<20 {
				return fmt.Errorf("arm64: conditional branch out of range")
			}
			w |= (uint32(d>>2) & 0x7FFFF) << 5
		case 2:
			// positions are relative to a 4 KiB aligned base
			pages := (t >> 12) - (f.at >> 12)
			w |= uint32(pages&3)<<29 | (uint32(pages>>2)&0x7FFFF)<<5
			le.PutUint32(a.buf[f.at+4:], le.Uint32(a.buf[f.at+4:])|uint32(t&0xFFF)<<10)
		}
		le.PutUint32(a.buf[f.at:], w)
	}
	return nil
}

func (a *a64) prologue() int {
	a.i(0xA9BF7BFD) // stp x29, x30, [sp, #-16]!
	a.i(0x910003FD) // mov x29, sp
	h := len(a.buf)
	a.i(0xD1400000 | a64SP<<5 | a64SP) // sub sp, sp, #hi, lsl #12
	a.i(0xD1000000 | a64SP<<5 | a64SP) // sub sp, sp, #lo
	return h
}

func (a *a64) setFrame(h, n int) {
	le := binary.LittleEndian
	le.PutUint32(a.buf[h:], le.Uint32(a.buf[h:])|uint32(n>>12&0xFFF)<<10)
	le.PutUint32(a.buf[h+4:], le.Uint32(a.buf[h+4:])|uint32(n&0xFFF)<<10)
}

func (a *a64) leave() {
	a.i(0x910003BF) // mov sp, x29
	a.i(0xA8C17BFD) // ldp x29, x30, [sp], #16
}

func (a *a64) epilogue() {
	a.leave()
	a.i(0xD65F03C0) // ret
}

func (a *a64) tailJump(t reg) {
	a.leave()
	a.i(0xD61F0000 | a.r(t)<<5) // br
}

func (a *a64) tailJumpLabel(l label) {
	a.leave()
	a.ref(0, l, 0)
	a.i(0x14000000)
}

func (a *a64) start(main, blob label, off int, imports label, os OS) {
	switch os {
	case OSWindows:
		a.adr(0, imports, 0)
		a.adr(1, main, 0)
	case OSDarwin:
		// dyld passes argc, argv and envp in x0-x2
		a.adr(3, imports, 0)
		a.adr(4, main, 0)
	default:
		a.i(0x910003E0) // mov x0, sp
		a.adr(1, main, 0)
	}
	a.ref(0, blob, off)
	a.i(0x94000000) // bl rt_start
	a.i(0xD4200000) // brk #0
}

func (a *a64) adr(d uint32, l label, off int) {
	a.ref(2, l, off)
	a.i(0x90000000 | d)        // adrp
	a.i(0x91000000 | d<<5 | d) // add #lo12
}

// mem emits a load or store of x t at [n+off].
func (a *a64) mem(load bool, t, n uint32, off int32) {
	scaled, unscaled := uint32(0xF9000000), uint32(0xF8000000)
	if load {
		scaled, unscaled = 0xF9400000, 0xF8400000
	}
	switch {
	case off >= 0 && off%8 == 0 && off/8 < 4096:
		a.i(scaled | uint32(off/8)<<10 | n<<5 | t)
	case off >= -256 && off < 256:
		a.i(unscaled | (uint32(off)&0x1FF)<<12 | n<<5 | t)
	default:
		a.addImmRaw(a64Tmp2, n, off)
		a.i(scaled | a64Tmp2<<5 | t)
	}
}

func (a *a64) load(dst, base reg, off int32)  { a.mem(true, a.r(dst), a.r(base), off) }
func (a *a64) store(src, base reg, off int32) { a.mem(false, a.r(src), a.r(base), off) }

func (a *a64) movRaw(d, s uint32) {
	if d == s {
		return
	}
	if d == a64SP || s == a64SP {
		a.i(0x91000000 | s<<5 | d) // add d, s, #0
		return
	}
	a.i(0xAA0003E0 | s<<16 | d) // orr d, xzr, s
}

func (a *a64) mov(dst, src reg) { a.movRaw(a.r(dst), a.r(src)) }

func (a *a64) immRaw(d uint32, v uint64) {
	first := true
	for hw := uint32(0); hw < 4; hw++ {
		part := uint32(v>>(16*hw)) & 0xFFFF
		if part == 0 && !(first && hw == 3) {
			continue
		}
		if first {
			a.i(0xD2800000 | hw<<21 | part<<5 | d) // movz
			first = false
		} else {
			a.i(0xF2800000 | hw<<21 | part<<5 | d) // movk
		}
	}
}

func (a *a64) imm(dst reg, v uint64) { a.immRaw(a.r(dst), v) }

func (a *a64) addImmRaw(d, n uint32, v int32) {
	switch {
	case v >= 0 && v < 4096:
		a.i(0x91000000 | uint32(v)<<10 | n<<5 | d)
	case v < 0 && v > -4096:
		a.i(0xD1000000 | uint32(-v)<<10 | n<<5 | d)
	default:
		a.immRaw(a64Tmp, uint64(int64(v)))
		a.i(0x8B206000 | a64Tmp<<16 | n<<5 | d) // add d, n, x16, uxtx
	}
}

func (a *a64) addImm(dst, src reg, v int32) { a.addImmRaw(a.r(dst), a.r(src), v) }
func (a *a64) addr(dst reg, l label)        { a.adr(a.r(dst), l, 0) }

var a64Alu = map[alu]uint32{aluAdd: 0x8B000000, aluSub: 0xCB000000, aluAnd: 0x8A000000, aluOr: 0xAA000000,
	aluXor: 0xCA000000, aluShl: 0x9AC02000, aluShr: 0x9AC02400}

func (a *a64) op(o alu, dst, x, y reg) {
	a.i(a64Alu[o] | a.r(y)<<16 | a.r(x)<<5 | a.r(dst))
}

func (a *a64) shiftImm(o alu, dst, x reg, n uint8) {
	s := uint32(n)
	if o == aluShl {
		a.i(0xD3400000 | ((64-s)&63)<<16 | (63-s)<<10 | a.r(x)<<5 | a.r(dst)) // lsl
	} else {
		a.i(0xD3400000 | s<<16 | 63<<10 | a.r(x)<<5 | a.r(dst)) // lsr
	}
}

func (a *a64) toD(d uint32, r reg)   { a.i(0x9E670000 | a.r(r)<<5 | d) } // fmov d, x
func (a *a64) fromD(r reg, d uint32) { a.i(0x9E660000 | d<<5 | a.r(r)) } // fmov x, d

func (a *a64) fop(o fop, dst, x, y reg) {
	switch o {
	case fAbs:
		a.shiftImm(aluShl, dst, x, 1)
		a.shiftImm(aluShr, dst, dst, 1)
		return
	case fNeg:
		a.immRaw(a64Tmp, 1<<63)
		a.i(0xCA000000 | a64Tmp<<16 | a.r(x)<<5 | a.r(dst)) // eor
		return
	}
	a.toD(0, x)
	a.toD(1, y)
	a.i(map[fop]uint32{fAdd: 0x1E612800, fSub: 0x1E613800, fMul: 0x1E610800, fDiv: 0x1E611800}[o])
	a.fromD(dst, 0)
}

func (a *a64) jmp(l label) {
	a.ref(0, l, 0)
	a.i(0x14000000)
}

func (a *a64) bcond(c uint32, l label) {
	a.ref(1, l, 0)
	a.i(0x54000000 | c)
}

var a64Cond = map[cond]uint32{cEq: 0, cNe: 1, cGeU: 2, cLtU: 3, cGtU: 8, cLeU: 9, cGe: 10, cLt: 11}

func (a *a64) br(c cond, x, y reg, l label) {
	a.i(0xEB00001F | a.r(y)<<16 | a.r(x)<<5) // cmp
	a.bcond(a64Cond[c], l)
}

// After fcmp, these conditions are false when the operands are unordered.
var a64FCond = map[fcond]uint32{fLt: 4, fLe: 9, fGt: 12, fGe: 10, fEq: 0}

func (a *a64) fbr(c fcond, x, y reg, l label) {
	a.toD(0, x)
	a.toD(1, y)
	a.i(0x1E612000) // fcmp d0, d1
	a.bcond(a64FCond[c], l)
}

func (a *a64) brZero(x reg, l label) {
	a.ref(1, l, 0)
	a.i(0xB4000000 | a.r(x))
}

func (a *a64) brNonZero(x reg, l label) {
	a.ref(1, l, 0)
	a.i(0xB5000000 | a.r(x))
}

func (a *a64) brTagged(x reg, l label) {
	a.toD(0, x)
	a.i(0x1E602000) // fcmp d0, d0
	a.bcond(6, l)   // b.vs
}

func (a *a64) call(l label) {
	a.ref(0, l, 0)
	a.i(0x94000000)
}

func (a *a64) callOff(l label, off int) {
	a.ref(0, l, off)
	a.i(0x94000000)
}

func (a *a64) callReg(r reg) { a.i(0xD63F0000 | a.r(r)<<5) }
