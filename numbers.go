//go:generate sh numrt/build.sh

package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// Operation codes shared with numrt/num.c.
const (
	numOpAdd = iota + 1
	numOpSub
	numOpMul
	numOpDiv
	numOpMod
	numOpPow
	numOpLT
	numOpLE
	numOpGT
	numOpGE
	numOpEQ
	numOpNE
	numOpStr
	numOpFloat
	numOpFloor
	numOpCeil
	numOpRound
	numOpTrunc
	numOpAbs
	numOpToI64
	numOpFromI64
	numOpFromU64
	numOpExact
)

const (
	numTagBig = 0xFFF9
	numTagRat = 0xFFFA
	numFixMax = 1 << 53
)

var numBinops = map[string]int{
	"+": numOpAdd, "-": numOpSub, "*": numOpMul, "/": numOpDiv, "%": numOpMod, "mod": numOpMod, "**": numOpPow,
	"<": numOpLT, "<=": numOpLE, ">": numOpGT, ">=": numOpGE, "==": numOpEQ, "!=": numOpNE,
}

// parseNumber returns the exact value of a numeric literal.
func parseNumber(s string) (*NumberExpr, error) {
	s = strings.ReplaceAll(s, "_", "")
	if len(s) > 2 && s[0] == '0' && strings.ContainsRune("xXbBoO", rune(s[1])) {
		n, ok := new(big.Int).SetString(s, 0)
		if !ok {
			return nil, fmt.Errorf("invalid number literal: %s", s)
		}
		return newExactNumber(new(big.Rat).SetInt(n)), nil
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("invalid number literal: %s", s)
	}
	return newExactNumber(r), nil
}

// newExactNumber canonicalizes: small integers are plain float64 values.
func newExactNumber(r *big.Rat) *NumberExpr {
	if r.IsInt() && r.Num().IsInt64() {
		if v := r.Num().Int64(); v > -numFixMax && v < numFixMax {
			return &NumberExpr{Value: float64(v)}
		}
	}
	f, _ := r.Float64()
	return &NumberExpr{Value: f, Exact: r}
}

// Rat returns the exact value, or false if the number is inexact.
func (n *NumberExpr) Rat() (*big.Rat, bool) {
	if n.Exact != nil {
		return n.Exact, true
	}
	if v := n.Value; v == math.Trunc(v) && v > -numFixMax && v < numFixMax {
		return new(big.Rat).SetInt64(int64(v)), true
	}
	return nil, false
}

func foldNumbers(op string, l, r *NumberExpr) Expression {
	a, aok := l.Rat()
	b, bok := r.Rat()
	if !aok || !bok {
		return nil
	}
	switch op {
	case "+":
		return newExactNumber(new(big.Rat).Add(a, b))
	case "-":
		return newExactNumber(new(big.Rat).Sub(a, b))
	case "*":
		return newExactNumber(new(big.Rat).Mul(a, b))
	case "/":
		if b.Sign() == 0 {
			return nil
		}
		return newExactNumber(new(big.Rat).Quo(a, b))
	case "%", "mod":
		if b.Sign() == 0 {
			return nil
		}
		q := new(big.Rat).Quo(a, b)
		t := new(big.Int).Quo(q.Num(), q.Denom())
		return newExactNumber(new(big.Rat).Sub(a, new(big.Rat).Mul(new(big.Rat).SetInt(t), b)))
	case "**":
		if !b.IsInt() || !b.Num().IsInt64() {
			return nil
		}
		e := b.Num().Int64()
		if e < -1024 || e > 1024 || (a.Sign() == 0 && e < 0) {
			return nil
		}
		ae := e
		if ae < 0 {
			ae = -ae
		}
		if int64(a.Num().BitLen()+a.Denom().BitLen())*ae > 1<<16 {
			return nil
		}
		x := new(big.Int).SetInt64(ae)
		num := new(big.Int).Exp(a.Num(), x, nil)
		den := new(big.Int).Exp(a.Denom(), x, nil)
		if e < 0 {
			num, den = den, num
		}
		return newExactNumber(new(big.Rat).SetFrac(num, den))
	}
	return nil
}

