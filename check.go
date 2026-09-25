package main

import (
	"fmt"
	"sort"
	"strings"
)

// check.go resolves every name to a symbol (global, local, parameter or
// closure capture), enforces mutability, infers the static types it can, and
// reports errors with positions before any code is generated.

type SymKind uint8

const (
	SymGlobal SymKind = iota
	SymLocal
	SymParam
	SymCapture
	SymBuiltin // a builtin function used as a value
)

type Type uint8

const (
	TAny Type = iota
	TNum
	TStr
	TList
	TMap
	TFn
	TPtr
)

var typeNames = [...]string{"any", "num", "str", "list", "map", "fn", "ptr"}

func (t Type) String() string { return typeNames[t] }

type Var struct {
	Name    string
	Kind    SymKind
	Mutable bool
	Boxed   bool      // a mutable variable captured by a closure: it lives in a heap cell
	Index   int       // global number, local slot, parameter number or capture number
	Fn      *Fun // the function whose frame or closure holds it (nil for globals)
	Outer   *Var   // for captures: the symbol captured from the enclosing function
	Func    *Fun // the function literal it is bound to, when known
	Type    Type
	Pos     Pos
}

// Fun is a function literal, or the top level of the program.
type Fun struct {
	Name     string
	Lambda   *LambdaExpr
	Parent   *Fun
	Params   []*Var
	Variadic *Var
	Locals   []*Var
	Captures []*Var
}

// Checked is the result of checking a program.
type Checked struct {
	Program     *Program
	Top         *Fun
	Funcs       []*Fun
	Globals     []*Var
	Uses        map[*IdentExpr]*Var
	Defs        map[Statement][]*Var // bindings made by AssignStmt, MultipleAssignStmt and LoopStmt
	Updates     map[Statement]*Var   // the variable updated by an update statement
	Callees     map[*CallExpr]*Var   // calls through a variable; absent for builtins
	Fns         map[*LambdaExpr]*Fun
	Types       map[Expression]Type
	Unsupported []string // features the core code generator does not handle yet
}

type scope struct {
	parent *scope
	fn     *Fun
	names  map[string]*Var
}

type checker struct {
	c      *Checked
	file   string
	errs   *ErrorCollector
	sc     *scope
	fn     *Fun
	loops  int
	cimps  map[string]bool
	unsupp map[string]bool
}

// Check analyses a parsed program. It returns an error when the program is
// invalid; the error has already been printed with source context.
func Check(prog *Program, file, src string) (*Checked, error) {
	top := &Fun{Name: "<top>"}
	k := &checker{
		c: &Checked{
			Program: prog, Top: top, Funcs: []*Fun{top},
			Uses: map[*IdentExpr]*Var{}, Defs: map[Statement][]*Var{}, Updates: map[Statement]*Var{},
			Callees: map[*CallExpr]*Var{}, Fns: map[*LambdaExpr]*Fun{}, Types: map[Expression]Type{},
		},
		file:   file,
		errs:   NewErrorCollector(20),
		fn:     top,
		cimps:  map[string]bool{"c": true, "C": true},
		unsupp: map[string]bool{},
	}
	k.errs.SetSourceCode(src)
	k.sc = &scope{fn: top, names: map[string]*Var{}}
	k.predeclare(prog.Statements)
	for _, s := range prog.Statements {
		k.stmt(s)
	}
	for f := range k.unsupp {
		k.c.Unsupported = append(k.c.Unsupported, f)
	}
	sort.Strings(k.c.Unsupported)
	if k.errs.HasErrors() {
		return nil, newReportedError(strings.TrimSpace(k.errs.Report(false)))
	}
	return k.c, nil
}

func (k *checker) errorf(pos Pos, format string, args ...any) {
	k.errs.AddError(CompilerError{Level: LevelError, Category: CategorySemantic, Message: fmt.Sprintf(format, args...),
		Location: SourceLocation{File: k.file, Line: pos.Line, Column: pos.Col, Length: 1}})
}

func (k *checker) unsupported(feature string) { k.unsupp[feature] = true }

