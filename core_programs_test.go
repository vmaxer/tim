package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCorePrograms compiles testdata/core/*.tim with the core code generator
// for the host, and on Linux also for the other architectures under qemu,
// and compares their output with the .out files. TIM_UPDATE=1 rewrites them.
func TestCorePrograms(t *testing.T) {
	files, _ := filepath.Glob("testdata/core/*.tim")
	if len(files) == 0 {
		t.Fatal("no programs")
	}
	host := GetDefaultPlatform()
	targets := []struct {
		p    Platform
		qemu string
	}{{host, ""}}
	if runtime.GOOS == "linux" && host.Arch == ArchX86_64 {
		for _, c := range []struct {
			arch Arch
			qemu string
		}{{ArchARM64, "qemu-aarch64"}, {ArchRiscv64, "qemu-riscv64"}} {
			if q, err := exec.LookPath(c.qemu); err == nil {
				targets = append(targets, struct {
					p    Platform
					qemu string
				}{Platform{Arch: c.arch, OS: OSLinux}, q})
			}
		}
	}
	for _, src := range files {
		name := strings.TrimSuffix(filepath.Base(src), ".tim")
		for _, tg := range targets {
			t.Run(name+"/"+tg.p.FullString(), func(t *testing.T) {
				code, err := os.ReadFile(src)
				if err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				exe := filepath.Join(dir, name)
				if tg.p.OS == OSWindows {
					exe += ".exe"
				}
				handled, err := tryCore(code, src, exe, tg.p)
				if !handled || err != nil {
					t.Fatalf("core did not compile it: handled=%v err=%v", handled, err)
				}
				var cmd *exec.Cmd
				if tg.qemu != "" {
					cmd = exec.Command(tg.qemu, exe, "a", "b")
				} else {
					cmd = exec.Command(exe, "a", "b")
				}
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "TIM_TEST=yes")
				if in, err := os.ReadFile(strings.TrimSuffix(src, ".tim") + ".in"); err == nil {
					cmd.Stdin = bytes.NewReader(in)
				}
				var stdout bytes.Buffer
				cmd.Stdout = &stdout
				err = cmd.Run()
				exit := 0
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					exit = ee.ExitCode()
				} else if err != nil {
					t.Fatal(err)
				}
				got := fmt.Sprintf("%s[exit %d]\n", stdout.String(), exit)
				golden := strings.TrimSuffix(src, ".tim") + ".out"
				if os.Getenv("TIM_UPDATE") != "" && tg.qemu == "" {
					if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				if got != string(want) {
					t.Errorf("got:\n%s\nwant:\n%s", got, want)
				}
			})
		}
	}
}
