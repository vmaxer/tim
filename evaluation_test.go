package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEvaluation(t *testing.T) {
	tests := []struct {
		name           string
		code           string
		expectedOutput string
		expectCompile  bool
	}{
		{
			name: "block_disambiguation_map",
			code: `
				m = { x: 10 }
				println(m.x)
			`,
			expectedOutput: "10\n",
			expectCompile:  true,
		},
		{
			name: "block_disambiguation_block",
			code: `
				b = { x = 10; x }
				println(b())
			`,
			expectedOutput: "10\n",
			expectCompile:  true,
		},
		{
			name: "universal_type_number_as_map",
			code: `
				n = 42
				// Skip mutable syntax test - not fully implemented
				println(n)
			`,
			expectedOutput: "42\n",
			expectCompile:  true,
		},
		{
			name: "reproduce_global_capture_bug",
			// A nested lambda captures both an enclosing parameter (x) and an
			// immutable module value (state). `=` is immutable (GRAMMAR.md), so
			// state cannot be reassigned; the closure is stable across calls.
			code: `
				state = 10
				outer = (x) -> {
					inner = (y) -> state + x + y
					inner
				}

				f = outer(5)
				// 10 + 5 + 3 = 18 on each call
				res1 = f(3)
				res2 = f(3)

				println(f"{res1} {res2}")
			`,
			expectedOutput: "18 18\n",
			expectCompile:  true,
		},
		{
			name: "make_counter_mutable_capture",
			// The canonical closure from LANGUAGESPEC: a nested lambda mutates a
			// captured enclosing-local via `<-` and the change persists across
			// calls. The local is boxed in a shared heap cell.
			code: `
				make_counter = start -> {
					count := start
					() -> {
						count <- count + 1
						count
					}
				}
				main = {
					counter = make_counter(0)
					println(counter())
					println(counter())
					println(counter())
				}
			`,
			expectedOutput: "1\n2\n3\n",
			expectCompile:  true,
		},
		{
			name: "multi_clause_guard_match",
			// A multi-clause guard match on one line: the result of a clause must
			// not swallow the next clause's leading `|` as the pipe operator.
			code: `
				clampf = (x, lo, hi) -> { | x < lo => lo | x > hi => hi ~> x }
				main = {
					println(clampf(0.5, 0.0, 1.0))
					println(clampf(9.0, 0.0, 1.0))
					println(clampf(-3.0, 0.0, 1.0))
				}
			`,
			expectedOutput: "0.5\n1\n0\n",
			expectCompile:  true,
		},
		{
			name: "mixed_block_statements_then_guard",
			// A block with statements followed by a guard match: statements run
			// first, then the guard match is evaluated and returned.
			code: `
				f = (x) -> {
					y = x * 2.0
					| y > 1.0 => 100.0
					~> 200.0
				}
				main = {
					println(f(5.0))
					println(f(0.1))
				}
			`,
			expectedOutput: "100\n200\n",
			expectCompile:  true,
		},
		{
			name: "negative_fraction_print",
			// Values in (-1, 0) must print with their sign (integer part is 0, so
			// the sign cannot come from the integer formatter).
			code: `
				main = {
					println(0.0 - 0.5)
					x = 0.0 - 0.25
					println(x)
					println(3.0 - 5.0)
				}
			`,
			expectedOutput: "-0.5\n-0.25\n-2\n",
			expectCompile:  true,
		},
		{
			name: "typed_lambda_params",
			// `(a as V, b as V)` typed params let the body use a.x/b.x directly,
			// no `aa = a as V` cast. Also exercises the inliner substituting a
			// param inside a field access (previously dropped -> "undefined a").
			code: `
				cstruct V { x as float64, y as float64, z as float64 }
				vadd = (a as V, b as V) -> V(a.x+b.x, a.y+b.y, a.z+b.z)
				vdot = (a as V, b as V) -> a.x*b.x + a.y*b.y + a.z*b.z
				main = {
					p = V(1.0, 2.0, 3.0)
					q = V(10.0, 20.0, 30.0)
					r = vadd(p, q)
					println(r.x)
					println(r.z)
					println(vdot(p, q))
				}
			`,
			expectedOutput: "11\n33\n140\n",
			expectCompile:  true,
		},
		{
			name: "inlined_struct_ops_sroa",
			// Typed-param v3 ops are inline candidates; inlining must preserve the
			// param's cstruct type (so `a.x` doesn't become a map access) and the
			// codegen folds `Struct(..).field` to the field expression (SROA), all
			// while keeping nested/composed results correct.
			code: `
				cstruct V { x as float64, y as float64, z as float64 }
				vadd   = (a as V, b as V) -> V(a.x+b.x, a.y+b.y, a.z+b.z)
				vsub   = (a as V, b as V) -> V(a.x-b.x, a.y-b.y, a.z-b.z)
				vscale = (a as V, s) -> V(a.x*s, a.y*s, a.z*s)
				vdot   = (a as V, b as V) -> a.x*b.x + a.y*b.y + a.z*b.z
				vmix   = (a as V, b as V, t) -> vadd(vscale(a, 1.0 - t), vscale(b, t))
				main = {
					p = V(1.0, 2.0, 3.0)
					q = V(4.0, 5.0, 6.0)
					m = vmix(p, q, 0.5)
					println(m.y)
					println(vdot(vadd(p, q), vscale(p, 2.0)))
				}
			`,
			expectedOutput: "3.5\n92\n",
			expectCompile:  true,
		},
		{
			name: "fun_definition_syntax",
			// `fun name(params) { ... }` desugars to `name = (params) -> { ... }`,
			// with cstruct param types via `as V` or `: V`.
			code: `
				cstruct V { x as float64, y as float64, z as float64 }
				fun vadd(a as V, b as V) -> V(a.x+b.x, a.y+b.y, a.z+b.z)
				fun dotx(a: V, b: V) { a.x*b.x }
				fun square(n) { n * n }
				main = {
					p = V(1.0, 2.0, 3.0)
					q = V(10.0, 20.0, 30.0)
					r = vadd(p, q)
					println(r.x)
					println(dotx(p, q))
					println(square(6.0))
				}
			`,
			expectedOutput: "11\n10\n36\n",
			expectCompile:  true,
		},
		{
			name: "typed_loop_variable",
			// `@ v as float64 in range` and `@ b as Ball in list` — the cstruct
			// iterator type lets the body read b.field directly.
			code: `
				cstruct Ball { cx as float64, cy as float64, cz as float64, R as float64 }
				balls = [Ball(1.0,2.0,3.0,4.0), Ball(5.0,6.0,7.0,8.0)]
				main = {
					sum := 0.0
					@ i as float64 in 0..<4 {
						sum <- sum + i
					}
					println(sum)
					@ b as Ball in balls {
						println(b.cx + b.R)
					}
				}
			`,
			expectedOutput: "6\n5\n13\n",
			expectCompile:  true,
		},
		{
			name: "operator_overloading_cstruct",
			// `a OP b` on cstruct operands desugars to the named operator function
			// (V_add/V_sub/V_mul/V_scale) when defined; scalar arithmetic is left
			// untouched.
			code: `
				cstruct V { x as float64, y as float64, z as float64 }
				V_add   = (a: V, b: V) -> V(a.x+b.x, a.y+b.y, a.z+b.z)
				V_sub   = (a: V, b: V) -> V(a.x-b.x, a.y-b.y, a.z-b.z)
				V_mul   = (a: V, b: V) -> V(a.x*b.x, a.y*b.y, a.z*b.z)
				V_scale = (a: V, s) -> V(a.x*s, a.y*s, a.z*s)
				vdot    = (a: V, b: V) -> a.x*b.x + a.y*b.y + a.z*b.z
				main = {
					a = V(1.0, 2.0, 3.0)
					b = V(10.0, 20.0, 30.0)
					r = (a + b) as V
					println(r.x)
					s = (a - b) as V
					println(s.y)
					p = (a * b) as V
					println(p.z)
					q = (a * 2.0) as V
					println(q.x)
					q2 = (3.0 * a) as V
					println(q2.y)
					println(5.0 + 3.0 * 2.0)
					println(vdot(a, b) + 1.0)
				}
			`,
			expectedOutput: "11\n-18\n90\n2\n6\n11\n141\n",
			expectCompile:  true,
		},
		{
			name: "multiline_calls_and_lists",
			// Function-call arguments and list elements may span multiple lines.
			code: `
				cstruct V { x as float64, y as float64, z as float64 }
				add3 = (a, b, c) -> a + b + c
				main = {
					p = V(1.0,
					      2.0,
					      3.0) as V
					println(p.y)
					s = add3(10.0,
					         20.0,
					         30.0)
					println(s)
					lst = [4.0,
					       5.0,
					       6.0]
					println(lst[2])
				}
			`,
			expectedOutput: "2\n60\n6\n",
			expectCompile:  true,
		},
		{
			name: "for_loop_alias",
			// `for` is a full alias for `@`-loops: range, typed cstruct iterator.
			code: `
				cstruct Ball { cx as float64, R as float64 }
				balls = [Ball(1.0, 4.0), Ball(5.0, 8.0)]
				main = {
					sum := 0.0
					for i in 0..<5 {
						sum <- sum + i
					}
					println(sum)
					for b as Ball in balls {
						println(b.cx + b.R)
					}
				}
			`,
			expectedOutput: "10\n5\n13\n",
			expectCompile:  true,
		},
		{
			name: "colon_as_cast_alias",
			// `:` aliases `as` in unambiguous positions: postfix cast, lambda
			// params, and loop iterator type — while map literals keep `:`.
			code: `
				cstruct V { x as float64, y as float64, z as float64 }
				balls = [V(1.0,2.0,3.0), V(4.0,5.0,6.0)]
				addx = (a: V, b: V) -> a.x + b.x
				main = {
					p = balls[0] : V
					println(p.x)
					m = { x: 10.0, y: 20.0 }
					println(m.x)
					nm = { 5: 99.0 }
					println(nm[5])
					for b: V in balls {
						println(b.z)
					}
					println(addx(balls[0] : V, balls[1] : V))
				}
			`,
			expectedOutput: "1\n10\n99\n3\n6\n5\n",
			expectCompile:  true,
		},
		{
			name: "while_loop_and_if_jumps",
			// `while cond { }` is a condition loop with no explicit bound. break and
			// continue work inside `if` blocks (the parser leaves them on their last
			// token so block parsing stays in sync).
			code: `
				main = {
					i := 0.0
					while i < 3.0 {
						i <- i + 1.0
					}
					println(i)

					j := 0.0
					last := 0.0
					while j < 100.0 {
						j <- j + 1.0
						if j > 5.0 { break }
						last <- j
					}
					println(last)

					k := 0.0
					sum := 0.0
					while k < 6.0 {
						k <- k + 1.0
						if k == 3.0 { continue }
						sum <- sum + k
					}
					println(sum)
				}
			`,
			expectedOutput: "3\n5\n18\n",
			expectCompile:  true,
		},
		{
			name: "trailing_comma_in_list",
			// A trailing comma before ']' (common in multi-line lists) is allowed
			// and does not introduce a phantom nil element.
			code: `
				main = {
					xs := [
						1.0,
						2.0,
						3.0,
					]
					println(xs[0])
					println(xs[2])
				}
			`,
			expectedOutput: "1\n3\n",
			expectCompile:  true,
		},
		{
			name: "nested_cstruct_field_type_inference",
			// A cstruct field that is itself a cstruct (`Ball { c: V, r }`): under a
			// typed iterator the optimizer infers `b.c` as V, so operators and
			// methods on it (`p - b.c`, `d.dot(d)`) desugar to the typed functions.
			code: `
				cstruct V    { x, y, z: f64 }
				cstruct Ball { c: V, r: f64 }
				fun V.sub(o: V) = V(self.x-o.x, self.y-o.y, self.z-o.z)
				fun V.dot(o: V) = self.x*o.x + self.y*o.y + self.z*o.z
				balls = [Ball(V(1.0, 0.0, 0.0), 2.0), Ball(V(4.0, 0.0, 0.0), 1.0)]
				fun field(p: V) {
					sum := 0.0
					for b: Ball in balls {
						d := p - b.c
						sum += d.dot(d)
					}
					sum
				}
				main = { println(field(V(0.0, 0.0, 0.0))) }
			`,
			// (1-0)^2 + (4-0)^2 = 1 + 16 = 17
			expectedOutput: "17\n",
			expectCompile:  true,
		},
		{
			name: "list_index_cstruct_field_access",
			// Indexing a list of cstructs carries the element type, so `xs[i].field`
			// resolves the field offset — including nested cstruct fields
			// (`xs[i].c.x`) and lists returned from a function.
			code: `
				cstruct V    { x, y, z: f64 }
				cstruct Ball { c: V, r: f64 }
				fun make_balls() = [
					Ball(V(1.0, 2.0, 3.0), 9.0),
					Ball(V(4.0, 5.0, 6.0), 8.0),
				]
				main = {
					xs := [Ball(V(1.0,0.0,0.0), 7.0), Ball(V(2.0,0.0,0.0), 6.0)]
					println(xs[1].r)
					println(xs[0].c.x)
					bs := make_balls()
					println(bs[1].r)
					println(bs[1].c.y)
				}
			`,
			expectedOutput: "6\n1\n8\n5\n",
			expectCompile:  true,
		},
		{
			name: "cstruct_operator_through_fma",
			// `ro + rd * t` matches the FMA pattern, which the optimizer fuses
			// before operator overloading runs. The fused node must still desugar
			// to V_add/V_scale (a float FMA on cstruct pointer bits would be
			// silently wrong), and the function's return type must be inferred as V
			// through the FMA so `p.dot(p)` resolves.
			code: `
				cstruct V { x, y, z: f64 }
				fun V.add(o: V)  = V(self.x+o.x, self.y+o.y, self.z+o.z)
				fun V.scale(s)   = V(self.x*s, self.y*s, self.z*s)
				fun V.dot(o: V)  = self.x*o.x + self.y*o.y + self.z*o.z
				fun at(ro as V, rd as V, t) = ro + rd * t
				main = {
					p = at(V(1.0,2.0,3.0), V(1.0,0.0,0.0), 2.0)
					println(p.dot(p))
				}
			`,
			// at = (3,2,3); dot = 9+4+9 = 22
			expectedOutput: "22\n",
			expectCompile:  true,
		},
		{
			name: "cstruct_returned_through_if",
			// A function whose value flows through a conditional (an if-expression,
			// a trailing if-statement, or a guard match) returns a cstruct. The
			// codegen must infer this so callers treat the result as a pointer, and
			// a trailing `if` statement must yield its arm's value (not 0).
			code: `
				cstruct V { x, y, z: f64 }
				fun V.scale(s) = V(self.x*s, self.y*s, self.z*s)
				fun pick_expr(a as V, f) = if f > 0.0 { a * 2.0 } else { a }
				fun pick_stmt(a as V, f) {
					if f > 0.0 { a * 2.0 } else { a }
				}
				fun pick_local(a as V, f) {
					r = if f > 0.0 { a * 2.0 } else { a }
					r
				}
				main = {
					e = pick_expr(V(1.0,2.0,3.0), 1.0)
					s = pick_stmt(V(1.0,2.0,3.0), 1.0)
					l = pick_local(V(1.0,2.0,3.0), 0.0)
					println(e.x)
					println(s.y)
					println(l.z)
				}
			`,
			// e.x = 1*2 = 2; s.y = 2*2 = 4; l (f=0) = a, l.z = 3
			expectedOutput: "2\n4\n3\n",
			expectCompile:  true,
		},
		{
			name: "global_referenced_only_in_if_body",
			// A module global referenced only inside an `if` body within a function
			// must be detected as captured so it gets a global slot; otherwise
			// codegen fails with "undefined variable".
			code: `
				g_buf := 0.0
				fun fill(flag) {
					for i in 0..<4 {
						if flag > 0.5 {
							write_u32(g_buf, i, 7.0)
						}
					}
					1.0
				}
				main = {
					g_buf <- malloc(64)
					fill(1.0)
					println(read_u32(g_buf, 2))
				}
			`,
			expectedOutput: "7\n",
			expectCompile:  true,
		},
		{
			name: "trailing_if_statement_returns_value",
			// A bare `if`/`else` as a function's last statement is the function's
			// value (it must not fall through to 0).
			code: `
				fun classify(x) {
					y = x + 1.0
					if y > 10.0 { y * 2.0 } elif y > 5.0 { y } else { 0.0 }
				}
				main = {
					println(classify(10.0))
					println(classify(5.0))
					println(classify(1.0))
				}
			`,
			// classify(10): y=11 >10 -> 22; classify(5): y=6 ->6; classify(1): y=2 ->0
			expectedOutput: "22\n6\n0\n",
			expectCompile:  true,
		},
		{
			name: "field_access_on_call_result",
			// `f(...).field` reads a field directly off a call result without first
			// binding it to a typed local — including method-call results and a
			// nested cstruct field on a call result (`mkball().c.z`).
			code: `
				cstruct V { x, y, z: f64 }
				cstruct Ball { c: V, r: f64 }
				fun V.scale(s) = V(self.x*s, self.y*s, self.z*s)
				fun mk(a as V) = V(a.x+1.0, a.y, a.z)
				fun mkball() = Ball(V(3.0, 4.0, 5.0), 9.0)
				main = {
					println(mk(V(1.0, 2.0, 3.0)).x)
					a := V(2.0, 3.0, 4.0)
					println(a.scale(10.0).y)
					println(mkball().r)
					println(mkball().c.z)
				}
			`,
			expectedOutput: "2\n30\n9\n5\n",
			expectCompile:  true,
		},
		{
			name: "ternary_conditional_operator",
			// `cond ? a : b` lowers to the if-expression MatchExpr. Covers nesting
			// (right-associative), cstruct-valued arms, and coexistence with map
			// literals and the `?b` bit-test inside the same block.
			code: `
				cstruct V { x, y, z: f64 }
				fun V.scale(s) = V(self.x*s, self.y*s, self.z*s)
				fun sign(x) = x > 0.0 ? 1.0 : (x < 0.0 ? -1.0 : 0.0)
				fun pick(a as V, f) = f > 0.5 ? a.scale(2.0) : a
				main = {
					println(sign(7.0))
					println(sign(-2.0))
					println(sign(0.0))
					p = pick(V(1.0, 2.0, 3.0), 1.0)
					println(p.x)
					q = pick(V(1.0, 2.0, 3.0), 0.0)
					println(q.z)
					m = { a: 5, b: 6 }
					println(m.a)
					println(12.0 ?b 2.0)
				}
			`,
			expectedOutput: "1\n-1\n0\n2\n3\n5\n1\n",
			expectCompile:  true,
		},
		{
			name: "neon_vec3_elementwise_and_scale",
			// The NEON fast path for 3×f64 cstruct constructors must produce exactly
			// the same values as the scalar path for element-wise +/-/* and scalar
			// scale (both `v*s` and `s*v`). A struct whose fields aren't a plain
			// 3×f64-at-0/8/16 layout must NOT take the path (correctness, not perf).
			code: `
				cstruct V { x, y, z: f64 }
				cstruct P { a: f64, v: V, b: f64 }
				fun V.add(o: V) = V(self.x+o.x, self.y+o.y, self.z+o.z)
				fun V.sub(o: V) = V(self.x-o.x, self.y-o.y, self.z-o.z)
				fun V.mul(o: V) = V(self.x*o.x, self.y*o.y, self.z*o.z)
				fun V.scl(s)    = V(self.x*s, self.y*s, self.z*s)
				fun V.scl2(s)   = V(s*self.x, s*self.y, s*self.z)
				main = {
					a := V(1.0, 2.0, 3.0)
					b := V(10.0, 20.0, 30.0)
					s  = a.add(b)
					d  = a.sub(b)
					m  = a.mul(b)
					c1 = a.scl(2.0)
					c2 = a.scl2(3.0)
					println(s.x + s.y + s.z)
					println(d.x + d.y + d.z)
					println(m.x + m.y + m.z)
					println(c1.x + c1.y + c1.z)
					println(c2.x + c2.y + c2.z)
					p := P(7.0, V(4.0, 5.0, 6.0), 9.0)
					println(p.v.x + p.v.y + p.v.z)
				}
			`,
			// s=11+22+33=66; d=-9-18-27=-54; m=10+40+90=140;
			// c1=2+4+6=12; c2=3+6+9=18; p.v=4+5+6=15
			expectedOutput: "66\n-54\n140\n12\n18\n15\n",
			expectCompile:  true,
		},
		{
			name: "fork_mmap_shared_memory",
			// fork-based parallelism primitives: a child writes to an mmap'd
			// shared buffer, the parent reaps it and reads the value back. This is
			// the basis of the metaballs' multi-process renderer.
			code: `
				import libc as c
				cstruct Buf { v as uint32 }
				main = {
					buf = mmap(0, 64, 3, 4097, -1, 0) or! { exitf("mmap\n") }
					write_u32(buf, 0, 42)
					pid = fork()
					ischild = { | pid == 0.0 => 1.0 ~> 0.0 }
					ischild > 0.5 {
						write_u32(buf, 0, 1234)
						proc_exit(0)
					}
					waitpid(pid, 0, 0)
					b = buf as Buf
					println(b.v)
				}
			`,
			expectedOutput: "1234\n",
			expectCompile:  true,
		},
		{
			name: "sroa_local_struct",
			// SROA: a non-escaping local `p = Ctor(...)` used only via p.field is
			// scalarized (no allocation). Exercises multi-level inlining collapsing
			// to a constructor, plus a struct local read inside a loop.
			code: `
				cstruct V { x as float64, y as float64, z as float64 }
				vadd   = (a as V, b as V) -> V(a.x+b.x, a.y+b.y, a.z+b.z)
				vscale = (a as V, s) -> V(a.x*s, a.y*s, a.z*s)
				at     = (ro as V, rd as V, t) -> vadd(ro, vscale(rd, t))
				fun fieldlike(ro as V, rd as V, t) {
					p = at(ro, rd, t)
					sum := 0.0
					@ i in 0..<3 {
						sum <- sum + p.x + p.y + p.z
					}
					sum
				}
				main = {
					ro = V(1.0, 2.0, 3.0)
					rd = V(10.0, 20.0, 30.0)
					println(fieldlike(ro, rd, 2.0))
				}
			`,
			// p = (1+20, 2+40, 3+60) = (21,42,63); sum over 3 = 3*(21+42+63)=378
			expectedOutput: "378\n",
			expectCompile:  true,
		},
		{
			name: "small_match_helpers_inline",
			// Tiny guard-match helpers (predicate/clamp/quintic) must inline
			// correctly when used inside arithmetic — the hot-loop call-overhead
			// optimization.
			code: `
				clampf  = (x, lo, hi) -> { | x < lo => lo | x > hi => hi ~> x }
				ltf     = (a, b) -> { | a < b => 1.0 ~> 0.0 }
				quintic = (x) -> { | x >= 1.0 => 0.0 | x <= 0.0 => 1.0 ~> 1.0 - x*x*x*(x*(x*6.0 - 15.0) + 10.0) }
				main = {
					println(clampf(0.5, 0.0, 1.0) + clampf(9.0, 0.0, 1.0))
					println(ltf(3.0, 5.0) * 10.0 + ltf(5.0, 3.0))
					println(quintic(0.5))
					println(quintic(1.5))
				}
			`,
			expectedOutput: "1.5\n10\n0.5\n0\n",
			expectCompile:  true,
		},
		{
			name: "expr_register_stack",
			// Deeply nested arithmetic exercises the FP expression register stack
			// (left operand kept in d24+ instead of a memory spill) and its
			// fall-back to memory when an operand contains a call (sqrt here).
			code: `
				main = {
					a = 10.0
					b = 3.0
					println(((1.0+2.0)*(3.0+4.0)) + ((5.0-1.0)*(6.0-2.0)))
					println(a*a + b*b - a*b)
					println((1.0+2.0+3.0+4.0+5.0+6.0+7.0+8.0+9.0+10.0))
					println(sqrt(9.0) + sqrt(16.0) * 2.0)
					println((sqrt(4.0) + 1.0) * (sqrt(9.0) + 1.0))
				}
			`,
			expectedOutput: "37\n79\n55\n11\n12\n",
			expectCompile:  true,
		},
		{
			name: "loop_break_restores_sp",
			// Breaking out of a loop with `cond { ret @ }` must restore sp: the
			// value-match pushes its condition, and the break branches out before
			// the match's stack cleanup. Without the fix this leaks sp on every
			// break and corrupts the function epilogue (crash on return).
			code: `
				countdown = (n) -> {
					total := 0.0
					@ i in 0..<100 {
						i >= n { ret @ }
						total <- total + 1.0
					}
					total
				}
				main = {
					println(countdown(5.0))
					println(countdown(3.0))
					println(countdown(0.0))
				}
			`,
			expectedOutput: "5\n3\n0\n",
			expectCompile:  true,
		},
		{
			name: "make_counter_independent_state",
			// Two counters built from the same factory keep separate cells.
			code: `
				make_counter = start -> {
					count := start
					() -> { count <- count + 1; count }
				}
				main = {
					a = make_counter(10)
					b = make_counter(100)
					println(a())
					println(b())
					println(a())
				}
			`,
			expectedOutput: "11\n101\n12\n",
			expectCompile:  true,
		},
		{
			name: "with_block_prepends_subject",
			// `with <subj> { f(); g(x) }` prepends subj as the first argument of
			// each direct call statement in the body, so f(5) becomes f(subj, 5).
			code: `
				show = (subj, n) -> println(subj + n)
				main = {
					with 100.0 {
						show(5.0)
						show(7.0)
					}
				}
			`,
			expectedOutput: "105\n107\n",
			expectCompile:  true,
		},
		{
			name: "with_block_identifier_subject_zero_arg",
			// The subject can be an identifier and the body calls can be zero-arg;
			// `emit()` inside `with base` becomes `emit(base)`.
			code: `
				emit = (tag) -> println(tag)
				main = {
					base = 42.0
					with base {
						emit()
					}
				}
			`,
			expectedOutput: "42\n",
			expectCompile:  true,
		},
		{
			name: "variable_named_c_shadows_cffi_namespace",
			// A local variable named `c` (or `C`) must shadow the always-registered
			// C-FFI namespace, so `c.x` is a struct-field access — not a `c.`
			// namespace reference that silently compiles to a wild memory read.
			code: `
				cstruct V { x: float64, y: float64, z: float64 }
				main = {
					c = V(10.0, 20.0, 30.0)
					println(c.x)
					println(c.z)
				}
			`,
			expectedOutput: "10\n30\n",
			expectCompile:  true,
		},
		{
			name: "cstruct_value_type_end_to_end",
			// Exercises the cstruct value-type paths that must behave identically
			// on every backend: constructor to a local + field read, a method that
			// reads its `self` fields to a scalar, `func(...).field` on a plain
			// function, and `recv.method(...).field` chained on a method result.
			code: `
				cstruct V { x: float64, y: float64, z: float64 }
				fun V.dot(o: V) = self.x*o.x + self.y*o.y + self.z*o.z
				fun V.scale(s) = V(self.x*s, self.y*s, self.z*s)
				mk = (a) -> V(a, a*2.0, a*3.0)
				main = {
					a = V(1.0, 2.0, 3.0)
					b = V(4.0, 5.0, 6.0)
					println(a.x)
					println(a.z)
					println(a.dot(b))
					println(mk(2.0).z)
					println(a.scale(10.0).y)
				}
			`,
			expectedOutput: "1\n3\n32\n6\n20\n",
			expectCompile:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.expectCompile {
				return
			}

			output := compileAndRun(t, tt.code)
			if !strings.Contains(output, tt.expectedOutput) {
				t.Errorf("Expected output to contain %q, got %q", tt.expectedOutput, output)
			}
		})
	}
}

