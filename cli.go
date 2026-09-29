package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const usage = `tim - the Tim compiler

usage:
    tim [build] [flags] file.tim|dir   compile to an executable
    tim run [flags] file.tim [args]    compile and run, passing args
    tim test [flags] [dir]             run the test functions of *_test.tim and test_*.tim
    tim -c 'code' [args]               compile and run code
    tim version | help

flags:
    -o file            the executable (default: the source name, .exe for Windows)
    --target arch-os   amd64-linux arm64-linux riscv64-linux amd64-windows
                       arm64-windows arm64-darwin (default: this machine)
    --arch, --os       the target one part at a time
    -w, --watch        with run: rerun when a .tim file in the directory changes
    -d                 list the program's imports instead of compiling
    -v                 verbose

A .tim file that starts with #!/usr/bin/env tim runs as a script.
The language: GRAMMAR.md and STDLIB.md.
`

type command struct {
	name     string
	platform Platform
	out      string
	code     string
	watch    bool
	deps     bool
	source   string
	args     []string
}

func runCLI(argv []string) error {
	c, err := parseCommand(argv)
	if err != nil {
		return err
	}
	switch c.name {
	case "help":
		fmt.Print(usage)
		return nil
	case "version":
		fmt.Println(versionString)
		return nil
	case "test":
		return c.test()
	}
	if c.code != "" {
		dir, err := os.MkdirTemp("", "tim")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		c.source = filepath.Join(dir, "main.tim")
		if err := os.WriteFile(c.source, []byte(c.code), 0o644); err != nil {
			return err
		}
		c.name = "run"
	}
	if c.source == "" {
		if _, err := os.Stat("."); err == nil {
			c.source = "."
		}
	}
	src, err := mainSource(c.source)
	if err != nil {
		return err
	}
	if c.deps {
		imps, err := imports(src)
		for _, i := range imps {
			fmt.Println(i)
		}
		return err
	}
	if c.name == "run" {
		return c.run(src)
	}
	out := c.out
	if out == "" {
		out = strings.TrimSuffix(filepath.Base(src), ".tim")
		if c.platform.OS == OSWindows {
			out += ".exe"
		}
	}
	return CompileTim(src, out, c.platform)
}

// parseCommand reads the command, its flags wherever they appear before the
// program's own arguments, and the source.
func parseCommand(argv []string) (*command, error) {
	c := &command{name: "build", platform: GetDefaultPlatform()}
	if len(argv) == 0 {
		argv = []string{"help"}
	}
	switch argv[0] {
	case "build", "run", "test", "help", "version":
		c.name, argv = argv[0], argv[1:]
	case "-h", "--help":
		c.name = "help"
		return c, nil
	case "-V", "--version":
		c.name = "version"
		return c, nil
	}
	var target, arch, osName string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		value := func() (string, error) {
			if k := strings.IndexByte(a, '='); k > 0 {
				return a[k+1:], nil
			}
			if i+1 == len(argv) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return argv[i], nil
		}
		var err error
		switch name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "="); {
		case !strings.HasPrefix(a, "-") || a == "-":
			if c.source != "" || c.code != "" {
				return nil, fmt.Errorf("give one program, not %s and %s", c.source, a)
			}
			c.source = a
			// the program's arguments follow its source
			if c.name == "run" || c.code != "" {
				c.args = append(c.args, argv[i+1:]...)
				i = len(argv)
			}
		case name == "o" || name == "output":
			c.out, err = value()
		case name == "target":
			target, err = value()
		case name == "arch":
			arch, err = value()
		case name == "os":
			osName, err = value()
		case name == "c":
			c.code, err = value()
			c.args = append(c.args, argv[i+1:]...)
			i = len(argv)
		case name == "w" || name == "watch":
			c.watch = true
		case name == "d":
			c.deps = true
		case name == "v" || name == "verbose":
			VerboseMode = true
		default:
			return nil, fmt.Errorf("unknown flag %s (see tim help)", a)
		}
		if err != nil {
			return nil, err
		}
	}
	if target != "" {
		a, o, ok := strings.Cut(target, "-")
		if !ok {
			return nil, fmt.Errorf("--target %s: want arch-os, like arm64-linux", target)
		}
		arch, osName = a, o
	}
	if arch != "" {
		a, err := ParseArch(arch)
		if err != nil {
			return nil, err
		}
		c.platform.Arch = a
	}
	if osName != "" {
		o, err := ParseOS(osName)
		if err != nil {
			return nil, err
		}
		c.platform.OS = o
	} else if strings.HasSuffix(strings.ToLower(c.out), ".exe") {
		c.platform.OS = OSWindows
	}
	return c, nil
}

