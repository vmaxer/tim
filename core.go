package main

import (
	"fmt"
	"math"
	"math/big"
	"strings"
)

// core.go compiles a checked program to machine code for any instruction set
// through asm. Values are NaN-boxed 64-bit words (see runtime/rt.c); plain
// doubles get inline fast paths and everything else calls the runtime.

const (
	opAdd = 1 + iota
	opSub
	opMul
	opDiv
	opMod
	opPow
	opLt
	opLe
	opGt
	opGe
	opEq
	opNe
	opBor
	opBand
	opBxor
	opShl
	opShr
	opBit
	opRotl
	opRotr
)

var binops = map[string]uint64{
	"+": opAdd, "-": opSub, "*": opMul, "/": opDiv, "%": opMod, "**": opPow,
	"<": opLt, "<=": opLe, ">": opGt, ">=": opGe, "==": opEq, "!=": opNe,
	"|b": opBor, "&b": opBand, "^b": opBxor, "<<b": opShl, ">>b": opShr, "?b": opBit, "<<<b": opRotl, ">>>b": opRotr,
}

const (
	tagBig  = 0xFFF9
	tagRat  = 0xFFFA
	tagStr  = 0xFFFB
	tagFn   = 0xFFFE
	kindBig = 2
	kindRat = 3
	kindStr = 4

	valTrue   = 0x3FF0000000000000
	two53Bits = 0x4340000000000000
	oneBits   = valTrue
)

// coreError is an internal limitation; the driver falls back to the legacy backend.
type coreError struct{ msg string }

func (e coreError) Error() string { return e.msg }

func unsupportedf(format string, args ...any) { panic(coreError{fmt.Sprintf(format, args...)}) }

type loopLabels struct{ cont, exit label }

type frame struct {
	fn       *Fun
	top      bool
	fixed    int // slots before the temporaries
	temps    int
	maxTemps int
	maxOut   int
	variadic int   // slot of the variadic parameter
	defers   int32 // offset of the list of deferred functions, or 0
	loops    []loopLabels
}

type coreGen struct {
	c        *Checked
	a        asm
	syms     map[string]int
	blob     label
	fnLabels map[*Fun]label
	queue    []*Fun
	strs     map[string]label
	consts   []func()
	hoisted  map[Statement]bool
	wrappers map[string]*Var // builtins used as values, cached in globals
	nglobals int
	argBase  int
	f        *frame
}

// coreTarget describes where generated code will run.
type coreTarget struct {
	os   OS
	blob []byte
	syms map[string]int
	// importsAt returns where the loader's import table will be, as an
	// offset from the start of the code, given the code's length.
	importsAt func(codeLen int) int
}

// compileCore generates the whole program and returns the code, starting
// with the entry point.
func compileCore(c *Checked, a asm, t coreTarget) (code []byte, entry int, err error) {
	blob, syms := t.blob, t.syms
	defer func() {
		if r := recover(); r != nil {
			ce, ok := r.(coreError)
			if !ok {
				panic(r)
			}
			err = ce
		}
	}()
	g := &coreGen{c: c, a: a, syms: syms, blob: a.newLabel(), fnLabels: map[*Fun]label{},
		strs: map[string]label{}, hoisted: map[Statement]bool{}, wrappers: map[string]*Var{}, nglobals: len(c.Globals)}
	main, imports := a.newLabel(), a.newLabel()
	entry = a.pos()
	a.start(main, g.blob, g.sym("rt_start"), imports, t.os)
	a.bind(main)
	g.genTop()
	for len(g.queue) > 0 {
		f := g.queue[0]
		g.queue = g.queue[1:]
		g.genFun(f)
	}
	a.align(64)
	a.bind(g.blob)
	a.emit(blob)
	a.align(8)
	for i := 0; i < len(g.consts); i++ {
		g.consts[i]()
	}
	if t.importsAt != nil {
		a.bindAt(imports, t.importsAt(a.pos()))
	} else {
		a.bindAt(imports, 0)
	}
	if err := a.resolve(); err != nil {
		return nil, 0, err
	}
	return a.code(), entry, nil
}

func (g *coreGen) sym(name string) int {
	off, ok := g.syms[name]
	if !ok {
		panic(fmt.Sprintf("runtime has no %s", name))
	}
	return off
}

// Frames and temporaries.

func slotOff(k int) int32 { return int32(-8 * (k + 1)) }

func (g *coreGen) tmp() int32 {
	t := g.f.fixed + g.f.temps
	g.f.temps++
	g.f.maxTemps = max(g.f.maxTemps, g.f.temps)
	return slotOff(t)
}

// tmps reserves n adjacent slots and returns the offset of the lowest, which
// holds element 0 of an array.
func (g *coreGen) tmps(n int) int32 {
	g.f.temps += n
	g.f.maxTemps = max(g.f.maxTemps, g.f.temps)
	return slotOff(g.f.fixed + g.f.temps - 1)
}

func (g *coreGen) free(n int) { g.f.temps -= n }

func (g *coreGen) frameSize() int {
	n := 8*(g.f.fixed+g.f.maxTemps) + 8*g.f.maxOut
	return (n + 15) &^ 15
}

// Runtime calls. Operands are loaded in reverse, so rA (which is the first
// argument register on some machines) is read before it is overwritten.

type operand struct {
	kind uint8 // 0 rA, 1 slot, 2 immediate, 3 slot address, 4 runtime
	off  int32
	v    uint64
}

func inA() operand             { return operand{kind: 0} }
func slot(off int32) operand   { return operand{kind: 1, off: off} }
func immv(v uint64) operand    { return operand{kind: 2, v: v} }
func slotAt(off int32) operand { return operand{kind: 3, off: off} }
func rt() operand              { return operand{kind: 4} }

