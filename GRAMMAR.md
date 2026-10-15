# Tim Grammar

**Version:** 2.0
**Date:** 2026-10-15

This is the canonical grammar of Tim. It describes exactly what the compiler
accepts; the semantics of each construct are in
[LANGUAGESPEC.md](LANGUAGESPEC.md).

Tim 2.0 keeps one spelling for each idea. The [migration table](#migrating-from-tim-1x)
at the end lists the 1.x spellings that are gone and what replaces them.

## Contents

- [Notation](#notation)
- [Lexical structure](#lexical-structure)
- [Programs and statements](#programs-and-statements)
- [Expressions](#expressions)
- [Operator precedence](#operator-precedence)
- [Blocks: map, match or statements](#blocks-map-match-or-statements)
- [Functions](#functions)
- [Loops](#loops)
- [Declarations and modules](#declarations-and-modules)
- [Migrating from Tim 1.x](#migrating-from-tim-1x)

## Notation

The grammar is written in EBNF:

| Notation    | Meaning                 |
|-------------|-------------------------|
| `a = b ;`   | definition              |
| `a \| b`    | alternative             |
| `[ a ]`     | optional                |
| `{ a }`     | zero or more            |
| `( a )`     | grouping                |
| `"if"`      | literal token           |
| `NL`        | a newline token         |

## Lexical structure

Source files are UTF-8. A first line starting with `#!` is ignored.

### Whitespace, newlines and comments

Spaces, tabs and carriage returns separate tokens. Newlines are tokens: they
end statements. A semicolon does the same, so `x := 1; y := 2` is two
statements.

A newline does not end a statement:

- inside `( )`, `[ ]` and the entries of a map literal,
- after a binary operator (`a +` at the end of a line continues on the next),
- before the `{` of a loop or `if` body.

`//` starts a comment that runs to the end of the line. There are no block
comments.

### Identifiers

```ebnf
identifier = letter { letter | digit | "_" } ;
```

Letters and digits are Unicode (`λ`, `größe` and `x2` are identifiers). An
identifier cannot start with `_`: a lone `_` is the wildcard of a match, and
names starting with `_` are reserved for the compiler.

### Keywords

These words are reserved:

```
aligned   and       arena     as        bool      break     continue
cstruct   defer     elif      else      err       export    false
for       fun       if        import    in        inf       no
not       or        packed    ret       return    shadow    spawn
syscall   true      unsafe    while     with      xor       yes
```

`true`/`false` are spellings of `yes`/`no`, and `return` of `ret`.

Type names (`num`, `str`, `list`, `map`, `cstring`, `cptr`, `cint`, `clong`,
`cfloat`, `cdouble`, `cbool`, `cvoid`, `int8` … `uint64`, `float32`,
`float64`, `f64`, `u8`, …) are not reserved. They are types after `:`, `as`
and a function's `->`, and ordinary names elsewhere.

### Numbers

```ebnf
number   = decimal | "0x" hexdig { hexdig | "_" }
         | "0o" octdig { octdig | "_" } | "0b" bindig { bindig | "_" } ;
decimal  = digits [ "." digits ] [ ( "e" | "E" ) [ "+" | "-" ] digits ] ;
digits   = digit { digit | "_" } ;
```

`1_000_000`, `0xFF`, `0o755`, `0b1010`, `3.14` and `6.02e23` are numbers.
Underscores separate digit groups and are otherwise ignored. A number ends
before `..`, so `0..10` is a range. A number running into letters
(`12abc`, `0x`, `0b2`) is an error.

Every literal is exact: `0.1 + 0.2 == 0.3` holds. A leading `-` is the
unary minus operator, not part of the literal.

### Strings

```ebnf
string  = '"' { char | escape } '"' ;
fstring = 'f"' { char | escape | "{{" | "}}" | "{" expression "}" } '"' ;
escape  = "\" ( "n" | "t" | "r" | "0" | "e" | "a" | "b" | "f" | "v"
              | "\" | '"' | "'" | "{" | "}" )
        | "\x" hexdig hexdig
        | "\u" hexdig hexdig hexdig hexdig
        | "\u{" hexdig { hexdig } "}" ;
```

Strings may span lines. A backslash before any other character is kept as
written. Escapes are decoded once, when the string is read: `"a\\nb"` is
the four characters `a`, `\`, `n`, `b`, everywhere, including `printf`
formats.

In an f-string, `{expression}` interpolates and `{{`/`}}` are literal braces.
Interpolations may contain strings and nested f-strings:
`f"{name}: {f"{n}%"}"`.

### Other literals

| Literal     | Meaning                                   |
|-------------|-------------------------------------------|
| `yes`, `no` | the numbers 1 and 0                       |
| `inf`       | positive infinity (also an unbounded loop bound) |
| `??`        | a cryptographically random number in [0, 1) |

### Operators and punctuation

```
+  -  *  /  %  **  #
== != <  <= >  >=
=  := <-  += -= *= /= %= **= <<= >>= <<<= >>>=
-> => ~> |  || <> :: .. ..< ..= ... ? or! as!
&b |b ^b ~b ?b << >> <<< >>>
!  &  @  @N  _  :  .  ,  ;  ( ) [ ] { }
```

Tokens are read greedily: `<<=` is one token, and so is `..<`. The bitwise
operators ending in `b` are tokens only when no letter or digit follows:
`x &b y` is a bitwise and, `x &bits` is not.

Any other character, including `^`, `$` and `` ` ``, is an error.

## Programs and statements

```ebnf
program     = { statement terminator } ;
terminator  = NL | ";" ;
block       = "{" { statement terminator } "}" ;

statement   = assignment
            | update
            | expression_statement
            | if_statement
            | guard_statement
            | loop
            | jump
            | defer_statement
            | with_statement
            | spawn_statement
            | arena_block
            | unsafe_expression
            | fun_definition
            | cstruct_decl
            | import_statement
            | export_statement ;
```

A program is a list of statements. When it defines `main`, the compiler
calls it after the top-level statements run, unless they call it themselves.
The value of `main` (or of the last top-level expression) is the exit code.

### Assignment

```ebnf
assignment  = [ "shadow" ] identifier [ ":" type ] ( "=" | ":=" ) expression
            | identifier { "," identifier } ( "=" | ":=" | "<-" ) expression { "," expression } ;
update      = identifier ( "<-" | compound_op ) expression
            | identifier "[" expression "]" "<-" expression
            | identifier "." identifier "<-" expression
            | identifier ( "++" | "--" ) ;
compound_op = "+=" | "-=" | "*=" | "/=" | "%=" | "**="
            | "<<=" | ">>=" | "<<<=" | ">>>=" ;
```

- `x = v` binds an immutable name, `x := v` a mutable one, and `x <- v`
  updates a mutable name. `x += v` is `x <- x + v`.
- `shadow x = v` declares a name that hides one from an enclosing scope.
  Without `shadow`, such a declaration is an error.
- `a, b = xs` binds the first elements of a list (missing ones are 0).
  With several expressions on the right, `a, b <- b, a`, all of them are
  evaluated before any name changes, so this swaps.
- `xs[i] <- v` and `m.key <- v` update one entry of a mutable list or map.
- `x++` and `x--` are statements, never expressions.

### Expression statements and conditionals

```ebnf
expression_statement = expression [ match_block ] ;
if_statement   = "if" expression block { "elif" expression block } [ "else" block ] ;
guard_statement = "|" expression "=>" match_target ;
```

`if` is also an expression: `y = if x > 3 { "big" } else { "small" }`.

A guard statement is an early exit written as a guard clause, at the start of
a line in a statement block:

```tim
divide = (a, b) -> {
    | b == 0 => err "division by zero"
    a / b
}
```

### Jumps and deferral

```ebnf
jump            = "ret" [ expression ]
                | "err" [ expression ]
                | "break" [ "@" digits ]
                | "continue" [ "@" digits ] ;
defer_statement = "defer" expression ;
```

`ret` returns from the enclosing function. `err msg` returns an error value
(`ret error(msg)`). `break` and `continue` act on the innermost loop, or on
loop `@N`, counting from the outermost loop of the function as `@1`.

Deferred expressions run when the function returns, last first.

### `with`, `spawn`, `arena`

```ebnf
with_statement  = "with" expression block ;
spawn_statement = "spawn" expression [ "|" identifier { "," identifier } "|" block ] ;
arena_block     = "arena" block ;
```

In `with subject { … }`, every statement of the block that is a plain call
gets `subject` as its first argument: `with w { draw(1); flush() }` is
`draw(w, 1); flush(w)`.

`spawn` runs an expression in a child process. `arena { … }` is also an
expression whose value is the block's last expression; memory allocated
inside is freed when it ends.

## Expressions

```ebnf
expression   = ternary ;
ternary      = pipe [ "?" pipe ":" ternary ] ;
pipe         = or_bang { ( "|" | "||" ) or_bang } ;
or_bang      = compose [ "or!" ( block | or_bang ) ] ;
compose      = logic_or { "<>" logic_or } ;
logic_or     = logic_and { ( "or" | "xor" ) logic_and } ;
logic_and    = negation { "and" negation } ;
negation     = "not" negation | comparison ;
comparison   = conversion ( "in" conversion | { compare_op conversion } ) ;
conversion   = cons { ( "as" | "as!" ) type } ;
compare_op   = "==" | "!=" | "<" | "<=" | ">" | ">=" ;
cons         = range [ "::" cons ] ;
range        = additive [ ( ".." | "..=" | "..<" ) additive [ ".." additive ] ] ;
additive     = bitwise { ( "+" | "-" ) bitwise } ;
bitwise      = multiplicative { bitwise_op multiplicative } ;
bitwise_op   = "&b" | "|b" | "^b" | "?b" | "<<" | ">>" | "<<<" | ">>>" ;
multiplicative = unary { ( "*" | "/" | "%" ) unary } ;
unary        = ( "-" | "#" | "~b" ) unary | power ;
power        = postfix [ "**" unary ] ;
postfix      = primary { call | index | field | "!" } ;
call         = "(" [ argument { "," argument } [ "," ] ] ")" ;
argument     = expression [ match_block | "..." ] ;
index        = "[" expression "]"
             | "[" [ expression ] ":" [ expression ] [ ":" [ expression ] ] "]"
             | "[" [ expression ] "::" expression "]" ;
field        = "." identifier ;

primary      = number | string | fstring | "yes" | "no" | "inf" | "??"
             | identifier
             | "(" expression ")"
             | list_literal
             | block_expression
             | lambda
             | if_expression
             | arena_block
             | unsafe_expression ;

list_literal = "[" [ expression { "," expression } [ "," ] ] "]" ;
```

Notes:

- **Comparisons chain** as in Python: `0 <= i < n` means `0 <= i and i < n`.
  The middle operand is evaluated twice, so it may not call a function.
- **`not`** binds looser than comparisons: `not x == y` is `not (x == y)`.
- **Unary minus** binds looser than `**` and tighter than `*`: `-x ** 2` is
  `-(x ** 2)`, and `2 ** -1` is 0.5. `-xs[0]` negates the element.
- **`#`** is the length prefix: `#xs`, `#"abc"`, `#xs[0]`.
- **Bitwise operators** share one level, tighter than `+`: `1 + 2 &b 3` is
  `1 + (2 &b 3)`.
- **Ranges**: `a..b` and `a..=b` include `b`, `a..<b` excludes it, and a
  third operand is the step: `0..100..10`. A range used as a value is the
  list of its elements.
- **`::`** prepends: `1 :: 2 :: [3]` is `[1, 2, 3]`.
- **`|`** pipes: `xs | f` applies `f` to each element of a list, or to a
  single value. `xs || f` does the same in parallel.
- **`<>`** composes: `(f <> g)(x)` is `f(g(x))`.
- **`or!`** gives a fallback when its left side is an error or a null
  pointer. The right side is evaluated only then, and may be a block:
  `window := create() or! { println("failed"); ret 1 }`.
- **`? :`** is the conditional expression: `x > 0 ? "pos" : "non-pos"`.
- **Calls**: `obj.method(args)` is `method(obj, args)`, and `ns.f(args)`
  calls `f` from an imported namespace or C library. `xs...` spreads a list
  into a variadic parameter. Arguments may span lines and end with a comma.
- **`expr!`** moves a value; using the name afterwards is an error. After a
  call it reinterprets the returned bits: `f()!`.
- **Slices** follow Python: `xs[1:3]`, `xs[:2]`, `xs[::2]`, `xs[::-1]`.
- **`x as T`** converts, binding looser than arithmetic and tighter than
  comparisons: `1 / 3 as! float64` converts the quotient, `a + b as T` the
  sum. `:` annotates declarations and never converts.
- **`as` never loses information.** A conversion to a number type that is
  not exact is an error: `3.7 as int32` and `0.1 as float64` are `arg`
  errors, `300 as uint8` is an `ovf` error, and each is a compile error when
  the operand is a literal.
- **`as!` converts anyway.** To an integer type it rounds like `round` and
  keeps the low bits (`3.7 as! int8` is 4, `300 as! uint8` is 44, `-1 as!
  uint8` is 255); to a float type it rounds to the nearest float
  (`1 / 3 as! float64` is 0.333333).

## Operator precedence

From loosest to tightest:

| Level | Operators                               | Associativity |
|------:|-----------------------------------------|---------------|
| 1     | `? :`                                   | right         |
| 2     | `\|` `\|\|`                             | left          |
| 3     | `or!`                                   | right         |
| 4     | `<>`                                    | right         |
| 5     | `or` `xor`                              | left          |
| 6     | `and`                                   | left          |
| 7     | `not`                                   | prefix        |
| 8     | `==` `!=` `<` `<=` `>` `>=` `in`        | chaining (`in` does not chain) |
| 9     | `as` `as!`                              | left          |
| 10    | `::`                                    | right         |
| 11    | `..` `..=` `..<`                        | none          |
| 12    | `+` `-`                                 | left          |
| 13    | `&b` `\|b` `^b` `?b` `<<` `>>` `<<<` `>>>` | left       |
| 14    | `*` `/` `%`                             | left          |
| 15    | `-` `#` `~b`                            | prefix        |
| 16    | `**`                                    | right         |
| 17    | call, index, `.field`, `!`              | postfix       |

## Blocks: map, match or statements

A `{` that starts an expression opens one of three kinds of block. The
parser decides by scanning the block's top level (ignoring nested brackets):

1. **Map literal**: the first entry has the form `key:`, or the block is
   `{}`.
2. **Match block**: it contains `=>` or `~>`.
3. **Statement block**: anything else. Its value is its last expression.

```ebnf
map_literal = "{" [ entry { "," entry } [ "," ] ] "}" ;
entry       = ( identifier | expression ) ":" expression ;

match_block = "{" { arm } [ default_arm ] "}" ;
arm         = expression "=>" match_target        (* value: subject == expression *)
            | compare_op range "=>" match_target  (* comparison: subject < 3 *)
            | "in" range "=>" match_target        (* membership: subject in 1..5 *)
            | "|" expression "=>" match_target    (* guard, at the start of a line *)
            | "=>" match_target                   (* the subject is true *)
            | match_target ;                      (* the subject is true *)
default_arm = ( "~>" | "_" "=>" ) match_target ;
match_target = expression | block | jump | assignment | loop | if_statement ;
```

In a map literal a bare identifier key is a name, `{a: 1}` is
`{"a": 1}`; any other key is an expression. Entries may sit on separate lines.

A match block follows its **subject**:

```tim
score := 82
grade = score {
    >= 90 => "A"
    in 75..<90 => "B"
    50 => "exactly fifty"
    ~> "C"
}

x > 0 { println("positive") ~> println("not positive") }
```

Arms are tried in order. The subject is evaluated once, before any arm.
Without a matching arm and without a default, the value is 0.

A block whose arms are all guards needs no subject:

```tim
sign = x -> {
    | x > 0 => 1
    | x < 0 => -1
    ~> 0
}
```

A `|` that starts a line inside a block is a guard; anywhere else it pipes.

## Functions

```ebnf
lambda      = params "->" [ type ] lambda_body
            | "(" [ param_list ] ")" block
            | "->" lambda_body ;
params      = identifier | "(" [ param_list ] ")" ;
param_list  = param { "," param } [ "," identifier "..." ] | identifier "..." ;
param       = identifier [ ( ":" | "as" ) type ] ;
lambda_body = block | expression [ match_block ] ;

fun_definition = "fun" [ identifier "." ] identifier "(" [ param_list ] ")"
                 ( [ "->" type ] block | "->" expression | "=" expression ) ;
```

```tim
square = x -> x * x
add = (a, b) -> a + b
scale = (v: num, k: num) -> num { v * k }
greet = -> println("hi")
main = { println("hello") }              // a block assigned to a name is a function
total = (xs...) -> { t := 0; for x in xs { t += x }; t }
fun area(w, h) = w * h
fun Point.norm() { sqrt(self.x ** 2 + self.y ** 2) }
classify = n -> n { 0 => "zero" ~> "other" }
```

- A statement block assigned with `=` is a function of no arguments:
  `main = { … }`. A map or match block assigned that way is a value.
- `->` makes a lambda and `=>` is a match arm. They never stand in for each
  other.
- A return type sits between `->` and the body's `{`.
- `fun T.m(…)` defines a method: a function `T_m` whose first parameter is
  `self: T`.
- The last parameter may be variadic, `rest...`; it collects the remaining
  arguments as a list.

## Loops

```ebnf
loop  = "for" block
      | "for" "!" bound block
      | "for" number block
      | "for" expression "!" bound block
      | "for" identifier [ ":" type ] "in" expression [ "!" bound ] block
      | "for" identifier "," identifier "in" expression [ "!" bound ] block
      | "while" expression block
      | "||" identifier "in" expression block ;
bound = number | "inf" ;
```

| Form                       | Meaning                                       |
|----------------------------|-----------------------------------------------|
| `for { … }`                | forever, until `break` or `ret`               |
| `for 10 { … }`             | ten times                                     |
| `for x in xs { … }`        | each element of a list, range, map or string  |
| `for i, v in xs { … }`     | index (or key) and value                      |
| `for port, msg in ":7000" { … }` | each UDP message received on a port, with the sender's port |
| `for cond ! 100 { … }`     | while `cond`, at most 100 iterations          |
| `while cond { … }`         | while `cond`                                  |
| `\|\| x in xs { … }`       | iterations in parallel, on all cores          |

A `! bound` caps the number of iterations. Iterating a container walks a
snapshot, so updating the container in the body does not change the
iteration.

## Declarations and modules

```ebnf
cstruct_decl = "cstruct" identifier [ "packed" ] [ "aligned" "(" number ")" ]
               "{" { field_names ( ":" | "as" ) type [ "," ] } "}" ;
field_names  = identifier { "," identifier } ;

import_statement = "import" source [ "as" identifier ] ;
source       = string | identifier { ( "." | "/" | "-" ) identifier } [ "@" version ] | "." ;
export_statement = "export" ( "*" | identifier { identifier } ) ;

unsafe_expression = "unsafe" block block block [ "as" type ] ;
```

```tim
cstruct Vec3 { x, y, z: float64 }
v := Vec3(1.0, 2.0, 3.0)

fun Vec3.dot(o: Vec3) = self.x * o.x + self.y * o.y + self.z * o.z
println(v.dot(v))

import sdl3 as sdl
import github.com/user/geometry@v1.2.0 as geo
export area perimeter
```

- A `cstruct` has C layout. `Vec3.size` and `Vec3.x.offset` are constants.
- `import` reads C libraries (via pkg-config or a `.so` path), git
  repositories and directories. Without `export`, an importer reaches every
  function through its namespace; `export a b` limits that to `a` and `b`;
  `export *` puts the exported functions in the importer's namespace.
- `unsafe` takes one block per architecture (x86-64, arm64, riscv64) of
  register-level statements and yields the first return register, read as
  `T` (`uint64` by default).

## Migrating from Tim 1.x

| Tim 1.x                         | Tim 2.0                         |
|---------------------------------|---------------------------------|
| `2 ^ 3`                         | `2 ** 3`                        |
| `!x` (bitwise not), `!b x`      | `~b x`                          |
| `x <<b 2`, `x >>b 2`, `<<<b`, `>>>b` | `x << 2`, `x >> 2`, `<<<`, `>>>` |
| `x =<< 2`, `=>>`, `=<<<`, `=>>>` | `x <<= 2`, `>>=`, `<<<=`, `>>>=` |
| `foreach x in xs { }`           | `for x in xs { }`               |
| `ret @`, `ret @1`, `ret @ v`    | `break`, `break @1`             |
| `x ¤ y`                         | `x or! y`                       |
| `xs#`                           | `#xs`                           |
| `expr : Type` (conversion)      | `expr as Type`                  |
| `pattern -> result` in a match  | `pattern => result`             |
| `(0) -> 1, (n) -> n * f(n - 1)` | `n -> n { 0 => 1 ~> n * f(n - 1) }` |
| `f = x -> a, (x, y) -> b`       | a variadic or separately named function |
| `++x`, `--x`                    | `x++` as a statement, or `x + 1` |
| `alias loop = for`              | removed                         |
| `use "file"`                    | `import`                        |
| `&8080`, `<= &8080`, `$addr`    | `enet.send(":8080", msg)`, `for msg in ":8080" { }` |
| `@first`, `@last`, `@counter`, `@i` | `for i, x in xs { }`        |
| `malloc`/`free` keywords        | ordinary functions: `malloc(n)`, `c.free(p)` |
| `class C { … }`, `.field`       | `cstruct` with `fun C.method(…)`, or a map of closures |
