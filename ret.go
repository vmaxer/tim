// Completion: 100% - Instruction implementation complete
package main

import (
	"fmt"
	"os"
)

// RET instruction for returning from functions
// Essential for implementing Tim's function returns:
//   - Normal returns: return expression
//   - Early returns: x or return y
//   - Guard returns: me.running or return "game stopped"
//   - Error returns: or! "error message"
//   - Implicit returns from pattern matching

// Ret generates a return instruction
func (o *Out) Ret() {
	if o.backend != nil {
		o.backend.Ret()
		return
	}
	// Fallback for x86_64 (uses methods in this file)
	switch o.target.Arch() {
	case ArchX86_64:
		o.retX86()
	}
}

// x86-64 RET (near return)
func (o *Out) retX86() {
	if VerboseMode {
		fmt.Fprintf(os.Stderr, "ret:")
	}

	// RET (opcode 0xC3)
	o.Write(0xC3)

	if VerboseMode {
		fmt.Fprintln(os.Stderr)
	}
}
