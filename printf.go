// Printf runtime implementation for Tim
//
// STATUS: The functions in this file are stubs for a syscall-based printf implementation.
// Currently, printf is implemented inline in codegen.go by calling libc's printf function.
// This works correctly but depends on libc and generates PLT entry warnings.
//
// FUTURE: Implement a pure syscall-based printf similar to asm/printf.asm which:
//   - Parses format strings at runtime
//   - Converts arguments to strings using inline assembly
//   - Writes output using write(1, buf, len) syscall
//   - Eliminates dependency on libc's printf
//
// The assembly reference implementation in asm/printf.asm demonstrates the correct approach.
package main

// PrintfRuntime generates the printf runtime function for all architectures.
// NOTE: These functions are currently stubs and not used by the compiler.
// See codegen.go case "printf" for the actual implementation.
//
// Planned function signature for syscall-based implementation:
//   _tim_printf(format_str_ptr, arg1, arg2, arg3, ...)
//
// Calling convention (x86-64 System V ABI):
//   rdi = format string pointer (C-style null-terminated)
//   rsi,rdx,rcx,r8,r9 = integer arguments
//   xmm0-xmm7 = float arguments
//   Preserves: rbx, rbp, r12-r15
//   Clobbers: rax, rcx, rdx, rsi, rdi, r8-r11, xmm0-xmm15
//
// Approach (from asm/printf.asm):
//   1. Parse format string character by character
//   2. For '%' specifiers, convert corresponding argument to string
//   3. Write output using syscall write(1, buf, len)
//   4. Return total bytes written

type PrintfBackend interface {
	// EmitPrintfRuntime generates the complete printf function
	EmitPrintfRuntime() error

	// Helper methods for each architecture to implement
	emitPrintfPrologue()
	emitParseFormatString()
	emitConvertArg(specifier rune)
	emitWriteOutput()
	emitPrintfEpilogue()
}

// X86_64PrintfBackend implements printf for x86-64
type X86_64PrintfBackend struct {
	eb  *ExecutableBuilder
	out *Out
}

// ARM64PrintfBackend implements printf for ARM64
type ARM64PrintfBackend struct {
	eb  *ExecutableBuilder
	out *Out
}

// RISCV64PrintfBackend implements printf for RISC-V 64
type RISCV64PrintfBackend struct {
	eb  *ExecutableBuilder
	out *Out
}
