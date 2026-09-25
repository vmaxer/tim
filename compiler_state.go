// compiler_state.go - Central state management for compilation
package main

// CompilerState manages the overall compilation state and coordinates between components
type CompilerState struct {
	// Configuration
	target  Target
	options CompileOptions

	// Writers (one will be active based on target)
	elfWriter *ELFWriter
	peWriter  *PEWriter

	// Trackers
	regTracker   *RegisterTracker
	stackTracker *StackValidator

	// Pipeline
	pipeline *CompilationPipeline

	// Core builder
	builder *ExecutableBuilder

	// Current phase
	phase CompilationPhase
}

type CompileOptions struct {
	outputPath string
	verbose    bool
	optimize   bool
	targetArch Arch // Use existing Arch type
	targetOS   OS   // Use existing OS type
}

// NewCompilerState creates a new compiler state with all components initialized
func NewCompilerState(target Target, options CompileOptions, builder *ExecutableBuilder, isDynamic bool) *CompilerState {
	cs := &CompilerState{
		target:       target,
		options:      options,
		regTracker:   NewRegisterTracker(),
		stackTracker: NewStackValidator(),
		pipeline:     NewCompilationPipeline(),
		builder:      builder,
		phase:        PhaseInitial,
	}

	// Initialize appropriate writer based on target OS
	switch target.OS() {
	case OSLinux:
		// Determine if dynamic linking is needed
		cs.elfWriter = NewELFWriter(target, builder, isDynamic)
	case OSWindows:
		cs.peWriter = NewPEWriter(target, builder)
	}

	// No need to register stages - they're predefined in compilation_pipeline.go

	return cs
}

// GetBaseAddr returns the virtual base address from the appropriate writer
func (cs *CompilerState) GetBaseAddr() uint64 {
	if cs.elfWriter != nil {
		return cs.elfWriter.GetBaseAddr()
	}
	if cs.peWriter != nil {
		return cs.peWriter.GetBaseAddr()
	}
	return 0x400000 // Default fallback
}

// GetEstimatedRodataAddr returns estimated rodata address for first pass
func (cs *CompilerState) GetEstimatedRodataAddr() uint64 {
	if cs.elfWriter != nil {
		return cs.elfWriter.GetEstimatedRodataAddr()
	}
	if cs.peWriter != nil {
		return cs.peWriter.GetEstimatedRodataAddr()
	}
	return cs.GetBaseAddr() + 0x3100 // Fallback
}
