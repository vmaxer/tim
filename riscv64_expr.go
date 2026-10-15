package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Expressions: the value ends up in fa0.

func (g *rvGen) inTail(e Expression, tail bool) error {
	if !tail {
		return g.expr(e)
	}
	saved := g.sc.tailExpr
	g.sc.tailExpr = e
	defer func() { g.sc.tailExpr = saved }()
	return g.expr(e)
}

func (g *rvGen) number(v float64) { g.number1(rvFA0, v) }

func (g *rvGen) number1(fd uint32, v float64) {
	if bits := math.Float64bits(v); bits == 0 {
		g.fmvDX(fd, 0)
	} else {
		g.li(rvT0, int64(bits))
		g.fmvDX(fd, rvT0)
	}
}

func (g *rvGen) str(s string) *rvLabel {
	if l, ok := g.strs[s]; ok {
		return l
	}
	runes := []rune(s)
	b := binary.LittleEndian.AppendUint64(nil, math.Float64bits(tagString))
	b = binary.LittleEndian.AppendUint64(b, math.Float64bits(float64(len(runes))))
	for i, r := range runes {
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(float64(i)))
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(float64(r)))
	}
	l := g.constData(b)
	g.strs[s] = l
	return l
}

func (g *rvGen) loadStr(fd uint32, s string) {
	g.la(rvT0, g.str(s))
	g.fmvDX(fd, rvT0)
}

func (g *rvGen) expr(e Expression) error {
	switch e := e.(type) {
	case *NumberExpr:
		if e.Exact == nil {
			g.number(e.Value)
			return nil
		}
		data, tag := numStoreInt(nil, e.Exact.Num()), int64(numTagBig)
		if !e.Exact.IsInt() {
			data, tag = numStoreInt(data, e.Exact.Denom()), numTagRat
		}
		g.la(rvT0, g.constData(data))
		g.li(rvT1, tag<<48)
		g.op(0, 6, rvT0, rvT0, rvT1)
		g.fmvDX(rvFA0, rvT0)
	case *BooleanExpr:
		g.number(map[bool]float64{true: 1}[e.Value])
	case *RandomExpr:
		g.random()
	case *StringExpr:
		g.loadStr(rvFA0, e.Value)
	case *FStringExpr:
		return g.fstring(e)
	case *IdentExpr:
		return g.loadVar(rvFA0, e.Name)
	case *NamespacedIdentExpr:
		if v, ok := g.cConstant(e); ok {
			return g.expr(&NumberExpr{Value: float64(v)})
		}
		return g.field(&FieldAccessExpr{Object: &IdentExpr{Name: e.Namespace}, FieldName: e.Name})
	case *BinaryExpr:
		return g.binary(e)
	case *UnaryExpr:
		return g.unary(e.Operator, e.Operand)
	case *LengthExpr:
		return g.unary("#", e.Operand)
	case *FMAExpr:
		var prod Expression = &BinaryExpr{Left: e.A, Operator: "*", Right: e.B}
		if e.IsNegMul {
			prod = &UnaryExpr{Operator: "-", Operand: prod}
		}
		op := "+"
		if e.IsSub {
			op = "-"
		}
		return g.expr(&BinaryExpr{Left: prod, Operator: op, Right: e.C})
	case *CastExpr:
		return g.cast(e)
	case *UnsafeExpr:
		return g.unsafe(e)
	case *MoveExpr:
		return g.expr(e.Expr)
	case *ListExpr:
		return g.list(e)
	case *MapExpr:
		return g.mapLit(e)
	case *IndexExpr:
		return g.index(e)
	case *SliceExpr:
		return g.slice(e)
	case *FieldAccessExpr:
		return g.field(e)
	case *RangeExpr:
		return g.rangeValue(e)
	case *InExpr:
		return g.valOp2(valIn, e.Value, e.Container)
	case *MatchExpr:
		return g.match(e)
	case *BlockExpr:
		return g.block(e)
	case *JumpExpr:
		return g.jump(e.Label, e.IsBreak, e.Value)
	case *CallExpr:
		return g.callExpr(e)
	case *DirectCallExpr:
		return g.callValue(e.Callee, e.Args, e)
	case *LambdaExpr:
		return g.closure(e, "")
	case *PipeExpr:
		return g.pipe(e.Left, e.Right)
	case *ParallelExpr:
		return g.pipe(e.List, e.Operation)
	case *ComposeExpr:
		return g.compose(e)
	default:
		return fmt.Errorf("riscv64: %T expressions are not supported yet", e)
	}
	return nil
}

// random leaves a uniformly distributed number in [0, 1) from the kernel's
// CSPRNG (getrandom) in fa0.
func (g *rvGen) random() {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	t := g.tmp()
	g.slotAddr(rvA0, t)
	g.li(rvA1, 8)
	g.li(rvA2, 0)
	g.li(rvA7, 278)
	g.ecall()
	g.ldx(rvT0, rvS0, t)
	g.srli(rvT0, rvT0, 11)
	g.fcvtDL(rvFA0, rvT0)
	g.number1(rvFT0, 1.0/(1<<53))
	g.fop(0x09, 7, rvFA0, rvFA0, rvFT0)
}

func (g *rvGen) block(e *BlockExpr) error {
	tail := g.sc.tailExpr == Expression(e)
	for i, s := range e.Statements {
		if i == len(e.Statements)-1 {
			switch s := s.(type) {
			case *ExpressionStmt:
				if _, ok := s.Expr.(*PostfixExpr); !ok {
					return g.inTail(s.Expr, tail)
				}
			case *AssignStmt:
				if err := g.stmt(s); err != nil {
					return err
				}
				return g.loadVar(rvFA0, s.Name)
			case *IfStmt:
				return g.ifValue(s, tail)
			}
		}
		if err := g.stmt(s); err != nil {
			return err
		}
	}
	if len(e.Statements) == 0 {
		g.number(1)
		return nil
	}
	if _, ok := e.Statements[len(e.Statements)-1].(*ExpressionStmt); !ok {
		g.number(1)
	}
	return nil
}

// ifValue compiles an if statement whose value is used: each branch yields
// the value of its last statement (0 when no branch runs).
func (g *rvGen) ifValue(s *IfStmt, tail bool) error {
	end := g.newLabel()
	branch := func(body []Statement) error {
		b := &BlockExpr{Statements: body}
		if tail {
			return g.inTail(b, true)
		}
		return g.block(b)
	}
	for _, b := range s.Branches {
		next := g.newLabel()
		if err := g.truthy(b.Condition); err != nil {
			return err
		}
		g.beqz(rvA0, next)
		if err := branch(b.Body); err != nil {
			return err
		}
		g.j(end)
		g.bind(next)
	}
	if len(s.ElseBody) > 0 {
		if err := branch(s.ElseBody); err != nil {
			return err
		}
	} else {
		g.number(0)
	}
	g.bind(end)
	return nil
}

