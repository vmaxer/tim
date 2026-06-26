package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
    x as float64,
    y as float64
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
    a as uint8,
    b as uint32,
    c as uint8
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
    x as uint32
    y as uint32
}

p := c.malloc(8) as Point
p[0] <- 42 as uint32
p[1] <- 99 as uint32
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
    a as float64
    b as float64
}

v := c.malloc(16) as Vec
v[0] <- 10.0 as float64
v[1] <- 20.0 as float64
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
func TestExistingCStructPrograms(t *testing.T) {
	tests := []string{
		"cstruct_test",
		"cstruct_syntax_test",
		"cstruct_helpers_test",
		"cstruct_modifiers_test",
		"cstruct_arena_test",
	}

	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			srcPath := filepath.Join("testprograms", name+".tim")
			resultPath := filepath.Join("testprograms", name+".result")

			if _, err := os.Stat(srcPath); os.IsNotExist(err) {
				t.Skipf("Source file %s not found", srcPath)
				return
			}

			var expected string
			if data, err := os.ReadFile(resultPath); err == nil {
				expected = string(data)
			}

			tmpDir := t.TempDir()
			exePath := filepath.Join(tmpDir, name)

			platform := GetDefaultPlatform()
			if err := CompileTim(srcPath, exePath, platform); err != nil {
				t.Fatalf("Compilation failed: %v", err)
			}

			output, err := runWithTimeout(exePath, 5)
			if err != nil {
				if _, ok := err.(*exec.ExitError); !ok {
					t.Fatalf("Execution failed: %v", err)
				}
			}

			if expected != "" {
				actual := string(output)
				if actual != expected {
					t.Errorf("Output mismatch:\nExpected:\n%s\nActual:\n%s",
						expected, actual)
				}
			}
		})
	}
}
