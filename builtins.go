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
	"exitf":    {1, -1, "printf to stderr, then exit with 1"},
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
	"gcd":      {2, 2, "greatest common divisor of two integers"},
	"random":   {0, 0, "a random float64 in [0, 1)"},
	"bit":      {2, 2, "bit n of x, 0 or 1"},
	"popcount": {1, 1, "the number of 1 bits of a 64-bit integer"},
	"clz":      {1, 1, "leading zero bits of a 64-bit integer"}, "ctz": {1, 1, "trailing zero bits"}, "rotl": {2, 2, "rotate x left by n bits"}, "rotr": {2, 2, "rotate x right by n bits"},

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

	// Memory through ptr values, at a byte offset
	"cstr":    {1, 1, "copy the C string at a ptr into a string"},
	"alloc":   {1, 1, "n zeroed bytes on the garbage-collected heap, as a ptr"},
	"read_i8": {2, 2, ""}, "read_u8": {2, 2, ""}, "read_i16": {2, 2, ""}, "read_u16": {2, 2, ""},
	"read_i32": {2, 2, ""}, "read_u32": {2, 2, ""}, "read_i64": {2, 2, ""}, "read_u64": {2, 2, ""},
	"read_f32": {2, 2, ""}, "read_f64": {2, 2, ""}, "read_ptr": {2, 2, ""},
	"write_i8": {3, 3, ""}, "write_u8": {3, 3, ""}, "write_i16": {3, 3, ""}, "write_u16": {3, 3, ""},
	"write_i32": {3, 3, ""}, "write_u32": {3, 3, ""}, "write_i64": {3, 3, ""}, "write_u64": {3, 3, ""},
	"write_f32": {3, 3, ""}, "write_f64": {3, 3, ""}, "write_ptr": {3, 3, ""},

	"__sort_keys": {2, 2, ""}, // sort(xs, key) in the prelude
}

// memKinds are the C types of read_* and write_*.
var memKinds = map[string]uint8{"i8": cI8, "u8": cU8, "i16": cI16, "u16": cU16, "i32": cI32, "u32": cU32,
	"i64": cI64, "u64": cU64, "f32": cF32, "f64": cF64, "ptr": cPtr}

// removedBuiltins are Tim 1 builtins, with what to use instead.
var removedBuiltins = map[string]string{
	"malloc": "use alloc(n), or c.malloc(n)", "calloc": "use alloc(n)", "realloc": "use c.realloc(p, n)",
	"free":        "memory is garbage collected; use c.free for c.malloc memory",
	"arena_alloc": "use an arena { } block", "arena_create": "use an arena { } block",
	"arena_destroy": "use an arena { } block", "arena_reset": "use an arena { } block",
	"peek8": "use read_u8(p, offset)", "peek32": "use read_u32(p, offset)",
	"load": "use read_u64(p, offset) and the other read_ functions", "store": "use write_u64(p, offset, v) and the other write_ functions",
	"int8": "use x as int8", "int16": "use x as int16", "int32": "use x as int32", "int64": "use x as int64",
	"uint8": "use x as uint8", "uint16": "use x as uint16", "uint32": "use x as uint32", "uint64": "use x as uint64",
	"float32": "use x as float", "float64": "use float(x)",
	"head": "use xs[0]", "tail": "use xs[1:]", "append": "use push(xs, x) or xs + [x]",
	"and": "use the and operator", "or": "use the or operator", "exitln": "use exitf",
	"is_nan": "x != x is true only for NaN", "is_inf": "compare with 1 / 0.0", "is_finite": "compare with 1 / 0.0",
	"safe_divide": "division by zero is already an error value: a / b or! 0", "safe_sqrt": "use sqrt(x) or! 0", "safe_ln": "use log(x) or! 0",
	"getpid": "call C: c.getpid()", "fork": "call C: c.fork()", "waitpid": "call C: c.waitpid(pid, 0, 0)",
	"mmap": "call C: c.mmap(...)", "munmap": "call C: c.munmap(p, n)", "syscall": "call C functions through import",
	"dlopen": "import the library: import foo as f", "dlsym": "import the library: import foo as f",
	"atomic_add": "Tim 2 programs are single-threaded", "atomic_cas": "Tim 2 programs are single-threaded",
	"atomic_load": "Tim 2 programs are single-threaded", "atomic_store": "Tim 2 programs are single-threaded",
	"chan":      "Tim 2 programs are single-threaded",
	"sizeof_i8": "it is 1", "sizeof_u8": "it is 1", "sizeof_i16": "it is 2", "sizeof_u16": "it is 2",
	"sizeof_i32": "it is 4", "sizeof_u32": "it is 4", "sizeof_f32": "it is 4",
	"sizeof_i64": "it is 8", "sizeof_u64": "it is 8", "sizeof_f64": "it is 8", "sizeof_ptr": "it is 8",
}
