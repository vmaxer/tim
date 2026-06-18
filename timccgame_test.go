package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTimGameLibraryCompiles tests that timccgame library compiles
func TestTimGameLibraryCompiles(t *testing.T) {
	timccgameDir := filepath.Join(os.Getenv("HOME"), "clones", "timccgame")
	gamePath := filepath.Join(timccgameDir, "game.tim")

	if _, err := os.Stat(gamePath); os.IsNotExist(err) {
		t.Skip("timccgame library not found at ~/clones/timccgame")
	}

	tmpDir := t.TempDir()
	binary := filepath.Join(tmpDir, "game_lib")
	cmd := exec.Command("./timcc", gamePath, "-o", binary)
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Logf("Compilation output: %s", output)
		t.Fatalf("Failed to compile timccgame: %v", err)
	}

	t.Log("timccgame library compiled successfully")
}

// TestTimGameTest tests that timcc test works in timccgame directory
func TestTimGameTest(t *testing.T) {
	timccgameDir := filepath.Join(os.Getenv("HOME"), "clones", "timccgame")
	testPath := filepath.Join(timccgameDir, "timccgame_test.tim")

	if _, err := os.Stat(testPath); os.IsNotExist(err) {
		t.Skip("timccgame_test.tim not found")
	}

	// Run timcc test in the timccgame directory
	timccBinary := filepath.Join(filepath.Dir(timccgameDir), "timcc", "timcc")
	cmd := exec.Command(timccBinary, "test")
	cmd.Dir = timccgameDir
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Logf("Test output: %s", output)
		t.Fatalf("timcc test failed: %v", err)
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "PASS") {
		t.Errorf("Expected PASS in output, got: %s", outputStr)
	}

	t.Log("timccgame tests passed")
}

// TestTimGameSimpleProgram tests that a simple program using timccgame compiles
func TestTimGameSimpleProgram(t *testing.T) {
	cmd := exec.Command("pkg-config", "--exists", "sdl3")
	if err := cmd.Run(); err != nil {
		t.Skip("SDL3 not installed")
	}

	timccgameDir := filepath.Join(os.Getenv("HOME"), "clones", "timccgame")
	gamePath := filepath.Join(timccgameDir, "game.tim")

	if _, err := os.Stat(gamePath); os.IsNotExist(err) {
		t.Skip("timccgame library not found")
	}

	// Create a simple program that uses timccgame
	source := `import "` + gamePath + `"

main = {
    println("Testing timccgame import")
}
`

	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "test.tim")
	if err := os.WriteFile(srcFile, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(tmpDir, "test")
	cmd = exec.Command("./timcc", srcFile, "-o", binary)
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Logf("Compilation output: %s", output)
		t.Fatalf("Failed to compile: %v", err)
	}

	// Run it
	cmd = exec.Command(binary)
	output, err = cmd.CombinedOutput()

	if err != nil {
		t.Logf("Run output: %s", output)
		t.Fatalf("Failed to run: %v", err)
	}

	if !strings.Contains(string(output), "Testing timccgame import") {
		t.Errorf("Expected 'Testing timccgame import' in output, got: %s", output)
	}

	t.Log("Simple timccgame program works")
}
