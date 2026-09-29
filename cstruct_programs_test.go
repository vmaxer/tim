package main

import (
	"testing"
)

// TestCStructPrograms tests C struct interop
func TestCStructPrograms(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		expected string
	}{
		{
			name: "simple_cstruct",
			source: `cstruct Point {
    x: float64,
    y: float64
}

println(Point.size)
println(Point.x.offset)
println(Point.y.offset)
`,
			expected: "16\n0\n8\n",
		},
		{
			name: "packed_cstruct",
			source: `cstruct Data packed {
    a: uint8,
    b: uint32,
    c: uint8
}

println(Data.size)
`,
			expected: "6\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testInlineTim(t, tt.name, tt.source, tt.expected)
		})
	}
}

// TestCStructFieldAccess tests reading fields from cstruct pointers via dot-access
func TestCStructFieldAccess(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		expected string
	}{
		{
			name: "read_uint32_field",
			source: `import libc as c

cstruct Point {
    x: uint32
    y: uint32
}

p := c.malloc(8) as Point
p.x <- 42
p.y <- 99
println(p.x)
println(p.y)
c.free(p)
`,
			expected: "42\n99\n",
		},
		{
			name: "read_float64_field",
			source: `import libc as c

cstruct Vec {
    a: float64
    b: float64
}

v := c.malloc(16) as Vec
v.a <- 10.0
v.b <- 20.0
println(v.a)
println(v.b)
c.free(v)
`,
			expected: "10\n20\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testInlineTim(t, tt.name, tt.source, tt.expected)
		})
	}
}

// TestCStructByValue covers struct values: the constructor Vec(...), passing a
// struct to a function and re-tagging the param with `as`, returning a struct,
// and inferred return typing (no cast needed on the call result).
func TestCStructByValue(t *testing.T) {
	source := `cstruct Vec { x: float64, y: float64 }

vadd = (a, b) -> {
    aa = a as Vec
    bb = b as Vec
    Vec(aa.x + bb.x, aa.y + bb.y)
}

dot = (a, b) -> {
    aa = a as Vec
    bb = b as Vec
    aa.x*bb.x + aa.y*bb.y
}

main = {
    p = Vec(3.0, 4.0)
    q = Vec(10.0, 20.0)
    r = vadd(p, q)
    println(r.x)
    println(r.y)
    println(dot(p, q))
}
`
	testInlineTim(t, "cstruct_by_value", source, "13\n24\n110\n")
}

// TestReadWriteFloat32 covers the 32-bit-float FFI accessors. write_f32 existed
// only on x86; read_f32 did not exist at all. C graphics/audio APIs (vertex
// buffers, colours, SDL mouse coords, PCM samples) are float32-heavy, so both
// directions must work on both backends. Values chosen to be exactly
// representable in float32 so the round-trip is bit-clean.
func TestReadWriteFloat32(t *testing.T) {
	source := `import c
buf = c.malloc(16.0)
write_f32(buf, 0, 3.5)
write_f32(buf, 4, 0.0-2.25)
write_f32(buf, 8, 1000.5)
println(read_f32(buf, 0))
println(read_f32(buf, 4))
println(read_f32(buf, 8))
c.free(buf)
`
	testInlineTim(t, "read_write_f32", source, "3.5\n-2.25\n1000.5\n")
}

// TestChainedCStructMethodsCompileFast guards against the inliner blowing up on
// deeply chained cstruct method calls. Each such call inlines to a body whose
// arguments (the constructors) were being deep-copied at every use site, and the
// whole result was re-inlined at every level — so a chain like a matrix build +
// transform expanded the AST exponentially and OOM-killed the compiler. The fix
// binds a multiply-used constructor arg once and re-inlines only the body, not
// the already-inlined argument bindings. If that regresses, this test hangs
// instead of returning a value — a clear signal in CI.
func TestChainedCStructMethodsCompileFast(t *testing.T) {
	source := `cstruct V3 { x: float64, y: float64, z: float64 }

V3.add(self, o: V3) = V3(self.x+o.x, self.y+o.y, self.z+o.z)
V3.sub(self, o: V3) = V3(self.x-o.x, self.y-o.y, self.z-o.z)
V3.scale(self, s) = V3(self.x*s, self.y*s, self.z*s)
V3.dot(self, o: V3) = self.x*o.x + self.y*o.y + self.z*o.z
V3.cross(self, o: V3) = V3(self.y*o.z-self.z*o.y, self.z*o.x-self.x*o.z, self.x*o.y-self.y*o.x)
V3.norm(self) = {
    l = sqrt(self.x*self.x + self.y*self.y + self.z*self.z)
    if l > 0.0 { V3(self.x/l, self.y/l, self.z/l) } else { V3(0.0,0.0,0.0) }
}

build(t) = {
    a = V3(1.0, 2.0, 3.0)
    b = V3(4.0, 5.0, 6.0)
    // A long chain of struct-returning methods, the shape that used to explode.
    r = a.add(b).sub(a).scale(2.0).add(b.cross(a)).norm().scale(3.0)
    r.x + r.y + r.z
}

main = {
    println(build(1.0))
}
`
	// A non-hanging result is the real assertion; the value just pins correctness
	// (sum of the normalized-and-scaled chain = 90/sqrt(362) ≈ 4.7302). Match a
	// prefix so a last-digit rounding difference between backends can't flake it.
	testInlineTim(t, "chained_cstruct_methods", source, "4.7302")
}

// TestCStructMethodOnLocalInIfArm covers a cstruct local defined inside an
// `if`-expression arm and then used as a method-call receiver in the SAME arm.
// The operator-overload/method desugar pass used to skip locals declared inside
// a value-position `if` block, so `a.add(horizon)` kept its unresolved `a.add`
// form and was reported as an undefined function (and, in heavier scenes,
// showed up as a segfault once worked around). It must resolve to V_add now.
func TestCStructMethodOnLocalInIfArm(t *testing.T) {
	source := `cstruct V { x: float64, y: float64, z: float64 }

V.add(self, o: V) = V(self.x + o.x, self.y + o.y, self.z + o.z)
V.scale(self, s) = V(self.x*s, self.y*s, self.z*s)

f(dy) = {
    horizon = V(0.45, 0.10, 0.38)
    base = if dy < 0.0 {
        a = horizon.scale(2.0)
        a.add(horizon)
    } else {
        horizon
    }
    base
}

main = {
    w = f(-0.5)
    println(w.x)
    println(w.y)
    println(w.z)
}
`
	testInlineTim(t, "cstruct_method_on_local_in_if_arm", source, "1.35\n0.30000000000000004\n1.1400000000000001\n")
}
