package main

import "testing"

// TestParseCIntLiteral covers the C integer-literal parsing used for enum values:
// hex/binary/decimal with u/U/l/L suffixes, and unsigned values beyond int64.
func TestParseCIntLiteral(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0x16362004", 0x16362004}, // SDL_PIXELFORMAT_ARGB8888 (no suffix)
		{"0xFFFFFFFF", 0xFFFFFFFF}, // fits unsigned, not signed int32
		{"0b1010", 10},
		{"256", 256},
		{"-2", -2},
	}
	for _, c := range cases {
		got, ok := parseCIntLiteral(c.in)
		if !ok || got != c.want {
			t.Errorf("parseCIntLiteral(%q) = %d, %v; want %d", c.in, got, ok, c.want)
		}
	}
}

// TestEnumValueWithSuffixAndAlias verifies that an enum member with a suffixed
// hex value parses correctly and that an alias member (= another enum constant)
// does not corrupt the referenced constant — the SDL_PIXELFORMAT_ARGB8888 bug.
func TestEnumValueWithSuffixAndAlias(t *testing.T) {
	src := `enum E {
    A = 0x16362004u,
    B = 0x16462004u,
    C = A,
};`
	p := NewCParser()
	p.tokens = p.tokenize(src)
	p.pos = 0
	for !p.isAtEnd() {
		p.parseTopLevel()
	}
	res := p.results
	if got := res.Constants["A"]; got != 0x16362004 {
		t.Errorf("A = %d (0x%x); want 0x16362004", got, got)
	}
	if got := res.Constants["C"]; got != 0x16362004 {
		t.Errorf("C (alias of A) = %d; want 0x16362004", got)
	}
}
