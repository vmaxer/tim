package main

// Container operation codes shared with numrt/val.c, reached through the same
// entry point as the numeric runtime.
const (
	valStr = iota + 64
	valLen
	valIndex
	valEq
	valNe
	valAdd
	valAppend
	valGrow
	valCons
	valHead
	valTail
	valPop
	valSlice
	valRepeat
	valRunes
	valBytes
	valUTF8
	valSet
	valIn
	valKeyAt
	valError
	valErrStr
	valKind
	valTruthy
	valKeys
	valValues
	valFromUTF8
	valRange
	valPopcount
	valClz
	valCtz
	valSin
	valCos
	valTan
	valAsin
	valAcos
	valAtan
	valAtan2
	valExp
	valLog
	valLog10
	valCmp
	valCStr
)

const valAbsent = 0x7FFC0000DEAD0001

var valMathBuiltins = map[string]int{
	"sin": valSin, "cos": valCos, "tan": valTan, "asin": valAsin, "acos": valAcos,
	"atan": valAtan, "atan2": valAtan2, "exp": valExp, "log": valLog, "log10": valLog10,
	"popcount": valPopcount, "clz": valClz, "ctz": valCtz,
}
