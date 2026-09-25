# Tim Grammar

**Version:** 2.0

This is the canonical grammar of Tim. `lexer.go` and `parser.go` implement it
exactly; anything not described here is not Tim.

Tim is small on purpose. Each idea has one spelling, borrowed from wherever it
reads best:

| From        | Tim takes                                                                  |
|-------------|----------------------------------------------------------------------------|
| Mathematics | exact numbers, `f(x) = x**2` definitions, chained `0 <= i < n`, `..<` `..=`|
| Python      | `and or not in`, truthiness, negative indices, slices, comprehensions      |
| Scheme      | first-class closures, guaranteed tail calls, every block is a value        |
| Haskell     | guard matches `\| x > 0 =>`, `\|>` pipelines, terse `x -> x + 1` lambdas  |
| Rust        | `->` and `=>`, `as` casts, `if` as an expression, immutable by default     |
| Go          | one way to do things, no classes, `defer`, explicit mutability             |
| C           | the operator set, `cstruct`, direct FFI, `printf`                          |
| Assembly    | `unsafe` register blocks, `syscall`, `ret`                                 |

## 1. Notation

EBNF: `=` defines, `|` separates alternatives, `[ ]` is optional, `{ }` repeats
zero or more times, `( )` groups, `"x"` is a terminal. Rules in `UPPER` case are
tokens produced by the lexer.

## 2. Lexical structure

Source is UTF-8.

```ebnf
IDENT    = ( letter | "_" ) { letter | digit | "_" } ;     (* letter: any Unicode letter *)
NUMBER   = DECIMAL | "0x" HEX { [ "_" ] HEX } | "0o" OCT { [ "_" ] OCT } | "0b" BIN { [ "_" ] BIN } ;
DECIMAL  = DIGITS [ "." DIGITS ] [ ( "e" | "E" ) [ "+" | "-" ] DIGITS ] ;
DIGITS   = digit { [ "_" ] digit } ;
STRING   = '"' { char | escape } '"' | "`" { char } "`" ;   (* raw: no escapes, may span lines *)
FSTRING  = 'f"' { char | escape | "{{" | "}}" | "{" expr "}" } '"' ;
escape   = "\n" | "\t" | "\r" | "\0" | "\\" | '\"' | "\{" | "\x" HEX HEX | "\u{" HEX { HEX } "}" ;
```

- **Comments:** `// to end of line` and `/* block */` (block comments do not nest).
- **Numbers** are exact: `0.1` is the rational 1/10, `1e-9` is 1/10⁹. `_` separates
  digit groups: `1_000_000`.
- **Strings** are UTF-8 byte strings; `\u{1F600}` encodes a code point. A raw
  string in backquotes has no escapes and may span lines.
- **Newlines** end statements, except (1) inside `( )` and `[ ]`, (2) after a token
  that cannot end an expression (a binary operator, `,`, `(`, `[`, `{`, `->`, `=>`,
  `~>`, `=`, `:=`, `<-`, `|>`), and (3) before a line that starts with `|>`,
  `.`, `or!`, `and` or `or`. `;` also ends a statement.

**Keywords:**

```
and  as  break  continue  cstruct  defer  elif  else  err  export  if  import
in   inf  no  not  or  ret  unsafe  yes  arena
```

**Operators and punctuation:**

```
+  -  *  /  %  **         arithmetic
== != <  <= >  >=         comparison (chainable)
&  |  ^  ~  << >>         bitwise, on 64-bit two's complement integers
=  := <- += -= *= /= %=   binding and update
-> => ~>  |>  or!  ..< ..= ...  #  .  ,  :  ;  !  @  ( ) [ ] { }
```

## 3. Programs and statements

