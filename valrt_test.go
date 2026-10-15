package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestValrtOpcodesMatchC(t *testing.T) {
	src, err := os.ReadFile("numrt/val.c")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)enum \{\s*(V_STR = 64.*?)\};`).FindSubmatch(src)
	if m == nil {
		t.Fatal("V_ enum not found in numrt/val.c")
	}
	var names []string
	for f := range strings.SplitSeq(string(m[1]), ",") {
		if f = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(f), "= 64")); f != "" {
			names = append(names, strings.TrimSpace(strings.TrimSuffix(f, "= 64")))
		}
	}
	want := map[string]int{
		"V_STR": valStr, "V_LEN": valLen, "V_INDEX": valIndex, "V_EQ": valEq, "V_NE": valNe,
		"V_ADD": valAdd, "V_APPEND": valAppend, "V_GROW": valGrow, "V_CONS": valCons,
		"V_HEAD": valHead, "V_TAIL": valTail, "V_POP": valPop, "V_SLICE": valSlice,
		"V_REPEAT": valRepeat, "V_RUNES": valRunes, "V_BYTES": valBytes, "V_UTF8": valUTF8,
		"V_SET": valSet, "V_IN": valIn, "V_KEYAT": valKeyAt, "V_ERROR": valError,
		"V_ERRSTR": valErrStr, "V_KIND": valKind, "V_TRUTHY": valTruthy, "V_KEYS": valKeys,
		"V_VALUES": valValues, "V_FROM_UTF8": valFromUTF8, "V_RANGE": valRange,
		"V_POPCOUNT": valPopcount, "V_CLZ": valClz, "V_CTZ": valCtz, "V_SIN": valSin,
		"V_COS": valCos, "V_TAN": valTan, "V_ASIN": valAsin, "V_ACOS": valAcos,
		"V_ATAN": valAtan, "V_ATAN2": valAtan2, "V_EXP": valExp, "V_LOG": valLog,
		"V_LOG10": valLog10, "V_CMP": valCmp,
	}
	if len(names) != len(want) {
		t.Fatalf("C has %d ops, Go has %d", len(names), len(want))
	}
	for i, n := range names {
		if want[n] != 64+i {
			t.Errorf("%s: C value %d, Go value %d", n, 64+i, want[n])
		}
	}
}
