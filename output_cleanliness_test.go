package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDefaultBuildProducesCleanStderr ensures a normal (non-verbose) build emits
// no internal DEBUG/trace output to stderr. This guards against regressions where
// developer tracing leaks into normal compiler output.
func TestDefaultBuildProducesCleanStderr(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "clean.tim")
	if err := os.WriteFile(src, []byte("main = { println(\"hi\") }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(tmpDir, "clean")

	osType, _ := ParseOS(runtime.GOOS)
	archType, _ := ParseArch(runtime.GOARCH)
	platform := Platform{OS: osType, Arch: archType}

	// Make sure verbose tracing is off regardless of prior tests.
	oldVerbose := VerboseMode
	VerboseMode = false
	defer func() { VerboseMode = oldVerbose }()

	// Redirect os.Stderr to capture any compiler output.
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	captured := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		captured <- string(data)
	}()

	compileErr := CompileTimWithOptions(src, exe, platform, 0, false, false)

	_ = w.Close()
	os.Stderr = origStderr
	stderr := <-captured

	if compileErr != nil {
		t.Fatalf("compilation failed: %v", compileErr)
	}
	if strings.Contains(stderr, "DEBUG") {
		t.Errorf("default build leaked DEBUG output to stderr:\n%s", stderr)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("default build should produce clean stderr, got:\n%s", stderr)
	}
}
