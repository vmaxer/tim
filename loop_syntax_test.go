package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runTimSource(t *testing.T, name, source string) (string, int) {
	t.Helper()
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, name+".tim")
	exePath := filepath.Join(tmpDir, name)
	if err := os.WriteFile(srcPath, []byte(source), 0644); err != nil {
		t.Fatalf("Failed to write source: %v", err)
	}
	if err := CompileTim(srcPath, exePath, GetDefaultPlatform()); err != nil {
		t.Fatalf("Compilation failed: %v", err)
	}
	output, err := runWithTimeout(exePath, 10)
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("Execution failed: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return string(output), code
}

func compileErrorTimSource(t *testing.T, name, source, pattern string) {
	t.Helper()
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, name+".tim")
	if err := os.WriteFile(srcPath, []byte(source), 0644); err != nil {
		t.Fatalf("Failed to write source: %v", err)
	}
	err := CompileTim(srcPath, filepath.Join(tmpDir, name), GetDefaultPlatform())
	if err == nil {
		t.Fatalf("expected compilation to fail")
	}
	if !strings.Contains(err.Error(), pattern) {
		t.Fatalf("expected error %q, got: %v", pattern, err)
	}
}

func TestForLoopForms(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		expected string
	}{
		{"for_each_range", "for i in 0..<3 {\n    println(i)\n}\n", "0\n1\n2\n"},
		{"for_each_inclusive", "for i in 1..3 {\n    println(i)\n}\n", "1\n2\n3\n"},
		{"counted_loop", "for 3 {\n    println(\"x\")\n}\n", "x\nx\nx\n"},
		{"counted_zero", "for 0 {\n    println(\"x\")\n}\n", ""},
		{"infinite_with_exit", "n := 0\nfor {\n    n <- n + 1\n    n > 2 { ret @ }\n}\nprintln(n)\n", "3\n"},
		{"bounded_infinite", "m := 0\nfor ! 5 {\n    m <- m + 1\n    m > 2 { ret @ }\n}\nprintln(m)\n", "3\n"},
		{"condition_loop", "n := 3\nfor n > 0 ! 10 {\n    n <- n - 1\n}\nprintln(n)\n", "0\n"},
		{"step_range_inclusive", "for i in 0..10..3 {\n    println(i)\n}\n", "0\n3\n6\n9\n"},
		{"step_range_exclusive", "for i in 0..<10..2 {\n    println(i)\n}\n", "0\n2\n4\n6\n8\n"},
		{"step_range_as_list", "println(0..6..2)\n", "[0, 2, 4, 6]\n"},
		{"foreach_alias", "foreach i in 0..<2 {\n    println(i)\n}\n", "0\n1\n"},
		{"while_loop", "n := 0\nwhile n < 3 {\n    n <- n + 1\n}\nprintln(n)\n", "3\n"},
		{"for_each_list", "for x in [10, 20] {\n    println(x)\n}\n", "10\n20\n"},
		{"loop_as_lambda_body", "w = -> for i in 0..<2 {\n    println(i)\n}\nw()\n", "0\n1\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := runTimSource(t, tt.name, tt.source)
			if out != tt.expected {
				t.Errorf("got %q, want %q", out, tt.expected)
			}
		})
	}
}

