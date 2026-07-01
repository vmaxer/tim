package main

// CStructTypes is the platform-independent cstruct type oracle.
//
// Inferring which cstruct type an expression produces — through casts,
// constructors, function returns, if/match arms, nested fields, list elements,
// and inlined blocks — is pure semantic analysis with no architecture in it.
// Every code generator needs the exact same answers, so the logic lives here
// once and each backend delegates to it. Previously each backend carried its
// own copy and they drifted (the ARM64 copy learned func().field, ternary/if
// results and nested fields while the x86 copy still only knew casts and plain
// identifiers), which is exactly the kind of per-platform divergence this type
// exists to prevent.
//
// The maps are held by reference: a backend constructs CStructTypes over the
// same map instances it fills during compilation, so registrations it performs
// (and the temporary ones RegisterBlockLocals adds) are visible here and vice
// versa. The oracle never reassigns a map, only reads and mutates in place.
type CStructTypes struct {
	Decls               map[string]*CStructDecl // cstruct name -> declaration (field layout)
	VarType             map[string]string       // variable name -> cstruct type it points at
	FuncReturns         map[string]string       // function/lambda name -> cstruct type its body returns
	VarListElem         map[string]string       // list-variable name -> cstruct element type
	FuncReturnsListElem map[string]string       // function name -> cstruct element type of the list it returns
}

// TypeOf returns the name of the cstruct type expr evaluates to, or "" if expr
// is not a (known) cstruct value. Recognizes `as Struct` casts, struct
// constructors `Struct(...)`, calls to functions known to return a struct,
// identifiers already tracked as struct-typed, if/match results, nested cstruct
// fields, and blocks (via their final expression).
func (c *CStructTypes) TypeOf(expr Expression) string {
	switch e := expr.(type) {
	case *CastExpr:
		if _, ok := c.Decls[e.Type]; ok {
			return e.Type
		}
	case *CallExpr:
		if _, ok := c.Decls[e.Function]; ok {
			return e.Function
		}
		if t, ok := c.FuncReturns[e.Function]; ok {
			return t
		}
	case *IdentExpr:
		if t, ok := c.VarType[e.Name]; ok {
			return t
		}
	case *BlockExpr:
		// A block (e.g. an inlined function body) evaluates to its last
		// expression — look through it so `r = { …; V(…) }` is typed as V.
		if n := len(e.Statements); n > 0 {
			switch s := e.Statements[n-1].(type) {
			case *ExpressionStmt:
				return c.TypeOf(s.Expr)
			case *JumpStmt:
				if s.Value != nil {
					return c.TypeOf(s.Value)
				}
			case *IfStmt:
				return c.ifStmtType(s)
			}
		}
	case *MatchExpr:
		// `if c { a } else { b }` / guard match: every arm yields the same type,
		// so the result is that of any arm that evaluates to a cstruct.
		for _, cl := range e.Clauses {
			if cl.Result != nil {
				if t := c.TypeOf(cl.Result); t != "" {
					return t
				}
			}
		}
		if e.DefaultExpr != nil {
			return c.TypeOf(e.DefaultExpr)
		}
	case *FieldAccessExpr:
		// A nested cstruct-valued field, e.g. `b.c` where Ball has `c: V`.
		if decl := c.DeclForExpr(e); decl != nil {
			return decl.Name
		}
	}
	return ""
}

// DeclForExpr returns the cstruct declaration expr evaluates to (for field
// offset resolution), or nil. Handles identifiers, casts, list-element indexing
// (`xs[i]`), nested cstruct fields used as receivers (`b.c.x`), and call results
// used directly as receivers (`mk(...).x`).
func (c *CStructTypes) DeclForExpr(expr Expression) *CStructDecl {
	switch e := expr.(type) {
	case *IdentExpr:
		if name, ok := c.VarType[e.Name]; ok {
			return c.Decls[name]
		}
	case *CastExpr:
		if decl, ok := c.Decls[e.Type]; ok {
			return decl
		}
	case *IndexExpr:
		// `xs[i]` where xs is a known list of cstructs: the element is a pointer
		// to that cstruct, so `xs[i].field` reads at the field offset.
		if t := c.listElemTypeOf(e.List); t != "" {
			return c.Decls[t]
		}
	case *FieldAccessExpr:
		// A nested cstruct-valued field used as a receiver: `b.c.x` where `b.c`
		// is itself a cstruct (V). Resolve the outer object's struct, find the
		// field, and return the field's own struct declaration.
		if decl := c.DeclForExpr(e.Object); decl != nil {
			for i := range decl.Fields {
				if decl.Fields[i].Name == e.FieldName && decl.Fields[i].StructName != "" {
					return c.Decls[decl.Fields[i].StructName]
				}
			}
		}
	case *CallExpr:
		// A call result used directly as a receiver: `mk(...).x`. A constructor
		// yields its own struct; any other call yields the struct its function
		// is known to return. This is what lets `f(...).field` work without
		// first binding the result to a typed local.
		if decl, ok := c.Decls[e.Function]; ok {
			return decl
		}
		if t, ok := c.FuncReturns[e.Function]; ok {
			return c.Decls[t]
		}
	}
	return nil
}