// predeclare makes every top-level binding visible to all functions, so
// top-level functions and globals may be used before they are defined.
func (k *checker) predeclare(stmts []Statement) {
	for _, s := range stmts {
		var names []string
		var pos Pos
		mutable := false
		switch s := s.(type) {
		case *AssignStmt:
			if s.IsUpdate {
				continue
			}
			names, pos, mutable = []string{s.Name}, s.Pos, s.Mutable
		case *MultipleAssignStmt:
			if s.IsUpdate {
				continue
			}
			names, pos, mutable = s.Names, s.Pos, s.Mutable
		case *CImportStmt:
			k.cimps[s.Alias] = true
		}
		for _, n := range names {
			if prev, ok := k.sc.names[n]; ok {
				k.errorf(pos, "'%s' is already defined at line %d; bind a new name, or use := and <- for a variable that changes", n, prev.Pos.Line)
				continue
			}
			sym := &Var{Name: n, Kind: SymGlobal, Mutable: mutable, Index: len(k.c.Globals), Pos: pos}
			k.c.Globals = append(k.c.Globals, sym)
			k.sc.names[n] = sym
		}
	}
}

func (k *checker) push() { k.sc = &scope{parent: k.sc, fn: k.fn, names: map[string]*Var{}} }
func (k *checker) pop()  { k.sc = k.sc.parent }

// define binds a new name in the current block.
func (k *checker) define(name string, mutable bool, pos Pos) *Var {
	if k.sc.parent == nil {
		if sym, ok := k.sc.names[name]; ok && sym.Kind == SymGlobal {
			return sym // predeclared
		}
	}
	if prev, ok := k.sc.names[name]; ok {
		k.errorf(pos, "'%s' is already defined in this block at line %d; bind a new name, or use := and <- for a variable that changes", name, prev.Pos.Line)
		return prev
	}
	sym := &Var{Name: name, Kind: SymLocal, Mutable: mutable, Fn: k.fn, Index: len(k.fn.Locals), Pos: pos}
	k.fn.Locals = append(k.fn.Locals, sym)
	k.sc.names[name] = sym
	return sym
}

// lookup resolves a name, creating closure captures as needed.
func (k *checker) lookup(name string) *Var {
	for s := k.sc; s != nil; s = s.parent {
		if sym, ok := s.names[name]; ok {
			return k.capture(k.fn, sym)
		}
	}
	return nil
}

func (k *checker) capture(fn *Fun, sym *Var) *Var {
	if sym.Kind == SymGlobal || sym.Fn == fn {
		return sym
	}
	outer := k.capture(fn.Parent, sym)
	for _, c := range fn.Captures {
		if c.Outer == outer {
			return c
		}
	}
	root := outer
	for root.Kind == SymCapture {
		root = root.Outer
	}
	if root.Mutable {
		root.Boxed = true
	}
	c := &Var{Name: sym.Name, Kind: SymCapture, Mutable: sym.Mutable, Fn: fn, Outer: outer, Index: len(fn.Captures),
		Func: sym.Func, Type: sym.Type, Pos: sym.Pos}
	fn.Captures = append(fn.Captures, c)
	return c
}

// names lists everything visible, for "did you mean" suggestions.
func (k *checker) names() map[string]int {
	all := map[string]int{}
	for s := k.sc; s != nil; s = s.parent {
		for n := range s.names {
			all[n] = 1
		}
	}
	for n := range builtins {
		all[n] = 1
	}
	return all
}

func (k *checker) undefined(pos Pos, what, name string) {
	msg := fmt.Sprintf("undefined %s '%s'", what, name)
	if sugg := findSimilarIdentifiers(name, k.names(), 1); len(sugg) > 0 {
		msg += "; did you mean " + strings.Join(quoteAll(sugg), " or ") + "?"
	}
	k.errorf(pos, "%s", msg)
}

func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "'" + x + "'"
	}
	return out
}

// Statements.

func (k *checker) block(stmts []Statement) {
	k.push()
	for _, s := range stmts {
		k.stmt(s)
	}
	k.pop()
}

