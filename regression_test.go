package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func compileWithin(t *testing.T, src, exe string, limit time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- CompileTimWithOptions(src, exe, GetDefaultPlatform(), 0, false, false) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("compilation failed: %v", err)
		}
	case <-time.After(limit):
		t.Fatalf("compilation of %s did not finish within %v", src, limit)
	}
}

func compileAndRunTopLevel(t *testing.T, code string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.tim")
	exe := filepath.Join(dir, "main")
	if err := os.WriteFile(src, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	compileWithin(t, src, exe, 10*time.Second)
	out, err := runWithTimeout(exe, 5)
	if exitErr, ok := err.(*exec.ExitError); err != nil && (!ok || exitErr.ExitCode() < 0) {
		t.Fatalf("execution failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestExamplesCompile(t *testing.T) {
	files, err := filepath.Glob("examples/*.tim")
	if err != nil || len(files) == 0 {
		t.Skip("no examples")
	}
	for _, f := range files {
		if filepath.Base(f) == "hello.tim" {
			continue
		}
		t.Run(filepath.Base(f), func(t *testing.T) {
			compileWithin(t, f, filepath.Join(t.TempDir(), "out"), 10*time.Second)
		})
	}
}

func TestTopLevelRecursion(t *testing.T) {
	tests := []struct{ name, code, want string }{
		{"fib", "fib = n -> n < 2 { 1 => n ~> fib(n - 1) + fib(n - 2) }\nprintln(fib(20))\n", "6765\n"},
		{"guard", "fib = n -> { | n < 2 => n ~> fib(n - 1) + fib(n - 2) }\nprintln(fib(25))\n", "75025\n"},
		{"tail", "f = (n, acc) -> n == 0 { 1 => acc ~> f(n - 1, acc + 1) }\nprintln(f(1000000, 0))\n", "1000000\n"},
		{"mutual", "even = n -> n == 0 { 1 => 1 ~> odd(n - 1) }\nodd = n -> n == 0 { 1 => 0 ~> even(n - 1) }\nprintln(even(10))\n", "1\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compileAndRunTopLevel(t, tt.code); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrintNumbers(t *testing.T) {
	code := `x := 4294967296
x <- x + 1
println(x)
println(123456789012)
println(-2147483649)
println(9223372036854775807)
println(1e20)
println(-2.5e19)
println(1e-7)
println(3.25)
println(0)
f = 1e20 as float64
println(f)
println(-2.5e19 as float64)
println(1.5e300 as float64)
println(1e-7 as float64)
y := 1e308 as float64
println(y * 10)
println(-(y * 10))
println(0 / 0)
`
	want := `4294967297
123456789012
-2147483649
9223372036854775807
100000000000000000000
-25000000000000000000
0.0000001
3.25
0
1e+20
-2.5e+19
1.5e+300
1e-07
inf
-inf
nan
`
	if got := compileAndRunTopLevel(t, code); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestExactNumbers(t *testing.T) {
	code := `println(0.1 + 0.2 == 0.3)
println(0.1 + 0.2)
println(1 / 3 + 1 / 6)
println(22 / 7)
println(7 / 2)
println(2 ** 100)
println(2 ** -3)
println(10 ** 30 + 1 - 10 ** 30)
println(100000000 * 100000000 * 100000000)
x := 9007199254740991
x <- x + 2
println(x)
println(x - 2)
println(-(2 ** 70))
println(floor(-7 / 2))
println(ceil(7 / 2))
println(round(5 / 2))
println(abs(-1 / 3))
println(pow(3, 40))
println(1 / 3 < 0.34)
println((1 / 3) as float64)
println(sqrt(1 / 4))
println(17 % 5)
println((10 ** 20 + 7) % 10)
println(-7 / 2 % 2)
println(f"{1 / 3} and {2 ** 64}")
println((3.75 as str) + "!")
println(1 / 0 or! 42)
`
	want := `1
0.3
0.5
22/7
3.5
1267650600228229401496703205376
0.125
1
1000000000000000000000000
9007199254740993
9007199254740991
-1180591620717411303424
-4
4
3
1/3
12157665459056928801
1
0.333333
0.5
2
7
-1.5
1/3 and 18446744073709551616
3.75!
42
`
	if got := compileAndRunTopLevel(t, code); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintLists(t *testing.T) {
	code := `xs = [10, 20.5, 30]
println(xs)
print(xs)
println("")
println("a", xs, 3)
println([])
println({a: 1, b: 2})
`
	got := compileAndRunTopLevel(t, code)
	for _, want := range []string{"[10, 20.5, 30]\n[10, 20.5, 30]\n", "a [10, 20.5, 30] 3\n", "[]\n", "{1, 2}\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q does not contain %q", got, want)
		}
	}
}

func TestFunctionScoping(t *testing.T) {
	tests := []struct{ name, code, want string }{
		{"sameLocalName", "a = x -> {\n  h = y -> y + 1\n  h(x)\n}\nb = (x, z) -> {\n  h = (p, q) -> p * q\n  h(x, z)\n}\nprintln(a(1))\nprintln(b(2, 3))\n", "2\n6\n"},
		{"localShadowsTopLevel", "twice = x -> {\n  k := 3\n  go = y -> y * k\n  go(x) + go(x)\n}\ngo = x -> x - 1\nprintln(twice(2))\nprintln(go(10))\n", "12\n9\n"},
		{"localRecursion", "sum = n -> {\n  go = (i, acc) -> i > n { 1 => acc ~> go(i + 1, acc + i) }\n  go(1, 0)\n}\nprod = n -> {\n  go = (i, acc) -> i > n { 1 => acc ~> go(i + 1, acc * i) }\n  go(1, 1)\n}\nprintln(sum(10))\nprintln(prod(5))\n", "55\n120\n"},
		{"globalAlias", "f = x -> x * 2\ng = f\nprintln(g(4))\n", "8\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compileAndRunTopLevel(t, tt.code); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestArityMismatch(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "main.tim")
	if err := os.WriteFile(src, []byte("f = x -> x * 2\nprintln(f(1, 2))\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := CompileTimWithOptions(src, filepath.Join(dir, "main"), GetDefaultPlatform(), 0, false, false)
	if err == nil || !strings.Contains(err.Error(), "expects 1 argument(s), got 2") {
		t.Errorf("expected arity error, got %v", err)
	}
}

func TestNumberTruthiness(t *testing.T) {
	code := `t = x -> x { => 1 ~> 0 }
println(t(0))
println(t(1 / 3))
println(t(2 ** 80))
println(t(10 / 0))
println(t(1 / 3 - 1 / 3))
println(not (1 / 3))
println((1 / 3) and (2 ** 80))
println(0 or (1 / 3))
if 1 / 3 { println("if") }
f = x -> x {
    0.5 => "half"
    2 ** 70 => "big"
    ~> "other"
}
println(f(1 / 2))
println(f(2 ** 70))
println(f(2 ** 70 + 1))
`
	want := "0\n1\n1\n0\n0\n0\n1\n1\nif\nhalf\nbig\nother\n"
	if got := compileAndRunTopLevel(t, code); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