// ifStmtType returns the cstruct type a trailing `if`/`elif`/`else` statement
// evaluates to (the type of any arm's final value). A struct-returning function
// may end in such a statement (e.g. a normalize-or-self), and without this its
// callers wouldn't know the result is a cstruct pointer.
func (c *CStructTypes) ifStmtType(s *IfStmt) string {
	blockType := func(body []Statement) string {
		if n := len(body); n > 0 {
			switch st := body[n-1].(type) {
			case *ExpressionStmt:
				return c.TypeOf(st.Expr)
			case *JumpStmt:
				if st.Value != nil {
					return c.TypeOf(st.Value)
				}
			case *IfStmt:
				return c.ifStmtType(st)
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

// listElemTypeOf infers the cstruct type of the ELEMENTS of a list-valued
// expression, or "". This lets `xs[i].field` resolve the field offset when xs
// is a list of cstructs (`[Ball(...), Ball(...)]`, a list-returning call, or a
// variable holding one).
func (c *CStructTypes) listElemTypeOf(expr Expression) string {
	switch e := expr.(type) {
	case *ListExpr:
		if len(e.Elements) > 0 {
			return c.TypeOf(e.Elements[0])
		}
	case *IdentExpr:
		if t, ok := c.VarListElem[e.Name]; ok {
			return t
		}
	case *CallExpr:
		if t, ok := c.FuncReturnsListElem[e.Function]; ok {
			return t
		}
	case *BlockExpr:
		if n := len(e.Statements); n > 0 {
			switch s := e.Statements[n-1].(type) {
			case *ExpressionStmt:
				return c.listElemTypeOf(s.Expr)
			case *JumpStmt:
				if s.Value != nil {
					return c.listElemTypeOf(s.Value)
				}
			}
		}
	}
	return ""
}

// LambdaReturnsListElem returns the cstruct element type of the list a lambda
// body evaluates to, or "" — so `f = () -> [Ball(...), ...]` lets `bs = f()`
// know `bs[i]` is a Ball.
func (c *CStructTypes) LambdaReturnsListElem(body Expression) string {
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
	return c.listElemTypeOf(last)
}

// LambdaReturnType returns the cstruct type a lambda body evaluates to (its last
// expression), or "" — used so `vadd = (a,b)->{...Point(...)}` lets
// `p = vadd(a,b)` know p is a Point.
func (c *CStructTypes) LambdaReturnType(body Expression) string {
	last := body
	if b, ok := body.(*BlockExpr); ok {
		if len(b.Statements) == 0 {
			return ""
		}
		// Pre-register the body's local cstruct types so a returned local
		// identifier (`r = …; r`) resolves even though the body has not been
		// compiled yet. Restore afterward so these locals don't leak globally.
		added := c.RegisterBlockLocals(b.Statements)
		defer func() {
			for _, k := range added {
				delete(c.VarType, k)
			}
		}()
		switch s := b.Statements[len(b.Statements)-1].(type) {
		case *ExpressionStmt:
			last = s.Expr
		case *JumpStmt:
			last = s.Value
		case *IfStmt:
			// A function may end in a bare `if`/`else` whose arms yield a cstruct.
			return c.ifStmtType(s)
		default:
			return ""
		}
	}
	if last == nil {
		return ""
	}
	return c.TypeOf(last)
}

// RegisterBlockLocals scans a block's statements in order and records the
// cstruct type of each struct-valued local into VarType (so later statements —
// and a trailing `return local` — can resolve it). It only adds names not
// already tracked, and returns the list of names it added so the caller can
// restore the map. It descends into `arena { … }` and `with … { … }` blocks,
// which wrap most struct-temporary scopes, but not control flow (a
// conditionally-defined local has no single static type).
func (c *CStructTypes) RegisterBlockLocals(stmts []Statement) []string {
	var added []string
	var scan func(ss []Statement)
	scan = func(ss []Statement) {
		for _, stmt := range ss {
			switch s := stmt.(type) {
			case *AssignStmt:
				if s.IsUpdate {
					continue
				}
				if _, exists := c.VarType[s.Name]; exists {
					continue
				}
				if ct := c.TypeOf(s.Value); ct != "" {
					c.VarType[s.Name] = ct
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