```ebnf
program    = { stmt END } ;
END        = NEWLINE | ";" ;
block      = "{" { stmt END } [ guardtail ] "}" ;   (* value: the last statement's value *)
guardtail  = { "|" expr "=>" result END } [ "~>" result END ] ;

stmt       = import | export | cstruct
           | fundef | binding | update
           | loop | if | ret | err | break | continue | defer | arena
           | expr ;

import     = "import" ( IDENT | STRING ) [ "as" IDENT ] ;
export     = "export" ( "*" | IDENT { [ "," ] IDENT } ) ;

cstruct    = "cstruct" IDENT [ "packed" ] [ "aligned" "(" NUMBER ")" ] "{" { field [ "," | END ] } "}" ;
field      = IDENT { "," IDENT } ":" IDENT ;          (* int8..uint64, float32, float64, ptr, cstr, or a cstruct *)

fundef     = [ IDENT "." ] IDENT "(" [ params ] ")" [ ":" type ] "=" expr ;
binding    = target [ ":" type ] ( "=" | ":=" ) expr ;
target     = IDENT { "," IDENT } ;
update     = place ( "<-" | "+=" | "-=" | "*=" | "/=" | "%=" ) expr ;
place      = IDENT { "[" expr "]" | "." IDENT } ;

loop       = "@" [ loopspec ] [ "!" expr ] block ;
loopspec   = IDENT [ ":" IDENT ] "in" expr            (* for each element *)
           | expr ;                                   (* while the condition holds *)
break      = "break" [ "@" NUMBER ] ;
continue   = "continue" [ "@" NUMBER ] ;

ret        = "ret" [ expr ] ;
err        = "err" [ expr ] ;
defer      = "defer" expr ;
arena      = "arena" block ;
```

### Bindings

```tim
x = 42            // immutable binding
n := 0            // mutable binding
n <- n + 1        // update the nearest binding named n (it must be mutable)
n += 1            // same as n <- n + 1
xs[0] <- 7        // update an element of a mutable list or map
p.x <- 1.5        // update a field of a cstruct or a map
a, b = pair       // destructure a list: a = pair[0], b = pair[1]
count: num = 0    // optional type annotation
```

A binding introduces a new name in the current block; binding a name that already
exists in an *outer* block shadows it. Binding the same name twice in one block is
an error.

### Functions

A function is a value. There are two ways to write one:

```tim
square(x) = x * x                       // definition: name(params) = body
add = (a, b) -> a + b                   // lambda bound to a name
inc = x -> x + 1                        // one parameter needs no parentheses
greet = { println("hi") }               // a block on the right of `=` is a function of no arguments
sum(xs...) = fold(xs, 0, (a, b) -> a + b)   // the last parameter may be variadic
Point.norm(self) = sqrt(self.x**2 + self.y**2)   // method on a cstruct: p.norm()
```

`name(params) = body` at statement level is sugar for `name = (params) -> body`, and
top-level functions may call each other in any order. Every call in tail position
is a jump, so tail recursion runs in constant stack space. Closures capture
variables by reference.

### Loops

```tim
@ { ... }                    // forever
@ i in 0..<10 { ... }        // i = 0, 1, ..., 9
@ x in xs { ... }            // each element of a list, each byte of a string, each key of a map
@ n > 1 { ... }              // while n > 1
@ x in xs ! 1000 { ... }     // at most 1000 iterations
```

`break` and `continue` act on the innermost loop; `break @1` names loops by depth,
`@1` being the outermost. A loop's value is 0.

## 4. Expressions

From lowest to highest precedence:

```ebnf
expr       = lambda | pipe { matchblock } ;         (* a match applies to the whole expression *)
lambda     = params "->" ( expr | block ) ;
params     = IDENT | "(" [ param { "," param } ] ")" ;
param      = IDENT [ ":" type ] [ "..." ] ;

pipe       = orbang { "|>" orbang } ;                 (* x |> f  is f(x);  x |> f(y)  is f(x, y) *)
orbang     = or { "or!" or } ;                        (* a or! b  is a unless a is an error or 0 *)
or         = and { "or" and } ;
and        = not { "and" not } ;
not        = "not" not | compare ;
compare    = range { cmpop range } ;                  (* a < b < c  means  a < b and b < c *)
cmpop      = "==" | "!=" | "<" | "<=" | ">" | ">=" | "in" | "not" "in" ;
range      = bitor [ ( "..<" | "..=" ) bitor ] ;
bitor      = bitxor { "|" bitxor } ;
bitxor     = bitand { "^" bitand } ;
bitand     = shift { "&" shift } ;
shift      = sum { ( "<<" | ">>" ) sum } ;
sum        = product { ( "+" | "-" ) product } ;
product    = cast { ( "*" | "/" | "%" ) cast } ;
cast       = unary { "as" type } ;
unary      = ( "-" | "~" | "#" ) unary | power ;
power      = postfix [ "**" unary ] ;                 (* right-associative; -2**2 is -4 *)
postfix    = primary { "(" [ args ] ")" | "[" index "]" | "." IDENT } ;
args       = expr { "," expr } ;
index      = expr | [ expr ] ":" [ expr ] ;           (* xs[i], xs[a:b], xs[:b], xs[a:] *)

primary    = NUMBER | STRING | FSTRING | "yes" | "no" | "inf" | IDENT
           | "(" expr ")" | list | map | block | guards | if | unsafe ;
list       = "[" [ expr ( { "," expr } | "@" IDENT "in" expr [ "if" expr ] ) ] "]" ;
map        = "{" "}" | "{" key ":" expr { "," key ":" expr } "}" ;
key        = IDENT | STRING | NUMBER ;
if         = "if" expr block { "elif" expr block } [ "else" block ] ;

type       = "num" | "str" | "bool" | "list" | "map" | "fn" | IDENT ;   (* IDENT: a cstruct or C type *)
```

### Matching

A `{ ... }` directly after an expression, on the same line, matches on its value:

```ebnf
matchblock = "{" ( arms | { stmt END } ) "}" ;
arms       = { arm END } [ "~>" result END ] ;
arm        = expr "=>" result                         (* value equals expr, or lies in a range *)
           | "=>" result ;                            (* value is truthy *)
guards     = "{" { "|" expr "=>" result END } [ "~>" result END ] "}" ;
result     = expr | block | ret | err | break | continue ;
```

```tim
kind = n {
    0 => "zero"
    1..<10 => "small"
    ~> "large"
}
sign = {
    | x > 0 => 1
    | x < 0 => -1
    ~> 0
}
clamp(x, lo, hi) = {
    | x < lo => ret lo       // guard lines followed by statements are early exits
    | x > hi => ret hi
    x
}
n % 15 == 0 { println("FizzBuzz") }     // no arms: run the block when the value is truthy
ok = x > 0 { => "positive" ~> "not positive" }
```

Guard lines may also appear inside any block: when they end the block they form
a guard match that is the block's value; followed by more statements, each
`| c => r` means `if c { r }`.

The value `v` is evaluated once. Arms are tried in order; a pattern arm matches when
`v == pattern`, or `v in pattern` for a range pattern. The first match wins; with no
match the value is the `~>` result, or 0.

### Blocks

`{` starts a map when it is followed by `}` or by `key :` (but not `IDENT : type =`,
which is an annotated binding); after an expression on the same line it starts a
match; `{ |` starts a guard match; otherwise it is a block. A block is an expression
whose value is its last statement's value. A block that is the entire right-hand
side of a *binding* (`name = { ... }`) is a function of no arguments; the block of a
definition (`f(x) = { ... }`) or a lambda (`x -> { ... }`) is its body.

### Literals and collections

```tim
xs = [1, 2, 3]
squares = [x * x @ x in xs if x > 1]     // comprehension: [4, 9]
xs[0]  xs[-1]  xs[1:]  #xs               // 1, 3, [2, 3], 3
m = {name: "Tim", "two words": 2, 7: yes}
m.name  m["two words"]  m[7]  "name" in m
s = "héllo"
#s  s[0]  s[1:3]                        // 6 bytes, 104, "é"
f"{s} has {#s} bytes"
```

## 5. Values

| Type   | Values                                                                        |
|--------|-------------------------------------------------------------------------------|
| `num`  | exact integers and rationals of any size, and inexact float64                 |
| `bool` | `yes` and `no`, which are the numbers 1 and 0                                 |
| `str`  | immutable UTF-8 byte strings                                                  |
| `list` | ordered, indexed from 0; negative indices count from the end                  |
| `map`  | hash maps from numbers or strings to values                                   |
| `fn`   | functions and closures                                                        |
| error  | a value carrying a short error code, produced by failing operations or `err`  |