func (g *coreGen) callRT(name string, args ...operand) {
	for i := len(args) - 1; i >= 0; i-- {
		r := argReg(i)
		switch o := args[i]; o.kind {
		case 0:
			g.a.mov(r, rA)
		case 1:
			g.a.load(r, rFP, o.off)
		case 2:
			g.a.imm(r, o.v)
		case 3:
			g.a.addImm(r, rFP, o.off)
		case 4:
			g.a.mov(r, rRT)
		}
	}
	g.a.callOff(g.blob, g.sym(name))
}

func (g *coreGen) untag(r reg) {
	g.a.shiftImm(aluShl, r, r, 16)
	g.a.shiftImm(aluShr, r, r, 16)
}

func (g *coreGen) boxLabel(dst reg, l label, tag uint64) {
	g.a.addr(dst, l)
	g.a.imm(rD, tag<<48)
	g.a.op(aluOr, dst, dst, rD)
}

// Top level and functions.

func (g *coreGen) genTop() {
	g.f = &frame{fn: g.c.Top, top: true}
	// slots: 0 saved runtime register, 1 saved globals register, then locals
	g.f.fixed = 2 + len(g.c.Top.Locals)
	g.reserveDefers()
	h := g.a.prologue()
	g.a.store(rRT, rFP, slotOff(0))
	g.a.store(rGlob, rFP, slotOff(1))
	g.a.mov(rRT, argReg(0))
	countSlot := g.tmp()
	g.globalCount(countSlot)
	g.callRT("rt_globals", rt(), slot(countSlot))
	g.free(1)
	g.a.mov(rGlob, rA)
	g.initDefers()

	stmts := g.c.Program.Statements
	for _, s := range stmts {
		if as, ok := s.(*AssignStmt); ok && !as.IsUpdate && !as.Mutable {
			if l, ok := as.Value.(*LambdaExpr); ok {
				if f := g.c.Fns[l]; f != nil && len(f.Captures) == 0 {
					g.closure(f)
					g.storeVar(g.c.Defs[s][0])
					g.hoisted[s] = true
				}
			}
		}
	}
	g.a.imm(rA, 0)
	for _, s := range stmts {
		g.stmt(s, false)
	}
	g.a.imm(rA, 0)
	if v := g.mainToRun(); v != nil && v.Func != nil {
		g.callVar(v, nil, false)
	} else if v != nil {
		// main may hold a plain value, which is then the exit code
		g.loadVar(v)
		t := g.tmp()
		g.a.store(rA, rFP, t)
		g.callRT("rt_fn_code", rt(), slot(t), immv(0))
		plain, done := g.a.newLabel(), g.a.newLabel()
		g.a.brZero(rA, plain)
		g.timCall(nil, t, nil, false)
		g.a.jmp(done)
		g.a.bind(plain)
		g.a.load(rA, rFP, t)
		g.a.bind(done)
		g.free(1)
	}
	g.runDefers()
	g.a.load(rRT, rFP, slotOff(0))
	g.a.load(rGlob, rFP, slotOff(1))
	g.a.epilogue()
	g.a.setFrame(h, g.frameSize())
}

// globalCount loads the number of globals, which grows while code is
// generated (builtin wrappers), from a constant written at the end.
func (g *coreGen) globalCount(dst int32) {
	l := g.a.newLabel()
	g.a.addr(rA, l)
	g.a.load(rA, rA, 0)
	g.a.store(rA, rFP, dst)
	g.consts = append(g.consts, func() {
		g.a.bind(l)
		g.a.emit(le64(uint64(g.nglobals)))
	})
}

// mainToRun returns the global main when the program never calls it at top level.
func (g *coreGen) mainToRun() *Var {
	var mainVar *Var
	for _, v := range g.c.Globals {
		if v.Name == "main" {
			mainVar = v
		}
	}
	if mainVar == nil {
		return nil
	}
	called := false
	for _, s := range g.c.Program.Statements {
		walkCalls(s, func(e *CallExpr) {
			if e.Function == "main" {
				called = true
			}
		})
	}
	if called {
		return nil
	}
	return mainVar
}

func (g *coreGen) fnLabel(f *Fun) label {
	if l, ok := g.fnLabels[f]; ok {
		return l
	}
	l := g.a.newLabel()
	g.fnLabels[f] = l
	g.queue = append(g.queue, f)
	return l
}

func (g *coreGen) genFun(f *Fun) {
	g.f = &frame{fn: f, fixed: 1 + len(f.Locals)}
	if f.Variadic != nil {
		g.f.variadic = g.f.fixed
		g.f.fixed++
	}
	g.reserveDefers()
	g.a.bind(g.fnLabels[f])
	h := g.a.prologue()
	g.a.store(rEnv, rFP, slotOff(0))
	g.initDefers()
	if f.Variadic != nil {
		np := len(f.Params)
		g.a.addImm(rA, rArgc, int32(-np))
		g.a.addImm(rB, rFP, int32(16+8*np))
		t := g.tmp()
		g.a.store(rB, rFP, t)
		g.callRT("rt_list", rt(), slot(t), inA())
		g.free(1)
		g.a.store(rA, rFP, slotOff(g.f.variadic))
	}
	if f.Lambda != nil {
		g.expr(f.Lambda.Body, g.f.defers == 0)
	} else {
		g.builtinBody(f)
	}
	g.runDefers()
	g.a.epilogue()
	g.a.setFrame(h, g.frameSize())
}

// Deferred calls: a function with defer statements keeps a list of
// functions of no arguments, run last first when it returns.

func (g *coreGen) reserveDefers() {
	if g.f.fn.Defers {
		g.f.defers = slotOff(g.f.fixed)
		g.f.fixed++
	}
}

func (g *coreGen) initDefers() {
	if g.f.defers != 0 {
		g.callRT("rt_list", rt(), immv(0), immv(0))
		g.a.store(rA, rFP, g.f.defers)
	}
}

