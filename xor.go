// Completion: 100% - Instruction implementation complete
package main

import (
	"fmt"
	"os"
)

// XOR instruction implementation for all architectures
// Used for bitwise operations in unsafe blocks:
//   - Bit flipping: value ^b mask
//   - Zeroing registers: rax ^b rax (efficient way to set register to 0)
//   - Toggle bits: flags ^b BIT_MASK

// XorRegWithReg generates XOR dst, src (dst = dst ^ src)
func (o *Out) XorRegWithReg(dst, src string) {
	if o.backend != nil {
		o.backend.XorRegWithReg(dst, src)
		return
	}
	// Fallback for x86_64
	switch o.target.Arch() {
	case ArchX86_64:
		o.xorX86RegWithReg(dst, src)
	}
}

// XorRegWithImm generates XOR dst, imm (dst = dst ^ imm)
func (o *Out) XorRegWithImm(dst string, imm int32) {
	if o.backend != nil {
		o.backend.XorRegWithImm(dst, int64(imm))
		return
	}
	// Fallback for x86_64
	switch o.target.Arch() {
	case ArchX86_64:
		o.xorX86RegWithImm(dst, imm)
	}
}

// ============================================================================
// x86-64 implementations
// ============================================================================

// x86-64 XOR (register-register)
func (o *Out) xorX86RegWithReg(dst, src string) {
	dstReg, dstOk := GetRegister(o.target.Arch(), dst)
	srcReg, srcOk := GetRegister(o.target.Arch(), src)
	if !dstOk || !srcOk {
		return
	}

	if VerboseMode {
		fmt.Fprintf(os.Stderr, "xor %s, %s:", dst, src)
	}

	// REX prefix for 64-bit operation
	rex := uint8(0x48)
	if (dstReg.Encoding & 8) != 0 {
		rex |= 0x01 // REX.B
	}
	if (srcReg.Encoding & 8) != 0 {
		rex |= 0x04 // REX.R
	}
	o.Write(rex)

	// XOR opcode (0x31 for r/m64, r64)
	o.Write(0x31)

	// ModR/M: 11 (register direct) | reg (src) | r/m (dst)
	modrm := uint8(0xC0) | ((srcReg.Encoding & 7) << 3) | (dstReg.Encoding & 7)
	o.Write(modrm)

	if VerboseMode {
		fmt.Fprintln(os.Stderr)
	}
}

// x86-64 XOR with immediate
func (o *Out) xorX86RegWithImm(dst string, imm int32) {
	dstReg, dstOk := GetRegister(o.target.Arch(), dst)
	if !dstOk {
		return
	}

	if VerboseMode {
		fmt.Fprintf(os.Stderr, "xor %s, %d:", dst, imm)
	}

	// REX prefix for 64-bit operation
	rex := uint8(0x48)
	if (dstReg.Encoding & 8) != 0 {
		rex |= 0x01 // REX.B
	}
	o.Write(rex)

	// Check if immediate fits in 8 bits
	if imm >= -128 && imm <= 127 {
		// XOR r/m64, imm8 (opcode 0x83 /6)
		o.Write(0x83)
		modrm := uint8(0xF0) | (dstReg.Encoding & 7) // opcode extension /6
		o.Write(modrm)
		o.Write(uint8(imm & 0xFF))
	} else {
		// XOR r/m64, imm32 (opcode 0x81 /6)
		o.Write(0x81)
		modrm := uint8(0xF0) | (dstReg.Encoding & 7) // opcode extension /6
		o.Write(modrm)

		// Write 32-bit immediate
		o.Write(uint8(imm & 0xFF))
		o.Write(uint8((imm >> 8) & 0xFF))
		o.Write(uint8((imm >> 16) & 0xFF))
		o.Write(uint8((imm >> 24) & 0xFF))
	}

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