func (k *checker) stmt(s Statement) {
	switch s := s.(type) {
	case *ExpressionStmt:
		k.expr(s.Expr)
	case *AssignStmt:
		k.assign(s)
	case *MultipleAssignStmt:
		t := k.expr(s.Value)
		if t != TAny && t != TList {
			k.errorf(s.Pos, "cannot unpack a %s into %d names; the right side must be a list", t, len(s.Names))
		}
		if s.IsUpdate {
			for _, n := range s.Names {
				k.updateTarget(s.Pos, n)
			}
			return
		}
		var syms []*Var
		for _, n := range s.Names {
			syms = append(syms, k.define(n, s.Mutable, s.Pos))
		}
		k.c.Defs[s] = syms
	case *MapUpdateStmt:
		k.expr(s.Index)
		k.expr(s.Value)
		if sym := k.modifyTarget(s.Pos, s.MapName); sym != nil {
			k.c.Updates[s] = sym
		}
	case *IndexUpdateStmt:
		k.expr(s.Target)
		k.expr(s.Index)
		k.expr(s.Value)
		k.requireMutableRoot(s.Pos, s.Target)
	case *FieldUpdateStmt:
		k.expr(s.Object)
		k.expr(s.Value)
		k.requireMutableRoot(s.Pos, s.Object)
	case *LoopStmt:
		t := k.expr(s.Iterable)
		if t == TNum || t == TFn {
			k.errorf(s.Pos, "cannot loop over a %s; loop over a range like 0..<n, a list, a string or a map", t)
		}
		if s.IteratorType != "" {
			k.unsupported("typed loop variables")
		}
		k.push()
		it := k.define(s.Iterator, false, s.Pos)
		if _, isRange := s.Iterable.(*RangeExpr); isRange {
			it.Type = TNum
		}
		k.c.Defs[s] = []*Var{it}
		k.loops++
		k.block(s.Body)
		k.loops--
		k.pop()
	case *WhileStmt:
		k.expr(s.Condition)
		k.loops++
		k.block(s.Body)
		k.loops--
	case *IfStmt:
		for _, b := range s.Branches {
			k.expr(b.Condition)
			k.block(b.Body)
		}
		k.block(s.ElseBody)
	case *JumpStmt:
		if s.Value != nil {
			k.expr(s.Value)
		}
	case *DeferStmt:
		k.expr(s.Call)
	case *ArenaStmt:
		k.block(s.Body)
	case *CStructDecl:
		k.unsupported("cstruct")
	case *CImportStmt:
		k.unsupported("C imports")
	case *ImportStmt:
		k.unsupported("Tim imports")
	case *ExportStmt:
	default:
		k.unsupported(fmt.Sprintf("%T", s))
	}
}

func (k *checker) assign(s *AssignStmt) {
	if s.IsUpdate {
		t := k.expr(s.Value)
		if sym := k.updateTarget(s.Pos, s.Name); sym != nil {
			k.c.Updates[s] = sym
			if sym.Type != t {
				sym.Type = TAny
			}
		}
		return
	}
	if s.TypeAnnotation != nil && s.TypeAnnotation.Kind >= TypeCString {
		k.unsupported("C type annotations")
	}
	lambda, isFn := s.Value.(*LambdaExpr)
	var sym *Var
	if isFn {
		// Define first so the function can call itself.
		sym = k.define(s.Name, s.Mutable, s.Pos)
		sym.Func = k.lambda(lambda, s.Name)
		sym.Type = TFn
		k.c.Types[lambda] = TFn
	} else {
		t := k.expr(s.Value)
		sym = k.define(s.Name, s.Mutable, s.Pos)
		sym.Type = t
	}
	k.c.Defs[s] = []*Var{sym}
}

func (k *checker) updateTarget(pos Pos, name string) *Var {
	sym := k.lookup(name)
	if sym == nil {
		k.undefined(pos, "variable", name)
		return nil
	}
	if !sym.Mutable {
		k.errorf(pos, "cannot update '%s': it was bound with '=' at line %d; bind it with ':=' to let it change", name, sym.Pos.Line)
	}
	return sym
}

func (k *checker) modifyTarget(pos Pos, name string) *Var {
	sym := k.lookup(name)
	if sym == nil {
		k.undefined(pos, "variable", name)
		return nil
	}
	if !sym.Mutable {
		k.errorf(pos, "cannot modify '%s': it was bound with '=' at line %d; bind it with ':=' to let it change", name, sym.Pos.Line)
	}
	return sym
}

