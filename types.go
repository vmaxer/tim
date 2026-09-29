package main

// TimType represents a type in the Tim type system
type TimType struct {
	Kind     TypeKind // The category of type
	CType    string   // For Foreign types, the C type string (e.g., "char*", "SDL_Window*")
	ElemType *TimType // For container types, the element type
}

// TypeKind represents the category of a type
type TypeKind int

const (
	TypeUnknown  TypeKind = iota
	TypeNumber            // Tim's native float64 type
	TypeString            // Tim's native string (map-based)
	TypeList              // Tim's native list (map-based)
	TypeMap               // Tim's native map
	TypeBoolean           // Tim's native boolean (yes/no)
	TypeCString           // C char* (null-terminated string)
	TypeCInt              // C int, int32_t, etc.
	TypeCLong             // C long, int64_t
	TypeCFloat            // C float
	TypeCDouble           // C double
	TypeCBool             // C bool, _Bool
	TypeCPointer          // Generic C pointer (void*, SDL_Window*, etc.)
	TypeCVoid             // C void (for return types)
)

// String returns a human-readable representation of the type
func (t *TimType) String() string {
	switch t.Kind {
	case TypeUnknown:
		return "unknown"
	case TypeNumber:
		return "number"
	case TypeString:
		return "string"
	case TypeList:
		if t.ElemType != nil {
			return "list[" + t.ElemType.String() + "]"
		}
		return "list"
	case TypeMap:
		return "map"
	case TypeBoolean:
		return "bool"
	case TypeCString:
		return "cstring"
	case TypeCInt:
		return "cint"
	case TypeCLong:
		return "clong"
	case TypeCFloat:
		return "cfloat"
	case TypeCDouble:
		return "cdouble"
	case TypeCBool:
		return "cbool"
	case TypeCPointer:
		return "cpointer:" + t.CType
	case TypeCVoid:
		return "void"
	default:
		return "unknown"
	}
}
