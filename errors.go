// Completion: 100% - Error handling complete, clear and helpful messages
package main

import (
	"errors"
	"fmt"
	"strings"
)

// ErrAlreadyReported signals that a full, formatted diagnostic has already been
// written to stderr (e.g. by the parser's ErrorCollector, complete with source
// snippet and caret). Callers that see this error should fail with a non-zero
// exit status WITHOUT printing anything further, so the user is not shown a
// second, redundant, context-free copy of the same failure.
var ErrAlreadyReported = errors.New("compilation aborted (diagnostics already reported)")

// reportedError is an error that has already been printed to stderr in full. It
// still carries the plain-text diagnostic so programmatic callers (and tests)
// can inspect err.Error(), while matching ErrAlreadyReported via errors.Is so
// the top-level CLI knows not to print it a second time.
type reportedError struct{ msg string }

func (e *reportedError) Error() string { return e.msg }

// Is lets errors.Is(err, ErrAlreadyReported) succeed for any reportedError.
func (e *reportedError) Is(target error) bool { return target == ErrAlreadyReported }

// newReportedError builds an already-reported error carrying the given message.
func newReportedError(msg string) error { return &reportedError{msg: msg} }

// ErrorLevel indicates the severity of an error
type ErrorLevel int

const (
	LevelWarning ErrorLevel = iota
	LevelError
	LevelFatal
)

func (l ErrorLevel) String() string {
	switch l {
	case LevelWarning:
		return "warning"
	case LevelError:
		return "error"
	case LevelFatal:
		return "fatal error"
	default:
		return "unknown"
	}
}

// ErrorCategory classifies the type of error
type ErrorCategory int

const (
	CategorySyntax ErrorCategory = iota
	CategorySemantic
	CategoryCodegen
	CategoryInternal
)

// SourceLocation represents a position in source code
type SourceLocation struct {
	File   string
	Line   int
	Column int
	Length int // Length of the problematic token/expression
}

func (loc SourceLocation) String() string {
	if loc.File == "" {
		return fmt.Sprintf("%d:%d", loc.Line, loc.Column)
	}
	return fmt.Sprintf("%s:%d:%d", loc.File, loc.Line, loc.Column)
}

// ErrorContext provides additional context for an error
type ErrorContext struct {
	SourceLine string // The actual line of source code
	Suggestion string // "Did you mean 'x'?"
	HelpText   string // Explanatory help text
}

// CompilerError represents a single compilation error
type CompilerError struct {
	Level    ErrorLevel
	Category ErrorCategory
	Message  string
	Location SourceLocation
	Context  ErrorContext
}

// Format returns a nicely formatted error message with context
func (e CompilerError) Format(useColor bool) string {
	var sb strings.Builder

	// Error header
	if useColor {
		sb.WriteString("\033[1;31m") // Bold red
	}
	sb.WriteString(e.Level.String())
	sb.WriteString(": ")
	if useColor {
		sb.WriteString("\033[0m") // Reset
	}
	sb.WriteString(e.Message)
	sb.WriteString("\n")

	// Location
	if useColor {
		sb.WriteString("\033[1;34m") // Bold blue
	}
	sb.WriteString("  --> ")
	sb.WriteString(e.Location.String())
	if useColor {
		sb.WriteString("\033[0m")
	}
	sb.WriteString("\n")

	// Source context
	if e.Context.SourceLine != "" {
		lineNum := fmt.Sprintf("%d", e.Location.Line)
		padding := strings.Repeat(" ", len(lineNum)+1)

		sb.WriteString(padding)
		sb.WriteString("|\n")
		sb.WriteString(lineNum)
		sb.WriteString(" | ")
		sb.WriteString(e.Context.SourceLine)
		sb.WriteString("\n")
		sb.WriteString(padding)
		sb.WriteString("| ")

		// Underline the error position
		if e.Location.Column > 0 {
			sb.WriteString(strings.Repeat(" ", e.Location.Column-1))
			if useColor {
				sb.WriteString("\033[1;31m") // Bold red
			}
			if e.Location.Length > 0 {
				sb.WriteString(strings.Repeat("^", e.Location.Length))
			} else {
				sb.WriteString("^")
			}
			if useColor {
				sb.WriteString("\033[0m")
			}
			sb.WriteString("\n")
		}
	}

	// Suggestion
	if e.Context.Suggestion != "" {
		if useColor {
			sb.WriteString("\033[1;32m") // Bold green
		}
		sb.WriteString("   help: ")
		if useColor {
			sb.WriteString("\033[0m")
		}
		sb.WriteString(e.Context.Suggestion)
		sb.WriteString("\n")
	}

	// Help text
	if e.Context.HelpText != "" {
		if useColor {
			sb.WriteString("\033[1;36m") // Bold cyan
		}
		sb.WriteString("   note: ")
		if useColor {
			sb.WriteString("\033[0m")
		}
		sb.WriteString(e.Context.HelpText)
		sb.WriteString("\n")
	}

	return sb.String()
}

