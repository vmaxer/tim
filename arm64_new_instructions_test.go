package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// newTestARM64Out builds an ARM64Out that writes into the returned buffer, so
// instruction encodings can be checked in isolation.
func newTestARM64Out() (*ARM64Out, *bytes.Buffer) {
	var buf bytes.Buffer
	out := &Out{writer: &BufferWrapper{&buf}}
	return &ARM64Out{out: out}, &buf
}

// TestARM64NewInstructionEncodings verifies the instruction helpers added for
// Tim-native formatting and the bit builtins encode exactly as the system
// assembler (`as -arch arm64`) produces them.
func TestARM64NewInstructionEncodings(t *testing.T) {
	tests := []struct {
		name string
		emit func(a *ARM64Out) error
		want uint32 // expected 32-bit instruction word
	}{
		{"fcvtns x0,d0", func(a *ARM64Out) error { return a.FcvtnsDoubleToInt64("x0", "d0") }, 0x9e600000},
		{"clz x0,x0", func(a *ARM64Out) error { return a.Clz64("x0", "x0") }, 0xdac01000},
		{"rbit x0,x0", func(a *ARM64Out) error { return a.Rbit64("x0", "x0") }, 0xdac00000},
		{"msub x12,x11,x10,x0", func(a *ARM64Out) error { return a.Msub64("x12", "x11", "x10", "x0") }, 0x9b0a816c},
		{"add x6,x3,x6", func(a *ARM64Out) error { return a.AddReg64("x6", "x3", "x6") }, 0x8b060066},
		{"lsl x6,x5,#4", func(a *ARM64Out) error { return a.LslImm64("x6", "x5", 4) }, 0xd37ceca6},
		{"ldrb w6,[x5,x4]", func(a *ARM64Out) error { return a.LdrbRegOffset("x6", "x5", "x4") }, 0x386468a6},
		{"cnt v1.8b,v1.8b", func(a *ARM64Out) error { return a.CntVec8b("d1", "d1") }, 0x0e205821},
		{"addv b1,v1.8b", func(a *ARM64Out) error { return a.AddvBytes8b("d1", "d1") }, 0x0e31b821},
		{"fmov w0,s1", func(a *ARM64Out) error { return a.FmovSingleToGP("x0", "d1") }, 0x1e260020},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, buf := newTestARM64Out()
			if err := tt.emit(a); err != nil {
				t.Fatalf("emit %s: %v", tt.name, err)
			}
			if buf.Len() != 4 {
				t.Fatalf("%s: emitted %d bytes, want 4", tt.name, buf.Len())
			}
			got := binary.LittleEndian.Uint32(buf.Bytes())
			if got != tt.want {
				t.Errorf("%s: got 0x%08x, want 0x%08x", tt.name, got, tt.want)
			}
		})
	}
}
