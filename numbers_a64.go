package main

import "fmt"

// arm64 support for the universal number type; see numbers.go for the model.
// Scratch registers are x16/x17 (IP0/IP1), which the code generator never holds
// values in across an operation.

func (acg *ARM64CodeGen) emitWords(words ...uint32) {
	for _, w := range words {
		acg.out.out.writer.WriteBytes([]byte{byte(w), byte(w >> 8), byte(w >> 16), byte(w >> 24)})
	}
}

// a64Label is a forward branch target (B, B.cond, CBZ/CBNZ).
type a64Label struct {
	acg  *ARM64CodeGen
	refs []int
}

func (acg *ARM64CodeGen) newLabel() *a64Label { return &a64Label{acg: acg} }

func (l *a64Label) bcond(cond string) {
	l.refs = append(l.refs, l.acg.eb.text.Len())
	l.acg.out.BranchCond(cond, 0)
}

func (l *a64Label) b() {
	l.refs = append(l.refs, l.acg.eb.text.Len())
	l.acg.out.Branch(0)
}

func (l *a64Label) cbz(reg uint32) {
	l.refs = append(l.refs, l.acg.eb.text.Len())
	l.acg.emitWords(0xB4000000 | reg)
}

func (l *a64Label) bind() {
	for _, p := range l.refs {
		l.acg.patchJumpOffset(p, int32(l.acg.eb.text.Len()-p))
	}
}

const (
	a64FmovX16D0  = 0x9E660010 // fmov x16, d0
	a64FmovD0X16  = 0x9E670200 // fmov d0, x16
	a64StpD0D1    = 0x6D0007E0 // stp d0, d1, [sp]
	a64LdpD0D1    = 0x6D4007E0 // ldp d0, d1, [sp]
	a64SubSp16    = 0xD10043FF // sub sp, sp, #16
	a64AddSp16    = 0x910043FF // add sp, sp, #16
	a64PushFrame  = 0xA9BF7BFD // stp x29, x30, [sp, #-16]!
	a64MovFp      = 0x910003FD // mov x29, sp
	a64PopFrame   = 0xA8C17BFD // ldp x29, x30, [sp], #16
	a64Ret        = 0xD65F03C0 // ret
	a64Scvtf      = 0x9E620000 // scvtf d0, x0
	a64FrintzD2D0 = 0x1E65C002 // frintz d2, d0
)

// emitFixnumCheckA64 branches to slow unless d<reg> is a non-NaN value with |x| < 2^53.
func (acg *ARM64CodeGen) emitFixnumCheckA64(reg uint32, slow *a64Label) {
	acg.emitWords(
		0x9E660010|reg<<5, // fmov x16, d<reg>
		0xD37FFA10,        // lsl x16, x16, #1
		0xD375FE10,        // lsr x16, x16, #53
		0xF110D21F,        // cmp x16, #0x434
	)
	slow.bcond("hs")
}

// emitIsSmallIntA64 branches to yes if the double at [sp, #off] is an integer with
// |x| < 2^53, otherwise to no. Clobbers d2, d3, x16.
func (acg *ARM64CodeGen) emitIsSmallIntA64(off uint32, yes, no *a64Label) {
	acg.emitWords(
		0xFD4003E2|(off/8)<<10, // ldr d2, [sp, #off]
		0x1E65C043,             // frintz d3, d2
		0x1E622060,             // fcmp d3, d2
	)
	no.bcond("ne")
	acg.emitFixnumCheckA64(2, no)
	yes.b()
}

func (acg *ARM64CodeGen) emitNumCallA64(op int) {
	acg.emitWords(0xD2800000 | uint32(op)<<5) // movz x0, #op
	acg.eb.GenerateCallInstruction("_tim_num")
	acg.usesNumRuntime = true
}

var a64CmpCond = map[string]uint32{"==": 0, "!=": 1, ">=": 0xA, "<": 0x4, ">": 0xC, "<=": 0x9}