- **Numbers.** `+ - * / % **` are exact on exact operands: `7 / 2` is `3.5`,
  `2 ** 100` is exact, `0.1 + 0.2 == 0.3`. `sqrt`, `sin`, `log` and friends, C
  `float`/`double`, and `x as float64` give inexact float64; any arithmetic with an
  inexact operand is inexact.
- **Truthiness.** `0`, `no`, `""`, `[]`, `{}` and errors are false; everything else is true.
- **Equality** compares numbers by value, strings and lists by content, maps and
  functions by identity.
- **Errors.** `10 / 0`, `xs[99]` and `m["missing"]` evaluate to errors, and
  arithmetic on an error propagates it. `v.error` is the error's code as a string
  (`""` for a non-error), `v or! d` substitutes `d`, and `err "code"` returns an
  error from the current function. Printing an error prints `error: <message>`.
- **Memory.** Values live on a garbage-collected heap. `arena { ... }` runs its
  block; it is kept for programs written for manual arenas.

## 6. Unsafe code

`unsafe` gives direct access to registers, memory and system calls, with one block
per architecture:

```ebnf
unsafe     = "unsafe" [ ctype ] ublock ublock ublock [ "as" ctype ] ;   (* x86_64, arm64, riscv64 *)
ublock     = "{" { ustmt END } "}" ;
ustmt      = REG "<-" ( uexpr | "[" REG [ ( "+" | "-" ) NUMBER ] "]" [ "as" ctype ] )
           | "[" REG [ ( "+" | "-" ) NUMBER ] "]" "<-" ( REG | NUMBER ) [ "as" ctype ]
           | "syscall" ;
uexpr      = ( REG | NUMBER | IDENT ) [ uop ( REG | NUMBER ) ] | "~" REG ;
uop        = "+" | "-" | "*" | "/" | "%" | "&" | "|" | "^" | "<<" | ">>" ;
```

```tim
pid = unsafe int64 {
    rax <- 39
    syscall
} {
    x8 <- 172
    syscall
} {
    a7 <- 172
    syscall
}
```

The value of `unsafe` is the return register (`rax`, `x0`, `a0`) read as `ctype`.

## 7. Program execution

Top-level statements run in order. If the program defines a function `main` and
never calls it at top level, `main()` runs after the top level. The exit code is the
value of the last top-level statement (or of `main()`), truncated to an integer; a
non-number exits with 0. `ret v` at top level exits with `v`.

## 8. What changed from Tim 1

| Tim 1                                    | Tim 2                                        |
|------------------------------------------|----------------------------------------------|
| `&b \|b ^b ~b <<b >>b`                   | `& \| ^ ~ << >>`                             |
| `?b`, `<<<b`, `>>>b`, `!b`               | `bit(x, n)`, `rotl(x, n)`, `rotr(x, n)`, `~` |
| `^` as a power operator                  | `**` only                                    |
| `x \| f` pipe, `\|\|` parallel map       | `x \|> f`, `map(xs, f)`                      |
| `f <> g` composition, `::` cons          | lambdas, `[x] + xs`                          |
| `yes`/`no` as heap maps                  | the numbers 1 and 0                          |
| `field as type` in `cstruct`, `(p as T)` | `field: type`, `(p: T)`                      |
| `fun f(x) { }`                           | `f(x) = { }`                                 |
| `ret @N`, `@N`, `foreach`                | `break @N`, `continue @N`, `@`               |
| `\| cond => stmt` guard statements       | `cond { stmt }` or `if`                      |
| `_ =>` default arm                       | `~>`                                         |
| `??`, `µ`, `$`, `¤`, `x++`, postfix `#`  | `random()`, removed, `+= 1`, `#x`            |
| `call()!`, `! N` recursion bounds        | removed: FFI signatures carry the types      |
| ENet `&8080`, `<-` send, `<=` receive    | removed from the language                    |
| `class`, `with`, `alias`, `spawn`        | removed: use functions, maps and cstructs    |
| `shadow`                                 | inner blocks shadow freely                   |
| `@first` `@last` `@counter` `@i`         | removed                                      |
| condition loops require `! N`            | `! N` is optional everywhere                 |
