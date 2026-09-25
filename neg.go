// Completion: 100% - Instruction implementation complete
package main

import (
	"fmt"
	"os"
)

// NEG instruction for arithmetic negation (two's complement)
// Essential for implementing Tim's negation operations:
//   - Unary minus: -x, -value
//   - Direction reversal: -velocity
//   - Sign flipping: -balance
//   - Opposite values: -delta
//   - Negating results: -(a + b)

// NegReg generates NEG dst (dst = -dst)
func (o *Out) NegReg(dst string) {
	if o.backend != nil {
		o.backend.NegReg(dst)
		return
	}
	// Fallback for x86_64 (uses methods in this file)
	switch o.target.Arch() {
	case ArchX86_64:
		o.negX86Reg(dst)
	}
}

// ============================================================================
// x86-64 implementations
// ============================================================================

// x86-64 NEG (two's complement negation)
func (o *Out) negX86Reg(dst string) {
	dstReg, dstOk := GetRegister(o.target.Arch(), dst)
	if !dstOk {
		return
	}

	if VerboseMode {
		fmt.Fprintf(os.Stderr, "neg %s:", dst)
	}

	// REX prefix for 64-bit operation
	rex := uint8(0x48)
	if (dstReg.Encoding & 8) != 0 {
		rex |= 0x01 // REX.B
	}
	o.Write(rex)

	// NEG r/m64 (opcode 0xF7 /3)
	o.Write(0xF7)

	// ModR/M: 11 011 reg (register direct, opcode extension /3 for NEG)
	modrm := uint8(0xD8) | (dstReg.Encoding & 7)
	o.Write(modrm)

	if VerboseMode {
		fmt.Fprintln(os.Stderr)
	}
}

// ============================================================================
// ARM64 implementations
// ============================================================================

// ============================================================================
// RISC-V implementations
// ============================================================================