// runDefers calls the deferred functions, keeping rA.
func (g *coreGen) runDefers() {
	if g.f.defers == 0 {
		return
	}
	saved, fn := g.tmp(), g.tmp()
	g.a.store(rA, rFP, saved)
	loop, done := g.a.newLabel(), g.a.newLabel()
	g.a.bind(loop)
	g.callRT("rt_len", rt(), slot(g.f.defers))
	g.a.brZero(rA, done)
	g.callRT("rt_pop", rt(), slot(g.f.defers))
	g.a.store(rA, rFP, fn)
	g.timCall(nil, fn, nil, false)
	g.a.jmp(loop)
	g.a.bind(done)
	g.a.load(rA, rFP, saved)
	g.free(2)
}

// Variables.

func root(v *Var) *Var {
	for v.Kind == SymCapture {
		v = v.Outer
	}
	return v
}

// loadRaw loads a variable's slot: for a boxed variable, its cell.
func (g *coreGen) loadRaw(v *Var) {
	switch v.Kind {
	case SymGlobal:
		g.a.load(rA, rGlob, int32(8*v.Index))
	case SymLocal:
		g.a.load(rA, rFP, slotOff(g.localSlot(v)))
	case SymParam:
		if g.f.fn.Variadic == v {
			g.a.load(rA, rFP, slotOff(g.f.variadic))
		} else {
			g.a.load(rA, rFP, int32(16+8*v.Index))
		}
	case SymCapture:
		g.a.load(rA, rFP, slotOff(0))
		g.untag(rA)
		g.a.load(rA, rA, int32(32+8*v.Index))
	case SymBuiltin:
		g.builtinValue(v.Name)
	}
}

func (g *coreGen) localSlot(v *Var) int {
	if g.f.top {
		return 2 + v.Index
	}
	return 1 + v.Index
}

func (g *coreGen) loadVar(v *Var) {
	g.loadRaw(v)
	if v.Kind != SymGlobal && v.Kind != SymBuiltin && root(v).Boxed {
		g.untag(rA)
		g.a.load(rA, rA, 8)
	}
}

// storeVar stores rA into a variable that already exists.
func (g *coreGen) storeVar(v *Var) {
	switch {
	case v.Kind == SymGlobal:
		g.a.store(rA, rGlob, int32(8*v.Index))
	case root(v).Boxed:
		g.a.mov(rC, rA)
		g.loadRaw(v)
		g.untag(rA)
		g.a.store(rC, rA, 8)
		g.a.mov(rA, rC)
	case v.Kind == SymLocal:
		g.a.store(rA, rFP, slotOff(g.localSlot(v)))
	default:
		unsupportedf("cannot store to %s", v.Name)
	}
}

// define binds a new variable to the value computed by value.
func (g *coreGen) define(v *Var, value func()) {
	if v.Kind == SymLocal && v.Boxed {
		g.callRT("rt_cell", rt(), immv(0))
		g.a.store(rA, rFP, slotOff(g.localSlot(v)))
	}
	value()
	g.storeVar(v)
}

// Statements. When value is set, rA holds the value of the statement.

func (g *coreGen) stmts(ss []Statement, tail bool) {
	if len(ss) == 0 {
		g.a.imm(rA, 0)
		return
	}
	for i, s := range ss {
		last := i == len(ss)-1
		g.stmt(s, tail && last)
		if last {
			g.stmtValue(s)
		}
	}
}

// stmtValue leaves the value of a statement just generated in rA:
// expressions, ifs and bindings have values, other statements are 0.
func (g *coreGen) stmtValue(s Statement) {
	switch s := s.(type) {
	case *ExpressionStmt, *IfStmt:
	case *AssignStmt:
		if g.hoisted[s] {
			g.loadVar(g.c.Defs[s][0])
		}
	default:
		g.a.imm(rA, 0)
	}
}

func (g *coreGen) stmt(s Statement, tail bool) {
	switch s := s.(type) {
	case *ExpressionStmt:
		g.expr(s.Expr, tail)
	case *AssignStmt:
		if g.hoisted[s] {
			return
		}
		if v := g.c.Updates[s]; v != nil {
			if g.appendInPlace(s, v) {
				return
			}
			g.expr(s.Value, false)
			g.storeVar(v)
			return
		}
		g.define(g.c.Defs[s][0], func() { g.expr(s.Value, false) })
	case *MultipleAssignStmt:
		g.expr(s.Value, false)
		t := g.tmp()
		g.a.store(rA, rFP, t)
		for i := range s.Names {
			get := func() { g.callRT("rt_index", rt(), slot(t), immv(math.Float64bits(float64(i)))) }
			if s.IsUpdate {
				get()
				g.storeVar(g.c.Defs[s][i])
			} else {
				g.define(g.c.Defs[s][i], get)
			}
		}
		g.free(1)
	case *MapUpdateStmt:
		v := g.c.Updates[s]
		g.loadVar(v)
		g.setIndex(func() {}, s.Index, s.Value)
	case *IndexUpdateStmt:
		g.expr(s.Target, false)
		g.setIndex(func() {}, s.Index, s.Value)
	case *FieldUpdateStmt:
		g.expr(s.Object, false)
		g.setIndex(func() {}, &StringExpr{Value: s.Field}, s.Value)
	case *LoopStmt:
		g.loop(s)
	case *WhileStmt:
		g.while(s)
	case *IfStmt:
		end := g.a.newLabel()
		for _, b := range s.Branches {
			next := g.a.newLabel()
			g.jumpIf(b.Condition, false, next)
			g.stmts(b.Body, tail)
			g.a.jmp(end)
			g.a.bind(next)
		}
		g.stmts(s.ElseBody, tail)
		g.a.bind(end)
	case *JumpStmt:
		g.jump(s.IsBreak, s.Label, s.Value)
	case *ArenaStmt:
		g.stmts(s.Body, false)
	case *DeferStmt:
		g.closure(g.c.Deferred[s])
		t := g.tmp()
		g.a.store(rA, rFP, t)
		g.callRT("rt_push", rt(), slot(g.f.defers), slot(t))
		g.free(1)
	case *ExportStmt:
	default:
		unsupportedf("statement %T", s)
	}
}

