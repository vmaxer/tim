package main

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

// rv emits RV64GC machine code (without compressed instructions) for the
// core code generator.
type rv struct {
	buf    []byte
	labels []int
	fixups []cvFixup
}

type cvFixup struct {
	at   int
	l    label
	off  int
	kind uint8 // 0 jal, 1 auipc+addi/jalr pair
}

func newRV() *rv { return &rv{} }

// a0 a1 a2 a3, t0 t1, s1 s2, s0 sp, a0..a5
var cvRegs = [...]uint32{10, 11, 12, 13, 5, 6, 9, 18, 8, 2, 10, 11, 12, 13, 14, 15}

const (
	xZero = 0
	xRA   = 1
	xSP   = 2
	xFP   = 8
	xT5   = 30
	xT6   = 31
)

func (a *rv) r(r reg) uint32 { return cvRegs[r] }

func (a *rv) newLabel() label {
	a.labels = append(a.labels, -1)
	return label(len(a.labels) - 1)
}

func (a *rv) bind(l label)   { a.labels[l] = len(a.buf) }
func (a *rv) pos() int       { return len(a.buf) }
func (a *rv) code() []byte   { return a.buf }
func (a *rv) emit(bs []byte) { a.buf = append(a.buf, bs...) }
func (a *rv) i(w uint32)     { a.buf = binary.LittleEndian.AppendUint32(a.buf, w) }

func (a *rv) align(n int) {
	for len(a.buf)%n != 0 {
		a.buf = append(a.buf, 0)
	}
}

func cvI(op, f3, rd, rs1 uint32, imm int32) uint32 {
	return uint32(imm)<<20 | rs1<<15 | f3<<12 | rd<<7 | op
}

func cvS(f3, rs1, rs2 uint32, imm int32) uint32 {
	u := uint32(imm)
	return (u>>5&0x7F)<<25 | rs2<<20 | rs1<<15 | f3<<12 | (u&0x1F)<<7 | 0x23
}

func cvR(f7, f3, rd, rs1, rs2 uint32) uint32 {
	return f7<<25 | rs2<<20 | rs1<<15 | f3<<12 | rd<<7 | 0x33
}

func (a *rv) addi(rd, rs uint32, imm int32) { a.i(cvI(0x13, 0, rd, rs, imm)) }

func (a *rv) ref(kind uint8, l label, off int) {
	a.fixups = append(a.fixups, cvFixup{at: len(a.buf), l: l, off: off, kind: kind})
}

func cvJ(d int) uint32 {
	u := uint32(d)
	return (u>>20&1)<<31 | (u>>1&0x3FF)<<21 | (u>>11&1)<<20 | (u>>12&0xFF)<<12
}

func (a *rv) resolve() error {
	le := binary.LittleEndian
	for _, f := range a.fixups {
		t := a.labels[f.l]
		if t < 0 {
			return fmt.Errorf("riscv64: unbound label %d", f.l)
		}
		d := t + f.off - f.at
		switch f.kind {
		case 0:
			if d < -1<<20 || d >= 1<<20 {
				return fmt.Errorf("riscv64: jump out of range")
			}
			le.PutUint32(a.buf[f.at:], le.Uint32(a.buf[f.at:])|cvJ(d))
		case 1:
			hi := (d + 0x800) >> 12
			lo := d - hi<<12
			le.PutUint32(a.buf[f.at:], le.Uint32(a.buf[f.at:])|uint32(hi)<<12)
			le.PutUint32(a.buf[f.at+4:], le.Uint32(a.buf[f.at+4:])|uint32(lo)<<20)
		}
	}
	return nil
}

// li loads a 64-bit constant.
func (a *rv) li(rd uint32, v int64) {
	if v >= -1<<31 && v < 1<<31 {
		hi := (v + 0x800) >> 12
		lo := v - hi<<12
		if hi != 0 {
			a.i(uint32(hi)<<12&0xFFFFF000 | rd<<7 | 0x37) // lui
			if lo != 0 {
				a.i(cvI(0x1B, 0, rd, rd, int32(lo))) // addiw
			}
			return
		}
		a.addi(rd, xZero, int32(lo))
		return
	}
	lo := v << 52 >> 52
	hi := (v - lo) >> 12
	shift := 12 + bits.TrailingZeros64(uint64(hi))
	hi = (v - lo) >> shift
	a.li(rd, hi)
	a.i(cvI(0x13, 1, rd, rd, int32(shift))) // slli
	if lo != 0 {
		a.addi(rd, rd, int32(lo))
	}
}

