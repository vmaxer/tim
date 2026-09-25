# Tim

Tim is a small programming language and a compiler that writes machine code
directly: no LLVM, no assembler, no linker. It targets Linux on x86-64, arm64
and riscv64, Windows on x86-64 and arm64, and macOS on arm64.

```tim
fact(n) = n <= 1 { => 1 ~> n * fact(n - 1) }
println(fact(30))                       // 265252859812191058636308480000000

words = split("the quick brown fox", " ")
println([upper(w) @ w in words if #w > 3])   // ["QUICK", "BROWN"]
println(0.1 + 0.2 == 0.3, 7 / 2)        // 1 3.5
```

## Install

```sh
go install github.com/vmaxer/tim@latest
tim hello.tim -o hello && ./hello
tim --os windows --arch arm64 hello.tim -o hello.exe
```

## The language in a minute

- **One number type.** Integers of any size and rationals are exact; `sqrt`,
  `sin` and `as float64` give float64. `yes` and `no` are 1 and 0.
- **Bindings.** `x = 1` is immutable, `n := 0` is mutable and `n <- n + 1`
  (or `n += 1`) updates it.
- **Functions** are values: `square(x) = x * x`, `inc = x -> x + 1`.
  Closures capture by reference and every tail call is a jump.
- **Matching.** A block after an expression matches on its value:
  `n { 0 => "zero" 1..<10 => "small" ~> "large" }`; `{ | x > 0 => 1 ~> -1 }`
  is a guard match.
- **Loops** all start with `@`: `@ i in 0..<10 { }`, `@ x in xs { }`,
  `@ n > 1 { }`, `@ { }`, with `break`, `continue` and optional `! N` bounds.
- **Errors are values.** `10 / 0` and `xs[99]` are errors that print as
  `error: division by zero`; `x or! default` replaces an error, `v.error`
  reads its code and `err "code"` returns one.
- **Memory** is garbage collected.
- **C interop.** `import sdl3 as sdl` makes C functions callable as `sdl.SDL_Init(...)`.

The full grammar and semantics are in [GRAMMAR.md](GRAMMAR.md) and the
builtin functions in [STDLIB.md](STDLIB.md).

## How it works

The compiler lexes and parses (`lexer.go`, `parser.go`), then checks names,
mutability, arities and the types it can infer (`check.go`), reporting errors
with source context and suggestions. The core code generator (`core.go`)
compiles through a small interface that each instruction set implements
(`core_x86.go`, `core_a64.go`, `core_rv64.go`), and writes ELF, PE or Mach-O
files. Values are NaN-boxed 64-bit words: plain doubles get inline fast paths,
and everything else calls the runtime, `runtime/rt.c`, a freestanding C
library with exact arithmetic, UTF-8 strings, lists, maps and a garbage
collector, embedded in each executable as a position-independent blob.

Programs that use C libraries, cstructs, `unsafe` or `defer` are compiled by
the older backends (`codegen.go`, `arm64_codegen.go`, `riscv64_codegen.go`).

## Development

```sh
go test ./...                              # includes cross-architecture tests when qemu is installed
TIM_UPDATE=1 go test -run TestCorePrograms # rewrite testdata/core/*.want
sh runtime/build.sh                        # rebuild the runtime blobs (clang, ld.lld)
TIM_LEGACY=1 tim prog.tim                  # force the legacy backends
```

License: The Unlicense.
