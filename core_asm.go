package main

// The core code generator emits code through asm, a small set of
// operations every instruction set provides. Registers are logical; each
// emitter maps them to machine registers.

type reg uint8

const (
	rA    reg = iota // accumulator and return value
	rB               // scratch
	rC               // scratch
	rD               // scratch
	rEnv             // the closure being called, at a Tim call
	rArgc            // the argument count, at a Tim call
	rRT              // the runtime, callee-saved
	rGlob            // the global variables, callee-saved
	rFP              // frame pointer
	rSP              // stack pointer
	rArg0            // C argument registers rArg0..rArg0+5
)

func argReg(i int) reg { return rArg0 + reg(i) }

type alu uint8

const (
	aluAdd alu = iota
	aluSub
	aluAnd
	aluOr
	aluXor
	aluShl
	aluShr // logical
)

type fop uint8

const (
	fAdd fop = iota
	fSub
	fMul
	fDiv
	fAbs // unary: dst = |a|
	fNeg // unary
)

type cond uint8

const (
	cEq cond = iota
	cNe
	cLtU
	cLeU
	cGtU
	cGeU
	cLt // signed
	cGe
)

// Float conditions are ordered: false when either side is a NaN.
type fcond uint8

const (
	fLt fcond = iota
	fLe
	fGt
	fGe
	fEq
)

type label int

// asm emits machine code. Offsets of loads and stores are in bytes.
type asm interface {
	newLabel() label
	bind(l label)
	pos() int
	code() []byte
	resolve() error // patch all label references

	// Frames: fp points at the saved fp, [fp+8] holds the return address
	// and the incoming arguments start at [fp+16].
	prologue() int                   // returns a handle for setFrame
	setFrame(handle, n int)          // reserve n bytes below fp (a multiple of 16)
	epilogue()                       // return to the caller
	tailJump(target reg)             // pop the frame and jump
	tailJumpLabel(l label)           // pop the frame and jump
	start(main, blob label, off int) // the process entry point: calls blob+off (rt_start) with sp and main

	load(dst, base reg, off int32)
	store(src, base reg, off int32)
	mov(dst, src reg)
	imm(dst reg, v uint64)
	addImm(dst, src reg, v int32)
	addr(dst reg, l label) // dst = address of l
	op(o alu, dst, a, b reg)
	shiftImm(o alu, dst, a reg, n uint8)
	fop(o fop, dst, a, b reg) // doubles held as bits in integer registers

	jmp(l label)
	br(c cond, a, b reg, l label)
	fbr(c fcond, a, b reg, l label)
	brZero(a reg, l label)
	brNonZero(a reg, l label)
	brTagged(a reg, l label) // a is not a plain double: a NaN-boxed value or a NaN

	call(l label)
	callOff(l label, off int) // call l+off
	callReg(r reg)

	align(n int)
	emit(b []byte) // raw bytes such as constants
}
