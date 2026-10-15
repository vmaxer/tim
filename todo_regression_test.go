package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFunctionValues(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"lambdas_in_list_and_map", "xs = [z -> z + 1, z -> z * 10]\nm = {a: z -> z + 1, b: z -> z * 10}\nfa = m.a\nfb = m.b\nprintln(xs[0](5), xs[1](5), fa(5), fb(5))\n", "6 50 6 50\n"},
		{"zero_argument_indirect_calls", "f = { println(\"x\") }\nlst = [f]\nlst[0]()\nm = {fn: f}\ng = m.fn\ng()\nh = -> 42\nk = h\nprintln(k())\nv = 5\nprintln(v())\n", "x\nx\n42\n5\n"},
		{"short_circuit", "w = n -> {\n    println(\"called\")\n    n\n}\nprintln(0 == 0 or w(0))\nprintln(0 == 1 and w(0))\nxs := []\nprintln(#xs > 0 and xs[0] == 1)\n", "1\n0\n0\n"},
		{"runtime_string_keys", "m := {a: 1}\nput = (k, v) -> { m[k] <- v }\nput(\"b\", 2)\nput(\"c\" + \"d\", 3)\nn := {}\nfor i in 0..<2 { n[f\"k{i}\"] <- i }\nprintln(m, n)\n", "{a: 1, b: 2, cd: 3} {k0: 0, k1: 1}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if out, _ := runTimSource(t, tt.name, tt.src); out != tt.want {
				t.Errorf("got %q, want %q", out, tt.want)
			}
		})
	}
}

func TestFileBuiltins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	src := "p = \"" + path + "\"\nprintln(write_file(p, \"a\\nblåbær\\n\"))\ns = read_file(p)\nprint(s)\nprintln(#s)\nm := {}\nm[read_file(p)] <- 1\nprintln(#m)\nprintln(read_file(\"/nonexistent/x\").error, write_file(\"/nonexistent/x\", \"y\").error)\n"
	if out, _ := runTimSource(t, "files", src); out != "11\na\nblåbær\n9\n1\nio io\n" {
		t.Errorf("got %q", out)
	}
}

func TestCLibraryCalls(t *testing.T) {
	lines := filepath.Join(t.TempDir(), "lines.txt")
	if err := os.WriteFile(lines, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, src, want string }{
		{"strtod", "println(c.strtod(\"-3.25\", 0), c.atof(\"-3.25\"), c.strtol(\"ff\", 0, 16))\n", "-3.25 -3.25 255\n"},
		{"narrow_int_results", "println(c.strcmp(\"a\", \"b\"), c.isalpha(97) > 0, c.isdigit(97), c.toupper(97))\n", "-1 1 0 65\n"},
		{"fgets_loop", "f := c.fopen(\"" + lines + "\", \"r\")\nbuf := c.malloc(64)\nn := 0\nfor c.fgets(buf, 64, f) != 0 ! 10 {\n    n += 1\n    print(str(buf as cstr))\n}\nc.fclose(f)\nprintln(n)\n", "one\ntwo\n2\n"},
		{"c_string_results", "p := c.getenv(\"TIM_TODO_TEST\")\nprintln(p, #p, str(p as cstr))\nprintln(c.getenv(\"TIM_NO_SUCH_VARIABLE\") or! \"unset\")\n", "abc 3 abc\nunset\n"},
		{"fopen_fseek_fclose", "f := c.fopen(\"" + lines + "\", \"r\")\nc.fseek(f, 0, 2)\nprintln(c.ftell(f))\nc.fclose(f)\n", "8\n"},
		{"many_imports", "println(c.abs(-1), c.labs(-2), c.toupper(97), c.tolower(65), c.atoi(\"4\"), c.atol(\"5\"), c.atof(\"6\"), c.strtod(\"7\", 0), c.strtol(\"8\", 0, 10), c.strlen(\"abc\"))\n" +
			"println(c.strcmp(\"a\", \"a\"), c.sqrt(4), c.sin(0), c.cos(0), c.tan(0), c.exp(0), c.log(1), c.floor(1.5), c.ceil(1.5), c.fabs(-1), c.pow(2, 2))\n" +
			"println(c.isalpha(97) > 0, c.isdigit(48) > 0, c.isspace(32) > 0, c.isupper(65) > 0, c.islower(97) > 0, c.atan(0), c.asin(0), c.acos(1), c.sinh(0), c.cosh(0), c.tanh(0), c.log10(10), c.cbrt(8), c.hypot(3, 4))\n",
			"1 2 65 97 4 5 6 7 8 3\n0 2 0 1 0 1 0 1 2 1 4\n1 1 1 1 1 0 0 0 0 1 0 1 2 5\n"},
	}
	t.Setenv("TIM_TODO_TEST", "abc")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if out, _ := runTimSource(t, tt.name, tt.src); out != tt.want {
				t.Errorf("got %q, want %q", out, tt.want)
			}
		})
	}
}

func TestStderrPrinting(t *testing.T) {
	dir := t.TempDir()
	src, exe := filepath.Join(dir, "e.tim"), filepath.Join(dir, "e")
	code := "a = \"hi\"\nprintln(\"out1\")\neprintln(\"a\", a, [1, 2], 1 / 3)\neprint(\"x\", 5)\neprintln()\neprintf(\"n=%d s=%s\\n\", 42, a)\nprintln(\"out2\")\n"
	if err := os.WriteFile(src, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CompileTim(src, exe, testPlatform()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stdout, stderr, _ := runCommandSeparate(testCommand(ctx, exe))
	if stdout != "out1\nout2\n" || stderr != "a hi [1, 2] 1/3\nx 5\nn=42 s=hi\n" {
		t.Errorf("stdout %q, stderr %q", stdout, stderr)
	}
	if strings.Contains(stdout, "hi") {
		t.Error("stderr output reached stdout")
	}
}
