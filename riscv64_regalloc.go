package main

import (
	"fmt"
	"slices"
	"strings"
)

// Register allocation. Every variable of a function (parameters and locals,
// not captured or global ones) lives in a callee-saved FP register while one
// is free, the most used first (uses inside loops weigh more). Loop counters
// take callee-saved integer registers. Callee-saved registers survive calls
// into other Tim functions, the runtime helpers and the C runtime, so values
// stay in registers across calls; each function saves and restores the ones
// it uses.

var (
	rvVarRegs     = []uint32{8, 9, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27}
	rvCounterRegs = []uint32{18, 19, 20, 21, 22, 23, 24, 25, 26, 27}
)

// rvAddNames records every variable name a node mentions.
func rvAddNames(n any, names map[string]bool) {
	switch s := n.(type) {
	case *IdentExpr:
		names[s.Name] = true
	case *AssignStmt:
		names[s.Name] = true
	case *MapUpdateStmt:
		names[s.MapName] = true
	case *CallExpr:
		names[s.Function] = true
		if recv, _, ok := strings.Cut(s.Function, "."); ok {
			names[recv] = true
		}
	case *LoopStmt:
		names[s.Iterator] = true
		names[s.KeyVar] = true
	case *ReceiveLoopStmt:
		names[s.MessageVar] = true
		names[s.SenderVar] = true
	case *MultipleAssignStmt:
		for _, name := range s.Names {
			names[name] = true
		}
	case *PostfixExpr:
		if id, ok := s.Operand.(*IdentExpr); ok {
			names[id.Name] = true
		}
	}
}

// useWeights counts variable uses, ten times more per enclosing loop.
func useWeights(root any) map[string]int {
	w := map[string]int{}
	var walk func(n any, weight int)
	walk = func(n any, weight int) {
		names := map[string]bool{}
		if _, isList := n.(*[]Statement); !isList {
			rvAddNames(n, names)
		}
		for name := range names {
			w[name] += weight
		}
		inner := weight
		switch n.(type) {
		case *LoopStmt, *WhileStmt, *ReceiveLoopStmt:
			inner = min(weight*10, 1_000_000)
		}
		for _, c := range astChildren(n, false) {
			walk(c, inner)
		}
	}
	walk(root, 1)
	return w
}

// loopDepth is the deepest nesting of loops that keep integer counters.
func loopDepth(n any) int {
	deepest := 0
	for _, c := range astChildren(n, false) {
		deepest = max(deepest, loopDepth(c))
	}
	if l, ok := n.(*LoopStmt); ok && l.NumThreads == 0 {
		deepest++
	}
	return deepest
}

func (g *rvGen) allocRegs(root any) {
	sc := g.sc
	sc.regs = map[string]uint32{}
	var names []string
	for name := range sc.vars {
		if !strings.HasPrefix(name, "\x00") {
			names = append(names, name)
		}
	}
	w := useWeights(root)
	slices.SortFunc(names, func(a, b string) int {
		if w[a] != w[b] {
			return w[b] - w[a]
		}
		return strings.Compare(a, b)
	})
	for i, name := range names {
		if i == len(rvVarRegs) {
			break
		}
		r := rvVarRegs[i]
		sc.regs[name] = r
		sc.saves = append(sc.saves, rvSave{r, sc.newVar(fmt.Sprintf("\x00save_f%d", r)), true})
	}
	counters := min(2*loopDepth(root), len(rvCounterRegs))
	for i := counters - 1; i >= 0; i-- {
		r := rvCounterRegs[i]
		sc.intPool = append(sc.intPool, r)
		sc.saves = append(sc.saves, rvSave{r, sc.newVar(fmt.Sprintf("\x00save_x%d", r)), false})
	}
}

// rvIntLoc is an integer loop counter: a register when one is free,
// otherwise a frame slot.
type rvIntLoc struct {
	reg  uint32
	slot int32
}

func (g *rvGen) intLoc() rvIntLoc {
	sc := g.sc
	if n := len(sc.intPool); n > 0 {
		r := sc.intPool[n-1]
		sc.intPool = sc.intPool[:n-1]
		return rvIntLoc{reg: r}
	}
	return rvIntLoc{slot: g.tmp()}
}

func (g *rvGen) releaseInt(l rvIntLoc) {
	if l.reg != 0 {
		g.sc.intPool = append(g.sc.intPool, l.reg)
	}
}

func (g *rvGen) sdi(rs uint32, l rvIntLoc) {
	if l.reg == 0 {
		g.sdx(rs, rvS0, l.slot)
	} else if rs != l.reg {
		g.mv(l.reg, rs)
	}
}

// intReg names the register holding l, loading slots into scratch.
func (g *rvGen) intReg(l rvIntLoc, scratch uint32) uint32 {
	if l.reg != 0 {
		return l.reg
	}
	g.ldx(scratch, rvS0, l.slot)
	return scratch
}