// appendInPlace compiles the `acc <- acc + [x]` of a list comprehension as a push.
func (g *coreGen) appendInPlace(s *AssignStmt, v *Var) bool {
	b, ok := s.Value.(*BinaryExpr)
	if !ok || b.Operator != "+" || !strings.HasPrefix(s.Name, "tmp·") {
		return false
	}
	id, ok := b.Left.(*IdentExpr)
	l, ok2 := b.Right.(*ListExpr)
	if !ok || !ok2 || id.Name != s.Name || len(l.Elements) != 1 {
		return false
	}
	g.expr(l.Elements[0], false)
	t := g.tmp()
	g.a.store(rA, rFP, t)
	g.loadVar(v)
	g.callRT("rt_push", rt(), inA(), slot(t))
	g.free(1)
	return true
}

// setIndex stores value at index of the collection in rA.
func (g *coreGen) setIndex(_ func(), index, value Expression) {
	t := g.tmps(2)
	g.a.store(rA, rFP, t)
	g.expr(index, false)
	g.a.store(rA, rFP, t+8)
	g.expr(value, false)
	g.callRT("rt_setindex", rt(), slot(t), slot(t+8), inA())
	g.free(2)
}

func (g *coreGen) jump(isBreak bool, lbl int, value Expression) {
	if isBreak && lbl == 0 {
		if value != nil {
			g.expr(value, !g.f.top && g.f.defers == 0)
		} else {
			g.a.imm(rA, 0)
		}
		g.runDefers()
		if g.f.top {
			g.callRT("rt_exit", rt(), inA())
			return
		}
		g.a.epilogue()
		return
	}
	loops := g.f.loops
	if len(loops) == 0 {
		unsupportedf("break outside a loop")
	}
	target := loops[len(loops)-1]
	if lbl > 0 {
		target = loops[lbl-1]
	}
	if isBreak {
		g.a.jmp(target.exit)
	} else {
		g.a.jmp(target.cont)
	}
}

func (g *coreGen) bounded(max int64, exit label) func() {
	if max <= 0 || max == math.MaxInt64 {
		return func() {}
	}
	t := g.tmp()
	g.a.imm(rA, 0)
	g.a.store(rA, rFP, t)
	return func() {
		g.a.load(rA, rFP, t)
		g.a.addImm(rA, rA, 1)
		g.a.store(rA, rFP, t)
		g.a.imm(rB, uint64(max))
		g.a.br(cGtU, rA, rB, exit)
	}
}

func (g *coreGen) loop(s *LoopStmt) {
	it := g.c.Defs[s][0]
	top, cont, exit := g.a.newLabel(), g.a.newLabel(), g.a.newLabel()
	saved := g.f.temps
	if r, ok := s.Iterable.(*RangeExpr); ok {
		ts := g.tmps(2) // ts: counter, ts+8: end
		for i, e := range []Expression{r.Start, r.End} {
			g.expr(e, false)
			plain := g.a.newLabel()
			g.a.brTagged(rA, plain)
			done := g.a.newLabel()
			g.a.jmp(done)
			g.a.bind(plain)
			g.callRT("rt_bound", rt(), inA())
			g.a.bind(done)
			g.a.store(rA, rFP, ts+int32(8*i))
		}
		check := g.bounded(s.MaxIterations, exit)
		g.a.bind(top)
		g.a.load(rA, rFP, ts)
		g.a.load(rB, rFP, ts+8)
		body := g.a.newLabel()
		c := fLt
		if r.Inclusive {
			c = fLe
		}
		g.a.fbr(c, rA, rB, body)
		g.a.jmp(exit)
		g.a.bind(body)
		check()
		g.a.load(rA, rFP, ts)
		g.define(it, func() {})
		g.f.loops = append(g.f.loops, loopLabels{cont, exit})
		g.stmts(s.Body, false)
		g.f.loops = g.f.loops[:len(g.f.loops)-1]
		g.a.bind(cont)
		g.a.load(rA, rFP, ts)
		g.a.imm(rB, oneBits)
		g.a.fop(fAdd, rA, rA, rB)
		g.a.store(rA, rFP, ts)
		g.a.jmp(top)
	} else {
		ts := g.tmps(2) // ts: list, ts+8: index
		g.expr(s.Iterable, false)
		g.callRT("rt_iter", rt(), inA())
		g.a.store(rA, rFP, ts)
		g.a.imm(rA, 0)
		g.a.store(rA, rFP, ts+8)
		check := g.bounded(s.MaxIterations, exit)
		g.a.bind(top)
		g.a.load(rB, rFP, ts)
		g.untag(rB)
		g.a.load(rC, rB, 8)
		g.a.load(rD, rFP, ts+8)
		g.a.br(cGeU, rD, rC, exit)
		check()
		g.a.load(rB, rFP, ts)
		g.untag(rB)
		g.a.load(rB, rB, 24)
		g.a.load(rD, rFP, ts+8)
		g.a.shiftImm(aluShl, rD, rD, 3)
		g.a.op(aluAdd, rB, rB, rD)
		g.a.load(rA, rB, 8)
		g.define(it, func() {})
		g.f.loops = append(g.f.loops, loopLabels{cont, exit})
		g.stmts(s.Body, false)
		g.f.loops = g.f.loops[:len(g.f.loops)-1]
		g.a.bind(cont)
		g.a.load(rA, rFP, ts+8)
		g.a.addImm(rA, rA, 1)
		g.a.store(rA, rFP, ts+8)
		g.a.jmp(top)
	}
	g.a.bind(exit)
	g.f.temps = saved
	g.a.imm(rA, 0)
}

func (g *coreGen) while(s *WhileStmt) {
	top, exit := g.a.newLabel(), g.a.newLabel()
	saved := g.f.temps
	check := g.bounded(s.MaxIterations, exit)
	g.a.bind(top)
	g.jumpIf(s.Condition, false, exit)
	check()
	g.f.loops = append(g.f.loops, loopLabels{top, exit})
	g.stmts(s.Body, false)
	g.f.loops = g.f.loops[:len(g.f.loops)-1]
	g.a.jmp(top)
	g.a.bind(exit)
	g.f.temps = saved
	g.a.imm(rA, 0)
}