func (g *rvGen) match(e *MatchExpr) error {
	tail := g.sc.tailExpr == Expression(e)
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	cond := g.tmp()
	if err := g.expr(e.Condition); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, cond)
	end := g.newLabel()
	for _, c := range e.Clauses {
		next := g.newLabel()
		switch {
		case c.IsValueMatch:
			if err := g.expr(c.Guard); err != nil {
				return err
			}
			g.fmvD(rvFA1, rvFA0)
			g.fldx(rvFA0, rvS0, cond)
			g.numBinop("==", !g.isNum(c.Guard) || !g.isNum(e.Condition))
			g.fmvXD(rvA0, rvFA0)
		case c.Guard != nil:
			if err := g.truthy(c.Guard); err != nil {
				return err
			}
		default:
			g.fldx(rvFA0, rvS0, cond)
			g.truthyFA0()
		}
		g.beqz(rvA0, next)
		if c.Result != nil {
			if err := g.inTail(c.Result, tail); err != nil {
				return err
			}
		} else {
			g.number(0)
		}
		g.j(end)
		g.bind(next)
	}
	switch {
	case e.DefaultExpr != nil:
		if err := g.inTail(e.DefaultExpr, tail); err != nil {
			return err
		}
	case len(e.Clauses) == 0:
		g.fldx(rvFA0, rvS0, cond)
	default:
		g.number(0)
	}
	g.bind(end)
	return nil
}

// simple reports whether e can be loaded into any FP register without a
// call or touching other FP registers.
func (g *rvGen) simple(e Expression) bool {
	switch e := e.(type) {
	case *NumberExpr:
		return e.Exact == nil
	case *BooleanExpr, *StringExpr:
		return true
	case *IdentExpr:
		return g.isVar(e.Name)
	}
	return false
}

// exprTo evaluates a simple expression into fd.
func (g *rvGen) exprTo(fd uint32, e Expression) error {
	switch e := e.(type) {
	case *NumberExpr:
		g.number1(fd, e.Value)
	case *BooleanExpr:
		g.number1(fd, map[bool]float64{true: 1}[e.Value])
	case *StringExpr:
		g.loadStr(fd, e.Value)
	case *IdentExpr:
		return g.loadVar(fd, e.Name)
	}
	return nil
}

// operands evaluates l into fa0 and r into fa1, spilling l only when
// evaluating r could clobber it.
func (g *rvGen) operands(l, r Expression) error {
	if g.simple(r) {
		if err := g.expr(l); err != nil {
			return err
		}
		return g.exprTo(rvFA1, r)
	}
	if g.simple(l) {
		if err := g.expr(r); err != nil {
			return err
		}
		g.fmvD(rvFA1, rvFA0)
		return g.exprTo(rvFA0, l)
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	t := g.tmp()
	if err := g.expr(l); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, t)
	if err := g.expr(r); err != nil {
		return err
	}
	g.fmvD(rvFA1, rvFA0)
	g.fldx(rvFA0, rvS0, t)
	return nil
}

func (g *rvGen) valOp1(op int, a Expression) error {
	if err := g.expr(a); err != nil {
		return err
	}
	g.numCall(op)
	return nil
}

func (g *rvGen) valOp2(op int, a, b Expression) error {
	if err := g.operands(a, b); err != nil {
		return err
	}
	g.numCall(op)
	return nil
}

func (g *rvGen) binary(e *BinaryExpr) error {
	switch e.Operator {
	case "and", "or":
		short, end := g.newLabel(), g.newLabel()
		if err := g.truthy(e.Left); err != nil {
			return err
		}
		if e.Operator == "and" {
			g.beqz(rvA0, short)
		} else {
			g.bnez(rvA0, short)
		}
		if err := g.truthy(e.Right); err != nil {
			return err
		}
		g.fcvtDL(rvFA0, rvA0)
		g.j(end)
		g.bind(short)
		g.number(map[bool]float64{true: 1}[e.Operator == "or"])
		g.bind(end)
		return nil
	case "xor":
		mark := g.sc.ntmp
		defer func() { g.sc.ntmp = mark }()
		t := g.tmp()
		if err := g.truthy(e.Left); err != nil {
			return err
		}
		g.sdx(rvA0, rvS0, t)
		if err := g.truthy(e.Right); err != nil {
			return err
		}
		g.ldx(rvT0, rvS0, t)
		g.op(0, 4, rvA0, rvA0, rvT0)
		g.fcvtDL(rvFA0, rvA0)
		return nil
	case "or!":
		useDefault, end := g.newLabel(), g.newLabel()
		if err := g.expr(e.Left); err != nil {
			return err
		}
		g.fmvXD(rvT0, rvFA0)
		g.slli(rvT1, rvT0, 1)
		g.beqz(rvT1, useDefault)
		g.srli(rvT0, rvT0, 51)
		g.li(rvT1, 0xFFF)
		g.br(1, rvT0, rvT1, end)
		g.bind(useDefault)
		if err := g.expr(e.Right); err != nil {
			return err
		}
		g.bind(end)
		return nil
	case "::":
		return g.valOp2(valCons, e.Left, e.Right)
	}
	if numBitwiseOps[e.Operator] {
		return g.bitwise(e)
	}
	if _, ok := numBinops[e.Operator]; !ok {
		return fmt.Errorf("riscv64: operator %s is not supported yet", e.Operator)
	}
	if err := g.operands(e.Left, e.Right); err != nil {
		return err
	}
	g.numBinop(e.Operator, !g.isNum(e.Left) || !g.isNum(e.Right))
	return nil
}

func (g *rvGen) numCall(op int) {
	g.li(rvA0, int64(op))
	g.call(g.l["num"])
}

// fixnumCheck branches to slow unless freg holds a non-NaN value with |x| < 2^53.
func (g *rvGen) fixnumCheck(freg uint32, slow *rvLabel) {
	g.fmvXD(rvT0, freg)
	g.slli(rvT0, rvT0, 1)
	g.srli(rvT0, rvT0, 53)
	g.li(rvT1, 0x434)
	g.br(7, rvT0, rvT1, slow)
}

func (g *rvGen) smallIntCheck(freg uint32, no *rvLabel) {
	g.fixnumCheck(freg, no)
	g.fcvtLD(rvT0, freg, 1)
	g.fcvtDL(rvFT2, rvT0)
	g.feq(rvT1, rvFT2, freg)
	g.beqz(rvT1, no)
}

// operandsNotBoxed branches to slow when fa0 or fa1 holds a container.
func (g *rvGen) operandsNotBoxed(slow *rvLabel) {
	g.fmvXD(rvT0, rvFA0)
	g.addi(rvT0, rvT0, -1)
	g.srli(rvT0, rvT0, 52)
	g.beqz(rvT0, slow)
	g.fmvXD(rvT0, rvFA1)
	g.addi(rvT0, rvT0, -1)
	g.srli(rvT0, rvT0, 52)
	g.beqz(rvT0, slow)
}

