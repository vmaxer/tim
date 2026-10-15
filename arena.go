// Arena allocator for Tim runtime
// Provides fast bump allocation with scope-based deallocation
package main

import (
	"fmt"
	"slices"
)

// ArenaScope represents different allocation scopes
type ArenaScope int

const (
	ArenaGlobal   ArenaScope = iota // Program lifetime
	ArenaFrame                      // Per-frame allocation
	ArenaFunction                   // Per-function call
	ArenaBlock                      // arena { ... } block
)

// Arena represents an allocation arena in the generated code
type Arena struct {
	name       string
	scope      ArenaScope
	baseReg    string // Register holding base pointer
	currentReg string // Register holding current pointer
	sizeReg    string // Register holding size
	labelNum   int    // Unique label number
}

// Arena runtime structure (in generated code):
// struct Arena {
//     void* base;      // Start of arena memory
//     void* current;   // Current allocation pointer
//     size_t size;     // Total arena size
//     size_t used;     // Bytes used
// }

// Code generation for arena operations

// generateArenaInit generates code to initialize an arena
// This is called at program start or arena block entry
func (fc *TimCompiler) generateArenaInit(arena *Arena, sizeBytes int) {
	// Allocate arena memory with mmap (Linux) or malloc (Windows/macOS)
	if fc.eb.target.OS() == OSLinux {
		// mmap(NULL, size, PROT_READ|PROT_WRITE, MAP_PRIVATE|MAP_ANONYMOUS, -1, 0)
		fc.out.XorRegWithReg("rdi", "rdi")                      // addr = NULL
		fc.out.MovImmToReg("rsi", fmt.Sprintf("%d", sizeBytes)) // length
		fc.out.MovImmToReg("rdx", "7")                          // PROT_READ|PROT_WRITE|PROT_EXEC = 7
		fc.out.MovImmToReg("r10", "34")                         // MAP_PRIVATE|MAP_ANONYMOUS = 0x22 = 34
		fc.out.MovImmToReg("r8", "-1")                          // fd = -1
		fc.out.XorRegWithReg("r9", "r9")                        // offset = 0
		fc.out.MovImmToReg("rax", "9")                          // sys_mmap = 9
		fc.out.Syscall()
	} else {
		// Fall back to malloc for Windows/macOS
		if !fc.hasCFunction("malloc") {
			fc.importCFunction("malloc", "msvcrt.dll")
		}
		fc.out.MovImmToReg("rdi", fmt.Sprintf("%d", sizeBytes))
		mallocSymbol := "__imp_malloc"
		fc.out.CallSymbol(mallocSymbol)
	}

	// rax now contains the arena base pointer
	// Store in arena structure (we'll use stack for now)
	fc.out.MovRegToMem("rax", "rbp", -16) // arena.base
	fc.out.MovRegToMem("rax", "rbp", -24) // arena.current = base
	fc.out.MovImmToReg("rcx", fmt.Sprintf("%d", sizeBytes))
	fc.out.MovRegToMem("rcx", "rbp", -32) // arena.size
	fc.out.XorRegWithReg("rcx", "rcx")
	fc.out.MovRegToMem("rcx", "rbp", -40) // arena.used = 0
}

// generateArenaAlloc generates code to allocate from current arena
// Returns pointer in rax
func (fc *TimCompiler) generateArenaAlloc(sizeBytes int) {
	// Load current pointer
	fc.out.MovMemToReg("rax", "rbp", -24) // rax = arena.current

	// Allocate by bumping pointer
	fc.out.MovRegToReg("rcx", "rax") // Save allocation pointer
	fc.out.AddImmToReg("rax", int64(sizeBytes))
	fc.out.MovRegToMem("rax", "rbp", -24) // Update arena.current

	// Update used count
	fc.out.MovMemToReg("rdx", "rbp", -40) // Load arena.used
	fc.out.AddImmToReg("rdx", int64(sizeBytes))
	fc.out.MovRegToMem("rdx", "rbp", -40) // Store arena.used

	// Return allocation pointer in rax
	fc.out.MovRegToReg("rax", "rcx")
}

// generateArenaReset generates code to reset arena to initial state
func (fc *TimCompiler) generateArenaReset() {
	// Reset current = base
	fc.out.MovMemToReg("rax", "rbp", -16) // Load arena.base
	fc.out.MovRegToMem("rax", "rbp", -24) // Store to arena.current

	// Reset used = 0
	fc.out.XorRegWithReg("rax", "rax")
	fc.out.MovRegToMem("rax", "rbp", -40)
}

