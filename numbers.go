package main

import (
	"fmt"
	"math"
	"math/big"
	"strings"
)

// numFixMax bounds the integers a float64 holds exactly; larger ones and
// fractions are exact big numbers.
const numFixMax = 1 << 53

// parseNumber returns the exact value of a numeric literal.
func parseNumber(s string) (*NumberExpr, error) {
	s = strings.ReplaceAll(s, "_", "")
	if len(s) > 2 && s[0] == '0' && strings.ContainsRune("xXbBoO", rune(s[1])) {
		n, ok := new(big.Int).SetString(s, 0)
		if !ok {
			return nil, fmt.Errorf("invalid number literal: %s", s)
		}
		return newExactNumber(new(big.Rat).SetInt(n)), nil
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("invalid number literal: %s", s)
	}
	return newExactNumber(r), nil
}

// newExactNumber canonicalizes: small integers are plain float64 values.
func newExactNumber(r *big.Rat) *NumberExpr {
	if r.IsInt() && r.Num().IsInt64() {
		if v := r.Num().Int64(); v > -numFixMax && v < numFixMax {
			return &NumberExpr{Value: float64(v)}
		}
	}
	f, _ := r.Float64()
	return &NumberExpr{Value: f, Exact: r}
}

// Rat returns the exact value, or false if the number is inexact.
func (n *NumberExpr) Rat() (*big.Rat, bool) {
	if n.Exact != nil {
		return n.Exact, true
	}
	if v := n.Value; v == math.Trunc(v) && v > -numFixMax && v < numFixMax {
		return new(big.Rat).SetInt64(int64(v)), true
	}
	return nil, false
}