// ErrorCollector accumulates errors during compilation
type ErrorCollector struct {
	errors     []CompilerError
	warnings   []CompilerError
	maxErrors  int
	sourceCode string // Full source code for context
}

// NewErrorCollector creates a new error collector
func NewErrorCollector(maxErrors int) *ErrorCollector {
	if maxErrors <= 0 {
		maxErrors = 10 // Default: stop after 10 errors
	}
	return &ErrorCollector{
		errors:    make([]CompilerError, 0),
		warnings:  make([]CompilerError, 0),
		maxErrors: maxErrors,
	}
}

// SetSourceCode stores the source code for error context
func (ec *ErrorCollector) SetSourceCode(source string) {
	ec.sourceCode = source
}

// AddError adds a compilation error
func (ec *ErrorCollector) AddError(err CompilerError) {
	// Auto-populate source line if not provided
	if err.Context.SourceLine == "" && ec.sourceCode != "" {
		err.Context.SourceLine = ec.getSourceLine(err.Location.Line)
	}

	if err.Level == LevelFatal || err.Level == LevelError {
		ec.errors = append(ec.errors, err)
	} else {
		ec.warnings = append(ec.warnings, err)
	}
}

// getSourceLine extracts a specific line from source code
func (ec *ErrorCollector) getSourceLine(lineNum int) string {
	if ec.sourceCode == "" || lineNum <= 0 {
		return ""
	}

	lines := strings.Split(ec.sourceCode, "\n")
	if lineNum > len(lines) {
		return ""
	}
	return lines[lineNum-1]
}

// HasErrors returns true if any errors were collected
func (ec *ErrorCollector) HasErrors() bool {
	return len(ec.errors) > 0
}

// ShouldStop returns true if we've hit the error limit
func (ec *ErrorCollector) ShouldStop() bool {
	return len(ec.errors) >= ec.maxErrors
}

// Report formats all errors and warnings for display
func (ec *ErrorCollector) Report(useColor bool) string {
	var sb strings.Builder

	// Report all errors
	for i, err := range ec.errors {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(err.Format(useColor))
	}

	// Report all warnings
	for i, warn := range ec.warnings {
		if i > 0 || len(ec.errors) > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(warn.Format(useColor))
	}

	// Summary
	if len(ec.errors) > 0 || len(ec.warnings) > 0 {
		sb.WriteString("\n")
		if len(ec.errors) > 0 {
			if useColor {
				sb.WriteString("\033[1;31m")
			}
			sb.WriteString(fmt.Sprintf("%d error(s)", len(ec.errors)))
			if useColor {
				sb.WriteString("\033[0m")
			}
		}
		if len(ec.warnings) > 0 {
			if len(ec.errors) > 0 {
				sb.WriteString(", ")
			}
			if useColor {
				sb.WriteString("\033[1;33m")
			}
			sb.WriteString(fmt.Sprintf("%d warning(s)", len(ec.warnings)))
			if useColor {
				sb.WriteString("\033[0m")
			}
		}
		sb.WriteString(" found\n")
	}

	return sb.String()
}

// Helper functions for creating common errors

// SyntaxError creates a syntax error
func SyntaxError(message string, loc SourceLocation) CompilerError {
	return CompilerError{
		Level:    LevelError,
		Category: CategorySyntax,
		Message:  message,
		Location: loc,
	}
}
