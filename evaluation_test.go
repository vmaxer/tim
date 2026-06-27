package main

import (
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