func (a *rv) prologue() int {
	a.addi(xSP, xSP, -16)
	a.i(cvS(3, xSP, xRA, 8))
	a.i(cvS(3, xSP, xFP, 0))
	a.addi(xFP, xSP, 0)
	h := len(a.buf)
	a.i(xT6<<7 | 0x37)                   // lui t6, hi
	a.i(cvI(0x13, 0, xT6, xT6, 0))    // addi t6, t6, lo
	a.i(cvR(0x20, 0, xSP, xSP, xT6)) // sub sp, sp, t6
	return h
}

func (a *rv) setFrame(h, n int) {
	le := binary.LittleEndian
	hi := (n + 0x800) >> 12
	lo := n - hi<<12
	le.PutUint32(a.buf[h:], le.Uint32(a.buf[h:])|uint32(hi)<<12)
	le.PutUint32(a.buf[h+4:], le.Uint32(a.buf[h+4:])|uint32(lo)<<20)
}

func (a *rv) leave() {
	a.addi(xSP, xFP, 0)
	a.i(cvI(0x03, 3, xRA, xSP, 8))
	a.i(cvI(0x03, 3, xFP, xSP, 0))
	a.addi(xSP, xSP, 16)
}

func (a *rv) epilogue() {
	a.leave()
	a.i(cvI(0x67, 0, xZero, xRA, 0)) // ret
}

func (a *rv) tailJump(t reg) {
	a.leave()
	a.i(cvI(0x67, 0, xZero, a.r(t), 0))
}

func (a *rv) far(link uint32, l label, off int) {
	a.ref(1, l, off)
	a.i(xT5<<7 | 0x17)                // auipc t5
	a.i(cvI(0x67, 0, link, xT5, 0)) // jalr link, lo(t5)
}

func (a *rv) tailJumpLabel(l label) {
	a.leave()
	a.far(xZero, l, 0)
}

func (a *rv) start(main, blob label, off int) {
	a.addi(10, xSP, 0) // a0 = sp
	a.adr(11, main)
	a.far(xRA, blob, off)
	a.i(0x00100073) // ebreak
}

func (a *rv) adr(rd uint32, l label) {
	a.ref(1, l, 0)
	a.i(rd<<7 | 0x17)              // auipc
	a.i(cvI(0x13, 0, rd, rd, 0)) // addi
}

// base returns a register holding base+off with off in 12 bits.
func (a *rv) base(b uint32, off int32) (uint32, int32) {
	if off >= -2048 && off < 2048 {
		return b, off
	}
	a.li(xT6, int64(off))
	a.i(cvR(0, 0, xT6, b, xT6))
	return xT6, 0
}

func (a *rv) load(dst, b reg, off int32) {
	r, o := a.base(a.r(b), off)
	a.i(cvI(0x03, 3, a.r(dst), r, o))
}

func (a *rv) store(src, b reg, off int32) {
	r, o := a.base(a.r(b), off)
	a.i(cvS(3, r, a.r(src), o))
}

func (a *rv) mov(dst, src reg) {
	if a.r(dst) != a.r(src) {
		a.addi(a.r(dst), a.r(src), 0)
	}
}

func (a *rv) imm(dst reg, v uint64) { a.li(a.r(dst), int64(v)) }

func (a *rv) addImm(dst, src reg, v int32) {
	if v >= -2048 && v < 2048 {
		a.addi(a.r(dst), a.r(src), v)
		return
	}
	a.li(xT6, int64(v))
	a.i(cvR(0, 0, a.r(dst), a.r(src), xT6))
}

func (a *rv) addr(dst reg, l label) { a.adr(a.r(dst), l) }