// preloadedExpr is an operand whose value is already in xmm0.
type preloadedExpr struct{}

func (*preloadedExpr) String() string  { return "<xmm0>" }
func (*preloadedExpr) expressionNode() {}

var numFloatBuiltins = map[string]bool{
	"sqrt": true, "sin": true, "cos": true, "tan": true, "asin": true, "acos": true, "atan": true,
	"log": true, "log10": true, "exp": true,
}

var numRoundBuiltins = map[string]int{
	"floor": numOpFloor, "ceil": numOpCeil, "round": numOpRound, "abs": numOpAbs,
}

// compileNumBuiltin handles math builtins on exact numbers: float functions
// convert big integers and rationals to float64 first, rounding functions and
// abs stay exact, and pow is exact exponentiation.
func (fc *TimCompiler) compileNumBuiltin(call *CallExpr) bool {
	name := call.Function
	if fc.functionSignatures[name] != nil {
		return false
	}
	if _, ok := fc.variables[name]; ok {
		return false
	}
	if name == "pow" && len(call.Args) == 2 {
		fc.compileExpression(&BinaryExpr{Left: call.Args[0], Operator: "**", Right: call.Args[1]})
		return true
	}
	if len(call.Args) != 1 {
		return false
	}
	if _, ok := call.Args[0].(*preloadedExpr); ok {
		return false
	}
	op, round := numRoundBuiltins[name]
	if !round && !numFloatBuiltins[name] {
		return false
	}
	fc.compileExpression(call.Args[0])
	if round {
		done := fc.emitNumUnaryIfBoxed(op)
		fc.compileCall(&CallExpr{Function: name, Args: []Expression{&preloadedExpr{}}})
		done.bind()
	} else {
		fc.emitNumToFloat()
		fc.compileCall(&CallExpr{Function: name, Args: []Expression{&preloadedExpr{}}})
	}
	return true
}

// numLabel is a forward rel32 jump target.
type numLabel struct {
	fc   *TimCompiler
	refs []int
}

func (fc *TimCompiler) newNumLabel() *numLabel { return &numLabel{fc: fc} }

func (l *numLabel) jcc(c JumpCondition) {
	p := l.fc.eb.text.Len()
	l.fc.out.JumpConditional(c, 0)
	l.refs = append(l.refs, p+2)
}

func (l *numLabel) jmp() {
	p := l.fc.eb.text.Len()
	l.fc.out.JumpUnconditional(0)
	l.refs = append(l.refs, p+1)
}

func (l *numLabel) bind() {
	for _, r := range l.refs {
		l.fc.patchJumpOffset(r, l.fc.eb.text.Len())
	}
}

func numStoreInt(buf []byte, x *big.Int) []byte {
	words := new(big.Int).Abs(x).Bits()
	hdr := uint64(len(words))
	if x.Sign() < 0 {
		hdr |= 1 << 63
	}
	buf = binary.LittleEndian.AppendUint64(buf, hdr)
	for _, w := range words {
		buf = binary.LittleEndian.AppendUint64(buf, uint64(w))
	}
	return buf
}

// compileExactLiteral loads a boxed big integer or rational constant into xmm0.
func (fc *TimCompiler) compileExactLiteral(r *big.Rat) {
	data := numStoreInt(nil, r.Num())
	tag := numTagBig
	if !r.IsInt() {
		data = numStoreInt(data, r.Denom())
		tag = numTagRat
	}
	label := fmt.Sprintf("num_%d", fc.stringCounter)
	fc.stringCounter++
	fc.eb.Define(label, string(data))
	fc.out.LeaSymbolToReg("rax", label)
	fc.out.ShlImmReg("rax", 16)
	fc.out.Emit([]byte{0x66, 0xB8, byte(tag), byte(tag >> 8)}) // mov ax, tag
	fc.out.Emit([]byte{0x48, 0xC1, 0xC8, 0x10})                // ror rax, 16
	fc.out.MovqRegToXmm("xmm0", "rax")
}

