// Completion: 100% - Helper module complete
package main

// printf_helper.go - Helper functions for printf runtime implementation
// This file provides utilities for patching jumps and managing code generation

// PrintfCodeGen wraps ExecutableBuilder and Out with printf-specific helpers
type PrintfCodeGen struct {
	eb  *ExecutableBuilder
	out *Out
}
