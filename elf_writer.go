// elf_writer.go - Object-oriented ELF writer
package main

// ELFWriter handles ELF file generation with proper state management
type ELFWriter struct {
	target    Target
	eb        *ExecutableBuilder
	baseAddr  uint64 // Virtual base address (0 for PIE, 0x400000 for non-PIE)
	pageSize  uint64
	isDynamic bool

	// Layout information
	layout     map[string]SegmentLayout
	entryPoint uint64

	// State tracking
	phase CompilationPhase
}

type SegmentLayout struct {
	offset uint64
	addr   uint64
	size   int
}

// NewELFWriter creates a new ELF writer with proper configuration
func NewELFWriter(target Target, eb *ExecutableBuilder, isDynamic bool) *ELFWriter {
	baseAddr := uint64(0x0) // PIE by default
	if !isDynamic {
		// Static executables can use fixed base
		baseAddr = 0x400000
	}

	return &ELFWriter{
		target:    target,
		eb:        eb,
		baseAddr:  baseAddr,
		pageSize:  0x1000,
		isDynamic: isDynamic,
		layout:    make(map[string]SegmentLayout),
		phase:     PhaseInitial,
	}
}

// GetBaseAddr returns the virtual base address
func (w *ELFWriter) GetBaseAddr() uint64 {
	return w.baseAddr
}

// GetEstimatedRodataAddr returns an estimated rodata address for first-pass compilation
func (w *ELFWriter) GetEstimatedRodataAddr() uint64 {
	// Typical layout: headers at 0x0-0x2FFF, code at 0x3000+
	return w.baseAddr + 0x3000 + 0x100
}