var cvAlu = map[alu][2]uint32{aluAdd: {0, 0}, aluSub: {0x20, 0}, aluAnd: {0, 7}, aluOr: {0, 6}, aluXor: {0, 4},
	aluShl: {0, 1}, aluShr: {0, 5}}

func (a *rv) op(o alu, dst, x, y reg) {
	f := cvAlu[o]
	a.i(cvR(f[0], f[1], a.r(dst), a.r(x), a.r(y)))
}

func (a *rv) shiftImm(o alu, dst, x reg, n uint8) {
	f3 := uint32(1)
	if o == aluShr {
		f3 = 5
	}
	a.i(cvI(0x13, f3, a.r(dst), a.r(x), int32(n)))
}

func (a *rv) toF(f uint32, r reg)   { a.i(0xF2000053 | a.r(r)<<15 | f<<7) } // fmv.d.x
func (a *rv) fromF(r reg, f uint32) { a.i(0xE2000053 | f<<15 | a.r(r)<<7) } // fmv.x.d

func (a *rv) fop(o fop, dst, x, y reg) {
	switch o {
	case fAbs:
		a.shiftImm(aluShl, dst, x, 1)
		a.shiftImm(aluShr, dst, dst, 1)
		return
	case fNeg:
		a.li(xT6, -1<<63)
		a.i(cvR(0, 4, a.r(dst), a.r(x), xT6))
		return
	}
	a.toF(0, x)
	a.toF(1, y)
	f7 := map[fop]uint32{fAdd: 0x01, fSub: 0x05, fMul: 0x09, fDiv: 0x0D}[o]
	a.i(f7<<25 | 1<<20 | 0<<15 | 7<<12 | 0<<7 | 0x53)
	a.fromF(dst, 0)
}

func (a *rv) jmp(l label) {
	a.ref(0, l, 0)
	a.i(0x6F) // jal x0
}

// branch jumps to l when (x f3 y) holds, via an inverted branch over a jal.
func (a *rv) branch(f3, x, y uint32, l label) {
	inv := f3 ^ 1
	a.i(inv<<12 | y<<20 | x<<15 | 8<<7 | 0x63) // b!cond x, y, +8
	a.jmp(l)
}

var cvCond = map[cond][3]uint32{cEq: {0, 0, 0}, cNe: {1, 0, 0}, cLt: {4, 0, 0}, cGe: {5, 0, 0},
	cLtU: {6, 0, 0}, cGeU: {7, 0, 0}, cGtU: {6, 1, 0}, cLeU: {7, 1, 0}}

func (a *rv) br(c cond, x, y reg, l label) {
	k := cvCond[c]
	if k[1] == 1 {
		x, y = y, x
	}
	a.branch(k[0], a.r(x), a.r(y), l)
}

func (a *rv) fbr(c fcond, x, y reg, l label) {
	a.toF(0, x)
	a.toF(1, y)
	switch c {
	case fLt:
		a.i(0xA2001053 | 1<<20 | 0<<15 | xT5<<7) // flt t5, f0, f1
	case fLe:
		a.i(0xA2000053 | 1<<20 | 0<<15 | xT5<<7)
	case fGt:
		a.i(0xA2001053 | 0<<20 | 1<<15 | xT5<<7)
	case fGe:
		a.i(0xA2000053 | 0<<20 | 1<<15 | xT5<<7)
	case fEq:
		a.i(0xA2002053 | 1<<20 | 0<<15 | xT5<<7)
	}
	a.branch(1, xT5, xZero, l)
}

func (a *rv) brZero(x reg, l label)    { a.branch(0, a.r(x), xZero, l) }
func (a *rv) brNonZero(x reg, l label) { a.branch(1, a.r(x), xZero, l) }

func (a *rv) brTagged(x reg, l label) {
	a.toF(0, x)
	a.i(0xA2002053 | 0<<20 | 0<<15 | xT5<<7) // feq t5, f0, f0
	a.branch(0, xT5, xZero, l)
}

func (a *rv) call(l label)             { a.far(xRA, l, 0) }
func (a *rv) callOff(l label, off int) { a.far(xRA, l, off) }
func (a *rv) callReg(r reg)            { a.i(cvI(0x67, 0, xRA, a.r(r), 0)) }
