package main

// builtin describes a function every Tim program can call. Programs may
// define their own function with the same name, which then takes precedence.
type builtin struct {
	min, max int // accepted argument counts; max < 0 means any
	doc      string
}

var builtins = map[string]builtin{
	// Output and the process
	"print":    {0, -1, "print values separated by spaces"},
	"println":  {0, -1, "print values separated by spaces, then a newline"},
	"printf":   {1, -1, "print with a C-style format: %d %f %.2f %s %v %x %%"},
	"eprint":   {0, -1, "print to stderr"},
	"eprintln": {0, -1, "print to stderr with a newline"},
	"eprintf":  {1, -1, "printf to stderr"},
	"exit":     {0, 1, "end the program with an exit code"},
	"args":     {0, 0, "the command-line arguments as a list of strings"},
	"env":      {1, 1, "an environment variable, or an error"},

	// Files and input
	"readln":     {0, 0, "read a line from stdin without its newline, or an error at end of input"},
	"read_file":  {1, 1, "read a whole file as a string, or an error"},
	"write_file": {2, 2, "write a string to a file, returning its length or an error"},

	// Conversions and inspection
	"str":   {1, 1, "format a value as a string"},
	"num":   {1, 1, "parse a string as an exact number, or an error"},
	"float": {1, 1, "convert a number to an inexact float64"},
	"type":  {1, 1, `the type of a value: "num" "str" "list" "map" "fn" "error" "ptr"`},
	"error": {1, 1, "an error value with the given code"},
	"chr":   {1, 1, "the UTF-8 string for a code point"},
	"ord":   {1, 1, "the first code point of a string"},
	"bytes": {1, 1, "the bytes of a string as a list of numbers"},
	"runes": {1, 1, "the code points of a string as a list of numbers"},

	// Numbers
	"abs": {1, 1, ""}, "floor": {1, 1, ""}, "ceil": {1, 1, ""}, "round": {1, 1, ""}, "trunc": {1, 1, ""},
	"sqrt": {1, 1, ""}, "exp": {1, 1, ""}, "log": {1, 1, ""}, "log10": {1, 1, ""},
	"sin": {1, 1, ""}, "cos": {1, 1, ""}, "tan": {1, 1, ""},
	"asin": {1, 1, ""}, "acos": {1, 1, ""}, "atan": {1, 1, ""}, "atan2": {2, 2, ""},
	"pow": {2, 2, ""}, "min": {1, -1, ""}, "max": {1, -1, ""},
	"gcd":    {2, 2, "greatest common divisor of two integers"},
	"random": {0, 0, "a random float64 in [0, 1)"},
	"bit":    {2, 2, "bit n of x, 0 or 1"}, "rotl": {2, 2, "rotate x left by n bits"}, "rotr": {2, 2, "rotate x right by n bits"},

	// Strings
	"upper": {1, 1, ""}, "lower": {1, 1, ""}, "trim": {1, 1, ""},
	"split":       {2, 2, "split a string on a separator"},
	"join":        {2, 2, "join a list of strings with a separator"},
	"replace":     {3, 3, "replace every occurrence of old with new"},
	"starts_with": {2, 2, ""}, "ends_with": {2, 2, ""},
	"find": {2, 2, "the index of a substring or element, or -1"},

	// Lists and maps
	"push":    {2, 2, "append an element to a mutable list"},
	"pop":     {1, 1, "remove and return the last element of a mutable list"},
	"keys":    {1, 1, "the keys of a map in insertion order"},
	"values":  {1, 1, "the values of a map in insertion order"},
	"remove":  {2, 2, "remove a key from a mutable map"},
	"sort":    {1, 2, "a sorted copy of a list, optionally by a key function"},
	"reverse": {1, 1, "a reversed copy of a list or string"},
	"sum":     {1, 1, ""},
	"map":     {2, 2, "apply a function to each element"},
	"filter":  {2, 2, "the elements for which a function is true"},
	"fold":    {3, 3, "combine the elements from left to right: fold(xs, init, (acc, x) -> ...)"},
	"any":     {2, 2, ""}, "all": {2, 2, ""},
	"zip":       {2, 2, "pairs of corresponding elements"},
	"enumerate": {1, 1, "pairs of index and element"},

	"__sort_keys": {2, 2, ""}, // sort(xs, key) in the prelude
}

// legacyBuiltins are low-level functions only the legacy code generators
// provide (raw memory, arenas, atomics, processes); programs that use them
// are compiled by the legacy backends.
var legacyBuiltins = map[string]bool{
	"__tim_map_update": true, "alloc": true, "and": true, "append": true, "approx": true,
	"arena_alloc": true, "arena_create": true, "arena_destroy": true, "arena_reset": true,
	"atomic_add": true, "atomic_cas": true, "atomic_load": true, "atomic_store": true, "call": true,
	"calloc": true, "chan": true, "close": true, "clz": true, "cstr": true, "ctz": true,
	"dlclose": true, "dlopen": true, "dlsym": true, "exitf": true, "exitln": true, "float32": true,
	"float64": true, "fork": true, "free": true, "getpid": true, "head": true, "int16": true,
	"int32": true, "int64": true, "int8": true, "is_finite": true, "is_inf": true, "is_nan": true,
	"is_neg_inf": true, "is_pos_inf": true, "load": true, "malloc": true, "mmap": true,
	"munmap": true, "or": true, "peek32": true, "peek8": true, "popcount": true, "printa": true,
	"proc_exit": true, "ptr": true, "read_f32": true, "read_f64": true, "read_i16": true,
	"read_i32": true, "read_i64": true, "read_i8": true, "read_u16": true, "read_u32": true,
	"read_u64": true, "read_u8": true, "realloc": true, "result_value": true, "safe_divide": true,
	"safe_divide_result": true, "safe_ln": true, "safe_ln_result": true, "safe_sqrt": true,
	"safe_sqrt_result": true, "sizeof_f32": true, "sizeof_f64": true, "sizeof_i16": true,
	"sizeof_i32": true, "sizeof_i64": true, "sizeof_i8": true, "sizeof_ptr": true, "sizeof_u16": true,
	"sizeof_u32": true, "sizeof_u64": true, "sizeof_u8": true, "store": true, "syscall": true,
	"tail": true, "uint16": true, "uint32": true, "uint64": true, "uint8": true, "vadd": true,
	"vdiv": true, "vdot": true, "vmul": true, "vsub": true, "waitpid": true, "write_f32": true,
	"write_f64": true, "write_i16": true, "write_i32": true, "write_i64": true, "write_i8": true,
	"write_u16": true, "write_u32": true, "write_u64": true, "write_u8": true,
}
