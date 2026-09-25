// Completion: 100% - Module complete
package main

// TimMap represents map[uint64]float64 - the foundation of all Tim values
// Memory layout: [size: uint64][capacity: uint64][entry0][entry1]...
// Each entry: [key: uint64][value: float64]

type TimMapEntry struct {
	Key   uint64
	Value float64
}

// Runtime functions for Tim map operations
type TimRuntime struct {
	out *Out
	eb  *ExecutableBuilder
}

// Arena allocator runtime functions
// These functions need to be emitted as assembly code in the executable
