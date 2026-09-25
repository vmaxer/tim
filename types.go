// Completion: 100% - Type system complete with C FFI integration
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

// IsNative returns true if this is a native Tim type
func (t *TimType) IsNative() bool {
	switch t.Kind {
	case TypeNumber, TypeString, TypeList, TypeMap, TypeBoolean:
		return true
	default:
		return false
	}
}

// IsForeign returns true if this is a C foreign type
func (t *TimType) IsForeign() bool {
	return !t.IsNative() && t.Kind != TypeUnknown
}

// IsPointer returns true if this represents a pointer type
func (t *TimType) IsPointer() bool {
	return t.Kind == TypeCString || t.Kind == TypeCPointer
}

// NeedsConversionToC returns true if this type needs conversion when passing to C
func (t *TimType) NeedsConversionToC() bool {
	// Tim strings need conversion to C strings
	return t.Kind == TypeString
}

// NeedsConversionFromC returns true if this type needs conversion when receiving from C
func (t *TimType) NeedsConversionFromC() bool {
	// Currently no conversions needed from C to Tim
	// (C strings stay as cstrings until explicitly converted)
	return false
}

// Native type constructors
var (
	TypeNumberValue  = &TimType{Kind: TypeNumber}
	TypeStringValue  = &TimType{Kind: TypeString}
	TypeListValue    = &TimType{Kind: TypeList}
	TypeMapValue     = &TimType{Kind: TypeMap}
	TypeBooleanValue = &TimType{Kind: TypeBoolean}
	TypeCStringValue = &TimType{Kind: TypeCString, CType: "char*"}
	TypeUnknownValue = &TimType{Kind: TypeUnknown}
)
