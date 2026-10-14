package main

// Benchmark runner: compiles each benchmark in Tim, C and Go, runs it several
// times, and reports the median wall time. Run with `make bench`.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

type bench struct {
	name string
	id   string
}

const runs = 5

func main() {
	root, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	dir := filepath.Join(root, "benchmarks")
	prog := filepath.Join(dir, "programs")
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		panic(err)
	}
	timc := filepath.Join(out, "timc")
	if err := run("", "go", "build", "-o", timc, "."); err != nil {
		fmt.Println("building the compiler:", err)
		os.Exit(1)
	}

	benches := []bench{
		{"fib (recursion, fib(27))", "fib"},
		{"loops (nested sums to 1e6)", "loops"},
		{"listops (build + sum 5000)", "listops"},
	}

	fmt.Printf("%-32s %12s %12s %12s\n", "benchmark", "tim", "c (-O2)", "go")
	fmt.Printf("%-32s %12s %12s %12s\n", "---------", "---", "-------", "--")
	for _, b := range benches {
		timBin := filepath.Join(out, b.id+".tim.bin")
		cBin := filepath.Join(out, b.id+".c.bin")
		goBin := filepath.Join(out, b.id+".go.bin")
		tim, err1 := timeIt(build(timc, filepath.Join(prog, b.id+".tim"), "-o", timBin))
		c, err2 := timeIt(build("gcc", "-O2", "-o", cBin, filepath.Join(prog, b.id+".c")))
		tmp, err := os.MkdirTemp("", "timbench")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(tmp)
		goSrc := filepath.Join(tmp, b.id+".go")
		copyFile(filepath.Join(prog, b.id+".go.txt"), goSrc)
		g, err3 := timeIt(build("go", "build", "-o", goBin, goSrc))
		fmt.Printf("%-32s %12s %12s %12s\n", b.name, show(tim, err1), show(c, err2), show(g, err3))
	}
}

func copyFile(src, dst string) {
	in, err := os.ReadFile(src)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(dst, in, 0o644); err != nil {
		panic(err)
	}
}

func build(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v: %s", err, out)
	}
	for i, a := range args {
		if a == "-o" && i+1 < len(args) {
			return args[i+1], nil
		}
	}
	return args[len(args)-1], nil
}

func run(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

func timeIt(bin string, err error) (time.Duration, error) {
	if err != nil {
		return 0, err
	}
	var times []time.Duration
	for range runs {
		start := time.Now()
		if err := run("", bin); err != nil {
			return 0, err
		}
		times = append(times, time.Since(start))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2], nil
}

func show(d time.Duration, err error) string {
	if err != nil {
		return "ERR"
	}
	return d.Round(time.Millisecond).String()
}