func (k *checker) requireMutableRoot(pos Pos, e Expression) {
	for {
		switch x := e.(type) {
		case *IndexExpr:
			e = x.List
			continue
		case *FieldAccessExpr:
			e = x.Object
			continue
		case *IdentExpr:
			if sym := k.c.Uses[x]; sym != nil && !sym.Mutable {
				k.errorf(pos, "cannot modify '%s': it was bound with '=' at line %d; bind it with ':=' to let it change", x.Name, sym.Pos.Line)
			}
		}
		return
	}
}

// lambda checks a function literal and returns its Fun.
func (k *checker) lambda(l *LambdaExpr, name string) *Fun {
	if name == "" {
		name = fmt.Sprintf("lambda@%s", l.Pos)
	}
	fn := &Fun{Name: name, Lambda: l, Parent: k.fn}
	k.c.Fns[l] = fn
	k.c.Funcs = append(k.c.Funcs, fn)
	if len(l.ParamCStructTypes) > 0 {
		k.unsupported("cstruct parameters")
	}
	savedFn, savedLoops := k.fn, k.loops
	k.fn, k.loops = fn, 0
	k.push()
	param := func(n string) *Var {
		if _, dup := k.sc.names[n]; dup {
			k.errorf(l.Pos, "parameter '%s' appears twice", n)
		}
		sym := &Var{Name: n, Kind: SymParam, Fn: fn, Index: len(fn.Params), Pos: l.Pos}
		k.sc.names[n] = sym
		return sym
	}
	for _, p := range l.Params {
		fn.Params = append(fn.Params, param(p))
	}
	if l.VariadicParam != "" {
		fn.Variadic = param(l.VariadicParam)
		fn.Variadic.Type = TList
	}
	k.expr(l.Body)
	k.pop()
	k.fn, k.loops = savedFn, savedLoops
	return fn
}

// Expressions.

var numericOps = map[string]bool{"-": true, "*": true, "/": true, "%": true, "**": true,
	"|b": true, "&b": true, "^b": true, "<<b": true, ">>b": true, "?b": true, "<<<b": true, ">>>b": true}

var opNames = map[string]string{"-": "subtract", "*": "multiply", "/": "divide", "%": "take the remainder of",
	"**": "raise", "|b": "or", "&b": "and", "^b": "xor", "<<b": "shift", ">>b": "shift", "?b": "test bits of",
	"<<<b": "rotate", ">>>b": "rotate"}

var builtinTypes = map[string]Type{
	"str": TStr, "upper": TStr, "lower": TStr, "trim": TStr, "join": TStr, "replace": TStr, "chr": TStr,
	"type": TStr, "readln": TStr, "read_file": TStr, "_error_code_extract": TStr,
	"split": TList, "keys": TList, "values": TList, "sort": TList, "bytes": TList, "runes": TList,
	"map": TList, "filter": TList, "zip": TList, "enumerate": TList, "args": TList,
	"abs": TNum, "floor": TNum, "ceil": TNum, "round": TNum, "trunc": TNum, "sqrt": TNum, "exp": TNum,
	"log": TNum, "log10": TNum, "sin": TNum, "cos": TNum, "tan": TNum, "asin": TNum, "acos": TNum,
	"atan": TNum, "atan2": TNum, "pow": TNum, "gcd": TNum, "random": TNum, "float": TNum, "ord": TNum,
	"starts_with": TNum, "ends_with": TNum, "find": TNum, "any": TNum, "all": TNum,
}

func (k *checker) exprs(es []Expression) {
	for _, e := range es {
		k.expr(e)
	}
}

func (k *checker) expr(e Expression) Type {
	t := k.infer(e)
	if t != TAny {
		k.c.Types[e] = t
	}
	return t
}

