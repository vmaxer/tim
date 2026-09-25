// compilation_pipeline.go - Explicit compilation stages with validation
package main

// CompilationStage represents a stage in the compilation pipeline (legacy)
type CompilationStage int

// CompilationPhase is the new name for CompilationStage
type CompilationPhase = CompilationStage

const (
	StageInit CompilationStage = iota
	StageFirstPassSymbolCollection
	StageFirstPassCodeGen
	StageFirstPassAddressAssignment
	StageELFStructureGeneration
	StageSecondPassSymbolCollection
	StageSecondPassCodeGen
	StageRuntimeHelperGeneration
	StagePCRelocationPatching
	StageELFFinalization
	StageComplete

	// New aliases for clearer names
	PhaseInitial        = StageInit
	PhaseParsing        = StageFirstPassSymbolCollection
	PhaseCodegenInitial = StageFirstPassCodeGen
	PhaseELFLayout      = StageFirstPassAddressAssignment
	PhaseCodegenFinal   = StageSecondPassCodeGen
	PhasePatching       = StagePCRelocationPatching
	PhaseWriting        = StageELFFinalization
	PhaseComplete       = StageComplete
)

// CompilationPipeline tracks the current stage and validates state transitions
type CompilationPipeline struct {
	currentStage CompilationStage
	stages       []CompilationStage // History of stages
	enabled      bool               // Can be disabled for performance
}

func NewCompilationPipeline() *CompilationPipeline {
	return &CompilationPipeline{
		currentStage: StageInit,
		stages:       []CompilationStage{StageInit},
		enabled:      true,
	}
}
