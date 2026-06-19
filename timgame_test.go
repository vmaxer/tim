package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTimGameLibraryCompiles tests that timgame library compiles
func TestTimGameLibraryCompiles(t *testing.T) {
	timgameDir := filepath.Join(os.Getenv("HOME"), "clones", "timgame")
	gamePath := filepath.Join(timgameDir, "game.tim")

	if _, err := os.Stat(gamePath); os.IsNotExist(err) {
		t.Skip("timgame library not found at ~/clones/timgame")
	}

	tmpDir := t.TempDir()
	binary := filepath.Join(tmpDir, "game_lib")
	cmd := exec.Command("./tim", gamePath, "-o", binary)
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Logf("Compilation output: %s", output)
		t.Fatalf("Failed to compile timgame: %v", err)
	}

	t.Log("timgame library compiled successfully")
}

// TestTimGameTest tests that tim test works in timgame directory
func TestTimGameTest(t *testing.T) {
	timgameDir := filepath.Join(os.Getenv("HOME"), "clones", "timgame")
	testPath := filepath.Join(timgameDir, "timgame_test.tim")

	if _, err := os.Stat(testPath); os.IsNotExist(err) {
		t.Skip("timgame_test.tim not found")
	}

	// Run tim test in the timgame directory
	timBinary := filepath.Join(filepath.Dir(timgameDir), "tim", "tim")
	cmd := exec.Command(timBinary, "test")
	cmd.Dir = timgameDir
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Logf("Test output: %s", output)
		t.Fatalf("tim test failed: %v", err)
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "PASS") {
		t.Errorf("Expected PASS in output, got: %s", outputStr)
	}

	t.Log("timgame tests passed")
}

// TestTimGameSimpleProgram tests that a simple program using timgame compiles
func TestTimGameSimpleProgram(t *testing.T) {
	cmd := exec.Command("pkg-config", "--exists", "sdl3")
	if err := cmd.Run(); err != nil {
		t.Skip("SDL3 not installed")
	}

	timgameDir := filepath.Join(os.Getenv("HOME"), "clones", "timgame")
	gamePath := filepath.Join(timgameDir, "game.tim")

	if _, err := os.Stat(gamePath); os.IsNotExist(err) {
		t.Skip("timgame library not found")
	}

	// Create a simple program that uses timgame
	source := `import "` + gamePath + `"

main = {
    println("Testing timgame import")
}
`

	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "test.tim")
	if err := os.WriteFile(srcFile, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(tmpDir, "test")
	cmd = exec.Command("./tim", srcFile, "-o", binary)
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

	if !strings.Contains(string(output), "Testing timgame import") {
		t.Errorf("Expected 'Testing timgame import' in output, got: %s", output)
	}

	t.Log("Simple timgame program works")
}