// Expressions leave their value in rA.

func (g *coreGen) expr(e Expression, tail bool) {
	switch e := e.(type) {
	case nil:
		g.a.imm(rA, 0)
	case *NumberExpr:
		g.number(e)
	case *BooleanExpr:
		if e.Value {
			g.a.imm(rA, valTrue)
		} else {
			g.a.imm(rA, 0)
		}
	case *StringExpr:
		g.boxLabel(rA, g.str(e.Value), tagStr)
	case *FStringExpr:
		g.array(e.Parts, func(base int32, n int) { g.callRT("rt_concat", rt(), slotAt(base), immv(uint64(n))) })
	case *ListExpr:
		g.array(e.Elements, func(base int32, n int) { g.callRT("rt_list", rt(), slotAt(base), immv(uint64(n))) })
	case *MapExpr:
		var kv []Expression
		for i := range e.Keys {
			k := e.Keys[i]
			if i < len(e.Names) && e.Names[i] != "" {
				k = &StringExpr{Value: e.Names[i]}
			}
			kv = append(kv, k, e.Values[i])
		}
		g.array(kv, func(base int32, n int) { g.callRT("rt_map", rt(), slotAt(base), immv(uint64(n/2))) })
	case *IdentExpr:
		g.loadVar(g.c.Uses[e])
	case *BinaryExpr:
		g.binary(e)
	case *UnaryExpr:
		g.unary(e)
	case *InExpr:
		g.rtCall("rt_in", e.Value, e.Container)
	case *RangeExpr:
		g.args(e.Start, e.End)
		inc := uint64(0)
		if e.Inclusive {
			inc = 1
		}
		g.callRT("rt_range", rt(), slot(g.argSlot(0)), inA(), immv(inc))
		g.free(1)
	case *CastExpr:
		g.cast(e)
	case *IndexExpr:
		g.rtCall("rt_index", e.List, e.Index)
	case *SliceExpr:
		flags := uint64(0)
		start, end := e.Start, e.End
		if start != nil {
			flags |= 1
		}
		if end != nil {
			flags |= 2
		}
		g.args(e.List, start, end)
		g.callRT("rt_slice", rt(), slot(g.argSlot(0)), slot(g.argSlot(1)), inA(), immv(flags))
		g.free(2)
	case *FieldAccessExpr:
		g.expr(e.Object, false)
		t := g.tmp()
		g.a.store(rA, rFP, t)
		g.boxLabel(rA, g.str(e.FieldName), tagStr)
		g.callRT("rt_field", rt(), slot(t), inA())
		g.free(1)
	case *CallExpr:
		g.call(e, tail)
	case *DirectCallExpr:
		g.expr(e.Callee, false)
		t := g.tmp()
		g.a.store(rA, rFP, t)
		g.timCall(nil, t, e.Args, tail)
		g.free(1)
	case *LambdaExpr:
		g.closure(g.c.Fns[e])
	case *BlockExpr:
		g.stmts(e.Statements, tail)
	case *MatchExpr:
		end := g.a.newLabel()
		for _, c := range e.Clauses {
			next := g.a.newLabel()
			if c.Guard == nil {
				g.jumpIf(e.Condition, false, next)
			} else {
				g.jumpIf(c.Guard, false, next)
			}
			g.expr(c.Result, tail)
			g.a.jmp(end)
			g.a.bind(next)
		}
		g.expr(e.DefaultExpr, tail)
		g.a.bind(end)
	case *JumpExpr:
		g.jump(e.IsBreak, e.Label, e.Value)
	case *ArenaExpr:
		g.stmts(e.Body, tail)
	case *LengthExpr:
		g.rtCall("rt_len", e.Operand)
	case *RandomExpr:
		g.callRT("rt_random", rt())
	default:
		unsupportedf("expression %T", e)
	}
}

// args evaluates expressions into temporaries, leaving the last one in rA;
// argSlot(i) is where the i-th was saved. The caller frees len-1 slots.
func (g *coreGen) args(es ...Expression) {
	g.argBase = g.f.temps
	for i, e := range es {
		g.expr(e, false)
		if i < len(es)-1 {
			t := g.tmp()
			g.a.store(rA, rFP, t)
		}
	}
}

func (g *coreGen) argSlot(i int) int32 { return slotOff(g.f.fixed + g.argBase + i) }

// rtCall calls a runtime function with the runtime and the values of es.
func (g *coreGen) rtCall(name string, es ...Expression) {
	base := g.f.temps
	for i, e := range es {
		g.expr(e, false)
		if i < len(es)-1 {
			g.a.store(rA, rFP, g.tmp())
		}
	}
	ops := []operand{rt()}
	for i := range es {
		if i < len(es)-1 {
			ops = append(ops, slot(slotOff(g.f.fixed+base+i)))
		} else {
			ops = append(ops, inA())
		}
	}
	g.callRT(name, ops...)
	g.f.temps = base
}

// array evaluates es into adjacent slots and calls use with the address of element 0.
func (g *coreGen) array(es []Expression, use func(base int32, n int)) {
	n := len(es)
	base := g.tmps(max(n, 1))
	for i, e := range es {
		g.expr(e, false)
		g.a.store(rA, rFP, base+int32(8*i))
	}
	use(base, n)
	g.free(max(n, 1))
}

func (g *coreGen) number(e *NumberExpr) {
	if e.Exact == nil {
		g.a.imm(rA, math.Float64bits(e.Value))
		return
	}
	if e.Exact.IsInt() && e.Exact.Num().IsInt64() && math.Abs(float64(e.Exact.Num().Int64())) < 1<<53 {
		g.a.imm(rA, math.Float64bits(float64(e.Exact.Num().Int64())))
		return
	}
	l := g.a.newLabel()
	x := new(big.Rat).Set(e.Exact)
	g.consts = append(g.consts, func() {
		g.a.bind(l)
		if x.IsInt() {
			body := encodeInt(x.Num())
			g.a.emit(le64(kindBig | uint64(1+len(body))<<8))
			g.a.emit(words(body))
		} else {
			body := append(encodeInt(x.Num()), encodeInt(x.Denom())...)
			g.a.emit(le64(kindRat | uint64(1+len(body))<<8))
			g.a.emit(words(body))
		}
	})
	tag := uint64(tagBig)
	if !x.IsInt() {
		tag = tagRat
	}
	g.boxLabel(rA, l, tag)
}

