package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var crossArchPrograms = []struct{ name, code, want string }{
	{"exact", `println(0.1 + 0.2 == 0.3, 0.1 + 0.2, 1 / 3 + 1 / 6, 22 / 7, 2 ** 100)
x := 9007199254740991
x <- x + 2
println(x, -(2 ** 70), floor(-7 / 2), round(5 / 2), abs(-1 / 3), 17 % 5, -7 % 3)
println(f"{1 / 3} and {2 ** 64}", 1 / 0 or! 42, 1 / 3 or! 9, 0 or! 5)
`, "1 0.3 0.5 22/7 1267650600228229401496703205376\n9007199254740993 -1180591620717411303424 -4 3 1/3 2 -1\n1/3 and 18446744073709551616 42 1/3 5\n"},
	{"functions", `fib = n -> n < 2 { 1 => n ~> fib(n - 1) + fib(n - 2) }
loop = (n, acc) -> n == 0 { 1 => acc ~> loop(n - 1, acc + 1 / 2) }
count := 0
bump = n -> {
    count <- count + n
    count
}
bump(5)
bump(7)
println(fib(20), loop(1000000, 0), count)
`, "6765 500000 12\n"},
	{"loops", `sum := 0
for i in 0..<10 {
    sum <- sum + i * i
}
n := 0
for n < 5 ! 100 {
    n <- n + 1
}
m := 0
for yes ! 3 {
    m <- m + 1
}
println(sum, n, m)
`, "285 5 3\n"},
	{"lists", `xs := [1, 1 / 2, 2 ** 65] + [3]
xs[0] <- 10
ys := [0] * 3
tot := 0
for v in xs {
    tot <- tot + v
}
println(xs, #xs, xs[9], ys, tot)
`, "[10, 0.5, 36893488147419103232, 3] 4 0 [0, 0, 0] 36893488147419103245.5\n"},
	{"strings", `s := "ab"
for k in 1..=3 {
    s <- s + (k as string)
}
g = y -> y {
    2 ** 70 => "big"
    ~> "other"
}
println(s, #s, g(2 ** 70), g(1))
printf("%d|%.3f|%v|%s|%f|%.0f\n", -7 / 2, -1 / 3, 10 ** 20, s, 0.9999999, 2.5)
`, "ab123 5 big other\n-3|-0.333|100000000000000000000|ab123|1.000000|2\n"},
	{"bitwise", `println(12 &b 10, ~b 0, 1 << 62, 5 ?b 2, -8 >> 60)
println(1 << 3, 16 >> 2, 1 <<< 3, 1 >>> 1)
x := 5
x <<= 1
y := 3
y >>>= 1
println(x, y)
`, "8 -1 4611686018427387904 1 15\n8 4 8 -9223372036854775808\n10 -9223372036854775807\n"},
	{"map_keys", `m := { a: 1, b: 2 }
m.c <- 3
k = "d"
m[k] <- 4
println(m.a, m.b, m.c, m.d, #m)
for kk, v in m {
    println(kk, v)
}
`, "1 2 3 4 4\na 1\nb 2\nc 3\nd 4\n"},
	{"pairs", `xs := [10, 20]
for i, v in xs {
    println(i, v)
}
println(1 :: 2 :: [], pop([1, 2, 3]), [1, 2] + [3], [7] * 2)
`, "0 10\n1 20\n[1, 2] [[1, 2], 3] [1, 2, 3] [7, 7]\n"},
	{"strs2", `s := "hi"
println(s.runes, s.bytes, #s, s[1], str([1, 2]))
e = error("bad")
println(e.error, e.error.runes)
`, "[104, 105] [104, 105] 2 105 [1, 2]\nbad [98, 97, 100]\n"},
	{"lists2", `xs := [1, 2, 3]
ys = xs
xs <- append(xs, 4)
println(xs, ys, #xs, #ys)
a, b = pop([1, 2, 3])
println(a, b, #a)
nested := [[1, 2], [3]]
nested[1] <- append(nested[1], 4)
println(nested, nested[1][1], #nested[0])
zs = [5, 6, 7, 8, 9]
println(zs[1:3], zs[2:], zs[:2], head(zs), tail(zs), zs[::2], zs[::-1])
println([1, [2]] == [1, [2]], [1] == [2], "ab" == "ab", [1, 2] != [1, 2])
big := []
for i in 0..<100000 { big <- append(big, i) }
println(#big, big[99999])
println("héllo", #"héllo", "héllo"[1], "日本" + "語")
m := { a: 1, b: [2, 3] }
m.c <- "x"
println(m, m.b, #m, m.a + 1)
for k, v in m { println(k, v) }
println(2 in [1, 2, 3], 5 in [1, 2], "ell" in "hello")
s = str(123)
println(s[1], s + "!", #s)
println(0..<5, [0] * 3, [1, 2] + [3], 1 :: [2, 3])
`, "[1, 2, 3, 4] [1, 2, 3] 4 3\n[1, 2] 3 2\n[[1, 2], [3, 4]] 4 2\n[6, 7] [7, 8, 9] [5, 6] 5 [6, 7, 8, 9] [5, 7, 9] [9, 8, 7, 6, 5]\n1 0 1 0\n100000 99999\nhéllo 5 233 日本語\n{a: 1, b: [2, 3], c: x} [2, 3] 3 2\na 1\nb [2, 3]\nc x\n1 0 1\n50 123! 3\n[0, 1, 2, 3, 4] [0, 0, 0] [1, 2, 3] [1, 2, 3]\n"},
	{"append_owned", `xs := []
for i in 0..<100000 { xs <- append(xs, i) }
println(#xs, xs[99999], xs[3])
ys := [1]
zs = ys
ys <- append(ys, 2)
ys <- append(ys, 3)
println(ys, zs)
w := []
for i in 0..<10 {
    w <- append(w, i)
    snap = w
    w <- append(w, 100 + i)
    if i == 9 { println(snap, #w) }
}
f = n -> {
    acc := []
    for i in 0..<n { acc <- acc + [i * i] }
    acc
}
println(f(6), #f(1000))
`, "100000 99999 3\n[1, 2, 3] [1]\n[0, 100, 1, 101, 2, 102, 3, 103, 4, 104, 5, 105, 6, 106, 7, 107, 8, 108, 9] 20\n[0, 1, 4, 9, 16, 25] 1000\n"},
	{"closures", `make_counter = start -> {
    count := start
    () -> {
        count <- count + 1
        count
    }
}
counter = make_counter(10)
counter()
println(counter(), counter())
apply_twice = (f, x) -> f(f(x))
increment = x -> x + 1
println(apply_twice(increment, 10))
k = 5
addk = x -> x + k
println([1, 2, 3] | addk, [1, 2, 3] | x -> x * 2)
compose = increment <> addk
println(compose(1))
h = n -> m -> n + m
add1 = h(1)
println(add1(2))
fact = n -> n <= 1 { 1 => 1 ~> n * fact(n - 1) }
println(fact(20), fact(25))
sum = (xs, acc) -> #xs == 0 { 1 => acc ~> sum(xs[1:], acc + xs[0]) }
println(sum([1, 2, 3, 4], 0))
`, "12 13\n12\n[6, 7, 8] [2, 4, 6]\n7\n3\n2432902008176640000 15511210043330985984000000\n10\n"},
	{"strings_maps", `name = "Tim"
n = 3
println(f"Hello {name}, n={n}, half={n / 2}, list={[1, 2]}")
s := ""
w = "abc"
for c in w { s <- s + str(c) + "," }
println(s, "abc".runes, "hé".bytes, #"hé".bytes)
e = error("bad")
println(e.error, e or! "default", 1 / 0 or! "div")
printf("%d %s %v %.2f|\n", 42, "str", [1, 2], 3.14159)
for i in 0..<10 {
    i == 3 { continue }
    i == 6 { break }
    print(i, "")
}
println()
x = 7
r = x {
    1 => "one"
    7 => "seven"
    ~> "other"
}
q = x > 5 { 1 => "big" ~> "small" }
println(r, q)
words = ["b", "a"]
m := {}
for wd in words { m[wd] <- #wd }
println(m, m["a"], m.b, "a" in m, "z" in m)
`, "Hello Tim, n=3, half=1.5, list=[1, 2]\n97,98,99, [97, 98, 99] [104, 195, 169] 3\nbad default div\n42 str [1, 2] 3.14|\n0 1 2 4 5 \nseven big\n{b: 1, a: 1} 1 1 1 0\n"},
	{"cstructs", `cstruct V3 { x as float64, y as float64, z as float64 }
cstruct Px { n as int32, w as float32, tag as uint8 }
fun V3.add(o: V3) = V3(self.x + o.x, self.y + o.y, self.z + o.z)
fun V3.dot(o: V3) = self.x * o.x + self.y * o.y + self.z * o.z
a := V3(1.0, 2.0, 3.0)
b = V3(4.0, 5.0, 6.0)
c = a.add(b)
a.x <- 10.0
p = Px(-7, 2.5, 300)
println(c.x, c.y, c.z, a.dot(b), a.x, p.n, p.w, p.tag)
buf = malloc(64)
for i in 0..<4 { write_u32(buf, i, i * 3) }
println(read_u32(buf, 3), read_u32(buf, 1))
`, "5 7 9 68 10 -7 2.5 44\n9 3\n"},
	{"append_aliasing", `xs := []
for i in 0..<5 { xs <- append(xs, i) }
id = l -> l
ys = id(xs)
xs <- append(xs, 99)
println(ys, xs)
zs := []
for i in 0..<6 {
    zs <- append(zs, i)
    m = { k: zs }
    zs <- append(zs, 100)
    if i == 5 { println(m.k, #zs) }
}
for v in xs { xs <- append(xs, v * 10) }
println(xs)
grid := []
for r in 0..<3 {
    row := []
    for c in 0..<4 { row <- append(row, r * 4 + c) }
    grid <- append(grid, row)
}
println(grid, #grid[2])
ws := [1]
ws <- ws + [2, 3]
vs = ws
ws <- ws + [4]
println(vs, ws)
`, "[0, 1, 2, 3, 4] [0, 1, 2, 3, 4, 99]\n[0, 100, 1, 100, 2, 100, 3, 100, 4, 100, 5] 12\n[0, 1, 2, 3, 4, 99, 0, 10, 20, 30, 40, 990]\n[[0, 1, 2, 3], [4, 5, 6, 7], [8, 9, 10, 11]] 4\n[1, 2, 3] [1, 2, 3, 4]\n"},
	{"register_pressure", `f = (a, b, c) -> {
    v1 := a + 1
    v2 := b + 2
    v3 := c + 3
    v4 := v1 * v2
    v5 := v2 * v3
    v6 := v3 * v1
    v7 := v4 + v5
    v8 := v5 + v6
    v9 := v6 + v4
    v10 := v7 - v8
    v11 := v8 - v9
    v12 := v9 - v7
    v13 := v10 * v11
    v14 := v11 * v12
    v15 := v12 * v10
    acc := 0
    for i in 0..<3 {
        for j in 0..<3 {
            for k in 0..<3 {
                for l in 0..<2 {
                    for m in 0..<2 {
                        for n in 0..<2 {
                            acc <- acc + i + j * k - l + m * n + v13 % 7
                        }
                    }
                }
            }
        }
    }
    [v1, v2, v3, v4, v5, v6, v7, v8, v9, v10, v11, v12, v13, v14, v15, acc]
}
println(f(1, 2, 3))
adder = n -> x -> x + n
add5 = adder(5)
total := 0
for w in [1, 2, 3] { total <- total + add5(w) }
println(total)
ack = (m, n) -> m == 0 { 1 => n + 1 ~> n == 0 { 1 => ack(m - 1, 1) ~> ack(m - 1, ack(m, n - 1)) } }
println(ack(2, 3))
`, "[2, 4, 6, 8, 24, 12, 32, 36, 20, -4, 16, -12, -64, -192, 48, 162]\n21\n9\n"},
	{"c_ffi", `println("before")
n = c.strlen("héllo")
p = c.malloc(16)
c.memset(p, 65, 3)
println(n, c.abs(-5), c.sqrt(2.0), c.atoi("123") + 1, c.toupper(97), read_u8(p, 1))
c.printf("printf %d %s %.3f|%5.1f\n", 42, "world", 2.5, -1.25)
c.puts("puts")
s = c.getenv("TIM_SURELY_UNSET_VARIABLE")
println("after", s == 0)
c.free(p)
`, "before\n6 5 1.414214 124 65 65\nprintf 42 world 2.500| -1.2\nputs\nafter 1\n"},
	{"currying", `h = n -> m -> n + m
println(h(1)(2))
inc = x -> x + 1
println(inc)
printf("%c%c|\n", 72, 105)
k = (a) -> (b) -> (c) -> a * 100 + b * 10 + c
println(k(1)(2)(3))
`, "3\n<function>\nHi|\n123\n"},
	{"random", `n := 0
lo := 1
hi := 0
for i in 0..<1000 {
    r = ??
    lo <- min(lo, r)
    hi <- max(hi, r)
    n <- n + r
}
println(lo >= 0, hi < 1, n > 400, n < 600, ?? != ??)
`, "1 1 1 1 1\n"},
	{"nested_fstrings", `inner = "world"
outer = f"Hello, {f"{inner}"}!"
m = { a: 1 }
println(outer, f"x={"}"} y={ {b: 2}.b } z={f"<{f"[{inner}]"}>"} {{literal}}", f"{m.a + 1}")
`, "Hello, world! x=} y=2 z=<[world]> {literal} 2\n"},
	{"error_propagation", `validate = x -> x < 0 { 1 => error("arg") ~> x }
transform = x -> x * 2
finalize = x -> x + 1
process = input -> {
    step1 = validate(input)
    step1.error != "" { 1 => ret step1 }
    step2 = transform(step1)
    step2.error != "" { 1 => ret step2 }
    finalize(step2)
}
println(process(5), process(-1).error, process(-1) or! "failed")
divide = (a, b) -> {
    result = a / b
    result.error != "" {
        1 => result
        ~> result
    }
}
println(divide(10, 4), divide(1, 0).error)
`, "11 arg failed\n2.5 dv0\n"},
	{"unsafe", `msg := "hi\n"
n = unsafe {
    rax <- 7
    rbx <- 5
    rax <- rax * rbx
    stack <- rax
    rcx <- stack
    rax <- rcx << 62
    rax <- rax + 1
} {
    x0 <- 0
} {
    a0 <- 7
    a1 <- 5
    a0 <- a0 * a1
    stack <- a0
    a2 <- stack
    a0 <- a2 << 62
    a0 <- a0 + 1
} as int64
w = unsafe {
    rax <- 1
    rdi <- 1
    rsi <- msg as cstr
    rdx <- 3
    syscall
} {
    x0 <- 0
} {
    a7 <- 64
    a0 <- 1
    a1 <- msg as cstr
    a2 <- 3
    syscall
} as int64
for i in 0..<2 {
    println(n, w, i)
}
`, "hi\n-4611686018427387903 3 0\n-4611686018427387903 3 1\n"},
	{"variadic", `sum = (first, rest...) -> {
    total := first
    for item in rest {
        total <- total + item
    }
    total
}
log = (messages...) -> {
    for msg in messages {
        println(msg)
    }
}
max = (nums...) -> {
    result := nums[0]
    for n in nums {
        if n > result { result <- n }
    }
    result
}
values = [5, 10, 3, 8]
println(sum(1, 2, 3, 4, 5), sum(7), max(5, 10, 3, 8), max(values...), max(1, 2, values..., 99))
log("Error:", "File not found:", "x.txt")
count = (xs...) -> #xs
println(count(), count(values..., values...), count(1, values...))
`, "15 7 10 10 99\nError:\nFile not found:\nx.txt\n0 8 5\n"},
	{"exact_edges", `println(2 ** 64 - 1, -(2 ** 63), (2 ** 64) / (2 ** 32), 7 / 3 * 3, 1 / 3 + 2 / 3)
println(10 % 3, -10 % 3, 10 % -3, 5.5 % 2, (2 ** 70) % 7)
println(floor(7 / 2), ceil(7 / 2), round(-5 / 2), abs(-2 ** 70), 0.1 * 3)
println(1e20, 1e-5, 123456789012345678, 3.14159, 1 / 7)
println(sqrt(16), sqrt(2) * sqrt(2) == 2, 2 ** 0.5, 10 ** -2, (-8) ** (1 / 3))
println(1 < 2, 2 <= 2, (2 ** 80) > (2 ** 79), 1 / 3 < 0.34, 0.1 + 0.2 == 0.3)
`, "18446744073709551615 -9223372036854775808 4294967296 7 1\n1 -1 1 1.5 2\n3 4 -3 1180591620717411303424 0.3\n100000000000000000000 0.00001 123456789012345678 3.14159 1/7\n4 0 1.414214 0.01 nan\n1 1 1 1 1\n"},
	{"strings_ops", `s = "Hello, World"
println(#s, s[0], s[1:5], s[-5:], s[::-1], s + "!")
println("abc" < "abd", "a" == "a", "abc" != "abd", "ell" in s, "xyz" in s)
t := ""
for i in 0..<5 { t <- t + str(i) }
println(t, #t, t[2])
u = f"{1 + 2}-{"x"}-{[1, 2]}-{{}}"
println(u, #u)
println(str(1 / 3), str(2 ** 70), str([1, "a", [2]]), str({k: "v"}))
`, "12 72 ello World dlroW ,olleH Hello, World!\n1 1 1 1 0\n01234 5 50\n3-x-[1, 2]-{} 13\n1/3 1180591620717411303424 [1, a, [2]] {k: v}\n"},
	{"maps_ops", `m := { a: 1, b: 2 }
m["c"] <- 3
m.d <- 4
m.a <- 10
k = "e"
m[k] <- 5
println(m, #m, m.a, m["b"], m[k], m.zz, "a" in m, "q" in m)
for key, v in m { print(key, v, "") }
println()
n = { 1: "one", 2: "two" }
println(n[1], n[2], n[3], #n)
nested := { inner: { x: 1 } }
println(nested.inner.x, nested)
`, "{a: 10, b: 2, c: 3, d: 4, e: 5} 5 10 2 5 0 1 0\na 10 b 2 c 3 d 4 e 5 \none two 0 2\n1 {inner: {x: 1}}\n"},
	{"closures2", `counter = () -> {
    c := 0
    () -> {
        c <- c + 1
        c
    }
}
a = counter()
b = counter()
a()
a()
b()
println(a(), b())
adders = [1, 2, 3] | n -> (x -> x + n)
println(adders[0](10), adders[2](10))
twice = f -> x -> f(f(x))
println(twice(x -> x * 3)(2))
fib = n -> n < 2 { 1 => n ~> fib(n - 1) + fib(n - 2) }
println([0, 1, 2, 3, 10] | fib)
`, "3 2\n11 13\n18\n[0, 1, 1, 2, 55]\n"},
	{"control_flow", `classify = x -> {
    | x < 0 => "neg"
    | x == 0 => "zero"
    | x < 10 => "small"
    ~> "big"
}
println(classify(-5), classify(0), classify(3), classify(100))
v = 3
r = v {
    1 => "one"
    2 => "two"
    3 => "three"
    ~> "many"
}
println(r)
s = "b"
println(s { "a" => 1 "b" => 2 ~> 0 })
total := 0
for i in 0..<10 {
    i % 2 == 0 { 1 => continue }
    i > 7 { 1 => break }
    total <- total + i
}
println(total)
x := 0
for x < 100 ! 1000 { x <- x * 2 + 1 }
println(x)
`, "neg zero small big\nthree\n2\n16\n127\n"},
	{"conversions", `println(3.7 as! int, -3.7 as! int, 65 as string, "42" as number, [1, 2] as string)
println(1 / 3 as! float64)
xs = [3, 1, 2]
println(head(xs), tail(xs), #xs, xs[#xs - 1])
a, b, c = [1, 2]
println(a, b, c)
p, q := pop([9, 8, 7])
println(p, q)
e = error("oops")
println(e.error, e or! "fallback", is_nan(e), is_nan(1))
println(min(3, 1 / 2), max(2 ** 70, 5), popcount(255), clz(1), ctz(8))
`, "4 -4 65 42 [1, 2]\n0.333333\n3 [1, 2] 3 2\n1 2 0\n[9, 8] 7\noop fallback 1 0\n0.5 1180591620717411303424 8 63 3\n"},
	{"ordering", `println("b" < "a", "a" < "b", "abd" > "abc", "ab" < "abc", "B" < "a", [1, 2] < [1, 3])
words = ["pear", "apple", "fig"]
println(words[1] < words[0], words[2] > words[1])
`, "0 1 1 1 1 1\n1 1\n"},
	{"match_args", `s = "b"
x = 7
println(s { "a" => 1 "b" => 2 ~> 0 }, x > 5 { 1 => "big" ~> "small" }, x {
    7 => "seven"
    ~> "?"
})
f = (a, b) -> a + b
println(f(x > 1 { 1 => 10 ~> 20 }, 1))
`, "2 big seven\n11\n"},
	{"inlined_lambda_arg", `app = (f, x) -> f(x)
println(app(x -> x + 1, 2))
`, "3\n"},
	{"output_order", `println("a")
c.puts("b")
print("c ")
c.printf("d%d\n", 1)
pid = fork()
if pid == 0 {
    println("child")
    proc_exit(0)
}
waitpid(pid, 0, 0)
for i in 0..<3 { print(i, "") }
println("done")
`, "a\nb\nc d1\nchild\n0 1 2 done\n"},
	{"quicksort", `quicksort = xs -> #xs <= 1 {
    1 => xs
    ~> {
        pivot = xs[0]
        rest = xs[1:]
        smaller := []
        larger := []
        for x in rest {
            x < pivot { 1 => smaller <- append(smaller, x) ~> larger <- append(larger, x) }
        }
        quicksort(smaller) + [pivot] + quicksort(larger)
    }
}
println(quicksort([3, 1, 4, 1, 5, 9, 2, 6, 5, 3, 5]))
println(quicksort(["pear", "apple", "fig", "banana"]))
println(quicksort([]))
`, "[1, 1, 2, 3, 3, 4, 5, 5, 5, 6, 9]\n[apple, banana, fig, pear]\n[]\n"},
	{"sieve", `n = 100
is := [1] * (n + 1)
is[0] <- 0
is[1] <- 0
for i in 2..=n {
    if is[i] == 1 {
        j := i * i
        for j <= n ! 1000 {
            is[j] <- 0
            j <- j + i
        }
    }
}
primes := []
for i in 0..=n {
    if is[i] == 1 { primes <- append(primes, i) }
}
println(#primes, primes)
`, "25 [2, 3, 5, 7, 11, 13, 17, 19, 23, 29, 31, 37, 41, 43, 47, 53, 59, 61, 67, 71, 73, 79, 83, 89, 97]\n"},
	{"matrix", `matmul = (a, b) -> {
    n = #a
    m = #b[0]
    k = #b
    result := []
    for i in 0..<n {
        row := []
        for j in 0..<m {
            s := 0
            for t in 0..<k { s <- s + a[i][t] * b[t][j] }
            row <- append(row, s)
        }
        result <- append(result, row)
    }
    result
}
a = [[1, 2], [3, 4]]
b = [[5, 6], [7, 8]]
println(matmul(a, b), matmul([[1 / 2, 1 / 3]], [[3], [6]]))
`, "[[19, 22], [43, 50]] [[3.5]]\n"},
	{"algorithms", `gcd = (a, b) -> b == 0 { 1 => a ~> gcd(b, a % b) }
println(gcd(2 ** 100, 6 ** 40), gcd(48, 18))
fact = n -> n <= 1 { 1 => 1 ~> n * fact(n - 1) }
println(fact(30))
collatz = (n, steps) -> n == 1 { 1 => steps ~> collatz(n % 2 == 0 { 1 => n / 2 ~> 3 * n + 1 }, steps + 1) }
println(collatz(27, 0))
bsearch = (xs, target, lo, hi) -> lo > hi {
    1 => -1
    ~> {
        mid = floor((lo + hi) / 2)
        xs[mid] == target { 1 => mid ~> xs[mid] < target { 1 => bsearch(xs, target, mid + 1, hi) ~> bsearch(xs, target, lo, mid - 1) } }
    }
}
data = [1, 3, 5, 7, 9, 11, 13]
println(bsearch(data, 9, 0, #data - 1), bsearch(data, 4, 0, #data - 1))
rev = s -> #s == 0 { 1 => "" ~> rev(s[1:]) + s[0:1] }
println(rev("héllo wörld"))
memo := {}
fibm = n -> n < 2 { 1 => n ~> {
    key = str(n)
    key in memo { 1 => memo[key] ~> {
        v = fibm(n - 1) + fibm(n - 2)
        memo[key] <- v
        v
    } }
} }
println(fibm(80))
`, "1099511627776 6\n265252859812191058636308480000000\n111\n4 -1\ndlröw olléh\n23416728348467685\n"},
	{"match_in_arm_block", `f = xs -> #xs <= 1 {
    1 => xs
    ~> {
        s := []
        l := []
        for x in xs {
            x < 3 { 1 => s <- append(s, x) ~> l <- append(l, x) }
        }
        [s, l]
    }
}
println(f([5, 1, 4, 2]))
g = xs -> {
    s := []
    l := []
    for x in xs {
        x < 3 { 1 => s <- append(s, x) ~> l <- append(l, x) }
    }
    [s, l]
}
println(g([5, 1, 4, 2]))
`, "[[1, 2], [5, 4]]\n[[1, 2], [5, 4]]\n"},
	{"iterables", `for c in "abc" { print(c, "") }
println()
f = () -> [1, 2]
for v in f() { print(v) }
for k, v in {a: 1}.runes { print(k) }
println()
for ch in "héj".runes { print(ch, "") }
println()
for x in [5, 6, 7][1:] { print(x, "") }
println()
`, "97 98 99 \n120\n104 233 106 \n6 7 \n"},
	{"iterate_number", `x = 5
for v in x { println("v", v) }
e = []
for v in e { println("never") }
println("end")
`, "v 5\nend\n"},
	{"map_props", `m = {runes: 7, bytes: [1]}
println(m.runes, m.bytes, "ab".runes, {x: 1}.runes)
`, "7 [1] [97, 98] 0\n"},
}

