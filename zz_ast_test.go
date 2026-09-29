package main

import (
	"fmt"
	"os"
	"testing"
)

func TestDumpAST(t *testing.T) {
	src, _ := os.ReadFile(os.Getenv("TIM_SRC"))
	p := NewParserWithFilename(string(src), "test.tim")
	prog := p.ParseProgram()
	for _, s := range prog.Statements {
		fmt.Printf("%T %s\n", s, s.String())
	}
}
