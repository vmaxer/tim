// codegen_guards.go - Runtime guards compiled into generated code
package main

import (
	"fmt"
)

// GuardConfig controls which runtime guards are enabled
type GuardConfig struct {
	NullPointerChecks    bool // Insert null checks before pointer dereferences
	StackAlignmentChecks bool // Check stack alignment before calls
	BoundsChecks         bool // Check array bounds (when we add arrays)
}

var DefaultGuardConfig = GuardConfig{
	NullPointerChecks:    false, // Disabled for now - too aggressive
	StackAlignmentChecks: false,
	BoundsChecks:         false, // Not implemented yet
}

// EmitNullCheck generates code to check if a register is null and trap if so
// This is compiled INTO the binary for runtime checking
func (fc *TimCompiler) EmitNullCheck(reg, context string) {
	if !DefaultGuardConfig.NullPointerChecks {
		return
	}

	if fc.eb.target.OS() == OSWindows {
		// Windows: just test and continue (no good way to print error)
		return
	}

	// Linux: test register and exit with error message if null
	fc.out.TestRegReg(reg, reg)
	jmpNotNull := fc.eb.text.Len()
	fc.out.JumpConditional(JumpNotEqual, 0) // jne not_null

	// Register is null - print error and exit
	fc.out.MovImmToReg("rdi", "2") // stderr

	// Write error message on stack
	errorMsg := fmt.Sprintf("NULL POINTER: %s in %s\n", reg, context)
	msgLen := len(errorMsg)
	stackSpace := int64(((msgLen + 15) / 16) * 16) // Align to 16 bytes

	fc.out.SubImmFromReg("rsp", stackSpace)
	for i, ch := range errorMsg {
		fc.out.MovImmToMem(int64(ch), "rsp", i)
	}

	fc.out.MovRegToReg("rsi", "rsp")
	fc.out.MovImmToReg("rdx", fmt.Sprintf("%d", msgLen))
	fc.out.MovImmToReg("rax", "1") // write syscall
	fc.out.Syscall()

	// Exit with code 1
	fc.out.MovImmToReg("rdi", "1")
	fc.out.MovImmToReg("rax", "60") // exit syscall
	fc.out.Syscall()

	// not_null:
	notNullPos := fc.eb.text.Len()
	fc.patchJumpImmediate(jmpNotNull+2, int32(notNullPos-(jmpNotNull+6)))

	if VerboseMode {
		debugf("DEBUG: Inserted null check for %s in %s\n", reg, context)
	}
}