// mainSource returns the file to compile for a file or directory argument:
// a directory's only non-test .tim file, or its main.tim.
func mainSource(path string) (string, error) {
	if path == "" {
		return "", errors.New("no program given (see tim help)")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return path, nil
	}
	files, _ := filepath.Glob(filepath.Join(path, "*.tim"))
	files = slices.DeleteFunc(files, isTestFile)
	switch {
	case len(files) == 1:
		return files[0], nil
	case slices.Contains(files, filepath.Join(path, "main.tim")):
		return filepath.Join(path, "main.tim"), nil
	case len(files) == 0:
		return "", fmt.Errorf("%s has no .tim files", path)
	}
	return "", fmt.Errorf("%s has several .tim files and no main.tim", path)
}

func isTestFile(path string) bool {
	b := filepath.Base(path)
	return strings.HasPrefix(b, "test_") || strings.HasSuffix(b, "_test.tim")
}

// run compiles src to a temporary executable and runs it with the program's
// arguments. It exits with the program's exit code.
func (c *command) run(src string) error {
	dir, err := os.MkdirTemp("", "tim")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	exe := filepath.Join(dir, strings.TrimSuffix(filepath.Base(src), ".tim"))
	if c.platform.OS == OSWindows {
		exe += ".exe"
	}
	start := func() (*exec.Cmd, error) {
		if err := CompileTim(src, exe, c.platform); err != nil {
			return nil, err
		}
		cmd := exec.Command(exe, c.args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd, cmd.Start()
	}
	if !c.watch {
		cmd, err := start()
		if err != nil {
			return err
		}
		return exitWith(cmd.Wait())
	}
	return watch(filepath.Dir(src), func() func() {
		cmd, err := start()
		if err != nil {
			if !errors.Is(err, ErrAlreadyReported) {
				fmt.Fprintf(os.Stderr, "tim: %v\n", err)
			}
			return func() {}
		}
		done := make(chan struct{})
		go func() {
			cmd.Wait()
			close(done)
		}()
		return func() {
			cmd.Process.Kill()
			<-done
		}
	})
}

// watch calls start, and again, after calling the stop it returned, each
// time a .tim file in dir changes.
func watch(dir string, start func() (stop func())) error {
	stamp := func() string {
		var b strings.Builder
		files, _ := filepath.Glob(filepath.Join(dir, "*.tim"))
		for _, f := range files {
			if info, err := os.Stat(f); err == nil {
				fmt.Fprintf(&b, "%s %d %d\n", f, info.Size(), info.ModTime().UnixNano())
			}
		}
		return b.String()
	}
	last := stamp()
	stop := start()
	for {
		time.Sleep(250 * time.Millisecond)
		if now := stamp(); now != last {
			last = now
			stop()
			fmt.Fprintf(os.Stderr, "tim: %s changed, restarting\n", dir)
			stop = start()
		}
	}
}

func exitWith(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode())
	}
	return err
}

// test compiles each test file with a runner that calls its functions named
// test*, and reports those that fail: that return an error value, exit with
// a nonzero code or stop at a runtime error.
func (c *command) test() error {
	dir := c.source
	if dir == "" {
		dir = "."
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.tim"))
	files = slices.DeleteFunc(files, func(f string) bool { return !isTestFile(f) })
	if len(files) == 0 {
		fmt.Printf("no test files in %s\n", dir)
		return nil
	}
	tmp, err := os.MkdirTemp("", "tim")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	failed := 0
	for _, f := range files {
		ok, err := c.testFile(f, tmp)
		if err != nil {
			if !errors.Is(err, ErrAlreadyReported) {
				fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			}
			ok = false
		}
		status := "ok  "
		if !ok {
			status = "FAIL"
			failed++
		}
		fmt.Printf("%s %s\n", status, f)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d test files failed", failed, len(files))
	}
	return nil
}

func (c *command) testFile(path, tmp string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	prog := parseQuietly(string(src), path)
	if prog == nil {
		return false, CompileTim(path, filepath.Join(tmp, "t"), c.platform)
	}
	var runner strings.Builder
	runner.WriteString("\n__failed := 0\n")
	for _, s := range prog.Statements {
		if a, ok := s.(*AssignStmt); ok && strings.HasPrefix(strings.ToLower(a.Name), "test") {
			if _, fn := a.Value.(*LambdaExpr); fn {
				fmt.Fprintf(&runner, "type(%s()) == \"error\" {\n eprintln(\"--- FAIL: %s\")\n __failed += 1\n}\n", a.Name, a.Name)
			}
		}
	}
	runner.WriteString("exit(__failed > 0)\n")
	exe := filepath.Join(tmp, strings.TrimSuffix(filepath.Base(path), ".tim"))
	if c.platform.OS == OSWindows {
		exe += ".exe"
	}
	abs, _ := filepath.Abs(path)
	if err := compileSource(append(src, runner.String()...), abs, exe, c.platform); err != nil {
		return false, err
	}
	cmd := exec.Command(exe)
	cmd.Dir = filepath.Dir(path)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run() == nil, nil
}