// encodeInt lays out an integer as the runtime does: a word with the limb
// count and sign, then the limbs from least significant.
func encodeInt(n *big.Int) []uint64 {
	limbs := []uint64{}
	m := new(big.Int).Abs(n)
	mask := new(big.Int).SetUint64(math.MaxUint64)
	for m.Sign() > 0 {
		limbs = append(limbs, new(big.Int).And(m, mask).Uint64())
		m.Rsh(m, 64)
	}
	head := uint64(len(limbs))
	if n.Sign() < 0 {
		head |= 1 << 63
	}
	return append([]uint64{head}, limbs...)
}

func le64(v uint64) []byte {
	b := make([]byte, 8)
	for i := range b {
		b[i] = byte(v >> (8 * i))
	}
	return b
}

func words(ws []uint64) []byte {
	var b []byte
	for _, w := range ws {
		b = append(b, le64(w)...)
	}
	return b
}

// str returns the label of a constant string object.
func (g *coreGen) str(s string) label {
	if l, ok := g.strs[s]; ok {
		return l
	}
	l := g.a.newLabel()
	g.strs[s] = l
	g.consts = append(g.consts, func() {
		g.a.bind(l)
		n := len(s)
		w := 2 + (n+8)/8
		g.a.emit(le64(kindStr | uint64(w)<<8))
		g.a.emit(le64(uint64(n)))
		body := make([]byte, (w-2)*8)
		copy(body, s)
		g.a.emit(body)
	})
	return l
}

func (g *coreGen) cast(e *CastExpr) {
	switch e.Type {
	case "str", "string":
		g.rtCall("rt_str", e.Expr)
	case "num", "number":
		g.rtCall("rt_num", e.Expr)
	case "float64", "float32":
		g.rtCall("rt_float", e.Expr)
	case "bool":
		g.boolValue(e.Expr)
	case "int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64":
		g.rtCall("rt_trunc", e.Expr)
	default:
		unsupportedf("cast to %s", e.Type)
	}
}

func (g *coreGen) unary(e *UnaryExpr) {
	switch e.Operator {
	case "-":
		g.expr(e.Operand, false)
		slow, done := g.a.newLabel(), g.a.newLabel()
		g.a.brTagged(rA, slow)
		g.a.fop(fNeg, rA, rA, rA)
		g.a.jmp(done)
		g.a.bind(slow)
		g.callRT("rt_neg", rt(), inA())
		g.a.bind(done)
	case "~b":
		g.expr(e.Operand, false)
		g.callRT("rt_binop", rt(), immv(opBxor), inA(), immv(math.Float64bits(-1)))
	case "#":
		g.rtCall("rt_len", e.Operand)
	case "not":
		g.boolValue(e)
	default:
		unsupportedf("unary %s", e.Operator)
	}
}

func isCondition(e Expression) bool {
	switch e := e.(type) {
	case *BinaryExpr:
		switch e.Operator {
		case "and", "or", "<", "<=", ">", ">=", "==", "!=":
			return true
		}
	case *UnaryExpr:
		return e.Operator == "not"
	}
	return false
}

// boolValue computes 1 or 0 from the truth of e.
func (g *coreGen) boolValue(e Expression) {
	f, end := g.a.newLabel(), g.a.newLabel()
	g.jumpIf(e, false, f)
	g.a.imm(rA, valTrue)
	g.a.jmp(end)
	g.a.bind(f)
	g.a.imm(rA, 0)
	g.a.bind(end)
}

func (g *coreGen) binary(e *BinaryExpr) {
	if isCondition(e) {
		g.boolValue(e)
		return
	}
	if e.Operator == "or!" {
		g.expr(e.Left, false)
		t := g.tmp()
		g.a.store(rA, rFP, t)
		g.callRT("rt_failed", rt(), inA())
		keep, end := g.a.newLabel(), g.a.newLabel()
		g.a.brZero(rA, keep)
		g.free(1)
		g.expr(e.Right, false)
		g.a.jmp(end)
		g.a.bind(keep)
		g.a.load(rA, rFP, t)
		g.a.bind(end)
		return
	}
	op, ok := binops[e.Operator]
	if !ok {
		unsupportedf("operator %s", e.Operator)
	}
	g.expr(e.Left, false)
	t := g.tmp()
	g.a.store(rA, rFP, t)
	g.expr(e.Right, false)
	slow, done := g.a.newLabel(), g.a.newLabel()
	if op == opAdd || op == opSub || op == opMul {
		ok := g.a.newLabel()
		g.a.load(rB, rFP, t)
		g.a.brTagged(rB, slow)
		g.a.brTagged(rA, slow)
		g.a.fop(map[uint64]fop{opAdd: fAdd, opSub: fSub, opMul: fMul}[op], rC, rB, rA)
		// exact operands stay exact only below 2^53
		g.a.fop(fAbs, rD, rC, rC)
		g.a.imm(rB, two53Bits)
		g.a.fbr(fLt, rD, rB, ok)
		g.a.bind(slow)
		g.callRT("rt_binop", rt(), immv(op), slot(t), inA())
		g.a.jmp(done)
		g.a.bind(ok)
		g.a.mov(rA, rC)
	} else {
		g.callRT("rt_binop", rt(), immv(op), slot(t), inA())
	}
	g.a.bind(done)
	g.free(1)
}

var fconds = map[string]fcond{"<": fLt, "<=": fLe, ">": fGt, ">=": fGe}

