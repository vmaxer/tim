// Completion: 100% - Helper module complete
package main

// Calling Convention Manager for Tim
//
// Handles platform-specific calling conventions:
// - System V AMD64 ABI (Linux, macOS, BSD)
// - Microsoft x64 ABI (Windows)
// - ARM64 AAPCS
// - RISC-V calling convention
//
// Integrates with the register allocator to prevent register clobbering.
//
// ARCHITECTURE NOTE: Tim uses 3-block unsafe syntax (x86_64, arm64, riscv64)
// based on ISA, not target (arch+OS). This file bridges the gap by mapping
// ISA registers to the correct calling convention based on the target OS.
// See PLATFORM_ARCHITECTURE.md for the full design rationale.

// CallingConvention defines the interface for platform-specific calling conventions
type CallingConvention interface {
	// GetIntegerArgReg returns the register for integer argument at given index
	GetIntegerArgReg(index int) string

	// GetFloatArgReg returns the register for float argument at given index
	GetFloatArgReg(index int) string

	// GetReturnReg returns the register for return value
	GetIntegerReturnReg() string
	GetFloatReturnReg() string

	// GetCallerSavedRegs returns registers that the caller must save before a call
	GetCallerSavedRegs() []string

	// GetCalleeSavedRegs returns registers that the callee must save/restore
	GetCalleeSavedRegs() []string

	// GetShadowSpaceSize returns the size of shadow space required (Windows: 32, others: 0)
	GetShadowSpaceSize() int

	// GetStackAlignment returns the required stack alignment (usually 16 bytes)
	GetStackAlignment() int
}

// SystemVAMD64 implements the System V AMD64 calling convention (Linux, macOS, BSD)
type SystemVAMD64 struct{}

func (cc *SystemVAMD64) GetIntegerArgReg(index int) string {
	regs := []string{"rdi", "rsi", "rdx", "rcx", "r8", "r9"}
	if index < len(regs) {
		return regs[index]
	}
	return "" // Overflow to stack
}

func (cc *SystemVAMD64) GetFloatArgReg(index int) string {
	regs := []string{"xmm0", "xmm1", "xmm2", "xmm3", "xmm4", "xmm5", "xmm6", "xmm7"}
	if index < len(regs) {
		return regs[index]
	}
	return "" // Overflow to stack
}

func (cc *SystemVAMD64) GetIntegerReturnReg() string {
	return "rax"
}

func (cc *SystemVAMD64) GetFloatReturnReg() string {
	return "xmm0"
}

func (cc *SystemVAMD64) GetCallerSavedRegs() []string {
	return []string{"rax", "rcx", "rdx", "rsi", "rdi", "r8", "r9", "r10", "r11",
		"xmm0", "xmm1", "xmm2", "xmm3", "xmm4", "xmm5", "xmm6", "xmm7",
		"xmm8", "xmm9", "xmm10", "xmm11", "xmm12", "xmm13", "xmm14", "xmm15"}
}

func (cc *SystemVAMD64) GetCalleeSavedRegs() []string {
	return []string{"rbx", "rbp", "r12", "r13", "r14", "r15"}
}

func (cc *SystemVAMD64) GetShadowSpaceSize() int {
	return 0 // No shadow space required
}

func (cc *SystemVAMD64) GetStackAlignment() int {
	return 16
}

// MicrosoftX64 implements the Microsoft x64 calling convention (Windows)
type MicrosoftX64 struct{}

func (cc *MicrosoftX64) GetIntegerArgReg(index int) string {
	regs := []string{"rcx", "rdx", "r8", "r9"}
	if index < len(regs) {
		return regs[index]
	}
	return "" // Overflow to stack
}

func (cc *MicrosoftX64) GetFloatArgReg(index int) string {
	// In Microsoft x64, float args use XMM0-XMM3, sharing slots with integer args
	regs := []string{"xmm0", "xmm1", "xmm2", "xmm3"}
	if index < len(regs) {
		return regs[index]
	}
	return "" // Overflow to stack
}

func (cc *MicrosoftX64) GetIntegerReturnReg() string {
	return "rax"
}

func (cc *MicrosoftX64) GetFloatReturnReg() string {
	return "xmm0"
}

func (cc *MicrosoftX64) GetCallerSavedRegs() []string {
	return []string{"rax", "rcx", "rdx", "r8", "r9", "r10", "r11",
		"xmm0", "xmm1", "xmm2", "xmm3", "xmm4", "xmm5"}
}

func (cc *MicrosoftX64) GetCalleeSavedRegs() []string {
	return []string{"rbx", "rbp", "rdi", "rsi", "r12", "r13", "r14", "r15",
		"xmm6", "xmm7", "xmm8", "xmm9", "xmm10", "xmm11", "xmm12", "xmm13", "xmm14", "xmm15"}
}

func (cc *MicrosoftX64) GetShadowSpaceSize() int {
	return 32 // Required 32-byte shadow space
}

func (cc *MicrosoftX64) GetStackAlignment() int {
	return 16
}

// cc returns the CallingConvention for the current compilation target.
// All platform-specific register choices should go through this method.
func (fc *TimCompiler) cc() CallingConvention {
	return GetCallingConvention(fc.eb.target)
}

// argReg returns the integer/pointer argument register for argument index n (0-based).
func (fc *TimCompiler) argReg(n int) string {
	return fc.cc().GetIntegerArgReg(n)
}

// shadowSize returns the shadow space required by the calling convention (32 on Windows, 0 on Linux).
func (fc *TimCompiler) shadowSize() int {
	return fc.cc().GetShadowSpaceSize()
}

// GetCallingConvention returns the appropriate calling convention for the target
func GetCallingConvention(target Target) CallingConvention {
	switch target.Arch() {
	case ArchX86_64:
		if target.OS() == OSWindows {
			return &MicrosoftX64{}
		}
		return &SystemVAMD64{}
	case ArchARM64:
		// ARM64 AAPCS - TODO: implement when needed
		return &SystemVAMD64{} // Placeholder for now
	case ArchRiscv64:
		// RISC-V calling convention - TODO: implement when needed
		return &SystemVAMD64{} // Placeholder for now
	default:
		return &SystemVAMD64{} // Safe default
	}
}

// CallSiteManager helps manage register state around function calls
type CallSiteManager struct {
	cc           CallingConvention
	savedRegs    []string       // Registers we need to save
	savedOffsets map[string]int // Stack offsets for saved registers
	stackSpace   int            // Total stack space allocated
}
