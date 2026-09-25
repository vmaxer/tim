// pe_writer.go - Object-oriented PE writer
package main

// PEWriter handles PE (Windows) file generation with proper state management
type PEWriter struct {
	target   Target
	eb       *ExecutableBuilder
	baseAddr uint64 // Image base (typically 0x400000 for Windows)
	pageSize uint64

	// Layout information
	layout map[string]SegmentLayout

	// State tracking
	phase CompilationPhase
}

// NewPEWriter creates a new PE writer with proper configuration
func NewPEWriter(target Target, eb *ExecutableBuilder) *PEWriter {
	return &PEWriter{
		target:   target,
		eb:       eb,
		baseAddr: 0x400000, // Standard Windows image base
		pageSize: 0x1000,
		layout:   make(map[string]SegmentLayout),
		phase:    PhaseInitial,
	}
}

// GetBaseAddr returns the image base address
func (w *PEWriter) GetBaseAddr() uint64 {
	return w.baseAddr
}

// GetEstimatedRodataAddr returns an estimated rodata address for first-pass compilation
func (w *PEWriter) GetEstimatedRodataAddr() uint64 {
	return w.baseAddr + 0x3000 + 0x100
}
