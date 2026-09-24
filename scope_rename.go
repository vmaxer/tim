package main

import (
	"fmt"
	"reflect"
	"slices"
)

var statementsType = reflect.TypeFor[[]Statement]()

// uniquifyLocalFunctions gives local function definitions whose names collide with
// another function definition a unique name, so each gets its own code label.
func uniquifyLocalFunctions(program *Program) {
	type def struct {
		stmts []Statement
		index int
	}
	counts := make(map[string]int)
	var locals []def
	isFuncDef := func(s Statement) (*AssignStmt, bool) {
		a, ok := s.(*AssignStmt)
		if !ok || a.IsUpdate {
			return nil, false
		}
		_, ok = a.Value.(*LambdaExpr)
		return a, ok
	}
	for _, s := range program.Statements {
		if a, ok := isFuncDef(s); ok {
			counts[a.Name]++
		}
		forEachStatementList(reflect.ValueOf(s), func(stmts []Statement) {
			for i, st := range stmts {
				if a, ok := isFuncDef(st); ok {
					counts[a.Name]++
					locals = append(locals, def{stmts, i})
				}
			}
		})
	}
	seen := make(map[string]int)
	for _, d := range locals {
		a := d.stmts[d.index].(*AssignStmt)
		if counts[a.Name] < 2 {
			continue
		}
		seen[a.Name]++
		old, name := a.Name, fmt.Sprintf("%s__%d", a.Name, seen[a.Name])
		a.Name = name
		renameRefs(reflect.ValueOf(a.Value), old, name)
		renameInStatements(d.stmts[d.index+1:], old, name)
	}
}

func forEachStatementList(v reflect.Value, fn func([]Statement)) {
	walkAST(v, func(v reflect.Value) bool {
		if v.Type() == statementsType {
			fn(v.Interface().([]Statement))
		}
		return true
	})
}

func renameInStatements(stmts []Statement, old, name string) {
	for _, s := range stmts {
		if a, ok := s.(*AssignStmt); ok && a.Name == old {
			renameRefs(reflect.ValueOf(a.Value), old, name)
			if !a.IsUpdate {
				return
			}
			a.Name = name
			continue
		}
		if l, ok := s.(*LoopStmt); ok && l.Iterator == old {
			renameRefs(reflect.ValueOf(l.Iterable), old, name)
			continue
		}
		renameRefs(reflect.ValueOf(s), old, name)
	}
}

func renameRefs(v reflect.Value, old, name string) {
	walkAST(v, func(v reflect.Value) bool {
		if v.Kind() != reflect.Pointer || v.IsNil() {
			if v.Type() == statementsType {
				renameInStatements(v.Interface().([]Statement), old, name)
				return false
			}
			return true
		}
		switch n := v.Interface().(type) {
		case *IdentExpr:
			if n.Name == old {
				n.Name = name
			}
		case *CallExpr:
			if n.Function == old {
				n.Function = name
			}
		case *LambdaExpr:
			return !slices.Contains(n.Params, old) && n.VariadicParam != old
		}
		return true
	})
}

// walkAST visits every value reachable from v through pointers, interfaces,
// structs and slices, calling visit before descending; visit returns false to
// skip a subtree.
func walkAST(v reflect.Value, visit func(reflect.Value) bool) {
	seen := make(map[uintptr]bool)
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		if !v.IsValid() {
			return
		}
		switch v.Kind() {
		case reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
			return
		case reflect.Pointer:
			if v.IsNil() || seen[v.Pointer()] {
				return
			}
			seen[v.Pointer()] = true
		}
		if !visit(v) {
			return
		}
		switch v.Kind() {
		case reflect.Pointer:
			walk(v.Elem())
		case reflect.Struct:
			for i := range v.NumField() {
				if f := v.Field(i); f.CanInterface() {
					walk(f)
				}
			}
		case reflect.Slice:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		}
	}
	walk(v)
}
