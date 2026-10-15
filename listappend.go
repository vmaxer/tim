package main

import "slices"

import "fmt"

// Amortized list appends.
//
// `xs <- append(xs, v)` copies xs, so building a list in a loop is quadratic.
// When a mutable list variable never lets its value escape while it grows,
// the copy is unobservable: appendOwned rewrites such statements to
//
//	xs <- __append_owned(xs, owner, v)
//
// where owner is a hidden variable holding the buffer xs exclusively owns.
// Backends append in place while xs is that buffer and it has room, and
// otherwise grow into a fresh power-of-two buffer that becomes the new owned
// one. Wherever the current value of xs may be shared (stored, passed to a
// function, returned, captured...) owner is reset first, so the next append
// copies again and the shared value never changes.

const appendOwnedBuiltin = "__append_owned"

func optimizeListAppends(program *Program) {
	p := &appendPass{}
	var lambdas []*LambdaExpr
	astVisit(&program.Statements, true, func(n any) {
		if lam, ok := n.(*LambdaExpr); ok {
			lambdas = append(lambdas, lam)
		}
	})
	p.scope(&program.Statements, nil)
	for _, lam := range lambdas {
		p.scope(lam.Body, lam.Params)
	}
}

type appendPass struct{ n int }

type appendSite struct {
	list *[]Statement
	decl *AssignStmt
}

func (s appendSite) at() int {
	for i, st := range *s.list {
		if st == Statement(s.decl) {
			return i
		}
	}
	return -1
}

// scope optimizes the variables declared in one function body (or the top
// level), not counting nested lambdas.
func (p *appendPass) scope(root any, params []string) {
	decls := map[string]int{}
	for _, param := range params {
		decls[param]++
	}
	sites := map[string]appendSite{}
	appends := map[string]int{}
	forEachList(root, func(list *[]Statement) {
		for _, s := range *list {
			switch s := s.(type) {
			case *AssignStmt:
				if s.IsUpdate || s.IsReuseMutable {
					if appendValues(s) != nil {
						appends[s.Name]++
					}
					continue
				}
				decls[s.Name]++
				if s.Mutable {
					sites[s.Name] = appendSite{list, s}
				}
			case *MultipleAssignStmt:
				if !s.IsUpdate {
					for _, name := range s.Names {
						decls[name]++
					}
				}
			case *LoopStmt:
				decls[s.Iterator]++
				decls[s.KeyVar]++
			}
		}
	})
	for name, count := range appends {
		site, ok := sites[name]
		if !ok || count == 0 || decls[name] != 1 || !p.qualifies(root, name, site) {
			continue
		}
		p.rewrite(name, site)
	}
}

// qualifies checks that every use of x lies after its declaration in the
// same statement list, outside lambdas and loop conditions, and that no
// statement both shares x and grows it from inside one of its expressions.
func (p *appendPass) qualifies(root any, x string, site appendSite) bool {
	if astMentions(site.decl.Value, x) {
		return false
	}
	inRegion := 0
	ok := true
	for _, s := range (*site.list)[site.at()+1:] {
		astVisit(s, true, func(n any) {
			if nodeNames(n, x) {
				inRegion++
			}
			switch n := n.(type) {
			case *LambdaExpr:
				if astMentions(n.Body, x) || nodeNames(n, x) {
					ok = false
				}
			case *WhileStmt:
				if astMentions(n.Condition, x) {
					ok = false
				}
			case *ArenaStmt, *DeferStmt, *ReceiveLoopStmt, *SpawnStmt, *ParallelExpr, *BackgroundExpr:
				if astMentions(n, x) {
					ok = false
				}
			case *LoopStmt:
				if n.NumThreads != 0 && astMentions(n, x) {
					ok = false
				}
			}
		})
		forEachList(s, func(list *[]Statement) {
			for _, st := range *list {
				if shares(st, x) && growsInside(st, x) {
					ok = false
				}
			}
		})
		if shares(s, x) && growsInside(s, x) {
			ok = false
		}
	}
	total := 0
	astVisit(root, true, func(n any) {
		if nodeNames(n, x) {
			total++
		}
	})
	return ok && total == inRegion+1
}

func (p *appendPass) rewrite(x string, site appendSite) {
	p.n++
	owner := fmt.Sprintf("__own_%s_%d", x, p.n)
	at := site.at()
	var out []Statement
	for _, s := range (*site.list)[at+1:] {
		out = append(out, p.rewriteStmt(s, x, owner)...)
	}
	list := append([]Statement{}, (*site.list)[:at+1]...)
	list = append(list, &AssignStmt{Name: owner, Value: &NumberExpr{}, Mutable: true})
	*site.list = append(list, out...)
}

// rewriteStmt returns s, preceded by an owner reset when s shares x, with its
// nested statement lists rewritten the same way.
func (p *appendPass) rewriteStmt(s Statement, x, owner string) []Statement {
	shared := shares(s, x)
	forEachChildList(s, func(list *[]Statement) {
		var out []Statement
		for _, st := range *list {
			out = append(out, p.rewriteStmt(st, x, owner)...)
		}
		*list = out
	})
	if a, ok := s.(*AssignStmt); ok && a.Name == x {
		if vals := appendValues(a); vals != nil {
			a.Value = &CallExpr{Function: appendOwnedBuiltin, Args: append([]Expression{&IdentExpr{Name: x}, &IdentExpr{Name: owner}}, vals...)}
		}
	}
	if shared {
		return []Statement{&AssignStmt{Name: owner, Value: &NumberExpr{}, Mutable: true, IsUpdate: true}, s}
	}
	return []Statement{s}
}

