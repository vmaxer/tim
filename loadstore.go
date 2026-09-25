// Completion: 100% - Module complete
package main

import (
	"fmt"
	"os"
)

// Load/Store instructions for memory access
// Essential for implementing Tim's variable and data access:
//   - Variable access: me.health, me.x
//   - Array element access: entities[i]
//   - Map value access: map[key]
//   - Struct field access: player.position.x
//   - Stack variable access: local_var
//   - Global variable access: game_state

// StoreRegToMem stores a register value to memory
// [base + offset] = src
func (o *Out) StoreRegToMem(src, base string, offset int32) {
	switch o.target.Arch() {
	case ArchX86_64:
		o.storeX86RegToMem(src, base, offset)
	case ArchARM64:
		o.storeARM64RegToMem(src, base, offset)
	case ArchRiscv64:
		o.storeRISCVRegToMem(src, base, offset)
	}
}

// ============================================================================
// x86-64 implementations
// ============================================================================

// x86-64 MOV [reg + offset], reg (store)
func (o *Out) storeX86RegToMem(src, base string, offset int32) {
	srcReg, srcOk := GetRegister(o.target.Arch(), src)
	baseReg, baseOk := GetRegister(o.target.Arch(), base)
	if !srcOk || !baseOk {
		return
	}

	if VerboseMode {
		fmt.Fprintf(os.Stderr, "mov [%s + %d], %s:", base, offset, src)
	}

	// REX prefix for 64-bit operation
	rex := uint8(0x48)
	if (srcReg.Encoding & 8) != 0 {
		rex |= 0x04 // REX.R
	}
	if (baseReg.Encoding & 8) != 0 {
		rex |= 0x01 // REX.B
	}
	o.Write(rex)

	// MOV r/m64, r64 (opcode 0x89)
	o.Write(0x89)

	// Determine ModR/M and displacement size (same logic as load)
	if offset == 0 && (baseReg.Encoding&7) != 5 {
		modrm := uint8(0x00) | ((srcReg.Encoding & 7) << 3) | (baseReg.Encoding & 7)
		o.Write(modrm)

		if (baseReg.Encoding & 7) == 4 {
			sib := uint8(0x24) | (baseReg.Encoding & 7)
			o.Write(sib)
		}
	} else if offset >= -128 && offset <= 127 {
		modrm := uint8(0x40) | ((srcReg.Encoding & 7) << 3) | (baseReg.Encoding & 7)
		o.Write(modrm)

		if (baseReg.Encoding & 7) == 4 {
			sib := uint8(0x24) | (baseReg.Encoding & 7)
			o.Write(sib)
		}

		o.Write(uint8(offset & 0xFF))
	} else {
		modrm := uint8(0x80) | ((srcReg.Encoding & 7) << 3) | (baseReg.Encoding & 7)
		o.Write(modrm)

		if (baseReg.Encoding & 7) == 4 {
			sib := uint8(0x24) | (baseReg.Encoding & 7)
			o.Write(sib)
		}

		o.Write(uint8(offset & 0xFF))
		o.Write(uint8((offset >> 8) & 0xFF))
		o.Write(uint8((offset >> 16) & 0xFF))
		o.Write(uint8((offset >> 24) & 0xFF))
	}

	if VerboseMode {
		fmt.Fprintln(os.Stderr)
	}
}

// ============================================================================
// ARM64 implementations
// ============================================================================

// ARM64 STR Xt, [Xn, #offset] (store)
func (o *Out) storeARM64RegToMem(src, base string, offset int32) {
	srcReg, srcOk := GetRegister(o.target.Arch(), src)
	baseReg, baseOk := GetRegister(o.target.Arch(), base)
	if !srcOk || !baseOk {
		return
	}

	if VerboseMode {
		fmt.Fprintf(os.Stderr, "str %s, [%s, #%d]:", src, base, offset)
	}

	// STR Xt, [Xn, #offset]
	// Format: 11 111 0 01 00 imm12 Rn Rt

	if offset >= 0 && offset <= 32760 && (offset%8) == 0 {
		// Use STR with unsigned offset
		imm12 := uint32(offset / 8)
		instr := uint32(0xF9000000) |
			(imm12 << 10) | // imm12
			(uint32(baseReg.Encoding&31) << 5) | // Rn (base)
			uint32(srcReg.Encoding&31) // Rt (source)

		o.Write(uint8(instr & 0xFF))
		o.Write(uint8((instr >> 8) & 0xFF))
		o.Write(uint8((instr >> 16) & 0xFF))
		o.Write(uint8((instr >> 24) & 0xFF))
	} else if offset >= -256 && offset <= 255 {
		// Use STUR with signed offset (unscaled)
		// Format: 11 111 0 00 00 0 imm9 00 Rn Rt
		imm9 := uint32(offset & 0x1FF)
		instr := uint32(0xF8000000) |
			(imm9 << 12) | // imm9
			(uint32(baseReg.Encoding&31) << 5) | // Rn (base)
			uint32(srcReg.Encoding&31) // Rt (source)

		o.Write(uint8(instr & 0xFF))
		o.Write(uint8((instr >> 8) & 0xFF))
		o.Write(uint8((instr >> 16) & 0xFF))
		o.Write(uint8((instr >> 24) & 0xFF))
	} else {
		if VerboseMode {
			fmt.Fprintf(os.Stderr, " (offset out of range)")
		}
	}

	if VerboseMode {
		fmt.Fprintln(os.Stderr)
	}
}

// ============================================================================
// RISC-V implementations
// ============================================================================

// RISC-V SD rs2, offset(rs1) (store)
func (o *Out) storeRISCVRegToMem(src, base string, offset int32) {
	srcReg, srcOk := GetRegister(o.target.Arch(), src)
	baseReg, baseOk := GetRegister(o.target.Arch(), base)
	if !srcOk || !baseOk {
		return
	}

	if VerboseMode {
		fmt.Fprintf(os.Stderr, "sd %s, %d(%s):", src, offset, base)
	}

	// SD: imm[11:5] rs2 rs1 011 imm[4:0] 0100011
	// 12-bit signed immediate split into two fields
	if offset < -2048 || offset > 2047 {
		if VerboseMode {
			fmt.Fprintf(os.Stderr, " (offset out of range)")
		}
		if VerboseMode {
			fmt.Fprintln(os.Stderr)
		}
		return
	}

	imm11_5 := uint32((offset >> 5) & 0x7F)
	imm4_0 := uint32(offset & 0x1F)

	instr := uint32(0x23) |
		(3 << 12) | // funct3 = 011 (SD)
		(imm11_5 << 25) | // imm[11:5]
		(uint32(srcReg.Encoding&31) << 20) | // rs2 (source)
		(uint32(baseReg.Encoding&31) << 15) | // rs1 (base)
		(imm4_0 << 7) // imm[4:0]

	o.Write(uint8(instr & 0xFF))
	o.Write(uint8((instr >> 8) & 0xFF))
	o.Write(uint8((instr >> 16) & 0xFF))
	o.Write(uint8((instr >> 24) & 0xFF))

	if VerboseMode {
		fmt.Fprintln(os.Stderr)
	}
}
