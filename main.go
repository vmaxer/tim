// Tim compiles Tim programs to executables for Linux, Windows and macOS.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

const versionString = "tim 2.0.0"

type Arch int

const (
	ArchUnknown Arch = iota
	ArchX86_64
	ArchARM64
	ArchRiscv64
)

func (a Arch) String() string {
	switch a {
	case ArchX86_64:
		return "x86_64"
	case ArchARM64:
		return "aarch64"
	case ArchRiscv64:
		return "riscv64"
	}
	return "unknown"
}

// ParseArch accepts GOARCH names and their common aliases.
func ParseArch(s string) (Arch, error) {
	switch strings.ToLower(s) {
	case "x86_64", "amd64", "x86-64":
		return ArchX86_64, nil
	case "aarch64", "arm64":
		return ArchARM64, nil
	case "riscv64", "riscv", "rv64":
		return ArchRiscv64, nil
	}
	return 0, fmt.Errorf("unsupported architecture %q (amd64, arm64, riscv64)", s)
}

type OS int

const (
	OSLinux OS = iota
	OSDarwin
	OSWindows
)

func (o OS) String() string {
	switch o {
	case OSLinux:
		return "linux"
	case OSDarwin:
		return "darwin"
	case OSWindows:
		return "windows"
	}
	return "unknown"
}

// ParseOS accepts GOOS names and their common aliases.
func ParseOS(s string) (OS, error) {
	switch strings.ToLower(s) {
	case "linux":
		return OSLinux, nil
	case "darwin", "macos":
		return OSDarwin, nil
	case "windows", "win":
		return OSWindows, nil
	}
	return 0, fmt.Errorf("unsupported OS %q (linux, darwin, windows)", s)
}

type Platform struct {
	Arch Arch
	OS   OS
}

// FullString returns the platform as arch-os, like arm64-darwin.
func (p Platform) FullString() string {
	arch := map[Arch]string{ArchX86_64: "amd64", ArchARM64: "arm64", ArchRiscv64: "riscv64"}[p.Arch]
	return arch + "-" + p.OS.String()
}

// GetDefaultPlatform returns the platform tim runs on.
func GetDefaultPlatform() Platform {
	p := Platform{Arch: ArchX86_64, OS: OSLinux}
	if a, err := ParseArch(runtime.GOARCH); err == nil {
		p.Arch = a
	}
	if o, err := ParseOS(runtime.GOOS); err == nil {
		p.OS = o
	}
	return p
}

// VerboseMode makes the compiler explain what it does on stderr.
var VerboseMode bool

func main() {
	if err := runCLI(os.Args[1:]); err != nil {
		if !errors.Is(err, ErrAlreadyReported) {
			fmt.Fprintf(os.Stderr, "tim: %v\n", err)
		}
		os.Exit(1)
	}
}
