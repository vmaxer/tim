// Completion: 100% - Instruction implementation complete
package main

import (
	"fmt"
	"os"
)

// Conditional jump instructions for all architectures
// Critical for implementing Tim language control flow:
//   - Pattern matching: n <= 1 -> 1; ~> n * me(n - 1)
//   - Error handling: x or! "error message"
//   - Guard expressions: x or return y
//   - Loop filtering: @ entity in entities{health > 0}
//   - Default patterns: ~> (catch-all)

// Condition codes for jumps
type JumpCondition int

const (
	JumpEqual          JumpCondition = iota // JE/JZ - equal/zero
	JumpNotEqual                            // JNE/JNZ - not equal/not zero
	JumpGreater                             // JG/JNLE - greater (signed)
	JumpGreaterOrEqual                      // JGE/JNL - greater or equal (signed)
	JumpLess                                // JL/JNGE - less (signed)
	JumpLessOrEqual                         // JLE/JNG - less or equal (signed)
	JumpAbove                               // JA/JNBE - above (unsigned)
	JumpAboveOrEqual                        // JAE/JNB - above or equal (unsigned)
	JumpBelow                               // JB/JNAE - below (unsigned)
	JumpBelowOrEqual                        // JBE/JNA - below or equal (unsigned)
	JumpParity                              // JP - parity/NaN
	JumpNotParity                           // JNP - not parity/not NaN
)

// JumpConditional generates a conditional jump instruction
// offset is the relative offset to jump to (signed, from the end of the instruction)
func (o *Out) JumpConditional(condition JumpCondition, offset int32) {
	if o.backend != nil {
		o.backend.JumpConditional(condition, offset)
		return
	}
	// Fallback for x86_64
	switch o.target.Arch() {
	case ArchX86_64:
		o.jmpX86Conditional(condition, offset)
	}
}

// JumpUnconditional generates an unconditional jump
func (o *Out) JumpUnconditional(offset int32) {
	if o.backend != nil {
		o.backend.JumpUnconditional(offset)
		return
	}
	// Fallback for x86_64
	switch o.target.Arch() {
	case ArchX86_64:
		o.jmpX86Unconditional(offset)
	}
}

// x86-64 conditional jump implementation
func (o *Out) jmpX86Conditional(condition JumpCondition, offset int32) {
	var opcode uint8
	var name string

	switch condition {
	case JumpEqual:
		opcode = 0x84
		name = "je"
	case JumpNotEqual:
		opcode = 0x85
		name = "jne"
	case JumpGreater:
		opcode = 0x8F
		name = "jg"
	case JumpGreaterOrEqual:
		opcode = 0x8D
		name = "jge"
	case JumpLess:
		opcode = 0x8C
		name = "jl"
	case JumpLessOrEqual:
		opcode = 0x8E
		name = "jle"
	case JumpAbove:
		opcode = 0x87
		name = "ja"
	case JumpAboveOrEqual:
		opcode = 0x83
		name = "jae"
	case JumpBelow:
		opcode = 0x82
		name = "jb"
	case JumpBelowOrEqual:
		opcode = 0x86
		name = "jbe"
	case JumpParity:
		opcode = 0x8A
		name = "jp"
	case JumpNotParity:
		opcode = 0x8B
		name = "jnp"
	default:
		return
	}

	if VerboseMode {
		fmt.Fprintf(os.Stderr, "%s %d:", name, offset)
	}

	// Use near jump (32-bit offset) with 0x0F prefix
	o.Write(0x0F)
	o.Write(opcode)

	// Write 32-bit offset (little-endian)
	o.Write(uint8(offset & 0xFF))
	o.Write(uint8((offset >> 8) & 0xFF))
	o.Write(uint8((offset >> 16) & 0xFF))
	o.Write(uint8((offset >> 24) & 0xFF))

	if VerboseMode {
		fmt.Fprintln(os.Stderr)
	}
}

// x86-64 unconditional jump
func (o *Out) jmpX86Unconditional(offset int32) {
	if VerboseMode {
		fmt.Fprintf(os.Stderr, "jmp %d:", offset)
	}

	// Use near jump (32-bit offset)
	o.Write(0xE9)

	// Write 32-bit offset (little-endian)
	o.Write(uint8(offset & 0xFF))
	o.Write(uint8((offset >> 8) & 0xFF))
	o.Write(uint8((offset >> 16) & 0xFF))
	o.Write(uint8((offset >> 24) & 0xFF))

	if VerboseMode {
		fmt.Fprintln(os.Stderr)
	}
}