// numBinop computes fa0 = fa0 op fa1 with exact semantics. When boxed may be
// set, +, *, == and != also accept strings, lists and maps.
func (g *rvGen) numBinop(op string, boxed bool) {
	slow, done := g.newLabel(), g.newLabel()
	slowOp := numBinops[op]
	switch op {
	case "+", "-", "*":
		if boxed && op != "-" {
			g.operandsNotBoxed(slow)
			slowOp = map[string]int{"+": valAdd, "*": valRepeat}[op]
		}
		g.fop(map[string]uint32{"+": 0x01, "-": 0x05, "*": 0x09}[op], 7, rvFT0, rvFA0, rvFA1)
		g.fixnumCheck(rvFT0, slow)
		g.fmvD(rvFA0, rvFT0)
		g.j(done)
	case "/":
		notInt, inexact := g.newLabel(), g.newLabel()
		g.fop(0x0D, 7, rvFT0, rvFA0, rvFA1)
		g.smallIntCheck(rvFT0, notInt)
		g.j(inexact)
		g.bind(notInt)
		g.smallIntCheck(rvFA0, inexact)
		g.smallIntCheck(rvFA1, inexact)
		g.j(slow)
		g.bind(inexact)
		g.fmvD(rvFA0, rvFT0)
		g.j(done)
	case "%", "mod":
		g.smallIntCheck(rvFA0, slow)
		g.smallIntCheck(rvFA1, slow)
		g.fcvtLD(rvT0, rvFA0, 1)
		g.fcvtLD(rvT1, rvFA1, 1)
		g.beqz(rvT1, slow)
		g.op(1, 6, rvT0, rvT0, rvT1)
		g.fcvtDL(rvFA0, rvT0)
		g.j(done)
	case "<", "<=", ">", ">=", "==", "!=":
		if boxed && (op == "==" || op == "!=") {
			g.operandsNotBoxed(slow)
			slowOp = map[string]int{"==": valEq, "!=": valNe}[op]
		} else if boxed {
			order := g.newLabel()
			defer func() {
				end := g.newLabel()
				g.j(end)
				g.bind(order)
				g.numCall(valCmp)
				g.fcvtLD(rvT0, rvFA0, 1)
				switch op {
				case "<":
					g.op(0, 2, rvT1, rvT0, 0)
				case "<=":
					g.op(0, 2, rvT1, 0, rvT0)
					g.xori(rvT1, rvT1, 1)
				case ">":
					g.op(0, 2, rvT1, 0, rvT0)
				case ">=":
					g.op(0, 2, rvT1, rvT0, 0)
					g.xori(rvT1, rvT1, 1)
				}
				g.fcvtDL(rvFA0, rvT1)
				g.bind(end)
			}()
			g.operandsNotBoxed(order)
		}
		g.feq(rvT0, rvFA0, rvFA0)
		g.beqz(rvT0, slow)
		g.feq(rvT0, rvFA1, rvFA1)
		g.beqz(rvT0, slow)
		switch op {
		case "<":
			g.fop(0x51, 1, rvT0, rvFA0, rvFA1)
		case "<=":
			g.fop(0x51, 0, rvT0, rvFA0, rvFA1)
		case ">":
			g.fop(0x51, 1, rvT0, rvFA1, rvFA0)
		case ">=":
			g.fop(0x51, 0, rvT0, rvFA1, rvFA0)
		default:
			g.feq(rvT0, rvFA0, rvFA1)
			if op == "!=" {
				g.xori(rvT0, rvT0, 1)
			}
		}
		g.fcvtDL(rvFA0, rvT0)
		g.j(done)
	}
	g.bind(slow)
	g.numCall(slowOp)
	g.bind(done)
}

func (g *rvGen) unary(op string, operand Expression) error {
	switch op {
	case "-":
		if err := g.expr(operand); err != nil {
			return err
		}
		g.fmvD(rvFA1, rvFA0)
		g.fmvDX(rvFA0, 0)
		g.numBinop("-", false)
	case "not", "!":
		if err := g.truthy(operand); err != nil {
			return err
		}
		g.sltiu(rvA0, rvA0, 1)
		g.fcvtDL(rvFA0, rvA0)
	case "#":
		if err := g.expr(operand); err != nil {
			return err
		}
		one, done := g.newLabel(), g.newLabel()
		g.fmvXD(rvT2, rvFA0)
		g.seqTest(rvT2, rvT1, one)
		g.fld(rvFA0, rvT2, 8)
		g.j(done)
		g.bind(one)
		g.number(1)
		g.bind(done)
	case "~b":
		if err := g.expr(operand); err != nil {
			return err
		}
		g.toI64()
		g.xori(rvA0, rvA0, -1)
		g.fromI64()
	default:
		return fmt.Errorf("riscv64: unary %s is not supported yet", op)
	}
	return nil
}

func (g *rvGen) bitwise(e *BinaryExpr) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	t := g.tmp()
	if err := g.expr(e.Left); err != nil {
		return err
	}
	g.toI64()
	g.sdx(rvA0, rvS0, t)
	if err := g.expr(e.Right); err != nil {
		return err
	}
	g.toI64()
	g.mv(rvA1, rvA0)
	g.ldx(rvA0, rvS0, t)
	switch e.Operator {
	case "|b":
		g.op(0, 6, rvA0, rvA0, rvA1)
	case "&b":
		g.op(0, 7, rvA0, rvA0, rvA1)
	case "^b":
		g.op(0, 4, rvA0, rvA0, rvA1)
	case "<<", "<<b":
		g.op(0, 1, rvA0, rvA0, rvA1)
	case "?b":
		g.op(0, 5, rvA0, rvA0, rvA1)
		g.andi(rvA0, rvA0, 1)
	case ">>", ">>b":
		g.op(0, 5, rvA0, rvA0, rvA1)
	case "<<<", "<<<b", ">>>", ">>>b":
		g.op(0x20, 0, rvA2, 0, rvA1)
		if strings.HasPrefix(e.Operator, "<<<") {
			g.op(0, 1, rvT0, rvA0, rvA1)
			g.op(0, 5, rvT1, rvA0, rvA2)
		} else {
			g.op(0, 5, rvT0, rvA0, rvA1)
			g.op(0, 1, rvT1, rvA0, rvA2)
		}
		g.op(0, 6, rvA0, rvT0, rvT1)
	default:
		return fmt.Errorf("riscv64: operator %s is not supported yet", e.Operator)
	}
	g.fromI64()
	return nil
}

