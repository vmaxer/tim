package main

import "testing"

func TestCConstantExpressions(t *testing.T) {
	p := NewCParser()
	p.results.Constants["A"] = 4
	for _, c := range []struct {
		e string
		v int64
	}{{"((SDL_AudioDeviceID) 0xFFFFFFFFu)", 0xFFFFFFFF}, {"0x00000020u", 32}, {"(1u << 5)", 32}, {"SDL_UINT64_C(0x20)", 32},
		{"A | 1", 5}, {"1 + 2 * 3", 7}, {"(-1)", -1}, {"~0", -1}, {"0xFFFFFFFFFFFFFFFFull", -1}, {"(A << 2) | (A >> 1)", 18}, {"(int)-1", -1}} {
		v, ok := p.evalConstant(c.e)
		if !ok || v != c.v {
			t.Errorf("%s = %d %v, want %d", c.e, v, ok, c.v)
		}
	}
	if _, ok := p.evalConstant("SDL_FOO"); ok {
		t.Error("unknown constant evaluated")
	}
}