// emitNumCall calls the runtime: xmm0 = a, xmm1 = b -> xmm0.
func (fc *TimCompiler) emitNumCall(op int) {
	fc.trackFunctionCall("_tim_num")
	fc.out.MovImmToReg("rax", fmt.Sprint(op))
	fc.out.CallSymbol("_tim_num")
}

// emitFixnumCheck jumps to slow unless xmm0 is a non-NaN value with |x| < 2^53.
func (fc *TimCompiler) emitFixnumCheck(slow *numLabel) {
	fc.out.MovqXmmToReg("rax", "xmm0")
	fc.out.ShlImmReg("rax", 1)
	fc.out.ShrRegByImm("rax", 53)
	fc.out.CmpRegToImm("rax", 0x434)
	slow.jcc(JumpAboveOrEqual)
}

// emitIntegralCheck classifies the float64 bits in rax (clobbers rax, rcx).
func (fc *TimCompiler) emitIntegralCheck(notInt, isInt *numLabel) {
	fc.out.MovRegToReg("rcx", "rax")
	fc.out.ShlImmReg("rcx", 1)
	isInt.jcc(JumpEqual)
	fc.out.ShrRegByImm("rcx", 53)
	fc.out.CmpRegToImm("rcx", 0x434)
	notInt.jcc(JumpAboveOrEqual)
	fc.out.CmpRegToImm("rcx", 1023)
	notInt.jcc(JumpBelow)
	fc.out.SubImmFromReg("rcx", 1011)
	fc.out.ShlClReg("rax", "cl")
	isInt.jcc(JumpEqual)
	notInt.jmp()
}

// emitNumBinop computes xmm0 = xmm0 op xmm1 with exact semantics: an inline
// float64 fast path for small integers and inexact values, and a runtime call
// for big integers, rationals, overflow and errors.
func (fc *TimCompiler) emitNumBinop(op string) {
	code, ok := numBinops[op]
	if !ok {
		compilerError("unsupported numeric operator %s", op)
	}
	slow, done := fc.newNumLabel(), fc.newNumLabel()
	fc.out.SubImmFromReg("rsp", 16)
	fc.out.MovXmmToMem("xmm0", "rsp", 0)
	fc.out.MovXmmToMem("xmm1", "rsp", 8)
	switch op {
	case "+", "-", "*":
		switch op {
		case "+":
			fc.out.AddsdXmm("xmm0", "xmm1")
		case "-":
			fc.out.SubsdXmm("xmm0", "xmm1")
		case "*":
			fc.out.MulsdXmm("xmm0", "xmm1")
		}
		fc.emitFixnumCheck(slow)
		done.jmp()
	case "/":
		fc.out.DivsdXmm("xmm0", "xmm1")
		fc.emitFixnumCheck(slow)
		nonInt, aInt := fc.newNumLabel(), fc.newNumLabel()
		fc.out.MovqXmmToReg("rax", "xmm0")
		fc.emitIntegralCheck(nonInt, done)
		nonInt.bind()
		fc.out.MovMemToReg("rax", "rsp", 0)
		fc.emitIntegralCheck(done, aInt)
		aInt.bind()
		fc.out.MovMemToReg("rax", "rsp", 8)
		fc.emitIntegralCheck(done, slow)
	case "%", "mod":
		fc.out.Ucomisd("xmm0", "xmm1")
		slow.jcc(JumpParity)
		fc.out.MovqXmmToReg("rax", "xmm1")
		fc.out.ShlImmReg("rax", 1)
		slow.jcc(JumpEqual)
		fc.out.DivsdXmm("xmm0", "xmm1")
		fc.emitFixnumCheck(slow)
		fc.out.Cvttsd2si("rax", "xmm0")
		fc.out.Cvtsi2sd("xmm0", "rax")
		fc.out.MulsdXmm("xmm0", "xmm1")
		fc.out.MovMemToXmm("xmm1", "rsp", 0)
		fc.out.SubsdXmm("xmm1", "xmm0")
		fc.out.MovXmmToXmm("xmm0", "xmm1")
		done.jmp()
	case "**":
		slow.jmp()
	default:
		fc.out.Ucomisd("xmm0", "xmm1")
		slow.jcc(JumpParity)
		fc.out.MovImmToReg("rax", "0")
		fc.out.MovImmToReg("rcx", "1")
		switch op {
		case "<":
			fc.out.Cmovb("rax", "rcx")
		case "<=":
			fc.out.Cmovbe("rax", "rcx")
		case ">":
			fc.out.Cmova("rax", "rcx")
		case ">=":
			fc.out.Cmovae("rax", "rcx")
		case "==":
			fc.out.Cmove("rax", "rcx")
		case "!=":
			fc.out.Cmovne("rax", "rcx")
		}
		fc.out.Cvtsi2sd("xmm0", "rax")
		done.jmp()
	}
	slow.bind()
	fc.out.MovMemToXmm("xmm0", "rsp", 0)
	fc.out.MovMemToXmm("xmm1", "rsp", 8)
	fc.emitNumCall(code)
	done.bind()
	fc.out.AddImmToReg("rsp", 16)
}