func TestCrossArch(t *testing.T) {
	targets := []struct {
		arch   Arch
		qemu   string
		prefix string
	}{
		{ArchARM64, "qemu-aarch64", "/usr/aarch64-linux-gnu"},
		{ArchRiscv64, "qemu-riscv64", "/usr/riscv64-linux-gnu"},
	}
	for _, target := range targets {
		t.Run(target.qemu, func(t *testing.T) {
			qemu, err := exec.LookPath(target.qemu)
			if err != nil {
				t.Skipf("%s not installed", target.qemu)
			}
			if target.prefix != "" {
				if _, err := os.Stat(target.prefix); err != nil {
					t.Skipf("%s not installed", target.prefix)
				}
			}
			for _, p := range crossArchPrograms {
				t.Run(p.name, func(t *testing.T) {
					dir := t.TempDir()
					src, exe := filepath.Join(dir, "main.tim"), filepath.Join(dir, "main")
					if err := os.WriteFile(src, []byte(p.code), 0o644); err != nil {
						t.Fatal(err)
					}
					if err := CompileTimWithOptions(src, exe, Platform{Arch: target.arch, OS: OSLinux}, 0, false, false); err != nil {
						if strings.Contains(err.Error(), "container format v2 migration") {
							t.Skipf("backend pending container format v2 migration")
						}
						t.Fatalf("compilation failed: %v", err)
					}
					cmd := exec.Command("sh", "-c", `ulimit -c 0; exec "$0" "$1"`, qemu, exe)
					cmd.Env = append(os.Environ(), "QEMU_LD_PREFIX="+target.prefix)
					done := make(chan struct{})
					var out []byte
					go func() {
						out, err = cmd.Output()
						close(done)
					}()
					select {
					case <-done:
					case <-time.After(60 * time.Second):
						_ = cmd.Process.Kill()
						t.Fatal("timed out")
					}
					if err != nil {
						t.Fatalf("run failed: %v\n%s", err, out)
					}
					if got := string(out); got != p.want {
						t.Errorf("got:\n%s\nwant:\n%s", got, strings.TrimSuffix(p.want, "\n"))
					}
				})
			}
		})
	}
}

func TestCrossArchMatchesNative(t *testing.T) {
	if GetDefaultPlatform().Arch != ArchX86_64 {
		t.Skip("reference outputs come from x86_64")
	}
	for _, p := range crossArchPrograms {
		t.Run(p.name, func(t *testing.T) {
			if got := compileAndRunTopLevel(t, p.code); got != p.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, p.want)
			}
		})
	}
}
