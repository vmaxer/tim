package main

import (
	"os"
	"strings"
	"testing"
)

// TestStdlibDocumented keeps STDLIB.md in step with the builtin table.
func TestStdlibDocumented(t *testing.T) {
	doc, err := os.ReadFile("STDLIB.md")
	if err != nil {
		t.Fatal(err)
	}
	for name := range builtins {
		if strings.HasPrefix(name, "__") {
			continue
		}
		if !strings.Contains(string(doc), name) {
			t.Errorf("STDLIB.md does not mention %s", name)
		}
	}
}
