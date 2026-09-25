package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var crossArchPrograms = []struct{ name, code, want string }{
	{"exact", `println(0.1 + 0.2 == 0.3, 0.1 + 0.2, 1 / 3 + 1 / 6, 22 / 7, 2 ** 100)
x := 9007199254740991
x <- x + 2
println(x, -(2 ** 70), floor(-7 / 2), round(5 / 2), abs(-1 / 3), 17 % 5, -7 % 3)
println(f"{1 / 3} and {2 ** 64}", 1 / 0 or! 42, 1 / 3 or! 9, 0 or! 5)
`, "1 0.3 0.5 22/7 1267650600228229401496703205376\n9007199254740993 -1180591620717411303424 -4 3 1/3 2 -1\n1/3 and 18446744073709551616 42 1/3 5\n"},
	{"functions", `fib = n -> n < 2 { 1 => n ~> fib(n - 1) + fib(n - 2) }
loop = (n, acc) -> n == 0 { 1 => acc ~> loop(n - 1, acc + 1 / 2) }
count := 0
bump = n -> {
    count <- count + n
    count
}
bump(5)
bump(7)
println(fib(20), loop(1000000, 0), count)
`, "6765 500000 12\n"},
	{"loops", `sum := 0
@ i in 0..<10 {
    sum <- sum + i * i
}
n := 0
@ n < 5 ! 100 {
    n <- n + 1
}
m := 0
@ yes ! 3 {
    m <- m + 1
}
println(sum, n, m)
`, "285 5 3\n"},
	{"lists", `xs := [1, 1 / 2, 2 ** 65] + [3]
xs[0] <- 10
ys := [0] * 3
tot := 0
@ v in xs {
    tot <- tot + v
}
println(xs, #xs, xs[9], ys, tot)
`, "[10, 0.5, 36893488147419103232, 3] 4 0 [0, 0, 0] 36893488147419103245.5\n"},
	{"strings", `s := "ab"
@ k in 1..=3 {
    s <- s + (k as string)
}
g = y -> y {
    2 ** 70 => "big"
    ~> "other"
}
println(s, #s, g(2 ** 70), g(1))
printf("%d|%.3f|%v|%s|%f|%.0f\n", -7 / 2, -1 / 3, 10 ** 20, s, 0.9999999, 2.5)
`, "ab123 5 big other\n-3|-0.333|100000000000000000000|ab123|1.000000|2\n"},
	{"bitwise", `println(12 & 10, ~ 0, 1 << 62, bit(5, 2), -8 >> 60)
`, "8 -1 4611686018427387904 1 15\n"},
}

func TestCrossArch(t *testing.T) {
	targets := []struct {
		arch   Arch
		qemu   string
		prefix string
	}{
		{ArchARM64, "qemu-aarch64", "/usr/aarch64-linux-gnu"},
		{ArchRiscv64, "qemu-riscv64", ""},
	}
	for _, target := range targets {
		t.Run(target.qemu, func(t *testing.T) {
			qemu, err := exec.LookPath(target.qemu)
			if err != nil {
				t.Skipf("%s not installed", target.qemu)
			}
			if target.prefix != "" {
				if _, err := os.Stat(target.prefix); err != nil {
					t.Skipf("%s not installed", target.prefix)
				}
			}
			for _, p := range crossArchPrograms {
				t.Run(p.name, func(t *testing.T) {
					dir := t.TempDir()
					src, exe := filepath.Join(dir, "main.tim"), filepath.Join(dir, "main")
					if err := os.WriteFile(src, []byte(p.code), 0o644); err != nil {
						t.Fatal(err)
					}
					if err := CompileTimWithOptions(src, exe, Platform{Arch: target.arch, OS: OSLinux}, 0, false, false); err != nil {
						t.Fatalf("compilation failed: %v", err)
					}
					cmd := exec.Command(qemu, exe)
					cmd.Env = append(os.Environ(), "QEMU_LD_PREFIX="+target.prefix)
					done := make(chan struct{})
					var out []byte
					go func() {
						out, err = cmd.Output()
						close(done)
					}()
					select {
					case <-done:
					case <-time.After(60 * time.Second):
						_ = cmd.Process.Kill()
						t.Fatal("timed out")
					}
					if err != nil {
						t.Fatalf("run failed: %v\n%s", err, out)
					}
					if got := string(out); got != p.want {
						t.Errorf("got:\n%s\nwant:\n%s", got, strings.TrimSuffix(p.want, "\n"))
					}
				})
			}
		})
	}
}

func TestCrossArchMatchesNative(t *testing.T) {
	if GetDefaultPlatform().Arch != ArchX86_64 {
		t.Skip("reference outputs come from x86_64")
	}
	for _, p := range crossArchPrograms {
		t.Run(p.name, func(t *testing.T) {
			if got := compileAndRunTopLevel(t, p.code); got != p.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, p.want)
			}
		})
	}
}
