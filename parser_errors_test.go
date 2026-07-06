package main

import (
	"strings"
	"testing"
)

// TestParserDiagnostics verifies that common mistakes produce precise,
// actionable error messages (right cause, right location, useful hints).
func TestParserDiagnostics(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		wantErr  string // substring the compile error must contain
		wantErr2 string // optional second required substring
	}{
		{
			name:    "missing operand after plus",
			code:    "main = { x = 1 + }\n",
			wantErr: "expected expression after '+'",
		},
		{
			name:    "missing operand after star",
			code:    "main = { x = 2 * }\n",
			wantErr: "expected expression after '*'",
		},
		{
			name:    "missing comparison operand",
			code:    "main = { x = 3 < }\n",
			wantErr: "expected expression after '<'",
		},
		{
			name:    "unterminated string points at opening quote",
			code:    "main = { println(\"unclosed }\n",
			wantErr: "unterminated string literal",
		},
		{
			name:    "unterminated f-string",
			code:    "main = { x = f\"oops {1}\n",
			wantErr: "unterminated f-string literal",
		},
		{
			name:     "undefined function suggests similar name",
			code:     "main = { prinltn(42) }\n",
			wantErr:  "undefined function: prinltn",
			wantErr2: "println",
		},
		{
			name:     "undefined user function suggests defined one",
			code:     "helper = x -> x * 2\nmain = { println(helpr(3)) }\n",
			wantErr:  "undefined function: helpr",
			wantErr2: "helper",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileTestCodeAllowError(t, tt.code)
			if err == nil {
				t.Fatalf("expected compilation error containing %q, got success", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
			if tt.wantErr2 != "" && !strings.Contains(err.Error(), tt.wantErr2) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr2)
			}
		})
	}
}