// truthyFA0 sets a0 to 1 when fa0 is truthy: non-zero numbers, boxed exact
// numbers and containers are; zero and error NaNs are not.
func (g *rvGen) truthyFA0() {
	nan, done := g.newLabel(), g.newLabel()
	g.feq(rvT0, rvFA0, rvFA0)
	g.beqz(rvT0, nan)
	g.fmvDX(rvFT0, 0)
	g.feq(rvA0, rvFA0, rvFT0)
	g.xori(rvA0, rvA0, 1)
	g.j(done)
	g.bind(nan)
	g.fmvXD(rvA0, rvFA0)
	g.srli(rvA0, rvA0, 63)
	g.bind(done)
}

func (g *rvGen) truthy(e Expression) error {
	if err := g.expr(e); err != nil {
		return err
	}
	g.truthyFA0()
	return nil
}

// toI64 truncates fa0 to a signed 64-bit integer in a0.
func (g *rvGen) toI64() {
	inline, done := g.newLabel(), g.newLabel()
	g.feq(rvT0, rvFA0, rvFA0)
	g.bnez(rvT0, inline)
	g.numCall(numOpToI64)
	g.fmvXD(rvA0, rvFA0)
	g.j(done)
	g.bind(inline)
	g.fcvtLD(rvA0, rvFA0, 1)
	g.bind(done)
}

// fromI64 converts the signed integer in a0 to a number in fa0.
func (g *rvGen) fromI64() {
	slow, done := g.newLabel(), g.newLabel()
	g.li(rvT0, numFixMax-1)
	g.add(rvT0, rvT0, rvA0)
	g.li(rvT1, 2*numFixMax-2)
	g.br(6, rvT1, rvT0, slow)
	g.fcvtDL(rvFA0, rvA0)
	g.j(done)
	g.bind(slow)
	g.fmvDX(rvFA0, rvA0)
	g.numCall(numOpFromI64)
	g.bind(done)
}

func (g *rvGen) toFloat() {
	done := g.newLabel()
	g.feq(rvT0, rvFA0, rvFA0)
	g.bnez(rvT0, done)
	g.numCall(numOpFloat)
	g.bind(done)
}

func (g *rvGen) fstring(e *FStringExpr) error {
	if len(e.Parts) == 0 {
		g.loadStr(rvFA0, "")
		return nil
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	acc := g.tmp()
	for i, p := range e.Parts {
		if s, ok := p.(*StringExpr); ok {
			g.loadStr(rvFA0, s.Value)
		} else {
			if err := g.expr(p); err != nil {
				return err
			}
			g.numCall(valStr)
		}
		if i > 0 {
			g.fmvD(rvFA1, rvFA0)
			g.fldx(rvFA0, rvS0, acc)
			g.numCall(valAdd)
		}
		g.fsdx(rvFA0, rvS0, acc)
	}
	return nil
}

func (g *rvGen) cast(e *CastExpr) error {
	if err := g.expr(e.Expr); err != nil {
		return err
	}
	switch e.Type {
	case "string", "str":
		g.numCall(valStr)
	case "float", "float32", "float64", "double", "f32", "f64":
		g.li(rvT0, int64(floatCastBits(e.Type)))
		g.fmvDX(rvFA1, rvT0)
		if e.Wrap {
			g.numCall(numOpFloatWrap)
		} else {
			g.numCall(numOpFloatCast)
		}
	case "int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64",
		"int", "uint", "long", "ulong", "short", "ushort", "char", "uchar", "size_t", "ssize_t", "ptrdiff_t":
		kind, _ := intCastKind(e.Type)
		g.li(rvT0, int64(kind))
		g.fmvDX(rvFA1, rvT0)
		if e.Wrap {
			g.numCall(numOpIntWrap)
		} else {
			g.numCall(numOpIntCast)
		}
	case "bool", "boolean", "cbool":
		g.truthyFA0()
		g.fcvtDL(rvFA0, rvA0)
	case "cstr", "cstring":
		g.numCall(valCStr)
	}
	return nil
}

// Containers.

func (g *rvGen) list(e *ListExpr) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	n := len(e.Elements)
	if n == 0 {
		g.la(rvT0, g.constData(binary.LittleEndian.AppendUint64(
			binary.LittleEndian.AppendUint64(nil, math.Float64bits(tagList)), 0)))
		g.fmvDX(rvFA0, rvT0)
		return nil
	}
	p := g.tmp()
	g.li(rvA1, int64(16+16*n))
	g.call(g.l["alloc"])
	g.sdx(rvA0, rvS0, p)
	g.li(rvT0, int64(math.Float64bits(tagList)))
	g.sd(rvT0, rvA0, 0)
	g.li(rvT0, int64(math.Float64bits(float64(n))))
	g.sd(rvT0, rvA0, 8)
	for i, el := range e.Elements {
		if err := g.expr(el); err != nil {
			return err
		}
		g.ldx(rvT0, rvS0, p)
		if off := int64(16 + 16*i); off < 2032 {
			g.addi(rvT0, rvT0, int32(off))
		} else {
			g.li(rvT1, off)
			g.add(rvT0, rvT0, rvT1)
		}
		g.li(rvT1, int64(math.Float64bits(float64(i))))
		g.sd(rvT1, rvT0, 0)
		g.fsd(rvFA0, rvT0, 8)
	}
	g.ldx(rvT0, rvS0, p)
	g.fmvDX(rvFA0, rvT0)
	return nil
}

// mapLit writes literal maps with constant keys directly; maps with runtime
// keys are built by inserting into an empty map.
func (g *rvGen) mapLit(e *MapExpr) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	n := len(e.Keys)
	static := true
	for _, k := range e.Keys {
		if num, ok := k.(*NumberExpr); !ok || num.Exact != nil {
			static = false
		}
	}
	p := g.tmp()
	if !static || n == 0 {
		g.la(rvT0, g.constData(binary.LittleEndian.AppendUint64(
			binary.LittleEndian.AppendUint64(nil, math.Float64bits(tagMap)), 0)))
		g.sdx(rvT0, rvS0, p)
		for i := range e.Keys {
			var key Expression = e.Keys[i]
			if i < len(e.KeyNames) && e.KeyNames[i] != "" {
				key = &StringExpr{Value: e.KeyNames[i]}
			}
			if err := g.setEntry(p, key, e.Values[i]); err != nil {
				return err
			}
		}
		g.fldx(rvFA0, rvS0, p)
		return nil
	}
	g.li(rvA1, int64(16+24*n))
	g.call(g.l["alloc"])
	g.sdx(rvA0, rvS0, p)
	g.li(rvT0, int64(math.Float64bits(tagMap)))
	g.sd(rvT0, rvA0, 0)
	g.li(rvT0, int64(math.Float64bits(float64(n))))
	g.sd(rvT0, rvA0, 8)
	slot := func(off int) {
		g.ldx(rvT0, rvS0, p)
		g.li(rvT1, int64(off))
		g.add(rvT0, rvT0, rvT1)
	}
	for i := range e.Keys {
		if err := g.expr(e.Values[i]); err != nil {
			return err
		}
		slot(16 + 16*i)
		g.li(rvT1, int64(math.Float64bits(e.Keys[i].(*NumberExpr).Value)))
		g.sd(rvT1, rvT0, 0)
		g.fsd(rvFA0, rvT0, 8)
		slot(16 + 16*n + 8*i)
		if i < len(e.KeyNames) && e.KeyNames[i] != "" {
			g.la(rvT1, g.str(e.KeyNames[i]))
			g.sd(rvT1, rvT0, 0)
		} else {
			g.sd(0, rvT0, 0)
		}
	}
	g.fldx(rvFA0, rvS0, p)
	return nil
}