func (k *checker) infer(e Expression) Type {
	switch e := e.(type) {
	case nil:
		return TAny
	case *NumberExpr, *BooleanExpr, *RandomExpr:
		return TNum
	case *StringExpr:
		return TStr
	case *FStringExpr:
		k.exprs(e.Parts)
		return TStr
	case *ListExpr:
		k.exprs(e.Elements)
		return TList
	case *MapExpr:
		for i := range e.Keys {
			k.expr(e.Keys[i])
			k.expr(e.Values[i])
		}
		return TMap
	case *IdentExpr:
		sym := k.lookup(e.Name)
		if sym == nil {
			if _, ok := builtins[e.Name]; ok {
				sym = &Var{Name: e.Name, Kind: SymBuiltin, Type: TFn, Pos: e.Pos}
			} else {
				k.undefined(e.Pos, "variable", e.Name)
				return TAny
			}
		}
		k.c.Uses[e] = sym
		if sym.Mutable {
			return TAny
		}
		return sym.Type
	case *BinaryExpr:
		return k.binary(e)
	case *UnaryExpr:
		t := k.expr(e.Operand)
		switch e.Operator {
		case "-", "~b":
			if t != TAny && t != TNum {
				k.errorf(e.Pos, "cannot negate a %s", t)
			}
			return TNum
		case "#":
			if t == TNum || t == TFn {
				k.errorf(e.Pos, "'#' needs a string, list or map, not a %s", t)
			}
			return TNum
		}
		return TNum
	case *InExpr:
		k.expr(e.Value)
		if t := k.expr(e.Container); t == TNum || t == TFn {
			k.errorf(e.Pos, "'in' needs a string, list, map or range on the right, not a %s", t)
		}
		return TNum
	case *RangeExpr:
		for _, x := range []Expression{e.Start, e.End} {
			if t := k.expr(x); t != TAny && t != TNum {
				k.errorf(e.Pos, "a range needs numbers, not a %s", t)
			}
		}
		return TList
	case *CastExpr:
		k.expr(e.Expr)
		switch e.Type {
		case "str", "string":
			return TStr
		case "num", "number", "float64", "float32", "int8", "int16", "int32", "int64",
			"uint8", "uint16", "uint32", "uint64", "bool":
			return TNum
		}
		k.unsupported("C casts")
		return TAny
	case *IndexExpr:
		t := k.expr(e.List)
		k.expr(e.Index)
		if t == TNum || t == TFn {
			k.errorf(e.Pos, "cannot index a %s", t)
		}
		if t == TStr {
			return TNum
		}
		return TAny
	case *SliceExpr:
		t := k.expr(e.List)
		k.expr(e.Start)
		k.expr(e.End)
		if e.Step != nil {
			k.expr(e.Step)
		}
		if t == TNum || t == TFn || t == TMap {
			k.errorf(e.Pos, "cannot slice a %s", t)
		}
		return t
	case *FieldAccessExpr:
		t := k.expr(e.Object)
		if t == TNum || t == TStr || t == TList || t == TFn {
			k.errorf(e.Pos, "a %s has no field '%s'", t, e.FieldName)
		}
		return TAny
	case *CallExpr:
		return k.call(e)
	case *DirectCallExpr:
		if t := k.expr(e.Callee); t != TAny && t != TFn {
			k.errorf(e.Pos, "cannot call a %s", t)
		}
		k.exprs(e.Args)
		return TAny
	case *LambdaExpr:
		k.lambda(e, "")
		return TFn
	case *BlockExpr:
		k.push()
		var t Type
		for i, s := range e.Statements {
			k.stmt(s)
			if es, ok := s.(*ExpressionStmt); ok && i == len(e.Statements)-1 {
				t = k.c.Types[es.Expr]
			}
		}
		k.pop()
		return t
	case *MatchExpr:
		k.expr(e.Condition)
		var ts []Type
		for _, c := range e.Clauses {
			k.expr(c.Guard)
			ts = append(ts, k.expr(c.Result))
		}
		ts = append(ts, k.expr(e.DefaultExpr))
		for _, t := range ts[1:] {
			if t != ts[0] {
				return TAny
			}
		}
		return ts[0]
	case *JumpExpr:
		k.expr(e.Value)
		return TAny
	case *UnsafeExpr:
		k.unsupported("unsafe")
		return TAny
	case *ArenaExpr:
		k.block(e.Body)
		return TAny
	case *NamespacedIdentExpr:
		k.unsupported("C constants")
		return TAny
	case *VectorExpr:
		k.unsupported("SIMD vectors")
		k.exprs(e.Components)
		return TAny
	case *LengthExpr:
		k.expr(e.Operand)
		return TNum
	case *FMAExpr:
		k.expr(e.A)
		k.expr(e.B)
		k.expr(e.C)
		return TNum
	}
	k.unsupported(fmt.Sprintf("%T", e))
	return TAny
}