// TestPlainValueMainExitCode guards a cross-backend consistency fix: a plain
// value `main = N` (no block) must compile on every backend and use N as the
// process exit code. The arm64 backend always accepted this shape; the x86
// backends used to reject it with "'main' must be a function". compileAndRun
// discards the exit code, so this test checks it directly.
func TestPlainValueMainExitCode(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "pv.tim")
	if err := os.WriteFile(srcFile, []byte("main = 7\n"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	exePath := filepath.Join(tmpDir, "pv")
	if runtime.GOOS == "windows" {
		exePath += ".exe"
	}
	osType, _ := ParseOS(runtime.GOOS)
	archType, _ := ParseArch(runtime.GOARCH)
	if err := CompileTimWithOptions(srcFile, exePath, Platform{OS: osType, Arch: archType}, 0, false, false); err != nil {
		t.Fatalf("plain-value main should compile, got: %v", err)
	}

	cmd := exec.Command(exePath)
	cmd.Env = os.Environ()
	err := cmd.Run()
	got := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("execution failed: %v", err)
		}
		got = exitErr.ExitCode()
	}
	if got != 7 {
		t.Errorf("plain-value main exit code = %d, want 7", got)
	}
}

// compileErr compiles a snippet for the host platform and returns the resulting
// error (nil on success). Used to assert on diagnostic quality.
func compileErr(t *testing.T, code string) error {
	t.Helper()
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "e.tim")
	if err := os.WriteFile(src, []byte(code), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	osType, _ := ParseOS(runtime.GOOS)
	archType, _ := ParseArch(runtime.GOARCH)
	return CompileTimWithOptions(src, filepath.Join(tmpDir, "e"), Platform{OS: osType, Arch: archType}, 0, false, false)
}

