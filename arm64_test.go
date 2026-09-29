package main

import (
	"debug/macho"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestARM64BasicCompilation tests that ARM64 code can be compiled
// Note: We can't execute the binaries on Linux, but we can verify they compile and have correct structure
func TestARM64BasicCompilation(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		wantText int // expected minimum text size in bytes
	}{
		{
			name:     "exit_zero",
			code:     "exit(0)",
			wantText: 40, // prologue + exit syscall + epilogue
		},
		{
			name:     "exit_code",
			code:     "exit(42)",
			wantText: 40,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := filepath.Join(t.TempDir(), "test_arm64_"+tt.name+".tim")
			outFile := filepath.Join(t.TempDir(), "test_arm64_"+tt.name)

			// Write test program
			if err := os.WriteFile(tmpFile, []byte(tt.code), 0644); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			// Compile for ARM64 macOS
			platform := Platform{Arch: ArchARM64, OS: OSDarwin}
			err := CompileTim(tmpFile, outFile, platform)
			if err != nil {
				t.Fatalf("Compilation failed: %v", err)
			}

			// Verify file exists
			info, err := os.Stat(outFile)
			if err != nil {
				t.Fatalf("Output file not created: %v", err)
			}

			// Verify file is executable (Linux only - Windows chmod has no effect)
			if runtime.GOOS == "linux" && info.Mode()&0111 == 0 {
				t.Errorf("Output file is not executable")
			} else if runtime.GOOS != "linux" {
				t.Logf("Output file permissions: %o (chmod not effective on this OS)", info.Mode())
			}

			f, err := macho.Open(outFile)
			if err != nil {
				t.Fatalf("not Mach-O: %v", err)
			}
			defer f.Close()
			if f.Cpu != macho.CpuArm64 {
				t.Errorf("cpu %v, want arm64", f.Cpu)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
