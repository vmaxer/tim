# TODO

- Lambdas nested inside an assignment take the assignment's name (`codegen.go`, `case *LambdaExpr`, `fc.currentAssignName`), so `xs = [z -> a, z -> b]` or a map of inline lambdas makes every entry call the last lambda. Give non-top-level lambdas unique names.
- Zero-argument functions called indirectly do nothing: `f = { println("x") }; lst = [f]; lst[0]()` or `g = m.fn; g()` prints nothing. One-argument lambdas work.
- `and` / `or` do not short-circuit: `w == 0 or w(0)` calls `w(0)`. Make them short-circuit.
- `m[k] <- v` with a runtime string key `k` that is not yet in the map inserts numeric key `0` instead of `k` (existing keys update correctly).
- `read_file` / `write_file` segfault (`write_file(path, "a\n")` then `read_file(path)`).
- `for c.fgets(buf, 256, f) != 0 ! 100 { ... }` never runs the loop body, and the program then segfaults.
- `str(p as cstr)` returns `""` when `p` is a `char*` returned by a C function (e.g. `sdl.SDL_GetPrefPath`), and `#p` on such a value segfaults, although `println(p)` prints it correctly.
- `eprintln` prints nothing for string values from C (`eprintln("a", a)` prints only `a`).
- ELF: dynsym, dynstr, hash and rela.plt are packed into the one page at 0x401000. When they outgrow it (around 31 imported C functions; JMPREL 0x401d38 + PLTRELSZ 744 = 0x402020), they spill past 0x402000 and the program segfaults before the first statement runs. Size that region from the import count (`elf_complete.go`).
- Programs that call `c.fopen` and then `c.fclose` / `c.fseek` on the result segfault at exit.
- `c.strtod("-3.25", 0)` prints `0	0`; `c.atof` works.
- `SDL_AUDIO_DEVICE_DEFAULT_PLAYBACK` (a cast macro `((SDL_AudioDeviceID) 0xFFFFFFFFu)`) is not picked up as a constant from the SDL3 headers.
