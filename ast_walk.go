package main

import "slices"

// astChildren lists a node's direct children in source order. Nested
// statement lists appear as *[]Statement so callers can rewrite them; lambda
// bodies are included only when lambdas is set.
func astChildren(node any, lambdas bool) []any {
	var out []any
	e := func(x Expression) {
		if x != nil && !isNilExpr(x) {
			out = append(out, x)
		}
	}
	switch n := node.(type) {
	case *[]Statement:
		for _, s := range *n {
			out = append(out, s)
		}
	case *AssignStmt:
		e(n.Value)
	case *MultipleAssignStmt:
		e(n.Value)
	case *ExpressionStmt:
		e(n.Expr)
	case *LoopStmt:
		e(n.Iterable)
		out = append(out, &n.Body)
	case *WhileStmt:
		e(n.Condition)
		out = append(out, &n.Body)
	case *IfStmt:
		for i := range n.Branches {
			e(n.Branches[i].Condition)
			out = append(out, &n.Branches[i].Body)
		}
		out = append(out, &n.ElseBody)
	case *ArenaStmt:
		out = append(out, &n.Body)
	case *WithStmt:
		e(n.Subject)
		out = append(out, &n.Body)
	case *ReceiveLoopStmt:
		e(n.Address)
		out = append(out, &n.Body)
	case *DeferStmt:
		e(n.Call)
	case *MapUpdateStmt:
		e(n.Index)
		e(n.Value)
	case *JumpStmt:
		e(n.Value)
	case *BlockExpr:
		out = append(out, &n.Statements)
	case *MatchExpr:
		e(n.Condition)
		for _, c := range n.Clauses {
			e(c.Guard)
			e(c.Result)
		}
		e(n.DefaultExpr)
	case *BinaryExpr:
		e(n.Left)
		e(n.Right)
	case *UnaryExpr:
		e(n.Operand)
	case *PostfixExpr:
		e(n.Operand)
	case *LengthExpr:
		e(n.Operand)
	case *CastExpr:
		e(n.Expr)
	case *MoveExpr:
		e(n.Expr)
	case *SpreadExpr:
		e(n.Expr)
	case *CallExpr:
		for _, a := range n.Args {
			e(a)
		}
	case *DirectCallExpr:
		e(n.Callee)
		for _, a := range n.Args {
			e(a)
		}
	case *FStringExpr:
		for _, p := range n.Parts {
			e(p)
		}
	case *ListExpr:
		for _, x := range n.Elements {
			e(x)
		}
	case *MapExpr:
		for i := range n.Keys {
			e(n.Keys[i])
			e(n.Values[i])
		}
	case *IndexExpr:
		e(n.List)
		e(n.Index)
	case *SliceExpr:
		e(n.List)
		e(n.Start)
		e(n.End)
		e(n.Step)
	case *RangeExpr:
		e(n.Start)
		e(n.End)
		e(n.Step)
	case *FieldAccessExpr:
		e(n.Object)
	case *InExpr:
		e(n.Value)
		e(n.Container)
	case *PipeExpr:
		e(n.Left)
		e(n.Right)
	case *ParallelExpr:
		e(n.List)
		e(n.Operation)
	case *ComposeExpr:
		e(n.Left)
		e(n.Right)
	case *JumpExpr:
		e(n.Value)
	case *FMAExpr:
		e(n.A)
		e(n.B)
		e(n.C)
	case *LambdaExpr:
		if lambdas {
			e(n.Body)
		}
	}
	return out
}

// astVisit calls f for every node of a subtree in source order (statement
// lists themselves are not reported), descending into lambda bodies only
// when lambdas is set.
func astVisit(node any, lambdas bool, f func(any)) {
	if node == nil {
		return
	}
	if x, ok := node.(Expression); ok && isNilExpr(x) {
		return
	}
	if _, isList := node.(*[]Statement); !isList {
		f(node)
	}
	for _, c := range astChildren(node, lambdas) {
		astVisit(c, lambdas, f)
	}
}

// isNilExpr reports whether an interface holds a typed nil pointer.
func isNilExpr(e Expression) bool {
	switch x := e.(type) {
	case *BlockExpr:
		return x == nil
	case *MatchExpr:
		return x == nil
	case *IdentExpr:
		return x == nil
	case *NumberExpr:
		return x == nil
	case *LambdaExpr:
		return x == nil
	case *CallExpr:
		return x == nil
	case *BinaryExpr:
		return x == nil
	}
	return false
}

// astMentions reports whether a subtree (lambdas included) names variable x.
func astMentions(node any, x string) bool {
	found := false
	astVisit(node, true, func(n any) {
		if !found && nodeNames(n, x) {
			found = true
		}
	})
	return found
}

func nodeNames(n any, x string) bool {
	switch s := n.(type) {
	case *IdentExpr:
		return s.Name == x
	case *AssignStmt:
		return s.Name == x
	case *MapUpdateStmt:
		return s.MapName == x
	case *CallExpr:
		return s.Function == x
	case *LoopStmt:
		return s.Iterator == x || s.KeyVar == x
	case *ReceiveLoopStmt:
		return s.MessageVar == x || s.SenderVar == x
	case *MultipleAssignStmt:
		if slices.Contains(s.Names, x) {
			return true
		}
	case *LambdaExpr:
		if slices.Contains(s.Params, x) {
			return true
		}
	}
	return false
}