// emitNumUnaryIfBoxed applies a runtime unary op when xmm0 is NaN (a boxed
// exact number or an error); otherwise leaves xmm0 for the inline path and
// jumps nowhere. Returns the label the inline path must bind when done.
func (fc *TimCompiler) emitNumUnaryIfBoxed(op int) *numLabel {
	inline, done := fc.newNumLabel(), fc.newNumLabel()
	fc.out.Ucomisd("xmm0", "xmm0")
	inline.jcc(JumpNotParity)
	fc.emitNumCall(op)
	done.jmp()
	inline.bind()
	return done
}

// emitNumToFloat converts xmm0 to an inexact float64 if it is a boxed number.
func (fc *TimCompiler) emitNumToFloat() {
	fc.emitNumUnaryIfBoxed(numOpFloat).bind()
}

// emitTruthy sets dst to 1 if src is truthy, else 0. Zero and error NaNs are
// falsy; boxed exact numbers (negative NaNs) are never zero. Clobbers rcx and zero.
func (fc *TimCompiler) emitTruthy(src, zero, dst string) {
	done := fc.newNumLabel()
	fc.out.XorpdXmm(zero, zero)
	fc.out.Ucomisd(src, zero)
	fc.out.MovImmToReg(dst, "0")
	fc.out.MovImmToReg("rcx", "1")
	fc.out.Cmovne(dst, "rcx")
	done.jcc(JumpNotParity)
	fc.out.MovqXmmToReg(dst, src)
	fc.out.ShrRegByImm(dst, 63)
	done.bind()
}

// emitNumToI64 truncates the number in xmm0 to a 64-bit integer in rax.
func (fc *TimCompiler) emitNumToI64() {
	inline, done := fc.newNumLabel(), fc.newNumLabel()
	fc.out.Ucomisd("xmm0", "xmm0")
	inline.jcc(JumpNotParity)
	fc.emitNumCall(numOpToI64)
	fc.out.MovqXmmToReg("rax", "xmm0")
	done.jmp()
	inline.bind()
	fc.out.Cvttsd2si("rax", "xmm0")
	done.bind()
}

// emitNumToString replaces the number in xmm0 with a Tim string.
func (fc *TimCompiler) emitNumToString() {
	fc.emitNumCall(numOpStr)
}

