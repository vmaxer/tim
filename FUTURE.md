# Tim — Deferred & Future Features

This file holds language features and implementation notes that were removed from
the authoritative specs (`LANGUAGESPEC.md`, `GRAMMAR.md`) to keep the core small
and fully implementable. Nothing here is guaranteed to be implemented or stable.
Content is preserved verbatim from earlier revisions of the specs.

Status legend:
- **Deferred** — designed but not implemented; may be added later.
- **Implementation note** — describes one compiler's internals, not language semantics.

---

# Deferred: Classes and Object-Oriented Programming

> **Status: Deferred.** Not implemented (tests skip with "OOP features not yet
> fully implemented"). Removed from `LANGUAGESPEC.md` / `GRAMMAR.md`. The grammar
> productions below are kept for reference if OOP is revived.

### Grammar productions (removed from GRAMMAR.md)

```ebnf
declaration     = ... | class_decl | ... ;
class_decl      = "class" identifier [ extend_clause ] "{" { class_member } "}" ;
extend_clause   = { "<>" identifier } ;
class_member    = class_field_decl
                | method_decl ;
class_field_decl = identifier "." identifier "=" expression ;
method_decl     = identifier "=" lambda_expr ;
```


### Specification prose (removed from LANGUAGESPEC.md)

## Classes and Object-Oriented Programming

Tim supports classes as syntactic sugar over maps and closures, following the philosophy that everything is `map[uint64]float64`.

### Design Philosophy

- **Syntactic sugar:** Classes compile to regular maps and lambdas
- **No new types:** Objects are still `map[uint64]float64`
- **Composition:** Use `<>` to extend with behavior maps (no inheritance)
- **Minimal syntax:** Only adds the `class` keyword
- **Transparent:** You can always see what the class desugars to

### Class Declaration

Classes group data and methods together:

```tim
class Point {
    init = (x, y) -> {
        .x = x
        .y = y
    }

    distance = other -> {
        dx := other.x - .x
        dy := other.y - .y
        sqrt(dx * dx + dy * dy)
    }

    move = (dx, dy) -> {
        .x <- .x + dx
        .y <- .y + dy
    }
}

// Create instance
p1 := Point(10, 20)
p2 := Point(30, 40)

// Call methods
dist := p1.distance(p2)
p1.move(5, 5)
```

### How Classes Work

A class declaration creates a constructor function:

```tim
// This class:
class Counter {
    init = start -> {
        .count = start
    }

    increment = () -> {
        .count <- .count + 1
    }
}

// Desugars to this:
Counter := start -> {
    instance := {}
    instance["count"] = start
    instance["increment"] = () -> {
        instance["count"] <- instance["count"] + 1
    }
    ret instance
}
```

### Instance Fields and "this"

Inside methods, `.field` accesses instance fields. The `. ` expression (dot followed by space or newline) means "this":

```tim
class List {
    init = () => {
        .items = []
    }

    add = item -> {
        .items <- .items :: item
        ret .   // Return this (self) for chaining
    }

    size = () => .items.length
}

list = List().add(1).add(2).add(3)  // Method chaining via `. `
println(list.size())  // 3
```

**Key points:**
- `.field` accesses instance field inside methods
- `. ` (dot space or dot newline) means "this" (the current instance)
- Return `. ` for method chaining
- Outside methods, use `instance.field` explicitly
- No `this` keyword - use `. ` instead

```tim
class Account {
    init = balance -> {
        .balance = balance
    }

    withdraw = amount -> {
        amount > .balance {
            ret -1  // Insufficient funds
        }
        .balance <- .balance - amount
        ret 0
    }

    deposit = amount -> {
        .balance <- .balance + amount
    }

    get_balance = () => .balance
}

acc = Account(100)
acc.deposit(50)
println(acc.get_balance())  // 150
```

### Class Fields (Static)

Class fields are shared across all instances:

```tim
class Entity {
    Entity.count = 0
    Entity.all = []

    init = name -> {
        .name = name
        .id = Entity.count
        Entity.count <- Entity.count + 1
        Entity.all <- Entity.all :: instance
    }

    get_total = () -> Entity.count
}

e1 := Entity("Alice")
e2 := Entity("Bob")
println(e1.get_total())  // 2
println(Entity.count)    // 2
```

### Composition with `<>`

Extend classes with behavior maps using the `<>` composition operator:

```tim
// Define behavior map
Serializable := {
    to_json: () -> {
        // Serialize instance to JSON string
        keys := this.keys()
        @ i in 0..<keys.length {
            // Build JSON...
        }
    },
    from_json: json -> {
        // Parse JSON and populate instance
    }
}

// Extend class with behavior using <>
class User <> Serializable {
    init = (name, email) -> {
        .name = name
        .email = email
    }
}

user := User("Alice", "alice@example.com")
json := user.to_json()
```

**Multiple composition** - chain `<>` operators:

```tim
class Product <> Serializable <> Validatable <> Timestamped {
    init = (name, price) -> {
        .name = name
        .price = price
        .created_at = now()
    }
}
```

**How `<>` works:** The `<>` operator merges behavior maps into the class. At runtime, all methods from the behavior maps are copied into the instance during construction, with later maps overriding earlier ones if there are conflicts.

### Method Semantics

**Instance methods** close over the instance:

```tim
class Box {
    init = value -> {
        .value = value
    }

    get = () -> .value
    set = v -> { .value <- v }
}

b := Box(42)
getter := b.get  // Captures b
println(getter())  // 42
```

**Class methods** don't capture instances:

```tim
class Math {
    Math.PI = 3.14159

    // Note: no init, Math is never instantiated
    Math.circle_area = radius -> Math.PI * radius * radius
}

area := Math.circle_area(10)
```

### Private Methods (Convention)

Use underscore prefix for "private" methods:

```tim
class Parser {
    init = input -> {
        .input = input
        .pos = 0
    }

    _peek = () -> {
        .pos < .input.length {
            ret .input[.pos]
        }
        ret -1
    }

    _advance = () -> {
        .pos <- .pos + 1
    }

    parse_number = () -> {
        result := 0
        @ ._peek() >= 48 and ._peek() <= 57 {
            result <- result * 10 + (._peek() - 48)
            ._advance()
        }
        ret result
    }
}
```

### Method Chaining

Return `. ` (this) to enable chaining:

```tim
class StringBuilder {
    init = () => {
        .parts = []
    }

    append = str -> {
        .parts <- .parts :: str
        ret .  // Return this (self)
    }

    build = () => {
        result := ""
        @ part in .parts {
            result <- result + part
        }
        ret result
    }
}

str = StringBuilder()
    .append("Hello")
    .append(" ")
    .append("World")
    .build()

println(str)  // "Hello World"
```

### Integration with CStruct

Combine classes and CStruct for high performance:

```tim
cstruct Vec3Data {
    x as float64,
    y as float64,
    z as float64
}

class Vec3 {
    init = (x, y, z) -> {
        .data = c.malloc(Vec3Data.size as uint64)

        unsafe float64 {
            rax <- .data as ptr
            [rax] <- x
            [rax + 8] <- y
            [rax + 16] <- z
        }
    }

    dot = other -> {
        unsafe float64 {
            rax <- .data as ptr
            rbx <- other.data as ptr
            xmm0 <- [rax]
            xmm0 <- xmm0 * [rbx]
            xmm1 <- [rax + 8]
            xmm1 <- xmm1 * [rbx + 8]
            xmm0 <- xmm0 + xmm1
            xmm1 <- [rax + 16]
            xmm1 <- xmm1 * [rbx + 16]
            xmm0 <- xmm0 + xmm1
        }
    }

    free = () -> c.free(.data as ptr)
}

v1 := Vec3(1, 2, 3)
v2 := Vec3(4, 5, 6)
println(v1.dot(v2))  // 32.0
v1.free()
v2.free()
```

### No Inheritance

Tim does not support classical inheritance. Use composition:

```tim
// Instead of:
// class Dog extends Animal { ... }

// Do this:
Animal := {
    eat: () -> println("Eating..."),
    sleep: () -> println("Sleeping...")
}

class Dog <> Animal {
    init = name -> {
        .name = name
    }

    bark = () -> println("Woof!")
}

dog := Dog("Rex")
dog.eat()    // From Animal
dog.bark()   // From Dog
```

### When to Use Classes

**Use classes when:**
- You have related data and behavior
- You want familiar OOP syntax
- You need encapsulation (via naming conventions)
- You're building objects with state

**Don't use classes when:**
- Simple data structures (use maps)
- Stateless functions (use plain functions)
- Performance-critical code (use CStruct + functions)

### Examples

**Stack data structure:**

```tim
class Stack {
    init = () => {
        .items = []
    }

    push = item -> {
        .items <- .items :: item
    }

    pop = () => {
        .items.length == 0 {
            ret ??  // Empty
        }
        last := .items[.items.length - 1]
        .items <- .items[0..<(.items.length - 1)]
        ret last
    }

    is_empty = () => .items.length == 0
}

s = Stack()
s.push(1)
s.push(2)
s.push(3)
println(s.pop())  // 3
```

**Simple ORM-like class:**

```tim
class Model {
    Model.table = ""

    init = data -> {
        .data = data
    }

    save = () -> {
        query := f"INSERT INTO {Model.table} VALUES (...)"
        // Execute query...
    }

    delete = () -> {
        id := .data["id"]
        query := f"DELETE FROM {Model.table} WHERE id = {id}"
        // Execute query...
    }
}

class User <> Model {
    Model.table = "users"

    init = (name, email) -> {
        .data = { name: name, email: email }
    }
}

user := User("Alice", "alice@example.com")
user.save()
```


---

# Implementation notes (removed from LANGUAGESPEC.md)

> **Status: Implementation note.** These sections describe the current Go
> compiler's internals (arena machine code, optimization passes), not language
> semantics a reimplementation must match.

# Arena Allocator System

## Overview

Tim uses an arena-based memory allocator for all runtime string, list, and map operations. This provides fast, predictable memory allocation with automatic cleanup at scope boundaries.

## High-Level Design

### Memory Allocation Strategy

**Static Data (Read-Only):**
- String literals: stored in `.rodata` section
- Constant lists: `[1, 2, 3]` stored in `.rodata`
- Constant maps: `{x: 10}` stored in `.rodata`
- Never allocated at runtime, zero overhead

**Dynamic Data (Arena-Allocated):**
- String concatenation: `"hello" + "world"`
- Dynamic lists: `[x, y, z]` where x/y/z are variables
- Runtime map construction
- Function closures (future)
- Variadic argument lists

**User-Controlled Allocation:**
- `c.malloc()` - C malloc, user must call `c.free()`
- `c.realloc()` - C realloc
- `c.free()` - C free
- `alloc()` - Tim builtin (uses malloc internally)

### Arena Lifecycle

```
Program Start:
  └─> Initialize global arena (1MB)
      └─> All runtime operations use this arena
          └─> Arena grows automatically via realloc
Program End:
  └─> Free global arena

Future: arena { ... } blocks:
  Block Start:
    └─> Create scoped arena
        └─> All allocations inside use this arena
  Block End:
    └─> Free scoped arena (all allocations freed at once)
```

### Benefits

1. **Speed**: O(1) bump allocation (just increment a pointer)
2. **Predictability**: Deterministic memory usage
3. **No Fragmentation**: Memory is contiguous within an arena
4. **Batch Deallocation**: Free entire arena at once
5. **Efficient**: Perfect for frame-based allocation patterns

## Machine Code Level

### Data Structures

#### Meta-Arena Structure
```c
// _tim_arena_meta: Global variable holding pointer to arena array
uint64_t _tim_arena_meta;         // Pointer to arena_ptr[]

// Meta-arena array (dynamically allocated)
void* arena_ptrs[];                 // Array of pointers to arena structs

// Meta-arena metadata
uint64_t _tim_arena_meta_cap;     // Capacity of arena_ptrs array
uint64_t _tim_arena_meta_len;     // Number of arenas currently allocated
```

#### Arena Structure
```c
// Individual arena (32 bytes)
struct Arena {
    void*    base;         // [offset 0]  Base pointer to arena buffer
    uint64_t capacity;     // [offset 8]  Total arena size in bytes
    uint64_t used;         // [offset 16] Bytes currently used
    uint64_t alignment;    // [offset 24] Allocation alignment (typically 8)
};
```

### Initialization Sequence

At program start, `initializeMetaArenaAndGlobalArena()` executes:

```assembly
# 1. Allocate meta-arena array (8 bytes for 1 pointer)
mov rdi, 8
call malloc
# rax = pointer to meta-arena array (P1)

# 2. Store meta-arena pointer in global variable
lea rbx, [_tim_arena_meta]
mov [rbx], rax              # _tim_arena_meta = P1

# 3. Allocate arena buffer (1MB)
mov rdi, 1048576
call malloc
# rax = arena buffer (P2)
mov r12, rax

# 4. Allocate arena struct (32 bytes)
mov rdi, 32
call malloc
# rax = arena struct (P3)

# 5. Initialize arena struct
mov [rax + 0], r12          # base = P2
mov rcx, 1048576
mov [rax + 8], rcx          # capacity = 1MB
xor rcx, rcx
mov [rax + 16], rcx         # used = 0
mov rcx, 8
mov [rax + 24], rcx         # alignment = 8

# 6. Store arena struct pointer in meta-arena[0]
lea rbx, [_tim_arena_meta]
mov rbx, [rbx]              # rbx = P1 (meta-arena array)
mov [rbx], rax              # P1[0] = P3 (arena struct)
```

**Memory Layout After Initialization:**
```
_tim_arena_meta --> P1 --> [P3, NULL, NULL, ...]
                             |
                             v
                        Arena Struct {
                          base: P2 --> [1MB buffer]
                          capacity: 1048576
                          used: 0
                          alignment: 8
                        }
```

### Allocation Sequence

When allocating N bytes, `tim_arena_alloc(arena_ptr, size)` executes:

```assembly
# Input: rdi = arena_ptr (P3), rsi = size (N)
# Output: rax = allocated pointer

# 1. Load arena fields
mov r8,  [rdi + 0]          # r8  = base (P2)
mov r9,  [rdi + 8]          # r9  = capacity
mov r10, [rdi + 16]         # r10 = used
mov r11, [rdi + 24]         # r11 = alignment

# 2. Align offset
mov rax, r10                # rax = used
add rax, r11                # rax += alignment
sub rax, 1                  # rax += alignment - 1
mov rcx, r11
sub rcx, 1                  # rcx = alignment - 1
not rcx                     # rcx = ~(alignment - 1)
and rax, rcx                # rax = aligned_offset

# 3. Check capacity
mov rdx, rax
add rdx, rsi                # rdx = aligned_offset + size
cmp rdx, r9                 # if (rdx > capacity)
jg  arena_grow              #   goto grow path

# 4. Fast path: allocate
arena_fast:
mov rax, r8                 # rax = base
add rax, r13                # rax = base + aligned_offset
mov rdx, r13
add rdx, r12                # rdx = aligned_offset + size
mov [rbx + 16], rdx         # arena->used = new_offset
jmp arena_done

# 5. Grow path: realloc buffer
arena_grow:
mov rdi, r9
add rdi, r9                 # rdi = capacity * 2
# ... (grow logic, realloc arena buffer)

arena_done:
# rax = allocated pointer
ret
```

### String Concatenation Example

Concatenating `"hello" + "world"`:

```assembly
# Strings in rodata:
str_1: [5.0][0][104.0][1][101.0][2][108.0][3][108.0][4][111.0]  # "hello"
str_2: [5.0][0][119.0][1][111.0][2][114.0][3][108.0][4][100.0]  # "world"

# Concatenation code:
lea r12, [str_1]            # r12 = left string
lea r13, [str_2]            # r13 = right string

# Load lengths
movsd xmm0, [r12]           # xmm0 = 5.0
cvttsd2si r14, xmm0         # r14 = 5 (left length)
movsd xmm0, [r13]
cvttsd2si r15, xmm0         # r15 = 5 (right length)

# Calculate size: 8 + (left_len + right_len) * 16
mov rbx, r14
add rbx, r15                # rbx = 10 (total length)
mov rax, rbx
shl rax, 4                  # rax = 10 * 16 = 160
add rax, 8                  # rax = 168 (total size)

# Allocate from arena
mov rdi, rax                # rdi = 168 (size)
call callArenaAlloc         # Arena allocation
# rax = pointer to new string

# Copy data (omitted for brevity)
# Result: [10.0][0][104.0][1][101.0]...[9][100.0]  # "helloworld"
```

### Dynamic List Creation Example

Creating `[x, y]` where x=10, y=20:

```assembly
# Calculate size: 8 + (2 * 16) = 40 bytes
mov rdi, 40                 # size = 40

# Allocate from arena
lea r11, [_tim_arena_meta] # Step 1: Get meta-arena variable address
mov r11, [r11]              # Step 2: Load meta-arena pointer (P1)
mov r11, [r11]              # Step 3: Load arena[0] pointer (P3)
mov rsi, rdi                # rsi = size
mov rdi, r11                # rdi = arena_ptr
call tim_arena_alloc       # Allocate
# rax = list pointer

# Store count
mov rcx, 2
cvtsi2sd xmm0, rcx
movsd [rax], xmm0           # list[0] = 2.0 (count)

# Store element 0: key=0, value=10
xor rcx, rcx
mov [rax + 8], rcx          # list[8] = 0 (key)
movsd xmm0, [rbp - 16]      # xmm0 = x = 10.0
movsd [rax + 16], xmm0      # list[16] = 10.0 (value)

# Store element 1: key=1, value=20
mov rcx, 1
mov [rax + 24], rcx         # list[24] = 1 (key)
movsd xmm0, [rbp - 24]      # xmm0 = y = 20.0
movsd [rax + 32], xmm0      # list[32] = 20.0 (value)
```

## Implementation Details

### callArenaAlloc() Function

The `callArenaAlloc()` function in `arena.go` is a helper that:

1. Takes size in `rdi`
2. Loads the global arena pointer
3. Calls `tim_arena_alloc` with proper arguments
4. Returns allocated pointer in `rax`

**Critical Pattern (must load twice):**
```go
// Step 1: Load address of _tim_arena_meta variable
fc.out.LeaSymbolToReg("r11", "_tim_arena_meta")  // r11 = &_tim_arena_meta

// Step 2: Load meta-arena pointer from variable
fc.out.MovMemToReg("r11", "r11", 0)               // r11 = *_tim_arena_meta = P1

// Step 3: Load arena struct pointer from meta-arena[0]
fc.out.MovMemToReg("r11", "r11", 0)               // r11 = P1[0] = P3

// Now r11 = arena struct pointer, ready to pass to tim_arena_alloc
```

**Why TWO loads?**
- First load: dereference `_tim_arena_meta` to get meta-arena array pointer
- Second load: dereference `meta_arena[0]` to get arena struct pointer

### Integration Points

**String Concatenation (`_tim_string_concat`):**
```go
// OLD: fc.eb.GenerateCallInstruction("malloc")
// NEW:
fc.callArenaAlloc()
```

**Dynamic List Creation:**
```go
// OLD: fc.trackFunctionCall("malloc")
//      fc.eb.GenerateCallInstruction("malloc")
// NEW:
fc.callArenaAlloc()
```

**Dynamic Map Creation:**
```go
// Maps with constant keys/values use .rodata
// Maps with runtime keys/values use callArenaAlloc()
```

## Cleanup

At program end, `cleanupAllArenas()` frees all arenas:

```assembly
# Load meta-arena pointer
lea rbx, [_tim_arena_meta]
mov rbx, [rbx]              # rbx = P1

# Loop through arenas
xor r8, r8                  # r8 = index = 0
cleanup_loop:
cmp r8, rcx                 # if (index >= len)
jge cleanup_done            #   exit loop

# Load arena pointer
mov rax, r8
shl rax, 3                  # offset = index * 8
add rax, rbx                # rax = &meta_arena[index]
mov rdi, [rax]              # rdi = arena_ptr
call free                   # Free arena struct (also frees buffer)

inc r8                      # index++
jmp cleanup_loop

cleanup_done:
# Free meta-arena array
mov rdi, rbx
call free
```

## Future: Arena Blocks

Planned syntax for scoped arenas:

```tim
# Global arena (default)
global_data := "stays alive"

arena {
    # Scoped arena - all allocations freed at block exit
    frame_data := "temporary"
    temp_list := [1, 2, 3]

    do_work(temp_list)

} # <-- All arena allocations freed here

# global_data still valid, frame_data freed
```

**Implementation:**
- Push new arena on arena stack
- Update `currentArena` index
- All allocations use new arena
- Pop arena and free at block exit

## Debugging

**Common Issues:**

1. **Segfault in callArenaAlloc:**
   - Check that meta-arena is initialized before first use
   - Verify TWO loads are performed to get arena struct pointer
   - Ensure `tim_arena_alloc` is being called, not generated inline

2. **Null pointer from allocation:**
   - Arena may be full and realloc failed
   - Check error handling in `tim_arena_alloc`

3. **Corrupted data:**
   - Check alignment is respected
   - Verify arena->used is updated correctly
   - Ensure no double-free scenarios

**Verification:**
```tim
# Test arena allocation
s1 := "hello"
s2 := " world"
s3 := s1 + s2          # Should allocate from arena
printf("%s\n", s3)     # Should print "hello world"

list := [1, 2, 3]      # Should allocate from arena
printf("%f\n", list[0]) # Should print "1.000000"
```

## Performance Characteristics

**Arena Allocation:**
- Time: O(1) - just pointer arithmetic
- Space: Minimal overhead (32 bytes per arena struct)
- Growth: O(n) when realloc needed (rare)

**vs. Malloc:**
- ~10x faster for typical frame-based allocations
- No fragmentation
- Better cache locality
- Batch deallocation is instant

**Best Use Cases:**
- Frame-based event loops (allocate per frame, free at frame end)
- Level loading (allocate for level, free when done)
- String building (concatenate many strings, use once)
- Temporary data structures

## Summary

The arena allocator provides:
- ✅ Fast O(1) allocation
- ✅ Automatic cleanup at scope boundaries
- ✅ Zero fragmentation
- ✅ Predictable memory usage
- ✅ Integration with existing Tim runtime
- ✅ Compatibility with C malloc/free when needed

All Tim runtime operations (string concat, list creation, etc.) now use arena allocation by default, while user code can still use `c.malloc()` for manual memory management when needed.
# Tim Compiler Optimizations

## Overview

The Tim compiler includes several modern CPU instruction optimizations that provide significant performance improvements for numerical and bit manipulation code. These optimizations use runtime CPU feature detection to ensure compatibility across different processors.

## Implemented Optimizations

### 1. FMA (Fused Multiply-Add) 🚀

**Status:** ✅ Core Implementation Complete (2025-12-10)
**CPU Requirements:** Intel Haswell (2013+), AMD Piledriver (2012+) or newer
**CPU Coverage:** ~98% of modern x86-64 CPUs
**Performance Impact:** 1.5-2.0x speedup on numerical code
**Architecture Support:** x86-64 (FMA3/AVX-512), ARM64 (NEON/SVE), RISC-V (RVV)

#### What is FMA?

FMA (Fused Multiply-Add) combines multiplication and addition into a single instruction with higher precision and better performance:
- **Single instruction** instead of two separate operations
- **One rounding** instead of two (better numerical accuracy)
- **Lower latency** - typically 3-4 cycles vs 7-8 cycles for separate mul+add

#### Automatic Pattern Detection

The compiler automatically detects and optimizes these patterns:

```tim
// Pattern 1: (a * b) + c
result := x * y + z  // Optimized to VFMADD132SD

// Pattern 2: c + (a * b)
result := z + x * y  // Optimized to VFMADD132SD

// Pattern 3: Polynomial evaluation
result := a * x * x + b * x + c  // Multiple FMA instructions
```

#### Runtime Behavior

```asm
; CPU Feature Check (done once at startup)
cpuid                    ; Query CPU features
bt ecx, 12              ; Test FMA bit
setc [cpu_has_fma]      ; Store result

; Code Generation (for a * b + c)
test [cpu_has_fma]      ; Check if FMA available
jz fallback             ; Jump to fallback if not

; FMA path (modern CPUs):
vfmadd132sd xmm0, xmm2, xmm1  ; xmm0 = xmm0*xmm1 + xmm2 (3 cycles)
jmp done

fallback:               ; Fallback path (older CPUs):
mulsd xmm0, xmm1       ; xmm0 = xmm0 * xmm1 (4 cycles)
addsd xmm0, xmm2       ; xmm0 = xmm0 + xmm2 (3 cycles)

done:
```

#### Benefits

1. **Performance:** 30-80% faster for numerical computations
2. **Accuracy:** Single rounding provides IEEE 754 compliant results
3. **Compatibility:** Graceful fallback ensures code runs on older CPUs
4. **Zero configuration:** Works automatically, no compiler flags needed

#### Use Cases

- **Polynomial evaluation:** `a*x² + b*x + c`
- **Dot products:** Sum of element-wise products
- **Matrix operations:** Accumulating products
- **Physics simulations:** Force calculations, numerical integration
- **Graphics:** Vertex transformations, ray tracing

#### Implementation Details

The FMA optimization is implemented in three stages:

1. **Pattern Detection (optimizer.go):** The `foldConstantExpr` function detects `(a * b) + c` and `c + (a * b)` patterns during constant folding and creates `FMAExpr` AST nodes.

2. **AST Representation (ast.go):** The `FMAExpr` type captures the three operands (A, B, C) and operation type (add/subtract):
   ```go
   type FMAExpr struct {
       A, B, C Expression  // a * b ± c
       IsSub   bool        // true for FMSUB (subtract)
       IsNegMul bool       // true for FNMADD (negate multiply)
   }
   ```

3. **Code Generation (codegen.go):** The compiler emits architecture-specific FMA instructions:
   - **x86-64:** `VFMADD231PD` (AVX2 256-bit) or `VFMADD231PD` with EVEX (AVX-512 512-bit)
   - **ARM64:** `FMLA` (NEON 128-bit) or `FMLA` (SVE 512-bit scalable)
   - **RISC-V:** `vfmadd.vv` (RVV scalable vector)

The instruction encoders in `vfmadd.go` handle all three architectures with complete VEX/EVEX/SVE/RVV encoding.

**Current Limitations:**
- No runtime CPU feature detection yet (assumes FMA available)
- FMSUB (subtract variant) AST support exists but needs instruction encoder
- Only vector width variants implemented (no scalar VFMADD213SD yet)

### 2. Bit Manipulation Instructions ⚡

**Status:** ✅ Fully Implemented
**CPU Requirements:** Intel Nehalem (2008+), AMD K10 (2007+) or newer
**CPU Coverage:** ~95% of x86-64 CPUs
**Performance Impact:** 10-50x speedup for bit counting operations

#### POPCNT - Population Count

Counts the number of set bits in a 64-bit integer.

```tim
count := popcount(255.0)  // Returns 8.0 (0b11111111 has 8 bits set)
```

**Performance:**
- **With POPCNT:** 3 cycles (single instruction)
- **Without POPCNT:** 25+ cycles (loop implementation)
- **Speedup:** ~8x

**Machine Code:**
```asm
; Optimized path:
popcnt rax, rax        ; Count bits (3 cycles)

; Fallback path (loop):
xor rcx, rcx           ; count = 0
.loop:
  test rdx, rdx        ; while (x != 0)
  jz .done
  mov rax, rdx
  and rax, 1           ; count += x & 1
  add rcx, rax
  shr rdx, 1           ; x >>= 1
  jmp .loop
.done:
```

#### LZCNT - Leading Zero Count

Counts the number of leading zero bits (useful for finding highest set bit).

```tim
zeros := clz(8.0)      // Returns 60.0 (0b1000 in 64-bit has 60 leading zeros)
```

**Performance:**
- **With LZCNT:** 3 cycles
- **Without LZCNT:** 10-15 cycles (BSR + adjustment)
- **Speedup:** ~4x

#### TZCNT - Trailing Zero Count

Counts the number of trailing zero bits (useful for finding lowest set bit).

```tim
zeros := ctz(8.0)      // Returns 3.0 (0b1000 has 3 trailing zeros)
```

**Performance:**
- **With TZCNT:** 3 cycles
- **Without TZCNT:** 10-15 cycles (BSF)
- **Speedup:** ~4x

#### Use Cases

- **Bit manipulation:** Fast bit counting and scanning
- **Data structures:** Bloom filters, bit sets, hash tables
- **Compression:** Huffman coding, entropy encoding
- **Cryptography:** Bit-level operations
- **Game development:** Collision detection, spatial hashing

### 3. CPU Feature Detection

All optimizations use runtime CPU feature detection to ensure compatibility:

```tim
// This code automatically:
// 1. Detects CPU features at startup (CPUID)
// 2. Uses optimal instructions if available
// 3. Falls back to compatible code on older CPUs

x := 2.0
y := 3.0
z := 4.0
result := x * y + z  // Uses FMA if available, mul+add otherwise
```

**Features Detected:**
- `cpu_has_fma` - FMA3 support (Haswell 2013+)
- `cpu_has_avx2` - AVX2 support (Haswell 2013+) [Reserved for future use]
- `cpu_has_popcnt` - POPCNT/LZCNT/TZCNT support (Nehalem 2008+)
- `cpu_has_avx512` - AVX-512 support (Skylake-X 2017+) [Used for hashmap operations]

## Performance Benchmarks

### FMA Optimization

```tim
// Polynomial evaluation benchmark
polynomial := fn(x) { a * x * x + b * x + c }

// Results (1 million iterations):
Without FMA: 142 ms  (baseline)
With FMA:     78 ms  (1.82x faster)
```

### Bit Operations

```tim
// Bit counting benchmark
sum := 0.0
for i in 0..1000000 {
    sum += popcount(i)
}

// Results:
Loop implementation:  850 ms  (baseline)
POPCNT instruction:    98 ms  (8.7x faster)
```

## AVX-512 for Hashmaps 🔥

**Status:** ✅ Implemented
**CPU Requirements:** Intel Skylake-X (2017+), AMD Zen 4 (2022+) or newer
**CPU Coverage:** ~30% (high on servers, growing on desktop)
**Performance Impact:** 4-8x speedup for hashmap lookups

### What is AVX-512?

AVX-512 processes 8 double-precision values simultaneously (vs 2 for SSE2, 4 for AVX2) and includes advanced features:
- **512-bit ZMM registers:** 8x float64 operations per instruction
- **Mask registers (k0-k7):** Predicated execution without branches
- **Gather/Scatter:** Load/store non-contiguous memory in single instruction

### Hashmap Optimization

Tim uses AVX-512 `vgatherqpd` to search 8 hashmap entries at once:

```tim
// This automatically uses AVX-512 if available:
my_map := {"key1": 10.0, "key2": 20.0, "key3": 30.0}
value := my_map["key2"]  // Searches 8 keys per iteration with AVX-512
```

**Performance:**
- **With AVX-512:** Process 8 keys per iteration (~12 cycles)
- **With SSE2:** Process 2 keys per iteration (~8 cycles each)
- **Speedup:** 4-5x for large hashmaps

**Machine Code (simplified):**
```asm
; Broadcast search key to all 8 lanes
vbroadcastsd zmm3, xmm2              ; zmm3 = [key, key, key, ...]

; Gather 8 keys from hashmap
vmovdqu64 zmm4, [indices]            ; Load indices: 0, 16, 32, ...
vgatherqpd zmm0{k1}, [rbx + zmm4*1]  ; Gather 8 keys in one instruction

; Compare all 8 keys simultaneously
vcmppd k2{k1}, zmm0, zmm3, 0         ; Compare, result in mask k2

; Extract which key matched
kmovb eax, k2                         ; Move mask to GPR
test eax, eax                         ; Check if any matched
bsf edx, eax                          ; Find first match index
```

### Graceful Degradation

The compiler generates both AVX-512 and SSE2 paths:
- **AVX-512 CPUs:** Use gather instructions (8 keys/iteration)
- **Older CPUs:** Use SSE2 scalar path (2 keys/iteration)
- **Detection:** Single CPUID check at startup (~100 cycles)

## Future Optimizations (Planned)

### AVX2 Loop Vectorization

**Effort:** 2-4 weeks
**Impact:** 3-4x for array operations, 7x for matrices
**Status:** Infrastructure exists (20+ SIMD instruction files), needs wiring

Process 4 float64 values simultaneously:
```tim
// Future optimization:
for i in 0..length {
    c[i] = a[i] + b[i]  // Could use VADDPD ymm (4 doubles per instruction)
}
```

### General AVX-512 Vectorization

**Effort:** 3-4 weeks
**Impact:** 6-8x for vectorizable loops
**Status:** Infrastructure ready, needs loop analysis and transformation

Process 8 float64 values simultaneously:
```tim
// Future optimization:
for i in 0..length {
    c[i] = a[i] + b[i]  // Could use VADDPD zmm (8 doubles per instruction)
}
```

## Compiler Implementation Details

### Code Organization

- **Feature Detection:** `codegen.go` lines 553-595 (CPU feature detection at startup)
- **FMA Pattern Detection:** `codegen.go` lines 10114-10216 (AST pattern matcher)
- **FMA Code Generation:** `codegen.go` lines 10118-10169 (runtime dispatch)
- **Bit Operations:** `codegen.go` lines 14784-15002 (POPCNT/LZCNT/TZCNT)
- **SIMD Instructions:** `v*.go` files (20+ files, ~3000 LOC ready for future use)

### Testing

Comprehensive test suite in `optimization_test.go`:
- FMA pattern detection tests
- Bit manipulation correctness tests
- CPU feature detection tests
- Precision tests for FMA

Run tests:
```bash
go test -v -run "TestFMA|TestBit"
```

## Compatibility

### Minimum Requirements

- **x86-64:** Any 64-bit Intel/AMD processor (2003+)
- **FMA Optimization:** Intel Haswell (2013+), AMD Piledriver (2012+)
- **Bit Optimizations:** Intel Nehalem (2008+), AMD K10 (2007+)

### Graceful Degradation

All optimizations include fallback code paths:
- Older CPUs use traditional instructions (mul+add, bit loops)
- No performance penalty for CPU feature detection (~100 cycles at startup)
- Binary works on any x86-64 CPU, optimizes automatically

## References

### CPU Instructions

- [Intel FMA Reference](https://www.intel.com/content/www/us/en/docs/intrinsics-guide/)
- [AMD FMA3 Support](https://www.amd.com/en/technologies/fma4)
- [POPCNT Instruction](https://www.felixcloutier.com/x86/popcnt)
- [LZCNT Instruction](https://www.felixcloutier.com/x86/lzcnt)

### Performance Analysis

- [Agner Fog's Instruction Tables](https://www.agner.org/optimize/)
- [Intel Optimization Manual](https://www.intel.com/content/www/us/en/develop/documentation/cpp-compiler-developer-guide-and-reference/top/optimization-and-programming-guide.html)

## Summary

Tim now includes production-ready optimizations that provide:
- ✅ **1.5-1.8x faster** numerical code (FMA)
- ✅ **8-50x faster** bit operations (POPCNT/LZCNT/TZCNT)
- ✅ **4-8x faster** hashmap lookups (AVX-512 gather)
- ✅ **Better precision** for floating-point math (single rounding)
- ✅ **Zero configuration** (automatic detection and optimization)
- ✅ **Full compatibility** (works on all x86-64 CPUs)

The compiler is positioned to add general loop vectorization (AVX2/AVX-512) in the future, with all infrastructure already implemented (~3000 LOC in v*.go files).

**Tim can now compete with C/Rust for numerical computing and data structure performance!** 🚀

---

# Deferred: Variadic functions

> **Status: Not working.** Variadic functions (`(first, rest...) -> ...`, the
> `#rest` length operator on the collected args, and spread `f(a, xs..., b)`)
> parse and compile but segfault at runtime — the variadic argument-list codegen
> is incomplete. Documented in `LANGUAGESPEC.md` with a NOT-YET-WORKING banner.
> Fix requires correcting the runtime arg-list construction in `codegen.go`
> (search `VariadicParam` / `IsVariadic`).