func (k *checker) binary(e *BinaryExpr) Type {
	l, r := k.expr(e.Left), k.expr(e.Right)
	switch op := e.Operator; {
	case op == "+":
		switch {
		case l == TAny || r == TAny:
			if l == r || l == TAny && r == TAny {
				return TAny
			}
			if l == TAny {
				return r
			}
			return l
		case l == r && (l == TNum || l == TStr || l == TList):
			return l
		case l == TStr || r == TStr:
			k.errorf(e.Pos, "cannot add a %s and a %s; use an f-string like f\"{a}{b}\" or str()", l, r)
		default:
			k.errorf(e.Pos, "cannot add a %s and a %s", l, r)
		}
		return TAny
	case op == "*" && l == TList:
		return TList
	case numericOps[op]:
		for _, t := range []Type{l, r} {
			if t != TAny && t != TNum {
				k.errorf(e.Pos, "cannot %s a %s; '%s' needs numbers", opNames[op], t, strings.TrimSuffix(op, "b"))
				break
			}
		}
		return TNum
	case op == "or!":
		if l == r {
			return l
		}
		return TAny
	case op == "and" || op == "or":
		return TNum
	case op == "<" || op == "<=" || op == ">" || op == ">=":
		if l != TAny && r != TAny && l != r {
			k.errorf(e.Pos, "cannot compare a %s with a %s", l, r)
		}
		return TNum
	}
	return TNum
}

func (k *checker) call(e *CallExpr) Type {
	name := e.Function
	if recv, method, ok := strings.Cut(name, "."); ok {
		if k.cimps[recv] && k.lookup(recv) == nil {
			k.unsupported("C calls")
			k.exprs(e.Args)
			return TAny
		}
		if sym := k.lookup(recv); sym != nil {
			// x.f(args) calls f(x, args).
			e.Function = method
			e.Args = append([]Expression{&IdentExpr{Pos: e.Pos, Name: recv}}, e.Args...)
			return k.call(e)
		}
		k.unsupported("module calls")
		k.exprs(e.Args)
		return TAny
	}
	if e.IsCFFI {
		k.unsupported("C calls")
		k.exprs(e.Args)
		return TAny
	}
	k.exprs(e.Args)
	if sym := k.lookup(name); sym != nil {
		k.c.Callees[e] = sym
		if t := sym.Type; t != TAny && t != TFn && !sym.Mutable {
			k.errorf(e.Pos, "cannot call '%s': it is a %s", name, t)
		}
		if f := sym.Func; f != nil && !sym.Mutable {
			if f.Variadic == nil && len(e.Args) != len(f.Params) || f.Variadic != nil && len(e.Args) < len(f.Params) {
				k.errorf(e.Pos, "'%s' takes %s, but %d %s given", name, plural(len(f.Params), "argument"), len(e.Args), wasWere(len(e.Args)))
			}
		}
		return TAny
	}
	if name == "_error_code_extract" {
		return TStr
	}
	b, ok := builtins[name]
	if !ok {
		if legacyBuiltins[name] {
			k.unsupported("legacy builtin " + name)
		} else {
			k.undefined(e.Pos, "function", name)
		}
		return TAny
	}
	if len(e.Args) < b.min || b.max >= 0 && len(e.Args) > b.max {
		want := plural(b.min, "argument")
		switch {
		case b.max < 0:
			want = "at least " + want
		case b.max != b.min:
			want = fmt.Sprintf("%d to %d arguments", b.min, b.max)
		}
		k.errorf(e.Pos, "'%s' takes %s, but %d %s given", name, want, len(e.Args), wasWere(len(e.Args)))
	}
	if t, ok := builtinTypes[name]; ok {
		return t
	}
	if (name == "min" || name == "max") && len(e.Args) > 1 {
		return TNum
	}
	return TAny
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func wasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}
