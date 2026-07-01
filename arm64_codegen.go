// Completion: 95% - ARM64 codegen complete, all core features implemented
package main

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"unsafe"
)

// ARM64CodeGen handles ARM64 code generation for macOS
type ARM64CodeGen struct {
	out                 *ARM64Out
	eb                  *ExecutableBuilder
	stackVars           map[string]int               // variable name -> stack offset from fp
	mutableVars         map[string]bool              // variable name -> is mutable
	lambdaVars          map[string]bool              // variable name -> is callable function (has signature)
	varTypes            map[string]string            // variable name -> type (for type tracking)
	stackSize           int                          // current stack size
	stackFrameSize      uint64                       // total stack frame size allocated in prologue
	stringCounter       int                          // counter for string labels
	stringInterns       map[string]string            // string value -> label (for string interning)
	labelCounter        int                          // counter for jump labels
	activeLoops         []ARM64LoopInfo              // stack of active loops for break/continue
	lambdaFuncs         []ARM64LambdaFunc            // list of lambda functions to generate
	lambdaCounter       int                          // counter for lambda names
	currentLambda       *ARM64LambdaFunc             // current lambda being compiled (for recursion)
	cConstants          map[string]*CHeaderConstants // C constants from imports
	currentArena        int                          // Arena depth (0=none, 1=first arena, 2=nested, etc.)
	usesArenas          bool                         // Track if program uses any arena blocks
	currentAssignName   string                       // Name of variable being assigned (for lambda self-reference)
	deferredExprs       [][]Expression               // Stack of deferred expressions per scope (LIFO order)
	globalSlots         map[string]int               // module-global var name -> slot index in the x28 globals array
	globalMutable       map[string]bool              // module-global var name -> is mutable
	cstructs            map[string]*CStructDecl      // cstruct name -> declaration (field layout)
	varCStructType      map[string]string            // variable name -> cstruct type it points at
	varListElemType     map[string]string            // list-variable name -> cstruct type of its elements (so `xs[i].field` works)
	funcReturnsListElem map[string]string            // lambda-var name -> cstruct elem type of the list its body returns
	closurePtrOffset    int32                        // frame offset where the current lambda's closure pointer (x9) is saved
	boxedVars           map[string]bool              // names that live in a heap cell in the current scope (captured-and-mutated locals)
	returnsClosure      map[string]bool              // lambda-var name -> its body evaluates to a closure (so `g = f(x); g()` invokes it)
	funcReturnsCStruct  map[string]string            // lambda-var name -> cstruct type its body returns (so `p = vadd(a,b)` is typed)
	fpDepth             int                          // expression-stack depth: BinaryExpr keeps its left operand in d24+fpDepth instead of spilling to memory, when the right operand has no call
}

// ARM64LambdaFunc represents a lambda function for ARM64
type ARM64LambdaFunc struct {
	Name              string
	Params            []string
	ParamCStructTypes map[string]string // param name -> cstruct type (from `(a as V)`)
	Body              Expression
	BodyStart         int      // Position where lambda body code starts (for tail recursion)
	FuncStart         int      // Position where function starts (including prologue, for recursion)
	VarName           string   // Variable name this lambda is assigned to (for recursion)
	IsRecursive       bool     // Whether this lambda calls itself recursively
	Captures          []string // Names captured from the enclosing scope (closure env order)
	// BoxedCaptures marks which of Captures are boxed: the env slot holds a heap
	// cell pointer (by reference) rather than a value, so the lambda can mutate
	// the captured variable and have it persist (the make_counter pattern).
	BoxedCaptures map[string]bool
}

// ARM64LoopInfo tracks information about an active loop
type ARM64LoopInfo struct {
	Label            int   // Loop label (@1, @2, @3, etc.)
	StartPos         int   // Code position of loop start (condition check)
	ContinuePos      int   // Code position for continue (increment step)
	EndPatches       []int // Positions that need to be patched to jump to loop end
	ContinuePatches  []int // Positions that need to be patched to jump to continue position
	IteratorOffset   int   // Stack offset for iterator variable
	IndexOffset      int   // Stack offset for index counter (list loops only)
	UpperBoundOffset int   // Stack offset for limit (range) or length (list)
	ListPtrOffset    int   // Stack offset for list pointer (list loops only)
	IsRangeLoop      bool  // True for range loops, false for list loops
}

// NewARM64CodeGen creates a new ARM64 code generator
func NewARM64CodeGen(eb *ExecutableBuilder, cConstants map[string]*CHeaderConstants) *ARM64CodeGen {
	// Use the target from ExecutableBuilder (which has the correct OS)
	return &ARM64CodeGen{
		out:                 &ARM64Out{out: NewOut(eb.target, eb.TextWriter(), eb)},
		eb:                  eb,
		stackVars:           make(map[string]int),
		mutableVars:         make(map[string]bool),
		lambdaVars:          make(map[string]bool),
		varTypes:            make(map[string]string),
		stackSize:           0,
		stringCounter:       0,
		stringInterns:       make(map[string]string),
		labelCounter:        0,
		cConstants:          cConstants,
		globalSlots:         make(map[string]int),
		globalMutable:       make(map[string]bool),
		cstructs:            make(map[string]*CStructDecl),
		varCStructType:      make(map[string]string),
		boxedVars:           make(map[string]bool),
		returnsClosure:      make(map[string]bool),
		funcReturnsCStruct:  make(map[string]string),
		varListElemType:     make(map[string]string),
		funcReturnsListElem: make(map[string]string),
	}
}

// cStructTypeOf returns the cstruct type name that an expression evaluates to, or
// "" if it is not a (known) cstruct value. Recognizes `as Struct` casts, struct
// constructors `Struct(...)`, calls to functions known to return a struct, and
// identifiers already tracked as struct-typed.
func (acg *ARM64CodeGen) cStructTypeOf(expr Expression) string {
	switch e := expr.(type) {
	case *CastExpr:
		if _, ok := acg.cstructs[e.Type]; ok {
			return e.Type
		}
	case *CallExpr:
		if _, ok := acg.cstructs[e.Function]; ok {
			return e.Function
		}
		if t, ok := acg.funcReturnsCStruct[e.Function]; ok {
			return t
		}
	case *IdentExpr:
		if t, ok := acg.varCStructType[e.Name]; ok {
			return t
		}
	case *BlockExpr:
		// A block (e.g. an inlined function body) evaluates to its last
		// expression — look through it so `r = { …; V(…) }` is typed as V.
		if n := len(e.Statements); n > 0 {
			switch s := e.Statements[n-1].(type) {
			case *ExpressionStmt:
				return acg.cStructTypeOf(s.Expr)
			case *JumpStmt:
				if s.Value != nil {
					return acg.cStructTypeOf(s.Value)
				}
			case *IfStmt:
				return acg.ifStmtCStructType(s)
			}
		}
	case *MatchExpr:
		// `if c { a } else { b }` / guard match: every arm yields the same type,
		// so the result is that of any arm that evaluates to a cstruct.
		for _, c := range e.Clauses {
			if c.Result != nil {
				if t := acg.cStructTypeOf(c.Result); t != "" {
					return t
				}
			}
		}
		if e.DefaultExpr != nil {
			return acg.cStructTypeOf(e.DefaultExpr)
		}
	case *FieldAccessExpr:
		// A nested cstruct-valued field, e.g. `b.c` where Ball has `c: V`.
		if decl := acg.cstructForExpr(e); decl != nil {
			return decl.Name
		}
	}
	return ""
}

// ifStmtToMatchExpr lowers an `if`/`elif`/`else` statement to the value-producing
// MatchExpr the parser would have built for the same condition in expression
// position. Used to give a trailing `if` (the last statement of a value block) its
// value, instead of falling through to a 0 result. Each branch body becomes a
// BlockExpr (evaluating to its last expression); the `else` becomes the default.
func ifStmtToMatchExpr(s *IfStmt) *MatchExpr {
	clauses := make([]*MatchClause, 0, len(s.Branches))
	for _, br := range s.Branches {
		clauses = append(clauses, &MatchClause{Guard: br.Condition, Result: &BlockExpr{Statements: br.Body}})
	}
	var def Expression = &NumberExpr{Value: 0.0}
	if len(s.ElseBody) > 0 {
		def = &BlockExpr{Statements: s.ElseBody}
	}
	return &MatchExpr{Condition: &NumberExpr{Value: 1.0}, Clauses: clauses, DefaultExpr: def}
}

// ifStmtCStructType returns the cstruct type a trailing `if`/`elif`/`else`
// statement evaluates to (the type of any arm's final value). A struct-returning
// function may end in such a statement (e.g. `V.norm`'s normalize-or-self), and
// without this its callers wouldn't know the result is a cstruct pointer.
func (acg *ARM64CodeGen) ifStmtCStructType(s *IfStmt) string {
	blockType := func(body []Statement) string {
		if n := len(body); n > 0 {
			switch st := body[n-1].(type) {
			case *ExpressionStmt:
				return acg.cStructTypeOf(st.Expr)
			case *JumpStmt:
				if st.Value != nil {
					return acg.cStructTypeOf(st.Value)
				}
			case *IfStmt:
				return acg.ifStmtCStructType(st)
			}
		}
		return ""
	}
	for _, br := range s.Branches {
		if t := blockType(br.Body); t != "" {
			return t
		}
	}
	return blockType(s.ElseBody)
}

// listElemCStructTypeOf infers the cstruct type of the ELEMENTS of a list-valued
// expression, or "". This lets `xs[i].field` resolve the field offset when xs is
// a list of cstructs (`[Ball(...), Ball(...)]`, a list-returning call, or a
// variable holding one).
func (acg *ARM64CodeGen) listElemCStructTypeOf(expr Expression) string {
	switch e := expr.(type) {
	case *ListExpr:
		if len(e.Elements) > 0 {
			return acg.cStructTypeOf(e.Elements[0])
		}
	case *IdentExpr:
		if t, ok := acg.varListElemType[e.Name]; ok {
			return t
		}
	case *CallExpr:
		if t, ok := acg.funcReturnsListElem[e.Function]; ok {
			return t
		}
	case *BlockExpr:
		if n := len(e.Statements); n > 0 {
			switch s := e.Statements[n-1].(type) {
			case *ExpressionStmt:
				return acg.listElemCStructTypeOf(s.Expr)
			case *JumpStmt:
				if s.Value != nil {
					return acg.listElemCStructTypeOf(s.Value)
				}
			}
		}
	}
	return ""
}

// lambdaReturnsListElemType returns the cstruct element type of the list a lambda
// body evaluates to, or "" — so `f = () -> [Ball(...), ...]` lets `bs = f()` know
// `bs[i]` is a Ball.
func (acg *ARM64CodeGen) lambdaReturnsListElemType(body Expression) string {
	last := body
	if b, ok := body.(*BlockExpr); ok {
		if len(b.Statements) == 0 {
			return ""
		}
		switch s := b.Statements[len(b.Statements)-1].(type) {
		case *ExpressionStmt:
			last = s.Expr
		case *JumpStmt:
			last = s.Value
		default:
			return ""
		}
	}
	if last == nil {
		return ""
	}
	return acg.listElemCStructTypeOf(last)
}

// lambdaReturnCStructType returns the cstruct type a lambda body evaluates to
// (its last expression), or "" — used so `vadd = (a,b)->{...Point(...)}` lets
// `p = vadd(a,b)` know p is a Point.
func (acg *ARM64CodeGen) lambdaReturnCStructType(body Expression) string {
	last := body
	if b, ok := body.(*BlockExpr); ok {
		if len(b.Statements) == 0 {
			return ""
		}
		// Pre-register the body's local cstruct types so a returned local
		// identifier (`r = …; r`) resolves even though the body has not been
		// compiled yet. Restore afterward so these locals don't leak globally.
		added := acg.registerBlockLocalCStructTypes(b.Statements)
		defer func() {
			for _, k := range added {
				delete(acg.varCStructType, k)
			}
		}()
		switch s := b.Statements[len(b.Statements)-1].(type) {
		case *ExpressionStmt:
			last = s.Expr
		case *JumpStmt:
			last = s.Value
		case *IfStmt:
			// A function may end in a bare `if`/`else` whose arms yield a cstruct.
			return acg.ifStmtCStructType(s)
		default:
			return ""
		}
	}
	if last == nil {
		return ""
	}
	return acg.cStructTypeOf(last)
}

// registerBlockLocalCStructTypes scans a block's statements in order and records
// the cstruct type of each struct-valued local into varCStructType (so later
// statements — and a trailing `return local` — can resolve it). It only adds names
// not already tracked, and returns the list of names it added so the caller can
// restore the map. It descends into `arena { … }` blocks, which wrap most
// struct-temporary scopes, but not control flow (a conditionally-defined local has
// no single static type).
func (acg *ARM64CodeGen) registerBlockLocalCStructTypes(stmts []Statement) []string {
	var added []string
	var scan func(ss []Statement)
	scan = func(ss []Statement) {
		for _, stmt := range ss {
			switch s := stmt.(type) {
			case *AssignStmt:
				if s.IsUpdate {
					continue
				}
				if _, exists := acg.varCStructType[s.Name]; exists {
					continue
				}
				if ct := acg.cStructTypeOf(s.Value); ct != "" {
					acg.varCStructType[s.Name] = ct
					added = append(added, s.Name)
				}
			case *ArenaStmt:
				scan(s.Body)
			case *WithStmt:
				scan(s.Body)
			}
		}
	}
	scan(stmts)
	return added
}

// isLambdaValue reports whether expr is a lambda literal in any of its forms.
func isLambdaValue(expr Expression) bool {
	switch expr.(type) {
	case *LambdaExpr, *PatternLambdaExpr, *MultiLambdaExpr:
		return true
	}
	return false
}

// lambdaBodyReturnsClosure reports whether a lambda whose body is `body`
// evaluates to a closure — its value (the body itself, or the last expression of
// a block) is a lambda. This lets `g = make_counter(0)` mark g as callable so
// `g()` invokes the returned closure rather than yielding its value.
func lambdaBodyReturnsClosure(body Expression) bool {
	if isLambdaValue(body) {
		return true
	}
	if blk, ok := body.(*BlockExpr); ok && len(blk.Statements) > 0 {
		switch s := blk.Statements[len(blk.Statements)-1].(type) {
		case *ExpressionStmt:
			return isLambdaValue(s.Expr)
		case *JumpStmt:
			return s.Value != nil && isLambdaValue(s.Value)
		}
	}
	return false
}

// markIfClosure records that variable `name` holds a callable closure when its
// initializer is a lambda, a call to a closure-returning function, or an alias of
// another closure variable. Tracking this is what makes a closure stored in a
// variable invocable with zero arguments (e.g. counter()).
func (acg *ARM64CodeGen) markIfClosure(name string, value Expression) {
	switch v := value.(type) {
	case *LambdaExpr:
		acg.lambdaVars[name] = true
		acg.returnsClosure[name] = lambdaBodyReturnsClosure(v.Body)
		if ct := acg.lambdaReturnCStructType(v.Body); ct != "" {
			acg.funcReturnsCStruct[name] = ct
		}
		if et := acg.lambdaReturnsListElemType(v.Body); et != "" {
			acg.funcReturnsListElem[name] = et
		}
	case *PatternLambdaExpr, *MultiLambdaExpr:
		acg.lambdaVars[name] = true
	case *CallExpr:
		if acg.returnsClosure[v.Function] {
			acg.lambdaVars[name] = true
		}
	case *IdentExpr:
		if acg.lambdaVars[v.Name] {
			acg.lambdaVars[name] = true
			acg.returnsClosure[name] = acg.returnsClosure[v.Name]
		}
	}
}

// boxedCaptureVars returns the names that the function whose body is `body` must
// heap-box: variables that some nested lambda updates via `<-` and captures from
// the enclosing scope. Boxing gives them a shared heap cell so a closure's
// mutation persists and is visible across calls (the canonical make_counter
// pattern). Read-only captures are unaffected (still captured by value).
func boxedCaptureVars(body Expression) map[string]bool {
	out := make(map[string]bool)
	forEachChildLambda(body, func(lam *LambdaExpr) {
		bound := make(map[string]bool)
		for _, p := range lam.Params {
			bound[p] = true
		}
		collectUpdatedFreeVars(lam.Body, bound, out)
	})
	return out
}

// forEachChildLambda walks expr and calls fn for each lambda nested within it,
// without descending into the lambdas' own bodies (so fn receives the immediate
// child lambdas; deeper nesting is handled by the recursion in
// collectUpdatedFreeVars, which tracks the correct bound names per level).
func forEachChildLambda(expr Expression, fn func(*LambdaExpr)) {
	switch e := expr.(type) {
	case *LambdaExpr:
		fn(e)
	case *BlockExpr:
		for _, stmt := range e.Statements {
			switch s := stmt.(type) {
			case *AssignStmt:
				forEachChildLambda(s.Value, fn)
			case *ExpressionStmt:
				forEachChildLambda(s.Expr, fn)
			case *JumpStmt:
				if s.Value != nil {
					forEachChildLambda(s.Value, fn)
				}
			}
		}
	case *BinaryExpr:
		forEachChildLambda(e.Left, fn)
		forEachChildLambda(e.Right, fn)
	case *CallExpr:
		for _, a := range e.Args {
			forEachChildLambda(a, fn)
		}
	case *MatchExpr:
		forEachChildLambda(e.Condition, fn)
		for _, c := range e.Clauses {
			if c.Guard != nil {
				forEachChildLambda(c.Guard, fn)
			}
			forEachChildLambda(c.Result, fn)
		}
		if e.DefaultExpr != nil {
			forEachChildLambda(e.DefaultExpr, fn)
		}
	case *JumpExpr:
		if e.Value != nil {
			forEachChildLambda(e.Value, fn)
		}
	}
}

// collectUpdatedFreeVars records, into out, the names that expr updates via `<-`
// that are not bound within expr (i.e. free variables it mutates). bound tracks
// names introduced by params and `:=` declarations as the walk descends; nested
// lambdas extend bound with their own params.
func collectUpdatedFreeVars(expr Expression, bound map[string]bool, out map[string]bool) {
	switch e := expr.(type) {
	case *BlockExpr:
		local := make(map[string]bool)
		maps.Copy(local, bound)
		for _, stmt := range e.Statements {
			switch s := stmt.(type) {
			case *AssignStmt:
				collectUpdatedFreeVars(s.Value, local, out)
				if s.IsUpdate {
					if !local[s.Name] {
						out[s.Name] = true
					}
				} else {
					local[s.Name] = true
				}
			case *ExpressionStmt:
				collectUpdatedFreeVars(s.Expr, local, out)
			case *JumpStmt:
				if s.Value != nil {
					collectUpdatedFreeVars(s.Value, local, out)
				}
			}
		}
	case *LambdaExpr:
		inner := make(map[string]bool)
		maps.Copy(inner, bound)
		for _, p := range e.Params {
			inner[p] = true
		}
		collectUpdatedFreeVars(e.Body, inner, out)
	case *BinaryExpr:
		collectUpdatedFreeVars(e.Left, bound, out)
		collectUpdatedFreeVars(e.Right, bound, out)
	case *CallExpr:
		for _, a := range e.Args {
			collectUpdatedFreeVars(a, bound, out)
		}
	case *MatchExpr:
		collectUpdatedFreeVars(e.Condition, bound, out)
		for _, c := range e.Clauses {
			if c.Guard != nil {
				collectUpdatedFreeVars(c.Guard, bound, out)
			}
			collectUpdatedFreeVars(c.Result, bound, out)
		}
		if e.DefaultExpr != nil {
			collectUpdatedFreeVars(e.DefaultExpr, bound, out)
		}
	case *JumpExpr:
		if e.Value != nil {
			collectUpdatedFreeVars(e.Value, bound, out)
		}
	}
}

// isCaptureName reports whether name is captured by the lambda currently being
// compiled (used to resolve multi-level captures).
func (acg *ARM64CodeGen) isCaptureName(name string) bool {
	if acg.currentLambda == nil {
		return false
	}
	return slices.Contains(acg.currentLambda.Captures, name)
}

// computeCaptures returns the enclosing-scope variables a lambda body references
// that must be captured by value into its closure env: free variables (excluding
// the lambda's own params) that are neither mutable module globals (shared via
// x28) nor the lambda's self-reference, and that are resolvable in the current
// scope (a stack variable, or itself a capture one level up). Sorted for
// deterministic env layout.
func (acg *ARM64CodeGen) computeCaptures(body Expression, params []string) []string {
	free := make(map[string]bool)
	collectCapturedVariables(body, params, free)
	var caps []string
	for name := range free {
		if _, isGlobal := acg.globalSlots[name]; isGlobal {
			continue
		}
		if name == acg.currentAssignName {
			continue
		}
		if _, inScope := acg.stackVars[name]; inScope {
			caps = append(caps, name)
		} else if acg.isCaptureName(name) {
			caps = append(caps, name)
		}
	}
	slices.Sort(caps)
	return caps
}

// arm64UnaryFPOps maps single-arg math functions to the ARM64 double-precision
// FP instruction (operating d0 -> d0) that computes them directly, avoiding a
// libm call. Encodings: fsqrt d0,d0 / fabs d0,d0 / frint{m,p,z,n} d0,d0.
var arm64UnaryFPOps = map[string]uint32{
	"sqrt":  0x1e61c000, // fsqrt d0, d0
	"fabs":  0x1e60c000, // fabs  d0, d0
	"abs":   0x1e60c000, // fabs  d0, d0 (Tim's float abs)
	"floor": 0x1e654000, // frintm d0, d0 (round toward -inf)
	"ceil":  0x1e64c000, // frintp d0, d0 (round toward +inf)
	"trunc": 0x1e65c000, // frintz d0, d0 (round toward zero)
	// `round` is intentionally NOT here: frintn is ties-to-even while C round() is
	// ties-away-from-zero, so it keeps using the libm call for exact semantics.
}

// emitFmovD emits `fmov d{dst}, d{src}` (register move between FP registers).
func (acg *ARM64CodeGen) emitFmovD(dst, src uint32) {
	instr := uint32(0x1e604000) | (src << 5) | dst
	acg.out.out.writer.WriteBytes([]byte{byte(instr), byte(instr >> 8), byte(instr >> 16), byte(instr >> 24)})
}

// exprHasCall conservatively reports whether compiling expr emits any `bl`
// (function call, libm, map access via a runtime helper, struct construction that
// may spill to malloc, match, etc.). It is used to decide whether the expression
// register stack (d24-d31, caller-saved) is safe to use: a call would clobber
// those registers, so when the right operand of a binary op has a call we fall
// back to a memory spill instead.
func (acg *ARM64CodeGen) exprHasCall(expr Expression) bool {
	switch e := expr.(type) {
	case *NumberExpr, *StringExpr, *IdentExpr:
		return false
	case *BinaryExpr:
		return acg.exprHasCall(e.Left) || acg.exprHasCall(e.Right)
	case *UnaryExpr:
		return acg.exprHasCall(e.Operand)
	case *CastExpr:
		return acg.exprHasCall(e.Expr)
	case *FMAExpr:
		return acg.exprHasCall(e.A) || acg.exprHasCall(e.B) || acg.exprHasCall(e.C)
	case *FieldAccessExpr:
		// `Ctor(...).field` folds to the field argument (no call there).
		if ctor, ok := e.Object.(*CallExpr); ok {
			if decl, isS := acg.cstructs[ctor.Function]; isS && len(ctor.Args) == len(decl.Fields) {
				for i := range decl.Fields {
					if decl.Fields[i].Name == e.FieldName {
						return acg.exprHasCall(ctor.Args[i])
					}
				}
			}
		}
		// A cstruct field is a plain typed load; a map field uses a helper call.
		if acg.cstructForExpr(e.Object) != nil {
			return acg.exprHasCall(e.Object)
		}
		return true
	default:
		return true
	}
}

// emitLoadBoxCellPtr loads the heap cell pointer of boxed variable `name` into
// the given GP register. The pointer lives either in the variable's stack slot
// (a boxed local) or in the closure env (a boxed capture, by reference).
func (acg *ARM64CodeGen) emitLoadBoxCellPtr(name, reg string) error {
	if so, ok := acg.stackVars[name]; ok && so != -1 {
		return acg.out.LdrImm64(reg, "x29", int32(16+so-8))
	}
	if acg.currentLambda != nil {
		if idx := slices.Index(acg.currentLambda.Captures, name); idx >= 0 {
			if err := acg.out.LdrImm64(reg, "x29", acg.closurePtrOffset); err != nil {
				return err
			}
			return acg.out.LdrImm64(reg, reg, int32(8+idx*8))
		}
	}
	return fmt.Errorf("boxed variable '%s' not found in scope", name)
}

// emitStoreBoxedVar stores the value currently in d0 through the heap cell of
// boxed variable `name` (used for `<-` updates of a boxed local or capture).
func (acg *ARM64CodeGen) emitStoreBoxedVar(name string) error {
	if err := acg.emitLoadBoxCellPtr(name, "x10"); err != nil {
		return err
	}
	return acg.out.StrImm64Double("d0", "x10", 0)
}

// emitBoxedDeclaration allocates a heap cell for a new boxed local, stores the
// value in d0 into it, and writes the cell pointer into the variable's stack
// slot at slotOffset (relative to x29). The slot then holds a shared pointer
// that nested closures capture by reference.
func (acg *ARM64CodeGen) emitBoxedDeclaration(slotOffset int32) error {
	// Spill the value across the malloc call (malloc may clobber d0).
	acg.out.SubImm64("sp", "sp", 16)
	if err := acg.out.StrImm64Double("d0", "sp", 0); err != nil {
		return err
	}
	if err := acg.out.MovImm64("x0", 8); err != nil {
		return err
	}
	if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
		return err
	}
	if err := acg.out.LdrImm64Double("d0", "sp", 0); err != nil {
		return err
	}
	acg.out.AddImm64("sp", "sp", 16)
	if err := acg.out.StrImm64Double("d0", "x0", 0); err != nil { // cell[0] = value
		return err
	}
	return acg.out.StrImm64("x0", "x29", slotOffset) // slot = cell pointer
}

// collectGlobals identifies module-level variables that are captured by a lambda
// and promotes them to globals stored in a heap array addressed via x28. This is
// how closures over module state (the common case: top-level `state`, counters,
// configuration) work on ARM64 — a captured global is a single shared slot that
// both the module body and any lambda read/write by reference. Per-invocation
// capture of an enclosing lambda's parameters (e.g. make_adder) is not handled
// here and remains a known limitation.
func (acg *ARM64CodeGen) collectGlobals(program *Program) {
	// The module body is the program's top-level statements; if the program is
	// auto-wrapped as `main = { ... }`, the block's statements are module-level
	// too. Collect both so captured module vars are found in either shape.
	var moduleStmts []Statement
	moduleStmts = append(moduleStmts, program.Statements...)
	for _, stmt := range program.Statements {
		if as, ok := stmt.(*AssignStmt); ok && as.Name == "main" {
			if lam, ok := as.Value.(*LambdaExpr); ok {
				if blk, ok := lam.Body.(*BlockExpr); ok {
					moduleStmts = append(moduleStmts, blk.Statements...)
				}
			}
		}
	}

	// Names that any lambda defined at module level captures from the enclosing
	// scope.
	captured := make(map[string]bool)
	for _, stmt := range moduleStmts {
		as, ok := stmt.(*AssignStmt)
		if !ok || as.IsUpdate {
			continue
		}
		if lam, ok := as.Value.(*LambdaExpr); ok {
			collectCapturedVariables(lam.Body, lam.Params, captured)
		}
	}

	// A module var that some lambda captures becomes a shared global slot
	// (captured by reference). Module-level lambdas may be compiled before the
	// vars they reference (statement ordering), so these can't be snapshotted by
	// value at definition the way an enclosing lambda's parameters are — the
	// global slot gives them a stable home reachable from any lambda body.
	for _, stmt := range moduleStmts {
		as, ok := stmt.(*AssignStmt)
		if !ok || as.IsUpdate {
			continue
		}
		if captured[as.Name] {
			if _, seen := acg.globalSlots[as.Name]; !seen {
				acg.globalSlots[as.Name] = len(acg.globalSlots)
				acg.globalMutable[as.Name] = as.Mutable
			}
		}
	}
}

// CompileProgram compiles a Tim program to ARM64
func (acg *ARM64CodeGen) CompileProgram(program *Program) error {
	// Register cstruct layouts up front so constructors, field access, and
	// return-type inference work regardless of statement order (the per-stmt
	// CStructDecl case still records any declared inline as well).
	maps.Copy(acg.cstructs, program.CStructs)

	// Initialize arena tracking
	acg.currentArena = 1 // Start at 1 to enable default global arena

	// Push defer scope for program-level defers
	acg.pushDeferScope()

	// PHASE 1: Compile program to calculate needed stack size
	// Save the current text buffer position to patch prologue later
	prologueStart := acg.eb.text.Len()

	// Emit placeholder prologue (we'll patch this later with correct stack size)
	// Reserve space for: sub sp, sp, #SIZE (4 bytes)
	acg.out.out.writer.WriteBytes([]byte{0xff, 0x43, 0x04, 0xd1}) // placeholder: sub sp, sp, #0x110
	// Save frame pointer and link register
	if err := acg.out.StrImm64("x29", "sp", 0); err != nil {
		return err
	}
	if err := acg.out.StrImm64("x30", "sp", 8); err != nil {
		return err
	}
	// Set frame pointer
	if err := acg.out.AddImm64("x29", "sp", 0); err != nil {
		return err
	}

	// Identify module-level globals captured by lambdas and, if any, allocate a
	// heap array for them whose base lives in x28 (callee-saved, so it survives
	// every call and is visible from lambda bodies). See collectGlobals.
	acg.collectGlobals(program)
	if len(acg.globalSlots) > 0 {
		if err := acg.out.MovImm64("x0", uint64(len(acg.globalSlots)*8)); err != nil {
			return err
		}
		if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
			return err
		}
		if err := acg.out.MovReg64("x28", "x0"); err != nil {
			return err
		}
	}

	// Bump arena for cstruct values: x27 = current top, x26 = limit. cstruct
	// constructors bump x27 instead of calling malloc, and `arena { }` frees a
	// scope's allocations by restoring x27. The buffer is overcommitted (pages are
	// lazy), so the reservation is cheap. x26/x27 are callee-saved, so they
	// survive every libc/SDL/lambda call. Only set up when the program uses
	// cstructs (the only bump-arena consumer).
	if len(program.CStructs) > 0 {
		if err := acg.out.MovImm64("x0", arenaBumpSize); err != nil {
			return err
		}
		if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
			return err
		}
		if err := acg.out.MovReg64("x27", "x0"); err != nil { // top = base
			return err
		}
		if err := acg.out.MovImm64("x1", arenaBumpSize); err != nil {
			return err
		}
		if err := acg.out.AddReg64("x26", "x27", "x1"); err != nil { // limit = base + size
			return err
		}
	}

	// Compile each statement
	for _, stmt := range program.Statements {
		if err := acg.compileStatement(stmt); err != nil {
			return err
		}
	}

	// Evaluate main (if it exists) to get the exit code
	// main can be a direct value (main = 42) or a function (main = { 42 })
	if _, exists := acg.stackVars["main"]; exists {
		// main exists - check if it's a lambda/function or a direct value
		if acg.lambdaVars["main"] {
			// main is a lambda/function - call it with no arguments
			if VerboseMode {
				debugf("DEBUG: Calling main function for exit code\n")
			}
			if err := acg.compileExpression(&CallExpr{Function: "main", Args: []Expression{}}); err != nil {
				return err
			}
		} else {
			// main is a direct value - just load it
			if VerboseMode {
				debugf("DEBUG: Loading main value for exit code\n")
			}
			if err := acg.compileExpression(&IdentExpr{Name: "main"}); err != nil {
				return err
			}
		}
		// Result is in d0 (float64)
		if VerboseMode {
			debugf("DEBUG: Main expression compiled, converting to int32\n")
		}
	} else {
		// No main - use exit code 0
		if VerboseMode {
			debugf("DEBUG: No main variable found, using exit code 0\n")
		}
		// fmov d0, xzr (d0 = 0.0)
		acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x67, 0x9e})
	}

	// Pop defer scope and execute deferred expressions
	if err := acg.popDeferScope(); err != nil {
		return err
	}

	// PHASE 2: Calculate actual stack frame size needed
	// Stack frame = 16 bytes (saved fp+lr) + acg.stackSize (local vars) + padding
	// Round up to 16-byte alignment
	if VerboseMode {
		debugf("DEBUG: CompileProgram finished, acg.stackSize = %d bytes\n", acg.stackSize)
	}
	actualStackSize := uint64((16 + acg.stackSize + 15) &^ 15)
	acg.stackFrameSize = actualStackSize
	if VerboseMode {
		debugf("DEBUG: Calculated actualStackSize = %d bytes (0x%x)\n", actualStackSize, actualStackSize)
	}

	// PHASE 3: Patch the prologue with correct stack size
	if actualStackSize > 0xFFF {
		// Stack frame too large for immediate encoding
		// This requires using a different instruction sequence
		if VerboseMode {
			fmt.Fprintf(os.Stderr, "Warning: Stack frame size %d exceeds 12-bit immediate limit\n", actualStackSize)
		}
		// For now, cap at maximum encodable value
		actualStackSize = 0xFFF
		acg.stackFrameSize = actualStackSize
	}

	// Patch the SUB sp instruction at prologueStart
	// ARM64 SUB immediate encoding: 0xd10003ff | (imm12 << 10)
	textBytes := acg.eb.text.Bytes()
	subInstr := uint32(0xd10003ff) | (uint32(actualStackSize) << 10)
	textBytes[prologueStart] = byte(subInstr)
	textBytes[prologueStart+1] = byte(subInstr >> 8)
	textBytes[prologueStart+2] = byte(subInstr >> 16)
	textBytes[prologueStart+3] = byte(subInstr >> 24)

	// Function epilogue (if no explicit exit)
	// Convert d0 (float64 result from main) to w0 (int32 exit code)
	// fcvtzs w0, d0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x1e})

	// For static Linux builds, exit with syscall instead of returning
	if acg.eb.target.OS() == OSLinux && !acg.eb.useDynamicLinking {
		// mov x8, #93 (sys_exit on ARM64 Linux)
		acg.out.out.writer.WriteBytes([]byte{0xa8, 0x0b, 0x80, 0xd2})
		// svc #0
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0xd4})
		// Don't return here - continue to generate lambdas and helpers
	} else {
		// Dynamic builds or macOS: restore frame and return
		if err := acg.out.LdrImm64("x30", "sp", 8); err != nil {
			return err
		}
		if err := acg.out.LdrImm64("x29", "sp", 0); err != nil {
			return err
		}
		if err := acg.out.AddImm64("sp", "sp", uint32(acg.stackFrameSize)); err != nil {
			return err
		}
		if err := acg.out.Return("x30"); err != nil {
			return err
		}
	}

	if VerboseMode {
		debugf("DEBUG: About to generate lambda functions (count=%d)\n", len(acg.lambdaFuncs))
	}

	// Generate lambda functions after main program
	if err := acg.generateLambdaFunctions(); err != nil {
		return err
	}

	if VerboseMode {
		debugf("DEBUG: Finished generating lambda functions\n")
	}

	// Generate runtime helper functions
	if err := acg.generateRuntimeHelpers(); err != nil {
		return err
	}

	if VerboseMode {
		debugf("DEBUG: CompileProgram completed successfully\n")
	}

	return nil
}

// compileStatement compiles a single statement
func (acg *ARM64CodeGen) compileStatement(stmt Statement) error {
	switch s := stmt.(type) {
	case *ExpressionStmt:
		// Handle PostfixExpr as a statement (x++, x--)
		if postfix, ok := s.Expr.(*PostfixExpr); ok {
			return acg.compilePostfixStmt(postfix)
		}
		return acg.compileExpression(s.Expr)
	case *AssignStmt:
		return acg.compileAssignment(s)
	case *LoopStmt:
		return acg.compileLoopStatement(s)
	case *WhileStmt:
		return acg.compileWhileStatement(s)
	case *CStructDecl:
		// Cstruct declarations generate no runtime code, but we record the field
		// layout so field access (p.x) and indexed field writes (p[i] <- v) can
		// emit typed loads/stores at the right offsets.
		acg.cstructs[s.Name] = s
		return nil
	case *CImportStmt:
		// C imports are handled at compile-time to populate cConstants
		// No runtime code generation needed
		return nil
	case *ArenaStmt:
		return acg.compileArenaStmt(s)
	case *WithStmt:
		// A with-block is transparent at runtime: the subject was already
		// injected into each body call at parse time, so just run the body.
		for _, bodyStmt := range s.Body {
			if err := acg.compileStatement(bodyStmt); err != nil {
				return err
			}
		}
		return nil
	case *DeferStmt:
		// Defer statement: collect for execution at scope exit
		if len(acg.deferredExprs) == 0 {
			return fmt.Errorf("defer can only be used inside a function or block scope")
		}
		currentScope := len(acg.deferredExprs) - 1
		acg.deferredExprs[currentScope] = append(acg.deferredExprs[currentScope], s.Call)
		return nil
	case *SpawnStmt:
		// Process spawning with fork()
		// Full implementation needs process management
		return fmt.Errorf("spawn statements not yet implemented in ARM64 (requires fork/exec support)")
	case *RegisterAssignStmt:
		// Register assignment in unsafe blocks
		return acg.compileRegisterAssignment(s)
	case *MemoryStore:
		// Memory store operation in unsafe blocks
		return acg.compileMemoryStore(s)
	case *SyscallStmt:
		// System call instruction
		// Registers must be set up before calling syscall:
		// ARM64: x8=syscall#, x0-x6=args
		// svc #0 instruction
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0xd4}) // svc #0
		return nil
	case *JumpStmt:
		return acg.compileJumpStatement(s)
	case *MapUpdateStmt:
		return acg.compileMapUpdate(s)
	case *IfStmt:
		return acg.compileIfStatement(s)
	case *MultipleAssignStmt:
		return acg.compileMultipleAssign(s)
	default:
		return fmt.Errorf("unsupported statement type for ARM64: %T", stmt)
	}
}

// compileBitCount compiles the popcount/clz/ctz bit builtins. The argument is
// converted from float64 to a 64-bit integer, the bit operation is applied, and
// the integer result is converted back to float64.
func (acg *ARM64CodeGen) compileBitCount(call *CallExpr, op string) error {
	if len(call.Args) != 1 {
		return fmt.Errorf("%s requires exactly 1 argument", op)
	}
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil {
		return err
	}
	switch op {
	case "popcount":
		if err := acg.out.FmovGPToDouble("d1", "x0"); err != nil {
			return err
		}
		if err := acg.out.CntVec8b("d1", "d1"); err != nil {
			return err
		}
		if err := acg.out.AddvBytes8b("d1", "d1"); err != nil {
			return err
		}
		if err := acg.out.FmovSingleToGP("x0", "d1"); err != nil {
			return err
		}
	case "clz":
		if err := acg.out.Clz64("x0", "x0"); err != nil {
			return err
		}
	case "ctz":
		if err := acg.out.Rbit64("x0", "x0"); err != nil {
			return err
		}
		if err := acg.out.Clz64("x0", "x0"); err != nil {
			return err
		}
	}
	return acg.out.ScvtfInt64ToDouble("d0", "x0")
}

// compileIfStatement compiles an if / elif / else chain. Each branch's
// condition is treated as a boolean: zero is false, anything else is true.
func (acg *ARM64CodeGen) compileIfStatement(stmt *IfStmt) error {
	var endJumps []int
	for _, branch := range stmt.Branches {
		if err := acg.compileExpression(branch.Condition); err != nil {
			return err
		}
		// Skip the body when the condition is 0.0.
		acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x67, 0x9e}) // fmov d1, xzr
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e}) // fcmp d0, d1
		nextJump := acg.eb.text.Len()
		acg.out.BranchCond("eq", 0)

		for _, s := range branch.Body {
			if err := acg.compileStatement(s); err != nil {
				return err
			}
		}
		endJump := acg.eb.text.Len()
		acg.out.Branch(0)
		endJumps = append(endJumps, endJump)

		nextPos := acg.eb.text.Len()
		acg.patchJumpOffset(nextJump, int32(nextPos-nextJump))
	}

	for _, s := range stmt.ElseBody {
		if err := acg.compileStatement(s); err != nil {
			return err
		}
	}

	endPos := acg.eb.text.Len()
	for _, j := range endJumps {
		acg.patchJumpOffset(j, int32(endPos-j))
	}
	return nil
}

// compileMapUpdate compiles an in-place element update: name[index] <- value.
// Lists/maps are laid out as [count(8)][elem0(8)][elem1(8)]..., so element i
// lives at base + 8 + i*8 (matching the IndexExpr read path).
func (acg *ARM64CodeGen) compileMapUpdate(stmt *MapUpdateStmt) error {
	// C-struct indexed field write: p[i] <- v writes field i (by declaration
	// order) as raw typed memory. The index must be a constant.
	if decl := acg.cstructForExpr(&IdentExpr{Name: stmt.MapName}); decl != nil {
		idxExpr := stmt.Index
		if cast, ok := idxExpr.(*CastExpr); ok {
			idxExpr = cast.Expr
		}
		num, ok := idxExpr.(*NumberExpr)
		if !ok {
			return fmt.Errorf("cstruct field write requires a constant index")
		}
		fi := int(num.Value)
		if fi < 0 || fi >= len(decl.Fields) {
			return fmt.Errorf("cstruct %s has no field index %d", decl.Name, fi)
		}
		// Evaluate value -> d0, spill; load pointer -> x9; store.
		if err := acg.compileExpression(stmt.Value); err != nil {
			return err
		}
		acg.out.SubImm64("sp", "sp", 16)
		if err := acg.out.StrImm64Double("d0", "sp", 0); err != nil {
			return err
		}
		if err := acg.compileExpression(&IdentExpr{Name: stmt.MapName}); err != nil {
			return err
		}
		acg.out.out.writer.WriteBytes([]byte{0x09, 0x00, 0x78, 0x9e}) // fcvtzs x9, d0 (numeric ptr)
		if err := acg.out.LdrImm64Double("d0", "sp", 0); err != nil {
			return err
		}
		acg.out.AddImm64("sp", "sp", 16)
		return acg.emitCStructFieldStore(&decl.Fields[fi])
	}

	// Evaluate the new value into d0 and stash it.
	if err := acg.compileExpression(stmt.Value); err != nil {
		return err
	}
	acg.out.SubImm64("sp", "sp", 16)
	if err := acg.out.StrImm64Double("d0", "sp", 0); err != nil {
		return err
	}

	// Load the container pointer (variable holds it as a float64) into x0, stash.
	if err := acg.compileExpression(&IdentExpr{Name: stmt.MapName}); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e}) // fcvtzs x0, d0
	acg.out.SubImm64("sp", "sp", 16)
	if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
		return err
	}

	// Evaluate the index into x1.
	if err := acg.compileExpression(stmt.Index); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x78, 0x9e}) // fcvtzs x1, d0

	// Restore container pointer (x0); value is now at [sp,0].
	acg.out.LdrImm64("x0", "sp", 0)
	acg.out.AddImm64("sp", "sp", 16)

	// Maps/strings (key,value pairs) update by key; lists update by flat index.
	updType := acg.getExprType(&IdentExpr{Name: stmt.MapName})
	if updType == "map" || updType == "string" {
		// x7 = address of the value slot for key x1 (0 if the key is absent).
		if err := acg.emitMapValueSlotAddr(); err != nil {
			return err
		}
		if err := acg.out.LdrImm64Double("d0", "sp", 0); err != nil {
			return err
		}
		acg.out.AddImm64("sp", "sp", 16)
		// Skip the store when the key was not found.
		skipPos := acg.eb.text.Len()
		acg.out.out.writer.WriteBytes([]byte{0x07, 0x00, 0x00, 0xb4}) // cbz x7, +0
		acg.out.out.writer.WriteBytes([]byte{0xe0, 0x00, 0x00, 0xfd}) // str d0, [x7]
		skipLabel := acg.eb.text.Len()
		acg.patchJumpOffset(skipPos, int32(skipLabel-skipPos))
		return nil
	}

	// List: element address = x0 + 8 + index*8.
	acg.out.AddImm64("x0", "x0", 8)
	acg.out.out.writer.WriteBytes([]byte{0x21, 0xf0, 0x7d, 0xd3}) // lsl x1, x1, #3
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x01, 0x8b}) // add x0, x0, x1

	// Restore the value and store it.
	if err := acg.out.LdrImm64Double("d0", "sp", 0); err != nil {
		return err
	}
	acg.out.AddImm64("sp", "sp", 16)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0xfd}) // str d0, [x0]
	return nil
}

// cstructForExpr returns the cstruct declaration for an expression that holds a
// pointer to one (a cstruct-typed variable, or an `as Struct` cast), else nil.
func (acg *ARM64CodeGen) cstructForExpr(expr Expression) *CStructDecl {
	switch e := expr.(type) {
	case *IdentExpr:
		if name, ok := acg.varCStructType[e.Name]; ok {
			return acg.cstructs[name]
		}
	case *CastExpr:
		if decl, ok := acg.cstructs[e.Type]; ok {
			return decl
		}
	case *IndexExpr:
		// `xs[i]` where xs is a known list of cstructs: the element is a pointer to
		// that cstruct, so `xs[i].field` reads at the field offset.
		if t := acg.listElemCStructTypeOf(e.List); t != "" {
			return acg.cstructs[t]
		}
	case *FieldAccessExpr:
		// A nested cstruct-valued field used as a receiver: `b.c.x` where `b.c` is
		// itself a cstruct (V). Resolve the outer object's struct, find the field,
		// and return the field's own struct declaration.
		if decl := acg.cstructForExpr(e.Object); decl != nil {
			for i := range decl.Fields {
				if decl.Fields[i].Name == e.FieldName && decl.Fields[i].StructName != "" {
					return acg.cstructs[decl.Fields[i].StructName]
				}
			}
		}
	case *CallExpr:
		// A call result used directly as a receiver: `mk(...).x`. A constructor
		// yields its own struct; any other call yields the struct its function is
		// known to return. This is what lets `f(...).field` work without first
		// binding the result to a typed local.
		if decl, ok := acg.cstructs[e.Function]; ok {
			return decl
		}
		if t, ok := acg.funcReturnsCStruct[e.Function]; ok {
			return acg.cstructs[t]
		}
	}
	return nil
}

// emitLDST emits a single scaled load/store with base register x9 and Rt=0
// (x0/w0 or d0/s0 depending on baseOp). baseOp is the opcode with imm/Rn/Rt=0.
func (acg *ARM64CodeGen) emitLDST(baseOp uint32, scale, off int) {
	imm := uint32(off/scale) & 0xfff
	instr := baseOp | (imm << 10) | (9 << 5)
	acg.out.out.writer.WriteBytes([]byte{byte(instr), byte(instr >> 8), byte(instr >> 16), byte(instr >> 24)})
}

// emitCStructFieldLoad loads the field at its offset (pointer in x9) and leaves
// the value in d0 (numeric, or pointer-bits for ptr/cstr fields).
func (acg *ARM64CodeGen) emitCStructFieldLoad(f *CStructField) error {
	w := acg.out.out.writer
	off := f.Offset
	ucvtf := []byte{0x00, 0x00, 0x63, 0x9e} // ucvtf d0, x0
	scvtf := []byte{0x00, 0x00, 0x62, 0x9e} // scvtf d0, x0
	switch f.Type {
	case "uint8":
		acg.emitLDST(0x39400000, 1, off) // ldrb w0
		w.WriteBytes(ucvtf)
	case "int8":
		acg.emitLDST(0x39800000, 1, off) // ldrsb x0
		w.WriteBytes(scvtf)
	case "uint16":
		acg.emitLDST(0x79400000, 2, off) // ldrh w0
		w.WriteBytes(ucvtf)
	case "int16":
		acg.emitLDST(0x79800000, 2, off) // ldrsh x0
		w.WriteBytes(scvtf)
	case "uint32":
		acg.emitLDST(0xb9400000, 4, off) // ldr w0
		w.WriteBytes(ucvtf)
	case "int32":
		acg.emitLDST(0xb9800000, 4, off) // ldrsw x0
		w.WriteBytes(scvtf)
	case "uint64":
		acg.emitLDST(0xf9400000, 8, off) // ldr x0
		w.WriteBytes(ucvtf)
	case "int64":
		acg.emitLDST(0xf9400000, 8, off) // ldr x0
		w.WriteBytes(scvtf)
	case "float32":
		acg.emitLDST(0xbd400000, 4, off)             // ldr s0
		w.WriteBytes([]byte{0x00, 0xc0, 0x22, 0x1e}) // fcvt d0, s0
	case "float64":
		acg.emitLDST(0xfd400000, 8, off) // ldr d0
	case "cstr", "ptr":
		acg.emitLDST(0xf9400000, 8, off)             // ldr x0
		w.WriteBytes([]byte{0x00, 0x00, 0x67, 0x9e}) // fmov d0, x0 (keep bits)
	default:
		return fmt.Errorf("unsupported cstruct field type for ARM64 load: %s", f.Type)
	}
	return nil
}

// emitCStructFieldStore stores d0 into the field at its offset (pointer in x9),
// converting d0 to the field's width/type.
func (acg *ARM64CodeGen) emitCStructFieldStore(f *CStructField) error {
	w := acg.out.out.writer
	off := f.Offset
	fcvtzuW := []byte{0x00, 0x00, 0x79, 0x1e} // fcvtzu w0, d0
	fcvtzsW := []byte{0x00, 0x00, 0x78, 0x1e} // fcvtzs w0, d0
	fcvtzuX := []byte{0x00, 0x00, 0x79, 0x9e} // fcvtzu x0, d0
	fcvtzsX := []byte{0x00, 0x00, 0x78, 0x9e} // fcvtzs x0, d0
	switch f.Type {
	case "uint8":
		w.WriteBytes(fcvtzuW)
		acg.emitLDST(0x39000000, 1, off) // strb w0
	case "int8":
		w.WriteBytes(fcvtzsW)
		acg.emitLDST(0x39000000, 1, off)
	case "uint16":
		w.WriteBytes(fcvtzuW)
		acg.emitLDST(0x79000000, 2, off) // strh w0
	case "int16":
		w.WriteBytes(fcvtzsW)
		acg.emitLDST(0x79000000, 2, off)
	case "uint32":
		w.WriteBytes(fcvtzuW)
		acg.emitLDST(0xb9000000, 4, off) // str w0
	case "int32":
		w.WriteBytes(fcvtzsW)
		acg.emitLDST(0xb9000000, 4, off)
	case "uint64":
		w.WriteBytes(fcvtzuX)
		acg.emitLDST(0xf9000000, 8, off) // str x0
	case "int64":
		w.WriteBytes(fcvtzsX)
		acg.emitLDST(0xf9000000, 8, off)
	case "float32":
		w.WriteBytes([]byte{0x00, 0x40, 0x62, 0x1e}) // fcvt s0, d0
		acg.emitLDST(0xbd000000, 4, off)             // str s0
	case "float64":
		acg.emitLDST(0xfd000000, 8, off) // str d0
	case "cstr", "ptr":
		w.WriteBytes([]byte{0x00, 0x00, 0x66, 0x9e}) // fmov x0, d0 (bits)
		acg.emitLDST(0xf9000000, 8, off)             // str x0
	default:
		return fmt.Errorf("unsupported cstruct field type for ARM64 store: %s", f.Type)
	}
	return nil
}

// arenaBumpSize is the size of the cstruct bump-arena buffer reserved at startup
// (1 GiB). It is overcommitted, so only pages actually touched cost memory; it
// must comfortably hold the live allocations of one `arena { }` scope.
const arenaBumpSize = 0x40000000

// emitBumpAlloc allocates `size` bytes (rounded up to 16) from the x27 bump arena,
// leaving the pointer in x0. On overflow it falls back to malloc (so correctness
// never depends on the arena being large enough — only memory reuse does).
func (acg *ARM64CodeGen) emitBumpAlloc(size int) error {
	rounded := (size + 15) &^ 15
	if err := acg.out.MovReg64("x0", "x27"); err != nil { // x0 = current top
		return err
	}
	if rounded <= 4095 {
		if err := acg.out.AddImm64("x1", "x27", uint32(rounded)); err != nil {
			return err
		}
	} else {
		if err := acg.out.MovImm64("x1", uint64(rounded)); err != nil {
			return err
		}
		if err := acg.out.AddReg64("x1", "x27", "x1"); err != nil {
			return err
		}
	}
	// cmp x1, x26  (subs xzr, x1, x26)
	cmp := uint32(0xEB000000) | (26 << 16) | (1 << 5) | 31
	acg.out.out.writer.WriteBytes([]byte{byte(cmp), byte(cmp >> 8), byte(cmp >> 16), byte(cmp >> 24)})
	fbJump := acg.eb.text.Len()
	if err := acg.out.BranchCond("hi", 0); err != nil { // overflow -> fall back to malloc
		return err
	}
	if err := acg.out.MovReg64("x27", "x1"); err != nil { // commit new top
		return err
	}
	doneJump := acg.eb.text.Len()
	if err := acg.out.Branch(0); err != nil {
		return err
	}
	fbPos := acg.eb.text.Len()
	acg.patchJumpOffset(fbJump, int32(fbPos-fbJump))
	if err := acg.out.MovImm64("x0", uint64(size)); err != nil {
		return err
	}
	if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
		return err
	}
	donePos := acg.eb.text.Len()
	acg.patchJumpOffset(doneJump, int32(donePos-doneJump))
	return nil
}

// pureBaseExprEqual reports whether two expressions are syntactically equal AND
// side-effect-free (so one may be compiled in place of the other, or compiled
// once and reused). Only the shapes that appear as a vec-arithmetic operand base
// are handled — identifiers, nested field reads, numeric/`self` literals.
func pureBaseExprEqual(a, b Expression) bool {
	switch ae := a.(type) {
	case *IdentExpr:
		be, ok := b.(*IdentExpr)
		return ok && ae.Name == be.Name
	case *FieldAccessExpr:
		be, ok := b.(*FieldAccessExpr)
		return ok && ae.FieldName == be.FieldName && pureBaseExprEqual(ae.Object, be.Object)
	case *NumberExpr:
		be, ok := b.(*NumberExpr)
		return ok && ae.Value == be.Value
	}
	return false
}

// isPureVecBase reports whether an expression is a side-effect-free struct-valued
// operand base (safe to compile to a pointer without double-evaluating a call).
func isPureVecBase(e Expression) bool {
	switch e.(type) {
	case *IdentExpr, *FieldAccessExpr:
		return true
	}
	return false
}

// tryCompileNEONVec3 recognizes a 3-field f64 cstruct constructor whose arguments
// are an element-wise binary op over the fields of one or two operand structs and
// emits NEON: the x/y lanes are computed with a single .2D op, z stays scalar.
// Returns (true, _) when it emitted code; (false, nil) to fall back to the scalar
// constructor. It only fires when operand layouts are statically known to match
// `decl` (so the 128-bit load of lanes x,y is sound).
func (acg *ARM64CodeGen) tryCompileNEONVec3(decl *CStructDecl, args []Expression) (bool, error) {
	// Exactly three contiguous f64 fields at offsets 0, 8, 16.
	if len(decl.Fields) != 3 {
		return false, nil
	}
	for i, f := range decl.Fields {
		if (f.Type != "float64" && f.Type != "f64") || f.Offset != i*8 {
			return false, nil
		}
	}

	// Every argument must be `<recv>.field_i <op> <rhs_i>` with the same operator.
	op := ""
	lefts := make([]*FieldAccessExpr, 3)
	rights := make([]Expression, 3)
	for i, a := range args {
		be, ok := a.(*BinaryExpr)
		if !ok || (be.Operator != "+" && be.Operator != "-" && be.Operator != "*") {
			return false, nil
		}
		if op == "" {
			op = be.Operator
		} else if be.Operator != op {
			return false, nil
		}
		fa, ok := be.Left.(*FieldAccessExpr)
		if !ok || fa.FieldName != decl.Fields[i].Name || !isPureVecBase(fa.Object) {
			// `*` may also be written `s * v.field`; try the mirrored shape.
			fa, ok = be.Right.(*FieldAccessExpr)
			if op != "*" || !ok || fa.FieldName != decl.Fields[i].Name || !isPureVecBase(fa.Object) {
				return false, nil
			}
			lefts[i], rights[i] = fa, be.Left
		} else {
			lefts[i], rights[i] = fa, be.Right
		}
	}

	// The left operand base (the receiver) must be identical across lanes and have
	// `decl`'s layout, so loading lanes x,y as one 128-bit vector is correct.
	baseA := lefts[0].Object
	for i := 1; i < 3; i++ {
		if !pureBaseExprEqual(baseA, lefts[i].Object) {
			return false, nil
		}
	}
	if acg.cStructTypeOf(baseA) != decl.Name {
		return false, nil
	}

	// Determine the right-hand side: either element-wise (`b.field_i`, base B of
	// `decl`'s type) or a broadcast scalar (the same value across all lanes).
	rhsIsField := false
	if rfa, ok := rights[0].(*FieldAccessExpr); ok && rfa.FieldName == decl.Fields[0].Name && isPureVecBase(rfa.Object) {
		rhsIsField = true
	}
	var baseB Expression
	var scalarS Expression
	if rhsIsField {
		for i := range 3 {
			rfa, ok := rights[i].(*FieldAccessExpr)
			if !ok || rfa.FieldName != decl.Fields[i].Name || !isPureVecBase(rfa.Object) {
				return false, nil
			}
		}
		baseB = rights[0].(*FieldAccessExpr).Object
		for i := 1; i < 3; i++ {
			if !pureBaseExprEqual(baseB, rights[i].(*FieldAccessExpr).Object) {
				return false, nil
			}
		}
		if acg.cStructTypeOf(baseB) != decl.Name {
			return false, nil
		}
	} else {
		// Broadcast-scalar (scale): only valid for `*`, with the same scalar value
		// on every lane and the scalar not itself a struct field of decl.
		if op != "*" {
			return false, nil
		}
		scalarS = rights[0]
		for i := 1; i < 3; i++ {
			if !pureBaseExprEqual(scalarS, rights[i]) {
				return false, nil
			}
		}
	}

	return true, acg.emitNEONVec3(decl, op, baseA, baseB, scalarS)
}

// emitNEONVec3 emits the NEON body recognized by tryCompileNEONVec3: it allocates
// the result struct, computes lanes x,y with one .2D op and lane z scalar, and
// leaves the result pointer (as a numeric double) in d0.
func (acg *ARM64CodeGen) emitNEONVec3(decl *CStructDecl, op string, baseA, baseB, scalarS Expression) error {
	vecOp := map[string]func(d, a, b string) error{
		"+": acg.out.FaddV2D, "-": acg.out.FsubV2D, "*": acg.out.FmulV2D,
	}[op]
	sclOp := map[string]func(d, a, b string) error{
		"+": acg.out.FaddScalar64, "-": acg.out.FsubScalar64, "*": acg.out.FmulScalar64,
	}[op]

	// Reserve a 16-byte spill slot for operand pointers / the scalar.
	acg.out.SubImm64("sp", "sp", 16)

	// ptrA = &baseA, spilled to [sp, 0].
	if err := acg.compileExpression(baseA); err != nil { // d0 = pointer (float bits)
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x9", "d0"); err != nil {
		return err
	}
	if err := acg.out.StrImm64("x9", "sp", 0); err != nil {
		return err
	}

	if baseB != nil {
		if err := acg.compileExpression(baseB); err != nil { // d0 = pointer
			return err
		}
		if err := acg.out.FcvtzsDoubleToInt64("x9", "d0"); err != nil {
			return err
		}
		if err := acg.out.StrImm64("x9", "sp", 8); err != nil {
			return err
		}
	} else {
		if err := acg.compileExpression(scalarS); err != nil { // d0 = scalar value
			return err
		}
		if err := acg.out.StrImm64Double("d0", "sp", 8); err != nil {
			return err
		}
	}

	// Allocate the result struct (pointer in x0); may call malloc, so operands are
	// reloaded from the spill slot afterward.
	if err := acg.emitBumpAlloc(decl.Size); err != nil {
		return err
	}
	if err := acg.out.LdrImm64("x9", "sp", 0); err != nil { // x9 = ptrA
		return err
	}
	if err := acg.out.LdrQ("v0", "x9", 0); err != nil { // A.x, A.y
		return err
	}
	if err := acg.out.LdrImm64Double("d3", "x9", 16); err != nil { // A.z
		return err
	}
	if baseB != nil {
		if err := acg.out.LdrImm64("x10", "sp", 8); err != nil { // x10 = ptrB
			return err
		}
		if err := acg.out.LdrQ("v1", "x10", 0); err != nil { // B.x, B.y
			return err
		}
		if err := acg.out.LdrImm64Double("d4", "x10", 16); err != nil { // B.z
			return err
		}
	} else {
		if err := acg.out.LdrImm64Double("d4", "sp", 8); err != nil { // scalar
			return err
		}
		if err := acg.out.DupV2D("v1", "d4"); err != nil { // broadcast to both lanes
			return err
		}
	}
	if err := vecOp("v2", "v0", "v1"); err != nil {
		return err
	}
	if err := acg.out.StrQ("v2", "x0", 0); err != nil { // result.x, result.y
		return err
	}
	if err := sclOp("d3", "d3", "d4"); err != nil {
		return err
	}
	if err := acg.out.StrImm64Double("d3", "x0", 16); err != nil { // result.z
		return err
	}
	acg.out.AddImm64("sp", "sp", 16)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0 (numeric ptr)
	return nil
}

// compileCStructConstructor compiles a cstruct value constructor, e.g.
// `Point(3.0, 4.0)`: allocate the struct, store each argument into its field,
// and yield a numeric pointer to the value (the convention cstruct field access
// expects). A field whose type is itself a cstruct receives another struct
// pointer (stored as an 8-byte pointer), so nested structs compose.
func (acg *ARM64CodeGen) compileCStructConstructor(decl *CStructDecl, args []Expression) error {
	if len(args) != len(decl.Fields) {
		return fmt.Errorf("%s constructor expects %d arguments, got %d", decl.Name, len(decl.Fields), len(args))
	}
	// NEON fast path for element-wise vec3 arithmetic (`V(a.x±b.x, ...)`,
	// `V(a.x*s, ...)`): processes the x/y pair with one .2D instruction.
	if handled, err := acg.tryCompileNEONVec3(decl, args); err != nil {
		return err
	} else if handled {
		return nil
	}
	size := decl.Size
	if size <= 0 {
		size = len(decl.Fields) * 8
	}
	// Allocate the struct value from the bump arena (freed by the enclosing
	// `arena { }`), falling back to malloc on overflow.
	if err := acg.emitBumpAlloc(size); err != nil {
		return err
	}
	// Spill the struct pointer so it survives compiling each field value.
	acg.out.SubImm64("sp", "sp", 16)
	if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
		return err
	}
	for i := range decl.Fields {
		if err := acg.compileExpression(args[i]); err != nil { // value in d0
			return err
		}
		if err := acg.out.LdrImm64("x9", "sp", 0); err != nil { // struct pointer
			return err
		}
		if err := acg.emitCStructFieldStore(&decl.Fields[i]); err != nil {
			return err
		}
	}
	if err := acg.out.LdrImm64("x0", "sp", 0); err != nil {
		return err
	}
	acg.out.AddImm64("sp", "sp", 16)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0 (numeric ptr)
	return nil
}

// emitMapValueSlotAddr searches a map/string for key x1 (pointer in x0) and
// leaves the address of its value slot in x7, or 0 if the key is absent. Same
// layout as emitMapKeyLookup.
func (acg *ARM64CodeGen) emitMapValueSlotAddr() error {
	if err := acg.out.LdrImm64Double("d1", "x0", 0); err != nil { // count
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x2", "d1"); err != nil {
		return err
	}
	if err := acg.out.MovImm64("x3", 0); err != nil { // i
		return err
	}
	loopStart := acg.eb.text.Len()
	if err := acg.out.CmpReg64("x3", "x2"); err != nil {
		return err
	}
	notFoundJump := acg.eb.text.Len()
	acg.out.BranchCond("ge", 0)
	if err := acg.out.LslImm64("x5", "x3", 4); err != nil {
		return err
	}
	if err := acg.out.AddReg64("x4", "x0", "x5"); err != nil {
		return err
	}
	if err := acg.out.AddImm64("x4", "x4", 8); err != nil { // &key[i]
		return err
	}
	if err := acg.out.LdrImm64Double("d1", "x4", 0); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x6", "d1"); err != nil {
		return err
	}
	if err := acg.out.CmpReg64("x6", "x1"); err != nil {
		return err
	}
	foundJump := acg.eb.text.Len()
	acg.out.BranchCond("eq", 0)
	if err := acg.out.AddImm64("x3", "x3", 1); err != nil {
		return err
	}
	backPos := acg.eb.text.Len()
	acg.out.Branch(0)
	acg.patchJumpOffset(backPos, int32(loopStart-backPos))

	foundLabel := acg.eb.text.Len()
	acg.patchJumpOffset(foundJump, int32(foundLabel-foundJump))
	if err := acg.out.AddImm64("x7", "x4", 8); err != nil { // value slot
		return err
	}
	endJump := acg.eb.text.Len()
	acg.out.Branch(0)

	notFoundLabel := acg.eb.text.Len()
	acg.patchJumpOffset(notFoundJump, int32(notFoundLabel-notFoundJump))
	if err := acg.out.MovImm64("x7", 0); err != nil {
		return err
	}

	endLabel := acg.eb.text.Len()
	acg.patchJumpOffset(endJump, int32(endLabel-endJump))
	return nil
}

// compileMultipleAssign compiles `a, b = expr` where expr yields a flat tuple
// [count][elem0][elem1]... (e.g. pop returns [new_list, popped]). Element i is
// unpacked from offset 8+i*8 into the i-th name.
func (acg *ARM64CodeGen) compileMultipleAssign(stmt *MultipleAssignStmt) error {
	if err := acg.compileExpression(stmt.Value); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil { // tuple ptr
		return err
	}
	// Spill the pointer so it survives across element loads.
	acg.out.SubImm64("sp", "sp", 16)
	if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
		return err
	}

	for i, name := range stmt.Names {
		var offset int32
		if stmt.IsUpdate {
			so, ok := acg.stackVars[name]
			if !ok {
				return fmt.Errorf("cannot update undefined variable '%s'", name)
			}
			offset = int32(16 + so - 8)
		} else if so, exists := acg.stackVars[name]; exists && acg.mutableVars[name] {
			offset = int32(16 + so - 8)
		} else {
			acg.stackSize += 8
			acg.stackVars[name] = acg.stackSize
			acg.mutableVars[name] = stmt.Mutable
			offset = int32(16 + acg.stackSize - 8)
		}

		if err := acg.out.LdrImm64("x0", "sp", 0); err != nil {
			return err
		}
		if err := acg.out.LdrImm64Double("d0", "x0", int32(8+i*8)); err != nil {
			return err
		}
		if err := acg.out.StrImm64Double("d0", "x29", offset); err != nil {
			return err
		}
	}

	acg.out.AddImm64("sp", "sp", 16)
	return nil
}

// compileJumpStatement compiles jump statements (ret, @label)
func (acg *ARM64CodeGen) compileJumpStatement(stmt *JumpStmt) error {
	// Handle function return: ret with Label=0
	if stmt.Label == 0 && stmt.IsBreak {
		// Return from function
		if stmt.Value != nil {
			if err := acg.compileExpression(stmt.Value); err != nil {
				return err
			}
			// d0 now contains return value
		}

		// Restore frame pointer and link register, then return
		if err := acg.out.LdrImm64("x30", "sp", 8); err != nil {
			return err
		}
		if err := acg.out.LdrImm64("x29", "sp", 0); err != nil {
			return err
		}
		// Add back stack frame size
		if acg.currentLambda != nil {
			// Lambda has its own frame size
			// Calculate based on stored params - simplified version
			paramCount := len(acg.currentLambda.Params)
			frameSize := uint32((16 + paramCount*8 + 2048 + 15) &^ 15)
			if err := acg.out.AddImm64("sp", "sp", frameSize); err != nil {
				return err
			}
		} else {
			// Main program frame
			if err := acg.out.AddImm64("sp", "sp", uint32(acg.stackFrameSize)); err != nil {
				return err
			}
		}
		// ret instruction
		if err := acg.out.Return("x30"); err != nil {
			return err
		}
		return nil
	}

	// All other cases require being inside a loop
	if len(acg.activeLoops) == 0 {
		keyword := "@"
		if stmt.IsBreak {
			keyword = "ret"
		}
		return fmt.Errorf("%s used outside of loop", keyword)
	}

	// Resolve which loop this jump targets. Default to the innermost; a positive
	// label selects an enclosing loop by its @N label.
	target := len(acg.activeLoops) - 1
	for j := range acg.activeLoops {
		if acg.activeLoops[j].Label == stmt.Label {
			target = j
			break
		}
	}

	// Emit a placeholder branch; the loop's epilogue patches it to the loop end
	// (break) or the continue/step position (continue).
	pos := acg.eb.text.Len()
	if err := acg.out.Branch(0); err != nil {
		return err
	}
	if stmt.IsBreak {
		acg.activeLoops[target].EndPatches = append(acg.activeLoops[target].EndPatches, pos)
	} else {
		acg.activeLoops[target].ContinuePatches = append(acg.activeLoops[target].ContinuePatches, pos)
	}
	return nil
}

// pushDeferScope creates a new defer scope for collecting deferred expressions
func (acg *ARM64CodeGen) pushDeferScope() {
	acg.deferredExprs = append(acg.deferredExprs, []Expression{})
}

// popDeferScope executes all deferred expressions in reverse order and removes the scope
func (acg *ARM64CodeGen) popDeferScope() error {
	if len(acg.deferredExprs) == 0 {
		return nil
	}

	currentScope := len(acg.deferredExprs) - 1
	deferred := acg.deferredExprs[currentScope]

	// Execute deferred expressions in LIFO order
	for i := len(deferred) - 1; i >= 0; i-- {
		if err := acg.compileExpression(deferred[i]); err != nil {
			return err
		}
	}

	acg.deferredExprs = acg.deferredExprs[:currentScope]
	return nil
}

// compileArenaStmt compiles an arena block with auto-cleanup
func (acg *ARM64CodeGen) compileArenaStmt(stmt *ArenaStmt) error {
	// Mark that this program uses arenas
	acg.usesArenas = true

	// Save previous arena context and increment depth
	previousArena := acg.currentArena
	acg.currentArena++

	// Save the bump-arena top (x27) so it can be restored when the block exits,
	// freeing every cstruct value allocated within it.
	acg.stackSize += 8
	arenaSaveSlot := int32(16 + acg.stackSize - 8)
	if err := acg.out.StrImm64("x27", "x29", arenaSaveSlot); err != nil {
		return err
	}

	// Push defer scope for arena
	acg.pushDeferScope()

	// Compile statements in arena body
	for _, bodyStmt := range stmt.Body {
		if err := acg.compileStatement(bodyStmt); err != nil {
			return err
		}
	}

	// Pop defer scope and execute deferred expressions
	if err := acg.popDeferScope(); err != nil {
		return err
	}

	// Restore previous arena context
	acg.currentArena = previousArena

	// Restore the bump-arena top — frees this scope's cstruct allocations.
	if err := acg.out.LdrImm64("x27", "x29", arenaSaveSlot); err != nil {
		return err
	}
	return nil
}

// compileExpression compiles an expression and leaves result in d0 (float64 register)
func (acg *ARM64CodeGen) compileExpression(expr Expression) error {
	switch e := expr.(type) {
	case *BooleanExpr:
		// Booleans are just 1.0 / 0.0 in Tim's uniform float64 world.
		v := 0.0
		if e.Value {
			v = 1.0
		}
		return acg.compileExpression(&NumberExpr{Value: v})

	case *NumberExpr:
		// Tim uses float64 for all numbers
		// For whole numbers, convert via integer; for decimals, load from .rodata
		if e.Value == float64(int64(e.Value)) {
			// Whole number - convert to int64, then to float64
			val := int64(e.Value)
			// Load integer into x0
			if err := acg.out.MovImm64("x0", uint64(val)); err != nil {
				return err
			}
			// Convert x0 (int64) to d0 (float64)
			// scvtf d0, x0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0
		} else {
			// Decimal number - store in .rodata and load
			labelName := fmt.Sprintf("float_%d", acg.stringCounter)
			acg.stringCounter++

			// Convert float64 to 8 bytes (little-endian)
			bits := uint64(0)
			*(*float64)(unsafe.Pointer(&bits)) = e.Value
			var floatData []byte
			for i := range 8 {
				floatData = append(floatData, byte((bits>>(i*8))&0xFF))
			}
			acg.eb.Define(labelName, string(floatData))

			// Load address of float into x0 using PC-relative addressing
			offset := uint64(acg.eb.text.Len())
			acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{
				offset:     offset,
				symbolName: labelName,
			})
			// ADRP x0, label@PAGE
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x90})
			// ADD x0, x0, label@PAGEOFF
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x91})

			// Load float64 from [x0] into d0
			// ldr d0, [x0]
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x40, 0xfd})
		}

	case *StringExpr:
		// Strings are represented as map[uint64]float64
		// Map format: [count][key0][val0][key1][val1]...

		// Check if we've already interned this string
		var labelName string
		if existingLabel, exists := acg.stringInterns[e.Value]; exists {
			// Reuse existing label for this string
			labelName = existingLabel
		} else {
			// Create new label and intern it
			labelName = fmt.Sprintf("str_%d", acg.stringCounter)
			acg.stringCounter++
			acg.stringInterns[e.Value] = labelName

			// Build map data: count followed by key-value pairs
			var mapData []byte

			// Count (number of characters)
			count := float64(len(e.Value))
			countBits := uint64(0)
			*(*float64)(unsafe.Pointer(&countBits)) = count
			for i := range 8 {
				mapData = append(mapData, byte((countBits>>(i*8))&0xFF))
			}

			// Add each character as a key-value pair
			for idx, ch := range e.Value {
				// Key: character index as float64
				keyVal := float64(idx)
				keyBits := uint64(0)
				*(*float64)(unsafe.Pointer(&keyBits)) = keyVal
				for i := range 8 {
					mapData = append(mapData, byte((keyBits>>(i*8))&0xFF))
				}

				// Value: character code as float64
				charVal := float64(ch)
				charBits := uint64(0)
				*(*float64)(unsafe.Pointer(&charBits)) = charVal
				for i := range 8 {
					mapData = append(mapData, byte((charBits>>(i*8))&0xFF))
				}
			}

			acg.eb.Define(labelName, string(mapData))
		}

		// Load address into x0
		offset := uint64(acg.eb.text.Len())
		acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{
			offset:     offset,
			symbolName: labelName,
		})
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x90}) // ADRP x0, label@PAGE
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x91}) // ADD x0, x0, label@PAGEOFF

		// Convert pointer to float64: scvtf d0, x0
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})

	case *IdentExpr:
		// Module globals live in the heap array based at x28 (visible from any
		// lambda body), addressed by their fixed slot.
		if slot, isGlobal := acg.globalSlots[e.Name]; isGlobal {
			return acg.out.LdrImm64Double("d0", "x28", int32(slot*8))
		}
		// Load variable from stack into d0
		stackOffset, exists := acg.stackVars[e.Name]
		if !exists {
			// Captured variable: read from the closure env at [closurePtr+8+idx*8].
			if acg.currentLambda != nil {
				if idx := slices.Index(acg.currentLambda.Captures, e.Name); idx >= 0 {
					if err := acg.out.LdrImm64("x9", "x29", acg.closurePtrOffset); err != nil {
						return err
					}
					if acg.boxedVars[e.Name] {
						// Boxed capture: the env slot holds a cell pointer; deref it.
						if err := acg.out.LdrImm64("x9", "x9", int32(8+idx*8)); err != nil {
							return err
						}
						return acg.out.LdrImm64Double("d0", "x9", 0)
					}
					return acg.out.LdrImm64Double("d0", "x9", int32(8+idx*8))
				}
			}
			if VerboseMode {
				fmt.Fprintf(os.Stderr, "Error: undefined variable '%s'\n", e.Name)
			}
			return fmt.Errorf("undefined variable: %s", e.Name)
		}
		// ldr d0, [x29, #offset]
		// x29 points to saved fp location, variables start at offset 16
		offset := int32(16 + stackOffset - 8)
		if acg.boxedVars[e.Name] {
			// Boxed local: the slot holds a cell pointer; deref it.
			if err := acg.out.LdrImm64("x9", "x29", offset); err != nil {
				return err
			}
			return acg.out.LdrImm64Double("d0", "x9", 0)
		}
		if err := acg.out.LdrImm64Double("d0", "x29", offset); err != nil {
			return err
		}

	case *BinaryExpr:
		// Check for list concatenation with + operator
		if e.Operator == "+" {
			leftType := acg.getExprType(e.Left)
			rightType := acg.getExprType(e.Right)

			if leftType == "list" && rightType == "list" {
				// List concatenation: [1, 2] + [3, 4] -> [1, 2, 3, 4]
				// Compile left list (result in d0)
				if err := acg.compileExpression(e.Left); err != nil {
					return err
				}
				// Convert d0 (float) to x0 (pointer)
				acg.out.SubImm64("sp", "sp", 16)
				acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x00, 0xfd}) // str d0, [sp]
				if err := acg.out.LdrImm64("x0", "sp", 0); err != nil {
					return err
				}
				acg.out.AddImm64("sp", "sp", 16)

				// Push x0 (left ptr) to stack
				acg.out.SubImm64("sp", "sp", 16)
				if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
					return err
				}

				// Compile right list (result in d0)
				if err := acg.compileExpression(e.Right); err != nil {
					return err
				}
				// Convert d0 to x1
				acg.out.SubImm64("sp", "sp", 16)
				acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x00, 0xfd}) // str d0, [sp]
				if err := acg.out.LdrImm64("x1", "sp", 0); err != nil {
					return err
				}
				acg.out.AddImm64("sp", "sp", 16)

				// Restore left ptr to x0
				if err := acg.out.LdrImm64("x0", "sp", 0); err != nil {
					return err
				}
				acg.out.AddImm64("sp", "sp", 16)

				// Call _tim_list_concat(x0, x1) -> x0
				if err := acg.eb.GenerateCallInstruction("_tim_list_concat"); err != nil {
					return err
				}

				// Convert result x0 back to d0
				acg.out.SubImm64("sp", "sp", 16)
				if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
					return err
				}
				acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x40, 0xfd}) // ldr d0, [sp]
				acg.out.AddImm64("sp", "sp", 16)

				return nil
			}

			if leftType == "list" && rightType != "list" {
				// List + element: append the element (shorthand for list += elem).
				if err := acg.compileExpression(e.Left); err != nil {
					return err
				}
				if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil {
					return err
				}
				acg.out.SubImm64("sp", "sp", 16)
				if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
					return err
				}
				if err := acg.compileExpression(e.Right); err != nil {
					return err
				}
				if err := acg.out.LdrImm64("x0", "sp", 0); err != nil {
					return err
				}
				acg.out.AddImm64("sp", "sp", 16)
				if err := acg.eb.GenerateCallInstruction("_tim_append"); err != nil {
					return err
				}
				return acg.out.ScvtfInt64ToDouble("d0", "x0")
			}

			if leftType == "string" && rightType == "string" {
				// String concatenation. String pointers are stored numerically
				// (scvtf), so recover them with fcvtzs — a bit reinterpret would
				// pass garbage to the helper and crash.
				if err := acg.compileExpression(e.Left); err != nil {
					return err
				}
				if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil {
					return err
				}
				// Spill left pointer across the right operand's evaluation.
				acg.out.SubImm64("sp", "sp", 16)
				if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
					return err
				}
				if err := acg.compileExpression(e.Right); err != nil {
					return err
				}
				if err := acg.out.FcvtzsDoubleToInt64("x1", "d0"); err != nil {
					return err
				}
				if err := acg.out.LdrImm64("x0", "sp", 0); err != nil {
					return err
				}
				acg.out.AddImm64("sp", "sp", 16)

				// Call _tim_string_concat(x0, x1) -> x0
				if err := acg.eb.GenerateCallInstruction("_tim_string_concat"); err != nil {
					return err
				}
				// Result pointer back to the numeric convention.
				return acg.out.ScvtfInt64ToDouble("d0", "x0")
			}
		}

		// String equality/inequality compares contents, not pointer identity.
		// (Identical literals are interned to one address, but runtime-built
		// strings are not, so a numeric compare would be wrong.)
		if (e.Operator == "==" || e.Operator == "!=") &&
			acg.getExprType(e.Left) == "string" && acg.getExprType(e.Right) == "string" {
			if err := acg.compileExpression(e.Left); err != nil {
				return err
			}
			if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil {
				return err
			}
			acg.out.SubImm64("sp", "sp", 16)
			if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
				return err
			}
			if err := acg.compileExpression(e.Right); err != nil {
				return err
			}
			if err := acg.out.FcvtzsDoubleToInt64("x1", "d0"); err != nil {
				return err
			}
			if err := acg.out.LdrImm64("x0", "sp", 0); err != nil {
				return err
			}
			acg.out.AddImm64("sp", "sp", 16)
			if err := acg.eb.GenerateCallInstruction("_tim_string_eq"); err != nil {
				return err
			}
			// x0 = 1 if equal else 0. Invert for "!=".
			if e.Operator == "!=" {
				if err := acg.out.CmpImm64("x0", 0); err != nil {
					return err
				}
				acg.out.out.writer.WriteBytes([]byte{0xe0, 0x17, 0x9f, 0x9a}) // cset x0, eq
			}
			return acg.out.ScvtfInt64ToDouble("d0", "x0")
		}

		// Special handling for or! operator (railway-oriented programming)
		// or! requires conditional execution: only evaluate right side if left is error/null
		if e.Operator == "or!" {
			// Compile left expression into d0
			if err := acg.compileExpression(e.Left); err != nil {
				return err
			}

			// Check if d0 is NaN by comparing with itself
			// fcmp d0, d0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x60, 0x1e})
			// b.vs (branch if overflow/NaN) to execute_default
			executeDefaultPos1 := acg.eb.text.Len()
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x54}) // b.vs placeholder

			// Not NaN, now check if d0 == 0.0 (null pointer)
			// fmov d1, xzr (d1 = 0.0)
			acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x67, 0x9e})
			// fcmp d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e})
			// b.eq (branch if equal to 0) to execute_default
			executeDefaultPos2 := acg.eb.text.Len()
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x54}) // b.eq placeholder

			// Value is valid (not NaN and not 0), skip to end without evaluating right side
			skipDefaultPos := acg.eb.text.Len()
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x14}) // b placeholder

			// execute_default label: evaluate right expression (could be block or value)
			executeDefaultLabel := acg.eb.text.Len()
			if err := acg.compileExpression(e.Right); err != nil { // Result goes to d0
				return err
			}

			// End label
			endLabel := acg.eb.text.Len()

			// Patch the jumps
			// ARM64 branch offsets are in instructions (4 bytes each), not bytes
			// Offset = (target - current_pc) / 4

			// Patch NaN check (b.vs) to execute_default
			offset1 := int32((executeDefaultLabel - executeDefaultPos1) / 4)
			bytes1 := acg.eb.text.Bytes()
			// b.vs encoding: 0x54 with imm19 in bits [23:5] and cond=0110 (VS) in bits [3:0]
			instr1 := uint32(0x54000006) | (uint32(offset1&0x7ffff) << 5)
			bytes1[executeDefaultPos1] = byte(instr1 & 0xFF)
			bytes1[executeDefaultPos1+1] = byte((instr1 >> 8) & 0xFF)
			bytes1[executeDefaultPos1+2] = byte((instr1 >> 16) & 0xFF)
			bytes1[executeDefaultPos1+3] = byte((instr1 >> 24) & 0xFF)

			// Patch zero check (b.eq) to execute_default
			offset2 := int32((executeDefaultLabel - executeDefaultPos2) / 4)
			instr2 := uint32(0x54000000) | (uint32(offset2&0x7ffff) << 5)
			bytes1[executeDefaultPos2] = byte(instr2 & 0xFF)
			bytes1[executeDefaultPos2+1] = byte((instr2 >> 8) & 0xFF)
			bytes1[executeDefaultPos2+2] = byte((instr2 >> 16) & 0xFF)
			bytes1[executeDefaultPos2+3] = byte((instr2 >> 24) & 0xFF)

			// Patch skip jump (b) to end
			offset3 := int32((endLabel - skipDefaultPos) / 4)
			instr3 := uint32(0x14000000) | uint32(offset3&0x3ffffff)
			bytes1[skipDefaultPos] = byte(instr3 & 0xFF)
			bytes1[skipDefaultPos+1] = byte((instr3 >> 8) & 0xFF)
			bytes1[skipDefaultPos+2] = byte((instr3 >> 16) & 0xFF)
			bytes1[skipDefaultPos+3] = byte((instr3 >> 24) & 0xFF)

			// d0 now contains either original value (if not NaN/null) or result of right side
			return nil
		}

		// Compile left operand (result in d0)
		if err := acg.compileExpression(e.Left); err != nil {
			return err
		}

		// Keep the left operand in a scratch FP register (d24+depth) across the
		// right operand instead of spilling to memory — but only when the right
		// operand has no call (which would clobber the caller-saved d24-d31) and
		// the stack isn't exhausted. Deeper nesting uses higher registers.
		if acg.fpDepth < 8 && !acg.exprHasCall(e.Right) {
			reg := uint32(24 + acg.fpDepth)
			acg.emitFmovD(reg, 0) // fmov d{reg}, d0  (save left)
			acg.fpDepth++
			if err := acg.compileExpression(e.Right); err != nil {
				return err
			}
			acg.fpDepth--
			acg.out.out.writer.WriteBytes([]byte{0x01, 0x40, 0x60, 0x1e}) // fmov d1, d0 (right)
			acg.emitFmovD(0, reg)                                         // fmov d0, d{reg} (restore left)
		} else {
			// Memory spill (16-byte aligned).
			acg.out.SubImm64("sp", "sp", 16)
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x00, 0xfd}) // str d0, [sp]
			if err := acg.compileExpression(e.Right); err != nil {
				return err
			}
			acg.out.out.writer.WriteBytes([]byte{0x01, 0x40, 0x60, 0x1e}) // fmov d1, d0
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x40, 0xfd}) // ldr d0, [sp]
			acg.out.AddImm64("sp", "sp", 16)
		}

		// Perform operation: d0 = d0 op d1
		switch e.Operator {
		case "+":
			// fadd d0, d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x28, 0x61, 0x1e})
		case "-":
			// fsub d0, d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x38, 0x61, 0x1e})
		case "*":
			// fmul d0, d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x08, 0x61, 0x1e})
		case "/":
			// Division by zero yields an error-NaN (0x7FF8000064763000, "dv0")
			// that or! can detect, rather than +/-inf.
			// fcmp d1, #0.0
			acg.out.out.writer.WriteBytes([]byte{0x28, 0x20, 0x60, 0x1e})
			// b.ne do_div (divisor non-zero: normal division)
			nePos := acg.eb.text.Len()
			acg.out.BranchCond("ne", 0)
			// divisor is zero: load the error-NaN sentinel into d0
			if err := acg.out.MovImm64("x0", 0x7FF8000064763000); err != nil {
				return err
			}
			if err := acg.out.FmovGPToDouble("d0", "x0"); err != nil {
				return err
			}
			// b end_div
			bEndPos := acg.eb.text.Len()
			acg.out.Branch(0)
			// do_div:
			doDivLabel := acg.eb.text.Len()
			acg.patchJumpOffset(nePos, int32(doDivLabel-nePos))
			// fdiv d0, d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x18, 0x61, 0x1e})
			// end_div:
			endDivLabel := acg.eb.text.Len()
			acg.patchJumpOffset(bEndPos, int32(endDivLabel-bEndPos))
		case "==":
			// fcmp d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e})
			// cset x0, eq (x0 = 1 if equal, else 0)
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0x17, 0x9f, 0x9a})
			// scvtf d0, x0 (convert to float)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "!=":
			// fcmp d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e})
			// cset x0, ne
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0x07, 0x9f, 0x9a})
			// scvtf d0, x0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "<":
			// fcmp d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e})
			// cset x0, lt
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0xa7, 0x9f, 0x9a})
			// scvtf d0, x0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "<=":
			// fcmp d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e})
			// cset x0, le
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0xc7, 0x9f, 0x9a})
			// scvtf d0, x0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case ">":
			// fcmp d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e})
			// cset x0, gt
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0xd7, 0x9f, 0x9a})
			// scvtf d0, x0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case ">=":
			// fcmp d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e})
			// cset x0, ge
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0xb7, 0x9f, 0x9a})
			// scvtf d0, x0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "mod", "%":
			// Modulo by zero yields the same error-NaN as division by zero.
			// fcmp d1, #0.0
			acg.out.out.writer.WriteBytes([]byte{0x28, 0x20, 0x60, 0x1e})
			neModPos := acg.eb.text.Len()
			acg.out.BranchCond("ne", 0) // b.ne do_mod
			if err := acg.out.MovImm64("x0", 0x7FF8000064763000); err != nil {
				return err
			}
			if err := acg.out.FmovGPToDouble("d0", "x0"); err != nil {
				return err
			}
			bModEndPos := acg.eb.text.Len()
			acg.out.Branch(0) // b end_mod
			doModLabel := acg.eb.text.Len()
			acg.patchJumpOffset(neModPos, int32(doModLabel-neModPos))
			// Modulo: a % b = a - b * floor(a / b)
			// d0 = dividend (a), d1 = divisor (b)
			// fmov d2, d0 (save dividend in d2)
			acg.out.out.writer.WriteBytes([]byte{0x02, 0x40, 0x60, 0x1e})
			// fmov d3, d1 (save divisor in d3)
			acg.out.out.writer.WriteBytes([]byte{0x23, 0x40, 0x60, 0x1e})
			// fdiv d0, d0, d1 (d0 = a / b)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x18, 0x61, 0x1e})
			// fcvtzs x0, d0 (x0 = floor(a / b) as int)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// scvtf d0, x0 (d0 = floor(a / b) as float)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
			// fmul d0, d0, d3 (d0 = floor(a / b) * b)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x08, 0x63, 0x1e})
			// fsub d0, d2, d0 (d0 = a - floor(a / b) * b)
			acg.out.out.writer.WriteBytes([]byte{0x40, 0x38, 0x60, 0x1e})
			modEndLabel := acg.eb.text.Len()
			acg.patchJumpOffset(bModEndPos, int32(modEndLabel-bModEndPos))
		case "and":
			// Logical AND: returns 1.0 if both non-zero, else 0.0
			// Compare d0 with 0.0
			// fmov d2, xzr (d2 = 0.0)
			acg.out.out.writer.WriteBytes([]byte{0xe2, 0x03, 0x67, 0x9e})
			// fcmp d0, d2
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x62, 0x1e})
			// cset x0, ne (x0 = 1 if d0 != 0, else 0)
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0x07, 0x9f, 0x9a})
			// Compare d1 with 0.0
			// fcmp d1, d2
			acg.out.out.writer.WriteBytes([]byte{0x20, 0x20, 0x62, 0x1e})
			// cset x1, ne (x1 = 1 if d1 != 0, else 0)
			acg.out.out.writer.WriteBytes([]byte{0xe1, 0x07, 0x9f, 0x9a})
			// and x0, x0, x1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x01, 0x8a})
			// scvtf d0, x0 (convert result to float)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "or":
			// Logical OR: returns 1.0 if either non-zero, else 0.0
			// Compare d0 with 0.0
			// fmov d2, xzr (d2 = 0.0)
			acg.out.out.writer.WriteBytes([]byte{0xe2, 0x03, 0x67, 0x9e})
			// fcmp d0, d2
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x62, 0x1e})
			// cset x0, ne (x0 = 1 if d0 != 0, else 0)
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0x07, 0x9f, 0x9a})
			// Compare d1 with 0.0
			// fcmp d1, d2
			acg.out.out.writer.WriteBytes([]byte{0x20, 0x20, 0x62, 0x1e})
			// cset x1, ne (x1 = 1 if d1 != 0, else 0)
			acg.out.out.writer.WriteBytes([]byte{0xe1, 0x07, 0x9f, 0x9a})
			// orr x0, x0, x1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x01, 0xaa})
			// scvtf d0, x0 (convert result to float)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "xor":
			// Logical XOR: returns 1.0 if exactly one non-zero, else 0.0
			// Compare d0 with 0.0
			// fmov d2, xzr (d2 = 0.0)
			acg.out.out.writer.WriteBytes([]byte{0xe2, 0x03, 0x67, 0x9e})
			// fcmp d0, d2
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x62, 0x1e})
			// cset x0, ne (x0 = 1 if d0 != 0, else 0)
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0x07, 0x9f, 0x9a})
			// Compare d1 with 0.0
			// fcmp d1, d2
			acg.out.out.writer.WriteBytes([]byte{0x20, 0x20, 0x62, 0x1e})
			// cset x1, ne (x1 = 1 if d1 != 0, else 0)
			acg.out.out.writer.WriteBytes([]byte{0xe1, 0x07, 0x9f, 0x9a})
			// eor x0, x0, x1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x01, 0xca})
			// scvtf d0, x0 (convert result to float)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "shl":
			// Shift left: convert to int64, shift, convert back
			// fcvtzs x0, d0 (x0 = int64(d0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// fcvtzs x1, d1 (x1 = int64(d1))
			acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x78, 0x9e})
			// lsl x0, x0, x1 (x0 <<= x1)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0xc1, 0x9a})
			// scvtf d0, x0 (d0 = float64(x0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "shr":
			// Shift right: convert to int64, shift, convert back
			// fcvtzs x0, d0 (x0 = int64(d0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// fcvtzs x1, d1 (x1 = int64(d1))
			acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x78, 0x9e})
			// lsr x0, x0, x1 (x0 >>= x1)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x24, 0xc1, 0x9a})
			// scvtf d0, x0 (d0 = float64(x0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "rol":
			// Rotate left: convert to int64, rotate, convert back
			// fcvtzs x0, d0 (x0 = int64(d0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// fcvtzs x1, d1 (x1 = int64(d1))
			acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x78, 0x9e})
			// neg x2, x1 (x2 = -x1 for rotate)
			acg.out.out.writer.WriteBytes([]byte{0xe2, 0x03, 0x01, 0xcb})
			// ror x0, x0, x2 (rotate left by negating right rotate)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x2c, 0xc2, 0x9a})
			// scvtf d0, x0 (d0 = float64(x0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "ror":
			// Rotate right: convert to int64, rotate, convert back
			// fcvtzs x0, d0 (x0 = int64(d0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// fcvtzs x1, d1 (x1 = int64(d1))
			acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x78, 0x9e})
			// ror x0, x0, x1 (x0 rotate right by x1)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x2c, 0xc1, 0x9a})
			// scvtf d0, x0 (d0 = float64(x0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "&b":
			// Bitwise AND: convert to int64, AND, convert back
			// fcvtzs x0, d0 (x0 = int64(d0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// fcvtzs x1, d1 (x1 = int64(d1))
			acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x78, 0x9e})
			// and x0, x0, x1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x01, 0x8a})
			// scvtf d0, x0 (d0 = float64(x0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "|b":
			// Bitwise OR: convert to int64, OR, convert back
			// fcvtzs x0, d0 (x0 = int64(d0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// fcvtzs x1, d1 (x1 = int64(d1))
			acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x78, 0x9e})
			// orr x0, x0, x1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x01, 0xaa})
			// scvtf d0, x0 (d0 = float64(x0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "^b":
			// Bitwise XOR: convert to int64, XOR, convert back
			// fcvtzs x0, d0 (x0 = int64(d0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// fcvtzs x1, d1 (x1 = int64(d1))
			acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x78, 0x9e})
			// eor x0, x0, x1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x01, 0xca})
			// scvtf d0, x0 (d0 = float64(x0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "<<b":
			// Left shift: convert to int64, shift, convert back (same as shl)
			// fcvtzs x0, d0 (x0 = int64(d0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// fcvtzs x1, d1 (x1 = int64(d1))
			acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x78, 0x9e})
			// lsl x0, x0, x1 (x0 <<= x1)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0xc1, 0x9a})
			// scvtf d0, x0 (d0 = float64(x0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case ">>b":
			// Right shift: convert to int64, shift, convert back (same as shr)
			// fcvtzs x0, d0 (x0 = int64(d0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// fcvtzs x1, d1 (x1 = int64(d1))
			acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x78, 0x9e})
			// lsr x0, x0, x1 (x0 >>= x1)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x24, 0xc1, 0x9a})
			// scvtf d0, x0 (d0 = float64(x0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		case "**":
			// Power: call pow(base, exponent) from libm
			// d0 = base, d1 = exponent
			// Result in d0
			if err := acg.eb.GenerateCallInstruction("pow"); err != nil {
				return err
			}
		case "::":
			// Cons: prepend element to list
			// d0 = element, d1 = list pointer
			// Convert to pointers for function call
			acg.out.SubImm64("sp", "sp", 16)
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x00, 0xfd}) // str d0, [sp]
			if err := acg.out.LdrImm64("x0", "sp", 0); err != nil {
				return err
			}
			acg.out.AddImm64("sp", "sp", 16)

			acg.out.SubImm64("sp", "sp", 16)
			acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x00, 0xfd}) // str d1, [sp]
			if err := acg.out.LdrImm64("x1", "sp", 0); err != nil {
				return err
			}
			acg.out.AddImm64("sp", "sp", 16)

			// Call _tim_list_cons(element, list) -> new_list
			if err := acg.eb.GenerateCallInstruction("_tim_list_cons"); err != nil {
				return err
			}

			// Convert result back to d0
			acg.out.SubImm64("sp", "sp", 16)
			if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
				return err
			}
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x40, 0xfd}) // ldr d0, [sp]
			acg.out.AddImm64("sp", "sp", 16)
		case "?b":
			// Bit Test: (int64(d0) >> int64(d1)) & 1
			// fcvtzs x0, d0 (x0 = int64(d0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// fcvtzs x1, d1 (x1 = int64(d1))
			acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x78, 0x9e})
			// lsr x0, x0, x1 (x0 >>= x1)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x24, 0xc1, 0x9a})
			// mov x1, #1
			acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x80, 0xd2})
			// and x0, x0, x1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x01, 0x8a})
			// scvtf d0, x0 (d0 = float64(x0))
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
		default:
			return fmt.Errorf("unsupported binary operator for ARM64: %s", e.Operator)
		}

	case *ListExpr:
		// Lists are stored as: [count][elem0][elem1]...
		// If every element is a number literal we can emit the whole thing as
		// static rodata. Otherwise we build it on the heap at runtime, evaluating
		// each element expression (e.g. struct constructors or variables).
		allNumbers := true
		for _, elem := range e.Elements {
			if _, ok := elem.(*NumberExpr); !ok {
				allNumbers = false
				break
			}
		}

		if !allNumbers {
			n := len(e.Elements)
			if err := acg.out.MovImm64("x0", uint64((n+1)*8)); err != nil {
				return err
			}
			if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
				return err
			}
			// Spill the base pointer across element evaluation.
			acg.out.SubImm64("sp", "sp", 16)
			if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
				return err
			}
			// Store the count (as float64) at [base].
			if err := acg.out.MovImm64("x1", uint64(n)); err != nil {
				return err
			}
			if err := acg.out.ScvtfInt64ToDouble("d0", "x1"); err != nil {
				return err
			}
			if err := acg.out.LdrImm64("x9", "sp", 0); err != nil {
				return err
			}
			if err := acg.out.StrImm64Double("d0", "x9", 0); err != nil {
				return err
			}
			for i, elem := range e.Elements {
				if err := acg.compileExpression(elem); err != nil { // value in d0
					return err
				}
				if err := acg.out.LdrImm64("x9", "sp", 0); err != nil {
					return err
				}
				if err := acg.out.StrImm64Double("d0", "x9", int32(8+i*8)); err != nil {
					return err
				}
			}
			if err := acg.out.LdrImm64("x0", "sp", 0); err != nil {
				return err
			}
			acg.out.AddImm64("sp", "sp", 16)
			return acg.out.ScvtfInt64ToDouble("d0", "x0") // numeric pointer
		}

		// For now, store list data in rodata
		labelName := fmt.Sprintf("list_%d", acg.stringCounter)
		acg.stringCounter++

		var listData []byte

		// Count
		count := float64(len(e.Elements))
		countBits := uint64(0)
		*(*float64)(unsafe.Pointer(&countBits)) = count
		for i := range 8 {
			listData = append(listData, byte((countBits>>(i*8))&0xFF))
		}

		// Elements (number literals)
		for _, elem := range e.Elements {
			numExpr := elem.(*NumberExpr)
			elemBits := uint64(0)
			*(*float64)(unsafe.Pointer(&elemBits)) = numExpr.Value
			for i := range 8 {
				listData = append(listData, byte((elemBits>>(i*8))&0xFF))
			}
		}

		acg.eb.Define(labelName, string(listData))

		// Load address into x0
		offset := uint64(acg.eb.text.Len())
		acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{
			offset:     offset,
			symbolName: labelName,
		})
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x90}) // ADRP x0, label@PAGE
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x91}) // ADD x0, x0, label@PAGEOFF

		// Convert pointer to float64
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0

	case *IndexExpr:
		// Compile the list/map expression
		if err := acg.compileExpression(e.List); err != nil {
			return err
		}

		// d0 now contains pointer to list (as float64)
		// Convert to integer pointer: fcvtzs x0, d0
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})

		// Save list pointer (maintain 16-byte stack alignment)
		acg.out.SubImm64("sp", "sp", 16)
		acg.out.StrImm64("x0", "sp", 0)

		// Compile index expression
		if err := acg.compileExpression(e.Index); err != nil {
			return err
		}

		// Convert index from float64 to int64: fcvtzs x1, d0
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x78, 0x9e})

		// Restore list pointer
		acg.out.LdrImm64("x0", "sp", 0)
		acg.out.AddImm64("sp", "sp", 16)

		// x0 = pointer, x1 = index/key.
		// Maps and strings store [count][key0][val0]... (16-byte pairs), so
		// m[k] is a key lookup. Lists store [count][elem0][elem1]... (flat).
		idxType := acg.getExprType(e.List)
		if idxType == "map" || idxType == "string" {
			if err := acg.emitMapKeyLookup(); err != nil {
				return err
			}
		} else {
			// Skip past count (8 bytes) and index by (index * 8)
			acg.out.AddImm64("x0", "x0", 8)
			// x1 = x1 << 3 (multiply by 8)
			acg.out.out.writer.WriteBytes([]byte{0x21, 0xf0, 0x7d, 0xd3}) // lsl x1, x1, #3
			// x0 = x0 + x1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x01, 0x8b}) // add x0, x0, x1
			// Load element into d0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x40, 0xfd}) // ldr d0, [x0]
		}

	case *CallExpr:
		return acg.compileCall(e)

	case *DirectCallExpr:
		return acg.compileDirectCall(e)

	case *MatchExpr:
		return acg.compileMatchExpr(e)

	case *FMAExpr:
		// FMA pattern: a * b + c (or variants)
		// Compile A (result in d0)
		if err := acg.compileExpression(e.A); err != nil {
			return err
		}
		// Save d0 to stack (op1) - maintain 16-byte alignment
		acg.out.SubImm64("sp", "sp", 16)
		if err := acg.out.StrImm64Double("d0", "sp", 0); err != nil {
			return err
		}

		// Compile B (result in d0)
		if err := acg.compileExpression(e.B); err != nil {
			return err
		}
		// Save d0 to stack (op2) - maintain 16-byte alignment
		acg.out.SubImm64("sp", "sp", 16)
		if err := acg.out.StrImm64Double("d0", "sp", 0); err != nil {
			return err
		}

		// Compile C (result in d0)
		if err := acg.compileExpression(e.C); err != nil {
			return err
		}
		// Move C to d3 (accumulator)
		// fmov d3, d0 (double-precision register move)
		acg.out.out.writer.WriteBytes([]byte{0x03, 0x40, 0x60, 0x1e})

		// Pop B to d1 (Dm)
		if err := acg.out.LdrImm64Double("d1", "sp", 0); err != nil {
			return err
		}
		acg.out.AddImm64("sp", "sp", 16)

		// Pop A to d2 (Dn)
		if err := acg.out.LdrImm64Double("d2", "sp", 0); err != nil {
			return err
		}
		acg.out.AddImm64("sp", "sp", 16)

		// Result in d0 (Dd). ARM64 fused ops are defined as:
		//   FMADD  = Da + Dn*Dm        FMSUB  = Da - Dn*Dm
		//   FNMADD = -(Da + Dn*Dm)     FNMSUB = Dn*Dm - Da
		// with Dn=a (d2), Dm=b (d1), Da=c (d3).
		if e.IsNegMul {
			if e.IsSub {
				// -(a*b) - c -> -(Dn*Dm + Da) -> FNMADD
				return acg.out.FnmaddScalar64("d0", "d2", "d1", "d3")
			}
			// -(a*b) + c -> Da - Dn*Dm -> FMSUB
			return acg.out.FmsubScalar64("d0", "d2", "d1", "d3")
		}
		if e.IsSub {
			// a*b - c -> Dn*Dm - Da -> FNMSUB
			return acg.out.FnmsubScalar64("d0", "d2", "d1", "d3")
		}
		// a*b + c -> FMADD
		return acg.out.FmaddScalar64("d0", "d2", "d1", "d3")

	case *LambdaExpr:
		// Generate a unique function name for this lambda
		acg.lambdaCounter++
		funcName := fmt.Sprintf("lambda_%d", acg.lambdaCounter)

		// A lambda value is a heap closure object [func_ptr, cap0, cap1, ...].
		// Captures are the enclosing-scope variables the body references, taken
		// by value at creation time. (Mutable module state is shared via x28 and
		// is NOT captured here.)
		caps := acg.computeCaptures(e.Body, e.Params)

		acg.lambdaFuncs = append(acg.lambdaFuncs, ARM64LambdaFunc{
			Name:              funcName,
			Params:            e.Params,
			ParamCStructTypes: e.ParamCStructTypes,
			Body:              e.Body,
			VarName:           acg.currentAssignName, // Store variable name for self-recursion
			Captures:          caps,
		})
		lambdaIdx := len(acg.lambdaFuncs) - 1

		// Allocate the closure object: 8 (func ptr) + 8*len(caps).
		if err := acg.out.MovImm64("x0", uint64(8+len(caps)*8)); err != nil {
			return err
		}
		if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
			return err
		}
		acg.out.MovReg64("x10", "x0") // x10 = closure object pointer

		// Store the function address at [obj]. ADRP/ADD target x0 (the relocation
		// patcher expects Rd=0), then store into the object held in x10.
		off := uint64(acg.eb.text.Len())
		acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{offset: off, symbolName: funcName})
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x90}) // ADRP x0, funcName@PAGE
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x91}) // ADD x0, x0, funcName@PAGEOFF
		acg.out.out.writer.WriteBytes([]byte{0x40, 0x01, 0x00, 0xf9}) // str x0, [x10]

		// Stash the object pointer and store each captured value at [obj+8+i*8].
		acg.out.SubImm64("sp", "sp", 16)
		if err := acg.out.StrImm64("x10", "sp", 0); err != nil {
			return err
		}
		for i, name := range caps {
			if acg.boxedVars[name] {
				// Captured by reference: the env slot holds the shared cell
				// pointer so the closure's mutations persist and are visible.
				if err := acg.emitLoadBoxCellPtr(name, "x11"); err != nil {
					return err
				}
				if err := acg.out.LdrImm64("x9", "sp", 0); err != nil {
					return err
				}
				if err := acg.out.StrImm64("x11", "x9", int32(8+i*8)); err != nil {
					return err
				}
				if acg.lambdaFuncs[lambdaIdx].BoxedCaptures == nil {
					acg.lambdaFuncs[lambdaIdx].BoxedCaptures = make(map[string]bool)
				}
				acg.lambdaFuncs[lambdaIdx].BoxedCaptures[name] = true
				continue
			}
			if err := acg.compileExpression(&IdentExpr{Name: name}); err != nil {
				return err
			}
			if err := acg.out.LdrImm64("x9", "sp", 0); err != nil {
				return err
			}
			if err := acg.out.StrImm64Double("d0", "x9", int32(8+i*8)); err != nil {
				return err
			}
		}
		if err := acg.out.LdrImm64("x0", "sp", 0); err != nil {
			return err
		}
		acg.out.AddImm64("sp", "sp", 16)
		// Closure pointer -> numeric double convention in d0.
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0

	case *UnaryExpr:
		// Compile the operand first (result in d0)
		if err := acg.compileExpression(e.Operand); err != nil {
			return err
		}

		switch e.Operator {
		case "-":
			// Unary minus: negate the value
			// Use fneg d0, d0 instruction
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x40, 0x61, 0x1e}) // fneg d0, d0

		case "not":
			// Logical NOT: returns 1.0 if operand is 0.0, else 0.0
			// Compare d0 with 0.0
			// fmov d1, xzr (d1 = 0.0)
			acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x67, 0x9e})
			// fcmp d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e})
			// cset x0, eq (x0 = 1 if equal, else 0)
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0x17, 0x9f, 0x9a})
			// scvtf d0, x0 (convert to float64)
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})

		case "~b":
			// Bitwise NOT: convert to int64, NOT, convert back
			// fcvtzs x0, d0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// mvn x0, x0 (bitwise NOT)
			acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x20, 0xaa})
			// scvtf d0, x0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})

		case "#":
			// Length operator: the operand is a list/map pointer (numeric double);
			// the length is stored in the first 8 bytes.
			// fcvtzs x0, d0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// ldr d0, [x0]
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x40, 0xfd})

		default:
			return fmt.Errorf("unsupported unary operator for ARM64: %s", e.Operator)
		}

	case *LengthExpr:
		// Compile the operand (should be a list/map, returns pointer as float64 in d0)
		if err := acg.compileExpression(e.Operand); err != nil {
			return err
		}

		// Convert pointer from float64 to integer in x0
		// fcvtzs x0, d0
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})

		// Load length from list/map (first 8 bytes)
		// ldr d0, [x0]
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x40, 0xfd})

		// Length is now in d0 as float64

	case *MapExpr:
		// Map literal stored as: [count (float64)] [key1] [value1] [key2] [value2] ...
		labelName := fmt.Sprintf("map_%d", acg.stringCounter)
		acg.stringCounter++

		var mapData []byte

		// Add count
		count := float64(len(e.Keys))
		countBits := uint64(0)
		*(*float64)(unsafe.Pointer(&countBits)) = count
		for i := range 8 {
			mapData = append(mapData, byte((countBits>>(i*8))&0xFF))
		}

		// Add key-value pairs (only number literals supported for now)
		for i := range e.Keys {
			if keyNum, ok := e.Keys[i].(*NumberExpr); ok {
				keyBits := uint64(0)
				*(*float64)(unsafe.Pointer(&keyBits)) = keyNum.Value
				for j := range 8 {
					mapData = append(mapData, byte((keyBits>>(j*8))&0xFF))
				}
			} else {
				return fmt.Errorf("unsupported map key type for ARM64: %T", e.Keys[i])
			}

			if valNum, ok := e.Values[i].(*NumberExpr); ok {
				valBits := uint64(0)
				*(*float64)(unsafe.Pointer(&valBits)) = valNum.Value
				for j := range 8 {
					mapData = append(mapData, byte((valBits>>(j*8))&0xFF))
				}
			} else {
				return fmt.Errorf("unsupported map value type for ARM64: %T", e.Values[i])
			}
		}

		acg.eb.Define(labelName, string(mapData))

		// Load address into x0
		offset := uint64(acg.eb.text.Len())
		acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{
			offset:     offset,
			symbolName: labelName,
		})
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x90}) // ADRP x0, label@PAGE
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x91}) // ADD x0, x0, label@PAGEOFF

		// Convert pointer to float64
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0

	case *InExpr:
		// Compile value to search for (result in d0)
		if err := acg.compileExpression(e.Value); err != nil {
			return err
		}

		// Save search value to stack
		acg.out.SubImm64("sp", "sp", 16)
		acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x00, 0xfd}) // str d0, [sp]

		// Compile container expression (result in d0 as float64 pointer)
		if err := acg.compileExpression(e.Container); err != nil {
			return err
		}

		// Save container pointer
		acg.out.out.writer.WriteBytes([]byte{0xe0, 0x07, 0x00, 0xfd}) // str d0, [sp, #8]

		// Convert container pointer from float64 to integer in x0
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e}) // fcvtzs x0, d0

		// Load count from container (first 8 bytes)
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x40, 0xfd}) // ldr d1, [x0]

		// Convert count to integer in x1
		acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x78, 0x9e}) // fcvtzs x1, d1

		// x0 = container pointer, x1 = count
		// x2 = loop index (start at 0)
		if err := acg.out.MovImm64("x2", 0); err != nil {
			return err
		}

		// Load search value into d2
		acg.out.out.writer.WriteBytes([]byte{0xe2, 0x03, 0x40, 0xfd}) // ldr d2, [sp]

		// Loop start
		loopStartPos := acg.eb.text.Len()

		// Compare index with count: cmp x2, x1
		acg.out.out.writer.WriteBytes([]byte{0x5f, 0x00, 0x01, 0xeb}) // cmp x2, x1

		// If index >= count, jump to not_found
		notFoundJumpPos := acg.eb.text.Len()
		acg.out.BranchCond("ge", 0) // Placeholder

		// Calculate element address: x0 + 8 + (x2 * 8)
		// x3 = x2 * 8
		acg.out.out.writer.WriteBytes([]byte{0x43, 0xf0, 0x7d, 0xd3}) // lsl x3, x2, #3
		// x3 = x0 + x3
		acg.out.out.writer.WriteBytes([]byte{0x03, 0x00, 0x00, 0x8b}) // add x3, x0, x3
		// x3 = x3 + 8 (skip count)
		if err := acg.out.AddImm64("x3", "x3", 8); err != nil {
			return err
		}

		// Load element into d3
		acg.out.out.writer.WriteBytes([]byte{0x63, 0x00, 0x40, 0xfd}) // ldr d3, [x3]

		// Compare element with search value: fcmp d2, d3
		acg.out.out.writer.WriteBytes([]byte{0x40, 0x20, 0x63, 0x1e})

		// If equal, jump to found
		foundJumpPos := acg.eb.text.Len()
		acg.out.BranchCond("eq", 0) // Placeholder

		// Increment index: x2++
		if err := acg.out.AddImm64("x2", "x2", 1); err != nil {
			return err
		}

		// Jump back to loop start
		loopBackOffset := int32(loopStartPos - (acg.eb.text.Len() + 4))
		acg.out.Branch(loopBackOffset)

		// Not found: return 0.0
		notFoundPos := acg.eb.text.Len()
		acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x67, 0x9e}) // fmov d0, xzr

		// Jump to end
		endJumpPos := acg.eb.text.Len()
		acg.out.Branch(0) // Placeholder

		// Found: return 1.0
		foundPos := acg.eb.text.Len()
		if err := acg.out.MovImm64("x0", 1); err != nil {
			return err
		}
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0

		// End
		endPos := acg.eb.text.Len()

		// Clean up stack
		acg.out.AddImm64("sp", "sp", 16)

		// Patch jumps
		acg.patchJumpOffset(notFoundJumpPos, int32(notFoundPos-notFoundJumpPos))
		acg.patchJumpOffset(foundJumpPos, int32(foundPos-foundJumpPos))
		acg.patchJumpOffset(endJumpPos, int32(endPos-endJumpPos))

	case *ParallelExpr:
		return acg.compileParallelExpr(e)

	case *NamespacedIdentExpr:
		// Handle namespaced identifiers like sdl.SDL_INIT_VIDEO or data.field
		// Check if this is a C constant
		if constants, ok := acg.cConstants[e.Namespace]; ok {
			if value, found := constants.Constants[e.Name]; found {
				// Found a C constant - load it as a number
				if err := acg.out.MovImm64("x0", uint64(value)); err != nil {
					return err
				}
				// Convert to float64: scvtf d0, x0
				acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
				if VerboseMode {
					fmt.Fprintf(os.Stderr, "Resolved C constant %s.%s = %d\n", e.Namespace, e.Name, value)
				}
			} else {
				return fmt.Errorf("undefined constant '%s.%s'", e.Namespace, e.Name)
			}
		} else {
			// Not a C import - treat as field access (obj.field)
			// Convert to IndexExpr and compile it
			hashValue := hashStringKey(e.Name)
			indexExpr := &IndexExpr{
				List:  &IdentExpr{Name: e.Namespace},
				Index: &NumberExpr{Value: float64(hashValue)},
			}
			return acg.compileExpression(indexExpr)
		}

	case *MoveExpr:
		// Compile the expression being moved (loads into d0)
		// The move operator (!) just compiles the inner expression
		// Tracking of moved variables would be done at a higher level
		return acg.compileExpression(e.Expr)

	case *FStringExpr:
		// F-string: concatenate all parts
		if len(e.Parts) == 0 {
			// Empty f-string, return empty string
			return acg.compileExpression(&StringExpr{Value: ""})
		}

		// Compile first part
		firstPart := e.Parts[0]
		// Convert to string if needed
		if acg.getExprType(firstPart) == "string" {
			if err := acg.compileExpression(firstPart); err != nil {
				return err
			}
		} else {
			// Not a string - wrap with str() for conversion
			if err := acg.compileExpression(&CallExpr{
				Function: "str",
				Args:     []Expression{firstPart},
			}); err != nil {
				return err
			}
		}

		// Concatenate remaining parts
		for i := 1; i < len(e.Parts); i++ {
			// Save left pointer (current result) to stack
			acg.stackSize += 8
			leftOffset := acg.stackSize
			offset := int32(16 + leftOffset - 8)
			if err := acg.out.StrImm64Double("d0", "x29", offset); err != nil {
				return err
			}

			// Evaluate right string (next part)
			part := e.Parts[i]
			if acg.getExprType(part) == "string" {
				if err := acg.compileExpression(part); err != nil {
					return err
				}
			} else {
				// Not a string - wrap with str() for conversion
				if err := acg.compileExpression(&CallExpr{
					Function: "str",
					Args:     []Expression{part},
				}); err != nil {
					return err
				}
			}

			// Save right pointer to stack
			acg.stackSize += 8
			rightOffset := acg.stackSize
			offset = int32(16 + rightOffset - 8)
			if err := acg.out.StrImm64Double("d0", "x29", offset); err != nil {
				return err
			}

			// Load arguments: x0 = left ptr, x1 = right ptr
			// Convert float64 pointers to integers
			offset = int32(16 + leftOffset - 8)
			if err := acg.out.LdrImm64Double("d0", "x29", offset); err != nil {
				return err
			}
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e}) // fcvtzs x0, d0

			offset = int32(16 + rightOffset - 8)
			if err := acg.out.LdrImm64Double("d0", "x29", offset); err != nil {
				return err
			}
			acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x78, 0x9e}) // fcvtzs x1, d0

			// Call string_concat(left, right)
			if err := acg.compileCall(&CallExpr{
				Function: "string_concat",
				Args:     []Expression{}, // Args already in registers
			}); err != nil {
				return err
			}

			// Clean up stack (2 slots)
			acg.stackSize -= 16
		}

	case *JumpExpr:
		// Compile the value expression of return/jump statements
		// The value will be left in d0
		if e.Value != nil {
			return acg.compileExpression(e.Value)
		}
		// No value - leave 0.0 in d0
		if err := acg.out.MovImm64("x0", 0); err != nil {
			return err
		}
		// Convert to float64: scvtf d0, x0
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})

	case *BlockExpr:
		// Blocks execute all statements in sequence and return the last expression's value
		if len(e.Statements) == 0 {
			// Empty block: return 0
			return acg.compileExpression(&NumberExpr{Value: 0.0})
		}

		// Execute all statements
		for i, stmt := range e.Statements {
			isLast := (i == len(e.Statements)-1)

			if isLast {
				// For the last statement, make sure its value ends up in d0
				if exprStmt, ok := stmt.(*ExpressionStmt); ok {
					// Expression statement: compile it (result goes to d0)
					return acg.compileExpression(exprStmt.Expr)
				} else if assignStmt, ok := stmt.(*AssignStmt); ok {
					// Assignment: compile it, then load the assigned value into d0
					if err := acg.compileStatement(stmt); err != nil {
						return err
					}
					return acg.compileExpression(&IdentExpr{Name: assignStmt.Name})
				} else if ifStmt, ok := stmt.(*IfStmt); ok {
					// A trailing `if`/`else` is the block's value: compile it as the
					// equivalent value-producing match so its arm result lands in d0.
					return acg.compileExpression(ifStmtToMatchExpr(ifStmt))
				}
			}

			// Not the last statement, or last statement is not an expression/assignment
			if err := acg.compileStatement(stmt); err != nil {
				return err
			}
		}

		// If we get here, the last statement wasn't an expression or assignment
		// Return 0
		return acg.compileExpression(&NumberExpr{Value: 0.0})

	case *CastExpr:
		// For now, just compile the expression being cast
		// Actual type casting would be more complex
		return acg.compileExpression(e.Expr)

	case *PipeExpr:
		// Pipe operator: left | right
		// For now, implement basic scalar pipe (full list mapping would need ParallelExpr)
		leftType := acg.getExprType(e.Left)

		if leftType == "list" {
			// List mapping: would need ParallelExpr support
			return fmt.Errorf("pipe operator on lists not yet supported in ARM64 (requires ParallelExpr)")
		}

		// Scalar pipe: evaluate left, then apply right
		if err := acg.compileExpression(e.Left); err != nil {
			return err
		}

		// For now, just evaluate right (which should use the value in d0)
		// Full implementation would handle lambda calls
		return acg.compileExpression(e.Right)

	case *UnsafeExpr:
		// Inline assembly: execute ARM64-specific block
		if len(e.ARM64Block) > 0 {
			// Compile ARM64 block statements
			for _, stmt := range e.ARM64Block {
				if err := acg.compileStatement(stmt); err != nil {
					return err
				}
			}
			// Handle return value if specified
			if e.ARM64Return != nil {
				// Return value is already in the appropriate register
				// Just need to ensure it's in d0 if it's a float
				return nil
			}
		} else {
			// No ARM64 block - this is expected for x86_64-only unsafe code
			return fmt.Errorf("unsafe block has no ARM64 implementation")
		}

	case *PatternLambdaExpr:
		// Pattern matching lambda with multiple clauses
		// Full implementation would need pattern matching codegen
		return fmt.Errorf("pattern lambdas not yet implemented in ARM64 (requires pattern matching)")

	case *FieldAccessExpr:
		// SROA fast path: `Struct(e0, e1, ...).field` folds straight to the
		// matching argument expression — no allocation, no memory round-trip. After
		// inlining, the hot path is full of these (e.g. `vadd(...).x` becomes
		// `V(a.x+b.x, ...).x` -> `a.x+b.x`), so this keeps intermediate vectors out
		// of the bump arena entirely.
		if ctor, ok := e.Object.(*CallExpr); ok {
			if decl, isStruct := acg.cstructs[ctor.Function]; isStruct && len(ctor.Args) == len(decl.Fields) {
				for i := range decl.Fields {
					if decl.Fields[i].Name == e.FieldName {
						return acg.compileExpression(ctor.Args[i])
					}
				}
			}
		}

		// C-struct field access (p.x) reads raw typed memory at the field offset.
		if decl := acg.cstructForExpr(e.Object); decl != nil {
			for i := range decl.Fields {
				if decl.Fields[i].Name == e.FieldName {
					if err := acg.compileExpression(e.Object); err != nil {
						return err
					}
					acg.out.out.writer.WriteBytes([]byte{0x09, 0x00, 0x78, 0x9e}) // fcvtzs x9, d0 (numeric ptr)
					return acg.emitCStructFieldLoad(&decl.Fields[i])
				}
			}
			return fmt.Errorf("cstruct %s has no field %s", decl.Name, e.FieldName)
		}
		// Otherwise Tim map field access: m.x is sugar for m[hashStringKey("x")].
		indexExpr := &IndexExpr{
			List:  e.Object,
			Index: &NumberExpr{Value: float64(hashStringKey(e.FieldName))},
		}
		return acg.compileExpression(indexExpr)

	default:
		return fmt.Errorf("unsupported expression type for ARM64: %T", expr)
	}

	return nil
}

// compileAssignment compiles an assignment statement
func (acg *ARM64CodeGen) compileAssignment(assign *AssignStmt) error {
	// Module globals: store to the shared heap slot based at x28. Define and
	// update both resolve to the same slot (capture-by-reference).
	if slot, isGlobal := acg.globalSlots[assign.Name]; isGlobal {
		if assign.IsUpdate && !acg.globalMutable[assign.Name] {
			return fmt.Errorf("cannot update immutable variable '%s' (use <- only for mutable variables)", assign.Name)
		}
		oldName := acg.currentAssignName
		acg.currentAssignName = assign.Name
		if err := acg.compileExpression(assign.Value); err != nil {
			return err
		}
		acg.currentAssignName = oldName
		acg.markIfClosure(assign.Name, assign.Value)
		acg.varTypes[assign.Name] = acg.getExprType(assign.Value)
		return acg.out.StrImm64Double("d0", "x28", int32(slot*8))
	}

	// Validate assignment semantics
	_, exists := acg.stackVars[assign.Name]
	isMutable := acg.mutableVars[assign.Name]

	// A nested closure updating a variable it captured by reference: the name is
	// not a local of this lambda but is one of its boxed captures.
	isBoxedCaptureUpdate := assign.IsUpdate && !exists && acg.boxedVars[assign.Name] &&
		acg.currentLambda != nil && slices.Contains(acg.currentLambda.Captures, assign.Name)

	if assign.IsUpdate {
		// <- Update existing mutable variable
		if !exists && !isBoxedCaptureUpdate {
			return fmt.Errorf("cannot update undefined variable '%s'", assign.Name)
		}
		if exists && !isMutable {
			return fmt.Errorf("cannot update immutable variable '%s' (use <- only for mutable variables)", assign.Name)
		}
	} else if assign.Mutable {
		// := Define mutable variable
		if exists {
			return fmt.Errorf("variable '%s' already defined (use <- to update)", assign.Name)
		}
	} else {
		// = Define immutable variable (can shadow existing immutable, but not mutable)
		// HOWEVER: if variable exists and is mutable, allow update (don't create new variable)
		if exists && isMutable {
			// Allow updating existing mutable variable with =
			// Don't need to create new variable, just proceed with code generation
			assign.IsReuseMutable = true
		}
	}

	// Set the assignment name context for lambda self-reference
	oldAssignName := acg.currentAssignName
	acg.currentAssignName = assign.Name

	// Compile the value
	if err := acg.compileExpression(assign.Value); err != nil {
		return err
	}

	// Restore previous assignment context
	acg.currentAssignName = oldAssignName

	// Boxed variables live in a heap cell shared with nested closures: store the
	// value through the cell pointer rather than into the stack slot directly.
	// This covers both `<-` updates of a boxed local and a closure updating a
	// boxed capture by reference.
	if assign.IsUpdate && (isBoxedCaptureUpdate || acg.boxedVars[assign.Name]) {
		return acg.emitStoreBoxedVar(assign.Name)
	}

	var offset int32
	if assign.IsUpdate {
		// <- Update existing mutable variable - look up its offset
		stackOffset := acg.stackVars[assign.Name]
		offset = int32(16 + stackOffset - 8)
	} else {
		// = or := - Allocate stack space for new variable (8-byte aligned)
		// This includes shadowing for immutable variables
		// HOWEVER: if updating existing mutable variable with =, reuse its offset
		if assign.IsReuseMutable {
			// Reuse existing variable's offset (update with =)
			stackOffset := acg.stackVars[assign.Name]
			offset = int32(16 + stackOffset - 8)
			if VerboseMode {
				debugf("DEBUG: Updating existing mutable variable '%s' at offset=%d\n",
					assign.Name, offset)
			}
		} else {
			// Allocate new stack space for new variable or shadowing
			// Variables are stored at positive offsets from frame pointer
			acg.stackSize += 8
			acg.stackVars[assign.Name] = acg.stackSize
			acg.mutableVars[assign.Name] = assign.Mutable
			if VerboseMode {
				debugf("DEBUG: Allocated variable '%s' at stackSize=%d (offset from fp=%d)\n",
					assign.Name, acg.stackSize, 16+acg.stackSize-8)
			}
			// Track the type of the value being assigned
			acg.varTypes[assign.Name] = acg.getExprType(assign.Value)
			// Track if this is a lambda/function (or a variable holding a closure
			// returned from a call) so it can be invoked, including with no args.
			acg.markIfClosure(assign.Name, assign.Value)
			// Track cstruct-typed pointers (e.g. `p := malloc(n) as Point`,
			// `p = Point(...)`, or `p = f(...)` where f returns a cstruct).
			if ct := acg.cStructTypeOf(assign.Value); ct != "" {
				acg.varCStructType[assign.Name] = ct
			}
			// Track lists of cstructs so `xs[i].field` resolves the element layout.
			if et := acg.listElemCStructTypeOf(assign.Value); et != "" {
				acg.varListElemType[assign.Name] = et
			}
			// x29 points to saved fp location, variables start at offset 16
			offset = int32(16 + acg.stackSize - 8)

			// A new mutable local that a nested closure mutates is boxed: its
			// slot holds a heap cell pointer instead of the value.
			if acg.boxedVars[assign.Name] {
				return acg.emitBoxedDeclaration(offset)
			}
		}
	}

	// Store result on stack: str d0, [x29, #offset]
	return acg.out.StrImm64Double("d0", "x29", offset)
}

// compileMatchExpr compiles a match expression (if/else equivalent)
func (acg *ARM64CodeGen) compileMatchExpr(expr *MatchExpr) error {
	// Compile the condition expression (result in d0)
	if err := acg.compileExpression(expr.Condition); err != nil {
		return err
	}

	// The condition is only needed by value-match clauses, guardless `cond { … }`
	// clauses, and the no-clause form. A plain guard match (`{ | g => r ~> d }`)
	// never reads it — so skip spilling it (and the per-clause reload) entirely,
	// which is the common case in heavily-branchy code.
	condUsed := len(expr.Clauses) == 0
	for _, c := range expr.Clauses {
		if c.IsValueMatch || c.Guard == nil {
			condUsed = true
			break
		}
	}
	if condUsed {
		acg.out.SubImm64("sp", "sp", 16)
		if err := acg.out.StrImm64Double("d0", "sp", 0); err != nil {
			return err
		}
	}

	var endJumpPositions []int

	for _, clause := range expr.Clauses {
		var nextClauseJumpPos int

		if clause.IsValueMatch {
			// Compare condition (d0) with guard value. (Note: the parser usually
			// rewrites value matches into a `condition == guard` boolean guard, so
			// string matches are handled by the == operator's string path below.)
			if err := acg.compileExpression(clause.Guard); err != nil {
				return err
			}
			// Guard value in d0, move to d1
			acg.out.out.writer.WriteBytes([]byte{0x01, 0x40, 0x60, 0x1e}) // fmov d1, d0
			// Load condition back to d0
			if err := acg.out.LdrImm64Double("d0", "sp", 0); err != nil {
				return err
			}
			// fcmp d0, d1
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e})

			// Jump to next clause if not equal
			nextClauseJumpPos = acg.eb.text.Len()
			acg.out.BranchCond("ne", 0)
		} else if clause.Guard != nil {
			// Boolean guard: evaluate and check if non-zero
			if err := acg.compileExpression(clause.Guard); err != nil {
				return err
			}
			// fcmp d0, #0.0 (0.0 is not FMOV-immediate-encodable; zero via xzr)
			acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x67, 0x9e}) // fmov d1, xzr
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e}) // fcmp d0, d1

			// Jump to next clause if false (== 0.0)
			nextClauseJumpPos = acg.eb.text.Len()
			acg.out.BranchCond("eq", 0)
		} else {
			// Guardless clause from the `cond { body }` form: the condition itself
			// is the boolean test. Load it (it was spilled) and run the body if true.
			if err := acg.out.LdrImm64Double("d0", "sp", 0); err != nil {
				return err
			}
			acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x67, 0x9e}) // fmov d1, xzr
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e}) // fcmp d0, d1
			nextClauseJumpPos = acg.eb.text.Len()
			acg.out.BranchCond("eq", 0) // skip body if condition == 0
		}

		// Matched! Compile result
		if clause.Result != nil {
			if err := acg.compileExpression(clause.Result); err != nil {
				return err
			}
		}

		// Jump to end
		endJumpPos := acg.eb.text.Len()
		acg.out.Branch(0)
		endJumpPositions = append(endJumpPositions, endJumpPos)

		// Every clause now emits a guard branch; patch it to skip to the next clause.
		currentPos := acg.eb.text.Len()
		acg.patchJumpOffset(nextClauseJumpPos, int32(currentPos-nextClauseJumpPos))
	}

	// Default clause
	if expr.DefaultExpr != nil {
		if err := acg.compileExpression(expr.DefaultExpr); err != nil {
			return err
		}
	} else if len(expr.Clauses) == 0 {
		// No clauses and no default - restore condition
		if err := acg.out.LdrImm64Double("d0", "sp", 0); err != nil {
			return err
		}
	} else {
		// Default is 0.0 (0.0 is not FMOV-immediate-encodable; zero via xzr)
		acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x67, 0x9e}) // fmov d0, xzr
	}

	// End position
	endPos := acg.eb.text.Len()

	// Patch all end jumps
	for _, jumpPos := range endJumpPositions {
		offset := int32(endPos - jumpPos)
		acg.patchJumpOffset(jumpPos, offset)
	}

	// Clean up the condition spill slot (only allocated when condUsed).
	if condUsed {
		acg.out.AddImm64("sp", "sp", 16)
	}

	return nil
}

// patchJumpOffset patches a branch instruction's offset
func (acg *ARM64CodeGen) patchJumpOffset(pos int, offset int32) {
	// ARM64 branch offsets are in words (4 bytes), not bytes
	if offset%4 != 0 {
		// Offset not aligned - this shouldn't happen but handle gracefully
		offset = (offset >> 2) << 2
	}

	imm := offset >> 2 // Convert to word offset

	textBytes := acg.eb.text.Bytes()

	// Read existing instruction
	instr := uint32(textBytes[pos]) | (uint32(textBytes[pos+1]) << 8) |
		(uint32(textBytes[pos+2]) << 16) | (uint32(textBytes[pos+3]) << 24)

	// Check if it's a conditional branch (B.cond) or unconditional branch (B)
	if (instr & 0xff000010) == 0x54000000 {
		// Conditional branch: B.cond - imm19 at bits [23:5]
		instr = (instr & 0xff00001f) | ((uint32(imm) & 0x7ffff) << 5)
	} else if (instr & 0x7e000000) == 0x34000000 {
		// Compare-and-branch: CBZ/CBNZ - imm19 at bits [23:5]
		instr = (instr & 0xff00001f) | ((uint32(imm) & 0x7ffff) << 5)
	} else if (instr & 0xfc000000) == 0x14000000 {
		// Unconditional branch: B - imm26 at bits [25:0]
		instr = (instr & 0xfc000000) | (uint32(imm) & 0x3ffffff)
	}

	// Write back patched instruction
	textBytes[pos] = byte(instr)
	textBytes[pos+1] = byte(instr >> 8)
	textBytes[pos+2] = byte(instr >> 16)
	textBytes[pos+3] = byte(instr >> 24)
}

// compileParallelExpr compiles a parallel map operation (||)
func (acg *ARM64CodeGen) compileParallelExpr(expr *ParallelExpr) error {
	// For now, only support: list || lambda
	lambda, ok := expr.Operation.(*LambdaExpr)
	if !ok {
		return fmt.Errorf("parallel operator (||) currently only supports lambda expressions")
	}

	if len(lambda.Params) != 1 {
		return fmt.Errorf("parallel operator lambda must have exactly one parameter")
	}

	const (
		parallelResultAlloc    = 2080
		lambdaScratchOffset    = parallelResultAlloc - 8
		savedLambdaSpillOffset = parallelResultAlloc + 8
	)

	// Compile the lambda to get its function pointer (result in d0)
	if err := acg.compileExpression(expr.Operation); err != nil {
		return err
	}

	// Save lambda function pointer (currently in d0) to stack
	// str d0, [sp, #-16]! (pre-indexed: decrement sp by 16, then store)
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0xef, 0x1f, 0xfd})
	// Convert d0 to integer pointer: fmov x11, d0
	acg.out.out.writer.WriteBytes([]byte{0x0b, 0x00, 0x67, 0x9e})
	// Save integer pointer: str x11, [sp, #8]
	acg.out.out.writer.WriteBytes([]byte{0xeb, 0x07, 0x00, 0xf9})

	// Compile the input list expression (returns pointer as float64 in d0)
	if err := acg.compileExpression(expr.List); err != nil {
		return err
	}

	// Save list pointer and load as integer pointer
	// str d0, [sp, #-8]! (pre-indexed: decrement sp by 8, then store)
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0xff, 0x1f, 0xfd})
	// Load as integer: ldr x13, [sp]
	acg.out.out.writer.WriteBytes([]byte{0xed, 0x03, 0x40, 0xf9})

	// Load list length from [x13] into x14
	// ldr d0, [x13]
	acg.out.out.writer.WriteBytes([]byte{0xa0, 0x01, 0x40, 0xfd})
	// fcvtzs x14, d0 - convert float64 to int64
	acg.out.out.writer.WriteBytes([]byte{0x0e, 0x00, 0x78, 0x9e})

	// Allocate result list on stack
	// sub sp, sp, #parallelResultAlloc
	if err := acg.out.SubImm64("sp", "sp", parallelResultAlloc); err != nil {
		return err
	}

	// Store result list pointer in x12
	// mov x12, sp
	acg.out.out.writer.WriteBytes([]byte{0xec, 0x03, 0x00, 0x91})

	// Move the saved lambda pointer into the reserved scratch slot
	// ldr x10, [x12, #savedLambdaSpillOffset]
	spillOffsetImm := (savedLambdaSpillOffset / 8) << 10
	strInstr := uint32(0xf9400000) | uint32(10) | uint32(12<<5) | uint32(spillOffsetImm)
	acg.out.out.writer.WriteBytes([]byte{
		byte(strInstr),
		byte(strInstr >> 8),
		byte(strInstr >> 16),
		byte(strInstr >> 24),
	})
	// str x10, [x12, #lambdaScratchOffset]
	scratchOffsetImm := (lambdaScratchOffset / 8) << 10
	strInstr = uint32(0xf9000000) | uint32(10) | uint32(12<<5) | uint32(scratchOffsetImm)
	acg.out.out.writer.WriteBytes([]byte{
		byte(strInstr),
		byte(strInstr >> 8),
		byte(strInstr >> 16),
		byte(strInstr >> 24),
	})

	// Store length in result list
	// ldr d0, [x13]
	acg.out.out.writer.WriteBytes([]byte{0xa0, 0x01, 0x40, 0xfd})
	// str d0, [x12]
	acg.out.out.writer.WriteBytes([]byte{0x80, 0x01, 0x00, 0xfd})

	// Initialize loop counter to 0
	// mov x15, xzr
	acg.out.out.writer.WriteBytes([]byte{0xef, 0x03, 0x1f, 0xaa})

	// Loop start
	loopStart := acg.eb.text.Len()

	// Check if index >= length: cmp x15, x14
	acg.out.out.writer.WriteBytes([]byte{0xdf, 0x01, 0x0e, 0xeb})
	// b.ge loop_end
	loopEndJumpPos := acg.eb.text.Len()
	acg.out.BranchCond("ge", 0) // Placeholder

	// Load element from input list: input_list[index]
	// Element address = x13 + 8 + (x15 * 8)
	// mov x0, x15
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x0f, 0xaa})
	// lsl x0, x0, #3 (multiply by 8)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0xf0, 0x7d, 0xd3})
	// add x0, x0, #8 (skip length)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x00, 0x91})
	// add x0, x0, x13 (x0 = address of element)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x0d, 0x8b})

	// Load element into d0
	// ldr d0, [x0]
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x40, 0xfd})

	// Save loop index x15 to stack (will be clobbered by environment pointer)
	// str x15, [sp, #-16]! (pre-indexed: decrement sp by 16, then store)
	acg.out.out.writer.WriteBytes([]byte{0xef, 0xef, 0x1f, 0xf8})

	// Load lambda closure object pointer from scratch slot
	// ldr x0, [x12, #lambdaScratchOffset]
	scratchOffsetImm = (lambdaScratchOffset / 8) << 10
	ldrInstr := uint32(0xf9400000) | uint32(0) | uint32(12<<5) | uint32(scratchOffsetImm)
	acg.out.out.writer.WriteBytes([]byte{
		byte(ldrInstr),
		byte(ldrInstr >> 8),
		byte(ldrInstr >> 16),
		byte(ldrInstr >> 24),
	})

	// Extract function pointer from closure object (offset 0)
	// ldr x11, [x0, #0]
	acg.out.out.writer.WriteBytes([]byte{0x0b, 0x00, 0x40, 0xf9})

	// Extract environment pointer from closure object (offset 8) into x15
	// ldr x15, [x0, #8]
	acg.out.out.writer.WriteBytes([]byte{0x0f, 0x04, 0x40, 0xf9})

	// Call the lambda function with environment in x15: blr x11
	acg.out.out.writer.WriteBytes([]byte{0x60, 0x01, 0x3f, 0xd6})

	// Restore loop index from stack
	// ldr x15, [sp], #16
	acg.out.out.writer.WriteBytes([]byte{0xef, 0x07, 0x41, 0xf8})

	// Result is in d0, store it in output list: result_list[index]
	// mov x0, x15
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x0f, 0xaa})
	// lsl x0, x0, #3
	acg.out.out.writer.WriteBytes([]byte{0x00, 0xf0, 0x7d, 0xd3})
	// add x0, x0, #8
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x00, 0x91})
	// add x0, x0, x12
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x0c, 0x8b})
	// str d0, [x0]
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0xfd})

	// Increment index: add x15, x15, #1
	acg.out.out.writer.WriteBytes([]byte{0xef, 0x05, 0x00, 0x91})

	// Jump back to loop start
	loopBackJumpPos := acg.eb.text.Len()
	backOffset := int32(loopStart - loopBackJumpPos)
	acg.out.Branch(backOffset)

	// Loop end
	loopEndPos := acg.eb.text.Len()

	// Patch conditional jump
	acg.patchJumpOffset(loopEndJumpPos, int32(loopEndPos-loopEndJumpPos))

	// Return result list pointer as float64 in d0
	// mov x0, x12
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x0c, 0xaa})
	// scvtf d0, x0 - convert pointer to float64
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})

	// Adjust stack pointer
	// add sp, sp, #(parallelResultAlloc + 16 + 8)
	if err := acg.out.AddImm64("sp", "sp", parallelResultAlloc+24); err != nil {
		return err
	}

	return nil
}

// compileCall compiles a function call
// Confidence that this function is working: 75%
func (acg *ARM64CodeGen) compileCall(call *CallExpr) error {
	// CStruct value constructor: Point(x, y, ...) allocates the struct value.
	if decl, ok := acg.cstructs[call.Function]; ok {
		return acg.compileCStructConstructor(decl, call.Args)
	}

	// Check if this is a namespaced call (e.g., sdl.SDL_Init, c.sin)
	if strings.Contains(call.Function, ".") {
		parts := strings.SplitN(call.Function, ".", 2)
		if len(parts) == 2 {
			namespace := parts[0]
			funcName := parts[1]

			// Check if this is a C library function call
			if constants, ok := acg.cConstants[namespace]; ok {
				// Check if function signature exists in C header
				if sig, found := constants.Functions[funcName]; found {
					if VerboseMode {
						fmt.Fprintf(os.Stderr, "Calling C function %s.%s with signature: %s %s(...)\n",
							namespace, funcName, sig.ReturnType, funcName)
					}
					// Compile as external C function call
					return acg.compileCFunctionCall(funcName, call.Args, sig)
				}
				return fmt.Errorf("undefined C function '%s.%s'", namespace, funcName)
			}
			// If the "namespace" is actually a variable, this is method-call
			// syntax: desugar xs.append(a) -> append(xs, a) and fall through.
			if _, isVar := acg.stackVars[namespace]; isVar {
				call.Function = funcName
				call.Args = append([]Expression{&IdentExpr{Name: namespace}}, call.Args...)
			} else {
				// Not a C import - might be a method call or other namespaced access
				return fmt.Errorf("undefined namespace '%s' for function call", namespace)
			}
		}
	}

	// C.printf / c.printf explicitly select the C library implementation,
	// whereas the bare printf builtin uses Tim's own formatter.
	if call.IsCFFI && call.Function == "printf" {
		return acg.compilePrintf(call)
	}

	switch call.Function {
	case "println":
		return acg.compilePrintln(call)
	case "str":
		if len(call.Args) != 1 {
			return fmt.Errorf("str() requires exactly 1 argument")
		}
		if err := acg.compileExpression(call.Args[0]); err != nil {
			return err
		}
		// Arg in d0
		if err := acg.eb.GenerateCallInstruction("_tim_str"); err != nil {
			return err
		}
		// Result is a string pointer in x0. Strings use the numeric pointer
		// convention (scvtf to store, fcvtzs to recover) — matching string
		// literals and _tim_string_concat — so convert with scvtf, not fmov.
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0
		return nil
	case "eprint", "eprintln", "eprintf":
		return acg.compileEprint(call)
	case "exit":
		return acg.compileExit(call)
	case "exitf", "exitln":
		return acg.compileExitf(call)
	case "print":
		return acg.compilePrint(call)
	case "getpid":
		return acg.compileGetPid(call)
	case "printf":
		// Tim's own formatter (no libc). C.printf / c.printf still routes to
		// the C library via the namespaced-call path.
		return acg.compilePrintfNative(call, 1)
	case "me":
		// Tail recursion - only valid inside a lambda
		if acg.currentLambda == nil {
			return fmt.Errorf("'me' keyword can only be used inside a lambda")
		}
		return acg.compileTailCall(call)
	case "call":
		return acg.compileFFICall(call)
	case "alloc":
		return acg.compileAlloc(call)
	case "popcount", "clz", "ctz":
		return acg.compileBitCount(call, call.Function)
	case "head":
		return acg.compileHead(call)
	case "tail":
		return acg.compileTail(call)
	case "append":
		return acg.compileAppend(call)
	case "pop":
		return acg.compilePop(call)
	case "is_nan":
		return acg.compileIsNan(call)
	case "error":
		return acg.compileError(call)
	case "_error_code_extract":
		return acg.compileErrorCodeExtract(call)
	case "string_concat":
		// Internal string concatenation function
		// Arguments should already be in x0 and x1
		if err := acg.eb.GenerateCallInstruction("_tim_string_concat"); err != nil {
			return err
		}
		// Result pointer in x0 -> numeric double convention in d0.
		return acg.out.ScvtfInt64ToDouble("d0", "x0")
	case "write_i8", "write_i16", "write_i32", "write_i64",
		"write_u8", "write_u16", "write_u32", "write_u64", "write_f64":
		return acg.compileMemoryWrite(call)
	case "read_i8", "read_i16", "read_i32", "read_i64",
		"read_u8", "read_u16", "read_u32", "read_u64", "read_f64":
		return acg.compileMemoryRead(call)
	case "dlopen":
		// dlopen(path, flags)
		sig := &CFunctionSignature{
			ReturnType: "void*",
			Params: []CFunctionParam{
				{Type: "const char*", Name: "path"},
				{Type: "int", Name: "mode"},
			},
		}
		return acg.compileCFunctionCall("dlopen", call.Args, sig)
	case "dlsym":
		// dlsym(handle, symbol)
		sig := &CFunctionSignature{
			ReturnType: "void*",
			Params: []CFunctionParam{
				{Type: "void*", Name: "handle"},
				{Type: "const char*", Name: "symbol"},
			},
		}
		return acg.compileCFunctionCall("dlsym", call.Args, sig)
	case "dlclose":
		// dlclose(handle)
		sig := &CFunctionSignature{
			ReturnType: "int",
			Params: []CFunctionParam{
				{Type: "void*", Name: "handle"},
			},
		}
		return acg.compileCFunctionCall("dlclose", call.Args, sig)
	case "dlerror":
		// dlerror()
		sig := &CFunctionSignature{
			ReturnType: "char*",
			Params:     []CFunctionParam{},
		}
		return acg.compileCFunctionCall("dlerror", call.Args, sig)
	default:
		// Check if this is a self-recursive call within a lambda
		if acg.currentLambda != nil && call.Function == acg.currentLambda.VarName {
			// Mark lambda as recursive
			acg.currentLambda.IsRecursive = true
			// This is a recursive call - compile arguments and call current function
			return acg.compileSelfRecursiveCall(call)
		}

		// Check if it's a variable holding a function pointer or value
		if _, exists := acg.stackVars[call.Function]; exists {
			// Check if this is actually a lambda/function or just a value
			isLambda := acg.lambdaVars[call.Function]

			// If calling a non-lambda value with no args, just return the value
			if !isLambda && len(call.Args) == 0 {
				stackOffset := acg.stackVars[call.Function]
				offset := int32(16 + stackOffset - 8)
				if err := acg.out.LdrImm64Double("d0", "x29", offset); err != nil {
					return err
				}
				return nil
			}

			// Convert to DirectCallExpr and compile
			directCall := &DirectCallExpr{
				Callee: &IdentExpr{Name: call.Function},
				Args:   call.Args,
			}
			return acg.compileDirectCall(directCall)
		}

		// Hardware-instruction fast path: these map to a single ARM64 FP op, so
		// emit it inline instead of a libm call (which on macOS also pays an
		// expensive dynamic-link stub indirection — the stub cost dominated sqrt
		// in profiles of the metaballs). Must run BEFORE the implicit-`c` libm
		// route below, which would otherwise emit a call for sqrt/floor/etc.
		if op, ok := arm64UnaryFPOps[call.Function]; ok && len(call.Args) == 1 {
			if err := acg.compileExpression(call.Args[0]); err != nil { // arg in d0
				return err
			}
			acg.out.out.writer.WriteBytes([]byte{byte(op), byte(op >> 8), byte(op >> 16), byte(op >> 24)})
			return nil
		}

		// Check if it's a C function from the "c" namespace (implicit)
		// This handles bare function names like sin(), cos(), etc. from libm
		if constants, ok := acg.cConstants["c"]; ok {
			if sig, found := constants.Functions[call.Function]; found {
				if VerboseMode {
					fmt.Fprintf(os.Stderr, "Calling implicit C function c.%s\n", call.Function)
				}
				return acg.compileCFunctionCall(call.Function, call.Args, sig)
			}
		}

		// This is critical for macOS/ARM64 where header parsing might be flaky in tests
		switch call.Function {
		case "sin", "cos", "tan", "asin", "acos", "atan",
			"cbrt", "exp", "exp2", "log", "log2", "log10":
			// Single-argument libm functions: double f(double)
			sig := &CFunctionSignature{
				ReturnType: "double",
				Params:     []CFunctionParam{{Type: "double", Name: "x"}},
			}
			return acg.compileCFunctionCall(call.Function, call.Args, sig)
		case "pow", "atan2", "fmod", "hypot", "copysign", "fdim", "fmax", "fmin", "nextafter":
			// Two-argument libm functions: double f(double, double)
			sig := &CFunctionSignature{
				ReturnType: "double",
				Params: []CFunctionParam{
					{Type: "double", Name: "x"},
					{Type: "double", Name: "y"},
				},
			}
			return acg.compileCFunctionCall(call.Function, call.Args, sig)
		case "malloc":
			sig := &CFunctionSignature{
				ReturnType: "void*",
				Params:     []CFunctionParam{{Type: "size_t", Name: "size"}},
			}
			return acg.compileCFunctionCall(call.Function, call.Args, sig)
		case "free":
			sig := &CFunctionSignature{
				ReturnType: "void",
				Params:     []CFunctionParam{{Type: "void*", Name: "ptr"}},
			}
			return acg.compileCFunctionCall(call.Function, call.Args, sig)
		case "strlen":
			sig := &CFunctionSignature{
				ReturnType: "size_t",
				Params:     []CFunctionParam{{Type: "const char*", Name: "s"}},
			}
			return acg.compileCFunctionCall(call.Function, call.Args, sig)
		case "fork", "getpid":
			sig := &CFunctionSignature{ReturnType: "int", Params: nil}
			return acg.compileCFunctionCall(call.Function, call.Args, sig)
		case "mmap":
			sig := &CFunctionSignature{
				ReturnType: "void*",
				Params: []CFunctionParam{
					{Type: "void*", Name: "addr"}, {Type: "size_t", Name: "len"},
					{Type: "int", Name: "prot"}, {Type: "int", Name: "flags"},
					{Type: "int", Name: "fd"}, {Type: "long", Name: "offset"},
				},
			}
			return acg.compileCFunctionCall(call.Function, call.Args, sig)
		case "munmap":
			sig := &CFunctionSignature{
				ReturnType: "int",
				Params:     []CFunctionParam{{Type: "void*", Name: "addr"}, {Type: "size_t", Name: "len"}},
			}
			return acg.compileCFunctionCall(call.Function, call.Args, sig)
		case "waitpid":
			sig := &CFunctionSignature{
				ReturnType: "int",
				Params: []CFunctionParam{
					{Type: "int", Name: "pid"}, {Type: "void*", Name: "status"}, {Type: "int", Name: "options"},
				},
			}
			return acg.compileCFunctionCall(call.Function, call.Args, sig)
		case "proc_exit":
			// Immediate process exit (libc _exit): no atexit handlers, no stdio
			// flush, no framework teardown — essential for a forked worker that
			// must not run the parent's Metal/CoreFoundation cleanup.
			sig := &CFunctionSignature{ReturnType: "void", Params: []CFunctionParam{{Type: "int", Name: "code"}}}
			return acg.compileCFunctionCall("_exit", call.Args, sig)
		}

		// A call to a module-level named function that isn't visible as a stack
		// variable in this scope (e.g. recursion or a function referenced from a
		// different lambda). Emit a direct branch to its generated label.
		for idx := range acg.lambdaFuncs {
			if acg.lambdaFuncs[idx].VarName == call.Function {
				return acg.compileNamedCall(acg.lambdaFuncs[idx].Name, call.Args)
			}
		}

		return fmt.Errorf("unsupported function for ARM64: %s", call.Function)
	}
}

// compileNamedCall emits a direct call (bl) to a generated function label,
// passing arguments in d0-d7 per AAPCS64. The branch is left as a placeholder
// and resolved by the writer's internal call-patching using eb.labels.
func (acg *ARM64CodeGen) compileNamedCall(label string, args []Expression) error {
	if len(args) > 8 {
		return fmt.Errorf("too many arguments to call %s (max 8)", label)
	}
	// Evaluate arguments left to right, spilling each to the stack.
	for _, arg := range args {
		if err := acg.compileExpression(arg); err != nil {
			return err
		}
		acg.out.SubImm64("sp", "sp", 16)
		acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x00, 0xfd}) // str d0, [sp]
	}
	// Reload into d0..d(n-1) (reverse, since the last pushed is on top).
	for i := len(args) - 1; i >= 0; i-- {
		instr := uint32(0xfd400000) | uint32(i) | (31 << 5) // ldr dN, [sp]
		acg.out.out.writer.WriteBytes([]byte{byte(instr), byte(instr >> 8), byte(instr >> 16), byte(instr >> 24)})
		acg.out.AddImm64("sp", "sp", 16)
	}
	// bl <label> placeholder, patched to the internal function by the writer.
	pos := acg.eb.text.Len()
	acg.eb.callPatches = append(acg.eb.callPatches, CallPatch{position: pos, targetName: label})
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x94}) // bl #0
	// Result is in d0.
	return nil
}

// compileSelfRecursiveCall compiles a self-recursive call within a lambda
func (acg *ARM64CodeGen) compileSelfRecursiveCall(call *CallExpr) error {
	// Evaluate all arguments and save to stack
	for _, arg := range call.Args {
		if err := acg.compileExpression(arg); err != nil {
			return err
		}
		// Result in d0, save to stack
		acg.out.SubImm64("sp", "sp", 16)
		// str d0, [sp]
		acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x00, 0xfd})
	}

	// Load arguments from stack into d0-d7 registers (in reverse order)
	// ARM64 AAPCS64 passes float args in d0-d7
	if len(call.Args) > 8 {
		return fmt.Errorf("too many arguments to recursive call (max 8)")
	}

	for i := len(call.Args) - 1; i >= 0; i-- {
		// ldr dN, [sp]
		regNum := uint32(i)
		instr := uint32(0xfd400000) | (regNum) | (31 << 5) // ldr dN, [sp, #0]
		acg.out.out.writer.WriteBytes([]byte{
			byte(instr),
			byte(instr >> 8),
			byte(instr >> 16),
			byte(instr >> 24),
		})
		acg.out.AddImm64("sp", "sp", 16)
	}

	// Call the current lambda function recursively
	// BL to the start of the current lambda function (including prologue)
	// BL instruction format: 0x94000000 | ((offset >> 2) & 0x03ffffff)
	// offset is in bytes from current position to target

	currentPos := acg.eb.text.Len()
	targetPos := acg.currentLambda.FuncStart
	offset := targetPos - currentPos

	// BL uses signed 26-bit offset in instructions (multiply by 4 for bytes)
	instrOffset := int32(offset >> 2)
	if instrOffset < -0x2000000 || instrOffset > 0x1ffffff {
		return fmt.Errorf("recursive call offset too large: %d", offset)
	}

	blInstr := uint32(0x94000000) | (uint32(instrOffset) & 0x03ffffff)
	acg.out.out.writer.WriteBytes([]byte{
		byte(blInstr),
		byte(blInstr >> 8),
		byte(blInstr >> 16),
		byte(blInstr >> 24),
	})

	// Result is in d0
	return nil
}

// compilePrint compiles a print call (without newline) using Tim's own,
// libc-free output routines.
func (acg *ARM64CodeGen) compilePrintLibc(arg Expression) error {
	// String literal: write its bytes directly.
	if strExpr, ok := arg.(*StringExpr); ok {
		return acg.emitWriteLiteral(strExpr.Value, 1)
	}
	// String-typed expression: walk the Tim string.
	if acg.getExprType(arg) == "string" {
		if err := acg.compileExpression(arg); err != nil {
			return err
		}
		return acg.emitWriteTimString(1)
	}
	// Numbers print as their integer value (print() has no fractional form).
	if err := acg.compileExpression(arg); err != nil {
		return err
	}
	return acg.emitWriteInteger(1)
}

func (acg *ARM64CodeGen) compilePrint(call *CallExpr) error {
	if len(call.Args) == 0 {
		return fmt.Errorf("print requires an argument")
	}

	arg := call.Args[0]

	// On macOS, use libc printf for better compatibility
	if acg.eb.target.OS() == OSDarwin {
		return acg.compilePrintLibc(arg)
	}

	switch a := arg.(type) {
	case *StringExpr:
		// Store string in rodata
		label := fmt.Sprintf("str_%d", acg.stringCounter)
		acg.stringCounter++
		content := a.Value // No newline
		acg.eb.Define(label, content)

		// mov x0, #1 (stdout)
		if err := acg.out.MovImm64("x0", 1); err != nil {
			return err
		}

		// Load string address into x1
		offset := uint64(acg.eb.text.Len())
		acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{
			offset:     offset,
			symbolName: label,
		})
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0x90}) // ADRP x1, #0
		acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x00, 0x91}) // ADD x1, x1, #0

		// mov x2, length
		if err := acg.out.MovImm64("x2", uint64(len(content))); err != nil {
			return err
		}

		// Syscall number and invocation (OS-specific)
		if acg.eb.target.OS() == OSDarwin {
			// macOS: syscall number in x16, svc #0x80
			if err := acg.out.MovImm64("x16", 0x2000004); err != nil { // write syscall
				return err
			}
			acg.out.out.writer.WriteBytes([]byte{0x01, 0x10, 0x00, 0xd4}) // svc #0x80
		} else {
			// Linux: syscall number in x8, svc #0
			if err := acg.out.MovImm64("x8", 64); err != nil { // write syscall = 64
				return err
			}
			acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0xd4}) // svc #0
		}

	default:
		return fmt.Errorf("unsupported print argument type for ARM64: %T", arg)
	}

	return nil
}

// compilePrintlnLibc compiles println on macOS. Strings are written with Tim's
// own (libc-free) routines; numbers still use libc printf("%.15g") for smart
// shortest formatting.
func (acg *ARM64CodeGen) compilePrintlnLibc(arg Expression) error {
	// String literal: write bytes + newline directly.
	if strExpr, ok := arg.(*StringExpr); ok {
		return acg.emitWriteLiteral(strExpr.Value+"\n", 1)
	}
	// String-typed expression (variable, concatenation, str(), …): walk the
	// Tim string and emit its characters, then a newline.
	if acg.getExprType(arg) == "string" {
		if err := acg.compileExpression(arg); err != nil {
			return err
		}
		if err := acg.emitWriteTimString(1); err != nil {
			return err
		}
		return acg.emitWriteLiteral("\n", 1)
	}

	// Numbers: Tim's own smart float format (libc-free), then a newline.
	if err := acg.compileExpression(arg); err != nil {
		return err
	}
	if err := acg.emitWriteFloatSmart(1); err != nil {
		return err
	}
	return acg.emitWriteLiteral("\n", 1)
}

// compilePrintln compiles a println call
func (acg *ARM64CodeGen) compilePrintln(call *CallExpr) error {
	if len(call.Args) == 0 {
		return fmt.Errorf("println requires an argument")
	}

	arg := call.Args[0]

	// On macOS, use libc puts/printf for better compatibility
	if acg.eb.target.OS() == OSDarwin {
		return acg.compilePrintlnLibc(arg)
	}

	// For string literals, use syscall directly (more efficient)
	if strExpr, ok := arg.(*StringExpr); ok {
		// Store string in rodata
		label := fmt.Sprintf("str_%d", acg.stringCounter)
		acg.stringCounter++
		content := strExpr.Value + "\n"
		acg.eb.Define(label, content)

		// mov x0, #1 (stdout)
		if err := acg.out.MovImm64("x0", 1); err != nil {
			return err
		}

		// Load string address into x1
		offset := uint64(acg.eb.text.Len())
		acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{
			offset:     offset,
			symbolName: label,
		})
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0x90}) // ADRP x1, #0
		acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x00, 0x91}) // ADD x1, x1, #0

		// mov x2, length
		if err := acg.out.MovImm64("x2", uint64(len(content))); err != nil {
			return err
		}

		// Syscall number and invocation (OS-specific)
		if acg.eb.target.OS() == OSDarwin {
			// macOS: syscall number in x16, svc #0x80
			if err := acg.out.MovImm64("x16", 0x2000004); err != nil { // write syscall
				return err
			}
			acg.out.out.writer.WriteBytes([]byte{0x01, 0x10, 0x00, 0xd4}) // svc #0x80
		} else {
			// Linux: syscall number in x8, svc #0
			if err := acg.out.MovImm64("x8", 64); err != nil { // write syscall = 64
				return err
			}
			acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0xd4}) // svc #0
		}

		return nil
	}

	// For numbers, convert to string and output via syscall
	// This avoids libc printf which has calling convention issues on ARM64

	// Compile the expression to get the number in d0
	if err := acg.compileExpression(arg); err != nil {
		return err
	}

	// Convert float64 in d0 to signed integer in x0
	// fcvtzs x0, d0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})

	// Special case: if x0 == 0, just print "0\n"
	// cmp x0, #0
	acg.out.out.writer.WriteBytes([]byte{0x1f, 0x00, 0x00, 0xf1})
	// b.ne non_zero
	nonZeroJump := acg.eb.text.Len()
	acg.out.BranchCond("ne", 0) // Placeholder

	// Zero case - print "0\n" via syscall
	zeroLabel := fmt.Sprintf("println_zero_%d", acg.stringCounter)
	acg.stringCounter++
	acg.eb.Define(zeroLabel, "0\n")

	// Load "0\n" address into x1
	offset := uint64(acg.eb.text.Len())
	acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{
		offset:     offset,
		symbolName: zeroLabel,
	})
	acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0x90}) // ADRP x1, #0
	acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x00, 0x91}) // ADD x1, x1, #0

	// mov x0, #1 (stdout)
	if err := acg.out.MovImm64("x0", 1); err != nil {
		return err
	}
	// mov x2, #2 (length)
	if err := acg.out.MovImm64("x2", 2); err != nil {
		return err
	}
	// write syscall
	if acg.eb.target.OS() == OSDarwin {
		if err := acg.out.MovImm64("x16", 0x2000004); err != nil {
			return err
		}
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x10, 0x00, 0xd4}) // svc #0x80
	} else {
		if err := acg.out.MovImm64("x8", 64); err != nil {
			return err
		}
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0xd4}) // svc #0
	}

	// Jump to end after printing zero (don't fall through to non-zero case)
	zeroEndJump := acg.eb.text.Len()
	if err := acg.out.Branch(0); err != nil {
		return err
	}

	// non_zero:
	nonZeroPos := acg.eb.text.Len()
	acg.patchJumpOffset(nonZeroJump, int32(nonZeroPos-nonZeroJump))

	// For non-zero numbers, call _tim_itoa helper
	// x0 already has the integer value
	// itoa uses global buffer, no need to allocate or pass buffer address

	// Call _tim_itoa(x0=number) -> x1=buffer, x2=length
	if err := acg.eb.GenerateCallInstruction("_tim_itoa"); err != nil {
		return err
	}

	// On return: x1 = buffer pointer (global), x2 = length (excluding newline)
	// Add newline at end: strb w3, [x1, x2] where w3 = '\n'
	// mov x3, #10
	acg.out.out.writer.WriteBytes([]byte{0x43, 0x01, 0x80, 0xd2})
	// strb w3, [x1, x2]
	acg.out.out.writer.WriteBytes([]byte{0x23, 0x68, 0x22, 0x38})
	// add x2, x2, #1 (include newline in length)
	acg.out.out.writer.WriteBytes([]byte{0x42, 0x04, 0x00, 0x91})

	// Write syscall: write(1, buffer, length)
	// mov x0, #1 (stdout)
	if err := acg.out.MovImm64("x0", 1); err != nil {
		return err
	}
	// x1 already has buffer pointer
	// x2 already has length

	// Syscall
	if acg.eb.target.OS() == OSDarwin {
		if err := acg.out.MovImm64("x16", 0x2000004); err != nil {
			return err
		}
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x10, 0x00, 0xd4}) // svc #0x80
	} else {
		if err := acg.out.MovImm64("x8", 64); err != nil {
			return err
		}
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0xd4}) // svc #0
	}

	// Patch jump from zero case to here
	endPos := acg.eb.text.Len()
	acg.patchJumpOffset(zeroEndJump, int32(endPos-zeroEndJump))

	return nil
}

// compileEprint compiles eprint/eprintln/eprintf calls (stderr output)
// compileEprint compiles eprint/eprintln/eprintf calls (stderr output) using
// Tim's own, libc-free formatting.
func (acg *ARM64CodeGen) compileEprint(call *CallExpr) error {
	const fd = uint64(2) // stderr

	if call.Function == "eprintf" {
		if len(call.Args) == 0 {
			return fmt.Errorf("eprintf requires at least a format string")
		}
		return acg.compilePrintfNative(call, fd)
	}

	isNewline := call.Function == "eprintln"
	if len(call.Args) == 0 {
		if isNewline {
			return acg.emitWriteLiteral("\n", fd)
		}
		return fmt.Errorf("%s requires at least one argument", call.Function)
	}

	arg := call.Args[0]
	if strExpr, ok := arg.(*StringExpr); ok {
		s := strExpr.Value
		if isNewline {
			s += "\n"
		}
		return acg.emitWriteLiteral(s, fd)
	}
	if acg.getExprType(arg) == "string" {
		if err := acg.compileExpression(arg); err != nil {
			return err
		}
		if err := acg.emitWriteTimString(fd); err != nil {
			return err
		}
		if isNewline {
			return acg.emitWriteLiteral("\n", fd)
		}
		return nil
	}
	// Numbers print as their integer value.
	if err := acg.compileExpression(arg); err != nil {
		return err
	}
	if err := acg.emitWriteInteger(fd); err != nil {
		return err
	}
	if isNewline {
		return acg.emitWriteLiteral("\n", fd)
	}
	return nil
}

// compileLoopStatement compiles a loop statement
func (acg *ARM64CodeGen) compileLoopStatement(stmt *LoopStmt) error {
	// Check if iterating over a RangeExpr (like 1..<10)
	if rangeExpr, isRangeExpr := stmt.Iterable.(*RangeExpr); isRangeExpr {
		return acg.compileRangeExprLoop(stmt, rangeExpr)
	}

	// List iteration
	return acg.compileListLoop(stmt)
}

// compileWhileStatement compiles `while cond { body }` (and the `@ cond ! N`
// condition-loop form): evaluate the condition each iteration, run the body while
// it is non-zero. Supports break (`ret @`/`break`) and continue, with the same
// sp save/restore as range loops so a break out of a partially-pushed expression
// can't corrupt the frame.
func (acg *ARM64CodeGen) compileWhileStatement(stmt *WhileStmt) error {
	acg.labelCounter++

	// Save sp at loop entry.
	acg.stackSize += 8
	spSlot := int32(16 + acg.stackSize - 8)
	if err := acg.out.AddImm64("x16", "sp", 0); err != nil {
		return err
	}
	if err := acg.out.StrImm64("x16", "x29", spSlot); err != nil {
		return err
	}

	loopStartPos := acg.eb.text.Len()
	acg.activeLoops = append(acg.activeLoops, ARM64LoopInfo{
		Label:           len(acg.activeLoops) + 1,
		StartPos:        loopStartPos,
		EndPatches:      []int{},
		ContinuePatches: []int{},
	})

	// Evaluate the condition (result in d0); exit the loop when it is zero.
	if err := acg.compileExpression(stmt.Condition); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x67, 0x9e}) // fmov d1, xzr
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e}) // fcmp d0, d1
	condJumpPos := acg.eb.text.Len()
	if err := acg.out.BranchCond("eq", 0); err != nil { // condition == 0 → end
		return err
	}
	li := len(acg.activeLoops) - 1
	acg.activeLoops[li].EndPatches = append(acg.activeLoops[li].EndPatches, condJumpPos)

	// Body.
	for _, s := range stmt.Body {
		if err := acg.compileStatement(s); err != nil {
			return err
		}
	}

	// Continue target: restore sp, patch continues, branch back to the condition.
	continuePos := acg.eb.text.Len()
	acg.activeLoops[li].ContinuePos = continuePos
	if err := acg.out.LdrImm64("x16", "x29", spSlot); err != nil {
		return err
	}
	if err := acg.out.AddImm64("sp", "x16", 0); err != nil {
		return err
	}
	for _, patchPos := range acg.activeLoops[li].ContinuePatches {
		acg.patchJumpOffset(patchPos, int32(continuePos-patchPos))
	}
	backPos := acg.eb.text.Len()
	if err := acg.out.Branch(int32(loopStartPos - backPos)); err != nil {
		return err
	}

	// End: restore sp, patch the condition-exit and any breaks.
	loopEndPos := acg.eb.text.Len()
	if err := acg.out.LdrImm64("x16", "x29", spSlot); err != nil {
		return err
	}
	if err := acg.out.AddImm64("sp", "x16", 0); err != nil {
		return err
	}
	for _, patchPos := range acg.activeLoops[li].EndPatches {
		acg.patchJumpOffset(patchPos, int32(loopEndPos-patchPos))
	}
	acg.activeLoops = acg.activeLoops[:len(acg.activeLoops)-1]
	return nil
}

// compileRangeExprLoop compiles a range expression loop (@ i in 1..<10 { ... })
func (acg *ARM64CodeGen) compileRangeExprLoop(stmt *LoopStmt, rangeExpr *RangeExpr) error {
	// Increment label counter for uniqueness
	acg.labelCounter++

	// Evaluate the start value
	if err := acg.compileExpression(rangeExpr.Start); err != nil {
		return err
	}

	// Convert d0 (float64) to integer in x0: fcvtzs x0, d0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})

	// Allocate stack space for start value
	acg.stackSize += 8
	startOffset := acg.stackSize
	offset := int32(16 + startOffset - 8)
	if err := acg.out.StrImm64("x0", "x29", offset); err != nil {
		return err
	}

	// Evaluate the end value
	if err := acg.compileExpression(rangeExpr.End); err != nil {
		return err
	}

	// Convert d0 (float64) to integer in x0: fcvtzs x0, d0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})

	// For inclusive ranges (..=), add 1 to the end value
	if rangeExpr.Inclusive {
		// add x0, x0, #1
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x04, 0x00, 0x91})
	}

	// Allocate stack space for loop limit
	acg.stackSize += 8
	limitOffset := acg.stackSize
	offset = int32(16 + limitOffset - 8)
	if err := acg.out.StrImm64("x0", "x29", offset); err != nil {
		return err
	}

	// Allocate stack space for iterator variable
	acg.stackSize += 8
	iterOffset := acg.stackSize
	acg.stackVars[stmt.Iterator] = iterOffset

	// Initialize iterator to start value (load and convert to float64)
	offset = int32(16 + startOffset - 8)
	if err := acg.out.LdrImm64("x0", "x29", offset); err != nil {
		return err
	}
	// scvtf d0, x0 (convert to float64)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
	// Store iterator: str d0, [x29, #offset]
	offset = int32(16 + iterOffset - 8)
	if err := acg.out.StrImm64Double("d0", "x29", offset); err != nil {
		return err
	}

	// Save sp at loop entry so break/continue can restore it. A jump out of the
	// body from inside a partially-pushed expression (e.g. `cond { ret @ }`, which
	// pushes the match condition) would otherwise leave sp below entry and corrupt
	// the function epilogue.
	acg.stackSize += 8
	spSlot := int32(16 + acg.stackSize - 8)
	if err := acg.out.AddImm64("x16", "sp", 0); err != nil { // x16 = sp
		return err
	}
	if err := acg.out.StrImm64("x16", "x29", spSlot); err != nil {
		return err
	}

	// Loop start label
	loopStartPos := acg.eb.text.Len()

	// Register this loop on the active loop stack
	loopLabel := len(acg.activeLoops) + 1
	loopInfo := ARM64LoopInfo{
		Label:            loopLabel,
		StartPos:         loopStartPos,
		EndPatches:       []int{},
		ContinuePatches:  []int{},
		IteratorOffset:   iterOffset,
		UpperBoundOffset: limitOffset,
		IsRangeLoop:      true,
	}
	acg.activeLoops = append(acg.activeLoops, loopInfo)

	// Load iterator value as float: ldr d0, [x29, #offset]
	offset = int32(16 + iterOffset - 8)
	if err := acg.out.LdrImm64Double("d0", "x29", offset); err != nil {
		return err
	}

	// Convert iterator to integer for comparison: fcvtzs x0, d0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})

	// Load limit value: ldr x1, [x29, #offset]
	offset = int32(16 + limitOffset - 8)
	if err := acg.out.LdrImm64("x1", "x29", offset); err != nil {
		return err
	}

	// Compare iterator with limit: cmp x0, x1
	acg.out.out.writer.WriteBytes([]byte{0x1f, 0x00, 0x01, 0xeb})

	// Jump to loop end if iterator >= limit
	loopEndJumpPos := acg.eb.text.Len()
	acg.out.BranchCond("ge", 0) // Placeholder

	// Add this to the loop's end patches
	acg.activeLoops[len(acg.activeLoops)-1].EndPatches = append(
		acg.activeLoops[len(acg.activeLoops)-1].EndPatches,
		loopEndJumpPos,
	)

	// Compile loop body
	for _, s := range stmt.Body {
		if err := acg.compileStatement(s); err != nil {
			return err
		}
	}

	// Mark continue position (increment step)
	continuePos := acg.eb.text.Len()
	acg.activeLoops[len(acg.activeLoops)-1].ContinuePos = continuePos

	// Restore sp to its loop-entry value (no-op on normal fallthrough; corrects a
	// `continue` that jumped out of a partially-pushed expression).
	if err := acg.out.LdrImm64("x16", "x29", spSlot); err != nil {
		return err
	}
	if err := acg.out.AddImm64("sp", "x16", 0); err != nil { // sp = x16
		return err
	}

	// Patch all continue jumps to point here
	for _, patchPos := range acg.activeLoops[len(acg.activeLoops)-1].ContinuePatches {
		offset := int32(continuePos - patchPos)
		acg.patchJumpOffset(patchPos, offset)
	}

	// Increment iterator (add 1.0 to float64 value)
	offset = int32(16 + iterOffset - 8)
	if err := acg.out.LdrImm64Double("d0", "x29", offset); err != nil {
		return err
	}
	// Load 1.0 into d1
	if err := acg.out.MovImm64("x0", 1); err != nil {
		return err
	}
	// scvtf d1, x0
	acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x62, 0x9e})
	// fadd d0, d0, d1
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x28, 0x61, 0x1e})
	// Store incremented value: str d0, [x29, #offset]
	offset = int32(16 + iterOffset - 8)
	if err := acg.out.StrImm64Double("d0", "x29", offset); err != nil {
		return err
	}

	// Jump back to loop start
	loopBackJumpPos := acg.eb.text.Len()
	backOffset := int32(loopStartPos - loopBackJumpPos)
	acg.out.Branch(backOffset)

	// Loop end label — restore sp here so a `break` (`ret @`) that left sp below
	// the loop-entry value can't corrupt the function epilogue.
	loopEndPos := acg.eb.text.Len()
	if err := acg.out.LdrImm64("x16", "x29", spSlot); err != nil {
		return err
	}
	if err := acg.out.AddImm64("sp", "x16", 0); err != nil { // sp = x16
		return err
	}

	// Patch all end jumps
	for _, patchPos := range acg.activeLoops[len(acg.activeLoops)-1].EndPatches {
		endOffset := int32(loopEndPos - patchPos)
		acg.patchJumpOffset(patchPos, endOffset)
	}

	// Pop loop from active stack
	acg.activeLoops = acg.activeLoops[:len(acg.activeLoops)-1]

	return nil
}

// compileListLoop compiles a list iteration loop (@ elem in [1,2,3] { ... })
func (acg *ARM64CodeGen) compileListLoop(stmt *LoopStmt) error {
	// Increment label counter for uniqueness
	acg.labelCounter++

	// Evaluate the list expression (returns pointer as float64 in d0)
	if err := acg.compileExpression(stmt.Iterable); err != nil {
		return err
	}

	// Save list pointer to stack
	acg.stackSize += 8
	listPtrOffset := acg.stackSize
	offset := int32(16 + listPtrOffset - 8)
	if err := acg.out.StrImm64Double("d0", "x29", offset); err != nil {
		return err
	}

	// Convert pointer from float64 to integer in x0: fcvtzs x0, d0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})

	// Load list length from [x0] (first 8 bytes)
	// ldr d0, [x0]
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x40, 0xfd})

	// Convert length to integer: fcvtzs x0, d0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})

	// Store length in stack
	acg.stackSize += 8
	lengthOffset := acg.stackSize
	offset = int32(16 + lengthOffset - 8)
	if err := acg.out.StrImm64("x0", "x29", offset); err != nil {
		return err
	}

	// Allocate stack space for index variable
	acg.stackSize += 8
	indexOffset := acg.stackSize
	// Initialize index to 0: mov x0, #0
	if err := acg.out.MovImm64("x0", 0); err != nil {
		return err
	}
	offset = int32(16 + indexOffset - 8)
	if err := acg.out.StrImm64("x0", "x29", offset); err != nil {
		return err
	}

	// Allocate stack space for iterator variable (the actual value from the list)
	acg.stackSize += 8
	iterOffset := acg.stackSize
	acg.stackVars[stmt.Iterator] = iterOffset

	// A cstruct-typed iterator (`@ b as Ball in ...`) lets the body read b.field
	// directly without a per-iteration `b = elem as Ball` cast.
	if _, isStruct := acg.cstructs[stmt.IteratorType]; isStruct {
		acg.varCStructType[stmt.Iterator] = stmt.IteratorType
	}

	// Loop start label
	loopStartPos := acg.eb.text.Len()

	// Register this loop on the active loop stack
	loopLabel := len(acg.activeLoops) + 1
	loopInfo := ARM64LoopInfo{
		Label:            loopLabel,
		StartPos:         loopStartPos,
		EndPatches:       []int{},
		ContinuePatches:  []int{},
		IteratorOffset:   iterOffset,
		IndexOffset:      indexOffset,
		UpperBoundOffset: lengthOffset,
		ListPtrOffset:    listPtrOffset,
		IsRangeLoop:      false,
	}
	acg.activeLoops = append(acg.activeLoops, loopInfo)

	// Load index: ldr x0, [x29, #offset] (positive offset)
	offset = int32(16 + indexOffset - 8)
	if err := acg.out.LdrImm64("x0", "x29", offset); err != nil {
		return err
	}

	// Load length: ldr x1, [x29, #offset] (positive offset)
	offset = int32(16 + lengthOffset - 8)
	if err := acg.out.LdrImm64("x1", "x29", offset); err != nil {
		return err
	}

	// Compare index with length: cmp x0, x1
	acg.out.out.writer.WriteBytes([]byte{0x1f, 0x00, 0x01, 0xeb}) // cmp x0, x1

	// Jump to loop end if index >= length
	loopEndJumpPos := acg.eb.text.Len()
	acg.out.BranchCond("ge", 0) // Placeholder

	// Add this to the loop's end patches
	acg.activeLoops[len(acg.activeLoops)-1].EndPatches = append(
		acg.activeLoops[len(acg.activeLoops)-1].EndPatches,
		loopEndJumpPos,
	)

	// Load list pointer from stack to x2
	offset = int32(16 + listPtrOffset - 8)
	if err := acg.out.LdrImm64Double("d0", "x29", offset); err != nil {
		return err
	}
	// Convert to integer: fcvtzs x2, d0
	acg.out.out.writer.WriteBytes([]byte{0x02, 0x00, 0x78, 0x9e})

	// Skip length prefix: x2 += 8
	if err := acg.out.AddImm64("x2", "x2", 8); err != nil {
		return err
	}

	// Load index into x0
	offset = int32(16 + indexOffset - 8)
	if err := acg.out.LdrImm64("x0", "x29", offset); err != nil {
		return err
	}

	// Calculate offset: x0 = x0 << 3 (x0 * 8)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0xf0, 0x7d, 0xd3}) // lsl x0, x0, #3

	// Add to base: x2 = x2 + x0
	acg.out.out.writer.WriteBytes([]byte{0x42, 0x00, 0x00, 0x8b}) // add x2, x2, x0

	// Load element value: ldr d0, [x2]
	acg.out.out.writer.WriteBytes([]byte{0x40, 0x00, 0x40, 0xfd}) // ldr d0, [x2]

	// Store iterator value: str d0, [x29, #offset] (positive offset)
	offset = int32(16 + iterOffset - 8)
	if err := acg.out.StrImm64Double("d0", "x29", offset); err != nil {
		return err
	}

	// Compile loop body
	for _, s := range stmt.Body {
		if err := acg.compileStatement(s); err != nil {
			return err
		}
	}

	// Mark continue position (increment step)
	continuePos := acg.eb.text.Len()
	acg.activeLoops[len(acg.activeLoops)-1].ContinuePos = continuePos

	// Patch all continue jumps to point here
	for _, patchPos := range acg.activeLoops[len(acg.activeLoops)-1].ContinuePatches {
		offset := int32(continuePos - patchPos)
		acg.patchJumpOffset(patchPos, offset)
	}

	// Increment index
	offset = int32(16 + indexOffset - 8)
	if err := acg.out.LdrImm64("x0", "x29", offset); err != nil {
		return err
	}
	if err := acg.out.AddImm64("x0", "x0", 1); err != nil {
		return err
	}
	offset = int32(16 + indexOffset - 8)
	if err := acg.out.StrImm64("x0", "x29", offset); err != nil {
		return err
	}

	// Jump back to loop start
	loopBackJumpPos := acg.eb.text.Len()
	backOffset := int32(loopStartPos - loopBackJumpPos)
	acg.out.Branch(backOffset)

	// Loop end label
	loopEndPos := acg.eb.text.Len()

	// Patch all end jumps
	for _, patchPos := range acg.activeLoops[len(acg.activeLoops)-1].EndPatches {
		endOffset := int32(loopEndPos - patchPos)
		acg.patchJumpOffset(patchPos, endOffset)
	}

	// Pop loop from active stack
	acg.activeLoops = acg.activeLoops[:len(acg.activeLoops)-1]

	return nil
}

// compileExit compiles an exit call via dynamic linking
func (acg *ARM64CodeGen) compileExit(call *CallExpr) error {
	exitCode := uint64(0)

	// Evaluate exit code argument
	if len(call.Args) > 0 {
		if num, ok := call.Args[0].(*NumberExpr); ok {
			exitCode = uint64(int64(num.Value))
		} else {
			// Compile expression and convert to integer
			if err := acg.compileExpression(call.Args[0]); err != nil {
				return err
			}
			// Convert d0 to integer in x0: fcvtzs x0, d0
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})
			// x0 now contains exit code, ready for function call
			// Skip the constant load below
			goto callExit
		}
	}

	// Load constant exit code into x0 (first argument register for ARM64)
	if err := acg.out.MovImm64("x0", exitCode); err != nil {
		return err
	}

callExit:
	// On macOS, use syscall with BSD calling convention
	if acg.eb.target.IsMachO() {
		// mov x16, #1 (sys_exit)
		acg.out.out.writer.WriteBytes([]byte{0x30, 0x00, 0x80, 0xd2})
		// svc #0x80
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x10, 0x00, 0xd4})
		return nil
	}

	// For static Linux builds, use Linux ARM64 exit syscall
	if !acg.eb.useDynamicLinking {
		// mov x8, #93 (sys_exit on ARM64 Linux)
		acg.out.out.writer.WriteBytes([]byte{0xa8, 0x0b, 0x80, 0xd2})
		// svc #0
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0xd4})
		return nil
	}

	// Dynamic linking: call exit from libc
	acg.eb.useDynamicLinking = true

	// Add exit to needed functions list if not already there
	funcName := "exit"
	found := slices.Contains(acg.eb.neededFunctions, funcName)
	if !found {
		acg.eb.neededFunctions = append(acg.eb.neededFunctions, funcName)
	}

	// Generate call to exit stub
	stubLabel := funcName + "$stub"
	position := acg.eb.text.Len()
	acg.eb.callPatches = append(acg.eb.callPatches, CallPatch{
		position:   position,
		targetName: stubLabel,
	})

	// Emit placeholder bl instruction (will be patched)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x94}) // bl #0

	// exit() doesn't return, but we'll never reach here anyway
	return nil
}

// compileExitf compiles exitf() and exitln() - printf + exit
func (acg *ARM64CodeGen) compileExitf(call *CallExpr) error {
	// exitf/exitln: write a formatted message to stderr (Tim-native), then
	// exit with code 1. exitln appends a trailing newline.
	const fd = uint64(2) // stderr
	if err := acg.compilePrintfNative(call, fd); err != nil {
		return err
	}
	if call.Function == "exitln" {
		if err := acg.emitWriteLiteral("\n", fd); err != nil {
			return err
		}
	}
	exitCall := &CallExpr{
		Function: "exit",
		Args:     []Expression{&NumberExpr{Value: 1}},
	}
	return acg.compileExit(exitCall)
}

// compileTailCall compiles a tail-recursive call using the "me" keyword
func (acg *ARM64CodeGen) compileTailCall(call *CallExpr) error {
	// Verify we're in a lambda
	if acg.currentLambda == nil {
		return fmt.Errorf("'me' can only be used inside a lambda")
	}

	// Verify argument count matches parameter count
	if len(call.Args) != len(acg.currentLambda.Params) {
		return fmt.Errorf("'me' called with %d arguments, but lambda has %d parameters", len(call.Args), len(acg.currentLambda.Params))
	}

	// Strategy: Evaluate all arguments, then update parameters, then jump to body start
	// We need to avoid overwriting parameters before we're done evaluating arguments

	// Evaluate all arguments and push them on the stack
	for _, arg := range call.Args {
		if err := acg.compileExpression(arg); err != nil {
			return err
		}
		// Push d0 onto stack
		acg.out.SubImm64("sp", "sp", 16)
		// str d0, [sp]
		acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x00, 0xfd})
	}

	// Pop arguments from stack and store them in parameter locations
	// Parameters are stored at [x29, #16 + paramOffset - 8]
	for i := len(call.Args) - 1; i >= 0; i-- {
		// Pop d0 from stack
		// ldr d0, [sp]
		acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x40, 0xfd})
		acg.out.AddImm64("sp", "sp", 16)

		// Get parameter offset
		paramName := acg.currentLambda.Params[i]
		paramStackOffset := acg.stackVars[paramName]
		offset := int32(16 + paramStackOffset - 8)

		// Store to parameter location: str d0, [x29, #offset]
		if err := acg.out.StrImm64Double("d0", "x29", offset); err != nil {
			return err
		}
	}

	// Jump back to the start of the lambda body
	currentPos := acg.eb.text.Len()
	jumpOffset := int32(acg.currentLambda.BodyStart - currentPos)
	acg.out.Branch(jumpOffset)

	return nil
}

// compileDirectCall compiles a direct function call (e.g., lambda invocation)
func (acg *ARM64CodeGen) compileDirectCall(call *DirectCallExpr) error {
	// Special case: calling a value (not a lambda) with no arguments just returns the value
	// This handles cases like: main = 42; main() returns 42
	if len(call.Args) == 0 {
		// Check if callee is a simple value (not a lambda)
		isLambda := false
		switch call.Callee.(type) {
		case *LambdaExpr, *PatternLambdaExpr, *MultiLambdaExpr:
			isLambda = true
		case *IdentExpr:
			// Check if the identifier refers to a lambda/function
			if ident, ok := call.Callee.(*IdentExpr); ok {
				if acg.lambdaVars[ident.Name] {
					isLambda = true
				}
			}
		}

		if !isLambda {
			// Just compile the value and return it (calling a value returns the value)
			return acg.compileExpression(call.Callee)
		}
	}

	// Compile the callee expression to get the closure object pointer.
	// Result in d0 (closure pointer as float64).
	if err := acg.compileExpression(call.Callee); err != nil {
		return err
	}

	// Convert closure pointer from float64 to integer in x0
	// fcvtzs x0, d0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})

	// Save closure pointer to stack (x0 might get clobbered during arg evaluation)
	acg.out.SubImm64("sp", "sp", 16)
	if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
		return err
	}

	// Evaluate all arguments and save to stack
	for _, arg := range call.Args {
		if err := acg.compileExpression(arg); err != nil {
			return err
		}
		// Result in d0, save to stack
		acg.out.SubImm64("sp", "sp", 16)
		// str d0, [sp]
		acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x00, 0xfd})
	}
	// Load arguments from stack into d0-d7 registers (in reverse order)
	// ARM64 AAPCS64 passes float args in d0-d7
	if len(call.Args) > 8 {
		return fmt.Errorf("too many arguments to direct call (max 8)")
	}

	for i := len(call.Args) - 1; i >= 0; i-- {
		// ldr dN, [sp]
		regNum := uint32(i)
		instr := uint32(0xfd400000) | (regNum) | (31 << 5) // ldr dN, [sp, #0]
		acg.out.out.writer.WriteBytes([]byte{
			byte(instr),
			byte(instr >> 8),
			byte(instr >> 16),
			byte(instr >> 24),
		})
		acg.out.AddImm64("sp", "sp", 16)
	}
	// Load closure pointer from stack into x9 (passed to the callee so it can
	// read its captures), then load the function pointer from [closure].
	if err := acg.out.LdrImm64("x9", "sp", 0); err != nil {
		return err
	}
	if err := acg.out.AddImm64("sp", "sp", 16); err != nil {
		return err
	}
	if err := acg.out.LdrImm64("x16", "x9", 0); err != nil { // x16 = [closure] = func ptr
		return err
	}

	// Call the function pointer in x16: blr x16 (x9 holds the closure pointer)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x02, 0x3f, 0xd6})

	// Result is in d0
	return nil
}

// --- Tim-native formatting (no libc) ----------------------------------------
//
// Tim's own print/printf format numbers, strings and booleans using these
// helpers and emit output with the write(2) syscall directly, so the default
// print family never depends on the C library. Users who want C semantics can
// still call C.printf / c.printf, which routes through the dynamic linker.

// emitSyscallWrite emits write(fd, buf, len). The caller must have x1=buf and
// x2=len already set up; this sets x0=fd and invokes the OS write syscall.
func (acg *ARM64CodeGen) emitSyscallWrite(fd uint64) error {
	if err := acg.out.MovImm64("x0", fd); err != nil {
		return err
	}
	if acg.eb.target.OS() == OSDarwin {
		if err := acg.out.MovImm64("x16", 0x2000004); err != nil { // write
			return err
		}
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x10, 0x00, 0xd4}) // svc #0x80
	} else {
		if err := acg.out.MovImm64("x8", 64); err != nil { // write
			return err
		}
		acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0xd4}) // svc #0
	}
	return nil
}

// emitWriteLiteral writes a fixed string to fd via a rodata constant.
func (acg *ARM64CodeGen) emitWriteLiteral(s string, fd uint64) error {
	if s == "" {
		return nil
	}
	label := fmt.Sprintf("str_%d", acg.stringCounter)
	acg.stringCounter++
	acg.eb.Define(label, s)
	off := uint64(acg.eb.text.Len())
	acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{offset: off, symbolName: label})
	acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x00, 0x90}) // ADRP x1, #0
	acg.out.out.writer.WriteBytes([]byte{0x21, 0x00, 0x00, 0x91}) // ADD x1, x1, #0
	if err := acg.out.MovImm64("x2", uint64(len(s))); err != nil {
		return err
	}
	return acg.emitSyscallWrite(fd)
}

// emitWriteInteger writes the integer value of d0 (truncated) in base 10.
func (acg *ARM64CodeGen) emitWriteInteger(fd uint64) error {
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e}) // fcvtzs x0, d0
	if err := acg.eb.GenerateCallInstruction("_tim_itoa"); err != nil {
		return err
	}
	// _tim_itoa returns x1=buf, x2=len.
	return acg.emitSyscallWrite(fd)
}

// emitWriteFloat writes d0 as "<int>.<precision digits>" using Tim's format
// (fixed number of fractional digits, like %f).
func (acg *ARM64CodeGen) emitWriteFloat(precision int, fd uint64, trim bool) error {
	if precision < 1 {
		precision = 6
	}
	if precision > 15 {
		precision = 15
	}
	acg.out.SubImm64("sp", "sp", 48)
	if err := acg.out.StrImm64Double("d0", "sp", 0); err != nil { // save value
		return err
	}

	// Leading '-' for values in (-1, 0): the integer part is 0, so _tim_itoa
	// can't carry the sign. Emit it here when value < 0 and trunc(value) == 0.
	if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil { // intpart
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x67, 0x9e}) // fmov d1, xzr (0.0)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e}) // fcmp d0, d1
	geJump := acg.eb.text.Len()
	acg.out.BranchCond("ge", 0)                                   // value >= 0 -> no sign
	acg.out.out.writer.WriteBytes([]byte{0x1f, 0x00, 0x00, 0xf1}) // cmp x0, #0
	neJump := acg.eb.text.Len()
	acg.out.BranchCond("ne", 0)                        // intpart != 0 -> itoa carries the sign
	if err := acg.out.MovImm64("x9", 45); err != nil { // '-'
		return err
	}
	if err := acg.out.StrImm64("x9", "sp", 8); err != nil {
		return err
	}
	if err := acg.out.AddImm64("x1", "sp", 8); err != nil {
		return err
	}
	if err := acg.out.MovImm64("x2", 1); err != nil {
		return err
	}
	if err := acg.emitSyscallWrite(fd); err != nil {
		return err
	}
	signHere := acg.eb.text.Len()
	acg.patchJumpOffset(geJump, int32(signHere-geJump))
	acg.patchJumpOffset(neJump, int32(signHere-neJump))

	// Integer part via _tim_itoa.
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e}) // fcvtzs x0, d0
	if err := acg.eb.GenerateCallInstruction("_tim_itoa"); err != nil {
		return err
	}
	if err := acg.emitSyscallWrite(fd); err != nil {
		return err
	}

	// Decimal point.
	if err := acg.out.MovImm64("x9", 46); err != nil { // '.'
		return err
	}
	if err := acg.out.StrImm64("x9", "sp", 8); err != nil {
		return err
	}
	if err := acg.out.AddImm64("x1", "sp", 8); err != nil {
		return err
	}
	if err := acg.out.MovImm64("x2", 1); err != nil {
		return err
	}
	if err := acg.emitSyscallWrite(fd); err != nil {
		return err
	}

	// Fraction = |value - trunc(value)| scaled by 10^precision, rounded.
	if err := acg.out.LdrImm64Double("d0", "sp", 0); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil {
		return err
	}
	if err := acg.out.ScvtfInt64ToDouble("d1", "x0"); err != nil {
		return err
	}
	if err := acg.out.FsubScalar64("d0", "d0", "d1"); err != nil {
		return err
	}
	if err := acg.out.FabsScalar64("d0", "d0"); err != nil {
		return err
	}
	mult := uint64(1)
	for range precision {
		mult *= 10
	}
	if err := acg.out.MovImm64("x0", mult); err != nil {
		return err
	}
	if err := acg.out.ScvtfInt64ToDouble("d1", "x0"); err != nil {
		return err
	}
	if err := acg.out.FmulScalar64("d0", "d0", "d1"); err != nil {
		return err
	}
	if err := acg.out.FcvtnsDoubleToInt64("x0", "d0"); err != nil { // round to nearest
		return err
	}

	if err := acg.out.MovImm64("x10", 10); err != nil {
		return err
	}
	// Extract digits least-significant first into [sp+16 .. sp+16+precision).
	for i := precision - 1; i >= 0; i-- {
		if err := acg.out.UDiv64("x11", "x0", "x10"); err != nil {
			return err
		}
		if err := acg.out.Msub64("x12", "x11", "x10", "x0"); err != nil { // x12 = x0 - x11*x10
			return err
		}
		if err := acg.out.AddImm64("x12", "x12", 48); err != nil { // '0' + digit
			return err
		}
		if err := acg.out.StrbImm("x12", "sp", int32(16+i)); err != nil {
			return err
		}
		if err := acg.out.MovReg64("x0", "x11"); err != nil {
			return err
		}
	}
	// Length of the fraction to print, in x2. When trimming (used by println's
	// smart format), drop trailing '0' digits, keeping at least one.
	if trim {
		if err := acg.out.MovImm64("x3", uint64(precision)); err != nil {
			return err
		}
		trimLoop := acg.eb.text.Len()
		acg.out.out.writer.WriteBytes([]byte{0x7f, 0x04, 0x00, 0xf1}) // cmp x3, #1
		doneJump := acg.eb.text.Len()
		acg.out.BranchCond("le", 0)
		if err := acg.out.SubImm64("x4", "x3", 1); err != nil { // index of last digit
			return err
		}
		if err := acg.out.AddImm64("x5", "sp", 16); err != nil {
			return err
		}
		acg.out.out.writer.WriteBytes([]byte{0xa6, 0x68, 0x64, 0x38}) // ldrb w6, [x5, x4]
		acg.out.out.writer.WriteBytes([]byte{0xdf, 0xc0, 0x00, 0x71}) // cmp w6, #48 ('0')
		keepJump := acg.eb.text.Len()
		acg.out.BranchCond("ne", 0)
		if err := acg.out.SubImm64("x3", "x3", 1); err != nil {
			return err
		}
		backPos := acg.eb.text.Len()
		acg.out.Branch(0)
		acg.patchJumpOffset(backPos, int32(trimLoop-backPos))
		here := acg.eb.text.Len()
		acg.patchJumpOffset(doneJump, int32(here-doneJump))
		acg.patchJumpOffset(keepJump, int32(here-keepJump))
		acg.out.out.writer.WriteBytes([]byte{0xe2, 0x03, 0x03, 0xaa}) // mov x2, x3
	} else {
		if err := acg.out.MovImm64("x2", uint64(precision)); err != nil {
			return err
		}
	}
	if err := acg.out.AddImm64("x1", "sp", 16); err != nil {
		return err
	}
	if err := acg.emitSyscallWrite(fd); err != nil {
		return err
	}
	return acg.out.AddImm64("sp", "sp", 48)
}

// emitWriteFloatSmart writes d0 the way println does: whole numbers print with
// no fractional part (42, -7), other values print with trailing zeros trimmed
// (3.14159). This is Tim's libc-free equivalent of printf("%.15g").
func (acg *ARM64CodeGen) emitWriteFloatSmart(fd uint64) error {
	acg.out.SubImm64("sp", "sp", 16)
	if err := acg.out.StrImm64Double("d0", "sp", 0); err != nil {
		return err
	}
	// Whole-number test: trunc(value) == value ?
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e}) // fcvtzs x0, d0
	acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x62, 0x9e}) // scvtf d1, x0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e}) // fcmp d0, d1
	floatJump := acg.eb.text.Len()
	acg.out.BranchCond("ne", 0)

	// Whole: print integer and finish.
	if err := acg.out.LdrImm64Double("d0", "sp", 0); err != nil {
		return err
	}
	acg.out.AddImm64("sp", "sp", 16)
	if err := acg.emitWriteInteger(fd); err != nil {
		return err
	}
	endJump := acg.eb.text.Len()
	acg.out.Branch(0)

	// Fractional: print with trailing-zero trimming.
	floatPos := acg.eb.text.Len()
	acg.patchJumpOffset(floatJump, int32(floatPos-floatJump))
	if err := acg.out.LdrImm64Double("d0", "sp", 0); err != nil {
		return err
	}
	acg.out.AddImm64("sp", "sp", 16)
	if err := acg.emitWriteFloat(15, fd, true); err != nil {
		return err
	}

	endPos := acg.eb.text.Len()
	acg.patchJumpOffset(endJump, int32(endPos-endJump))
	return nil
}

// emitWriteTimString writes a Tim string (map[index]=charcode) held in d0,
// one character at a time. State is kept on the stack so the write syscall
// can't clobber it.
func (acg *ARM64CodeGen) emitWriteTimString(fd uint64) error {
	acg.out.SubImm64("sp", "sp", 32)
	if err := acg.out.FcvtzsDoubleToInt64("x3", "d0"); err != nil { // ptr
		return err
	}
	if err := acg.out.StrImm64("x3", "sp", 0); err != nil {
		return err
	}
	if err := acg.out.LdrImm64Double("d0", "x3", 0); err != nil { // count
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x4", "d0"); err != nil {
		return err
	}
	if err := acg.out.StrImm64("x4", "sp", 8); err != nil {
		return err
	}
	if err := acg.out.MovImm64("x5", 0); err != nil { // index
		return err
	}
	if err := acg.out.StrImm64("x5", "sp", 16); err != nil {
		return err
	}

	loopStart := acg.eb.text.Len()
	if err := acg.out.LdrImm64("x5", "sp", 16); err != nil {
		return err
	}
	if err := acg.out.LdrImm64("x4", "sp", 8); err != nil {
		return err
	}
	if err := acg.out.CmpReg64("x5", "x4"); err != nil {
		return err
	}
	endJump := acg.eb.text.Len()
	acg.out.BranchCond("ge", 0)

	if err := acg.out.LdrImm64("x3", "sp", 0); err != nil {
		return err
	}
	if err := acg.out.LslImm64("x6", "x5", 4); err != nil { // x6 = index*16
		return err
	}
	if err := acg.out.AddReg64("x6", "x3", "x6"); err != nil { // x6 = ptr + index*16
		return err
	}
	if err := acg.out.LdrImm64Double("d0", "x6", 16); err != nil { // value of pair
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x7", "d0"); err != nil { // char code
		return err
	}
	// Char buffer lives at [sp+24], away from ptr/count/index at [sp+0/8/16].
	if err := acg.out.StrbImm("x7", "sp", 24); err != nil {
		return err
	}
	if err := acg.out.AddImm64("x1", "sp", 24); err != nil { // x1 = &char
		return err
	}
	if err := acg.out.MovImm64("x2", 1); err != nil {
		return err
	}
	if err := acg.emitSyscallWrite(fd); err != nil {
		return err
	}
	// index++
	if err := acg.out.LdrImm64("x5", "sp", 16); err != nil {
		return err
	}
	if err := acg.out.AddImm64("x5", "x5", 1); err != nil {
		return err
	}
	if err := acg.out.StrImm64("x5", "sp", 16); err != nil {
		return err
	}
	// branch back to loopStart
	backPos := acg.eb.text.Len()
	acg.out.Branch(0)
	acg.patchJumpOffset(backPos, int32(loopStart-backPos))

	endPos := acg.eb.text.Len()
	acg.patchJumpOffset(endJump, int32(endPos-endJump))
	return acg.out.AddImm64("sp", "sp", 32)
}

// emitWriteBool writes "yes"/"no" (yesNo) or "true"/"false" for the value in d0.
func (acg *ARM64CodeGen) emitWriteBool(yesNo bool, fd uint64) error {
	t, f := "true", "false"
	if yesNo {
		t, f = "yes", "no"
	}
	acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x67, 0x9e}) // fmov d1, xzr
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e}) // fcmp d0, d1
	falseJump := acg.eb.text.Len()
	acg.out.BranchCond("eq", 0)
	if err := acg.emitWriteLiteral(t, fd); err != nil {
		return err
	}
	endJump := acg.eb.text.Len()
	acg.out.Branch(0)
	falsePos := acg.eb.text.Len()
	acg.patchJumpOffset(falseJump, int32(falsePos-falseJump))
	if err := acg.emitWriteLiteral(f, fd); err != nil {
		return err
	}
	endPos := acg.eb.text.Len()
	acg.patchJumpOffset(endJump, int32(endPos-endJump))
	return nil
}

// compilePrintfNative is Tim's own printf: it parses the literal format string
// at compile time and emits direct write syscalls for each piece, with no libc
// dependency. fd selects the output stream (1=stdout, 2=stderr).
func (acg *ARM64CodeGen) compilePrintfNative(call *CallExpr, fd uint64) error {
	formatArg := call.Args[0]
	strExpr, ok := formatArg.(*StringExpr)
	if !ok {
		return fmt.Errorf("printf first argument must be a string literal")
	}
	runes := []rune(processEscapeSequences(strExpr.Value))
	argIndex := 0
	i := 0
	for i < len(runes) {
		if runes[i] != '%' || i+1 >= len(runes) {
			// Literal run up to the next '%'.
			start := i
			for i < len(runes) && !(runes[i] == '%' && i+1 < len(runes)) {
				i++
			}
			if err := acg.emitWriteLiteral(string(runes[start:i]), fd); err != nil {
				return err
			}
			continue
		}
		if runes[i+1] == '%' {
			if err := acg.emitWriteLiteral("%", fd); err != nil {
				return err
			}
			i += 2
			continue
		}
		// Scan flags, width, precision, length modifiers, then the conversion.
		j := i + 1
		for j < len(runes) && strings.ContainsRune("-+ #0", runes[j]) {
			j++
		}
		for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
			j++
		}
		precision := 6
		if j < len(runes) && runes[j] == '.' {
			j++
			ps := j
			for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
				j++
			}
			if j > ps {
				if p, err := strconv.Atoi(string(runes[ps:j])); err == nil {
					precision = p
				}
			}
		}
		for j < len(runes) && strings.ContainsRune("lhLjzt", runes[j]) {
			j++
		}
		if j >= len(runes) {
			return fmt.Errorf("printf: incomplete format specifier")
		}
		conv := runes[j]
		i = j + 1

		if argIndex+1 >= len(call.Args) {
			return fmt.Errorf("printf: not enough arguments for format string")
		}
		arg := call.Args[argIndex+1]
		argIndex++

		switch conv {
		case 'd', 'i', 'u', 'x', 'X', 'o', 'c':
			if err := acg.compileExpression(arg); err != nil {
				return err
			}
			if err := acg.emitWriteInteger(fd); err != nil {
				return err
			}
		case 'v':
			if err := acg.compileExpression(arg); err != nil {
				return err
			}
			if err := acg.emitWriteFloat(6, fd, false); err != nil {
				return err
			}
		case 'f', 'F', 'g', 'G', 'e', 'E':
			if err := acg.compileExpression(arg); err != nil {
				return err
			}
			if err := acg.emitWriteFloat(precision, fd, false); err != nil {
				return err
			}
		case 's':
			if lit, ok := arg.(*StringExpr); ok {
				if err := acg.emitWriteLiteral(lit.Value, fd); err != nil {
					return err
				}
			} else {
				if err := acg.compileExpression(arg); err != nil {
					return err
				}
				if err := acg.emitWriteTimString(fd); err != nil {
					return err
				}
			}
		case 'b', 't':
			if err := acg.compileExpression(arg); err != nil {
				return err
			}
			if err := acg.emitWriteBool(conv == 'b', fd); err != nil {
				return err
			}
		default:
			return fmt.Errorf("printf: unsupported format specifier %%%c", conv)
		}
	}
	// Leave d0 = 0.0 as the call's result.
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x67, 0x9e}) // fmov d0, xzr
	return nil
}

// compilePrintf compiles a printf() call via dynamic linking
func (acg *ARM64CodeGen) compilePrintf(call *CallExpr) error {
	if len(call.Args) == 0 {
		return fmt.Errorf("printf requires at least a format string")
	}

	// First argument must be a string (format string)
	formatArg := call.Args[0]
	strExpr, ok := formatArg.(*StringExpr)
	if !ok {
		return fmt.Errorf("printf first argument must be a string literal")
	}

	// Process format string: %v -> %.15g (smart float), %b -> %s (boolean)
	processedFormat := processEscapeSequences(strExpr.Value)
	boolPositions := make(map[int]bool) // Track which args are %b (boolean)
	isFloatArg := make(map[int]bool)    // Track which args are floats
	isPtrArg := make(map[int]bool)      // Track which args are pointers

	// Parse each conversion specifier in full: %[flags][width][.precision][length]conv.
	// Classifying only on the char right after '%' breaks on specifiers like
	// "%.15g" or "%08x", which would then be treated as integers and misread the
	// argument. The conversion character determines how the argument is passed.
	argPos := 0
	var result strings.Builder
	i := 0
	for i < len(processedFormat) {
		c := processedFormat[i]
		if c != '%' {
			result.WriteByte(c)
			i++
			continue
		}
		if i+1 < len(processedFormat) && processedFormat[i+1] == '%' {
			result.WriteString("%%")
			i += 2
			continue
		}
		// Scan flags, width, precision and length modifiers up to the conversion.
		j := i + 1
		for j < len(processedFormat) && strings.IndexByte("-+ #0", processedFormat[j]) >= 0 {
			j++
		}
		for j < len(processedFormat) && processedFormat[j] >= '0' && processedFormat[j] <= '9' {
			j++
		}
		if j < len(processedFormat) && processedFormat[j] == '.' {
			j++
			for j < len(processedFormat) && processedFormat[j] >= '0' && processedFormat[j] <= '9' {
				j++
			}
		}
		for j < len(processedFormat) && strings.IndexByte("lhLjzt", processedFormat[j]) >= 0 {
			j++
		}
		if j >= len(processedFormat) {
			result.WriteByte('%') // malformed trailing '%'
			i++
			continue
		}
		conv := processedFormat[j]
		spec := processedFormat[i : j+1]
		switch conv {
		case 'v': // smart float (Tim extension)
			result.WriteString("%.15g")
			isFloatArg[argPos] = true
			argPos++
		case 'b': // boolean (Tim extension)
			result.WriteString("%s")
			boolPositions[argPos] = true
			argPos++
		case 'f', 'F', 'g', 'G', 'e', 'E', 'a', 'A':
			result.WriteString(spec)
			isFloatArg[argPos] = true
			argPos++
		case 's', 'p':
			result.WriteString(spec)
			isPtrArg[argPos] = true
			argPos++
		case 'd', 'i', 'u', 'x', 'X', 'o', 'c':
			result.WriteString(spec)
			argPos++
		default:
			result.WriteString(spec) // unknown - pass through, consumes no arg
		}
		i = j + 1
	}
	processedFormat = result.String()

	// If we have boolean arguments, create yes/no string labels
	var yesLabel, noLabel string
	if len(boolPositions) > 0 {
		yesLabel = fmt.Sprintf("bool_yes_%d", acg.stringCounter)
		noLabel = fmt.Sprintf("bool_no_%d", acg.stringCounter)
		acg.eb.Define(yesLabel, "yes\x00")
		acg.eb.Define(noLabel, "no\x00")
	}

	// Store format string in rodata
	labelName := fmt.Sprintf("str_%d", acg.stringCounter)
	acg.stringCounter++
	formatStr := processedFormat + "\x00"
	acg.eb.Define(labelName, formatStr)

	// Add printf to needed functions
	acg.eb.useDynamicLinking = true
	funcName := "printf"
	found := slices.Contains(acg.eb.neededFunctions, funcName)
	if !found {
		acg.eb.neededFunctions = append(acg.eb.neededFunctions, funcName)
	}

	numArgs := len(call.Args) - 1 // Excluding format string

	// Apple's ARM64 ABI differs from AAPCS64: the fixed arguments (just the
	// format string, in x0) use registers, but EVERY variadic argument is
	// passed on the stack in an 8-byte slot. Passing them in registers (as the
	// generic path below does) makes printf read garbage, so macOS needs its
	// own layout.
	if acg.eb.target.OS() == OSDarwin {
		varSize := uint32((numArgs*8 + 15) &^ 15)
		if varSize > 0 {
			if err := acg.out.SubImm64("sp", "sp", varSize); err != nil {
				return err
			}
		}
		for i := range numArgs {
			arg := call.Args[i+1]
			if se, ok := arg.(*StringExpr); ok {
				// String literal: store a pointer to its bytes.
				strLabel := fmt.Sprintf("str_%d", acg.stringCounter)
				acg.stringCounter++
				acg.eb.Define(strLabel, se.Value+"\x00")
				off := uint64(acg.eb.text.Len())
				acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{offset: off, symbolName: strLabel})
				acg.out.out.writer.WriteBytes([]byte{0x09, 0x00, 0x00, 0x90}) // ADRP x9, #0
				acg.out.out.writer.WriteBytes([]byte{0x29, 0x01, 0x00, 0x91}) // ADD x9, x9, #0
				if err := acg.out.StrImm64("x9", "sp", int32(i*8)); err != nil {
					return err
				}
				continue
			}
			if err := acg.compileExpression(arg); err != nil {
				return err
			}
			// Value now in d0; store it in the slot in the form printf expects.
			if boolPositions[i] {
				// %b -> %s: store pointer to "yes"/"no".
				acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x67, 0x9e}) // fmov d1, xzr
				acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e}) // fcmp d0, d1
				noJump := acg.eb.text.Len()
				acg.out.BranchCond("eq", 0)
				off := uint64(acg.eb.text.Len())
				acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{offset: off, symbolName: yesLabel})
				acg.out.out.writer.WriteBytes([]byte{0x09, 0x00, 0x00, 0x90}) // ADRP x9, #0
				acg.out.out.writer.WriteBytes([]byte{0x29, 0x01, 0x00, 0x91}) // ADD x9, x9, #0
				endJump := acg.eb.text.Len()
				acg.out.Branch(0)
				noPos := acg.eb.text.Len()
				acg.patchJumpOffset(noJump, int32(noPos-noJump))
				off = uint64(acg.eb.text.Len())
				acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{offset: off, symbolName: noLabel})
				acg.out.out.writer.WriteBytes([]byte{0x09, 0x00, 0x00, 0x90}) // ADRP x9, #0
				acg.out.out.writer.WriteBytes([]byte{0x29, 0x01, 0x00, 0x91}) // ADD x9, x9, #0
				endPos := acg.eb.text.Len()
				acg.patchJumpOffset(endJump, int32(endPos-endJump))
				if err := acg.out.StrImm64("x9", "sp", int32(i*8)); err != nil {
					return err
				}
			} else if isFloatArg[i] {
				if err := acg.out.StrImm64Double("d0", "sp", int32(i*8)); err != nil {
					return err
				}
			} else if isPtrArg[i] {
				if err := acg.out.FmovDoubleToGP("x9", "d0"); err != nil {
					return err
				}
				if err := acg.out.StrImm64("x9", "sp", int32(i*8)); err != nil {
					return err
				}
			} else {
				// Integer specifier (%d/%x): convert float64 -> int64.
				acg.out.out.writer.WriteBytes([]byte{0x09, 0x00, 0x78, 0x9e}) // fcvtzs x9, d0
				if err := acg.out.StrImm64("x9", "sp", int32(i*8)); err != nil {
					return err
				}
			}
		}
		// x0 = format string.
		off := uint64(acg.eb.text.Len())
		acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{offset: off, symbolName: labelName})
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x90}) // ADRP x0, #0
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x91}) // ADD x0, x0, #0
		pos := acg.eb.text.Len()
		acg.eb.callPatches = append(acg.eb.callPatches, CallPatch{position: pos, targetName: "printf$stub"})
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x94}) // bl printf$stub (patched)
		if varSize > 0 {
			if err := acg.out.AddImm64("sp", "sp", varSize); err != nil {
				return err
			}
		}
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0
		return nil
	}

	// On ARM64 macOS, variadic arguments are passed in registers x0-x7 and d0-d7
	// x0 is for format string. x1-x7 for remaining int/ptr args.
	// d0-d7 for float args.

	// Pre-evaluate all arguments and save to stack to avoid register clobbering
	// Use 16-byte alignment per argument for simplicity and safety
	stackSize := uint32(numArgs * 16)
	if numArgs > 0 {
		if err := acg.out.SubImm64("sp", "sp", stackSize); err != nil {
			return err
		}

		for i := range numArgs {
			arg := call.Args[i+1]
			if strExpr, ok := arg.(*StringExpr); ok {
				// String literal -> pointer
				strLabel := fmt.Sprintf("str_%d", acg.stringCounter)
				acg.stringCounter++
				acg.eb.Define(strLabel, strExpr.Value+"\x00")

				// Load address into x0
				offset := uint64(acg.eb.text.Len())
				acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{
					offset:     offset,
					symbolName: strLabel,
				})
				acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x90}) // ADRP x0, #0
				acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x91}) // ADD x0, x0, #0
				// bits to d0
				acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x67, 0x9e}) // fmov d0, x0
			} else {
				if err := acg.compileExpression(arg); err != nil {
					return err
				}
			}
			// Save d0 to stack
			if err := acg.out.StrImm64Double("d0", "sp", int32(i*16)); err != nil {
				return err
			}
		}
	}

	// Now load registers from stack
	nextX := 1
	nextD := 0

	for i := range numArgs {
		// Load from stack to d0
		if err := acg.out.LdrImm64Double("d0", "sp", int32(i*16)); err != nil {
			return err
		}

		if isFloatArg[i] {
			if nextD < 8 {
				// Move d0 to d(nextD)
				regNum := uint32(nextD)
				instr := uint32(0x1e604000) | (0 << 5) | regNum // fmov d(nextD), d0
				acg.out.out.writer.WriteBytes([]byte{byte(instr), byte(instr >> 8), byte(instr >> 16), byte(instr >> 24)})
				nextD++
			}
		} else if boolPositions[i] {
			// Boolean logic...
			// Compare d0 with 0.0
			acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x67, 0x9e}) // fmov d1, xzr
			acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x61, 0x1e}) // fcmp d0, d1

			noJumpPos := acg.eb.text.Len()
			acg.out.BranchCond("eq", 0)

			// yes
			offset := uint64(acg.eb.text.Len())
			acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{offset: offset, symbolName: yesLabel})
			acg.out.out.writer.WriteBytes([]byte{0x09, 0x00, 0x00, 0x90}) // ADRP x9, #0
			acg.out.out.writer.WriteBytes([]byte{0x29, 0x01, 0x00, 0x91}) // ADD x9, x9, #0

			endJumpPos := acg.eb.text.Len()
			acg.out.Branch(0)

			noPos := acg.eb.text.Len()
			acg.patchJumpOffset(noJumpPos, int32(noPos-noJumpPos))
			offset = uint64(acg.eb.text.Len())
			acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{offset: offset, symbolName: noLabel})
			acg.out.out.writer.WriteBytes([]byte{0x09, 0x00, 0x00, 0x90})
			acg.out.out.writer.WriteBytes([]byte{0x29, 0x01, 0x00, 0x91})

			endPos := acg.eb.text.Len()
			acg.patchJumpOffset(endJumpPos, int32(endPos-endJumpPos))

			if nextX < 8 {
				// mov x(nextX), x9
				regNum := uint32(nextX)
				instr := uint32(0xaa0003e0) | (9 << 16) | regNum
				acg.out.out.writer.WriteBytes([]byte{byte(instr), byte(instr >> 8), byte(instr >> 16), byte(instr >> 24)})
				nextX++
			}
		} else {
			// Integer/Pointer
			if nextX < 8 {
				regName := fmt.Sprintf("x%d", nextX)
				if isPtrArg[i] {
					// Pointer: transfer bits from d0 to xN
					if err := acg.out.FmovDoubleToGP(regName, "d0"); err != nil {
						return err
					}
				} else {
					// Integer: convert float64 to int64
					// fcvtzs x(nextX), d0
					regNum := uint32(nextX)
					instr := uint32(0x9e780000) | (0 << 5) | regNum
					acg.out.out.writer.WriteBytes([]byte{byte(instr), byte(instr >> 8), byte(instr >> 16), byte(instr >> 24)})
				}
				nextX++
			}
		}
	}

	// Load format string into x0
	offset := uint64(acg.eb.text.Len())
	acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{
		offset:     offset,
		symbolName: labelName,
	})
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x90}) // ADRP x0, #0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x91}) // ADD x0, x0, #0

	// Call printf
	stubLabel := "printf"
	if acg.eb.target.OS() == OSDarwin {
		stubLabel = "printf$stub"
	}
	pos := acg.eb.text.Len()
	acg.eb.callPatches = append(acg.eb.callPatches, CallPatch{position: pos, targetName: stubLabel})
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x94})

	// Restore stack
	if numArgs > 0 {
		acg.out.AddImm64("sp", "sp", stackSize)
	}

	// Result in d0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0
	return nil
}

// compileMathFunction compiles a call to a C math library function (sin, cos, sqrt, etc.)
func (acg *ARM64CodeGen) compileMathFunction(call *CallExpr) error {
	if len(call.Args) != 1 {
		return fmt.Errorf("%s requires exactly 1 argument", call.Function)
	}

	// Compile the argument - result will be in d0
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}

	// Argument is already in d0 (ARM64 ABI: first float arg in d0)

	// Mark that we need dynamic linking
	acg.eb.useDynamicLinking = true

	// Map function names to C library names (e.g., abs -> fabs)
	funcName := call.Function
	if funcName == "abs" {
		funcName = "fabs" // Use fabs for floating-point absolute value
	}

	// Add function to needed functions list if not already there
	found := slices.Contains(acg.eb.neededFunctions, funcName)
	if !found {
		acg.eb.neededFunctions = append(acg.eb.neededFunctions, funcName)
	}

	// Generate call to function stub
	stubLabel := funcName + "$stub"
	position := acg.eb.text.Len()
	acg.eb.callPatches = append(acg.eb.callPatches, CallPatch{
		position:   position,
		targetName: stubLabel,
	})

	// Emit placeholder bl instruction (will be patched later)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x94}) // bl #0

	// Result is returned in d0 (ARM64 ABI: float return value in d0)
	// No conversion needed, d0 already has the result

	return nil
}

// compilePowFunction compiles a call to pow(x, y)
func (acg *ARM64CodeGen) compilePowFunction(call *CallExpr) error {
	if len(call.Args) != 2 {
		return fmt.Errorf("pow requires exactly 2 arguments")
	}

	// Compile first argument (base) - result will be in d0
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}

	// Save first argument to d1 temporarily (we'll move it back)
	// fmov d8, d0 (use callee-saved register d8)
	acg.out.out.writer.WriteBytes([]byte{0x08, 0x40, 0x60, 0x1e})

	// Compile second argument (exponent) - result will be in d0
	if err := acg.compileExpression(call.Args[1]); err != nil {
		return err
	}

	// Move second argument to d1 (ARM64 ABI: second float arg in d1)
	// fmov d1, d0
	acg.out.out.writer.WriteBytes([]byte{0x01, 0x40, 0x60, 0x1e})

	// Move first argument back to d0 (ARM64 ABI: first float arg in d0)
	// fmov d0, d8
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x41, 0x60, 0x1e})

	// Mark that we need dynamic linking
	acg.eb.useDynamicLinking = true

	// Add function to needed functions list (pow, atan2, etc.)
	funcName := call.Function
	found := slices.Contains(acg.eb.neededFunctions, funcName)
	if !found {
		acg.eb.neededFunctions = append(acg.eb.neededFunctions, funcName)
	}

	// Generate call to function stub
	stubLabel := funcName + "$stub"
	position := acg.eb.text.Len()
	acg.eb.callPatches = append(acg.eb.callPatches, CallPatch{
		position:   position,
		targetName: stubLabel,
	})

	// Emit placeholder bl instruction
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x94}) // bl #0

	// Result is returned in d0
	return nil
}

// Confidence that this function is working: 70%
// compileCFunctionCall compiles a call to a C library function using signature information
func (acg *ARM64CodeGen) compileCFunctionCall(funcName string, args []Expression, sig *CFunctionSignature) error {
	// ARM64 calling convention:
	// Integer/pointer args: x0-x7
	// Float args: d0-d7
	// Return value: x0 (integer/pointer) or d0 (float)

	// For simplicity, we'll assume:
	// - All Tim values are float64 (our internal representation)
	// - Pointer types need conversion from float64 bits to integer register
	// - Integer types need fcvtzs conversion from float64 to int
	// - Float types stay in float registers

	numParams := len(sig.Params)
	numArgs := len(args)

	// Allow variadic functions (printf, etc.) to have more args than params
	if numArgs < numParams {
		return fmt.Errorf("%s requires at least %d arguments (got %d)", funcName, numParams, numArgs)
	}

	if numArgs > 8 {
		return fmt.Errorf("%s: too many arguments (max 8, got %d)", funcName, numArgs)
	}

	// Determine which arguments are integers/pointers vs floats
	argTypes := make([]string, numArgs)
	for i := range numArgs {
		if i < numParams {
			// Use signature information
			paramType := sig.Params[i].Type
			if isPointerType(paramType) {
				argTypes[i] = "ptr"
			} else if strings.Contains(paramType, "int") || strings.Contains(paramType, "long") ||
				strings.Contains(paramType, "short") || strings.Contains(paramType, "char") ||
				strings.Contains(paramType, "size") || strings.Contains(paramType, "bool") {
				argTypes[i] = "int"
			} else if strings.Contains(paramType, "float") {
				argTypes[i] = "float32"
			} else if strings.Contains(paramType, "double") {
				argTypes[i] = "float64"
			} else {
				// Unknown type - assume int for safety
				argTypes[i] = "int"
			}
		} else {
			// Variadic argument - check for explicit cast
			if castExpr, ok := args[i].(*CastExpr); ok {
				argTypes[i] = castExpr.Type
			} else {
				// Default to float64 for variadic args
				argTypes[i] = "float64"
			}
		}
	}

	// Save arguments to stack first (evaluate all expressions)
	// Calculate stack space needed (8 bytes per argument, 16-byte aligned)
	stackSize := ((numArgs * 8) + 15) &^ 15
	if stackSize > 0 {
		if err := acg.out.SubImm64("sp", "sp", uint32(stackSize)); err != nil {
			return err
		}

		for i := range numArgs {
			// Check for StringExpr when expecting a pointer (const char*)
			if strExpr, ok := args[i].(*StringExpr); ok && argTypes[i] == "ptr" {
				// Handle C string
				// Store format string in rodata
				labelName := fmt.Sprintf("cstr_%d", acg.stringCounter)
				acg.stringCounter++

				// Add null terminator
				cstr := strExpr.Value + "\x00"
				acg.eb.Define(labelName, cstr)

				// Load address into x0
				offset := uint64(acg.eb.text.Len())
				acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{
					offset:     offset,
					symbolName: labelName,
				})
				// ADRP x0, label@PAGE
				acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x90})
				// ADD x0, x0, label@PAGEOFF
				acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x91})

				// String pointer -> d0 numerically (scvtf), matching the unified
				// numeric pointer convention used by all FFI pointers below.
				acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0
			} else {
				if err := acg.compileExpression(args[i]); err != nil {
					return err
				}

				// If argument is a number 0 and type is ptr, it's NULL
				// compileExpression puts number in d0. If it was 0, d0 is 0.0.
				// fmov x0, d0 will make x0 = 0.
			}

			// Store d0 at [sp, #(i*8)]
			offset := int32(i * 8)
			if err := acg.out.StrImm64Double("d0", "sp", offset); err != nil {
				return err
			}
		}
	}

	// Load arguments into appropriate registers
	intRegNum := 0
	floatRegNum := 0

	for i := range numArgs {
		argType := argTypes[i]
		offset := int32(i * 8)
		isIntArg := (argType == "int" || argType == "ptr")

		if isIntArg {
			if intRegNum >= 8 {
				return fmt.Errorf("%s: too many integer/pointer arguments", funcName)
			}
			xreg := fmt.Sprintf("x%d", intRegNum)
			// Load the spilled float64 bit pattern into a scratch FP register
			// (d16 — not an argument register) so float args already placed in
			// d0-d7 are not clobbered, then convert into the GP arg register.
			if err := acg.out.LdrImm64Double("d16", "sp", offset); err != nil {
				return err
			}
			if argType == "ptr" {
				// Pointer arg: recover the raw pointer from the numeric value
				// (fcvtzs), matching c.malloc/write_*/cstruct and the FFI pointer
				// return below. (A bit-reinterpret would mis-pass numeric pointers.)
				if err := acg.out.FcvtzsDoubleToInt64(xreg, "d16"); err != nil {
					return err
				}
			} else {
				if err := acg.out.FcvtzsDoubleToInt64(xreg, "d16"); err != nil {
					return err
				}
			}
			intRegNum++
		} else {
			// Float argument: load directly into its destination register dN.
			// (The old code round-tripped every arg through d0, so loading the
			// second float arg clobbered the first — pow/atan2 got both args
			// equal to the last one.)
			if floatRegNum >= 8 {
				return fmt.Errorf("%s: too many float arguments", funcName)
			}
			dreg := fmt.Sprintf("d%d", floatRegNum)
			if err := acg.out.LdrImm64Double(dreg, "sp", offset); err != nil {
				return err
			}
			floatRegNum++
		}
	}

	// Restore stack pointer
	if stackSize > 0 {
		if err := acg.out.AddImm64("sp", "sp", uint32(stackSize)); err != nil {
			return err
		}
	}

	// Mark that we need dynamic linking
	acg.eb.useDynamicLinking = true

	// Add function to needed functions list
	found := slices.Contains(acg.eb.neededFunctions, funcName)
	if !found {
		acg.eb.neededFunctions = append(acg.eb.neededFunctions, funcName)
	}

	// Generate call to function stub
	stubLabel := funcName + "$stub"
	position := acg.eb.text.Len()
	acg.eb.callPatches = append(acg.eb.callPatches, CallPatch{
		position:   position,
		targetName: stubLabel,
	})

	// Emit placeholder bl instruction
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x94}) // bl #0

	// Handle return value conversion
	returnType := sig.ReturnType
	if isPointerType(returnType) {
		// Pointer return: store the pointer numerically (scvtf), the unified FFI
		// pointer convention (matches c.malloc, write_*, cstruct, and ptr args).
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e}) // scvtf d0, x0
	} else if strings.Contains(returnType, "int") || strings.Contains(returnType, "long") ||
		strings.Contains(returnType, "short") || strings.Contains(returnType, "char") ||
		strings.Contains(returnType, "size") || strings.Contains(returnType, "bool") {
		// Integer return: convert x0 to float64 in d0
		// scvtf d0, x0
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
	}
	// else: float/double return already in d0

	return nil
}

// compileGetPid compiles a getpid() call via dynamic linking
func (acg *ARM64CodeGen) compileGetPid(call *CallExpr) error {
	// Mark that we need dynamic linking
	acg.eb.useDynamicLinking = true

	// Add getpid to needed functions list if not already there
	funcName := "getpid" // Note: macho.go will add underscore prefix for macOS
	found := slices.Contains(acg.eb.neededFunctions, funcName)
	if !found {
		acg.eb.neededFunctions = append(acg.eb.neededFunctions, funcName)
	}

	// Generate a call through the stub
	// We'll create a stub for each imported function
	// For now, use PC-relative branch placeholder (will need stub generation later)
	stubLabel := funcName + "$stub"

	// bl stub (branch with link)
	// This is a placeholder - we'll need to patch it with actual stub address
	position := acg.eb.text.Len()
	acg.eb.callPatches = append(acg.eb.callPatches, CallPatch{
		position:   position,
		targetName: stubLabel,
	})

	// Emit placeholder bl instruction (will be patched)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x94}) // bl #0

	// Result is in x0 (integer), convert to float64 in d0
	// scvtf d0, x0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})

	return nil
}

// generateLambdaFunctions generates code for all lambda functions
func (acg *ARM64CodeGen) generateLambdaFunctions() error {
	if VerboseMode {
		debugf("DEBUG generateLambdaFunctions: generating %d lambdas\n", len(acg.lambdaFuncs))
	}

	// Index-based loop: compiling a lambda body can discover nested lambdas and
	// append them to acg.lambdaFuncs, which must also be generated. A range loop
	// would skip those late arrivals, leaving their labels undefined.
	for i := 0; i < len(acg.lambdaFuncs); i++ {
		lambda := acg.lambdaFuncs[i]
		if VerboseMode {
			debugf("DEBUG generateLambdaFunctions: generating lambda '%s'\n", lambda.Name)
		}

		// Mark the start of the lambda function with a label
		acg.eb.MarkLabel(lambda.Name)

		// Record where the function starts (including prologue, for recursion)
		funcStart := acg.eb.text.Len()

		// Function prologue - ARM64 ABI
		// Calculate total stack frame size upfront (similar to x86_64)
		// Layout: [saved fp/lr (16)] + [params (N*8)] + [temp space (2048)]
		// Temp space accounts for local variables, nested arithmetic, function calls, etc.
		// Keep under 4095 bytes to fit in 12-bit immediate
		paramCount := len(lambda.Params)
		frameSize := uint32((16 + paramCount*8 + 2048 + 15) &^ 15)

		// Save frame pointer and link register
		if err := acg.out.SubImm64("sp", "sp", frameSize); err != nil {
			return err
		}
		if err := acg.out.StrImm64("x29", "sp", 0); err != nil {
			return err
		}
		if err := acg.out.StrImm64("x30", "sp", 8); err != nil {
			return err
		}
		// Set frame pointer
		if err := acg.out.AddImm64("x29", "sp", 0); err != nil {
			return err
		}

		// Save previous state
		oldStackVars := acg.stackVars
		oldStackSize := acg.stackSize
		oldCurrentLambda := acg.currentLambda
		oldBoxedVars := acg.boxedVars

		// Create new scope for lambda
		acg.stackVars = make(map[string]int)
		acg.stackSize = 0
		acg.currentLambda = &lambda

		// Boxed names in this scope: locals this body's nested lambdas mutate,
		// plus captures inherited by reference from the enclosing scope. Module
		// globals (shared via x28) are never boxed.
		acg.boxedVars = boxedCaptureVars(lambda.Body)
		for name := range lambda.BoxedCaptures {
			acg.boxedVars[name] = true
		}
		for name := range acg.boxedVars {
			if _, isGlobal := acg.globalSlots[name]; isGlobal {
				delete(acg.boxedVars, name)
			}
		}

		// Reserve the first frame slot for the closure pointer (passed in x9 by
		// the caller) so the body can read its captured values from [x9+8+i*8].
		acg.stackSize += 8
		acg.closurePtrOffset = int32(16 + acg.stackSize - 8)
		if err := acg.out.StrImm64("x9", "x29", acg.closurePtrOffset); err != nil {
			return err
		}

		// Add the lambda's own variable name to scope for self-recursion
		// Mark it with a special offset so we know it's a function pointer
		if lambda.VarName != "" {
			acg.stackVars[lambda.VarName] = -1 // Special marker for self-reference
		}

		// Store parameters from d0-d7 registers to stack
		// Parameters come in d0, d1, d2, d3, d4, d5, d6, d7 (AAPCS64)
		// Store them at positive offsets after saved registers (like regular variables)
		for i, paramName := range lambda.Params {
			if i >= 8 {
				return fmt.Errorf("lambda has too many parameters (max 8)")
			}

			// Allocate stack space for parameter (8 bytes for float64)
			acg.stackSize += 8
			paramOffset := acg.stackSize
			acg.stackVars[paramName] = paramOffset

			// A `(a as V)` cstruct-typed param: record the type so `a.x` reads the
			// field directly, no manual `aa = a as V` needed.
			if ct, ok := lambda.ParamCStructTypes[paramName]; ok {
				if _, isStruct := acg.cstructs[ct]; isStruct {
					acg.varCStructType[paramName] = ct
				}
			}

			// Store parameter from d register to stack at positive offset
			// x29 points to saved fp, variables start at offset 16
			// str dN, [x29, #(16 + paramOffset - 8)]
			regName := fmt.Sprintf("d%d", i)
			offset := int32(16 + paramOffset - 8)
			if err := acg.out.StrImm64Double(regName, "x29", offset); err != nil {
				return err
			}
		}

		// Record where the lambda body starts (for tail recursion with "me")
		bodyStart := acg.eb.text.Len()
		acg.currentLambda.BodyStart = bodyStart
		acg.currentLambda.FuncStart = funcStart

		// Push defer scope for lambda
		acg.pushDeferScope()

		// Compile lambda body (result in d0)
		if err := acg.compileExpression(lambda.Body); err != nil {
			return err
		}

		// Pop defer scope and execute deferred expressions
		if err := acg.popDeferScope(); err != nil {
			return err
		}

		// Clear lambda context
		acg.currentLambda = nil

		// Function epilogue - ARM64 ABI
		// Restore registers first (from bottom of frame)
		// ldp x29, x30, [sp]
		acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0x40, 0xa9})

		// Restore stack pointer (deallocate locals)
		if err := acg.out.AddImm64("sp", "sp", frameSize); err != nil {
			return err
		}

		if err := acg.out.Return("x30"); err != nil {
			return err
		}

		// Restore previous state
		acg.stackVars = oldStackVars
		acg.stackSize = oldStackSize
		acg.currentLambda = oldCurrentLambda
		acg.boxedVars = oldBoxedVars
	}

	return nil
}

// getExprType returns the type of an expression for ARM64 code generation
func (acg *ARM64CodeGen) getExprType(expr Expression) string {
	switch e := expr.(type) {
	case *StringExpr:
		return "string"
	case *FStringExpr:
		return "string"
	case *NumberExpr:
		return "number"
	case *ListExpr:
		return "list"
	case *MapExpr:
		return "map"
	case *IdentExpr:
		// Look up in varTypes
		if typ, exists := acg.varTypes[e.Name]; exists {
			return typ
		}
		// Default to number if not tracked (most variables are numbers)
		return "number"
	case *BinaryExpr:
		// Binary expressions between strings return strings if operator is "+"
		if e.Operator == "+" {
			leftType := acg.getExprType(e.Left)
			rightType := acg.getExprType(e.Right)
			if leftType == "string" && rightType == "string" {
				return "string"
			}
			if leftType == "list" && rightType == "list" {
				return "list"
			}
		}
		return "number"
	case *CallExpr:
		// Function calls - check if function returns a string
		stringFuncs := map[string]bool{
			"str": true, "read_file": true, "readln": true,
			"upper": true, "lower": true, "trim": true,
			"_error_code_extract": true,
		}
		if stringFuncs[e.Function] {
			return "string"
		}
		// Other functions return numbers by default
		return "number"
	case *SliceExpr:
		// Slicing preserves the type of the list
		return acg.getExprType(e.List)
	case *ParallelExpr:
		// Parallel expr returns a list
		return "list"
	default:
		return "unknown"
	}
}

// emitMapKeyLookup searches a Tim map/string for a key. On entry x0 = pointer,
// x1 = key (integer). It leaves the matching value in d0, or 0.0 if the key is
// absent. Maps/strings are laid out [count][key0][val0][key1][val1]... with
// 16-byte (key,value) pairs, so pair i has its key at ptr+8+i*16.
func (acg *ARM64CodeGen) emitMapKeyLookup() error {
	if err := acg.out.LdrImm64Double("d1", "x0", 0); err != nil { // count
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x2", "d1"); err != nil {
		return err
	}
	if err := acg.out.MovImm64("x3", 0); err != nil { // i = 0
		return err
	}
	loopStart := acg.eb.text.Len()
	if err := acg.out.CmpReg64("x3", "x2"); err != nil {
		return err
	}
	notFoundJump := acg.eb.text.Len()
	acg.out.BranchCond("ge", 0)                             // i >= count -> not found
	if err := acg.out.LslImm64("x5", "x3", 4); err != nil { // i*16
		return err
	}
	if err := acg.out.AddReg64("x4", "x0", "x5"); err != nil {
		return err
	}
	if err := acg.out.AddImm64("x4", "x4", 8); err != nil { // x4 = &key[i]
		return err
	}
	if err := acg.out.LdrImm64Double("d1", "x4", 0); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x6", "d1"); err != nil {
		return err
	}
	if err := acg.out.CmpReg64("x6", "x1"); err != nil {
		return err
	}
	foundJump := acg.eb.text.Len()
	acg.out.BranchCond("eq", 0) // key matches
	if err := acg.out.AddImm64("x3", "x3", 1); err != nil {
		return err
	}
	backPos := acg.eb.text.Len()
	acg.out.Branch(0)
	acg.patchJumpOffset(backPos, int32(loopStart-backPos))

	// found: value sits right after the key.
	foundLabel := acg.eb.text.Len()
	acg.patchJumpOffset(foundJump, int32(foundLabel-foundJump))
	if err := acg.out.LdrImm64Double("d0", "x4", 8); err != nil {
		return err
	}
	endJump := acg.eb.text.Len()
	acg.out.Branch(0)

	// not found: 0.0
	notFoundLabel := acg.eb.text.Len()
	acg.patchJumpOffset(notFoundJump, int32(notFoundLabel-notFoundJump))
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x67, 0x9e}) // fmov d0, xzr

	endLabel := acg.eb.text.Len()
	acg.patchJumpOffset(endJump, int32(endLabel-endJump))
	return nil
}

// compileHead compiles head(list): the first element of a list. Lists store
// their pointer as a numeric double, so recover it with fcvtzs and load elem 0
// (which sits just past the 8-byte length field).
func (acg *ARM64CodeGen) compileHead(call *CallExpr) error {
	if len(call.Args) != 1 {
		return fmt.Errorf("head() requires exactly 1 argument")
	}
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil {
		return err
	}
	return acg.out.LdrImm64Double("d0", "x0", 8)
}

// compileTail compiles tail(list): a freshly allocated copy of the list with
// the first element removed. _tim_tail returns the new pointer in x0, which is
// converted back to the numeric double convention with scvtf.
func (acg *ARM64CodeGen) compileTail(call *CallExpr) error {
	if len(call.Args) != 1 {
		return fmt.Errorf("tail() requires exactly 1 argument")
	}
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil {
		return err
	}
	if err := acg.eb.GenerateCallInstruction("_tim_tail"); err != nil {
		return err
	}
	return acg.out.ScvtfInt64ToDouble("d0", "x0")
}

// compileAppend compiles append(list, value): a freshly allocated copy of the
// list with value appended. _tim_append takes the list pointer in x0 and the
// value in d0; the list pointer is spilled across the value's evaluation.
func (acg *ARM64CodeGen) compileAppend(call *CallExpr) error {
	if len(call.Args) != 2 {
		return fmt.Errorf("append() requires exactly 2 arguments")
	}
	// Evaluate the list, recover its pointer, and spill it to the stack.
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil {
		return err
	}
	acg.out.SubImm64("sp", "sp", 16)
	if err := acg.out.StrImm64("x0", "sp", 0); err != nil {
		return err
	}
	// Evaluate the value (result in d0), then restore the list pointer to x0.
	if err := acg.compileExpression(call.Args[1]); err != nil {
		return err
	}
	if err := acg.out.LdrImm64("x0", "sp", 0); err != nil {
		return err
	}
	acg.out.AddImm64("sp", "sp", 16)
	if err := acg.eb.GenerateCallInstruction("_tim_append"); err != nil {
		return err
	}
	return acg.out.ScvtfInt64ToDouble("d0", "x0")
}

// compilePop compiles pop(list): removes the last element. _tim_pop returns a
// pointer to a flat 2-tuple [new_list_ptr, popped_value] which a MultipleAssign
// destructures (element i at offset 8+i*8). The pointer is converted back to the
// numeric double convention with scvtf.
func (acg *ARM64CodeGen) compilePop(call *CallExpr) error {
	if len(call.Args) != 1 {
		return fmt.Errorf("pop() requires exactly 1 argument")
	}
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil {
		return err
	}
	if err := acg.eb.GenerateCallInstruction("_tim_pop"); err != nil {
		return err
	}
	return acg.out.ScvtfInt64ToDouble("d0", "x0")
}

// compileError compiles error("abc"): builds an error-NaN whose mantissa packs
// the first three characters of the code string (matching the .error extractor
// and the x86 backend). The string's char i lives at ptr+16+i*16.
func (acg *ARM64CodeGen) compileError(call *CallExpr) error {
	if len(call.Args) != 1 {
		return fmt.Errorf("error() requires exactly 1 argument")
	}
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x0", "d0"); err != nil { // x0 = string ptr
		return err
	}
	// x1 accumulates the packed code; chars go to bits 31:24, 23:16, 15:8.
	for i, shift := range []uint32{24, 16, 8} {
		if err := acg.out.LdrImm64Double("d1", "x0", int32(16+i*16)); err != nil {
			return err
		}
		if err := acg.out.FcvtzsDoubleToInt64("x2", "d1"); err != nil {
			return err
		}
		if err := acg.out.LslImm64("x2", "x2", shift); err != nil {
			return err
		}
		if i == 0 {
			if err := acg.out.MovReg64("x1", "x2"); err != nil {
				return err
			}
		} else if err := acg.out.OrrReg64("x1", "x1", "x2"); err != nil {
			return err
		}
	}
	// OR in the quiet-NaN base and reinterpret the bits as the error value.
	if err := acg.out.MovImm64("x0", 0x7FF8000000000000); err != nil {
		return err
	}
	if err := acg.out.OrrReg64("x0", "x0", "x1"); err != nil {
		return err
	}
	return acg.out.FmovGPToDouble("d0", "x0")
}

// compileIsNan compiles is_nan(x): 1.0 if x is NaN, else 0.0. fcmp of a value
// against itself sets the V (overflow/unordered) flag exactly when it is NaN.
func (acg *ARM64CodeGen) compileIsNan(call *CallExpr) error {
	if len(call.Args) != 1 {
		return fmt.Errorf("is_nan() requires exactly 1 argument")
	}
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x60, 0x1e}) // fcmp d0, d0
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x77, 0x9f, 0x9a}) // cset x0, vs
	return acg.out.ScvtfInt64ToDouble("d0", "x0")
}

// compileErrorCodeExtract compiles the .error property: it extracts the 4-byte
// error code packed into an error-NaN's mantissa and returns it as a 3-character
// Tim string (e.g. "dv0"). A non-error (non-NaN) value yields the empty string.
func (acg *ARM64CodeGen) compileErrorCodeExtract(call *CallExpr) error {
	if len(call.Args) != 1 {
		return fmt.Errorf("_error_code_extract requires exactly 1 argument")
	}
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}
	// fcmp d0, d0 — sets the V (overflow) flag when d0 is NaN (unordered).
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x20, 0x60, 0x1e})
	nanPos := acg.eb.text.Len()
	acg.out.BranchCond("vs", 0) // b.vs nan_path

	// Not an error: return an empty string (count = 0.0).
	if err := acg.out.MovImm64("x0", 8); err != nil {
		return err
	}
	if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
		return err
	}
	if err := acg.out.MovImm64("x1", 0); err != nil {
		return err
	}
	if err := acg.out.StrImm64("x1", "x0", 0); err != nil {
		return err
	}
	if err := acg.out.ScvtfInt64ToDouble("d0", "x0"); err != nil {
		return err
	}
	endPos := acg.eb.text.Len()
	acg.out.Branch(0) // b end

	// nan_path: build the 3-char error string from the mantissa bytes.
	nanLabel := acg.eb.text.Len()
	acg.patchJumpOffset(nanPos, int32(nanLabel-nanPos))
	if err := acg.out.FmovDoubleToGP("x9", "d0"); err != nil { // x9 = raw bits
		return err
	}
	acg.out.SubImm64("sp", "sp", 32)
	if err := acg.out.MovImm64("x10", 0xff); err != nil { // byte mask
		return err
	}
	// Characters live at bits 31:24, 23:16, 15:8 of the mantissa.
	for i, shift := range []int{24, 16, 8} {
		if err := acg.out.MovImm64("x11", uint64(shift)); err != nil {
			return err
		}
		if err := acg.out.LsrReg64("x12", "x9", "x11"); err != nil {
			return err
		}
		if err := acg.out.AndReg64("x12", "x12", "x10"); err != nil {
			return err
		}
		if err := acg.out.ScvtfInt64ToDouble("d1", "x12"); err != nil {
			return err
		}
		if err := acg.out.StrImm64Double("d1", "sp", int32(i*8)); err != nil {
			return err
		}
	}
	// Allocate the string-map: 8-byte count + 3 * 16-byte (key,value) pairs.
	if err := acg.out.MovImm64("x0", 56); err != nil {
		return err
	}
	if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
		return err
	}
	// count = 3.0 (0x4008000000000000)
	if err := acg.out.MovImm64("x1", 0x4008000000000000); err != nil {
		return err
	}
	if err := acg.out.StrImm64("x1", "x0", 0); err != nil {
		return err
	}
	for i := range 3 {
		if err := acg.out.MovImm64("x1", uint64(i)); err != nil { // key = i
			return err
		}
		if err := acg.out.StrImm64("x1", "x0", int32(8+i*16)); err != nil {
			return err
		}
		if err := acg.out.LdrImm64Double("d1", "sp", int32(i*8)); err != nil { // char value
			return err
		}
		if err := acg.out.StrImm64Double("d1", "x0", int32(16+i*16)); err != nil {
			return err
		}
	}
	acg.out.AddImm64("sp", "sp", 32)
	if err := acg.out.ScvtfInt64ToDouble("d0", "x0"); err != nil {
		return err
	}

	endLabel := acg.eb.text.Len()
	acg.patchJumpOffset(endPos, int32(endLabel-endPos))
	return nil
}

// listHelperPrologue emits the standard frame used by the list builtins:
// saves x29/x30 and callee-saved x19-x22 in a 64-byte frame and sets x29=sp.
func (acg *ARM64CodeGen) listHelperPrologue() {
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xbc, 0xa9}) // stp x29, x30, [sp, #-64]!
	acg.out.out.writer.WriteBytes([]byte{0xf3, 0x53, 0x01, 0xa9}) // stp x19, x20, [sp, #16]
	acg.out.out.writer.WriteBytes([]byte{0xf5, 0x5b, 0x02, 0xa9}) // stp x21, x22, [sp, #32]
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x03, 0x00, 0x91}) // mov x29, sp
}

// listHelperEpilogue restores the frame saved by listHelperPrologue and returns.
func (acg *ARM64CodeGen) listHelperEpilogue() {
	acg.out.out.writer.WriteBytes([]byte{0xf5, 0x5b, 0x42, 0xa9}) // ldp x21, x22, [sp, #32]
	acg.out.out.writer.WriteBytes([]byte{0xf3, 0x53, 0x41, 0xa9}) // ldp x19, x20, [sp, #16]
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xc4, 0xa8}) // ldp x29, x30, [sp], #64
	acg.out.Return("x30")
}

// emitMallocAligned allocates count*8 + 8 bytes (a list of `count` elements
// plus the length field), 16-byte aligned, with `count` already in x21.
// Result pointer is left in x0.
func (acg *ARM64CodeGen) emitListAlloc(countReg string) error {
	if err := acg.out.LslImm64("x0", countReg, 3); err != nil { // count*8
		return err
	}
	if err := acg.out.AddImm64("x0", "x0", 8); err != nil { // + length field
		return err
	}
	if err := acg.out.AddImm64("x0", "x0", 15); err != nil { // round up to 16
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0x00, 0xec, 0x7c, 0x92}) // and x0, x0, #0xfffffffffffffff0
	return acg.eb.GenerateCallInstruction("malloc")
}

// emitListCopyLoop copies `countReg` float64 elements from src to dst (both
// register names holding addresses), advancing past them. Uses x10 as counter.
func (acg *ARM64CodeGen) emitListCopyLoop(countReg, src, dst string) error {
	if err := acg.out.MovReg64("x10", countReg); err != nil {
		return err
	}
	loopStart := acg.eb.text.Len()
	if err := acg.out.CmpImm64("x10", 0); err != nil {
		return err
	}
	doneJump := acg.eb.text.Len()
	acg.out.BranchCond("eq", 0)
	if err := acg.out.LdrImm64Double("d0", src, 0); err != nil {
		return err
	}
	if err := acg.out.StrImm64Double("d0", dst, 0); err != nil {
		return err
	}
	acg.out.AddImm64(src, src, 8)
	acg.out.AddImm64(dst, dst, 8)
	acg.out.SubImm64("x10", "x10", 1)
	backPos := acg.eb.text.Len()
	acg.out.Branch(0)
	acg.patchJumpOffset(backPos, int32(loopStart-backPos))
	donePos := acg.eb.text.Len()
	acg.patchJumpOffset(doneJump, int32(donePos-doneJump))
	return nil
}

// generateListBuiltinHelpers emits _tim_tail and _tim_append.
func (acg *ARM64CodeGen) generateListBuiltinHelpers() error {
	// _tim_tail(x0=list_ptr) -> x0=new_ptr : a copy of the list without elem 0.
	acg.eb.MarkLabel("_tim_tail")
	acg.listHelperPrologue()
	acg.out.MovReg64("x19", "x0") // list ptr
	if err := acg.out.LdrImm64Double("d0", "x19", 0); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x20", "d0"); err != nil { // count
		return err
	}
	acg.out.SubImm64("x21", "x20", 1) // new count = count - 1
	if err := acg.emitListAlloc("x21"); err != nil {
		return err
	}
	acg.out.MovReg64("x22", "x0") // new ptr
	if err := acg.out.ScvtfInt64ToDouble("d0", "x21"); err != nil {
		return err
	}
	if err := acg.out.StrImm64Double("d0", "x22", 0); err != nil { // store new length
		return err
	}
	acg.out.AddImm64("x11", "x19", 16) // src = &elem1
	acg.out.AddImm64("x12", "x22", 8)  // dst = &new elem0
	if err := acg.emitListCopyLoop("x21", "x11", "x12"); err != nil {
		return err
	}
	acg.out.MovReg64("x0", "x22")
	acg.listHelperEpilogue()

	// _tim_append(x0=list_ptr, d0=value) -> x0=new_ptr : list with value appended.
	acg.eb.MarkLabel("_tim_append")
	acg.listHelperPrologue()
	if err := acg.out.StrImm64Double("d0", "sp", 48); err != nil { // stash value (survives malloc)
		return err
	}
	acg.out.MovReg64("x19", "x0")
	if err := acg.out.LdrImm64Double("d0", "x19", 0); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x20", "d0"); err != nil { // count
		return err
	}
	acg.out.AddImm64("x21", "x20", 1) // new count = count + 1
	if err := acg.emitListAlloc("x21"); err != nil {
		return err
	}
	acg.out.MovReg64("x22", "x0")
	if err := acg.out.ScvtfInt64ToDouble("d0", "x21"); err != nil {
		return err
	}
	if err := acg.out.StrImm64Double("d0", "x22", 0); err != nil { // store new length
		return err
	}
	acg.out.AddImm64("x11", "x19", 8) // src = &elem0
	acg.out.AddImm64("x12", "x22", 8) // dst = &new elem0
	if err := acg.emitListCopyLoop("x20", "x11", "x12"); err != nil {
		return err
	}
	// x12 now points at the appended slot; store the stashed value.
	if err := acg.out.LdrImm64Double("d0", "sp", 48); err != nil {
		return err
	}
	if err := acg.out.StrImm64Double("d0", "x12", 0); err != nil {
		return err
	}
	acg.out.MovReg64("x0", "x22")
	acg.listHelperEpilogue()

	// _tim_pop(x0=list_ptr) -> x0=ptr to flat 2-tuple [new_list_ptr, popped].
	// new_list is a copy without the last element; popped is the last value
	// (NaN for an empty list). new_list_ptr is stored scvtf-encoded so the
	// MultipleAssign that reads it back recovers a valid pointer.
	acg.eb.MarkLabel("_tim_pop")
	acg.listHelperPrologue()
	acg.out.MovReg64("x19", "x0") // list ptr
	if err := acg.out.LdrImm64Double("d0", "x19", 0); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x20", "d0"); err != nil { // count
		return err
	}
	if err := acg.out.CmpImm64("x20", 0); err != nil {
		return err
	}
	emptyJump := acg.eb.text.Len()
	acg.out.BranchCond("eq", 0) // empty list -> empty path

	// Non-empty: new count = count - 1; popped = last element.
	acg.out.SubImm64("x21", "x20", 1)
	if err := acg.out.LslImm64("x9", "x21", 3); err != nil { // new_count*8
		return err
	}
	if err := acg.out.AddReg64("x9", "x9", "x19"); err != nil {
		return err
	}
	if err := acg.out.LdrImm64Double("d0", "x9", 8); err != nil { // popped = elem[count-1]
		return err
	}
	if err := acg.out.StrImm64Double("d0", "sp", 48); err != nil { // stash popped
		return err
	}
	if err := acg.emitListAlloc("x21"); err != nil {
		return err
	}
	acg.out.MovReg64("x22", "x0") // new_list ptr
	if err := acg.out.ScvtfInt64ToDouble("d0", "x21"); err != nil {
		return err
	}
	if err := acg.out.StrImm64Double("d0", "x22", 0); err != nil { // new length
		return err
	}
	acg.out.AddImm64("x11", "x19", 8) // src = &elem0
	acg.out.AddImm64("x12", "x22", 8) // dst = &new elem0
	if err := acg.emitListCopyLoop("x21", "x11", "x12"); err != nil {
		return err
	}
	buildJump := acg.eb.text.Len()
	acg.out.Branch(0) // -> build tuple

	// Empty path: new_list is an empty list, popped is NaN.
	emptyLabel := acg.eb.text.Len()
	acg.patchJumpOffset(emptyJump, int32(emptyLabel-emptyJump))
	if err := acg.out.MovImm64("x21", 0); err != nil {
		return err
	}
	if err := acg.emitListAlloc("x21"); err != nil {
		return err
	}
	acg.out.MovReg64("x22", "x0")
	if err := acg.out.MovImm64("x9", 0); err != nil {
		return err
	}
	if err := acg.out.StrImm64("x9", "x22", 0); err != nil { // length 0
		return err
	}
	if err := acg.out.MovImm64("x9", 0x7FF8000000000000); err != nil { // NaN bits
		return err
	}
	if err := acg.out.StrImm64("x9", "sp", 48); err != nil { // stash NaN as popped
		return err
	}

	// Build the result tuple: [count=2.0][scvtf(new_list)][popped].
	buildLabel := acg.eb.text.Len()
	acg.patchJumpOffset(buildJump, int32(buildLabel-buildJump))
	if err := acg.out.MovImm64("x0", 32); err != nil { // 24 bytes, 16-aligned
		return err
	}
	if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
		return err
	}
	if err := acg.out.MovImm64("x9", 0x4000000000000000); err != nil { // 2.0
		return err
	}
	if err := acg.out.StrImm64("x9", "x0", 0); err != nil {
		return err
	}
	if err := acg.out.ScvtfInt64ToDouble("d0", "x22"); err != nil { // new_list ptr -> double
		return err
	}
	if err := acg.out.StrImm64Double("d0", "x0", 8); err != nil {
		return err
	}
	if err := acg.out.LdrImm64Double("d0", "sp", 48); err != nil { // popped
		return err
	}
	if err := acg.out.StrImm64Double("d0", "x0", 16); err != nil {
		return err
	}
	acg.listHelperEpilogue()

	if err := acg.generateStringEqHelper(); err != nil {
		return err
	}
	return nil
}

// generateStringEqHelper emits _tim_string_eq(x0=ptrA, x1=ptrB) -> x0 = 1 if the
// two Tim strings hold the same characters, else 0. Leaf function (no calls), so
// it needs no frame. Strings are [count][key,val pairs]; char i of a string at
// ptr sits at ptr+16+i*16.
func (acg *ARM64CodeGen) generateStringEqHelper() error {
	acg.eb.MarkLabel("_tim_string_eq")
	// countA -> x9, countB -> x10
	if err := acg.out.LdrImm64Double("d0", "x0", 0); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x9", "d0"); err != nil {
		return err
	}
	if err := acg.out.LdrImm64Double("d1", "x1", 0); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x10", "d1"); err != nil {
		return err
	}
	if err := acg.out.CmpReg64("x9", "x10"); err != nil {
		return err
	}
	neqJump1 := acg.eb.text.Len()
	acg.out.BranchCond("ne", 0) // lengths differ -> not equal

	if err := acg.out.MovImm64("x11", 0); err != nil { // i = 0
		return err
	}
	loopStart := acg.eb.text.Len()
	if err := acg.out.CmpReg64("x11", "x9"); err != nil {
		return err
	}
	eqJump := acg.eb.text.Len()
	acg.out.BranchCond("ge", 0) // i >= count -> equal

	if err := acg.out.LslImm64("x12", "x11", 4); err != nil { // i*16
		return err
	}
	if err := acg.out.AddReg64("x13", "x0", "x12"); err != nil {
		return err
	}
	if err := acg.out.LdrImm64Double("d0", "x13", 16); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x14", "d0"); err != nil {
		return err
	}
	if err := acg.out.AddReg64("x15", "x1", "x12"); err != nil {
		return err
	}
	if err := acg.out.LdrImm64Double("d1", "x15", 16); err != nil {
		return err
	}
	if err := acg.out.FcvtzsDoubleToInt64("x16", "d1"); err != nil {
		return err
	}
	if err := acg.out.CmpReg64("x14", "x16"); err != nil {
		return err
	}
	neqJump2 := acg.eb.text.Len()
	acg.out.BranchCond("ne", 0) // chars differ -> not equal
	if err := acg.out.AddImm64("x11", "x11", 1); err != nil {
		return err
	}
	backPos := acg.eb.text.Len()
	acg.out.Branch(0)
	acg.patchJumpOffset(backPos, int32(loopStart-backPos))

	// equal:
	eqLabel := acg.eb.text.Len()
	acg.patchJumpOffset(eqJump, int32(eqLabel-eqJump))
	if err := acg.out.MovImm64("x0", 1); err != nil {
		return err
	}
	if err := acg.out.Return("x30"); err != nil {
		return err
	}

	// not_equal:
	neqLabel := acg.eb.text.Len()
	acg.patchJumpOffset(neqJump1, int32(neqLabel-neqJump1))
	acg.patchJumpOffset(neqJump2, int32(neqLabel-neqJump2))
	if err := acg.out.MovImm64("x0", 0); err != nil {
		return err
	}
	return acg.out.Return("x30")
}

// generateRuntimeHelpers generates ARM64 runtime helper functions
func (acg *ARM64CodeGen) generateRuntimeHelpers() error {
	// Generate _tim_list_concat(left_ptr, right_ptr) -> new_ptr
	// Arguments: x0 = left_ptr, x1 = right_ptr
	// Returns: x0 = pointer to new concatenated list
	// List format: [length (8 bytes)][elem0 (8 bytes)][elem1 (8 bytes)]...

	acg.eb.MarkLabel("_tim_list_concat")

	// Function prologue
	// stp x29, x30, [sp, #-N]! (save fp and lr, pre-decrement sp by N)
	// We need to save: x29, x30, x19-x28 (callee-saved)
	// For simplicity, save x29, x30, x19, x20, x21, x22, x23 (7 regs = 56 bytes, round to 64)
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xbc, 0xa9}) // stp x29, x30, [sp, #-64]!
	acg.out.out.writer.WriteBytes([]byte{0xf3, 0x53, 0x01, 0xa9}) // stp x19, x20, [sp, #16]
	acg.out.out.writer.WriteBytes([]byte{0xf5, 0x5b, 0x02, 0xa9}) // stp x21, x22, [sp, #32]
	acg.out.out.writer.WriteBytes([]byte{0xf7, 0x03, 0x03, 0xa9}) // stp x23, x0, [sp, #48] (save x23 and use remaining slot for alignment)
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x03, 0x00, 0x91}) // mov x29, sp

	// Save arguments
	// x19 = left_ptr, x20 = right_ptr
	acg.out.out.writer.WriteBytes([]byte{0xf3, 0x03, 0x00, 0xaa}) // mov x19, x0
	acg.out.out.writer.WriteBytes([]byte{0xf4, 0x03, 0x01, 0xaa}) // mov x20, x1

	// Get left list length: ldr d0, [x19] then fcvtzs x21, d0
	if err := acg.out.LdrImm64Double("d0", "x19", 0); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0x15, 0x00, 0x78, 0x9e}) // fcvtzs x21, d0

	// Get right list length: ldr d0, [x20] then fcvtzs x22, d0
	if err := acg.out.LdrImm64Double("d0", "x20", 0); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0x16, 0x00, 0x78, 0x9e}) // fcvtzs x22, d0

	// Calculate total length: x23 = x21 + x22
	acg.out.out.writer.WriteBytes([]byte{0xb7, 0x02, 0x16, 0x8b}) // add x23, x21, x22

	// Calculate allocation size: x0 = 8 + x23 * 8
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0xf2, 0x7d, 0xd3}) // lsl x0, x23, #3 (multiply by 8)
	acg.out.AddImm64("x0", "x0", 8)                               // add x0, x0, #8

	// Align to 16 bytes: x0 = (x0 + 15) & ~15
	acg.out.AddImm64("x0", "x0", 15)                              // add x0, x0, #15
	acg.out.out.writer.WriteBytes([]byte{0x00, 0xec, 0x7c, 0x92}) // and x0, x0, #0xfffffffffffffff0

	// Call malloc(x0)
	if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
		return err
	}
	// x0 now contains result pointer, save it to x9
	acg.out.out.writer.WriteBytes([]byte{0xe9, 0x03, 0x00, 0xaa}) // mov x9, x0

	// Write total length to result: scvtf d0, x23 then str d0, [x9]
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x02, 0x62, 0x9e}) // scvtf d0, x23
	if err := acg.out.StrImm64Double("d0", "x9", 0); err != nil {
		return err
	}

	// Copy left list elements
	// x10 = counter (x21), x11 = src (x19 + 8), x12 = dst (x9 + 8)
	acg.out.out.writer.WriteBytes([]byte{0xaa, 0x02, 0x15, 0x8b}) // add x10, x21, x21 (x10 = x21, counter)
	acg.out.out.writer.WriteBytes([]byte{0x6b, 0x22, 0x00, 0x91}) // add x11, x19, #8
	acg.out.out.writer.WriteBytes([]byte{0x2c, 0x21, 0x00, 0x91}) // add x12, x9, #8

	// Actually just use x10 = x21 for counter
	acg.out.out.writer.WriteBytes([]byte{0xea, 0x03, 0x15, 0xaa}) // mov x10, x21

	// Loop to copy left elements
	acg.eb.MarkLabel("_list_concat_copy_left_loop")
	leftLoopStart := acg.eb.text.Len()

	// cbz x10, skip_left (if zero, skip this loop)
	leftSkipJumpPos := acg.eb.text.Len()
	acg.out.out.writer.WriteBytes([]byte{0x0a, 0x00, 0x00, 0xb4}) // cbz x10, +0 (placeholder)

	// ldr d0, [x11], str d0, [x12], increment pointers
	if err := acg.out.LdrImm64Double("d0", "x11", 0); err != nil {
		return err
	}
	if err := acg.out.StrImm64Double("d0", "x12", 0); err != nil {
		return err
	}
	acg.out.AddImm64("x11", "x11", 8) // add x11, x11, #8
	acg.out.AddImm64("x12", "x12", 8) // add x12, x12, #8
	acg.out.SubImm64("x10", "x10", 1) // sub x10, x10, #1

	// Branch back to loop start
	leftLoopEnd := acg.eb.text.Len()
	acg.out.Branch(int32(leftLoopStart - leftLoopEnd))

	// Patch the cbz to jump here
	leftSkipEndPos := acg.eb.text.Len()
	acg.patchJumpOffset(leftSkipJumpPos, int32(leftSkipEndPos-leftSkipJumpPos))

	// Copy right list elements
	// x10 = counter (x22), x11 = src (x20 + 8), x12 already points to correct position
	acg.out.out.writer.WriteBytes([]byte{0xea, 0x03, 0x16, 0xaa}) // mov x10, x22
	acg.out.out.writer.WriteBytes([]byte{0x8b, 0x22, 0x00, 0x91}) // add x11, x20, #8

	// Loop to copy right elements
	acg.eb.MarkLabel("_list_concat_copy_right_loop")
	rightLoopStart := acg.eb.text.Len()

	// cbz x10, skip_right
	rightSkipJumpPos := acg.eb.text.Len()
	acg.out.out.writer.WriteBytes([]byte{0x0a, 0x00, 0x00, 0xb4}) // cbz x10, +0 (placeholder)

	// ldr d0, [x11], str d0, [x12], increment pointers
	if err := acg.out.LdrImm64Double("d0", "x11", 0); err != nil {
		return err
	}
	if err := acg.out.StrImm64Double("d0", "x12", 0); err != nil {
		return err
	}
	acg.out.AddImm64("x11", "x11", 8) // add x11, x11, #8
	acg.out.AddImm64("x12", "x12", 8) // add x12, x12, #8
	acg.out.SubImm64("x10", "x10", 1) // sub x10, x10, #1

	// Branch back to loop start
	rightLoopEnd := acg.eb.text.Len()
	acg.out.Branch(int32(rightLoopStart - rightLoopEnd))

	// Patch the cbz
	rightSkipEndPos := acg.eb.text.Len()
	acg.patchJumpOffset(rightSkipJumpPos, int32(rightSkipEndPos-rightSkipJumpPos))

	// Return result pointer in x0
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x09, 0xaa}) // mov x0, x9

	// Function epilogue - restore registers and return
	acg.out.out.writer.WriteBytes([]byte{0xf7, 0x03, 0x43, 0xa9}) // ldp x23, x0, [sp, #48]
	acg.out.out.writer.WriteBytes([]byte{0xf5, 0x5b, 0x42, 0xa9}) // ldp x21, x22, [sp, #32]
	acg.out.out.writer.WriteBytes([]byte{0xf3, 0x53, 0x41, 0xa9}) // ldp x19, x20, [sp, #16]
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xc4, 0xa8}) // ldp x29, x30, [sp], #64
	acg.out.Return("x30")

	if err := acg.generateListBuiltinHelpers(); err != nil {
		return err
	}

	// Generate _tim_string_concat(left_ptr, right_ptr) -> new_ptr
	// Arguments: x0 = left_ptr, x1 = right_ptr
	// Returns: x0 = pointer to new concatenated string
	// String format (map): [count (8 bytes)][key0 (8)][val0 (8)]...

	acg.eb.MarkLabel("_tim_string_concat")

	// Function prologue - same as list concat
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xbc, 0xa9}) // stp x29, x30, [sp, #-64]!
	acg.out.out.writer.WriteBytes([]byte{0xf3, 0x53, 0x01, 0xa9}) // stp x19, x20, [sp, #16]
	acg.out.out.writer.WriteBytes([]byte{0xf5, 0x5b, 0x02, 0xa9}) // stp x21, x22, [sp, #32]
	acg.out.out.writer.WriteBytes([]byte{0xf7, 0x03, 0x03, 0xa9}) // stp x23, x0, [sp, #48]
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x03, 0x00, 0x91}) // mov x29, sp

	// Save arguments: x19 = left_ptr, x20 = right_ptr
	acg.out.out.writer.WriteBytes([]byte{0xf3, 0x03, 0x00, 0xaa}) // mov x19, x0
	acg.out.out.writer.WriteBytes([]byte{0xf4, 0x03, 0x01, 0xaa}) // mov x20, x1

	// Get left string length: ldr d0, [x19] then fcvtzs x21, d0
	if err := acg.out.LdrImm64Double("d0", "x19", 0); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0x15, 0x00, 0x78, 0x9e}) // fcvtzs x21, d0

	// Get right string length: ldr d0, [x20] then fcvtzs x22, d0
	if err := acg.out.LdrImm64Double("d0", "x20", 0); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0x16, 0x00, 0x78, 0x9e}) // fcvtzs x22, d0

	// Calculate total length: x23 = x21 + x22
	acg.out.out.writer.WriteBytes([]byte{0xb7, 0x02, 0x16, 0x8b}) // add x23, x21, x22

	// Calculate allocation size: x0 = 8 + x23 * 16 (strings use key-value pairs)
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0xf2, 0x7d, 0xd3}) // lsl x0, x23, #3 (multiply by 8)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0xf8, 0x7f, 0xd3}) // lsl x0, x0, #1 (multiply by 2, total *16)
	acg.out.AddImm64("x0", "x0", 8)                               // add x0, x0, #8

	// Align to 16 bytes
	acg.out.AddImm64("x0", "x0", 15)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0xec, 0x7c, 0x92}) // and x0, x0, #0xfffffffffffffff0

	// Call malloc(x0)
	if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0xe9, 0x03, 0x00, 0xaa}) // mov x9, x0

	// Write total count to result
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x02, 0x62, 0x9e}) // scvtf d0, x23
	if err := acg.out.StrImm64Double("d0", "x9", 0); err != nil {
		return err
	}

	// Copy left string entries (key-value pairs)
	// x10 = counter, x11 = src, x12 = dst
	acg.out.out.writer.WriteBytes([]byte{0xea, 0x03, 0x15, 0xaa}) // mov x10, x21
	acg.out.out.writer.WriteBytes([]byte{0x6b, 0x22, 0x00, 0x91}) // add x11, x19, #8
	acg.out.out.writer.WriteBytes([]byte{0x2c, 0x21, 0x00, 0x91}) // add x12, x9, #8

	acg.eb.MarkLabel("_string_concat_copy_left_loop")
	strLeftLoopStart := acg.eb.text.Len()

	strLeftSkipJumpPos := acg.eb.text.Len()
	acg.out.out.writer.WriteBytes([]byte{0x0a, 0x00, 0x00, 0xb4}) // cbz x10, +0 (placeholder)

	// Copy key and value (16 bytes total)
	if err := acg.out.LdrImm64Double("d0", "x11", 0); err != nil {
		return err
	}
	if err := acg.out.StrImm64Double("d0", "x12", 0); err != nil {
		return err
	}
	if err := acg.out.LdrImm64Double("d0", "x11", 8); err != nil {
		return err
	}
	if err := acg.out.StrImm64Double("d0", "x12", 8); err != nil {
		return err
	}
	acg.out.AddImm64("x11", "x11", 16)
	acg.out.AddImm64("x12", "x12", 16)
	acg.out.SubImm64("x10", "x10", 1)

	// Branch back
	strLeftLoopEnd := acg.eb.text.Len()
	acg.out.Branch(int32(strLeftLoopStart - strLeftLoopEnd))

	// Patch cbz
	strLeftSkipEndPos := acg.eb.text.Len()
	acg.patchJumpOffset(strLeftSkipJumpPos, int32(strLeftSkipEndPos-strLeftSkipJumpPos))

	// Copy right string entries with offset keys
	// x10 = counter (x22), x11 = src (x20 + 8), x12 already positioned, x21 = offset
	acg.out.out.writer.WriteBytes([]byte{0xea, 0x03, 0x16, 0xaa}) // mov x10, x22
	acg.out.out.writer.WriteBytes([]byte{0x8b, 0x22, 0x00, 0x91}) // add x11, x20, #8

	acg.eb.MarkLabel("_string_concat_copy_right_loop")
	strRightLoopStart := acg.eb.text.Len()

	strRightSkipJumpPos := acg.eb.text.Len()
	acg.out.out.writer.WriteBytes([]byte{0x0a, 0x00, 0x00, 0xb4}) // cbz x10, +0 (placeholder)

	// Load key, add offset, store
	if err := acg.out.LdrImm64Double("d0", "x11", 0); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0xa1, 0x02, 0x62, 0x9e}) // scvtf d1, x21 (convert offset to float)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x28, 0x61, 0x1e}) // fadd d0, d0, d1
	if err := acg.out.StrImm64Double("d0", "x12", 0); err != nil {
		return err
	}

	// Copy value
	if err := acg.out.LdrImm64Double("d0", "x11", 8); err != nil {
		return err
	}
	if err := acg.out.StrImm64Double("d0", "x12", 8); err != nil {
		return err
	}

	acg.out.AddImm64("x11", "x11", 16)
	acg.out.AddImm64("x12", "x12", 16)
	acg.out.SubImm64("x10", "x10", 1)

	// Branch back
	strRightLoopEnd := acg.eb.text.Len()
	acg.out.Branch(int32(strRightLoopStart - strRightLoopEnd))

	// Patch cbz
	strRightSkipEndPos := acg.eb.text.Len()
	acg.patchJumpOffset(strRightSkipJumpPos, int32(strRightSkipEndPos-strRightSkipJumpPos))

	// Return result
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x09, 0xaa}) // mov x0, x9

	// Epilogue. Restore only x23 (not the paired x0) so the result survives.
	if err := acg.out.LdrImm64("x23", "sp", 48); err != nil { // ldr x23, [sp, #48]
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0xf5, 0x5b, 0x42, 0xa9}) // ldp x21, x22, [sp, #32]
	acg.out.out.writer.WriteBytes([]byte{0xf3, 0x53, 0x41, 0xa9}) // ldp x19, x20, [sp, #16]
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xc4, 0xa8}) // ldp x29, x30, [sp], #64
	acg.out.Return("x30")

	// Note: Arena runtime generation disabled for ARM64 (using malloc directly)
	// The arena system is simplified - alloc() calls malloc, no arena management needed

	// Define a global buffer for itoa (128 bytes for safety, writable)
	acg.eb.DefineWritable("_itoa_buffer", string(make([]byte, 128)))

	// Generate _tim_itoa(int64) -> (buffer_ptr, length)
	// Converts integer in x0 to decimal string
	// Returns: x1 = buffer pointer (global), x2 = length
	// Uses global _itoa_buffer, builds string backwards
	acg.eb.MarkLabel("_tim_itoa")

	// Prologue: save link register (no stack allocation needed)
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xbe, 0xa9}) // stp x29, x30, [sp, #-32]!
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x03, 0x00, 0x91}) // mov x29, sp

	// x3 = is_negative flag (0 = positive, 1 = negative)
	// x4 = buffer pointer (starts at _itoa_buffer + 31, builds backwards)
	// x5 = digit counter

	// Load buffer address: ADRP + ADD for _itoa_buffer
	offset := uint64(acg.eb.text.Len())
	acg.eb.pcRelocations = append(acg.eb.pcRelocations, PCRelocation{
		offset:     offset,
		symbolName: "_itoa_buffer",
	})
	acg.out.out.writer.WriteBytes([]byte{0x04, 0x00, 0x00, 0x90}) // ADRP x4, #0
	acg.out.out.writer.WriteBytes([]byte{0x84, 0x00, 0x00, 0x91}) // ADD x4, x4, #0

	// Zero out the buffer (128 bytes) to ensure clean state
	// x9 = 0 (value to store)
	acg.out.out.writer.WriteBytes([]byte{0x09, 0x00, 0x80, 0xd2}) // mov x9, #0
	// Store 16 x 8-byte zeros (total 128 bytes)
	for i := range 16 {
		// str x9, [x4, #(i*8)]
		offset := uint32(i * 8)
		strInstr := uint32(0xf9000089) | ((offset / 8) << 10)
		acg.out.out.writer.WriteBytes([]byte{
			byte(strInstr),
			byte(strInstr >> 8),
			byte(strInstr >> 16),
			byte(strInstr >> 24),
		})
	}

	// Initialize: x3 = 0, x4 = buffer + 100, x5 = 0
	// Start at position 100 to leave room for both long numbers backwards and newline forwards
	acg.out.out.writer.WriteBytes([]byte{0x03, 0x00, 0x80, 0xd2}) // mov x3, #0

	// add x4, x4, #100 (point to middle-end of buffer for backwards building)
	// Use proper AddImm64 instead of manual bytes
	if err := acg.out.AddImm64("x4", "x4", 100); err != nil {
		return err
	}

	acg.out.out.writer.WriteBytes([]byte{0x05, 0x00, 0x80, 0xd2}) // mov x5, #0

	// Handle negative: if x0 < 0, negate and set flag
	// cmp x0, #0
	acg.out.out.writer.WriteBytes([]byte{0x1f, 0x00, 0x00, 0xf1})
	// b.ge positive
	posJumpItoa := acg.eb.text.Len()
	acg.out.BranchCond("ge", 0) // Placeholder
	// neg x0, x0 (encoded as sub x0, xzr, x0)
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x00, 0xcb})
	// mov x3, #1
	acg.out.out.writer.WriteBytes([]byte{0x23, 0x00, 0x80, 0xd2})

	// positive:
	posItoaPos := acg.eb.text.Len()
	acg.patchJumpOffset(posJumpItoa, int32(posItoaPos-posJumpItoa))

	// Special case: if x0 == 0, emit single '0'
	// cbz x0, zero_case
	zeroJumpItoa := acg.eb.text.Len()
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0xb4}) // Placeholder

	// Conversion loop: extract digits backwards using proper instruction methods
	// loop_start:
	loopStartItoa := acg.eb.text.Len()

	// x10 = 10 (divisor)
	if err := acg.out.MovImm64("x10", 10); err != nil {
		return err
	}

	// x11 = x0 / 10 (unsigned division)
	if err := acg.out.UDiv64("x11", "x0", "x10"); err != nil {
		return err
	}

	// x12 = x0 % 10 (compute as: x12 = x0 - (x11 * 10))
	// First: x13 = x11 * 10
	if err := acg.out.Mul64("x13", "x11", "x10"); err != nil {
		return err
	}
	// Then: x12 = x0 - x13
	if err := acg.out.SubReg64("x12", "x0", "x13"); err != nil {
		return err
	}

	// Convert digit to ASCII: x12 = x12 + 48 ('0')
	if err := acg.out.AddImm64("x12", "x12", 48); err != nil {
		return err
	}

	// Store byte at [x4, #0]
	if err := acg.out.StrbImm("x12", "x4", 0); err != nil {
		return err
	}

	// Decrement buffer pointer: x4 = x4 - 1
	if err := acg.out.SubImm64("x4", "x4", 1); err != nil {
		return err
	}

	// Increment digit count: x5 = x5 + 1
	if err := acg.out.AddImm64("x5", "x5", 1); err != nil {
		return err
	}

	// x0 = x11 (quotient becomes new number for next iteration)
	if err := acg.out.MovReg64("x0", "x11"); err != nil {
		return err
	}

	// if x0 != 0, continue loop (branch back to loop_start)
	// cmp x0, #0
	acg.out.out.writer.WriteBytes([]byte{0x1f, 0x00, 0x00, 0xf1})
	// b.ne loop_start
	loopOffsetItoa := int32(loopStartItoa - acg.eb.text.Len())
	if err := acg.out.BranchCond("ne", loopOffsetItoa); err != nil {
		return err
	}

	// After loop, x4 points to char before first digit, x5 = digit count
	// Add minus sign if negative
	// cbz x3, skip_minus_itoa
	skipMinusItoaJump := acg.eb.text.Len()
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0xb4}) // Placeholder
	// mov x8, #45 ('-')
	acg.out.out.writer.WriteBytes([]byte{0xa8, 0x05, 0x80, 0xd2})
	// strb w8, [x4], #-1
	acg.out.out.writer.WriteBytes([]byte{0x88, 0xf4, 0x1f, 0x38})
	// add x5, x5, #1
	acg.out.out.writer.WriteBytes([]byte{0xa5, 0x04, 0x00, 0x91})

	// skip_minus_itoa:
	skipMinusItoaPos := acg.eb.text.Len()
	skipMinusItoaOffset := uint32((skipMinusItoaPos - skipMinusItoaJump) >> 2)
	cbzInstrItoa := uint32(0xb4000003) | ((skipMinusItoaOffset & 0x7ffff) << 5)
	acg.eb.text.Bytes()[skipMinusItoaJump] = byte(cbzInstrItoa)
	acg.eb.text.Bytes()[skipMinusItoaJump+1] = byte(cbzInstrItoa >> 8)
	acg.eb.text.Bytes()[skipMinusItoaJump+2] = byte(cbzInstrItoa >> 16)
	acg.eb.text.Bytes()[skipMinusItoaJump+3] = byte(cbzInstrItoa >> 24)

	// x4 now points to char before first char, increment to get buffer start
	// add x1, x4, #1
	if err := acg.out.AddImm64("x1", "x4", 1); err != nil {
		return err
	}

	// x2 = length
	acg.out.out.writer.WriteBytes([]byte{0xe2, 0x03, 0x05, 0xaa}) // mov x2, x5

	// Jump to epilogue
	endItoaJump := acg.eb.text.Len()
	acg.out.Branch(0) // Placeholder

	// zero_case: emit single '0'
	zeroItoaPos := acg.eb.text.Len()
	zeroItoaOffset := uint32((zeroItoaPos - zeroJumpItoa) >> 2)
	cbzZeroInstr := uint32(0xb4000000) | ((zeroItoaOffset & 0x7ffff) << 5)
	acg.eb.text.Bytes()[zeroJumpItoa] = byte(cbzZeroInstr)
	acg.eb.text.Bytes()[zeroJumpItoa+1] = byte(cbzZeroInstr >> 8)
	acg.eb.text.Bytes()[zeroJumpItoa+2] = byte(cbzZeroInstr >> 16)
	acg.eb.text.Bytes()[zeroJumpItoa+3] = byte(cbzZeroInstr >> 24)

	// mov x8, #48 ('0')
	acg.out.out.writer.WriteBytes([]byte{0x08, 0x06, 0x80, 0xd2})
	// strb w8, [x4]
	acg.out.out.writer.WriteBytes([]byte{0x88, 0x00, 0x00, 0x39})
	// x1 = x4 (buffer start)
	acg.out.out.writer.WriteBytes([]byte{0xe1, 0x03, 0x04, 0xaa}) // mov x1, x4
	// mov x2, #1 (length)
	acg.out.out.writer.WriteBytes([]byte{0x22, 0x00, 0x80, 0xd2})

	// Epilogue: restore and return
	endItoaPos := acg.eb.text.Len()
	acg.patchJumpOffset(endItoaJump, int32(endItoaPos-endItoaJump))

	// Restore stack and return (buffer is global, so it's safe to deallocate)
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xc2, 0xa8}) // ldp x29, x30, [sp], #32
	acg.out.Return("x30")

	// Generate _tim_str(float64) -> pointer (to map string)
	// Converts float64 in d0 to a Tim string (map of indices to characters)
	acg.eb.MarkLabel("_tim_str")

	// Prologue
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xba, 0xa9}) // stp x29, x30, [sp, #-96]!
	acg.out.out.writer.WriteBytes([]byte{0xf3, 0x53, 0x01, 0xa9}) // stp x19, x20, [sp, #16]
	acg.out.out.writer.WriteBytes([]byte{0xf5, 0x5b, 0x02, 0xa9}) // stp x21, x22, [sp, #32]
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x03, 0x00, 0x91}) // mov x29, sp

	// Convert d0 to int64 in x0 for itoa
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e}) // fcvtzs x0, d0

	// Call itoa: x1=buf, x2=len
	if err := acg.eb.GenerateCallInstruction("_tim_itoa"); err != nil {
		return err
	}

	// Save itoa results
	acg.out.out.writer.WriteBytes([]byte{0xf3, 0x03, 0x01, 0xaa}) // mov x19, x1 (buf)
	acg.out.out.writer.WriteBytes([]byte{0xf4, 0x03, 0x02, 0xaa}) // mov x20, x2 (len)

	// Calculate allocation size for map: 8 + len * 16
	acg.out.out.writer.WriteBytes([]byte{0x80, 0xf2, 0x7d, 0xd3}) // lsl x0, x20, #3 (len * 8)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0xf8, 0x7f, 0xd3}) // lsl x0, x0, #1 (len * 16)
	acg.out.AddImm64("x0", "x0", 8)
	acg.out.AddImm64("x0", "x0", 15)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0xec, 0x7c, 0x92}) // and x0, x0, #0xfffffffffffffff0

	// Call malloc
	if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
		return err
	}
	// x0 = map pointer
	acg.out.out.writer.WriteBytes([]byte{0xea, 0x03, 0x00, 0xaa}) // mov x10, x0 (map_ptr)

	// Store length as float64 at [x10]
	acg.out.out.writer.WriteBytes([]byte{0x80, 0x02, 0x62, 0x9e}) // scvtf d0, x20
	if err := acg.out.StrImm64Double("d0", "x10", 0); err != nil {
		return err
	}

	// Loop to fill map: key = index, val = char
	acg.out.out.writer.WriteBytes([]byte{0xf5, 0x03, 0x1f, 0xaa}) // mov x21, xzr (index)
	acg.out.AddImm64("x11", "x10", 8)                             // x11 = dst pointer

	acg.eb.MarkLabel("_tim_str_loop")
	strLoopStart := acg.eb.text.Len()

	// cmp x21, x20; b.ge end
	acg.out.out.writer.WriteBytes([]byte{0xbf, 0x02, 0x14, 0xeb}) // subs xzr, x21, x20
	endJumpPos := acg.eb.text.Len()
	acg.out.BranchCond("ge", 0)

	// key = float64(index)
	acg.out.out.writer.WriteBytes([]byte{0xa0, 0x02, 0x62, 0x9e}) // scvtf d0, x21
	if err := acg.out.StrImm64Double("d0", "x11", 0); err != nil {
		return err
	}

	// val = float64(buf[index])
	acg.out.out.writer.WriteBytes([]byte{0x68, 0x6a, 0x75, 0x38}) // ldrb w8, [x19, x21]
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x01, 0x62, 0x9e}) // scvtf d0, x8
	if err := acg.out.StrImm64Double("d0", "x11", 8); err != nil {
		return err
	}

	acg.out.AddImm64("x11", "x11", 16)
	acg.out.AddImm64("x21", "x21", 1)
	acg.out.Branch(int32(strLoopStart - acg.eb.text.Len()))

	endPos := acg.eb.text.Len()
	acg.patchJumpOffset(endJumpPos, int32(endPos-endJumpPos))

	// Result in x0
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x0a, 0xaa}) // mov x0, x10

	// Epilogue
	acg.out.out.writer.WriteBytes([]byte{0xf5, 0x5b, 0x42, 0xa9}) // ldp x21, x22, [sp, #32]
	acg.out.out.writer.WriteBytes([]byte{0xf3, 0x53, 0x41, 0xa9}) // ldp x19, x20, [sp, #16]
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xc6, 0xa8}) // ldp x29, x30, [sp], #96
	acg.out.Return("x30")

	return nil
}

// compileRegisterAssignment compiles register assignment statements for ARM64 unsafe blocks
func (acg *ARM64CodeGen) compileRegisterAssignment(stmt *RegisterAssignStmt) error {
	// Resolve register aliases (a->x0, b->x1, etc.)
	register := resolveRegisterAlias(stmt.Register, ArchARM64)

	// Handle different value types
	switch v := stmt.Value.(type) {
	case *NumberExpr:
		// Immediate value: register <- 42
		val := int64(v.Value)
		if err := acg.out.MovImm64(register, uint64(val)); err != nil {
			return err
		}

	case string:
		// Register-to-register move: x0 <- x1
		sourceReg := resolveRegisterAlias(v, ArchARM64)
		// mov dest, source
		acg.out.out.writer.WriteBytes([]byte{
			byte((uint32(getRegisterNumber(sourceReg)) << 16) | uint32(getRegisterNumber(register))),
			0x03,
			byte(getRegisterNumber(sourceReg)),
			0xaa,
		}) // mov register, sourceReg

	case *RegisterOp:
		// Arithmetic or bitwise operation
		return acg.compileRegisterOp(register, v)

	case *MemoryLoad:
		// Memory load: x0 <- [x1] or x0 <- u8 [x1 + 16]
		return acg.compileMemoryLoad(register, v)

	default:
		return fmt.Errorf("unsupported value type in ARM64 register assignment: %T", v)
	}

	return nil
}

// compileRegisterOp compiles register arithmetic/bitwise operations for ARM64
func (acg *ARM64CodeGen) compileRegisterOp(dest string, op *RegisterOp) error {
	// Unary operations
	if op.Left == "" {
		switch op.Operator {
		case "~b":
			// Bitwise NOT: dest <- ~right
			sourceReg := resolveRegisterAlias(op.Right.(string), ArchARM64)
			destNum := getRegisterNumber(dest)
			srcNum := getRegisterNumber(sourceReg)
			// mvn dest, source (move NOT)
			acg.out.out.writer.WriteBytes([]byte{
				byte(destNum),
				byte(srcNum<<5 | 0x03),
				byte(srcNum>>3 | 0x20),
				0xaa, // mvn Xd, Xm
			})
			return nil
		default:
			return fmt.Errorf("unsupported unary operator in ARM64 register operation: %s", op.Operator)
		}
	}

	// Binary operations: dest <- left OP right
	leftReg := resolveRegisterAlias(op.Left, ArchARM64)

	switch op.Operator {
	case "+":
		// add dest, left, right
		switch r := op.Right.(type) {
		case string:
			rightReg := resolveRegisterAlias(r, ArchARM64)
			// add dest, left, right
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			rightNum := getRegisterNumber(rightReg)
			acg.out.out.writer.WriteBytes([]byte{
				byte((uint32(rightNum) << 16) | uint32(destNum)),
				byte(uint32(leftNum)<<2 | uint32(rightNum)>>14),
				byte(rightNum >> 6),
				0x8b, // add Xd, Xn, Xm
			})
		case *NumberExpr:
			// add dest, left, #imm
			return acg.out.AddImm64(dest, leftReg, uint32(r.Value))
		}

	case "-":
		// sub dest, left, right
		switch r := op.Right.(type) {
		case string:
			rightReg := resolveRegisterAlias(r, ArchARM64)
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			rightNum := getRegisterNumber(rightReg)
			acg.out.out.writer.WriteBytes([]byte{
				byte((uint32(rightNum) << 16) | uint32(destNum)),
				byte(uint32(leftNum)<<2 | uint32(rightNum)>>14),
				byte(rightNum >> 6),
				0xcb, // sub Xd, Xn, Xm
			})
		case *NumberExpr:
			return acg.out.SubImm64(dest, leftReg, uint32(r.Value))
		}

	case "&":
		// and dest, left, right
		switch r := op.Right.(type) {
		case string:
			rightReg := resolveRegisterAlias(r, ArchARM64)
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			rightNum := getRegisterNumber(rightReg)
			acg.out.out.writer.WriteBytes([]byte{
				byte((uint32(rightNum) << 16) | uint32(destNum)),
				byte(uint32(leftNum)<<2 | uint32(rightNum)>>14),
				byte(rightNum >> 6),
				0x8a, // and Xd, Xn, Xm
			})
		}

	case "|":
		// orr dest, left, right
		switch r := op.Right.(type) {
		case string:
			rightReg := resolveRegisterAlias(r, ArchARM64)
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			rightNum := getRegisterNumber(rightReg)
			acg.out.out.writer.WriteBytes([]byte{
				byte((uint32(rightNum) << 16) | uint32(destNum)),
				byte(uint32(leftNum)<<2 | uint32(rightNum)>>14),
				byte(rightNum >> 6),
				0xaa, // orr Xd, Xn, Xm
			})
		}

	case "^b":
		// eor dest, left, right
		switch r := op.Right.(type) {
		case string:
			rightReg := resolveRegisterAlias(r, ArchARM64)
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			rightNum := getRegisterNumber(rightReg)
			acg.out.out.writer.WriteBytes([]byte{
				byte((uint32(rightNum) << 16) | uint32(destNum)),
				byte(uint32(leftNum)<<2 | uint32(rightNum)>>14),
				byte(rightNum >> 6),
				0xca, // eor Xd, Xn, Xm
			})
		}

	case "*":
		// mul dest, left, right
		switch r := op.Right.(type) {
		case string:
			rightReg := resolveRegisterAlias(r, ArchARM64)
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			rightNum := getRegisterNumber(rightReg)
			acg.out.out.writer.WriteBytes([]byte{
				byte((uint32(rightNum) << 16) | uint32(destNum)),
				byte(0x7c | uint32(leftNum)<<2 | uint32(rightNum)>>14),
				byte(rightNum >> 6),
				0x9b, // mul Xd, Xn, Xm
			})
		}

	case "/":
		// sdiv dest, left, right (signed division)
		switch r := op.Right.(type) {
		case string:
			rightReg := resolveRegisterAlias(r, ArchARM64)
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			rightNum := getRegisterNumber(rightReg)
			acg.out.out.writer.WriteBytes([]byte{
				byte((uint32(rightNum) << 16) | uint32(destNum)),
				byte(0x0c | uint32(leftNum)<<2 | uint32(rightNum)>>14),
				byte(rightNum >> 6),
				0x9a, // sdiv Xd, Xn, Xm
			})
		}

	case "%":
		// ARM64 doesn't have a modulo instruction, need to use: a % b = a - (a/b)*b
		// This requires multiple steps
		switch r := op.Right.(type) {
		case string:
			rightReg := resolveRegisterAlias(r, ArchARM64)
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			rightNum := getRegisterNumber(rightReg)

			// First move left to dest if needed
			if dest != leftReg {
				// mov dest, left
				acg.out.out.writer.WriteBytes([]byte{
					byte(leftNum),
					0x03,
					byte(leftNum >> 3),
					0xaa,
				})
			}

			// sdiv x9, left, right (x9 = left / right)
			acg.out.out.writer.WriteBytes([]byte{
				byte((uint32(rightNum) << 16) | 9),
				byte(0x0c | uint32(leftNum)<<2 | uint32(rightNum)>>14),
				byte(rightNum >> 6),
				0x9a,
			})

			// msub dest, x9, right, left (dest = left - x9*right)
			// This is the ARM64 "multiply-subtract" instruction
			acg.out.out.writer.WriteBytes([]byte{
				byte((uint32(leftNum) << 16) | uint32(destNum)),
				byte(0x80 | uint32(rightNum)<<2 | uint32(leftNum)>>14),
				byte(9 | rightNum<<3),
				0x9b, // msub Xd, Xn, Xm, Xa
			})
		}

	case "<<", "<<b":
		// lsl dest, left, right (logical shift left)
		switch r := op.Right.(type) {
		case string:
			rightReg := resolveRegisterAlias(r, ArchARM64)
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			rightNum := getRegisterNumber(rightReg)
			acg.out.out.writer.WriteBytes([]byte{
				byte((uint32(rightNum) << 16) | uint32(destNum)),
				byte(0x20 | uint32(leftNum)<<2 | uint32(rightNum)>>14),
				byte(rightNum >> 6),
				0x9a, // lsl Xd, Xn, Xm
			})
		case *NumberExpr:
			// lsl with immediate
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			shift := uint32(r.Value) & 63 // Limit to 6 bits
			// LSL (immediate) encoding: ubfm Xd, Xn, #(-shift MOD 64), #(63-shift)
			immr := (64 - shift) & 63
			imms := 63 - shift
			acg.out.out.writer.WriteBytes([]byte{
				byte(destNum),
				byte(imms<<2 | uint32(leftNum)>>3),
				byte(0x40 | immr<<2 | uint32(leftNum)<<5),
				0xd3, // ubfm (acts as lsl)
			})
		}

	case ">>", ">>b":
		// lsr dest, left, right (logical shift right)
		switch r := op.Right.(type) {
		case string:
			rightReg := resolveRegisterAlias(r, ArchARM64)
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			rightNum := getRegisterNumber(rightReg)
			acg.out.out.writer.WriteBytes([]byte{
				byte((uint32(rightNum) << 16) | uint32(destNum)),
				byte(0x24 | uint32(leftNum)<<2 | uint32(rightNum)>>14),
				byte(rightNum >> 6),
				0x9a, // lsr Xd, Xn, Xm
			})
		case *NumberExpr:
			// lsr with immediate
			destNum := getRegisterNumber(dest)
			leftNum := getRegisterNumber(leftReg)
			shift := uint32(r.Value) & 63
			// LSR (immediate) encoding: ubfm Xd, Xn, #shift, #63
			acg.out.out.writer.WriteBytes([]byte{
				byte(destNum),
				byte(0xfc | uint32(leftNum)>>3),
				byte(0x40 | shift<<2 | uint32(leftNum)<<5),
				0xd3, // ubfm (acts as lsr)
			})
		}

	default:
		return fmt.Errorf("unsupported operator in ARM64 register operation: %s", op.Operator)
	}

	return nil
}

// compileMemoryLoad compiles memory load operations for ARM64
func (acg *ARM64CodeGen) compileMemoryLoad(dest string, load *MemoryLoad) error {
	addrReg := resolveRegisterAlias(load.Address, ArchARM64)
	offset := load.Offset

	// Simplified version: load 64-bit value
	// ldr dest, [addrReg, #offset]
	if offset == 0 {
		destNum := getRegisterNumber(dest)
		addrNum := getRegisterNumber(addrReg)
		acg.out.out.writer.WriteBytes([]byte{
			byte(destNum),
			byte(addrNum << 5),
			0x40,
			0xf9, // ldr Xd, [Xn]
		})
	} else {
		// ldr with offset
		return acg.out.LdrImm64(dest, addrReg, int32(offset))
	}

	return nil
}

// compileMemoryStore compiles memory store operations for ARM64
func (acg *ARM64CodeGen) compileMemoryStore(store *MemoryStore) error {
	addrReg := resolveRegisterAlias(store.Address, ArchARM64)
	offset := store.Offset

	// Determine what value to store
	var sourceReg string
	switch v := store.Value.(type) {
	case string:
		// Register name
		sourceReg = resolveRegisterAlias(v, ArchARM64)
	case *NumberExpr:
		// Immediate value - load into x9 first
		val := int64(v.Value)
		if err := acg.out.MovImm64("x9", uint64(val)); err != nil {
			return err
		}
		sourceReg = "x9"
	default:
		return fmt.Errorf("unsupported value type in memory store: %T", v)
	}

	// Determine store size
	addrNum := getRegisterNumber(addrReg)
	srcNum := getRegisterNumber(sourceReg)

	switch store.Size {
	case "", "uint64", "u64":
		// 64-bit store: str xN, [addr, #offset]
		if offset == 0 {
			// str sourceReg, [addrReg]
			acg.out.out.writer.WriteBytes([]byte{
				byte(srcNum),
				byte(addrNum << 5),
				0x00,
				0xf9, // str Xn, [Xm]
			})
		} else {
			// str with offset
			immField := (uint32(offset) / 8) << 10
			strInstr := uint32(0xf9000000) | uint32(srcNum) | (uint32(addrNum) << 5) | immField
			acg.out.out.writer.WriteBytes([]byte{
				byte(strInstr),
				byte(strInstr >> 8),
				byte(strInstr >> 16),
				byte(strInstr >> 24),
			})
		}

	case "uint32", "u32":
		// 32-bit store: str wN, [addr, #offset]
		if offset == 0 {
			acg.out.out.writer.WriteBytes([]byte{
				byte(srcNum),
				byte(addrNum << 5),
				0x00,
				0xb9, // str Wn, [Xm]
			})
		} else {
			immField := (uint32(offset) / 4) << 10
			strInstr := uint32(0xb9000000) | uint32(srcNum) | (uint32(addrNum) << 5) | immField
			acg.out.out.writer.WriteBytes([]byte{
				byte(strInstr),
				byte(strInstr >> 8),
				byte(strInstr >> 16),
				byte(strInstr >> 24),
			})
		}

	case "uint16", "u16":
		// 16-bit store: strh wN, [addr, #offset]
		if offset == 0 {
			acg.out.out.writer.WriteBytes([]byte{
				byte(srcNum),
				byte(addrNum << 5),
				0x00,
				0x79, // strh Wn, [Xm]
			})
		} else {
			immField := (uint32(offset) / 2) << 10
			strInstr := uint32(0x79000000) | uint32(srcNum) | (uint32(addrNum) << 5) | immField
			acg.out.out.writer.WriteBytes([]byte{
				byte(strInstr),
				byte(strInstr >> 8),
				byte(strInstr >> 16),
				byte(strInstr >> 24),
			})
		}

	case "uint8", "u8":
		// 8-bit store: strb wN, [addr, #offset]
		if offset == 0 {
			acg.out.out.writer.WriteBytes([]byte{
				byte(srcNum),
				byte(addrNum << 5),
				0x00,
				0x39, // strb Wn, [Xm]
			})
		} else {
			immField := uint32(offset) << 10
			strInstr := uint32(0x39000000) | uint32(srcNum) | (uint32(addrNum) << 5) | immField
			acg.out.out.writer.WriteBytes([]byte{
				byte(strInstr),
				byte(strInstr >> 8),
				byte(strInstr >> 16),
				byte(strInstr >> 24),
			})
		}

	default:
		return fmt.Errorf("unsupported memory store size: %s", store.Size)
	}

	return nil
}

// getRegisterNumber returns the numeric encoding for ARM64 registers
func getRegisterNumber(reg string) uint8 {
	// Handle x0-x30, sp, xzr
	switch reg {
	case "x0":
		return 0
	case "x1":
		return 1
	case "x2":
		return 2
	case "x3":
		return 3
	case "x4":
		return 4
	case "x5":
		return 5
	case "x6":
		return 6
	case "x7":
		return 7
	case "x8":
		return 8
	case "x9":
		return 9
	case "x10":
		return 10
	case "x11":
		return 11
	case "x12":
		return 12
	case "x13":
		return 13
	case "x14":
		return 14
	case "x15":
		return 15
	case "x16":
		return 16
	case "x17":
		return 17
	case "x18":
		return 18
	case "x19":
		return 19
	case "x20":
		return 20
	case "x21":
		return 21
	case "x22":
		return 22
	case "x23":
		return 23
	case "x24":
		return 24
	case "x25":
		return 25
	case "x26":
		return 26
	case "x27":
		return 27
	case "x28":
		return 28
	case "x29", "fp":
		return 29
	case "x30", "lr":
		return 30
	case "sp":
		return 31
	case "xzr":
		return 31
	default:
		return 0
	}
}

// compilePostfixStmt compiles postfix increment/decrement statements (x++, x--)
func (acg *ARM64CodeGen) compilePostfixStmt(postfix *PostfixExpr) error {
	// x++ and x-- are statements only, not expressions
	identExpr, ok := postfix.Operand.(*IdentExpr)
	if !ok {
		return fmt.Errorf("postfix operator %s requires a variable operand", postfix.Operator)
	}

	// Get the variable's stack offset
	offset, exists := acg.stackVars[identExpr.Name]
	if !exists {
		return fmt.Errorf("undefined variable '%s'", identExpr.Name)
	}

	// Check if variable is mutable
	if !acg.mutableVars[identExpr.Name] {
		return fmt.Errorf("cannot modify immutable variable '%s'", identExpr.Name)
	}

	// Load current value into d0: ldr d0, [x29, #offset]
	stackOffset := int32(16 + offset - 8)
	if err := acg.out.LdrImm64Double("d0", "x29", stackOffset); err != nil {
		return err
	}

	// Create 1.0 constant and load it into d1
	// Load 1 as integer, then convert to float
	if err := acg.out.MovImm64("x0", 1); err != nil {
		return err
	}
	// scvtf d1, x0 (convert int64 to float64)
	acg.out.out.writer.WriteBytes([]byte{0x01, 0x00, 0x62, 0x9e})

	// Apply the operation
	switch postfix.Operator {
	case "++":
		// fadd d0, d0, d1 (d0 = d0 + 1.0)
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x28, 0x61, 0x1e})
	case "--":
		// fsub d0, d0, d1 (d0 = d0 - 1.0)
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x38, 0x61, 0x1e})
	default:
		return fmt.Errorf("unknown postfix operator '%s'", postfix.Operator)
	}

	// Store result back: str d0, [x29, #offset]
	if err := acg.out.StrImm64Double("d0", "x29", stackOffset); err != nil {
		return err
	}

	return nil
}

// compileFFICall compiles FFI call() function for ARM64+macOS
func (acg *ARM64CodeGen) compileFFICall(call *CallExpr) error {
	// FFI: call(function_name, args...)
	// First argument must be a string literal (function name)
	if len(call.Args) < 1 {
		return fmt.Errorf("call() requires at least a function name")
	}

	fnNameExpr, ok := call.Args[0].(*StringExpr)
	if !ok {
		return fmt.Errorf("call() first argument must be a string literal (function name)")
	}
	fnName := fnNameExpr.Value

	// ARM64 calling convention (macOS):
	// Integer/pointer args: x0-x7
	// Float args: d0-d7
	intRegs := []string{"x0", "x1", "x2", "x3", "x4", "x5", "x6", "x7"}
	floatRegs := []string{"d0", "d1", "d2", "d3", "d4", "d5", "d6", "d7"}

	intArgCount := 0
	floatArgCount := 0
	numArgs := len(call.Args) - 1 // Exclude function name

	if numArgs > 8 {
		return fmt.Errorf("call() supports max 8 arguments (got %d)", numArgs)
	}

	// Determine argument types by checking for cast expressions
	argTypes := make([]string, numArgs)
	for i := range numArgs {
		arg := call.Args[i+1]
		if castExpr, ok := arg.(*CastExpr); ok {
			argTypes[i] = castExpr.Type
		} else {
			// No cast - assume float64
			argTypes[i] = "f64"
		}
	}

	// Evaluate all arguments and save to stack
	stackSize := numArgs * 8
	if stackSize > 0 {
		// Allocate stack space
		if err := acg.out.SubImm64("sp", "sp", uint32(stackSize)); err != nil {
			return err
		}

		for i := range numArgs {
			if err := acg.compileExpression(call.Args[i+1]); err != nil {
				return err
			}
			// Store d0 at [sp, #(i*8)]
			offset := int32(i * 8)
			if err := acg.out.StrImm64Double("d0", "sp", offset); err != nil {
				return err
			}
		}
	}

	// Load arguments into registers (in forward order from stack)
	for i := range numArgs {
		argType := argTypes[i]

		// Determine if this is an integer/pointer argument or float argument
		isIntArg := false
		switch argType {
		case "int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64", "ptr", "cstr":
			isIntArg = true
		case "float32", "float64", "f64":
			isIntArg = false
		default:
			// Unknown type - assume float
			isIntArg = false
		}

		// Load from stack into d0
		offset := int32(i * 8)
		if err := acg.out.LdrImm64Double("d0", "sp", offset); err != nil {
			return err
		}

		if isIntArg {
			// Integer/pointer argument
			if intArgCount < len(intRegs) {
				if argType == "cstr" || argType == "ptr" {
					// cstr/ptr is already a pointer - transfer bits from d0 to integer register
					// fmov xN, d0 (transfer bits)
					regNum := getRegisterNumber(intRegs[intArgCount])
					acg.out.out.writer.WriteBytes([]byte{
						byte(regNum),
						0x00,
						0x67,
						0x9e, // fmov xN, d0
					})
				} else {
					// Convert float64 to integer: fcvtzs xN, d0
					regNum := getRegisterNumber(intRegs[intArgCount])
					acg.out.out.writer.WriteBytes([]byte{
						byte(regNum),
						0x00,
						0x78,
						0x9e, // fcvtzs xN, d0
					})
				}
				intArgCount++
			} else {
				return fmt.Errorf("call() supports max 8 integer/pointer arguments")
			}
		} else {
			// Float argument
			if floatArgCount < len(floatRegs) {
				if floatArgCount != 0 {
					// Move to appropriate float register (d0 already has value for first arg)
					// fmov dN, d0
					destRegNum := floatArgCount // d0=0, d1=1, etc.
					acg.out.out.writer.WriteBytes([]byte{
						byte(destRegNum),
						0x40,
						0x60,
						0x1e, // fmov dN, d0
					})
				}
				// else: already in d0
				floatArgCount++
			} else {
				return fmt.Errorf("call() supports max 8 float arguments")
			}
		}
	}

	// Clean up stack if we allocated space
	if stackSize > 0 {
		if err := acg.out.AddImm64("sp", "sp", uint32(stackSize)); err != nil {
			return err
		}
	}

	// Mark that we need dynamic linking
	acg.eb.useDynamicLinking = true

	// Add function to needed functions list if not already there
	found := slices.Contains(acg.eb.neededFunctions, fnName)
	if !found {
		acg.eb.neededFunctions = append(acg.eb.neededFunctions, fnName)
	}

	// Generate call to the function
	stubLabel := fnName + "$stub"
	position := acg.eb.text.Len()
	acg.eb.callPatches = append(acg.eb.callPatches, CallPatch{
		position:   position,
		targetName: stubLabel,
	})

	// Emit placeholder bl instruction (will be patched)
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x00, 0x94}) // bl #0

	// Result is in x0 (for integer/pointer returns) or d0 (for float returns)
	// Check if this is a known floating-point function
	floatFunctions := map[string]bool{
		"sqrt": true, "sin": true, "cos": true, "tan": true,
		"asin": true, "acos": true, "atan": true, "atan2": true,
		"log": true, "log10": true, "exp": true, "pow": true,
		"fabs": true, "fmod": true, "ceil": true, "floor": true,
	}

	if floatFunctions[fnName] {
		// Float return - result already in d0
		// Nothing to do
	} else {
		// Integer/pointer return - result in x0
		// Convert to float64: scvtf d0, x0
		acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})
	}

	return nil
}

// compileAlloc compiles the alloc() builtin for arena allocation
func (acg *ARM64CodeGen) compileAlloc(call *CallExpr) error {
	// alloc(size) - Arena-based memory allocation
	// Allocates from current arena (global arena by default, or nested arena inside arena { } blocks)
	// The arena system auto-grows as needed using realloc
	if len(call.Args) != 1 {
		return fmt.Errorf("alloc() requires 1 argument (size)")
	}

	// Sanity check - currentArena should never be 0 (it starts at 1 for global arena)
	if acg.currentArena == 0 {
		return fmt.Errorf("internal error: alloc() called with currentArena=0 (should start at 1)")
	}

	// Simplified ARM64 implementation: just call malloc directly
	// Compile size argument - result in d0
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}
	// Convert size from float64 to int64: fcvtzs x0, d0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x78, 0x9e})

	// Call malloc(size)
	if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
		return err
	}

	// Result in x0, convert to float64: scvtf d0, x0
	acg.out.out.writer.WriteBytes([]byte{0x00, 0x00, 0x62, 0x9e})

	return nil
}

// compileMemoryWrite compiles memory write helper functions (write_i32, write_f64, etc.)
func (acg *ARM64CodeGen) compileMemoryWrite(call *CallExpr) error {
	// write_TYPE(ptr, index, value)
	if len(call.Args) != 3 {
		return fmt.Errorf("%s() requires exactly 3 arguments (ptr, index, value)", call.Function)
	}

	// Determine type size
	var typeSize int
	switch call.Function {
	case "write_i8", "write_u8":
		typeSize = 1
	case "write_i16", "write_u16":
		typeSize = 2
	case "write_i32", "write_u32":
		typeSize = 4
	case "write_i64", "write_u64", "write_f64":
		typeSize = 8
	}

	// Compile pointer (arg 0) - result in d0
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}
	// Recover the pointer into x9. C-FFI pointers (e.g. from c.malloc) use the
	// numeric convention, so convert value->int: fcvtzs x9, d0.
	acg.out.out.writer.WriteBytes([]byte{0x09, 0x00, 0x78, 0x9e})

	// Save pointer to stack
	if err := acg.out.SubImm64("sp", "sp", 16); err != nil {
		return err
	}
	// str x9, [sp]
	acg.out.out.writer.WriteBytes([]byte{0xe9, 0x03, 0x00, 0xf9})

	// Compile index (arg 1) - result in d0
	if err := acg.compileExpression(call.Args[1]); err != nil {
		return err
	}
	// Convert index to integer: fcvtzs x10, d0
	acg.out.out.writer.WriteBytes([]byte{0x0a, 0x00, 0x78, 0x9e})

	// Multiply index by type size: x10 = x10 * typeSize
	if typeSize > 1 {
		if err := acg.out.MovImm64("x11", uint64(typeSize)); err != nil {
			return err
		}
		// mul x10, x10, x11
		acg.out.out.writer.WriteBytes([]byte{0x4a, 0x7d, 0x0b, 0x9b})
	}

	// Load pointer from stack: ldr x9, [sp]
	acg.out.out.writer.WriteBytes([]byte{0xe9, 0x03, 0x40, 0xf9})
	// Add offset to pointer: add x9, x9, x10
	acg.out.out.writer.WriteBytes([]byte{0x29, 0x01, 0x0a, 0x8b})

	// Compile value (arg 2) - result in d0
	if err := acg.compileExpression(call.Args[2]); err != nil {
		return err
	}

	// Write value to memory
	if call.Function == "write_f64" {
		// Write float64 directly: str d0, [x9]
		acg.out.out.writer.WriteBytes([]byte{0x20, 0x01, 0x00, 0xfd})
	} else {
		// Convert to integer: fcvtzs x10, d0
		acg.out.out.writer.WriteBytes([]byte{0x0a, 0x00, 0x78, 0x9e})

		// Store based on size
		switch typeSize {
		case 1:
			// strb w10, [x9]
			acg.out.out.writer.WriteBytes([]byte{0x2a, 0x01, 0x00, 0x39})
		case 2:
			// strh w10, [x9]
			acg.out.out.writer.WriteBytes([]byte{0x2a, 0x01, 0x00, 0x79})
		case 4:
			// str w10, [x9]
			acg.out.out.writer.WriteBytes([]byte{0x2a, 0x01, 0x00, 0xb9})
		case 8:
			// str x10, [x9]
			acg.out.out.writer.WriteBytes([]byte{0x2a, 0x01, 0x00, 0xf9})
		}
	}

	// Clean up stack
	if err := acg.out.AddImm64("sp", "sp", 16); err != nil {
		return err
	}

	// Return 0.0 (these functions don't return meaningful values)
	// fmov d0, xzr
	acg.out.out.writer.WriteBytes([]byte{0xe0, 0x03, 0x67, 0x9e})

	return nil
}

// compileMemoryRead compiles memory read helpers (read_i32, read_u32, read_f64,
// …): read_TYPE(ptr, index) loads the index-th element of the given width and
// leaves it in d0 (sign- or zero-extended for ints, raw for f64).
func (acg *ARM64CodeGen) compileMemoryRead(call *CallExpr) error {
	if len(call.Args) != 2 {
		return fmt.Errorf("%s() requires exactly 2 arguments (ptr, index)", call.Function)
	}

	var typeSize int
	switch call.Function {
	case "read_i8", "read_u8":
		typeSize = 1
	case "read_i16", "read_u16":
		typeSize = 2
	case "read_i32", "read_u32":
		typeSize = 4
	case "read_i64", "read_u64", "read_f64":
		typeSize = 8
	}
	isSigned := strings.HasPrefix(call.Function, "read_i")

	// Pointer (arg 0) -> x9 (numeric C-FFI pointer convention: fcvtzs x9, d0).
	if err := acg.compileExpression(call.Args[0]); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0x09, 0x00, 0x78, 0x9e})

	// Save pointer to stack.
	if err := acg.out.SubImm64("sp", "sp", 16); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0xe9, 0x03, 0x00, 0xf9}) // str x9, [sp]

	// Index (arg 1) -> x10 (fcvtzs x10, d0).
	if err := acg.compileExpression(call.Args[1]); err != nil {
		return err
	}
	acg.out.out.writer.WriteBytes([]byte{0x0a, 0x00, 0x78, 0x9e})

	// x10 = x10 * typeSize.
	if typeSize > 1 {
		if err := acg.out.MovImm64("x11", uint64(typeSize)); err != nil {
			return err
		}
		acg.out.out.writer.WriteBytes([]byte{0x4a, 0x7d, 0x0b, 0x9b}) // mul x10, x10, x11
	}

	// Effective address: x9 = ptr + offset.
	acg.out.out.writer.WriteBytes([]byte{0xe9, 0x03, 0x40, 0xf9}) // ldr x9, [sp]
	acg.out.out.writer.WriteBytes([]byte{0x29, 0x01, 0x0a, 0x8b}) // add x9, x9, x10

	if call.Function == "read_f64" {
		acg.out.out.writer.WriteBytes([]byte{0x20, 0x01, 0x40, 0xfd}) // ldr d0, [x9]
	} else {
		switch typeSize {
		case 1:
			if isSigned {
				acg.out.out.writer.WriteBytes([]byte{0x2a, 0x81, 0x80, 0x39}) // ldrsb x10, [x9]
			} else {
				acg.out.out.writer.WriteBytes([]byte{0x2a, 0x01, 0x40, 0x39}) // ldrb w10, [x9]
			}
		case 2:
			if isSigned {
				acg.out.out.writer.WriteBytes([]byte{0x2a, 0x81, 0x80, 0x79}) // ldrsh x10, [x9]
			} else {
				acg.out.out.writer.WriteBytes([]byte{0x2a, 0x01, 0x40, 0x79}) // ldrh w10, [x9]
			}
		case 4:
			if isSigned {
				acg.out.out.writer.WriteBytes([]byte{0x2a, 0x81, 0x80, 0xb9}) // ldrsw x10, [x9]
			} else {
				acg.out.out.writer.WriteBytes([]byte{0x2a, 0x01, 0x40, 0xb9}) // ldr w10, [x9]
			}
		case 8:
			acg.out.out.writer.WriteBytes([]byte{0x2a, 0x01, 0x40, 0xf9}) // ldr x10, [x9]
		}
		// Convert integer to double.
		if isSigned || typeSize == 8 {
			acg.out.out.writer.WriteBytes([]byte{0x40, 0x01, 0x62, 0x9e}) // scvtf d0, x10
		} else {
			acg.out.out.writer.WriteBytes([]byte{0x40, 0x01, 0x63, 0x9e}) // ucvtf d0, x10
		}
	}

	if err := acg.out.AddImm64("sp", "sp", 16); err != nil {
		return err
	}
	return nil
}

// generateArenaRuntimeARM64 generates arena runtime functions for ARM64
func (acg *ARM64CodeGen) generateArenaRuntimeARM64() error {
	// Define arena global variables in .data section
	acg.eb.Define("_tim_arena_meta", "\x00\x00\x00\x00\x00\x00\x00\x00")     // Pointer to meta-arena array
	acg.eb.Define("_tim_arena_meta_cap", "\x00\x00\x00\x00\x00\x00\x00\x00") // Capacity of meta-arena
	acg.eb.Define("_tim_arena_meta_len", "\x00\x00\x00\x00\x00\x00\x00\x00") // Length (number of arenas)

	// Generate arena runtime functions
	// These will be placeholders that call through to libc functions
	// For now, we'll generate simple stub implementations

	// _tim_arena_ensure_capacity(depth) - Ensure meta-arena can hold depth arenas
	// Simplified stub: just return (arena allocation is done directly by alloc())
	acg.eb.MarkLabel("_tim_arena_ensure_capacity")
	if err := acg.out.Return("x30"); err != nil {
		return err
	}

	// tim_arena_create(capacity) -> arena_ptr
	// Creates a new arena with the specified capacity
	// Argument: x0 = capacity
	// Returns: x0 = arena pointer
	acg.eb.MarkLabel("_tim_arena_create")
	// Save link register
	// stp x29, x30, [sp, #-16]!
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xbf, 0xa9})
	// Arena structure: [buffer_ptr][capacity][offset][alignment] = 32 bytes
	// For now, allocate 4KB buffer via malloc
	if err := acg.out.MovImm64("x0", 4096); err != nil {
		return err
	}
	if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
		return err
	}
	// Restore link register and return
	// ldp x29, x30, [sp], #16
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xc1, 0xa8})
	if err := acg.out.Return("x30"); err != nil {
		return err
	}

	// tim_arena_alloc(arena_ptr, size) -> allocation_ptr
	// Allocates memory from the arena
	// Arguments: x0 = arena_ptr, x1 = size
	// Returns: x0 = allocated memory pointer
	acg.eb.MarkLabel("_tim_arena_alloc")
	// Save link register
	// stp x29, x30, [sp, #-16]!
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xbf, 0xa9})
	// Simple stub: just call malloc with size in x0
	if err := acg.out.MovReg64("x0", "x1"); err != nil {
		return err
	}
	if err := acg.eb.GenerateCallInstruction("malloc"); err != nil {
		return err
	}
	// Restore link register and return
	// ldp x29, x30, [sp], #16
	acg.out.out.writer.WriteBytes([]byte{0xfd, 0x7b, 0xc1, 0xa8})
	if err := acg.out.Return("x30"); err != nil {
		return err
	}

	// tim_arena_reset(arena_ptr)
	// Resets the arena offset to 0
	// Argument: x0 = arena_ptr
	acg.eb.MarkLabel("_tim_arena_reset")
	// No-op for now
	if err := acg.out.Return("x30"); err != nil {
		return err
	}

	return nil
}