func TestLoopLabels(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		expected string
	}{
		{"break_current", "for i in 0..<10 {\n    i > 2 { ret @ }\n    println(i)\n}\n", "0\n1\n2\n"},
		{"break_outer_label", "for i in 0..<3 {\n    for j in 0..<3 {\n        j > 1 { ret @ }\n        i == 2 { ret @1 }\n        println(i, j)\n    }\n}\n", "0 0\n0 1\n1 0\n1 1\n"},
		{"break_with_label", "for i in 0..<3 {\n    for j in 0..<3 {\n        j == 1 { break @1 }\n        println(i, j)\n    }\n}\n", "0 0\n"},
		{"break_continue_keywords", "for i in 0..<5 {\n    i == 2 { continue }\n    i == 4 { break }\n    println(i)\n}\n", "0\n1\n3\n"},
		{"ret_at_exits_only_loop", "f = {\n    for i in 0..<10 {\n        i == 3 { ret @ }\n    }\n    ret 7\n}\nprintln(f())\n", "7\n"},
		{"ret_at_with_value_exits", "n := 0\nfor i in 0..<100 {\n    i == 4 { ret @ 42 }\n    n <- n + 1\n}\nprintln(n)\n", "4\n"},
		{"ret_label_with_value_exits", "n := 0\nfor i in 0..<10 {\n    for j in 0..<10 {\n        j == 3 { ret @1 7 }\n        n <- n + 1\n    }\n    n <- n + 100\n}\nprintln(n)\n", "3\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := runTimSource(t, tt.name, tt.source)
			if out != tt.expected {
				t.Errorf("got %q, want %q", out, tt.expected)
			}
		})
	}
}

func TestLoopMaxIterations(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		expected string
	}{
		{"range_loop_capped", "for i in 0..<100 ! 5 {\n    println(i)\n}\n", "0\n1\n2\n3\n4\n"},
		{"condition_loop_capped", "n := 0\nfor yes ! 3 {\n    n <- n + 1\n}\nprintln(n)\n", "3\n"},
		{"infinite_capped", "m := 0\nfor ! 4 {\n    m <- m + 1\n}\nprintln(m)\n", "4\n"},
		{"bound_not_reached", "for i in 0..<3 ! 10 {\n    println(i)\n}\n", "0\n1\n2\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := runTimSource(t, tt.name, tt.source)
			if out != tt.expected {
				t.Errorf("got %q, want %q", out, tt.expected)
			}
		})
	}
}

func TestForSyntaxErrors(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		pattern string
	}{
		{"at_loop_rejected", "@ i in 0..<3 {\n    println(i)\n}\n", "loops start with 'for'"},
		{"counted_at_rejected", "@ 5 {\n    println(\"x\")\n}\n", "loops start with 'for'"},
		{"condition_without_bound", "n := 3\nfor n > 0 {\n    n <- n - 1\n}\n", "condition loop requires a '!' bound"},
		{"variadic_rejected", "sum = (a, rest...) -> a\nprintln(sum(1, 2))\n", "variadic functions are not yet supported"},
		{"dynamic_step_rejected", "s := 2\nfor i in 0..10..s {\n    println(i)\n}\n", "range step must be a positive integer constant"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compileErrorTimSource(t, tt.name, tt.source, tt.pattern)
		})
	}
}

func TestBooleanSemantics(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		expected string
	}{
		{"values", "println(yes)\nprintln(no)\n", "1\n0\n"},
		{"comparison_value", "println(10 > 5)\nprintln(3 > 5)\n", "1\n0\n"},
		{"match_on_bool", "b = 10 > 5\nc = b {\n    yes => \"b-yes\"\n    ~> \"b-other\"\n}\nprintln(c)\n", "b-yes\n"},
		{"match_on_number", "b = 10 > 5\nc = b {\n    1 => \"one\"\n    ~> \"other\"\n}\nprintln(c)\n", "one\n"},
		{"logic_ops", "println(yes and no)\nprintln(yes or no)\nprintln(not yes)\n", "0\n1\n0\n"},
		{"bool_equals_number", "println(yes == 1.0)\nprintln(no == 0.0)\n", "1\n1\n"},
		{"bool_as_num", "println(yes as num)\nprintln(no as num)\n", "1\n0\n"},
		{"bool_guard", "validate = x -> {\n    | x > 0 => \"pos\"\n    ~> \"non\"\n}\nprintln(validate(5))\nprintln(validate(-5))\n", "pos\nnon\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := runTimSource(t, tt.name, tt.source)
			if out != tt.expected {
				t.Errorf("got %q, want %q", out, tt.expected)
			}
		})
	}
}