// generateNumRuntime emits the numeric runtime: the _tim_num trampoline, the
// allocator callback and the precompiled numrt blob.
func (fc *TimCompiler) generateNumRuntime() {
	gprs := []string{"rcx", "rdx", "rsi", "rdi", "r8", "r9", "r10", "r11"}
	xmmSlot := func(i int) int { return -8*len(gprs) - 16*i }

	// _tim_num: rax = op, xmm0 = a, xmm1 = b -> xmm0. Preserves all other registers.
	fc.eb.MarkLabel("_tim_num")
	fc.out.PushReg("rbp")
	fc.out.MovRegToReg("rbp", "rsp")
	fc.out.SubImmFromReg("rsp", int64(-xmmSlot(15)))
	for i, r := range gprs {
		fc.out.MovRegToMem(r, "rbp", -8*(i+1))
	}
	for i := 1; i <= 15; i++ {
		fc.out.MovupdXmmToMem(fmt.Sprintf("xmm%d", i), "rbp", xmmSlot(i))
	}
	fc.out.AndRegWithImm("rsp", -16)
	fc.out.MovRegToReg("rdi", "rax")
	fc.out.MovqXmmToReg("rsi", "xmm0")
	fc.out.MovqXmmToReg("rdx", "xmm1")
	fc.out.LeaSymbolToReg("rcx", "_tim_num_alloc")
	fc.out.CallSymbol("_tim_num_rt")
	fc.out.MovqRegToXmm("xmm0", "rax")
	for i := 1; i <= 15; i++ {
		fc.out.MovupdMemToXmm(fmt.Sprintf("xmm%d", i), "rbp", xmmSlot(i))
	}
	for i, r := range gprs {
		fc.out.MovMemToReg(r, "rbp", -8*(i+1))
	}
	fc.out.MovRegToReg("rsp", "rbp")
	fc.out.PopReg("rbp")
	fc.out.Ret()

	// _tim_num_alloc: SysV callback used by the runtime, rdi = size -> rax.
	calleeSaved := []string{"rbx", "r12", "r13", "r14", "r15"}
	fc.eb.MarkLabel("_tim_num_alloc")
	fc.out.PushReg("rbp")
	fc.out.MovRegToReg("rbp", "rsp")
	for _, r := range calleeSaved {
		fc.out.PushReg(r)
	}
	fc.out.AndRegWithImm("rsp", -16)
	fc.out.SubImmFromReg("rsp", 32)
	saved := fc.currentArena
	fc.currentArena = 1
	fc.callArenaAlloc()
	fc.currentArena = saved
	fc.out.LeaMemToReg("rsp", "rbp", -8*len(calleeSaved))
	for i := len(calleeSaved) - 1; i >= 0; i-- {
		fc.out.PopReg(calleeSaved[i])
	}
	fc.out.PopReg("rbp")
	fc.out.Ret()

	for fc.eb.text.Len()%64 != 0 {
		fc.out.Write(0xCC)
	}
	fc.out.Emit(numRuntimeAMD64[:numRuntimeAMD64Entry])
	fc.eb.MarkLabel("_tim_num_rt")
	fc.out.Emit(numRuntimeAMD64[numRuntimeAMD64Entry:])
}

func mapIsStatic(e *MapExpr) bool {
	for i := range e.Keys {
		k, kok := e.Keys[i].(*NumberExpr)
		v, vok := e.Values[i].(*NumberExpr)
		if !kok || !vok || k.Exact != nil || v.Exact != nil {
			return false
		}
	}
	return true
}

// compileDynamicMap builds a map literal at runtime: [count][key0][val0]...
func (fc *TimCompiler) compileDynamicMap(e *MapExpr) {
	n := len(e.Keys)
	fc.out.MovImmToReg("rdi", fmt.Sprint(8+16*n))
	fc.callArenaAlloc()
	fc.out.SubImmFromReg("rsp", 16)
	fc.out.MovRegToMem("rax", "rsp", 0)
	fc.out.MovImmToReg("rcx", fmt.Sprint(math.Float64bits(float64(n))))
	fc.out.MovRegToMem("rcx", "rax", 0)
	for i := range e.Keys {
		fc.compileExpression(e.Keys[i])
		fc.out.MovMemToReg("rax", "rsp", 0)
		fc.out.MovXmmToMem("xmm0", "rax", 8+16*i)
		fc.compileExpression(e.Values[i])
		fc.out.MovMemToReg("rax", "rsp", 0)
		fc.out.MovXmmToMem("xmm0", "rax", 16+16*i)
	}
	fc.out.MovMemToXmm("xmm0", "rsp", 0)
	fc.out.AddImmToReg("rsp", 16)
}
