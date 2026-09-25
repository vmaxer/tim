// Arena allocator for Tim runtime
// Provides fast bump allocation with scope-based deallocation
package main

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

// Helper functions for calling C runtime

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