// setEntry runs container[key] <- value for the container held in slot p,
// storing the (possibly reallocated) container back into p.
func (g *rvGen) setEntry(p int32, key, value Expression) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	v := g.tmp()
	k := g.tmp()
	if err := g.expr(key); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, k)
	if err := g.expr(value); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, v)
	g.fldx(rvFA0, rvS0, p)
	g.slotAddr(rvT0, k)
	g.fmvDX(rvFA1, rvT0)
	g.numCall(valSet)
	g.fsdx(rvFA0, rvS0, p)
	return nil
}

func (g *rvGen) slotAddr(rd uint32, off int32) {
	if rvFits12(off) {
		g.addi(rd, rvS0, off)
		return
	}
	g.li(rd, int64(off))
	g.add(rd, rd, rvS0)
}

// listElem leaves the address of entry fa1 of the string or list in fa0 in
// t0, or branches to slow (maps, other values, non-integral or out-of-range
// indices).
func (g *rvGen) listElem(slow *rvLabel, listOnly bool) {
	g.fmvXD(rvT2, rvFA0)
	g.seqTest(rvT2, rvT0, slow)
	g.ld(rvT0, rvT2, 0)
	if listOnly {
		g.li(rvT1, int64(math.Float64bits(tagList)))
		g.br(1, rvT0, rvT1, slow)
	} else {
		g.li(rvT1, int64(math.Float64bits(tagMap)))
		g.br(0, rvT0, rvT1, slow)
	}
	g.fcvtLD(rvT0, rvFA1, 1)
	g.fcvtDL(rvFT0, rvT0)
	g.feq(rvT1, rvFT0, rvFA1)
	g.beqz(rvT1, slow)
	g.fld(rvFT0, rvT2, 8)
	g.fcvtLD(rvT1, rvFT0, 1)
	g.br(7, rvT0, rvT1, slow)
	g.slli(rvT0, rvT0, 4)
	g.add(rvT0, rvT0, rvT2)
}

func (g *rvGen) index(e *IndexExpr) error {
	if err := g.operands(e.List, e.Index); err != nil {
		return err
	}
	slow, done := g.newLabel(), g.newLabel()
	g.listElem(slow, false)
	g.fld(rvFA0, rvT0, 24)
	g.j(done)
	g.bind(slow)
	g.numCall(valIndex)
	g.bind(done)
	return nil
}

func (g *rvGen) update(s *MapUpdateStmt) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	var key Expression = s.Index
	if s.KeyName != "" {
		key = &StringExpr{Value: s.KeyName}
	}
	v := g.tmp()
	k := g.tmp()
	if err := g.expr(key); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, k)
	if err := g.expr(s.Value); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, v)
	if err := g.loadVar(rvFA0, s.MapName); err != nil {
		return err
	}
	g.fldx(rvFA1, rvS0, k)
	slow, done := g.newLabel(), g.newLabel()
	g.listElem(slow, true)
	g.fldx(rvFT0, rvS0, v)
	g.fsd(rvFT0, rvT0, 24)
	g.j(done)
	g.bind(slow)
	g.slotAddr(rvT0, k)
	g.fmvDX(rvFA1, rvT0)
	g.numCall(valSet)
	if err := g.storeVar(rvFA0, s.MapName); err != nil {
		return err
	}
	g.bind(done)
	return nil
}

// appendOwned appends in place while the variable owns a buffer with room.
// Owned buffers come from valGrow, whose capacity is the next power of two
// (at least 4) of the count, so a full buffer is one whose count is such a
// power.
func (g *rvGen) appendOwned(args []Expression) error {
	list, owner := args[0].(*IdentExpr), args[1].(*IdentExpr)
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	v := g.tmp()
	for _, val := range args[2:] {
		if err := g.expr(val); err != nil {
			return err
		}
		g.fsdx(rvFA0, rvS0, v)
		if err := g.loadVar(rvFA0, list.Name); err != nil {
			return err
		}
		if err := g.loadVar(rvFA1, owner.Name); err != nil {
			return err
		}
		slow, room, done := g.newLabel(), g.newLabel(), g.newLabel()
		g.fmvXD(rvT0, rvFA0)
		g.fmvXD(rvT1, rvFA1)
		g.br(1, rvT0, rvT1, slow)
		g.beqz(rvT0, slow)
		g.fld(rvFT0, rvT0, 8)
		g.fcvtLD(rvT2, rvFT0, 1)
		g.li(rvT3, 4)
		g.br(4, rvT2, rvT3, room)
		g.addi(rvT3, rvT2, -1)
		g.op(0, 7, rvT3, rvT3, rvT2)
		g.beqz(rvT3, slow)
		g.bind(room)
		g.slli(rvT3, rvT2, 4)
		g.add(rvT3, rvT3, rvT0)
		g.fcvtDL(rvFT1, rvT2)
		g.fsd(rvFT1, rvT3, 16)
		g.fldx(rvFT2, rvS0, v)
		g.fsd(rvFT2, rvT3, 24)
		g.addi(rvT2, rvT2, 1)
		g.fcvtDL(rvFT1, rvT2)
		g.fsd(rvFT1, rvT0, 8)
		g.j(done)
		g.bind(slow)
		g.fldx(rvFA1, rvS0, v)
		g.numCall(valGrow)
		if err := g.storeVar(rvFA0, list.Name); err != nil {
			return err
		}
		if err := g.storeVar(rvFA0, owner.Name); err != nil {
			return err
		}
		g.bind(done)
	}
	return g.loadVar(rvFA0, list.Name)
}

func (g *rvGen) slice(e *SliceExpr) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	l := g.tmp()
	g.tmps(2)
	args := g.tmp()
	if err := g.expr(e.List); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, l)
	for i, part := range []Expression{e.Start, e.End, e.Step} {
		if part == nil {
			g.li(rvT0, valAbsent)
			g.sdx(rvT0, rvS0, args+int32(8*i))
			continue
		}
		if err := g.expr(part); err != nil {
			return err
		}
		g.fsdx(rvFA0, rvS0, args+int32(8*i))
	}
	g.fldx(rvFA0, rvS0, l)
	g.slotAddr(rvT0, args)
	g.fmvDX(rvFA1, rvT0)
	g.numCall(valSlice)
	return nil
}

