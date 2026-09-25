// Completion: 100% - Instruction implementation complete
package main

import (
	"fmt"
	"os"
)

// SUB instruction implementation for all architectures
// Essential for implementing Tim's arithmetic and control flow:
//   - Arithmetic expressions: n - 1
//   - Decrement operations: me.health - amount
//   - Pointer arithmetic: end - start
//   - Loop counters: count - 1
//   - Comparisons (CMP uses SUB internally)

// SubRegFromReg generates SUB dst, src (dst = dst - src)
func (o *Out) SubRegFromReg(dst, src string) {
	if o.backend != nil {
		o.backend.SubRegToReg(dst, src)
		return
	}
	// Fallback for x86_64
	switch o.target.Arch() {
	case ArchX86_64:
		o.subX86RegFromReg(dst, src)
	}
}

// SubImmFromReg generates SUB dst, imm (dst = dst - imm)
func (o *Out) SubImmFromReg(dst string, imm int64) {
	if o.backend != nil {
		o.backend.SubImmFromReg(dst, imm)
		return
	}
	// Fallback for x86_64
	switch o.target.Arch() {
	case ArchX86_64:
		o.subX86ImmFromReg(dst, imm)
	}
}

// x86-64 SUB reg, reg
func (o *Out) subX86RegFromReg(dst, src string) {
	dstReg, dstOk := GetRegister(o.target.Arch(), dst)
	srcReg, srcOk := GetRegister(o.target.Arch(), src)
	if !dstOk || !srcOk {
		return
	}

	if VerboseMode {
		fmt.Fprintf(os.Stderr, "sub %s, %s:", dst, src)
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

	// SUB opcode (0x29 for r/m64, r64)
	o.Write(0x29)

	// ModR/M: 11 (register direct) | reg (src) | r/m (dst)
	modrm := uint8(0xC0) | ((srcReg.Encoding & 7) << 3) | (dstReg.Encoding & 7)
	o.Write(modrm)

	if VerboseMode {
		fmt.Fprintln(os.Stderr)
	}
}

// x86-64 SUB reg, imm
func (o *Out) subX86ImmFromReg(dst string, imm int64) {
	dstReg, dstOk := GetRegister(o.target.Arch(), dst)
	if !dstOk {
		return
	}

	if VerboseMode {
		fmt.Fprintf(os.Stderr, "sub %s, %d:", dst, imm)
	}

	// REX prefix for 64-bit operation
	rex := uint8(0x48)
	if (dstReg.Encoding & 8) != 0 {
		rex |= 0x01 // REX.B
	}
	o.Write(rex)

	// Check if immediate fits in 8 bits
	if imm >= -128 && imm <= 127 {
		// SUB r/m64, imm8 (opcode 0x83 /5)
		o.Write(0x83)
		modrm := uint8(0xE8) | (dstReg.Encoding & 7) // ModR/M: 11 101 reg
		o.Write(modrm)
		o.Write(uint8(imm & 0xFF))
	} else {
		// SUB r/m64, imm32 (opcode 0x81 /5)
		o.Write(0x81)
		modrm := uint8(0xE8) | (dstReg.Encoding & 7)
		o.Write(modrm)

		// Write 32-bit immediate
		imm32 := uint32(imm)
		o.Write(uint8(imm32 & 0xFF))
		o.Write(uint8((imm32 >> 8) & 0xFF))
		o.Write(uint8((imm32 >> 16) & 0xFF))
		o.Write(uint8((imm32 >> 24) & 0xFF))
	}

	if VerboseMode {
		fmt.Fprintln(os.Stderr)
	}
}
