# Tim builtin functions

Every program can call these. A program may define a function with the same
name, which then takes precedence. Functions that fail return an error value.

## Output and the process

| Function | Description |
|---|---|
| `print(v...)` | print values separated by spaces |
| `println(v...)` | print values separated by spaces, then a newline |
| `printf(fmt, v...)` | print with a format: `%d %x %X %f %.2f %e %g %s %v %q %c %b %%`, with widths like `%5d` and `%-8s` |
| `eprint(v...)`, `eprintln(v...)`, `eprintf(fmt, v...)` | the same, to stderr |
| `exit(code)` | end the program; an error exits with 1 |
| `args()` | the command-line arguments as a list of strings |
| `env(name)` | an environment variable, or an error |

## Files and input

| Function | Description |
|---|---|
| `readln()` | a line from stdin without its newline, or an error at the end of input |
| `read_file(path)` | a whole file as a string, or an error |
| `write_file(path, s)` | write a string, returning its length or an error |

## Conversions

| Function | Description |
|---|---|
| `str(v)` | a value as a string, as `println` shows it |
| `num(s)` | parse `42`, `-1_000`, `0.5`, `1e-9`, `0xff`, `0b101`; exact; an error otherwise |
| `float(x)` | a number as float64 |
| `type(v)` | `"num"`, `"str"`, `"list"`, `"map"`, `"fn"`, `"error"` or `"ptr"` |
| `error(v)` | an error value with the given code or message |
| `chr(n)`, `ord(s)` | a code point as UTF-8, and the first code point of a string |
| `bytes(s)`, `runes(s)` | a string's bytes, or its code points, as a list |

Casts: `x as str`, `x as num`, `x as float64`, `x as int64` (truncates), `x as bool`.

## Numbers

| Function | Description |
|---|---|
| `abs floor ceil round trunc` | exact on exact numbers; `round` rounds halves away from zero |
| `sqrt exp log log10 sin cos tan asin acos atan` | float64 |
| `atan2(y, x)`, `pow(x, y)` | `pow` is `x ** y` |
| `min(v...)`, `max(v...)` | of the arguments, or of one list |
| `gcd(a, b)` | the greatest common divisor of two integers |
| `random()` | a float64 in [0, 1) |
| `bit(x, n)`, `rotl(x, n)`, `rotr(x, n)` | bit n of x, and 64-bit rotations |

## Strings

| Function | Description |
|---|---|
| `upper(s)`, `lower(s)`, `trim(s)` | ASCII case and surrounding whitespace |
| `split(s, sep)`, `join(xs, sep)` | `split(s, "")` splits into characters |
| `replace(s, old, new)` | every occurrence |
| `starts_with(s, p)`, `ends_with(s, p)` | 1 or 0 |
| `find(s, sub)` | the byte index of `sub`, or -1; also finds an element in a list |
| `reverse(s)` | by code point |

`#s` is the length in bytes, `s[i]` a byte, `s[a:b]` a substring, `+` joins
and `*` repeats.

## Lists and maps

| Function | Description |
|---|---|
| `push(xs, v)`, `pop(xs)` | append to, or remove and return the last element of, a mutable list |
| `keys(m)`, `values(m)` | in insertion order |
| `remove(c, k)` | remove and return a map entry or a list element |
| `sort(xs)`, `sort(xs, key)` | a stable sorted copy |
| `reverse(xs)`, `sum(xs)` | |
| `map(xs, f)`, `filter(xs, f)`, `fold(xs, init, f)` | `fold(xs, 0, (acc, x) -> acc + x)` |
| `any(xs, f)`, `all(xs, f)` | 1 or 0 |
| `zip(a, b)`, `enumerate(xs)` | lists of pairs |

`#xs`, `xs[i]` (negative from the end), `xs[a:b]`, `x in xs`, `a + b`,
`xs * n`; `m[k]`, `m.name`, `k in m`.
