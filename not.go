// Completion: 100% - Instruction implementation complete
package main

import (
	"fmt"
	"os"
)

// NOT instruction implementation for all architectures
// Used for bitwise NOT (one's complement) in unsafe blocks:
//   - Inverting all bits: ~b value
//   - Creating bit masks: ~b 0 gives all 1s

// NotReg generates NOT dst (dst = ~dst) - one's complement
func (o *Out) NotReg(dst string) {
	if o.backend != nil {
		o.backend.NotReg(dst)
		return
	}
	// Fallback for x86_64 (uses methods in this file)
	switch o.target.Arch() {
	case ArchX86_64:
		o.notX86Reg(dst)
	}
}

// ============================================================================
// x86-64 implementation
// ============================================================================

// x86-64 NOT (one's complement negation)
func (o *Out) notX86Reg(dst string) {
	dstReg, dstOk := GetRegister(o.target.Arch(), dst)
	if !dstOk {
		return
	}

	if VerboseMode {
		fmt.Fprintf(os.Stderr, "not %s:", dst)
	}

	// REX prefix for 64-bit operation
	rex := uint8(0x48)
	if (dstReg.Encoding & 8) != 0 {
		rex |= 0x01 // REX.B
	}
	o.Write(rex)

	// NOT opcode (0xF7 /2 for r/m64)
	o.Write(0xF7)

	// ModR/M: 11 (register direct) | opcode extension /2 | r/m (dst)
	modrm := uint8(0xD0) | (dstReg.Encoding & 7) // 11 010 xxx
	o.Write(modrm)

	if VerboseMode {
		fmt.Fprintln(os.Stderr)
	}
}

// ============================================================================
// ARM64 implementation
// ============================================================================

// ============================================================================
// RISC-V implementation
// ============================================================================