// appendValues returns the appended values when s is `x <- append(x, v...)`
// or `x <- x + [v...]`.
func appendValues(s *AssignStmt) []Expression {
	switch v := s.Value.(type) {
	case *CallExpr:
		if v.Function == "append" && len(v.Args) >= 2 {
			if id, ok := v.Args[0].(*IdentExpr); ok && id.Name == s.Name {
				return v.Args[1:]
			}
		}
	case *BinaryExpr:
		if v.Operator == "+" {
			if id, ok := v.Left.(*IdentExpr); ok && id.Name == s.Name {
				if l, ok := v.Right.(*ListExpr); ok && len(l.Elements) > 0 {
					return l.Elements
				}
			}
		}
	}
	return nil
}

// shares reports whether evaluating s's own expressions (not its nested
// statement lists) may let the current value of x escape.
func shares(s Statement, x string) bool {
	switch s := s.(type) {
	case *AssignStmt:
		if s.Name == x {
			if vals := appendValues(s); vals != nil {
				for _, v := range vals {
					if escapes(v, x) {
						return true
					}
				}
				return false
			}
		}
		return escapes(s.Value, x)
	case *ExpressionStmt:
		return escapes(s.Expr, x)
	case *MultipleAssignStmt:
		return escapes(s.Value, x)
	case *MapUpdateStmt:
		return escapes(s.Index, x) || escapes(s.Value, x)
	case *JumpStmt:
		return escapes(s.Value, x)
	case *LoopStmt:
		if id, ok := s.Iterable.(*IdentExpr); ok && id.Name == x {
			grows := false
			forEachList(&s.Body, func(list *[]Statement) {
				for _, st := range *list {
					if a, ok := st.(*AssignStmt); ok && a.Name == x && appendValues(a) != nil {
						grows = true
					}
				}
			})
			return grows
		}
		return escapes(s.Iterable, x)
	case *WhileStmt:
		return escapes(s.Condition, x)
	case *IfStmt:
		for _, b := range s.Branches {
			if escapes(b.Condition, x) {
				return true
			}
		}
		return false
	}
	return false
}

// growsInside reports whether an append to x runs while one of s's own
// expressions is being evaluated (from a block inside it).
func growsInside(s Statement, x string) bool {
	found := false
	for _, c := range astChildren(s, false) {
		if _, isList := c.(*[]Statement); isList {
			continue
		}
		astVisit(c, false, func(n any) {
			if a, ok := n.(*AssignStmt); ok && a.Name == x && appendValues(a) != nil {
				found = true
			}
		})
	}
	return found
}

var appendSafeBuiltins = map[string]bool{
	"println": true, "print": true, "eprintln": true, "eprint": true, "printf": true,
	"eprintf": true, "exitf": true, "exitln": true, "str": true, "head": true,
	"tail": true, "pop": true,
}

// escapes reports whether e may hand out the value of x itself (rather than
// reading from it or copying it). Statements inside blocks are judged on
// their own, so blocks are not entered.
func escapes(e Expression, x string) bool {
	if e == nil || isNilExpr(e) {
		return false
	}
	read := func(o Expression) bool {
		if id, ok := o.(*IdentExpr); ok && id.Name == x {
			return false
		}
		return escapes(o, x)
	}
	switch e := e.(type) {
	case *IdentExpr:
		return e.Name == x
	case *BlockExpr, *LambdaExpr:
		return false
	case *IndexExpr:
		return read(e.List) || escapes(e.Index, x)
	case *SliceExpr:
		return read(e.List) || escapes(e.Start, x) || escapes(e.End, x) || escapes(e.Step, x)
	case *LengthExpr:
		return read(e.Operand)
	case *UnaryExpr:
		return read(e.Operand)
	case *InExpr:
		return read(e.Value) || read(e.Container)
	case *FieldAccessExpr:
		return read(e.Object)
	case *FStringExpr:
		return slices.ContainsFunc(e.Parts, read)
	case *CastExpr:
		if e.Type == "string" || e.Type == "str" {
			return read(e.Expr)
		}
		return escapes(e.Expr, x)
	case *BinaryExpr:
		switch e.Operator {
		case "or!", "and", "or":
			return escapes(e.Left, x) || escapes(e.Right, x)
		}
		return read(e.Left) || read(e.Right)
	case *CallExpr:
		if e.Function == x {
			return true
		}
		for i, a := range e.Args {
			safe := appendSafeBuiltins[e.Function] || e.Function == "append" && i == 0
			if safe && read(a) || !safe && escapes(a, x) {
				return true
			}
		}
		return false
	case *MatchExpr:
		if read(e.Condition) || escapes(e.DefaultExpr, x) {
			return true
		}
		for _, c := range e.Clauses {
			if read(c.Guard) || escapes(c.Result, x) {
				return true
			}
		}
		return false
	}
	for _, c := range astChildren(e, false) {
		if ce, ok := c.(Expression); ok && escapes(ce, x) {
			return true
		}
	}
	return false
}

// forEachList calls f for every statement list in a subtree, outside lambdas.
func forEachList(root any, f func(*[]Statement)) {
	if list, ok := root.(*[]Statement); ok {
		f(list)
	}
	for _, c := range astChildren(root, false) {
		if list, ok := c.(*[]Statement); ok {
			f(list)
			for _, s := range *list {
				forEachList(s, f)
			}
			continue
		}
		forEachList(c, f)
	}
}

// forEachChildList calls f for each statement list directly nested in s,
// including those of blocks inside its expressions, outside lambdas.
func forEachChildList(s any, f func(*[]Statement)) {
	for _, c := range astChildren(s, false) {
		if list, ok := c.(*[]Statement); ok {
			f(list)
			continue
		}
		forEachChildList(c, f)
	}
}