// jumpIf jumps to l when the truth of e equals want.
func (g *coreGen) jumpIf(e Expression, want bool, l label) {
	switch x := e.(type) {
	case *BooleanExpr:
		if x.Value == want {
			g.a.jmp(l)
		}
		return
	case *NumberExpr:
		if x.Exact == nil && (x.Value != 0) == want {
			g.a.jmp(l)
		}
		if x.Exact == nil {
			return
		}
	case *UnaryExpr:
		if x.Operator == "not" {
			g.jumpIf(x.Operand, !want, l)
			return
		}
	case *BinaryExpr:
		switch x.Operator {
		case "and", "or":
			if (x.Operator == "and") == want {
				skip := g.a.newLabel()
				g.jumpIf(x.Left, !want, skip)
				g.jumpIf(x.Right, want, l)
				g.a.bind(skip)
			} else {
				g.jumpIf(x.Left, want, l)
				g.jumpIf(x.Right, want, l)
			}
			return
		case "<", "<=", ">", ">=", "==", "!=":
			g.compare(x, want, l)
			return
		}
	}
	g.expr(e, false)
	slow, done := g.a.newLabel(), g.a.newLabel()
	g.a.brTagged(rA, slow)
	g.a.shiftImm(aluShl, rB, rA, 1) // plain doubles are true unless ±0
	if want {
		g.a.brNonZero(rB, l)
	} else {
		g.a.brZero(rB, l)
	}
	g.a.jmp(done)
	g.a.bind(slow)
	g.callRT("rt_truthy", rt(), inA())
	if want {
		g.a.brNonZero(rA, l)
	} else {
		g.a.brZero(rA, l)
	}
	g.a.bind(done)
}

func (g *coreGen) compare(x *BinaryExpr, want bool, l label) {
	g.expr(x.Left, false)
	t := g.tmp()
	g.a.store(rA, rFP, t)
	g.expr(x.Right, false)
	g.a.load(rB, rFP, t)
	slow, done := g.a.newLabel(), g.a.newLabel()
	g.a.brTagged(rB, slow)
	g.a.brTagged(rA, slow)
	// plain doubles are never NaN, so a comparison's negation is the swapped one
	switch x.Operator {
	case "==", "!=":
		if (x.Operator == "==") == want {
			g.a.fbr(fEq, rB, rA, l)
		} else {
			g.a.fbr(fEq, rB, rA, done)
			g.a.jmp(l)
		}
	default:
		c := fconds[x.Operator]
		if want {
			g.a.fbr(c, rB, rA, l)
		} else {
			g.a.fbr(map[fcond]fcond{fLt: fLe, fLe: fLt, fGt: fGe, fGe: fGt}[c], rA, rB, l)
		}
	}
	g.a.jmp(done)
	g.a.bind(slow)
	g.callRT("rt_binop", rt(), immv(binops[x.Operator]), slot(t), inA())
	g.a.imm(rB, valTrue)
	if want {
		g.a.br(cEq, rA, rB, l)
	} else {
		g.a.br(cNe, rA, rB, l)
	}
	g.a.bind(done)
	g.free(1)
}

// Functions and calls.

// closure makes a function value for f, capturing from the current frame.
func (g *coreGen) closure(f *Fun) {
	n := len(f.Captures)
	base := g.tmps(max(n, 1))
	for i, c := range f.Captures {
		g.loadRaw(c.Outer)
		g.a.store(rA, rFP, base+int32(8*i))
	}
	arity := uint64(len(f.Params))
	if f.Variadic != nil {
		arity |= 1 << 32
	}
	g.a.addr(rA, g.fnLabel(f))
	g.callRT("rt_closure", rt(), inA(), immv(arity), slotAt(base), immv(uint64(n)))
	g.free(max(n, 1))
}

func (g *coreGen) call(e *CallExpr, tail bool) {
	if v := g.c.Callees[e]; v != nil {
		g.callVar(v, e.Args, tail)
		return
	}
	g.builtinCall(e.Function, e.Args)
}

// callVar calls the function held by variable v.
func (g *coreGen) callVar(v *Var, args []Expression, tail bool) {
	f := v.Func
	if f != nil && !v.Mutable {
		t := int32(0)
		if len(f.Captures) > 0 {
			g.loadVar(v)
			t = g.tmp()
			g.a.store(rA, rFP, t)
		}
		g.timCall(f, t, args, tail)
		if t != 0 {
			g.free(1)
		}
		return
	}
	g.loadVar(v)
	t := g.tmp()
	g.a.store(rA, rFP, t)
	g.timCall(nil, t, args, tail)
	g.free(1)
}

// timCall calls a Tim function: f directly when known, otherwise the
// function value in slot fnSlot (which is also the environment, if nonzero).
func (g *coreGen) timCall(f *Fun, fnSlot int32, args []Expression, tail bool) {
	n := len(args)
	base := g.tmps(max(n, 1))
	for i, e := range args {
		g.expr(e, false)
		g.a.store(rA, rFP, base+int32(8*i))
	}
	fail, done := g.a.newLabel(), g.a.newLabel()
	code := int32(0)
	if f == nil {
		code = g.tmp()
		g.callRT("rt_fn_code", rt(), slot(fnSlot), immv(uint64(n)))
		g.a.brZero(rA, fail)
		g.a.store(rA, rFP, code)
	}
	cur := g.f.fn
	isTail := tail && !g.f.top && g.f.defers == 0 && n <= len(cur.Params)
	for i := 0; i < n; i++ {
		g.a.load(rB, rFP, base+int32(8*i))
		if isTail {
			g.a.store(rB, rFP, int32(16+8*i))
		} else {
			g.a.store(rB, rSP, int32(8*i))
		}
	}
	if !isTail {
		g.f.maxOut = max(g.f.maxOut, n)
	}
	if fnSlot != 0 {
		g.a.load(rEnv, rFP, fnSlot)
	} else {
		g.a.imm(rEnv, 0)
	}
	g.a.imm(rArgc, uint64(n))
	switch {
	case f != nil && isTail:
		g.a.tailJumpLabel(g.fnLabel(f))
	case f != nil:
		g.a.call(g.fnLabel(f))
	case isTail:
		g.a.load(rA, rFP, code)
		g.a.tailJump(rA)
	default:
		g.a.load(rA, rFP, code)
		g.a.callReg(rA)
	}
	if f == nil {
		g.a.jmp(done)
		g.a.bind(fail)
		g.callRT("rt_call_error", rt(), slot(fnSlot), immv(uint64(n)))
		g.free(1)
	}
	g.a.bind(done)
	g.free(max(n, 1))
}