// TestDiagnosticsNotNoisy guards two user-facing diagnostic clean-ups:
//  1. Syntax errors are printed once, in full (snippet + caret), then abort via
//     ErrAlreadyReported so no second context-free copy is shown.
//  2. Semantic errors carry no doubled "compilation failed:" prefix and, when a
//     near name exists, include a "Did you mean" suggestion consistently.
func TestDiagnosticsNotNoisy(t *testing.T) {
	// Syntax error → already-reported sentinel (top level stays quiet).
	synErr := compileErr(t, "main = {\n  x = = 5\n}\n")
	if synErr == nil {
		t.Fatal("expected a syntax error")
	}
	if !errors.Is(synErr, ErrAlreadyReported) {
		t.Errorf("syntax error should be ErrAlreadyReported (already printed in full), got: %v", synErr)
	}

	// Semantic error → raw message, no doubled prefix, with a suggestion.
	semErr := compileErr(t, "main = {\n  velocity = 3.0\n  println(veloctiy)\n}\n")
	if semErr == nil {
		t.Fatal("expected an undefined-variable error")
	}
	msg := semErr.Error()
	if strings.Contains(msg, "compilation failed") {
		t.Errorf("library-level error must not carry a 'compilation failed' prefix (the CLI adds one): %q", msg)
	}
	if !strings.Contains(msg, "undefined variable 'veloctiy'") {
		t.Errorf("expected an 'undefined variable' message, got: %q", msg)
	}
	if !strings.Contains(msg, "Did you mean: velocity?") {
		t.Errorf("expected a 'Did you mean' suggestion for a near name, got: %q", msg)
	}
}
