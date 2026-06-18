// Completion: 100% - Type system complete with C FFI integration
package engine

// TimType represents a type in the Tim type system
type TimType struct {
	Kind     TypeKind    // The category of type
	CType    string      // For Foreign types, the C type string (e.g., "char*", "SDL_Window*")
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
	case TypeNumber, TypeString, TypeList, TypeMap:
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

// ParseCType converts a C type string to a TimType
func ParseCType(ctype string) *TimType {
	// Remove const, volatile, etc.
	ctype = removeTypeQualifiers(ctype)

	// Check for pointer types
	if len(ctype) > 0 && ctype[len(ctype)-1] == '*' {
		baseType := ctype[:len(ctype)-1]
		baseType = removeTypeQualifiers(baseType)

		if baseType == "char" {
			return &TimType{Kind: TypeCString, CType: ctype}
		}
		return &TimType{Kind: TypeCPointer, CType: ctype}
	}

	// Check for basic types
	switch ctype {
	case "void":
		return &TimType{Kind: TypeCVoid}
	case "int", "int32_t", "unsigned", "unsigned int", "uint32_t":
		return &TimType{Kind: TypeCInt, CType: ctype}
	case "long", "int64_t", "uint64_t":
		return &TimType{Kind: TypeCLong, CType: ctype}
	case "float":
		return &TimType{Kind: TypeCFloat, CType: ctype}
	case "double":
		return &TimType{Kind: TypeCDouble, CType: ctype}
	case "bool", "_Bool":
		return &TimType{Kind: TypeCBool, CType: ctype}
	default:
		// Unknown C type - treat as pointer
		return &TimType{Kind: TypeCPointer, CType: ctype}
	}
}

// removeTypeQualifiers strips const, volatile, etc. from a type string
func removeTypeQualifiers(ctype string) string {
	// Simple implementation - just trim spaces
	// Could be more sophisticated if needed
	result := ""
	words := splitTypeWords(ctype)
	for _, word := range words {
		if word != "const" && word != "volatile" && word != "restrict" {
			if result != "" {
				result += " "
			}
			result += word
		}
	}
	return result
}

// splitTypeWords splits a C type into words
func splitTypeWords(s string) []string {
	var words []string
	var current string
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			if current != "" {
				words = append(words, current)
				current = ""
			}
		} else {
			current += string(s[i])
		}
	}
	if current != "" {
		words = append(words, current)
	}
	return words
}

// Native type constructors
var (
	TypeNumberValue  = &TimType{Kind: TypeNumber}
	TypeStringValue  = &TimType{Kind: TypeString}
	TypeListValue    = &TimType{Kind: TypeList}
	TypeMapValue     = &TimType{Kind: TypeMap}
	TypeCStringValue = &TimType{Kind: TypeCString, CType: "char*"}
	TypeUnknownValue = &TimType{Kind: TypeUnknown}
)
