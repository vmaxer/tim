// Completion: 100% - Utility module complete
package main

// Architecture defines the interface for different CPU architectures
type Architecture interface {
	// Instruction generation
	MovImmediate(w Writer, dest, val string) error
	Syscall(w Writer) error

	// Register validation
	IsValidRegister(reg string) bool

	// ELF header information
	ELFMachineType() uint16

	// Architecture identification
	Name() string
}

// X86_64 implements Architecture for x86_64
type X86_64 struct{}

// ARM64 implements Architecture for aarch64
type ARM64 struct{}

// Riscv64 implements Architecture for riscv64
type Riscv64 struct{}