func (g *rvGen) rangeValue(e *RangeExpr) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	s := g.tmp()
	g.tmps(2)
	args := g.tmp()
	if err := g.expr(e.Start); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, s)
	if err := g.expr(e.End); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, args)
	step := Expression(&NumberExpr{Value: 1})
	if e.Step != nil {
		step = e.Step
	}
	if err := g.expr(step); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, args+8)
	g.li(rvT0, map[bool]int64{true: 1}[e.Inclusive])
	g.sdx(rvT0, rvS0, args+16)
	g.fldx(rvFA0, rvS0, s)
	g.slotAddr(rvT0, args)
	g.fmvDX(rvFA1, rvT0)
	g.numCall(valRange)
	return nil
}

func (g *rvGen) field(e *FieldAccessExpr) error {
	if f := g.structField(e); f != nil {
		return g.fieldLoad(e, f)
	}
	switch e.FieldName {
	case "error":
		return g.valOp1(valErrStr, e.Object)
	case "runes":
		return g.valOp1(valRunes, e.Object)
	case "bytes":
		return g.valOp1(valBytes, e.Object)
	case "length":
		return g.unary("#", e.Object)
	}
	if err := g.expr(e.Object); err != nil {
		return err
	}
	g.number1(rvFA1, float64(hashStringKey(e.FieldName)))
	g.numCall(valIndex)
	return nil
}

// Calls.

func (g *rvGen) callExpr(c *CallExpr) error {
	if sym, sig, ok := g.cFunction(c); ok {
		return g.cCall(sym, sig, c.Args)
	}
	name := c.Function
	if name == "me" && g.sc.fn != nil {
		name = g.sc.fn.name
		if g.sc.fn.direct {
			return g.userCall(g.sc.fn, c)
		}
		if g.sc.tailExpr == Expression(c) {
			return g.selfTailCall(c.Args)
		}
		return g.callValue(nil, c.Args, c)
	}
	if g.isVar(name) {
		if g.sc.fn != nil && !g.sc.fn.direct && name == g.sc.fn.name && g.sc.tailExpr == Expression(c) {
			if _, local := g.sc.vars[name]; !local {
				return g.selfTailCall(c.Args)
			}
		}
		return g.callValue(&IdentExpr{Name: name}, c.Args, c)
	}
	if f := g.funcs[name]; f != nil {
		return g.userCall(f, c)
	}
	if d := g.cstruct(name); d != nil {
		return g.construct(d, c.Args)
	}
	if recv, method, ok := strings.Cut(name, "."); ok && g.isVar(recv) {
		args := append([]Expression{&IdentExpr{Name: recv}}, c.Args...)
		if t := g.structOf(&IdentExpr{Name: recv}); t != "" && g.funcs[t+"_"+method] != nil {
			method = t + "_" + method
		}
		return g.callExpr(&CallExpr{Function: method, Args: args})
	}
	return g.builtin(c)
}

func (g *rvGen) evalArgs(args []Expression) ([]int32, error) {
	if len(args) > rvMaxArgs {
		return nil, fmt.Errorf("riscv64: calls take at most %d arguments", rvMaxArgs)
	}
	slots := make([]int32, len(args))
	for i, a := range args {
		slots[i] = g.tmp()
		if err := g.expr(a); err != nil {
			return nil, err
		}
		g.fsdx(rvFA0, rvS0, slots[i])
	}
	return slots, nil
}

func (g *rvGen) selfTailCall(args []Expression) error {
	f := g.sc.fn
	if len(args) != len(f.params) {
		return fmt.Errorf("riscv64: %s takes %d arguments, got %d", f.name, len(f.params), len(args))
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	slots, err := g.evalArgs(args)
	if err != nil {
		return err
	}
	for i, p := range f.params {
		g.fldx(rvFT0, rvS0, slots[i])
		if err := g.storeVar(rvFT0, p); err != nil {
			return err
		}
	}
	g.j(g.sc.bodyStart)
	return nil
}

func (g *rvGen) userCall(f *rvFunc, c *CallExpr) error {
	if len(c.Args) != len(f.params) {
		return fmt.Errorf("riscv64: %s takes %d arguments, got %d", f.name, len(f.params), len(c.Args))
	}
	if g.sc.fn == f && g.sc.tailExpr == Expression(c) {
		return g.selfTailCall(c.Args)
	}
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	slots, err := g.evalArgs(c.Args)
	if err != nil {
		return err
	}
	for i := range c.Args {
		g.fldx(uint32(rvFA0+i), rvS0, slots[i])
	}
	g.li(rvA0, 0)
	g.call(f.label)
	return nil
}

// callValue calls a closure value (callee nil: the running closure itself).
// Calling anything that is not a closure yields 0.
func (g *rvGen) callValue(callee Expression, args []Expression, _ Expression) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	fn := g.tmp()
	slots, err := g.evalArgs(args)
	if err != nil {
		return err
	}
	if callee == nil {
		g.ldx(rvT0, rvS0, g.sc.envSlot)
		g.sdx(rvT0, rvS0, fn)
	} else {
		if err := g.expr(callee); err != nil {
			return err
		}
		g.fsdx(rvFA0, rvS0, fn)
	}
	for i := range args {
		g.fldx(uint32(rvFA0+i), rvS0, slots[i])
	}
	g.ldx(rvA0, rvS0, fn)
	g.emitClosureCall()
	return nil
}

// emitClosureCall calls the closure in a0 with its arguments in fa0 and up.
// Calling a value that is not a function gives the value.
func (g *rvGen) emitClosureCall() {
	bad, done := g.newLabel(), g.newLabel()
	g.boxTest(rvA0, rvT0, bad)
	g.ld(rvT0, rvA0, 0)
	g.li(rvT1, int64(math.Float64bits(rvTagFunc)))
	g.br(1, rvT0, rvT1, bad)
	g.ld(rvT0, rvA0, 8)
	g.jalr(rvRA, rvT0, 0)
	g.j(done)
	g.bind(bad)
	g.fmvDX(rvFA0, rvA0)
	g.bind(done)
}

