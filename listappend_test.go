package main

import (
	"strings"
	"testing"
)

func appendPassOutput(t *testing.T, code string) string {
	t.Helper()
	program := NewParser(code).ParseProgram()
	optimizeListAppends(program)
	return program.String()
}

func TestListAppendPass(t *testing.T) {
	tests := []struct {
		name          string
		code          string
		want, notWant []string
	}{
		{
			name:    "loop_append_is_owned",
			code:    "xs := []\nfor i in 0..<10 {\n    xs <- append(xs, i)\n}\nprintln(#xs)\n",
			want:    []string{"__own_xs_1 := 0", "xs <- __append_owned(xs, __own_xs_1, i)"},
			notWant: []string{"__own_xs_1 <- 0"},
		},
		{
			name: "plus_literal_is_owned",
			code: "xs := []\nfor i in 0..<10 {\n    xs <- xs + [i, i]\n}\n",
			want: []string{"__append_owned(xs, __own_xs_1, i, i)"},
		},
		{
			name: "sharing_resets_ownership",
			code: "xs := []\nfor i in 0..<10 {\n    xs <- append(xs, i)\n    ys = xs\n}\n",
			want: []string{"__own_xs_1 <- 0\n  ys = xs"},
		},
		{
			name:    "lambda_reference_disqualifies",
			code:    "xs := []\nf = () -> #xs\nfor i in 0..<10 {\n    xs <- append(xs, i)\n}\n",
			notWant: []string{"__append_owned"},
		},
		{
			name:    "declared_twice_disqualifies",
			code:    "xs := []\nxs := [1]\nfor i in 0..<10 {\n    xs <- append(xs, i)\n}\n",
			notWant: []string{"__append_owned"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendPassOutput(t, tt.code)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in:\n%s", w, got)
				}
			}
		})
	}
}
