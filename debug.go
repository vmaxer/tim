package main

import (
	"fmt"
	"os"
)

// debugf writes developer debug/trace output to stderr only when verbose mode
// (-v / --verbose) is enabled. All internal "DEBUG ..." tracing routes through
// here so that normal builds produce clean output.
func debugf(format string, args ...any) {
	if VerboseMode {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}