// emitNumBinopA64 computes d0 = d0 op d1 with exact semantics.
func (acg *ARM64CodeGen) emitNumBinopA64(op string) {
	code := numBinops[op]
	slow, done := acg.newLabel(), acg.newLabel()
	acg.emitWords(a64SubSp16, a64StpD0D1)
	switch op {
	case "+", "-", "*":
		acg.emitWords(map[string]uint32{"+": 0x1E612800, "-": 0x1E613800, "*": 0x1E610800}[op])
		acg.emitFixnumCheckA64(0, slow)
		done.b()
	case "/":
		acg.emitWords(0x1E611800) // fdiv d0, d0, d1
		acg.emitFixnumCheckA64(0, slow)
		acg.emitWords(a64FrintzD2D0, 0x1E602040) // fcmp d2, d0
		done.bcond("eq")
		aInt := acg.newLabel()
		acg.emitIsSmallIntA64(0, aInt, done)
		aInt.bind()
		acg.emitIsSmallIntA64(8, slow, done)
	case "%", "mod":
		acg.emitWords(0x1E612000) // fcmp d0, d1
		slow.bcond("vs")
		acg.emitWords(0x9E660030, 0xD37FFA10) // fmov x16, d1; lsl x16, x16, #1
		slow.cbz(16)
		acg.emitWords(0x1E611802) // fdiv d2, d0, d1
		acg.emitFixnumCheckA64(2, slow)
		acg.emitWords(
			0x1E65C042, // frintz d2, d2
			0x1E610842, // fmul d2, d2, d1
			0x1E623800, // fsub d0, d0, d2
		)
		done.b()
	case "**":
		slow.b()
	default:
		acg.emitWords(0x1E612000) // fcmp d0, d1
		slow.bcond("vs")
		acg.emitWords(0x9A9F07E0|(a64CmpCond[op]^1)<<12, a64Scvtf) // cset x0, cond; scvtf d0, x0
		done.b()
	}
	slow.bind()
	acg.emitWords(a64LdpD0D1)
	acg.emitNumCallA64(code)
	done.bind()
	acg.emitWords(a64AddSp16)
}

// emitNumUnaryIfBoxedA64 runs op in the runtime when d0 is NaN; the returned
// label must be bound after the inline path.
func (acg *ARM64CodeGen) emitNumUnaryIfBoxedA64(op int) *a64Label {
	inline, done := acg.newLabel(), acg.newLabel()
	acg.emitWords(0x1E602000) // fcmp d0, d0
	inline.bcond("vc")
	acg.emitNumCallA64(op)
	done.b()
	inline.bind()
	return done
}

func (acg *ARM64CodeGen) emitNumToFloatA64() {
	acg.emitNumUnaryIfBoxedA64(numOpFloat).bind()
}

// emitNumToI64A64 truncates the number in d0 to a signed 64-bit integer in x0.
func (acg *ARM64CodeGen) emitNumToI64A64() {
	inline, done := acg.newLabel(), acg.newLabel()
	acg.emitWords(0x1E602000) // fcmp d0, d0
	inline.bcond("vc")
	acg.emitNumCallA64(numOpToI64)
	acg.emitWords(0x9E660000) // fmov x0, d0
	done.b()
	inline.bind()
	acg.emitWords(0x9E780000) // fcvtzs x0, d0
	done.bind()
}

func (acg *ARM64CodeGen) compileExactLiteralA64(e *NumberExpr) {
	data := numStoreInt(nil, e.Exact.Num())
	tag := uint32(numTagBig)
	if !e.Exact.IsInt() {
		data = numStoreInt(data, e.Exact.Denom())
		tag = numTagRat
	}
	label := fmt.Sprintf("num_%d", acg.stringCounter)
	acg.stringCounter++
	acg.eb.Define(label, string(data))
	acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{offset: uint64(acg.eb.text.Len()), symbolName: label})
	acg.emitWords(
		0x90000010,        // adrp x16, label
		0x91000210,        // add x16, x16, :lo12:label
		0xD2E00011|tag<<5, // movz x17, #tag, lsl #48
		0xAA110210,        // orr x16, x16, x17
		a64FmovD0X16,
	)
}

