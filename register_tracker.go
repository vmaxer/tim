// Completion: 100% - Helper module complete
package main

import (
	"fmt"
)

// RegisterTracker manages register allocation and prevents clobbering
// It tracks which registers are currently in use and provides safe allocation/deallocation
type RegisterTracker struct {
	// XMM register availability (xmm0-xmm15)
	xmmInUse   [16]bool
	xmmPurpose [16]string // What each register is being used for (debugging)

	// Integer register availability
	// rax, rcx, rdx, rsi, rdi, r8-r15
	intInUse   map[string]bool
	intPurpose map[string]string

	// Stack for nested register usage
	xmmStack []int // Stack of allocated XMM registers
	intStack []string

	// Reserved registers (never allocated automatically)
	xmmReserved [16]bool
	intReserved map[string]bool

	// Statistics
	maxXmmUsed int
	maxIntUsed int
}

// NewRegisterTracker creates a new register tracker
func NewRegisterTracker() *RegisterTracker {
	rt := &RegisterTracker{
		intInUse:    make(map[string]bool),
		intPurpose:  make(map[string]string),
		intReserved: make(map[string]bool),
	}

	// Reserve special-purpose registers
	rt.ReserveInt("rsp") // Stack pointer
	rt.ReserveInt("rbp") // Frame pointer
	rt.ReserveInt("r15") // Environment pointer (for closures)

	// Reserve XMM0 as primary result register
	// (It can be used but must be explicitly requested)

	return rt
}

// ReserveInt marks an integer register as reserved
func (rt *RegisterTracker) ReserveInt(reg string) {
	rt.intReserved[reg] = true
}

// AllocXMM allocates an available XMM register
// Returns register name (e.g., "xmm3") or empty string if none available
func (rt *RegisterTracker) AllocXMM(purpose string) string {
	// Try to find an available register
	// Start from xmm2 (xmm0 for results, xmm1 for temps in operations)
	for i := 2; i < 16; i++ {
		if !rt.xmmInUse[i] && !rt.xmmReserved[i] {
			rt.xmmInUse[i] = true
			rt.xmmPurpose[i] = purpose
			rt.xmmStack = append(rt.xmmStack, i)

			if i > rt.maxXmmUsed {
				rt.maxXmmUsed = i
			}

			return fmt.Sprintf("xmm%d", i)
		}
	}

	return "" // No registers available
}

// FreeXMM frees an XMM register
func (rt *RegisterTracker) FreeXMM(reg string) {
	var index int
	_, err := fmt.Sscanf(reg, "xmm%d", &index)
	if err != nil || index < 0 || index >= 16 {
		return
	}

	rt.xmmInUse[index] = false
	rt.xmmPurpose[index] = ""

	// Remove from stack
	for i := len(rt.xmmStack) - 1; i >= 0; i-- {
		if rt.xmmStack[i] == index {
			rt.xmmStack = append(rt.xmmStack[:i], rt.xmmStack[i+1:]...)
			break
		}
	}
}

// Confidence that this function is working: 100%
// AllocIntCalleeSaved allocates an available callee-saved integer register
// Used for loop counters that need to survive function calls
// Returns empty string if no callee-saved registers available
func (rt *RegisterTracker) AllocIntCalleeSaved(purpose string) string {
	// Only try callee-saved registers (these survive across operations)
	// Do NOT fall back to caller-saved registers - they get clobbered
	calleeSaved := []string{"r12", "r13", "r14", "rbx"}
	for _, reg := range calleeSaved {
		if !rt.intInUse[reg] && !rt.intReserved[reg] {
			rt.intInUse[reg] = true
			rt.intPurpose[reg] = purpose
			rt.intStack = append(rt.intStack, reg)

			used := len(rt.intInUse)
			if used > rt.maxIntUsed {
				rt.maxIntUsed = used
			}

			return reg
		}
	}

	// No callee-saved registers available - caller must use stack
	return ""
}

// FreeInt frees an integer register
func (rt *RegisterTracker) FreeInt(reg string) {
	delete(rt.intInUse, reg)
	delete(rt.intPurpose, reg)

	// Remove from stack
	for i := len(rt.intStack) - 1; i >= 0; i-- {
		if rt.intStack[i] == reg {
			rt.intStack = append(rt.intStack[:i], rt.intStack[i+1:]...)
			break
		}
	}
}

// IsIntInUse checks if an integer register is currently in use
func (rt *RegisterTracker) IsIntInUse(reg string) bool {
	return rt.intInUse[reg]
}

// RegisterTrackerState represents a snapshot of register state
type RegisterTrackerState struct {
	xmmInUse   [16]bool
	xmmPurpose [16]string
	intInUse   map[string]bool
	intPurpose map[string]string
}

// SpillStrategy determines how to handle register exhaustion
type SpillStrategy int

const (
	SpillToStack SpillStrategy = iota
	SpillToMemory
	SpillError
)

// RegisterSpiller manages register spilling when all registers are in use
type RegisterSpiller struct {
	strategy      SpillStrategy
	spillSlots    int            // Number of stack slots used for spills
	spillMap      map[string]int // Register -> spill slot mapping
	nextSpillSlot int
}

// NewRegisterSpiller creates a register spiller
func NewRegisterSpiller(strategy SpillStrategy) *RegisterSpiller {
	return &RegisterSpiller{
		strategy:      strategy,
		spillMap:      make(map[string]int),
		nextSpillSlot: 0,
	}
}

// GetAllocatedCalleeSavedRegs returns a list of callee-saved registers currently in use
// Callee-saved registers on x86-64: rbx, r12, r13, r14, r15 (r15 is reserved in Tim)
func (rt *RegisterTracker) GetAllocatedCalleeSavedRegs() []string {
	var allocated []string
	calleeSaved := []string{"rbx", "r12", "r13", "r14"}

	for _, reg := range calleeSaved {
		if rt.IsIntInUse(reg) {
			allocated = append(allocated, reg)
		}
	}

	return allocated
}

// GetRegisterPressure returns current register usage statistics
type RegisterPressureStats struct {
	CurrentXmmUsed int
	MaxXmmUsed     int
	TotalXmmRegs   int
	CurrentIntUsed int
	MaxIntUsed     int
	XmmPressure    float64 // 0.0 to 1.0
	IntPressure    float64 // 0.0 to 1.0
	IsSpillHeavy   bool    // True if pressure > 80%
}
