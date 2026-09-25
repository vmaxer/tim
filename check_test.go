package main

import (
	"strings"
	"testing"
)

func checkSource(t *testing.T, src string) (*Checked, string) {
	t.Helper()
	prog, perr := parseString(t, src)
	if perr != "" {
		t.Fatalf("parse error: %s", perr)
	}
	c, err := Check(prog, "test.tim", src)
	if err != nil {
		return nil, err.Error()
	}
	return c, ""
}

func TestCheckAcceptsValidPrograms(t *testing.T) {
	for _, src := range []string{
		"fact(n) = n <= 1 { => 1 ~> n * fact(n - 1) }\nprintln(fact(5))",
		"even(n) = n == 0 { => yes ~> odd(n - 1) }\nodd(n) = n == 0 { => no ~> even(n - 1) }",
		"f = () -> g()\ng = () -> 1",
		"n := 0\n@ i in 0..<3 {\n  n <- n + i\n}",
		"xs := [1, 2]\nxs[0] <- 5\npush(xs, 3)",
		"x = 1\nf = () -> { x = 2\n x }",
		"counter = () -> {\n c := 0\n () -> {\n c <- c + 1\n c\n }\n}",
		"s = \"a\" + \"b\"\nprintln(#s, s.upper())",
		"sum(xs...) = fold(xs, 0, (a, b) -> a + b)\nprintln(sum(1, 2, 3))",
		"m = {a: 1}\nprintln(m.a, \"a\" in m)",
	} {
		if _, err := checkSource(t, src); err != "" {
			t.Errorf("%q: unexpected error: %s", src, err)
		}
	}
}

func TestCheckErrors(t *testing.T) {
	tests := []struct{ src, want string }{
		{"println(veloctiy)\nvelocity = 3", "undefined variable 'veloctiy'; did you mean 'velocity'?"},
		{"prnitln(1)", "undefined function 'prnitln'; did you mean 'println'?"},
		{"x = 1\nx <- 2", "cannot update 'x': it was bound with '=' at line 1"},
		{"x = 1\nx = 2", "'x' is already defined at line 1"},
		{"f = () -> {\n y = 1\n y = 2\n}", "'y' is already defined in this block at line 2"},
		{"xs = [1]\nxs[0] <- 2", "cannot modify 'xs'"},
		{"f(a, b) = a + b\nf(1)", "'f' takes 2 arguments, but 1 was given"},
		{"sqrt(1, 2)", "'sqrt' takes 1 argument, but 2 were given"},
		{"x = \"a\" - 1", "cannot subtract a str"},
		{"x = 1 + \"a\"", "cannot add a num and a str; use an f-string"},
		{"n = 3\nn()", "cannot call 'n': it is a num"},
		{"x = #5", "'#' needs a string, list or map, not a num"},
		{"@ x in 5 { }", "cannot loop over a num"},
		{"a, b = 5", "cannot unpack a num into 2 names"},
		{"f = (x, x) -> x", "parameter 'x' appears twice"},
	}
	for _, tt := range tests {
		_, err := checkSource(t, tt.src)
		if !strings.Contains(err, tt.want) {
			t.Errorf("%q:\n got %q\nwant %q", tt.src, err, tt.want)
		}
	}
}

func TestCheckCapturesAndBoxing(t *testing.T) {
	c, err := checkSource(t, "counter = () -> {\n  n := 0\n  step = 2\n  () -> {\n    n <- n + step\n    n\n  }\n}")
	if err != "" {
		t.Fatal(err)
	}
	var inner *Fun
	for _, f := range c.Funcs {
		if strings.HasPrefix(f.Name, "lambda@") {
			inner = f
		}
	}
	if inner == nil || len(inner.Captures) != 2 {
		t.Fatalf("inner function should capture n and step, got %+v", inner)
	}
	for _, cap := range inner.Captures {
		switch cap.Name {
		case "n":
			if !cap.Outer.Boxed {
				t.Error("mutable captured n should be boxed")
			}
		case "step":
			if cap.Outer.Boxed {
				t.Error("immutable captured step should not be boxed")
			}
		}
	}
}

func TestCheckRecordsUnsupported(t *testing.T) {
	c, err := checkSource(t, "cstruct P { x: f64 }\nimport sdl3 as sdl\nsdl.SDL_Init(0)")
	if err != "" {
		t.Fatal(err)
	}
	if got := strings.Join(c.Unsupported, ","); got != "C calls,C imports,cstruct" {
		t.Errorf("unsupported = %s", got)
	}
}