// pipe maps a function over a list, or applies it to any other value.
func (g *rvGen) pipe(left, right Expression) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	fn, v, res, i := g.tmp(), g.tmp(), g.tmp(), g.tmp()
	if err := g.expr(right); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, fn)
	if err := g.expr(left); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, v)
	apply, done := g.newLabel(), g.newLabel()
	g.fmvXD(rvT2, rvFA0)
	g.seqTest(rvT2, rvT0, apply)
	g.ld(rvT0, rvT2, 0)
	g.li(rvT1, int64(math.Float64bits(tagList)))
	g.br(1, rvT0, rvT1, apply)
	g.fld(rvFT0, rvT2, 8)
	g.fcvtLD(rvA1, rvFT0, 1)
	g.slli(rvA1, rvA1, 4)
	g.addi(rvA1, rvA1, 16)
	g.call(g.l["alloc"])
	g.sdx(rvA0, rvS0, res)
	g.ldx(rvT2, rvS0, v)
	g.ld(rvT0, rvT2, 0)
	g.sd(rvT0, rvA0, 0)
	g.ld(rvT0, rvT2, 8)
	g.sd(rvT0, rvA0, 8)
	g.sdx(0, rvS0, i)
	loop := g.newLabel()
	g.bind(loop)
	g.ldx(rvT2, rvS0, v)
	g.ldx(rvT0, rvS0, i)
	g.fld(rvFT0, rvT2, 8)
	g.fcvtLD(rvT1, rvFT0, 1)
	g.br(5, rvT0, rvT1, done)
	g.slli(rvT0, rvT0, 4)
	g.add(rvT0, rvT0, rvT2)
	g.fld(rvFA0, rvT0, 24)
	g.ldx(rvA0, rvS0, fn)
	g.emitClosureCall()
	g.ldx(rvT0, rvS0, i)
	g.ldx(rvT2, rvS0, res)
	g.slli(rvT1, rvT0, 4)
	g.add(rvT1, rvT1, rvT2)
	g.fcvtDL(rvFT0, rvT0)
	g.fsd(rvFT0, rvT1, 16)
	g.fsd(rvFA0, rvT1, 24)
	g.addi(rvT0, rvT0, 1)
	g.sdx(rvT0, rvS0, i)
	g.j(loop)
	g.bind(apply)
	g.fldx(rvFA0, rvS0, v)
	g.ldx(rvA0, rvS0, fn)
	g.emitClosureCall()
	end := g.newLabel()
	g.j(end)
	g.bind(done)
	g.fldx(rvFA0, rvS0, res)
	g.bind(end)
	return nil
}

func (g *rvGen) compose(e *ComposeExpr) error {
	g.nlambda++
	f := &rvFunc{
		name:   fmt.Sprintf("compose%d", g.nlambda),
		params: []string{"\x00x"},
		body: &CallExpr{Function: "\x00f", Args: []Expression{
			&CallExpr{Function: "\x00g", Args: []Expression{&IdentExpr{Name: "\x00x"}}},
		}},
		label:    g.newLabel(),
		captures: []rvCapture{{"\x00f", e.Left}, {"\x00g", e.Right}},
	}
	return g.makeClosure(f)
}

func (g *rvGen) minMax(name string, a, b Expression) error {
	mark := g.sc.ntmp
	defer func() { g.sc.ntmp = mark }()
	ta, tb := g.tmp(), g.tmp()
	if err := g.expr(a); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, ta)
	if err := g.expr(b); err != nil {
		return err
	}
	g.fsdx(rvFA0, rvS0, tb)
	g.fmvD(rvFA1, rvFA0)
	g.fldx(rvFA0, rvS0, ta)
	if name == "min" {
		g.numBinop("<=", false)
	} else {
		g.numBinop(">=", false)
	}
	pickB, done := g.newLabel(), g.newLabel()
	g.fmvXD(rvT0, rvFA0)
	g.beqz(rvT0, pickB)
	g.fldx(rvFA0, rvS0, ta)
	g.j(done)
	g.bind(pickB)
	g.fldx(rvFA0, rvS0, tb)
	g.bind(done)
	return nil
}

func (g *rvGen) builtin(c *CallExpr) error {
	if done, err := g.syscallBuiltin(c); done {
		return err
	}
	name := c.Function
	args := c.Args
	switch name {
	case "println", "print", "eprintln", "eprint", "exitln":
		toStderr := name[0] == 'e'
		if toStderr {
			g.selectOutput(2)
		}
		for i, a := range args {
			if i > 0 {
				g.printRaw(" ")
			}
			if err := g.printValue(a); err != nil {
				return err
			}
		}
		if strings.HasSuffix(name, "ln") {
			g.printRaw("\n")
		}
		if toStderr {
			g.selectOutput(1)
		}
		if name == "exitln" {
			g.exit(1)
		}
		g.number(0)
		return nil
	case "printf", "eprintf", "exitf":
		toStderr := name != "printf"
		if toStderr {
			g.selectOutput(2)
		}
		if err := g.printf(args); err != nil {
			return err
		}
		if toStderr {
			g.selectOutput(1)
		}
		if name == "exitf" {
			g.exit(1)
		}
		g.number(0)
		return nil
	case "exit":
		if len(args) > 0 {
			if err := g.expr(args[0]); err != nil {
				return err
			}
			g.toI64()
		} else {
			g.li(rvA0, 0)
		}
		g.exitWith()
		return nil
	case "str":
		if len(args) == 1 {
			return g.valOp1(valStr, args[0])
		}
	case "append":
		if len(args) >= 1 {
			mark := g.sc.ntmp
			defer func() { g.sc.ntmp = mark }()
			acc := g.tmp()
			if err := g.expr(args[0]); err != nil {
				return err
			}
			for _, a := range args[1:] {
				g.fsdx(rvFA0, rvS0, acc)
				if err := g.expr(a); err != nil {
					return err
				}
				g.fmvD(rvFA1, rvFA0)
				g.fldx(rvFA0, rvS0, acc)
				g.numCall(valAppend)
			}
			return nil
		}
	case "pop", "head", "tail":
		if len(args) == 1 {
			return g.valOp1(map[string]int{"pop": valPop, "head": valHead, "tail": valTail}[name], args[0])
		}
	case "error":
		if len(args) == 1 {
			return g.valOp1(valError, args[0])
		}
	case appendOwnedBuiltin:
		return g.appendOwned(args)
	case "_error_code_extract":
		if len(args) == 1 {
			return g.valOp1(valErrStr, args[0])
		}
	case "is_nan":
		if len(args) == 1 {
			if err := g.expr(args[0]); err != nil {
				return err
			}
			g.fmvXD(rvT0, rvFA0)
			g.srli(rvT0, rvT0, 63)
			g.feq(rvT1, rvFA0, rvFA0)
			g.op(0, 6, rvT0, rvT0, rvT1)
			g.xori(rvT0, rvT0, 1)
			g.fcvtDL(rvFA0, rvT0)
			return nil
		}
	case "pow":
		if len(args) == 2 {
			return g.expr(&BinaryExpr{Left: args[0], Operator: "**", Right: args[1]})
		}
	case "min", "max":
		if len(args) == 2 {
			return g.minMax(name, args[0], args[1])
		}
	case "sqrt":
		if len(args) == 1 {
			if err := g.expr(args[0]); err != nil {
				return err
			}
			g.toFloat()
			g.fop(0x2D, 7, rvFA0, rvFA0, 0)
			return nil
		}
	case "floor", "ceil", "round", "abs", "trunc":
		if len(args) == 1 {
			op, ok := numRoundBuiltins[name]
			if !ok {
				op = numOpTrunc
			}
			return g.valOp1(op, args[0])
		}
	case "float":
		if len(args) == 1 {
			if err := g.expr(args[0]); err != nil {
				return err
			}
			g.toFloat()
			return nil
		}
	case "atan2":
		if len(args) == 2 {
			return g.valOp2(valAtan2, args[0], args[1])
		}
	case "alloc", "malloc", "calloc":
		if len(args) >= 1 {
			if err := g.expr(args[len(args)-1]); err != nil {
				return err
			}
			if name == "calloc" && len(args) == 2 {
				if err := g.expr(&BinaryExpr{Left: args[0], Operator: "*", Right: args[1]}); err != nil {
					return err
				}
			}
			g.toI64()
			g.mv(rvA1, rvA0)
			g.call(g.l["alloc"])
			g.fcvtDL(rvFA0, rvA0)
			return nil
		}
	case "peek8", "peek32":
		if len(args) == 2 {
			if err := g.memAddr(args[0], args[1], 1); err != nil {
				return err
			}
			g.mv(rvT2, rvA0)
			g.loadField(&CStructField{Type: map[string]string{"peek8": "uint8", "peek32": "uint32"}[name]}, rvT2)
			return nil
		}
	case "free":
		if len(args) == 1 {
			if err := g.expr(args[0]); err != nil {
				return err
			}
			g.number(0)
			return nil
		}
	case "fmod", "hypot", "fmin", "fmax":
		if len(args) == 2 {
			return g.expr(map[string]Expression{
				"fmod":  &BinaryExpr{Left: args[0], Operator: "-", Right: &BinaryExpr{Left: &CallExpr{Function: "trunc", Args: []Expression{&BinaryExpr{Left: args[0], Operator: "/", Right: args[1]}}}, Operator: "*", Right: args[1]}},
				"hypot": &CallExpr{Function: "sqrt", Args: []Expression{&BinaryExpr{Left: &BinaryExpr{Left: args[0], Operator: "*", Right: args[0]}, Operator: "+", Right: &BinaryExpr{Left: args[1], Operator: "*", Right: args[1]}}}},
				"fmin":  &CallExpr{Function: "min", Args: args},
				"fmax":  &CallExpr{Function: "max", Args: args},
			}[name])
		}
	}
	if kind, ok := strings.CutPrefix(name, "read_"); ok && len(args) == 2 {
		if f := rvMemField(kind); f != nil {
			if err := g.memAddr(args[0], args[1], f.Size); err != nil {
				return err
			}
			g.mv(rvT2, rvA0)
			g.loadField(f, rvT2)
			return nil
		}
	}
	if kind, ok := strings.CutPrefix(name, "write_"); ok && len(args) == 3 {
		if f := rvMemField(kind); f != nil {
			mark := g.sc.ntmp
			defer func() { g.sc.ntmp = mark }()
			p := g.tmp()
			if err := g.memAddr(args[0], args[1], f.Size); err != nil {
				return err
			}
			g.sdx(rvA0, rvS0, p)
			if err := g.expr(args[2]); err != nil {
				return err
			}
			g.storeField(f, p)
			g.number(0)
			return nil
		}
	}
	if op, ok := valMathBuiltins[name]; ok && len(args) == 1 {
		return g.valOp1(op, args[0])
	}
	return fmt.Errorf("riscv64: function %s is not supported yet", c.Function)
}