// Builtins.

var printFns = map[string][2]uint64{"print": {1, 0}, "println": {1, 1}, "eprint": {2, 0}, "eprintln": {2, 1}}

func (g *coreGen) builtinCall(name string, args []Expression) {
	switch name {
	case "print", "println", "eprint", "eprintln":
		p := printFns[name]
		g.array(args, func(base int32, n int) {
			g.callRT("rt_print", rt(), immv(p[0]), slotAt(base), immv(uint64(n)), immv(p[1]))
		})
		return
	case "printf", "eprintf":
		fd := uint64(1)
		if name == "eprintf" {
			fd = 2
		}
		g.expr(args[0], false)
		t := g.tmp()
		g.a.store(rA, rFP, t)
		g.array(args[1:], func(base int32, n int) {
			g.callRT("rt_printf", rt(), immv(fd), slot(t), slotAt(base), immv(uint64(n)))
		})
		g.free(1)
		return
	case "min", "max":
		g.array(args, func(base int32, n int) { g.callRT("rt_"+name, rt(), slotAt(base), immv(uint64(n))) })
		return
	case "exit":
		if len(args) == 0 {
			args = []Expression{&NumberExpr{}}
		}
	case "sort":
		if len(args) == 2 {
			unsupportedf("sort with a key outside the prelude")
		}
	case "_error_code_extract":
		name = "errtext"
	case "bit", "rotl", "rotr":
		op := map[string]uint64{"bit": opBit, "rotl": opRotl, "rotr": opRotr}[name]
		g.expr(args[0], false)
		t := g.tmp()
		g.a.store(rA, rFP, t)
		g.expr(args[1], false)
		g.callRT("rt_binop", rt(), immv(op), slot(t), inA())
		g.free(1)
		return
	case "__sort_keys":
		name = "sort_by"
	}
	if _, ok := g.syms["rt_"+name]; !ok {
		unsupportedf("builtin %s", name)
	}
	if len(args) == 0 {
		g.callRT("rt_"+name, rt())
		return
	}
	g.rtCall("rt_"+name, args...)
}

// builtinValue loads a function value that calls builtin name, made on
// first use and kept in a hidden global.
func (g *coreGen) builtinValue(name string) {
	v := g.wrappers[name]
	if v == nil {
		b := builtins[name]
		if b.max < 0 || b.max != b.min {
			unsupportedf("builtin %s as a value", name)
		}
		f := &Fun{Name: "builtin " + name}
		for i := 0; i < b.min; i++ {
			f.Params = append(f.Params, &Var{Name: fmt.Sprintf("a%d", i), Kind: SymParam, Fn: f, Index: i})
		}
		v = &Var{Name: name, Kind: SymGlobal, Index: g.nglobals, Func: f}
		g.nglobals++
		g.wrappers[name] = v
	}
	made := g.a.newLabel()
	g.a.load(rA, rGlob, int32(8*v.Index))
	g.a.brNonZero(rA, made)
	g.closure(v.Func)
	g.a.store(rA, rGlob, int32(8*v.Index))
	g.a.bind(made)
}

// builtinBody is the body of a wrapper made by builtinValue.
func (g *coreGen) builtinBody(f *Fun) {
	name := strings.TrimPrefix(f.Name, "builtin ")
	var args []Expression
	for _, p := range f.Params {
		id := &IdentExpr{Name: p.Name}
		g.c.Uses[id] = p
		args = append(args, id)
	}
	g.builtinCall(name, args)
}

// walkCalls visits every call in a statement.
func walkCalls(n any, visit func(*CallExpr)) {
	switch n := n.(type) {
	case *CallExpr:
		visit(n)
		for _, a := range n.Args {
			walkCalls(a, visit)
		}
	case *ExpressionStmt:
		walkCalls(n.Expr, visit)
	case *AssignStmt:
		walkCalls(n.Value, visit)
	case *MultipleAssignStmt:
		walkCalls(n.Value, visit)
	case *BinaryExpr:
		walkCalls(n.Left, visit)
		walkCalls(n.Right, visit)
	case *UnaryExpr:
		walkCalls(n.Operand, visit)
	case *MatchExpr:
		walkCalls(n.Condition, visit)
		for _, c := range n.Clauses {
			walkCalls(c.Guard, visit)
			walkCalls(c.Result, visit)
		}
		walkCalls(n.DefaultExpr, visit)
	case *BlockExpr:
		for _, s := range n.Statements {
			walkCalls(s, visit)
		}
	case *LoopStmt:
		walkCalls(n.Iterable, visit)
		for _, s := range n.Body {
			walkCalls(s, visit)
		}
	case *WhileStmt:
		walkCalls(n.Condition, visit)
		for _, s := range n.Body {
			walkCalls(s, visit)
		}
	case *IfStmt:
		for _, b := range n.Branches {
			walkCalls(b.Condition, visit)
			for _, s := range b.Body {
				walkCalls(s, visit)
			}
		}
		for _, s := range n.ElseBody {
			walkCalls(s, visit)
		}
	case *JumpStmt:
		walkCalls(n.Value, visit)
	case *ListExpr:
		for _, e := range n.Elements {
			walkCalls(e, visit)
		}
	case *FStringExpr:
		for _, e := range n.Parts {
			walkCalls(e, visit)
		}
	case *DirectCallExpr:
		walkCalls(n.Callee, visit)
		for _, a := range n.Args {
			walkCalls(a, visit)
		}
	}
}
