# Tim Language Specification

**Version:** 1.5.0
**Date:** 2025-12-03
**Status:** Canonical Language Reference for Tim 1.5.0 Release

This document describes the complete semantics, behavior, and design philosophy of the Tim programming language. For the formal grammar, see [GRAMMAR.md](GRAMMAR.md).

## ⚠️ CRITICAL: The Universal Type

Tim has exactly ONE type: `map[uint64]float64`

Not "represented as" or "backed by" — every value IS this map:

```tim
42              // {0: 42.0}
"Hello"         // {0: 72.0, 1: 101.0, 2: 108.0, 3: 108.0, 4: 111.0}
[1, 2, 3]       // {0: 1.0, 1: 2.0, 2: 3.0}
{x: 10}         // {hash("x"): 10.0}
[]              // {}
```

There are NO special types, NO primitives, NO exceptions.
Everything is a map from uint64 to float64.

This is not an implementation detail — this IS Tim.

## Table of Contents

- [What Makes Tim Unique](#what-makes-tim-unique)
- [Design Philosophy](#design-philosophy)
- [Type System](#type-system)
- [Variables and Assignment](#variables-and-assignment)
- [Control Flow](#control-flow)
- [Functions and Lambdas](#functions-and-lambdas)
- [Loops](#loops)
- [Parallel Programming](#parallel-programming)
- [ENet Channels](#enet-channels)
- [C FFI](#c-ffi)
- [CStruct](#cstruct)
- [Memory Management](#memory-management)
- [Unsafe Blocks](#unsafe-blocks)
- [Built-in Functions](#built-in-functions)
- [Error Handling](#error-handling)
- [Examples](#examples)

## What Makes Tim Unique

Tim brings together several novel or rare features that distinguish it from other systems programming languages:

### 1. Universal Map Type System

The entire language is built on a single type: `map[uint64]float64`. Every value—numbers, strings, lists, functions—IS this map. This radical simplification enables:
- No type system complexity
- Uniform memory representation
- Natural duck typing
- Simple FFI (cast to native types only at boundaries)

### 2. Direct Machine Code Generation

The compiler emits x86_64, ARM64, and RISCV64 machine code directly from the AST:
- **No intermediate representation** - AST → machine code in one pass
- **No dependencies** - completely self-contained compiler
- **Fast compilation** - no IR translation overhead
- **Small compiler** - ~30k lines of Go
- **Deterministic output** - same code every time

### 3. Blocks: Maps, Matches, and Statements

Blocks `{ ... }` are disambiguated by their contents:

```tim
// Map literal: contains key: value
config = { port: 8080, host: "localhost" }

// Statement block: no => or ~> arrows
compute = x -> {
    temp = x * 2
    result = temp + 10
    result  // last value returned
}

// Value match: expression before {, patterns with =>
classify = x -> x {
    0 => "zero"
    5 => "five"
    ~> "other"
}

// Guard match: no expression before {, branches with | at line start
classify = x -> {
    | x == 0 => "zero"
    | x > 0 => "positive"
    ~> "negative"
}
```

**Block disambiguation rules:**
1. Contains `:` (before arrows) → Map literal
2. Contains `=>` or `~>` → Match block (value or guard) or Mixed block
3. Otherwise → Statement block

**Mixed blocks** contain both statements and guard clauses:
```tim
process = x -> {
    println("Processing:", x)  // Statement
    | x == 0 => "zero"          // Guard clause
    | x > 0 => "positive"
    ~> "negative"
}
```
Statements execute first, then the guard match is evaluated and returned.

This unifies maps, pattern matching, guards, and function bodies into one syntax.

**Blocks as Expressions:**

All blocks that return a value are valid expressions. The value of a block is the value of its last expression:

```tim
// Block as expression in assignment
result = {
    temp = compute()
    temp * 2 + 10
}

// Block as error handler with or!
window := sdl.SDL_CreateWindow("Title", 640, 480, 0) or! {
    println("Window creation failed")
    ret 1  // Block returns 1 as error code
}

// Block in conditional
value = condition {
    1 -> {
        println("Processing true case")
        process_true()
        42  // Block returns 42
    }
    0 -> {
        println("Processing false case")
        process_false()
        99  // Block returns 99
    }
}

// Nested blocks
result = {
    x = {
        compute_inner()
        inner_value
    }
    x * 2
}
```

This design enables:
- Clean error handling without explicit returns
- Railway-oriented programming with `or!`
- Composable computation blocks
- Consistent semantics across all contexts

### 4. Unified Lambda Syntax

All functions use `->` for lambda definitions and `=>` for match arms. Define with `=` (immutable) not `:=` unless reassignment needed:

```tim
// Use = for functions (standard) with -> for lambdas
square = x -> x * 2
add = (x, y) -> x + y
compute = x -> { temp = x * 2; temp + 10 }
classify = x -> x { 0 => "zero" ~> "other" }  // -> for lambda, => for match arm
hello = -> println("Hello!")        // No params: explicit ->

// Shorthand: () and -> can be omitted when context is clear (assignment + block)
main = { println("Hello!") }       // Inferred as: main = () -> { println("Hello!") }

// Only use := if function will be reassigned
handler := x -> println(x)
handler <- x -> println("DEBUG:", x)  // reassignment with <-
```

**Convention:**
- Functions are immutable by default (`=`), only use `:=` when needed
- `->` is always for lambda definitions
- `=>` is always for match arms

### 5. Minimal Parentheses

Avoid parentheses unless needed for precedence or grouping:

```tim
// Good: no unnecessary parens
x > 0 { 1 => "positive" ~> "negative" }
result = x + y * z
classify = x -> x { 0 => "zero" ~> "other" }

// () and -> can be omitted in assignment context with blocks
main = { println("Running") }     // Inferred: main = () -> { ... }

// Only use when needed
result = (x + y) * z              // precedence
cond = (x > 0 and y < 10) { ... }  // complex condition grouping
add = (x, y) -> x + y             // multiple lambda parameters
```

### 6. Bitwise Operators with `b` Suffix

All bitwise operations are suffixed with `b` to eliminate ambiguity:

```tim
<<b >>b <<<b >>>b    // Shifts and rotates
&b |b ^b !b ~b          // Bitwise logic
?b                   // Bit test (tests if bit at position is set)
```

### 7. Explicit String Encoding

```tim
text = "Hello"
bytes = text.bytes   // Map of byte values {0: byte0, 1: byte1, ...}
runes = text.runes   // Map of Unicode code points {0: rune0, 1: rune1, ...}
```

### 8. ENet for All Concurrency

Network-style message passing for concurrency:

```tim
&8080 <- "Hello"     // Send to channel
msg <= &8080         // Receive from channel
```

### 9. Fork-Based Process Model

Parallel loops use `fork()` for true isolation:

```tim
|| i in 0..10 {      // Each iteration in separate process
    compute(i)
}
```

### 10. Pipe Operators for Data Flow

```tim
|    Pipe (transform)
||   Parallel map
```

### 11. List Access Functions

```tim
head(xs)   // Get first element
tail(xs)   // Get all but first element
#xs        // Length (prefix or postfix)
```

**Semantics:**
```tim
// Lists and maps
xs = [1, 2, 3]
head(xs)     // 1.0 (first element)
tail(xs)     // [2, 3] (remaining elements)
#xs          // 3.0 (length)

// Numbers (single-element maps)
n = 42
head(n)      // 42.0 (the number itself)
tail(n)      // [] (empty - no remaining elements)
#n           // 1.0 (map has one entry)

// Empty collections
head([])     // [] (no head)
tail([])     // [] (no tail)
#[]          // 0.0 (empty)
```

### 12. C FFI via DWARF

Parse C headers automatically using DWARF debug info:

```tim
result = c_function(arg1, arg2)  // Direct C calls
```

### 13. CStruct with Direct Memory Access

```tim
cstruct Point {
    x as float64,
    y as float64
}
p = Point(1.0, 2.0)
p.x  // Direct memory offset access
```

### 14. Tail-Call Optimization Always On

```tim
factorial = (n, acc) -> (n == 0) {
    1 => acc
    ~> factorial(n - 1, acc * n)    // Optimized to loop
}
```

### 15. Cryptographically Secure Random

```tim
x = ??  // Uses OS CSPRNG
```

### 16. Memory Ownership Operator `µ` (Prefix)

```tim
new_owner = µold_owner  // Transfer ownership
```

### 17. Result Type with NaN Error Encoding

```tim
result = risky_operation()
result.error { != "" -> println("Error:", result.error) }
```

### 18. Immutable-by-Default

```tim
x = 42      // Immutable
y := 100    // Mutable (explicit)
```

## Design Philosophy

### Core Principles

1. **Simplicity over complexity**
   - One universal type (map)
   - Minimal syntax
   - Direct code generation

2. **Explicit over implicit**
   - Mutability must be declared (`:=`)
   - String encoding is explicit (`.bytes`, `.runes`)
   - Bitwise ops marked with `b` suffix

3. **Performance without compromise**
   - Direct machine code generation
   - Tail-call optimization
   - Zero-cost abstractions
   - No garbage collection overhead

4. **Safety where it matters**
   - Immutable by default
   - Explicit unsafe blocks
   - Arena allocators
   - Move semantics

5. **Minimal conventions**
   - Functions use `=` not `:=`
   - Avoid unnecessary parentheses
   - Match blocks require explicit condition or guards

## Type System

Tim uses a **universal map type**: `map[uint64]float64`

Every value in Tim IS `map[uint64]float64`:

- **Numbers**: `{0: number_value}`
- **Strings**: `{0: char0, 1: char1, 2: char2, ...}`
- **Lists**: `{0: elem0, 1: elem1, 2: elem2, ...}`
- **Objects**: `{key_hash: value, ...}`
- **Functions**: `{0: code_pointer, 1: closure_data, ...}`

There are no special cases. No "single entry maps", no "byte indices", no "field hashes" — just uint64 keys and float64 values in every case.

### Type Annotations

Type annotations are **optional metadata** that specify semantic intent and guide FFI conversions. They do NOT change the runtime representation (always `map[uint64]float64`).

**Native Tim types:**
```tim
num    // number (default type)
str    // string (map of character codes)
list   // list (map with sequential keys)
map    // explicit map
bool   // boolean (yes/no values)
```

**Foreign C types:**
```tim
cstring   // C char* (null-terminated string pointer)
cptr      // C pointer (e.g., SDL_Window*, void*)
cint      // C int/int32_t
clong     // C int64_t/long
cfloat    // C float
cdouble   // C double
cbool     // C bool/_Bool
cvoid     // C void (return type only)
```

**Usage:**

```tim
// Variable declarations
x: num = 42
name: str = "Alice"
values: list = [1, 2, 3]

// Function parameters and return types
add = (x: num, y: num) -> num { x + y }
greet = (name: str) -> str { f"Hello, {name}!" }

// FFI functions (type annotations required for correct marshalling)
window: cptr = sdl.SDL_CreateWindow("Game", 800, 600, 0)
error: cstring = sdl.SDL_GetError()
result: cint = sdl.SDL_Init(0x00000020)

// Without annotations, Tim uses heuristics (may be imprecise)
window := sdl.SDL_CreateWindow("Game", 800, 600, 0)  // Inferred as map
```

**When to use type annotations:**
- **Optional** for pure Tim code (types inferred from usage)
- **Recommended** for FFI code (enables precise marshalling)
- **Required** for complex FFI (pointer types, structs, proper error handling)

Without annotations, Tim uses heuristics at FFI boundaries. With annotations, Tim marshalls precisely.

### Type Conversions

Use `as` for explicit type casts at FFI boundaries:

```tim
x as int32      // Cast to C int32
ptr as cstr     // Cast to C string pointer
val as float64  // Cast to C double
```

**Supported cast types (legacy, for `unsafe` blocks):**
```
int8 int16 int32 int64
uint8 uint16 uint32 uint64
float32 float64
ptr cstr
```

### Duck Typing

Since everything is a map, Tim has structural typing:

```tim
point = { x: 10, y: 20 }
point.x  // Works - map has "x" key

person = { name: "Alice", x: 5 }
person.x  // Also works - different map, same key
```

Type annotations are contextual keywords - you can use them as identifiers:

```tim
num = 100              // OK - variable named num
x: num = num * 2       // OK - type annotation vs variable
```

### Boolean Type

Tim has a dedicated boolean type with two values: `yes` and `no`.

**Representation:**
Booleans are `map[uint64]float64` with a special marker to distinguish them from numbers:
```tim
yes    // {0: 1.0, 1: 1.0}  (marker: key 1 exists with value 1.0)
no     // {0: 0.0, 1: 0.0}  (marker: key 1 exists with value 0.0)
```

**Key distinction:** Booleans are NOT the same as `1.0` and `0.0`:
```tim
yes == 1.0      // no (different internal structure)
no == 0.0       // no (different internal structure)
yes == yes      // yes
no == no        // yes
```

**Boolean operations:**
```tim
result: bool = yes
flag: bool = no

// Logical operations
yes and yes     // yes
yes and no      // no
yes or no       // yes
not yes         // no
not no          // yes
```

**Type conversions:**
```tim
// To C strings
yes as cstr     // "true"
no as cstr      // "false"

// To C bool
yes as cbool    // true
no as cbool     // false

// To numbers
yes as num      // 1.0
no as num       // 0.0
```

**Default return value:**
Functions that don't explicitly return a value return `1.0` (number). To explicitly return success/failure as a boolean, use `yes` or `no`:
```tim
process = {
    println("Processing...")
    yes  // Explicitly return yes
}

validate = input -> {
    | input > 0 => yes
    ~> no
}
```

**Comparison operations return booleans:**
```tim
x = 10
result = x > 5      // yes
result = x < 5      // no

name = "Alice"
same = name == "Alice"   // yes
```

**Usage in control flow:**
```tim
// Match on booleans
check = value > 100
check {
    yes => println("Large value")
    no => println("Small value")
}

// Guard matches
result = {
    | value > 100 => yes
    | value > 50 => no
    ~> no
}
```

## Variables and Assignment

### Shadowing Rules

**Shadow Keyword Requirement:**
When declaring a variable that would shadow (hide) a variable from an outer scope, the `shadow` keyword is **required**. This prevents accidental shadowing bugs while allowing intentional shadowing.

```tim
// Module level
port = 8080
config = { host: "localhost" }

// Function that intentionally shadows
main = {
    shadow port = 9000        // ✓ OK: explicitly shadows module port
    shadow config = {}        // ✓ OK: explicitly shadows module config
    println(port)             // Prints 9000
}

// ERROR: Forgot shadow keyword
test = {
    port = 3000               // ✗ ERROR: would shadow module 'port' without 'shadow' keyword
}

// Nested scopes
process = x -> {
    shadow x = x * 2          // ✓ OK: shadows parameter x
    inner = y -> {
        shadow x = x + y      // ✓ OK: shadows outer x
        x
    }
    inner(10)
}

// First declaration doesn't need shadow
compute = {
    value = 42                // ✓ OK: first declaration in this scope
    result = value * 2        // ✓ OK: no shadowing
}
```

**Rationale:**
- **Prevents bugs**: Accidental shadowing is a common source of errors
- **Makes intent clear**: Reader knows shadowing is intentional
- **Helps refactoring**: Renaming outer variables won't silently break inner scopes
- **Natural naming**: Variables can use natural names in all scopes (no ALL_UPPERCASE requirement)

### Immutable Assignment (`=`)

Creates immutable binding (cannot reassign variable or modify contents):

```tim
x = 42
x = 100  // ERROR: cannot reassign immutable variable

nums = [1, 2, 3]
nums[0] = 99  // ERROR: cannot modify immutable value
```

**Use for:**
- Constants
- Function definitions (standard practice)
- Values that won't change

### Mutable Assignment (`:=`)

Creates mutable binding (can reassign variable and modify contents):

```tim
x := 42
x := 100  // OK: reassign mutable variable
x <- 200  // OK: update with <-

nums := [1, 2, 3]
nums[0] <- 99  // OK: modify mutable value
```

**Use for:**
- Loop counters
- Accumulators
- Values that will change
- Functions that need reassignment (rare)

### Update Operator (`<-`)

Updates mutable variables or map elements:

```tim
x := 10
x <- 20      // Update variable

nums := [1, 2, 3]
nums[0] <- 99    // Update list element

config := { port: 8080 }
config.port <- 9000  // Update map field
```

### Multiple Assignment (Tuple Unpacking)

Multiple variables can be assigned from a list in a single statement:

```tim
// Unpack function return (list)
new_list, popped_value = pop([1, 2, 3])
println(new_list)      // [1, 2]
println(popped_value)  // 3

// Unpack list literal
x, y, z := [10, 20, 30]

// Works with any list expression
first, second = some_function()
a, b, c = [1, 2, 3, 4, 5]  // a=1, b=2, c=3 (extras ignored)
```

**Rules:**
- Right side must evaluate to a list/map
- Variables are assigned elements at indices 0, 1, 2, etc.
- If list has fewer elements, remaining variables get `0`
- If list has more elements, extras are ignored
- Can use `=`, `:=`, or `<-` (with mutable vars)

**Common patterns:**
```tim
// Swap values
a, b := 1, 2
a, b <- [b, a]  // Swap using list literal

// Split list
first, rest = head(xs), tail(xs)  // First element and remaining

// Function with multiple returns
quotient, remainder = divmod(17, 5)
```

### Function Assignment Convention

**Always use `=` for functions** unless the function variable needs reassignment:

```tim
// Standard (use =)
add = (x, y) -> x + y
factorial = n -> n { 0 => 1 ~> n * factorial(n-1) }

// Only use := if reassigning
handler := x -> println(x)
handler <- x -> println("DEBUG:", x)  // reassign
```

### Mutability Semantics

The assignment operator determines both **variable mutability** and **value mutability**:

| Operator | Variable Mutability | Value Mutability |
|----------|---------------------|------------------|
| `=` | Immutable (can't reassign) | Immutable (can't modify contents) |
| `:=` | Mutable (can reassign) | Mutable (can modify contents) |

**Examples:**

```tim
// Immutable binding, immutable value
nums = [1, 2, 3]
nums <- [4, 5, 6]     // ERROR: can't reassign
nums[0] <- 99         // ERROR: can't modify

// Mutable binding, mutable value
vals := [1, 2, 3]
vals <- [4, 5, 6]     // OK: reassign
vals[0] <- 99         // OK: modify
```

## Control Flow

### If / Elif / Else

Tim supports statement-level conditional branching with `if`, `elif`, and `else`:

```tim
x := 10

if x > 20 {
    println("large")
} elif x > 5 {
    println("medium")
} else {
    println("small")
}
```

- Conditions use standard truthiness (`0` is false, non-zero is true)
- `elif` is optional and can be repeated
- `else` is optional

### Match Expressions

Match blocks have two forms: **value match** and **guard match**.

#### Value Match (with expression before `{`)

Evaluates expression, then matches its result against patterns:

```tim
// Match on literal values
x = 5
result = x {
    0 => "zero"
    5 => "five"
    10 => "ten"
    ~> "other"
}

// Match on boolean (1 = true, 0 = false)
result = (x > 0) {
    1 => "positive"
    0 => "not positive"
}

// Shorthand with default
result = (x > 10) {
    1 => "large"
    ~> "small"
}
```

#### Guard Match (no expression, branches with `|` at line start)

Each branch evaluates its own condition:

```tim
// Guard branches with | at line start
classify = x -> {
    | x == 0 => "zero"
    | x > 0 => "positive"
    | x < 0 => "negative"
    ~> "unknown"  // optional default
}

// Multiple conditions
category = age -> {
    | age < 13 => "child"
    | age < 18 => "teen"
    | age < 65 => "adult"
    ~> "senior"
}
```

**Important:** The `|` is only a guard marker when at the start of a line/clause.
Otherwise `|` is the pipe operator:

```tim
// This is a guard (| at start)
x -> { | x > 0 => "positive" }

// This is a pipe operator (| not at start)
result = data | transform | filter
```

**Key difference:**
- **Value match:** One expression evaluated once, result matched against patterns
- **Guard match:** Each `|` branch (at line start) evaluates independently (short-circuits on first true)

**Default case:** `~>` works in both forms

### Tail Calls

The compiler automatically optimizes tail calls to loops:

```tim
// Explicit tail call with => in match arms
factorial = (n, acc) -> (n == 0) {
    1 => acc
    ~> factorial(n - 1, acc * n)
}

// Tail call in default case
sum_list = (list, acc) -> (list.length) {
    0 => acc
    ~> sum_list(list[1:], acc + list[0])
}
```

**Tail position rules:**
- Last expression in function body
- After `=>` or `~>` in match arm
- In final expression of block

## Functions and Lambdas

### Function Definition

Functions can be defined using `=` with lambdas, or with optional `fun` keyword for clarity:

```tim
// Traditional lambda syntax
square = x -> x * x
add = (x, y) -> x + y

// With optional 'fun' keyword for clarity
fun factorial = n -> {
    result := 1
    @ i in 1..n {
        result *= i
    }
    result
}

// fun keyword doesn't change semantics, just visual clarity
fun process = data -> data * 2
// Same as: process = data -> data * 2

// No-argument lambda: () and -> can be omitted when assigning blocks
greet = { println("Hello!") }
fun init = { setup() }
// Both equivalent to: name = () -> { ... }
```

### Lambda Expressions

Lambdas can be defined with or without the `->` arrow, depending on syntax:

```tim
// Inline lambda with arrow (required for expressions)
[1, 2, 3] | x -> x * 2

// Single parameter with block
process = n -> { n * 2 + 1 }

// Parenthesized parameters - arrow optional with block body
square = (n) { n * n }              // Arrow omitted
add = (a, b) { a + b }              // Arrow omitted
mult = (x, y) -> { x * y }          // Arrow included (both work)

// Multi-line lambda
process = data -> {
    cleaned = data | x -> x.trim()
    cleaned | x -> x.length > 0
}

// Zero-argument lambda (inferred in assignment)
greet = { println("Hello!") }

// Explicit zero-argument lambda
greet = () { println("Hello!") }    // Arrow optional
greet = () -> { println("Hello!") } // Arrow explicit

// Lambdas can have local variables
compute = x -> {
    temp = x * 2
    result = temp + 10
    result  // Returns 2*x + 10
}

// Implicit true (1.0) return for statement-only blocks
init = {
    config := load_config()
    cache <- init_cache()
    // Implicitly returns true (1.0) - no explicit return needed
}
```

**Arrow Rules:**
- **Required:** Expression body: `x -> x * 2`, non-parenthesized params: `x -> { ... }`, lambda as argument: `map(xs, x -> x * 2)`
- **Optional:** Parenthesized params with block: `(n) { n * 2 }` or `(a, b) { a + b }`
- **Inferred:** Zero params in assignment: `main = { ... }` becomes `main = () { ... }`

### Closures

Lambdas capture their environment:

```tim
make_counter = start -> {
    count := start
    () -> {
        count <- count + 1
        count
    }
}

counter = make_counter(0)
counter()  // 1
counter()  // 2
```

### Higher-Order Functions

Functions can take and return functions, including calling a function passed as a parameter:

```tim
apply_twice = (f, x) -> f(f(x))

increment = x -> x + 1
result = apply_twice(increment, 10)  // 12
```

### Function Composition

The `<>` operator composes two functions, creating a new function that applies the right operand first, then the left:

```tim
// Basic composition
double = x -> x * 2
add_ten = x -> x + 10

// compose creates: x -> double(add_ten(x))
compose = double <> add_ten
result = compose(5)  // (5 + 10) * 2 = 30

// Multiple compositions
triple = x -> x * 3
add_five = x -> x + 5
transform = triple <> double <> add_five
value = transform(10)  // ((10 + 5) * 2) * 3 = 90

// Composition is right-associative, so:
// f <> g <> h  means  f <> (g <> h)
// And evaluates as: x -> f(g(h(x)))
```

The composition operator provides a concise way to build complex transformations from simple functions.

### Variadic Functions

> **Status: NOT YET WORKING.** Variadic functions parse and compile, but calling
> one currently segfaults at runtime (the variadic argument-list machinery is
> incomplete). The design below is the intended behavior; do not rely on it yet.

Functions can accept a variable number of arguments using the `...` suffix on the last parameter:

```tim
// Simple variadic function
sum = (first, rest...) -> {
    total := first
    @ item in rest {
        total <- total + item
    }
    total
}

result = sum(1, 2, 3, 4, 5)  // 15

// Variadic with multiple fixed parameters
printf = (format, args...) -> {
    // format is required, args... collects remaining arguments
    c.printf(format, args...)
}

// All arguments variadic
log = (messages...) -> {
    @ msg in messages {
        println(msg)
    }
}

log("Error:", "File not found:", filename)
```

**Variadic Rules:**
- Only the last parameter can be variadic (have `...` suffix)
- The variadic parameter receives a list of all remaining arguments
- If no extra arguments are passed, the variadic parameter is an empty list `[]`
- Variadic parameters require parentheses: `(args...)` not `args...`
- Can be used with fixed parameters: `(x, y, rest...)` is valid

**Variadic Parameter Passing:**

When calling a variadic function, you can:
1. Pass arguments individually: `sum(1, 2, 3, 4)`
2. Spread a list with `...`: `sum(values...)`
3. Mix both: `sum(1, 2, values...)`

```tim
// Define variadic function
max = (nums...) -> {
    result := nums[0]
    @ n in nums {
        ? n > result { result <- n }
    }
    result
}

// Call with individual args
max(5, 10, 3, 8)  // 10

// Call with spread operator
values = [5, 10, 3, 8]
max(values...)  // 10

// Mix individual and spread
max(1, 2, values..., 99)  // 99
```

## Loops

### Infinite Loop

```tim
@ {
    println("Forever")
}
```

### Counted Loop

```tim
@ 10 {
    println("Hello")
}
```

### Range Loop

```tim
@ i in 0..10 {
    println(i)
}

// With step
@ i in 0..100..10 {  // 0, 10, 20, ...
    println(i)
}
```

### Collection Loop

```tim
nums = [1, 2, 3, 4, 5]
@ n in nums {
    println(n)
}
```

### Loop Control

Tim supports both traditional and modern loop control syntax:

**Traditional syntax** (using `ret @`):
```tim
// Exit current loop
@ i in 0..<100 {
    i > 50 { ret @ }      // Exit current loop
    i == 42 { ret @ 42 }  // Exit loop with value 42
    println(i)
}

// Nested loops with explicit labels
@ i in 0..<10 {           // Loop @1 (outer)
    @ j in 0..<10 {       // Loop @2 (inner)
        j == 5 { ret @ }         // Exit inner loop (@2)
        i == 5 { ret @1 }        // Exit outer loop (@1)
        i == 3 and j == 7 { ret @1 42 }  // Exit outer loop with value
        println(i, j)
    }
}
```

**Modern syntax sugar** (keywords `break`, `continue`, `foreach`):
```tim
// break/continue syntax (aliases for ret @)
foreach i in 0..<100 {
    i > 50 { break }       // Same as: ret @
    i % 2 == 0 { continue } // Same as: ret @ []  (continue)
    println(i)
}

// Nested with explicit labels
foreach i in 0..<10 {      // Loop @1
    foreach j in 0..<10 {  // Loop @2
        j == 5 { break }      // Exit inner loop
        i == 5 { break @1 }   // Exit outer loop
        println(i, j)
    }
}

// foreach is syntax sugar for @ ... in
foreach x in items { process(x) }  // Same as: @ x in items { process(x) }
```

**Recommendation:** Use `foreach/break/continue` for familiar C-style loops, use `@ / ret @` for advanced control flow.

**Loop Label Numbering:**

Loops are automatically numbered from **outermost to innermost**:
- `@1` = outermost loop
- `@2` = second level (nested inside @1)
- `@3` = third level (nested inside @2)
- `@` = current/innermost loop (same as highest number)

**Loop Control Syntax:**
- `ret @` or `break` or `break @1` - Exit innermost loop
- `ret @2` or `break @2` - Exit second loop level (jump out to @1)
- `ret @N value` - Exit loop N with return value
- `continue` or `continue @1` - Continue to next iteration
- `ret value` - Return from function (not loop)

### Loop `!` Keyword

Loops with unknown bounds or modified counters require `!`:

```tim
// Counter modified in loop
@ i in 0..<10 ! 20 {
    i++  // Modified counter, needs !
}

// Unknown iteration count
@ msg in read_channel() ! inf {
    process(msg)
}

// Condition-based loop
@ x < threshold ! 1000 {
    x = compute_next(x)
}
```

## Parallel Programming

### Parallel Loops

Use `||` for parallel iteration (each iteration in separate process):

```tim
|| i in 0..10 {
    // Runs in separate forked process
    expensive_computation(i)
}
```

**Implementation:** Uses `fork()` for true OS-level parallelism.

### Parallel Map

```tim
// Sequential map
results = [1, 2, 3] | x => x * 2

// Parallel map
results = [1, 2, 3] || x => expensive(x)
```



## ENet Channels

Tim uses **ENet-style message passing** for concurrency:

### Send Messages

```tim
&8080 <- "Hello"          // Send to port 8080
&"host:9000" <- data      // Send to remote host
```

### Receive Messages

```tim
msg <= &8080              // Receive from port 8080
data <= &"server:9000"    // Receive from remote
```

### Channel Patterns

```tim
// Worker pattern
worker = -> {
    @ {
        task <= &8080
        result = process(task)
        &8081 <- result
    }
}

// Pipeline pattern
stage1 = -> @ { &8080 <- generate_data() }
stage2 = -> @ { data <= &8080; &8081 <- transform(data) }
stage3 = -> @ { result <= &8081; save(result) }
```

**Note:** ENet channels are compiled directly into machine code that uses ENet library calls.

## C FFI

Tim can call C functions directly using DWARF debug information and automatic header parsing:

### Calling C Functions

```tim
// Standard C library functions (automatically available)
result = c.malloc(1024)
c.free(result)

// C math functions
x := c.sin(0.0)     // Returns 0
y := c.cos(0.0)     // Returns 1
z := c.sqrt(16.0)   // Returns 4

// With type casts
size = buffer_size as int32
ptr = c.malloc(size)

// Import C library namespaces
import "sdl3" as sdl

// Access constants from C headers
flags := sdl.SDL_INIT_VIDEO
window := sdl.SDL_CreateWindow("Title", 640, 480, flags)
```

## Import and Export System

Tim provides a unified import system for libraries, git repositories, and local files. The export system controls function visibility and namespace requirements.

### Export Control

The `export` statement at the top of a Tim file controls which functions are available to importers:

**Three export modes:**

1. **`export *`** - All functions exported to global namespace (no prefix needed)
   ```tim
   // gamelib.tim
   export *

   init = (w, h) -> { ... }
   draw_rect = (x, y, w, h) -> { ... }
   ```
   Usage:
   ```tim
   import "gamelib" as game
   init(800, 600)      // No prefix
   draw_rect(10, 20, 50, 50)
   ```

2. **`export func1 func2`** - Only listed functions exported (prefix required)
   ```tim
   // api.tim
   export public_func another_func

   public_func = { ... }       // Exported
   another_func = { ... }      // Exported
   internal_helper = { ... }   // Not exported
   ```
   Usage:
   ```tim
   import "api" as api
   api.public_func()        // OK
   api.another_func()       // OK
   api.internal_helper()    // Error - not exported
   ```

3. **No export** - All functions available (prefix required)
   ```tim
   // utils.tim
   // No export statement

   helper1 = { ... }
   helper2 = { ... }
   ```
   Usage:
   ```tim
   import "utils" as utils
   utils.helper1()   // Prefix required
   utils.helper2()   // Prefix required
   ```

**Use cases:**
- `export *`: Beginner-friendly libraries and frameworks
- `export list`: Controlled public APIs with internal implementation details
- No export: General libraries where namespace pollution matters

### Import Priority

1. **Libraries** (highest priority) - system libraries, .dll/.so files
2. **Git Repositories** - GitHub, GitLab, etc. with version control
3. **Local Directories** (lowest priority) - relative/absolute paths

### Library Imports

```tim
// System library (uses pkg-config on Linux/macOS)
import "sdl3" as sdl
import "raylib" as rl

// Windows DLL (searches current dir, then system paths)
import "SDL3.dll" as sdl
import "kernel32.dll" as kernel

// Direct library file path
import "/usr/lib/libmylib.so" as mylib
import "C:\\Windows\\System32\\user32.dll" as user32
```

**Library resolution:**
- Linux/macOS: Uses `pkg-config` to find headers and libraries
- Windows: Searches for .dll in current directory, then system paths
- Parses C headers automatically for function signatures and constants

### Git Repository Imports

```tim
// HTTPS format (recommended)
import "github.com/example/timcc-math" as math

// With version specifier
import "github.com/example/timcc-math@v1.0.0" as math
import "github.com/example/timcc-math@v2.1.3" as math
import "github.com/example/timcc-math@latest" as math
import "github.com/example/timcc-math@main" as math

// SSH format
import "git@github.com:example/timcc-math.git" as math

// GitLab and other providers
import "gitlab.com/user/project" as proj
import "bitbucket.org/user/repo" as repo
```

**Git repository behavior:**
- Clones to `~/.cache/timcc/` (respects `XDG_CACHE_HOME`)
- Imports all top-level `.tim` files from the repository
- Version specifiers:
  - `@v1.0.0` - Specific tag
  - `@main` / `@master` - Specific branch
  - `@latest` - Latest tag, or default branch if no tags
  - No `@` - Uses default branch

### Directory Imports

```tim
// Current directory
import "." as local

// Relative paths
import "./lib" as lib
import "../shared" as shared

// Absolute paths
import "/opt/timlib" as timlib
import "C:\\Projects\\mylib" as mylib
```

**Directory behavior:**
- Imports all top-level `.tim` files from the directory
- Relative paths resolved from current working directory
- Allows organizing code into modules

### Import Examples

```tim
// Complete application setup
import "sdl3" as sdl                           // System library
import "github.com/example/timcc-math" as math  // Git repo
import "./game_logic" as game                  // Local directory

main = {
    sdl.SDL_Init(sdl.SDL_INIT_VIDEO)
    angle := math.radians(45)
    game.start_level(1)
}
```

### Header Parsing and Constants

Tim automatically parses C header files using pkg-config and library introspection:

```tim
import sdl3 as sdl  // Parses SDL3 headers, extracts constants and function signatures

// Constants are available with the namespace prefix
init_flags := sdl.SDL_INIT_VIDEO | sdl.SDL_INIT_AUDIO
window_flags := sdl.SDL_WINDOW_RESIZABLE | sdl.SDL_WINDOW_FULLSCREEN

// Function signatures are type-checked at compile time
window := sdl.SDL_CreateWindow("Title", 640, 480, window_flags)
```

**How it works:**
1. `import sdl3 as sdl` triggers header parsing
2. Compiler uses `pkg-config --cflags sdl3` to find header paths
3. Parses main header file for `#define` constants and function signatures
4. Constants become available as `sdl.CONSTANT_NAME`
5. Functions are linked and type-checked

### Type Mapping

| Tim | C |
|------|---|
| `x as int32` | `int32_t` |
| `x as float64` | `double` |
| `ptr as cstr` | `char*` |
| `ptr as ptr` | `void*` |

### Null Pointer Literals

When calling C functions, you can use any of these as null pointer (0):

```tim
// All of these represent null pointer when used in C FFI context
c.some_function(0)           // Number literal 0
c.some_function([])          // Empty list
c.some_function({})          // Empty map
c.some_function(0 as ptr)    // Explicit cast
c.some_function([] as ptr)   // Empty list cast
c.some_function({} as ptr)   // Empty map cast
```

This makes Tim's null pointer representation flexible and intuitive:
- `0` is the traditional null pointer value
- `[]` and `{}` represent "empty" or "nothing", which conceptually maps to null
- Explicit casts make the intent clear in code

### Null Pointer Handling with `or!`

C functions that return pointers return 0 (null) on failure. Use `or!` for clean error handling:

```tim
// Old style: manual null check
window := sdl.SDL_CreateWindow("Title", 640, 480, 0)
window == 0 {
    println("Failed to create window!")
    sdl.SDL_Quit()
    exit(1)
}

// New style: or! with block
window := sdl.SDL_CreateWindow("Title", 640, 480, 0) or! {
    println("Failed to create window!")
    sdl.SDL_Quit()
    exit(1)
}

// Or with default value
ptr := c.malloc(1024) or! 0
```

**Semantics:**
- `or!` checks for both NaN (error values) and 0 (null pointers)
- If the left side is NaN or 0:
  - If right side is a block: executes the block (lazy evaluation)
  - If right side is an expression: evaluates and returns it
- If the left side is valid (not NaN, not 0): returns the left value
- Right side is NOT evaluated unless left side is error/null (short-circuit evaluation)

### Railway-Oriented C Interop

Chain multiple C calls with `or!` for clean error handling:

```tim
init_graphics = () => {
    // Each call handles its own error with or!
    sdl.SDL_Init(sdl.SDL_INIT_VIDEO) or! {
        println("SDL_Init failed!")
        exit(1)
    }

    window := sdl.SDL_CreateWindow("Title", 640, 480, 0) or! {
        println("Create window failed!")
        sdl.SDL_Quit()
        exit(1)
    }

    renderer := sdl.SDL_CreateRenderer(window, 0) or! {
        println("Create renderer failed!")
        sdl.SDL_DestroyWindow(window)
        sdl.SDL_Quit()
        exit(1)
    }

    ret [window, renderer]
}
```

### C Library Linking

The compiler links with `-lc` by default. Additional libraries:

```bash
timcc program.tim -o program -L/path/to/libs -lmylib

# SDL3 example
timcc sdl_demo.tim -o sdl_demo $(pkg-config --libs sdl3)
```

## CStruct

Define C-compatible structures with explicit memory layout:

### Declaration

```tim
cstruct Point {
    x as float64,
    y as float64
}

cstruct Rect {
    top_left as Point,
    width as float64,
    height as float64
}
```

### Usage

```tim
// Create struct
p = Point(3.0, 4.0)

// Access fields (direct memory offset, no overhead)
println(p.x)  // 3.0
p.x <- 10.0   // Update field

// Nested structs
r = Rect(Point(0.0, 0.0), 100.0, 50.0)
println(r.top_left.x)
```

### Memory Layout

CStructs have C-compatible memory layout:
- Fields stored sequentially in memory
- No hidden metadata
- Can be passed to C functions directly
- Access via direct pointer arithmetic

## Memory Management

### Stack vs Heap

- **Stack**: Function local variables, temporaries
- **Heap**: Dynamically allocated data (lists, maps, large objects)

### Arena Allocators and Minimal Builtins

**CRITICAL DESIGN PRINCIPLE:** Tim keeps builtin functions to an ABSOLUTE MINIMUM.

**Memory management with syntax sugar:**
- `malloc(size)` - Arena allocator (syntax sugar for allocate within arena)
- `free(ptr)` - No-op (arena cleanup happens automatically)
- For explicit C memory: use `c.malloc`, `c.free`, `c.realloc`, `c.calloc`
- For zero-initialized memory: use `c.calloc(count, size)`

```tim
// Explicit arena syntax (recommended for large scopes)
result = arena {
    data = allocate(1024)
    process(data)
    final_value
}
// All arena memory freed here

// Convenient syntax sugar - uses arena automatically
buffer := malloc(1024)
// free(buffer) is no-op, arena cleanup happens automatically

// Explicit C FFI (when you need manual control)
ptr := c.malloc(1024)
defer c.free(ptr)

// For C structs that need zero-initialization:
event := c.calloc(1, 128)! as SDL_Event  // Allocates and zeros 128 bytes
```

**Note:** `malloc()`/`free()` keywords use arena allocator behind the scenes. This provides familiar syntax while maintaining arena safety. Only use `c.malloc`/`c.free` when you need explicit manual memory management.

**List operations:**
- Use builtin functions: `head(xs)` for first element, `tail(xs)` for remaining elements
- Use `#` length operator (prefix or postfix)

**Why minimal builtins?**
1. **Simplicity:** Less to learn, fewer concepts
2. **Orthogonality:** One way to do each thing
3. **Extensibility:** Users define their own functions
4. **Predictability:** No hidden behavior
5. **Transparency:** Everything explicit

**What IS builtin:**
- **Operators:** `#`, arithmetic, logic, bitwise, etc.
- **Control flow:** `@` loops, match blocks, `ret`, `defer`
- **Core I/O:** `print`, `println`, `printf`, `eprint`, `eprintln`, `eprintf`, `exitln`, `exitf`
- **List operations:** `head()`, `tail()`
- **Keywords:** `arena`, `unsafe`, `cstruct`, `import`, etc.

**Everything else via:**
1. **Operators** for common operations (`#xs` for length)
2. **Builtin functions** for core operations (`head(xs)`, `tail(xs)`)
3. **C FFI** for system functions (`c.sin` not `sin`, `c.malloc` not `malloc`)
4. **User-defined functions** for application logic

This keeps the language core minimal and forces clarity at call sites.

### Defer for Resource Management

The `defer` statement schedules cleanup code to execute when the current scope exits, enabling automatic resource management similar to Go's defer or C++'s RAII.

**Syntax:**
```tim
defer expression
```

**Execution Guarantees:**
- Deferred expressions execute when scope exits (return, block end, error)
- Execution order is LIFO (Last In, First Out)
- Always executes, even on early returns or errors
- Multiple defers in same scope form a cleanup stack

**Basic Example:**
```tim
open_file = filename -> {
    file := c.fopen(filename, "r") or! {
        println("Failed to open:", filename)
        ret 0
    }
    defer c.fclose(file)  // Always closes, even on error

    // Read and process file...
    data := read_all(file)

    ret data
    // c.fclose(file) executes here automatically
}
```

**LIFO Execution Order:**
```tim
process = () => {
    defer println("First")   // Executes last (3rd)
    defer println("Second")  // Executes second (2nd)
    defer println("Third")   // Executes first (1st)
}
// Output: Third, Second, First
```

**With C FFI (SDL3 Example):**
```tim
init_sdl = () => {
    sdl.SDL_Init(sdl.SDL_INIT_VIDEO) or! {
        println("SDL init failed")
        ret 0
    }
    defer sdl.SDL_Quit()  // Cleanup registered immediately

    window := sdl.SDL_CreateWindow("App", 640, 480, 0) or! {
        println("Window creation failed")
        ret 0  // SDL_Quit still called via defer
    }
    defer sdl.SDL_DestroyWindow(window)  // Will execute before SDL_Quit

    renderer := sdl.SDL_CreateRenderer(window, 0) or! {
        println("Renderer creation failed")
        ret 0  // Both SDL_DestroyWindow and SDL_Quit called
    }
    defer sdl.SDL_DestroyRenderer(renderer)  // Will execute first

    // Use SDL resources...
    render_frame(renderer)

    ret 1
    // Cleanup order: renderer, window, SDL_Quit
}
```

**Railway-Oriented Pattern:**
```tim
// Combine defer with or! for clean error handling
acquire_resources = () => {
    db := connect_db() or! {
        println("DB connection failed")
        ret error("db")
    }
    defer disconnect_db(db)

    cache := init_cache() or! {
        println("Cache init failed")
        ret error("cache")
    }
    defer cleanup_cache(cache)

    // Work with resources...
    // All cleanup happens automatically
}
```

**Best Practices:**
1. **Register immediately after acquisition:** `resource := acquire(); defer cleanup(resource)`
2. **Use with or! operator:** Error blocks can return early, defer ensures cleanup
3. **Avoid exit():** Use `ret` instead so defers execute
4. **LIFO cleanup order:** Register defers in acquisition order, they'll clean up in reverse
5. **C FFI resources:** Perfect for file handles, sockets, SDL objects, etc.

**When NOT to use defer:**
- For Tim data structures (use arena allocators instead)
- When cleanup must happen immediately, not at scope exit
- When cleanup order must be explicit (just call cleanup functions directly)

### Move Semantics

Transfer ownership with postfix `!`:

```tim
large_data := [1, 2, 3, /* ... */, 1000000]
new_owner = large_data!  // Move, don't copy
// large_data now invalid
```

### Manual Memory (C FFI)

Tim does NOT provide `malloc`/`free` as builtins. Use C FFI:

```tim
unsafe ptr {
    ptr = c.malloc(1024)
    // Use ptr
    c.free(ptr)
}
```

## Unsafe Blocks

For direct machine code access, Tim provides architecture-specific unsafe blocks. The compiler selects the appropriate block based on the target ISA (instruction set architecture).

### Architecture Model

Tim's platform support separates **ISA** from **OS**:
- **ISA** (x86_64, arm64, riscv64) → determines registers and instructions
- **OS** (Linux, Windows, macOS) → determines binary format and calling conventions
- **Target** = ISA + OS (e.g., arm64-darwin, x86_64-windows)

**Unsafe blocks are ISA-based, not target-based.** This means:
- Write assembly for the CPU architecture (3 blocks)
- Compiler handles OS differences automatically
- Same arm64 block works on Linux, macOS, and Windows

See [PLATFORM_ARCHITECTURE.md](PLATFORM_ARCHITECTURE.md) for the full design rationale.

### Supported Targets

| Target | ISA | OS | Status |
|--------|-----|-------|--------|
| x86_64-linux | x86_64 | Linux | ✅ Complete |
| x86_64-windows | x86_64 | Windows | ✅ Complete |
| arm64-linux | arm64 | Linux | 🚧 90% (needs defer, dynamic linking) |
| arm64-darwin | arm64 | macOS | ❌ Not started |
| arm64-windows | arm64 | Windows | ❌ Not started |
| riscv64-linux | riscv64 | Linux | 🚧 80% (needs testing) |

### Syntax

```tim
unsafe {
    # x86_64 block
} {
    # arm64 block
} {
    # riscv64 block
} as return_type

// Or the legacy syntax (type before blocks):
unsafe return_type {
    # x86_64 block
} {
    # arm64 block
} {
    # riscv64 block
}
```

### Examples

```tim
// Direct memory access (3 ISA variants) - new syntax
value = unsafe {
    rax <- ptr           # x86_64
    rax <- [rax + offset]
} {
    x0 <- ptr            # arm64
    x0 <- [x0 + offset]
} {
    a0 <- ptr            # riscv64
    a0 <- [a0 + offset]
} as float64

// Or legacy syntax:
value = unsafe float64 {
    rax <- ptr
    rax <- [rax + offset]
} {
    x0 <- ptr
    x0 <- [x0 + offset]
} {
    a0 <- ptr
    a0 <- [a0 + offset]
}

// Syscall (x86_64 only example, expand to 3 blocks in real code)
unsafe {
    rax <- 1        // sys_write (x86_64)
    rdi <- 1        // stdout
    rsi <- msg_ptr
    rdx <- msg_len
    syscall
} {
    x8 <- 64        // sys_write (arm64)
    x0 <- 1         // stdout
    x1 <- msg_ptr
    x2 <- msg_len
    svc
} {
    a7 <- 64        // sys_write (riscv64)
    a0 <- 1         // stdout
    a1 <- msg_ptr
    a2 <- msg_len
    ecall
}
```

### Platform Differences (Compiler Handles Automatically)

While you write ISA-specific code, the compiler handles these OS differences:

**Binary Format:**
- Linux/BSD → ELF
- Windows → PE
- macOS → Mach-O

**Calling Conventions:**
- x86_64 Linux/macOS → System V ABI (rdi, rsi, rdx, rcx, r8, r9)
- x86_64 Windows → Microsoft x64 (rcx, rdx, r8, r9)
- arm64 Linux/macOS → AAPCS64 (x0-x7)
- arm64 Windows → ARM64 Windows (x0-x7, different struct passing)

**Syscalls:**
- Linux x86_64 → `syscall` instruction
- Linux arm64 → `svc #0` instruction
- Windows (any arch) → C FFI to kernel32.dll (no direct syscall)

**Note:** Unsafe blocks break portability and safety guarantees. Use only when absolutely necessary (e.g., custom syscalls, direct hardware access, performance-critical assembly).

## Built-in Functions

### I/O

```tim
// Standard output (stdout)
println(x)           // Print with newline to stdout
print(x)            // Print without newline to stdout
printa(x)           // Atomic print (thread-safe)
printf(fmt, ...)    // Formatted print to stdout

// Standard error (stderr) - Returns Result with error code "out"
eprintln(x)         // Print with newline to stderr, returns Result
eprint(x)           // Print without newline to stderr, returns Result
eprintf(fmt, ...)   // Formatted print to stderr, returns Result

// Quick exit error printing - Print to stderr and exit(1)
exitln(x)           // Print with newline to stderr and exit(1)
exitf(fmt, ...)     // Formatted print to stderr and exit(1)
```

**Error Print Functions (`eprint`, `eprintln`, `eprintf`):**
- Print to stderr instead of stdout
- Return a Result type with error code "out"
- Useful for logging and error messages
- Can be chained with `.error` accessor or `or!` operator

**Quick Exit Functions (`exitln`, `exitf`):**
- Print to stderr and immediately exit with code 1
- Never return (equivalent to eprint + exit(1))
- Useful for fatal error messages and early termination
- Simpler than using eprint followed by manual exit()

**Usage examples:**

```tim
// Basic error printing
eprintln("Warning: low memory")
eprintf("Error: invalid value %v\n", x)

// Check result from error print
result := eprintln("This is an error message")
result.error {
    "out" -> println("Successfully wrote to stderr")
    ~> println("Unexpected error")
}

// Quick exit on fatal error
x < 0 {
    exitln("Fatal: negative value not allowed")
    // Never reaches here - program exits
}

// Formatted quick exit
age < 0 {
    exitf("Fatal: invalid age %v\n", age)
}

// Equivalent to:
x < 0 {
    eprintln("Fatal: negative value not allowed")
    exit(1)
}
```

### String Operations

```tim
s = "Hello"
s.length            // 5 (number of entries in the map)
s.bytes             // Map of byte values {0: 72.0, 1: 101.0, ...}
s.runes             // Map of Unicode code points
s + " World"        // Concatenation (merges maps)
```

#### F-String Interpolation

F-strings provide powerful string interpolation with full expression support:

```tim
// Basic interpolation
name = "Alice"
greeting = f"Hello, {name}!"  // "Hello, Alice!"

// Arithmetic expressions
x = 10
y = 20
result = f"{x} + {y} = {x + y}"  // "10 + 20 = 30"

// Function calls in interpolation
double = (n) { n * 2 }
triple = (n) { n * 3 }
msg = f"double(5) = {double(5)}"  // "double(5) = 10"

// Nested function calls
msg = f"triple(double(4)) = {triple(double(4))}"  // "triple(double(4)) = 24"

// Multiple interpolations in one string
a = 3
b = 4
result = f"{a} + {b} = {a + b}, {a} * {b} = {a * b}"
// "3 + 4 = 7, 3 * 4 = 12"

// Complex expressions
items = [1, 2, 3]
msg = f"List has {#items} items, first is {items[0]}"
// "List has 3 items, first is 1"

// Nested f-strings work too
inner = "world"
outer = f"Hello, {f"{inner}"}!"  // "Hello, world!"
```

**Implementation Note:** F-strings are evaluated at runtime, allowing dynamic expressions within `{}`. Any valid Tim expression can be used, including function calls, arithmetic, map access, and nested interpolations.

### List Operations

```tim
nums = [1, 2, 3]
nums.length         // 3
nums[0]             // 1
nums[1:]            // [2, 3]
nums + [4, 5]       // [1, 2, 3, 4, 5]
```

### Map Operations

```tim
m = { x: 10, y: 20 }
m.x                 // 10
m.z <- 30           // Add field
keys = m.keys()     // Get all keys
```

### Math Functions

All standard math via C FFI:

```tim
sin(x)
cos(x)
sqrt(x)
pow(x, y)
abs(x)
```

## Error Handling

### Result Type Design

Tim uses **NaN-boxing** to encode errors within float64 values. This elegant approach, inspired by ENet's use of bit patterns for encoding flags and types, keeps everything as `map[uint64]float64` while enabling robust error handling.

**Encoding Scheme:**
- **Success values:** Regular float64 (standard IEEE 754 representation)
- **Error values:** Quiet NaN with 32-bit error code encoded in the mantissa

**Error NaN Format:**
```
IEEE 754 Double (64 bits):
[Sign][Exponent (11)][Mantissa (52)]

Error encoding:
[0][11111111111][1][000][0...0][cccccccccccccccccccccccccccccccc]
    ^^^^^^^^^^^  ^              ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
    All 1s = NaN |              32-bit error code (4 ASCII chars)
                 Quiet NaN bit

Hex representation: 0x7FF8_0000_[CODE]_[CODE]
Example ("dv0\0"): 0x7FF8_0000_6476_3000
```

**Key Properties:**
- Errors are distinguishable from all valid floats (including ±Inf, regular NaN)
- Error checking is a single NaN test: `x != x` or `UCOMISD`
- Error codes are human-readable 4-char ASCII strings
- Zero runtime overhead for success cases
- Compatible with all IEEE 754 compliant hardware

### Standard Error Codes

Error codes are exactly 4 bytes (null-padded if shorter), encoded as 32-bit integers:

```
"dv0\0" (0x64763000) - Division by zero
"idx\0" (0x69647800) - Index out of bounds
"key\0" (0x6B657900) - Key not found
"typ\0" (0x74797000) - Type mismatch
"nil\0" (0x6E696C00) - Null pointer
"mem\0" (0x6D656D00) - Out of memory
"arg\0" (0x61726700) - Invalid argument
"io\0\0" (0x696F0000) - I/O error
"net\0" (0x6E657400) - Network error
"prs\0" (0x70727300) - Parse error
"ovf\0" (0x6F766600) - Overflow
"udf\0" (0x75646600) - Undefined
```

**Note:** The `.error` accessor extracts the code and converts it to a Tim string, stripping null bytes.

### Operations That Return Results

```tim
// Arithmetic errors
x = 10 / 0              // Error: "dv0 " (division by zero)
y = 2 ** 1000           // Error: "ovf " (overflow)

// Index errors
xs = [1, 2, 3]
z = xs[10]              // Error: "idx " (out of bounds)

// Key errors
m = { x: 10 }
w = m.y                 // Error: "key " (key not found)

// Custom errors
err = error("arg")      // Create error with code "arg "
```

### The `.error` Accessor

Every value has a `.error` accessor that:
- Returns `""` (empty string) for success values
- Returns the error code string (spaces stripped) for error values

```tim
x = 10 / 2              // Success: returns 5.0
x.error                 // Returns "" (empty)

y = 10 / 0              // Error: division by zero
y.error                 // Returns "dv0" (spaces stripped)

// Typical usage
result.error {
    "" -> proceed(result)
    ~> handle_error(result.error)
}

// Match on specific errors
result.error {
    "" -> println("Success:", result)
    "dv0" -> println("Division by zero")
    "mem" -> println("Out of memory")
    ~> println("Unknown error:", result.error)
}
```

### The `or!` Operator

The `or!` operator provides a default value or executes a block when the left side is an error or null:

```tim
// Handle errors
x = 10 / 0              // Error result
safe = x or! 99         // Returns 99 (error case)

y = 10 / 2              // Success result (value 5)
safe2 = y or! 99        // Returns 5 (success case)

// Handle null pointers from C FFI
window := sdl.SDL_CreateWindow("Title", 640, 480, 0) or! {
    println("Failed to create window!")
    sdl.SDL_Quit()
    exit(1)
}

// Inline null check with default
ptr := c.malloc(1024) or! 0  // Returns 0 if allocation failed

// Railway-oriented programming pattern
result := sdl.SDL_Init(sdl.SDL_INIT_VIDEO) or! {
    println("SDL_Init failed!")
    exit(1)
}
```

**Semantics:**
1. Evaluate left operand
2. Check if error (type byte 0xE0) or null (value is 0 for pointer types)
3. If error/null and right side is a block: execute block
4. If error/null and right side is an expression: return right operand
5. Otherwise: return left operand value

**When checking for null (C FFI pointers):**
- The compiler recognizes pointer-returning C functions
- `or!` treats 0 (null pointer) as a failure case
- Enables railway-oriented programming for C interop
- Blocks can contain cleanup code and exits

**Precedence:** Lower than logical OR, higher than send operator

### Error Propagation Patterns

```tim
// Check and early return
process = input -> {
    step1 = validate(input)
    step1.error { != "" -> step1 }  // Return error early

    step2 = transform(step1)
    step2.error { != "" -> step2 }

    finalize(step2)
}

// Default values with or!
compute = input -> {
    x = parse(input) or! 0     // Use 0 if parse fails
    y = divide(100, x) or! -1  // Use -1 if division fails
    y * 2
}

// Chained operations with error handling
result = fetch_data()
    | parse or! []              // Default to empty list
    | transform
    | validate or! error("typ") // Custom error

// C FFI null pointer handling
init_sdl = () => {
    // Initialize SDL
    sdl.SDL_Init(sdl.SDL_INIT_VIDEO) or! {
        println("Failed to initialize SDL!")
        exit(1)
    }

    // Create window with error handling
    window := sdl.SDL_CreateWindow("Title", 640, 480, 0) or! {
        println("Failed to create window!")
        sdl.SDL_Quit()
        exit(1)
    }

    // Create renderer with error handling
    renderer := sdl.SDL_CreateRenderer(window, 0) or! {
        println("Failed to create renderer!")
        sdl.SDL_DestroyWindow(window)
        sdl.SDL_Quit()
        exit(1)
    }

    ret [window, renderer]
}

// Simpler pattern with defaults
allocate_buffer = size -> {
    ptr := c.malloc(size) or! 0
    ptr == 0 {
        ret error("mem")  // Out of memory
    }
    ret ptr
}
```

### Creating Custom Errors

Use the `error` function to create error Results:

```tim
// Create error with code
validate = x -> {
    x < 0 { ret error("arg") }  // Negative argument
    x
}

// Or detect errors from operations
divide = (a, b) => {
    result = a / b
    result.error {
        != "" -> result          // Propagate error
        ~> result                // Return success
    }
}
```

### Error Type Tracking

The compiler tracks whether a value is a Result type:

```tim
// Compiler knows this returns Result
divide = (a, b) => {
    b == 0 { ret error("dv0") }
    a / b
}

// Compiler propagates Result type
compute = x -> {
    y = divide(100, x)  // y has Result type
    y or! 0             // Handles potential error
}

// Compiler warns if Result not checked
risky = x -> {
    y = divide(100, x)  // Warning: unchecked Result
    println(y)          // May print error value
}
```

See [TYPE_TRACKING.md](TYPE_TRACKING.md) for implementation details.

### Result Type Memory Layout

**Success value (number 42.0):**
```
IEEE 754: 0x4045000000000000 (standard float64 encoding)
Binary:   [0][10000000100][0101000000000000...] (exp=1028, mantissa=5*2^48)
```

**Error value (division by zero "dv0"):**
```
Hex:      0x7FF8000064763000
Binary:   [0][11111111111][1][000][0...0][01100100 01110110 00110000 00000000]
          ^  ^^^^^^^^^^^  ^              ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
          |  NaN exponent |              "dv0\0" = 0x64763000
          +  Quiet bit    +
          Sign=0          Reserved bits (future use)
```

### `.error` Implementation

The `.error` accessor:
1. Checks if value is NaN: `UCOMISD xmm0, xmm0` (sets parity flag if NaN)
2. If not NaN: returns empty string ""
3. If NaN: extracts low 32 bits of mantissa as error code
4. Converts 4-byte code to Tim string (strips null bytes)
5. Returns error code string
5. Otherwise: return empty string ""

### `or!` Implementation

The `or!` operator:
1. Evaluates left operand
2. Checks type byte (first byte)
3. If 0xE0: returns right operand
4. Otherwise: returns left operand value (strips type metadata)

### Best Practices

**Do:**
- Check `.error` for operations that can fail
- Use `or!` for simple default values
- Match on specific error codes for different handling
- Propagate errors up the call chain
- Create custom errors with descriptive codes

**Don't:**
- Ignore error results
- Use empty error codes (use specific 4-char codes)
- Mix error codes with success values
- Assume all errors are the same

**Examples:**

```tim
// Good: check errors
result = fetch_data()
result.error {
    "" -> process(result)
    "net" -> retry()
    ~> fail(result.error)
}

// Good: use or! for defaults
data = fetch_data() or! []
count = #data

// Bad: ignore errors
result = fetch_data()  // May be error
process(result)        // May process error value

// Bad: vague error code
validate = x -> x < 0 { ret error("bad") }  // Use "arg" instead
```

## Compilation and Execution

### Compiler Usage

```bash
# Compile to executable
timcc program.tim -o program

# Compile with C library
timcc program.tim -o program -lm

# Specify target architecture
timcc program.tim -o program -arch arm64
timcc program.tim -o program -arch riscv64

# Hot reload mode (Unix)
timcc --hot program.tim

# Show version
timcc --version
```

### Supported Architectures

- **x86_64** (AMD64) - Primary platform
- **ARM64** (AArch64) - Full support
- **RISCV64** - Full support

### Compilation Process

1. **Lexing**: Source code → tokens
2. **Parsing**: Tokens → AST
3. **Type Inference**: Track semantic types (see TYPE_TRACKING.md)
4. **Code Generation**: AST → machine code (direct, no IR)
5. **Linking**: Produce ELF (Linux), Mach-O (macOS), or PE (Windows)

### Performance Characteristics

- **Compilation**: Fast (no IR overhead)
- **Tail calls**: Always optimized to loops
- **Arithmetic**: SIMD for vectorizable operations
- **Memory**: Arena allocators for predictable patterns
- **Concurrency**: OS-level parallelism via fork()

### Program Execution Model

Tim supports three program structures:

#### 1. Main Function Entry Point

When a `main` function is defined, it serves as the entry point:

```tim
main = { println("Hello, World!") }    // main() called, prints and returns true (1.0)
main = { 42 }                           // Returns 42 as exit code
main = () -> { println("Starting"); 1 } // Returns 1
```

**Behavior:**
- If `main` is callable (function/lambda): called automatically at program end UNLESS `main()` is explicitly called anywhere in top-level code
- Auto-call is skipped if any top-level statement (outside of lambda definitions) calls `main()`, regardless of context (direct call, inside match expression, etc.)
- Return value is implicitly cast to int32 for the OS exit code
- Empty blocks `{}` return true (1.0)
- Numeric values are converted to int32

**Examples:**
```tim
// Auto-called (no explicit main() call in top-level code)
main = { println("Hello!") }
// Output: Hello!

// NOT auto-called (explicit main() call exists)
main = { println("Hello!") }
main()  // Explicit call
// Output: Hello! (printed once, not twice)

// NOT auto-called (main() called in match expression)
main = { println("Hello!") }
x = 42 { 0 => 0 ~> main() }  // main() in match default case
// Output: Hello! (called once from match)

// Auto-called (main() only called inside a lambda, not at top level)
main = { println("Hello!") }
wrapper = { main() }  // main() inside lambda doesn't count
// Output: Hello! (auto-called, wrapper is not executed)
```

**Rule:** The compiler tracks whether `main()` appears as a call expression in any top-level statement. If it does, the automatic call at program end is suppressed.

#### 2. Top-Level Code

When there's no `main` defined, top-level statements execute sequentially:

```tim
println("Starting")
result := compute_something()
println(f"Result: {result}")
0  // Last expression is exit code
```

**Exit code determination:**
- Last expression value becomes exit code
- `ret` statement sets explicit exit code
- No explicit return or value: defaults to true (1.0)

#### Mixed Cases

**Top-level code WITH main function:**

When both exist, top-level code executes first and is responsible for calling `main()`:

```tim
// Setup in top-level
config := load_config()

main = {
    println("In main")
    process(config)
    0
}

// Top-level must call main explicitly
main()  // Without this, main never runs
```

If top-level code doesn't call `main()`, the function is never executed, and the last top-level expression determines the exit code.

## Examples

### Hello World

```tim
println("Hello, World!")
```

### Factorial

```tim
// Iterative
factorial = n -> {
    result := 1
    @ i in 1..n {
        result *= i
    }
    result
}

// Tail-recursive (optimized to loop)
factorial = (n, acc) => n == 0 {
    -> acc
    ~> factorial(n-1, n*acc)
}

// Usage
println(factorial(5, 1))  // 120
```

### FizzBuzz

```tim
@ i in 1..100 {
    result = i % 15 {
        0 -> "FizzBuzz"
        ~> i % 3 {
            0 -> "Fizz"
            ~> i % 5 {
                0 -> "Buzz"
                ~> i
            }
        }
    }
    println(result)
}
```

### List Processing

```tim
// Map, filter, reduce
numbers = [1, 2, 3, 4, 5]

// Map: double each number
doubled = numbers | x -> x * 2

// Filter: only even numbers
evens = numbers | x -> x % 2 == 0 { 1 => x ~> [] }

println(f"Evens: {evens}")
```

### Pattern Matching

```tim
// Value match
classify_number = x -> x {
    0 -> "zero"
    1 -> "one"
    2 -> "two"
    ~> "many"
}

// Guard match
classify_age = age -> {
    | age < 13 -> "child"
    | age < 18 -> "teen"
    | age < 65 -> "adult"
    ~> "senior"
}

// Nested match
check_value = x -> x {
    0 -> "zero"
    ~> x > 0 {
        1 -> "positive"
        0 -> "negative"
    }
}
```

### Error Handling

```tim
// Division with error handling
safe_divide = (a, b) => {
    result = a / b
    result.error {
        "" -> f"Result: {result}"
        ~> f"Error: {result.error}"
    }
}

println(safe_divide(10, 2))   // "Result: 5"
println(safe_divide(10, 0))   // "Error: dv0"

// With or! operator
compute = (a, b) => {
    x = a / b or! 1.0     // Default to 1.0 on error
    y = x * 2
    y
}
```

### Parallel Processing

```tim
data = [1, 2, 3, 4, 5, 6, 7, 8]

// Process in parallel
results = data || x -> expensive_computation(x)

println(f"Results: {results}")
```

### Web Server (ENet)

```tim
// Simple echo server
server =>> {
    @ {
        request <= &8080
        println(f"Received: {request}")
        &8080 <- f"Echo: {request}"
    }
}

// HTTP-like handler
handle_request = req -> {
    method = req.method
    path = req.path

    method {
        "GET" -> path {
            "/" -> "Welcome!"
            "/api" -> "{status: ok}"
            ~> "Not found"
        }
        "POST" -> process_post(req)
        ~> "Method not allowed"
    }
}

server()
```

### C Interop

```tim
// Define C struct
cstruct Buffer {
    data as ptr,
    size as int32,
    capacity as int32
}

// Use C functions with or! for clean error handling
create_buffer = size -> {
    ptr := c.malloc(size) or! {
        println("Memory allocation failed!")
        ret Buffer(0, 0, 0)
    }
    Buffer(ptr, 0, size)
}

// Simpler version with default
create_buffer_safe = size -> {
    ptr := c.malloc(size) or! 0
    Buffer(ptr, 0, size)
}

write_buffer = (buf, data) => {
    buf.size + 1 > buf.capacity {
        1 -> buf  // Buffer full
        ~> {
            c_memcpy(buf.data + buf.size, data, 1)
            buf.size <- buf.size + 1
            buf
        }
    }
}

free_buffer = buf -> {
    buf.data != 0 { c.free(buf.data) }
}

// Usage
buf := create_buffer(1024)
buf := write_buffer(buf, 65)  // Write 'A'
free_buffer(buf)
```

### SDL3 Graphics Example

```tim
import sdl3 as sdl

// Initialize with railway-oriented error handling
init_sdl = () => {
    sdl.SDL_Init(sdl.SDL_INIT_VIDEO) or! {
        println("SDL_Init failed!")
        exit(1)
    }

    window := sdl.SDL_CreateWindow("Demo", 640, 480, 0) or! {
        println("Failed to create window!")
        sdl.SDL_Quit()
        exit(1)
    }

    renderer := sdl.SDL_CreateRenderer(window, 0) or! {
        println("Failed to create renderer!")
        sdl.SDL_DestroyWindow(window)
        sdl.SDL_Quit()
        exit(1)
    }

    ret [window, renderer]
}

// Main rendering loop
main = () => {
    [window, renderer] := init_sdl()

    @ frame in 0..<100 ! 200 {
        // Clear screen to black
        sdl.SDL_SetRenderDrawColor(renderer, 0, 0, 0, 255)
        sdl.SDL_RenderClear(renderer)

        // Draw a red rectangle
        sdl.SDL_SetRenderDrawColor(renderer, 255, 0, 0, 255)
        sdl.SDL_RenderFillRect(renderer, 100, 100, 200, 150)

        // Present
        sdl.SDL_RenderPresent(renderer)
        sdl.SDL_Delay(16)  // ~60 FPS
    }

    // Cleanup
    sdl.SDL_DestroyRenderer(renderer)
    sdl.SDL_DestroyWindow(window)
    sdl.SDL_Quit()
}

main()
```

### Advanced: Custom Allocator

```tim
// Arena allocator pattern
process_requests = requests -> {
    arena {
        results := []
        @ req in requests {
            result = handle_request(req)
            results <- results + [result]
        }
        results
    }
    // All arena memory freed here
}
```

### Advanced: Unsafe Assembly

```tim
// Direct syscall (Linux x86_64)
print_fast = msg -> {
    len = msg.length
    unsafe {
        rax <- 1         // sys_write
        rdi <- 1         // stdout
        rsi <- msg       // buffer
        rdx <- len       // length
        syscall
    }
}

// Atomic compare-and-swap
cas = (ptr, old, new) => unsafe int32 {
    rax <- old
    lock cmpxchg [ptr], new
} {
    1  // Success
} {
    0  // Failed
}
```

## Design Rationale

### Why One Universal Type?

Traditional languages have complex type hierarchies:
- Primitive types (int, float, char, bool)
- Reference types (objects, arrays, strings)
- Special types (null, undefined, NaN)
- Type conversions and coercions
- Boxing/unboxing overhead

**Tim's approach:** Everything is `map[uint64]float64`

**Benefits:**
1. **Conceptual simplicity:** Learn one type, understand the entire language
2. **Implementation simplicity:** One memory layout, one set of operations
3. **No type coercion bugs:** No implicit conversions to reason about
4. **Uniform FFI:** Cast to C types only at boundaries
5. **Natural duck typing:** If it has the key, it works
6. **Optimization freedom:** Compiler can represent values efficiently while preserving semantics

### Why Direct Machine Code Generation?

Most compilers use intermediate representations (IR):
- LLVM IR (Rust, Swift, Clang)
- JVM bytecode (Java, Kotlin, Scala)
- WebAssembly (many languages)
- Custom IR (Go, V8)

**Tim's approach:** AST → Machine code directly

**Benefits:**
1. **Fast compilation:** No IR translation overhead
2. **Small compiler:** ~30k lines vs hundreds of thousands
3. **No dependencies:** Self-contained, no LLVM/GCC required
4. **Predictable output:** Same code every time
5. **Full control:** Optimize for Tim's semantics, not general-purpose IR

**Trade-offs:**
- More code per architecture (x86_64, ARM64, RISCV64)
- Manual optimization (no LLVM optimization passes)
- More maintenance burden

**Why it's worth it:** Tim's simplicity makes per-architecture code manageable. The universal type system means optimizations work uniformly across all values.

### Why Fork-Based Parallelism?

Many languages use threads or async/await:
- Shared memory (requires synchronization)
- Green threads (runtime complexity)
- Async/await (function coloring problem)

**Tim's approach:** `fork()` + ENet channels

**Benefits:**
1. **True isolation:** Separate address spaces
2. **No data races:** No shared memory to corrupt
3. **OS-level scheduling:** Leverage existing scheduler
4. **Simple mental model:** Process per task
5. **Fault isolation:** One process crash doesn't kill others

**Trade-offs:**
- Higher memory overhead per task
- Process creation cost
- IPC overhead for communication

**Why it's worth it:** Safety and simplicity trump performance for most use cases. For hot paths, use threads in unsafe blocks.

### Why ENet for Concurrency?

Traditional approaches:
- Channels (Go, Rust): Good but language-specific
- Actors (Erlang, Akka): Heavy runtime
- MPI: Complex API

**Tim's approach:** ENet-style network channels

**Benefits:**
1. **Familiar model:** Network programming concepts
2. **Local or remote:** Same API for both
3. **Simple implementation:** Thin wrapper over ENet library
4. **Battle-tested:** ENet proven in real-time networking
5. **Scales naturally:** From single machine to distributed

**Design:**
```tim
&8080 <- msg           // Send to local port
&"host:9000" <- msg    // Send to remote host
data <= &8080        // Receive from port
```

Clean, minimal, network-inspired.

### Why No Garbage Collection?

GC languages (Java, Go, Python) have:
- Unpredictable pause times
- Memory overhead (GC metadata)
- Performance cliffs (GC pressure)
- Tuning complexity

**Tim's approach:** Arena allocators + move semantics

**Benefits:**
1. **Predictable performance:** No GC pauses
2. **Low overhead:** No GC metadata
3. **Simple reasoning:** Allocation/deallocation explicit
4. **Natural patterns:** Arena for requests, move for ownership

**Trade-offs:**
- Manual memory management
- Potential for leaks (if not careful)
- More cognitive load

**Why it's worth it:** Systems programming demands predictability. Arenas make manual management tractable.

### Why Minimal Syntax?

Many languages accumulate features:
- Multiple ways to do the same thing
- Special case syntax
- Keyword proliferation

**Tim's approach:** Minimal, orthogonal features

**Examples:**
- One loop construct: `@`
- One function syntax: `=>`
- One block syntax: `{ }`
- Disambiguate by contents, not syntax

**Benefits:**
1. **Easy to learn:** Fewer concepts
2. **Easy to parse:** Simpler compiler
3. **Less bikeshedding:** Fewer style debates
4. **Uniform code:** Looks consistent

**Philosophy:** "One obvious way to do it"

### Why Bitwise Operators Need `b` Suffix?

In C-like languages:
```c
if (x & FLAG)  // Bitwise AND - easy to confuse with &&
if (x | FLAG)  // Bitwise OR - easy to confuse with ||
if (~x)        // Bitwise NOT - easy to confuse with logical !
```

**Tim's approach:** Explicit `b` suffix for bitwise, word operators for logical

```tim
x &b FLAG     // Clearly bitwise AND
x and y       // Clearly logical AND
x | transform // Clearly pipe
x |b mask     // Clearly bitwise OR
not x         // Clearly logical NOT
!b x          // Clearly bitwise NOT
~b x          // Also bitwise NOT (alternative syntax)
x ?b 5        // Bit test: is bit 5 set?
```

**Benefits:**
1. **No ambiguity:** Obvious at a glance
2. **No precedence confusion:** Different operators, different precedence
3. **Frees `|` for pipes:** Pipe operator feels natural
4. **Consistent:** All bitwise ops have `b` suffix
5. **Readable:** `not` is clearer than `!` for logical negation
6. **Efficient bit testing:** `?b` compiles to BT/TEST instructions

### Design Principles Summary

1. **Radical simplification:** One type, one way
2. **Explicit over implicit:** No hidden complexity
3. **Performance without compromise:** Direct code generation
4. **Safety where practical:** Arenas, move semantics, immutable-by-default
5. **Minimal syntax:** Orthogonal features, no redundancy
6. **Predictable behavior:** No GC, no hidden allocations
7. **Systems-level control:** Direct assembly when needed
8. **Familiar concepts:** Borrow from proven designs

**Tim is not trying to be:**
- A replacement for application languages (Python, JavaScript)
- A replacement for safe languages (Rust, Ada)
- A general-purpose language for all domains

**Tim is designed for:**
- Systems programming with radical simplicity
- Performance-critical code with predictable behavior
- Programmers who value minimalism over features
- Domains where direct machine control matters

---

## Frequently Asked Questions

### Is Tim practical for real projects?

Yes, but in specific domains:
- Systems utilities
- Network services
- Embedded systems
- Performance-critical components

Not ideal for:
- Large applications (no module system yet)
- GUI applications (no standard library)
- Rapid prototyping (manual memory management)

### How fast is Tim?

Comparable to C for:
- Arithmetic operations
- Memory operations
- System calls

Slower than C for:
- String operations (map overhead)
- Complex data structures (map overhead)

Faster than C for:
- Compilation (direct code generation)
- FFI (no marshalling overhead)

### Is the universal type system really practical?

Yes, with caveats:
- **Numbers:** Zero overhead (compiler optimizes to registers)
- **Small strings:** Some overhead (map allocation)
- **Large data:** Similar to C (heap allocation either way)
- **FFI:** Zero overhead (direct casts at boundaries)

The compiler's type tracking (see TYPE_TRACKING.md) eliminates most overhead.

### Why not use LLVM?

LLVM would give:
- Better optimization
- More architectures
- Proven backend

But cost:
- 500MB+ dependency
- Slow compilation
- Complex integration
- Loss of control

For Tim's goals (fast compilation, small compiler, direct control), hand-written backends win.

### What about memory safety?

Tim is **not memory-safe by default** like Rust.

However:
- Immutable-by-default reduces bugs
- Arena allocators prevent use-after-free
- Move semantics reduce double-free
- No GC means no GC bugs

Trade-off: Less safe than Rust, simpler to use.

### Can I use Tim in production?

Tim 1.6.0 is ready for:
- Personal projects
- Internal tools
- Experiments
- Performance prototypes

Not yet ready for:
- Mission-critical systems
- Large teams (no module system)
- Long-term maintenance (young language)

Use your judgment.

---

**For grammar details, see [GRAMMAR.md](GRAMMAR.md)**

**For compiler type tracking, see [TYPE_TRACKING.md](TYPE_TRACKING.md)**

**For documentation accuracy, see [LIBERTIES.md](LIBERTIES.md)**

**For development info, see [DEVELOPMENT.md](DEVELOPMENT.md)**

**For known issues, see [FAILURES.md](FAILURES.md)**
