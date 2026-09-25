package main

import (
	"fmt"
	"os"
	"testing"
)

func TestDumpAST(t *testing.T) {
	src, _ := os.ReadFile(os.Getenv("TIM_SRC"))
	p := NewParser(string(src))
	prog := p.ParseProgram()
	for _, s := range prog.Statements {
		fmt.Printf("%T %s\n", s, s.String())
	}
}