func (g *rvGen) exit(code int64) {
	g.li(rvA0, code)
	g.exitWith()
}

func (g *rvGen) printRaw(s string) {
	if s == "" {
		return
	}
	g.la(rvA0, g.constData([]byte(s)))
	g.li(rvA1, int64(len(s)))
	g.call(g.l["print_raw"])
}

func (g *rvGen) printValue(e Expression) error {
	if s, ok := e.(*StringExpr); ok {
		g.printRaw(s.Value)
		return nil
	}
	if err := g.expr(e); err != nil {
		return err
	}
	g.call(g.l["print_val"])
	return nil
}

func (g *rvGen) printf(args []Expression) error {
	if len(args) == 0 {
		return fmt.Errorf("printf requires a format string")
	}
	f, ok := args[0].(*StringExpr)
	if !ok {
		return fmt.Errorf("printf format must be a string literal")
	}
	format := f.Value
	arg := 1
	var lit strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			lit.WriteByte(format[i])
			continue
		}
		j := i + 1
		for j < len(format) && strings.IndexByte("-+ #0123456789", format[j]) >= 0 {
			j++
		}
		prec := 6
		if j < len(format) && format[j] == '.' {
			k := j + 1
			for k < len(format) && format[k] >= '0' && format[k] <= '9' {
				k++
			}
			if p, err := strconv.Atoi(format[j+1 : k]); err == nil {
				prec = min(p, 15)
			}
			j = k
		}
		for j < len(format) && strings.IndexByte("lhLjzt", format[j]) >= 0 {
			j++
		}
		if j >= len(format) {
			lit.WriteString(format[i:])
			break
		}
		conv := format[j]
		if conv == '%' {
			lit.WriteByte('%')
			i = j
			continue
		}
		if arg >= len(args) {
			return fmt.Errorf("printf: not enough arguments for format string")
		}
		g.printRaw(lit.String())
		lit.Reset()
		a := args[arg]
		arg++
		switch conv {
		case 'v', 's':
			if err := g.printValue(a); err != nil {
				return err
			}
		case 'd', 'i', 'u':
			if err := g.expr(a); err != nil {
				return err
			}
			g.toI64()
			g.call(g.l["print_i64"])
		case 'f', 'F', 'g', 'G', 'e', 'E':
			if err := g.expr(a); err != nil {
				return err
			}
			g.toFloat()
			g.li(rvA0, int64(prec))
			g.call(g.l["print_fixed"])
		case 'p', 'x', 'X':
			if conv == 'p' {
				g.printRaw("0x")
			}
			if err := g.expr(a); err != nil {
				return err
			}
			g.toI64()
			g.li(rvA1, map[bool]int64{true: 'A' - 10, false: 'a' - 10}[conv == 'X'])
			g.call(g.l["print_hex"])
		case 'b', 't':
			if err := g.truthy(a); err != nil {
				return err
			}
			yes, no := "yes", "no"
			if conv == 't' {
				yes, no = "true", "false"
			}
			other, done := g.newLabel(), g.newLabel()
			g.beqz(rvA0, other)
			g.printRaw(yes)
			g.j(done)
			g.bind(other)
			g.printRaw(no)
			g.bind(done)
		case 'c':
			if err := g.expr(a); err != nil {
				return err
			}
			mark := g.sc.ntmp
			t := g.tmp()
			g.fsdx(rvFA0, rvS0, t)
			g.loadStr(rvFA0, "")
			g.fldx(rvFA1, rvS0, t)
			g.numCall(valAppend)
			g.call(g.l["print_val"])
			g.sc.ntmp = mark
		default:
			return fmt.Errorf("riscv64: printf %%%c is not supported yet", conv)
		}
		i = j
	}
	g.printRaw(lit.String())
	return nil
}
