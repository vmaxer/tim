package main

import (
	"testing"
)

func TestBitTestOperator(t *testing.T) {
	code := `
main = {
    x = 0b10110  // Binary 22 (bits: 1, 2, 4 are set)
    
    // Test each bit position
    bit0 = bit(x, 0)  // Should be 0 (bit 0 not set)
    bit1 = bit(x, 1)  // Should be 1 (bit 1 is set)
    bit2 = bit(x, 2)  // Should be 1 (bit 2 is set)
    bit3 = bit(x, 3)  // Should be 0 (bit 3 not set)
    bit4 = bit(x, 4)  // Should be 1 (bit 4 is set)
    bit5 = bit(x, 5)  // Should be 0 (bit 5 not set)
    
    // Verify results
    bit0 == 0 or exit(1)
    bit1 == 1 or exit(2)
    bit2 == 1 or exit(3)
    bit3 == 0 or exit(4)
    bit4 == 1 or exit(5)
    bit5 == 0 or exit(6)
}
`
	compileAndRun(t, code)
}

func TestBitTestWithVariablePosition(t *testing.T) {
	code := `
main = {
    value = 0b11111111  // All bits set in lower byte
    
    // Test with variable bit positions
    pos := 0
    result0 = bit(value, pos)
    result0 == 1 or exit(1)
    
    pos <- 3
    result3 = bit(value, pos)
    result3 == 1 or exit(2)
    
    pos <- 7
    result7 = bit(value, pos)
    result7 == 1 or exit(3)
    
    // Test with no bits set
    zero = 0
    pos <- 5
    result_zero = bit(zero, pos)
    result_zero == 0 or exit(4)
}
`
	compileAndRun(t, code)
}

func TestBitTestInExpression(t *testing.T) {
	code := `
main = {
    flags = 0b1010  // Bits 1 and 3 set
    
    // Use bit test in conditional expressions
    has_bit1 = (bit(flags, 1)) == 1
    has_bit2 = (bit(flags, 2)) == 1
    
    has_bit1 or exit(1)
    not has_bit2 or exit(2)
    
    // Use in arithmetic
    count = (bit(flags, 0)) + (bit(flags, 1)) + (bit(flags, 2)) + (bit(flags, 3))
    count == 2 or exit(3)  // Only bits 1 and 3 are set
}
`
	compileAndRun(t, code)
}
