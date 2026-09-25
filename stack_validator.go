// stack_validator.go - Track stack operations to detect corruption
package main

// StackValidator tracks push/pop operations to ensure balanced stack
type StackValidator struct {
	depth      int      // Current stack depth (in 8-byte words)
	operations []string // History of operations for debugging
	enabled    bool     // Can be disabled for performance
}

func NewStackValidator() *StackValidator {
	return &StackValidator{
		depth:      0,
		operations: make([]string, 0, 100),
		enabled:    true,
	}
}
