package main

import (
	"fmt"
	"strings"
)

// desugarVariadics turns variadic functions into ordinary ones whose last
// parameter is a list: `(a, rest...) -> body` takes `rest` as a list, and a
// call f(1, 2, xs..., 9) passes f(1, [2] + xs + [9]). Calls are resolved by
// the name the function is bound to.
func desugarVariadics(program *Program) error {
	fixed := map[string]int{}
	var lambdas []*LambdaExpr
	astVisit(&program.Statements, true, func(n any) {
		if a, ok := n.(*AssignStmt); ok {
			if lam, ok := a.Value.(*LambdaExpr); ok && lam.VariadicParam != "" {
				fixed[a.Name] = len(lam.Params)
			}
		}
		if lam, ok := n.(*LambdaExpr); ok && lam.VariadicParam != "" {
			lambdas = append(lambdas, lam)
		}
	})
	var errs []string
	astVisit(&program.Statements, true, func(n any) {
		call, ok := n.(*CallExpr)
		if !ok {
			return
		}
		name := call.Function
		if name == "me" {
			return
		}
		k, variadic := fixed[name]
		if !variadic {
			for _, a := range call.Args {
				if _, spread := a.(*SpreadExpr); spread {
					errs = append(errs, fmt.Sprintf("%s: spreading with ... is only supported when calling variadic Tim functions", name))
					break
				}
			}
			return
		}
		if len(call.Args) < k {
			errs = append(errs, fmt.Sprintf("%s expects at least %d argument(s), got %d", name, k, len(call.Args)))
			return
		}
		for _, a := range call.Args[:k] {
			if _, spread := a.(*SpreadExpr); spread {
				errs = append(errs, fmt.Sprintf("%s: ... can only spread into the variadic parameter", name))
				return
			}
		}
		call.Args = append(call.Args[:k:k], variadicList(call.Args[k:]))
	})
	for _, lam := range lambdas {
		lam.Params = append(lam.Params, lam.VariadicParam)
		lam.VariadicParam = ""
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// variadicList builds the list expression for the arguments passed to a
// variadic parameter: runs of plain arguments become list literals, joined
// to spread lists with +.
func variadicList(args []Expression) Expression {
	var result Expression
	var run []Expression
	join := func(e Expression) {
		if result == nil {
			result = e
		} else {
			result = &BinaryExpr{Left: result, Operator: "+", Right: e}
		}
	}
	for _, a := range args {
		if s, ok := a.(*SpreadExpr); ok {
			if run != nil || result == nil {
				join(&ListExpr{Elements: run})
				run = nil
			}
			join(s.Expr)
			continue
		}
		run = append(run, a)
	}
	if run != nil || result == nil {
		join(&ListExpr{Elements: run})
	}
	return result
}