// generateNumRuntimeA64 emits _tim_num (x0 = op, d0 = a, d1 = b -> d0; preserves
// all other registers), the allocation callback and the numrt blob.
func (acg *ARM64CodeGen) generateNumRuntimeA64() {
	const frame, ctx = 528, 512
	qregs := []uint32{1, 2, 3, 4, 5, 6, 7, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	saveRegs := func(stpX, stpQ, strQ uint32) {
		for r := uint32(1); r <= 17; r += 2 {
			acg.emitWords(stpX | ((r-1)/2*2)<<15 | (r+1)<<10 | 31<<5 | r)
		}
		for i := 0; i+1 < len(qregs); i += 2 {
			acg.emitWords(stpQ | uint32(144/16+i)<<15 | qregs[i+1]<<10 | 31<<5 | qregs[i])
		}
		last := len(qregs) - 1
		acg.emitWords(strQ | uint32(144/16+last)<<10 | 31<<5 | qregs[last])
	}

	acg.eb.MarkLabel("_tim_num")
	acg.emitWords(a64PushFrame, a64MovFp, 0xD1000000|frame<<10|31<<5|31)
	saveRegs(0xA9000000, 0xAD000000, 0x3D800000)
	adrPos := acg.eb.text.Len()
	acg.emitWords(
		0x9E660001,                 // fmov x1, d0
		0x9E660022,                 // fmov x2, d1
		0x10000003,                 // adr x3, _tim_num_alloc (patched below)
		0x91000000|ctx<<10|31<<5|4, // add x4, sp, #ctx
		0xF9000098,                 // str x24, [x4]
		0xF9000499,                 // str x25, [x4, #8]
	)
	adrPos += 8
	blPos := acg.eb.text.Len()
	acg.emitWords(0x94000000) // bl _tim_num_rt (patched below)
	acg.emitWords(
		0x9E670000,                      // fmov d0, x0
		0xF9400000|(ctx/8)<<10|31<<5|24, // ldr x24, [sp, #ctx]
		0xF9400000|(ctx/8+1)<<10|31<<5|25,
	)
	saveRegs(0xA9400000, 0xAD400000, 0x3DC00000)
	acg.emitWords(0x91000000|frame<<10|31<<5|31, a64PopFrame, a64Ret)

	// _tim_num_alloc(ctx, size): run _tim_malloc on Tim's heap registers kept in ctx.
	allocPos := acg.eb.text.Len()
	acg.eb.MarkLabel("_tim_num_alloc")
	acg.emitWords(
		a64PushFrame, a64MovFp,
		0xA9BF67F8, // stp x24, x25, [sp, #-16]!
		0xA9BF7FE0, // stp x0, xzr, [sp, #-16]!
		0xF9400018, // ldr x24, [x0]
		0xF9400419, // ldr x25, [x0, #8]
		0xAA0103E0, // mov x0, x1
	)
	acg.eb.GenerateCallInstruction("_tim_malloc")
	acg.emitWords(
		0xF94003E1, // ldr x1, [sp]
		a64AddSp16,
		0xF9000038, // str x24, [x1]
		0xF9000439, // str x25, [x1, #8]
		0xA8C167F8, // ldp x24, x25, [sp], #16
		a64PopFrame, a64Ret,
	)

	for acg.eb.text.Len()%16 != 0 {
		acg.emitWords(0xD503201F) // nop
	}
	blobPos := acg.eb.text.Len() + numRuntimeARM64Entry
	acg.out.out.writer.WriteBytes(numRuntimeARM64)

	text := acg.eb.text.Bytes()
	adrOff := uint32(allocPos - adrPos)
	adr := uint32(0x10000003) | (adrOff&3)<<29 | ((adrOff>>2)&0x7FFFF)<<5
	bl := uint32(0x94000000) | (uint32(blobPos-blPos)/4)&0x3FFFFFF
	for i := range 4 {
		text[adrPos+i] = byte(adr >> (8 * i))
		text[blPos+i] = byte(bl >> (8 * i))
	}
}

// emitNumToStringA64 replaces the number in d0 with a Tim string (numeric pointer).
func (acg *ARM64CodeGen) emitNumToStringA64() {
	acg.emitNumCallA64(numOpStr)
	acg.emitWords(0x9E660000, a64Scvtf) // fmov x0, d0; scvtf d0, x0
}

// checkFMA follows a fused multiply-add (d2*d1 ± d3, result in d0) with the
// exactness check, recomputing through the runtime when needed.
func (acg *ARM64CodeGen) checkFMA(e *FMAExpr, err error) error {
	if err != nil {
		return err
	}
	slow, done := acg.newLabel(), acg.newLabel()
	acg.emitFixnumCheckA64(0, slow)
	done.b()
	slow.bind()
	acg.emitWords(0x1E604040) // fmov d0, d2
	acg.emitNumCallA64(numOpMul)
	if e.IsNegMul {
		acg.emitWords(0x1E604001, 0x9E6703E0) // fmov d1, d0; fmov d0, xzr
		acg.emitNumCallA64(numOpSub)
	}
	acg.emitWords(0x1E604061) // fmov d1, d3
	if e.IsSub {
		acg.emitNumCallA64(numOpSub)
	} else {
		acg.emitNumCallA64(numOpAdd)
	}
	done.bind()
	return nil
}

var a64RoundOps = map[string]uint32{
	"floor": 0x1E654000, // frintm d0, d0
	"ceil":  0x1E64C000, // frintp d0, d0
	"round": 0x1E664000, // frinta d0, d0 (ties away from zero)
	"trunc": 0x1E65C000, // frintz d0, d0
	"abs":   0x1E60C000, // fabs d0, d0
}

var a64RoundCodes = map[string]int{"floor": numOpFloor, "ceil": numOpCeil, "round": numOpRound, "trunc": numOpTrunc, "abs": numOpAbs}

// compileNumBuiltinA64 is the arm64 counterpart of compileNumBuiltin.
func (acg *ARM64CodeGen) compileNumBuiltinA64(call *CallExpr) (bool, error) {
	name := call.Function
	if acg.lambdaVars[name] {
		return false, nil
	}
	if _, ok := acg.stackVars[name]; ok {
		return false, nil
	}
	if name == "pow" && len(call.Args) == 2 {
		return true, acg.compileExpression(&BinaryExpr{Left: call.Args[0], Operator: "**", Right: call.Args[1]})
	}
	if (name == "min" || name == "max") && len(call.Args) == 2 {
		if err := acg.compileExpression(call.Args[0]); err != nil {
			return true, err
		}
		acg.emitWords(a64SubSp16, 0xFD0003E0) // str d0, [sp]
		if err := acg.compileExpression(call.Args[1]); err != nil {
			return true, err
		}
		acg.emitWords(0xFD0007E0, a64LdpD0D1) // str d0, [sp, #8]; ldp d0, d1, [sp]
		if name == "min" {
			acg.emitNumBinopA64("<=")
		} else {
			acg.emitNumBinopA64(">=")
		}
		acg.emitWords(
			0x1E602008, // fcmp d0, #0.0
			0x6D400BE1, // ldp d1, d2, [sp]
			0x1E621C20, // fcsel d0, d1, d2, ne
			a64AddSp16,
		)
		return true, nil
	}
	if len(call.Args) != 1 {
		return false, nil
	}
	if _, ok := call.Args[0].(*preloadedExpr); ok {
		return false, nil
	}
	if instr, ok := a64RoundOps[name]; ok {
		if err := acg.compileExpression(call.Args[0]); err != nil {
			return true, err
		}
		done := acg.emitNumUnaryIfBoxedA64(a64RoundCodes[name])
		acg.emitWords(instr)
		done.bind()
		return true, nil
	}
	if numFloatBuiltins[name] {
		if err := acg.compileExpression(call.Args[0]); err != nil {
			return true, err
		}
		acg.emitNumToFloatA64()
		return true, acg.compileCall(&CallExpr{Function: name, Args: []Expression{&preloadedExpr{}}})
	}
	return false, nil
}

// emitNumFromI64A64 converts the signed 64-bit integer in x0 to a number in d0.
func (acg *ARM64CodeGen) emitNumFromI64A64() {
	slow, done := acg.newLabel(), acg.newLabel()
	acg.emitWords(a64Scvtf)
	acg.emitFixnumCheckA64(0, slow)
	done.b()
	slow.bind()
	acg.emitWords(0x9E670000) // fmov d0, x0
	acg.emitNumCallA64(numOpFromI64)
	done.bind()
}

var a64BitwiseOps = map[string][]uint32{
	"|b":   {0xAA010000},             // orr x0, x0, x1
	"&b":   {0x8A010000},             // and x0, x0, x1
	"^b":   {0xCA010000},             // eor x0, x0, x1
	"<<b":  {0x9AC12000},             // lsl x0, x0, x1
	">>b":  {0x9AC12400},             // lsr x0, x0, x1
	">>>b": {0x9AC12C00},             // ror x0, x0, x1
	"<<<b": {0xCB0103E1, 0x9AC12C00}, // neg x1, x1; ror x0, x0, x1
	"?b":   {0x9AC12400, 0x92400000}, // lsr x0, x0, x1; and x0, x0, #1
}

// emitBitwiseA64 computes d0 = d0 op d1 on 64-bit two's complement integers.
func (acg *ARM64CodeGen) emitBitwiseA64(op string) {
	acg.emitWords(a64SubSp16, 0xFD0003E1) // str d1, [sp]
	acg.emitNumToI64A64()
	acg.emitWords(0xF90007E0, 0xFD4003E0) // str x0, [sp, #8]; ldr d0, [sp]
	acg.emitNumToI64A64()
	acg.emitWords(0xAA0003E1, 0xF94007E0, a64AddSp16) // mov x1, x0; ldr x0, [sp, #8]
	acg.emitWords(a64BitwiseOps[op]...)
	acg.emitNumFromI64A64()
}

// emitTruthyA64 sets x<d> to 1 if d<n> is truthy, else 0. Zero and error NaNs are
// falsy; boxed exact numbers (negative NaNs) are never zero. Clobbers x16.
func (acg *ARM64CodeGen) emitTruthyA64(n, d uint32) {
	acg.emitWords(
		0x1E602008|n<<5,    // fcmp d<n>, #0.0
		0x9A9F07E0|d,       // cset x<d>, ne
		0x9E660010|n<<5,    // fmov x16, d<n>
		0xD37FFE10,         // lsr x16, x16, #63
		0x9A806200|d<<16|d, // csel x<d>, x16, x<d>, vs
	)
}

// emitListConcatA64 emits _tim_list_concat(x0 = left, x1 = right) -> x0, a new
// flat list [count][elem0][elem1]...
func (acg *ARM64CodeGen) emitListConcatA64() {
	acg.eb.MarkLabel("_tim_list_concat")
	acg.emitWords(
		0xA9BC7BFD, // stp x29, x30, [sp, #-64]!
		0xA90153F3, // stp x19, x20, [sp, #16]
		0xA9025BF5, // stp x21, x22, [sp, #32]
		0xF9001BF7, // str x23, [sp, #48]
		a64MovFp,
		0xAA0003F3, // mov x19, x0
		0xAA0103F4, // mov x20, x1
		0xFD400260, // ldr d0, [x19]
		0x9E780015, // fcvtzs x21, d0
		0xFD400280, // ldr d0, [x20]
		0x9E780016, // fcvtzs x22, d0
		0x8B1602B7, // add x23, x21, x22
		0xD37DF2E0, // lsl x0, x23, #3
		0x91002000, // add x0, x0, #8
	)
	acg.eb.GenerateCallInstruction("_tim_malloc")
	acg.emitWords(
		0xAA0003E9, // mov x9, x0
		0x9E6202E0, // scvtf d0, x23
		0xFC008520, // str d0, [x9], #8
	)
	for _, side := range [][2]uint32{{0x9100226B, 0xAA1503EA}, {0x9100228B, 0xAA1603EA}} {
		acg.emitWords(side[0], side[1]) // add x11, list, #8; mov x10, count
		loop := acg.eb.text.Len()
		done := acg.newLabel()
		done.cbz(10)
		acg.emitWords(
			0xFC408560, // ldr d0, [x11], #8
			0xFC008520, // str d0, [x9], #8
			0xD100054A, // sub x10, x10, #1
		)
		acg.emitWords(0x14000000 | uint32(int32(loop-acg.eb.text.Len())/4)&0x3FFFFFF)
		done.bind()
	}
	acg.emitWords(
		0xD37DF2E1, // lsl x1, x23, #3
		0xCB010120, // sub x0, x9, x1
		0xD1002000, // sub x0, x0, #8
		0xA94153F3, // ldp x19, x20, [sp, #16]
		0xA9425BF5, // ldp x21, x22, [sp, #32]
		0xF9401BF7, // ldr x23, [sp, #48]
		0xA8C47BFD, // ldp x29, x30, [sp], #64
		a64Ret,
	)
}

// emitListRepeatA64 emits _tim_list_repeat(x0 = list, x1 = times) -> x0.
func (acg *ARM64CodeGen) emitListRepeatA64() {
	acg.eb.MarkLabel("_tim_list_repeat")
	acg.emitWords(
		0xA9BC7BFD, // stp x29, x30, [sp, #-64]!
		0xA90153F3, // stp x19, x20, [sp, #16]
		0xA9025BF5, // stp x21, x22, [sp, #32]
		a64MovFp,
		0xAA0003F3, // mov x19, x0
		0xF100003F, // cmp x1, #0
		0x9A9FA034, // csel x20, x1, xzr, ge
		0xFD400260, // ldr d0, [x19]
		0x9E780015, // fcvtzs x21, d0
		0x9B147EB6, // mul x22, x21, x20
		0xD37DF2C0, // lsl x0, x22, #3
		0x91002000, // add x0, x0, #8
	)
	acg.eb.GenerateCallInstruction("_tim_malloc")
	acg.emitWords(
		0x9E6202C0, // scvtf d0, x22
		0xFD000000, // str d0, [x0]
		0x91002009, // add x9, x0, #8
	)
	outer := acg.eb.text.Len()
	done := acg.newLabel()
	done.cbz(20)
	acg.emitWords(
		0x9100226B, // add x11, x19, #8
		0xAA1503EA, // mov x10, x21
	)
	inner := acg.eb.text.Len()
	next := acg.newLabel()
	next.cbz(10)
	acg.emitWords(
		0xFC408560, // ldr d0, [x11], #8
		0xFC008520, // str d0, [x9], #8
		0xD100054A, // sub x10, x10, #1
	)
	acg.emitWords(0x14000000 | uint32(int32(inner-acg.eb.text.Len())/4)&0x3FFFFFF)
	next.bind()
	acg.emitWords(0xD1000694) // sub x20, x20, #1
	acg.emitWords(0x14000000 | uint32(int32(outer-acg.eb.text.Len())/4)&0x3FFFFFF)
	done.bind()
	acg.emitWords(
		0xA94153F3, // ldp x19, x20, [sp, #16]
		0xA9425BF5, // ldp x21, x22, [sp, #32]
		0xA8C47BFD, // ldp x29, x30, [sp], #64
		a64Ret,
	)
}

// emitWriteListA64 writes the flat list whose numeric pointer is in d0 as [a, b, ...].
func (acg *ARM64CodeGen) emitWriteListA64(fd uint64) error { return acg.emitWriteSeqA64(fd, false) }

// emitWriteSeqA64 writes a list as [a, b, ...] or a map's values as {a, b, ...}.
func (acg *ARM64CodeGen) emitWriteSeqA64(fd uint64, isMap bool) error {
	open, closing, idx, val := "[", "]", uint32(0x8B020C00), uint32(0xFD400400)
	if isMap {
		open, closing, idx, val = "{", "}", 0x8B021000, 0xFD400800 // add x0, x0, x2, lsl #4; ldr d0, [x0, #16]
	}
	acg.emitWords(0x9E780000, a64SubSp16, 0xF90003E0, 0xF90007FF) // fcvtzs x0, d0; sub sp; str x0, [sp]; str xzr, [sp, #8]
	if err := acg.emitWriteLiteral(open, fd); err != nil {
		return err
	}
	loop := acg.eb.text.Len()
	end, first := acg.newLabel(), acg.newLabel()
	acg.emitWords(
		0xF94003E0, // ldr x0, [sp]
		0xFD400000, // ldr d0, [x0]
		0x9E780001, // fcvtzs x1, d0
		0xF94007E2, // ldr x2, [sp, #8]
		0xEB01005F, // cmp x2, x1
	)
	end.bcond("ge")
	first.cbz(2)
	if err := acg.emitWriteLiteral(", ", fd); err != nil {
		return err
	}
	first.bind()
	acg.emitWords(
		0xF94003E0, // ldr x0, [sp]
		0xF94007E2, // ldr x2, [sp, #8]
		idx,        // add x0, x0, x2, lsl #3
		val,        // ldr d0, [x0, #8]
	)
	if err := acg.emitWriteFloatSmart(fd); err != nil {
		return err
	}
	acg.emitWords(
		0xF94007E2, // ldr x2, [sp, #8]
		0x91000442, // add x2, x2, #1
		0xF90007E2, // str x2, [sp, #8]
	)
	acg.emitWords(0x14000000 | uint32(int32(loop-acg.eb.text.Len())/4)&0x3FFFFFF)
	end.bind()
	acg.emitWords(a64AddSp16)
	return acg.emitWriteLiteral(closing, fd)
}

// compileDynamicMapA64 builds a map literal on the heap, evaluating each key and value.
func (acg *ARM64CodeGen) compileDynamicMapA64(e *MapExpr) error {
	n := len(e.Keys)
	if err := acg.out.MovImm64("x0", uint64(8+16*n)); err != nil {
		return err
	}
	if err := acg.eb.GenerateCallInstruction("_tim_malloc"); err != nil {
		return err
	}
	if err := acg.out.MovImm64("x1", uint64(n)); err != nil {
		return err
	}
	acg.emitWords(
		a64SubSp16,
		0xF90003E0, // str x0, [sp]
		0x9E620020, // scvtf d0, x1
		0xFD000000, // str d0, [x0]
	)
	for i := range e.Keys {
		for j, ex := range []Expression{e.Keys[i], e.Values[i]} {
			if err := acg.compileExpression(ex); err != nil {
				return err
			}
			acg.emitWords(0xF94003E9) // ldr x9, [sp]
			if err := acg.out.StrImm64Double("d0", "x9", int32(8+16*i+8*j)); err != nil {
				return err
			}
		}
	}
	acg.emitWords(0xF94003E0, a64AddSp16, a64Scvtf) // ldr x0, [sp]; add sp; scvtf d0, x0
	return nil
}