// generateArenaFree generates code to free arena memory
// This is called at program end or arena block exit
func (fc *TimCompiler) generateArenaFree() {
	// Load base pointer
	fc.out.MovMemToReg("rdi", "rbp", -16) // rdi = arena.base

	if fc.eb.target.OS() == OSLinux {
		// munmap(addr, length)
		fc.out.MovMemToReg("rsi", "rbp", -32) // rsi = arena.size
		fc.out.MovImmToReg("rax", "11")       // sys_munmap = 11
		fc.out.Syscall()
	} else {
		// Fall back to free for Windows/macOS
		if !fc.hasCFunction("free") {
			fc.importCFunction("free", "msvcrt.dll")
		}
		freeSymbol := "__imp_free"
		fc.out.CallSymbol(freeSymbol)
	}
}

// Helper functions for calling C runtime

// Check if C function is already imported
func (fc *TimCompiler) hasCFunction(name string) bool {
	// Check in imported functions
	return slices.Contains(fc.importedFunctions, name)
}

// Import a C function for calling
func (fc *TimCompiler) importCFunction(name, library string) {
	// Add to dynamic linker if we have one
	if fc.eb.dynlinker != nil {
		fc.eb.dynlinker.ImportFunction(library, name)
	}

	// Track that we've imported it
	fc.importedFunctions = append(fc.importedFunctions, name)
}

// Arena-aware allocation functions

// Default arena sizes and growth parameters
const (
	// Initial sizes - generous defaults for typical applications
	DefaultGlobalArenaSize   = 16 * 1024 * 1024 // 16 MB (was 1MB)
	DefaultFrameArenaSize    = 4 * 1024 * 1024  // 4 MB (was 256KB)
	DefaultFunctionArenaSize = 1024 * 1024      // 1 MB (was 64KB)
	DefaultBlockArenaSize    = 512 * 1024       // 512 KB (was 32KB)

	// Growth parameters
	// 1.3x growth is gentler than 2x, wastes less memory
	// Example: 16MB → 20.8MB → 27MB → 35.1MB → 45.6MB → 59.3MB → 77MB → 100MB
	ArenaGrowthNumerator   = 13 // Multiply by 13
	ArenaGrowthDenominator = 10 // Divide by 10 = 1.3x growth

	// Maximum arena size before failing (1GB)
	MaxArenaSize = 1024 * 1024 * 1024
)

// callArenaAlloc generates code to allocate from current arena.
// Input: rdi = size to allocate  (internal ABI; all call sites must put size in rdi)
// Output: rax = pointer to allocated memory
func (fc *TimCompiler) callArenaAlloc() {
	fc.usesArenas = true

	arenaIndex := fc.currentArena - 1
	offset := arenaIndex * 8

	// _tim_arena_alloc uses the internal (Linux SysV) ABI on all platforms:
	//   rdi = arena_ptr, rsi = size
	// Translate the size in rdi to the correct pair before the call.
	if fc.eb.target.OS() == OSWindows {
		// Windows: move size from rdi to rdx (arg1), load arena into rcx (arg0).
		// We use rcx/rdx to match _tim_arena_alloc's own entry code on Windows.
		fc.out.MovRegToReg("rdx", "rdi") // rdx = size
		fc.out.LeaSymbolToReg("rcx", "_tim_arena_meta")
		fc.out.MovMemToReg("rcx", "rcx", 0)      // rcx = meta-arena array pointer
		fc.out.MovMemToReg("rcx", "rcx", offset) // rcx = arena struct pointer
	} else {
		// Linux/SysV: push size, load arena into rdi, pop size to rsi.
		fc.out.PushReg("rdi")
		fc.out.LeaSymbolToReg("rdi", "_tim_arena_meta")
		fc.out.MovMemToReg("rdi", "rdi", 0)      // rdi = meta-arena array pointer
		fc.out.MovMemToReg("rdi", "rdi", offset) // rdi = arena struct pointer
		fc.out.PopReg("rsi")                     // rsi = size
	}

	fc.trackFunctionCall("_tim_arena_alloc")
	fc.out.CallSymbol("_tim_arena_alloc")
}
