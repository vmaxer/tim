package main

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// C types as the runtime converts them (runtime/rt.c, C_DYN ...).
const (
	cDyn uint8 = iota
	cI8
	cU8
	cI16
	cU16
	cI32
	cU32
	cI64
	cU64
	cF32
	cF64
	cPtr
	cCstr
	cVoid
)

// cFunc is a C function a program calls.
type cFunc struct {
	lib      *cLib
	name     string
	params   []uint8
	ret      uint8
	fixed    int  // declared parameters; more arguments are variadic
	variadic bool // the declaration ends in ...
	known    bool // the signature came from a header or the libc table
}

// cLib is an imported C library.
type cLib struct {
	name   string // as imported: "sdl3", "c", "libfoo.so"
	funcs  map[string]*CFunctionSignature
	consts map[string]int64
}

// cImport is a function the executable imports.
type cImport struct {
	lib  *cLib
	name string
}

// libcDecls are the C library functions Tim knows without headers.
const libcDecls = `
void *malloc(size_t); void *calloc(size_t, size_t); void *realloc(void *, size_t); void free(void *);
void *memcpy(void *, void *, size_t); void *memmove(void *, void *, size_t); void *memset(void *, int, size_t);
int memcmp(void *, void *, size_t); size_t strlen(char *); int strcmp(char *, char *); int strncmp(char *, char *, size_t);
char *strcpy(char *, char *); char *strncpy(char *, char *, size_t); char *strcat(char *, char *); char *strdup(char *);
char *strchr(char *, int); char *strstr(char *, char *); int puts(char *); int putchar(int); int getchar(void);
int printf(char *, ...); int sprintf(char *, char *, ...); int snprintf(char *, size_t, char *, ...);
char *getenv(char *); int system(char *); void exit(int); int abs(int); long labs(long); int atoi(char *);
double atof(char *); long strtol(char *, void *, int); double strtod(char *, void *); int rand(void); void srand(unsigned);
long time(void *); long clock(void); unsigned sleep(unsigned); int usleep(unsigned); int getpid(void);
long write(int, void *, size_t); long read(int, void *, size_t); int close(int); int fflush(void *);
double sqrt(double); double sin(double); double cos(double); double tan(double); double asin(double); double acos(double);
double atan(double); double atan2(double, double); double pow(double, double); double exp(double); double log(double);
double log2(double); double log10(double); double fabs(double); double floor(double); double ceil(double);
double round(double); double trunc(double); double fmod(double, double); double hypot(double, double);
double fmin(double, double); double fmax(double, double); double cbrt(double); float sqrtf(float);
void *mmap(void *, size_t, int, int, int, long); int munmap(void *, size_t); int fork(void); int waitpid(int, void *, int);
void _exit(int); int kill(int, int); int getppid(void); void *fopen(char *, char *); int fclose(void *);
`

var libcLib = func() *cLib {
	l := &cLib{name: "c", funcs: map[string]*CFunctionSignature{}, consts: map[string]int64{
		"NULL": 0, "EXIT_SUCCESS": 0, "EXIT_FAILURE": 1, "RAND_MAX": 2147483647, "SEEK_SET": 0, "SEEK_CUR": 1, "SEEK_END": 2,
	}}
	for _, d := range strings.Split(libcDecls, ";") {
		if sig, name := parseCDecl(d); name != "" {
			l.funcs[name] = sig
		}
	}
	return l
}()

// parseCDecl reads a declaration like "void *malloc(size_t)".
func parseCDecl(d string) (*CFunctionSignature, string) {
	d = strings.TrimSpace(d)
	open := strings.IndexByte(d, '(')
	if open < 0 || !strings.HasSuffix(d, ")") {
		return nil, ""
	}
	head := strings.TrimSpace(d[:open])
	i := strings.LastIndexAny(head, " *")
	name, ret := head[i+1:], strings.TrimSpace(head[:i+1])
	sig := &CFunctionSignature{ReturnType: ret}
	for _, p := range strings.Split(d[open+1:len(d)-1], ",") {
		if p = strings.TrimSpace(p); p != "" && p != "void" {
			sig.Params = append(sig.Params, CFunctionParam{Type: p})
		}
	}
	return sig, name
}

// cKind maps a C type to how its values convert.
func cKind(t string) uint8 {
	t = " " + strings.NewReplacer("*", " * ", "\t", " ").Replace(t) + " "
	for _, q := range []string{" const ", " volatile ", " restrict ", " __restrict ", " struct ", " enum ", " signed "} {
		t = strings.ReplaceAll(t, q, " ")
	}
	stars := strings.Count(t, "*")
	words := strings.Fields(strings.ReplaceAll(t, "*", " "))
	base := strings.Join(words, " ")
	if stars > 0 {
		if stars == 1 && base == "char" {
			return cCstr
		}
		return cPtr
	}
	switch base {
	case "void":
		return cVoid
	case "char", "int8_t", "Sint8", "gint8":
		return cI8
	case "unsigned char", "uint8_t", "Uint8", "bool", "_Bool", "SDL_bool", "guint8":
		return cU8
	case "short", "short int", "int16_t", "Sint16":
		return cI16
	case "unsigned short", "unsigned short int", "uint16_t", "Uint16":
		return cU16
	case "int", "int32_t", "Sint32", "wchar_t", "pid_t":
		return cI32
	case "unsigned", "unsigned int", "uint32_t", "Uint32", "DWORD", "UINT":
		return cU32
	case "long", "long int", "long long", "long long int", "int64_t", "Sint64", "ssize_t", "intptr_t", "ptrdiff_t", "off_t", "time_t", "clock_t":
		return cI64
	case "unsigned long", "unsigned long int", "unsigned long long", "uint64_t", "Uint64", "size_t", "uintptr_t":
		return cU64
	case "float":
		return cF32
	case "double", "long double":
		return cF64
	case "...", ". .", "":
		return cDyn
	}
	if strings.HasSuffix(base, "Flags") || strings.HasSuffix(base, "ID") {
		return cU32
	}
	return cI64
}

// signature turns a parsed declaration into argument conversions.
func (l *cLib) function(name string) *cFunc {
	f := &cFunc{lib: l, name: name, ret: cI64}
	sig := l.funcs[name]
	if sig == nil {
		return f
	}
	f.known = true
	f.ret = cKind(sig.ReturnType)
	for _, p := range sig.Params {
		k := cKind(p.Type)
		if k == cDyn {
			f.variadic = true
			break
		}
		f.params = append(f.params, k)
	}
	f.fixed = len(f.params)
	return f
}

// loadCLib finds the functions and constants of a C library.
func loadCLib(name, sourceDir string) *cLib {
	switch name {
	case "c", "C", "libc":
		return libcLib
	}
	l := &cLib{name: name, funcs: map[string]*CFunctionSignature{}, consts: map[string]int64{}}
	if h, err := ExtractConstantsFromLibrary(strings.TrimSuffix(name, ".so"), sourceDir); err == nil {
		l.funcs, l.consts = h.Functions, h.Constants
	}
	return l
}

// linkName is the file a library is loaded from at run time.
func (l *cLib) linkName(os OS) []string {
	if l == libcLib {
		switch os {
		case OSWindows:
			return []string{"msvcrt.dll"}
		case OSDarwin:
			return []string{"/usr/lib/libSystem.B.dylib"}
		}
		return []string{"libc.so.6", "libm.so.6"}
	}
	if strings.ContainsAny(l.name, "./") {
		return []string{l.name}
	}
	lib := pkgConfigLib(l.name)
	switch os {
	case OSWindows:
		return []string{mapLibraryToDLL(l.name)}
	case OSDarwin:
		for _, dir := range []string{"/opt/homebrew/lib", "/usr/local/lib"} {
			if matches, _ := filepath.Glob(filepath.Join(dir, "lib"+lib+".*.dylib")); len(matches) > 0 {
				return []string{matches[0]}
			}
		}
		return []string{"/opt/homebrew/lib/lib" + lib + ".dylib"}
	}
	if so := soname(lib); so != "" {
		return []string{so}
	}
	return []string{"lib" + lib + ".so"}
}

// pkgConfigLib returns the -l name pkg-config gives for a package, or the name itself.
func pkgConfigLib(name string) string {
	for _, pkg := range []string{name, strings.ToUpper(name), "lib" + name} {
		out, err := exec.Command("pkg-config", "--libs-only-l", pkg).Output()
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(out)) {
			if strings.HasPrefix(f, "-l") {
				return f[2:]
			}
		}
	}
	return name
}

// soname reads the SONAME of the development library lib<name>.so, so the
// executable needs only the runtime package.
func soname(lib string) string {
	dirs := []string{"/usr/local/lib", "/usr/lib", "/usr/lib64", "/lib", "/usr/lib/x86_64-linux-gnu",
		"/usr/lib/aarch64-linux-gnu", "/usr/lib/riscv64-linux-gnu"}
	if out, err := exec.Command("pkg-config", "--libs-only-L", lib).Output(); err == nil {
		for _, f := range strings.Fields(string(out)) {
			dirs = append([]string{strings.TrimPrefix(f, "-L")}, dirs...)
		}
	}
	for _, d := range dirs {
		path := filepath.Join(d, "lib"+lib+".so")
		if _, err := os.Stat(path); err != nil {
			continue
		}
		f, err := elf.Open(path)
		if err != nil {
			continue
		}
		names, _ := f.DynString(elf.DT_SONAME)
		f.Close()
		if len(names) > 0 {
			return names[0]
		}
	}
	return ""
}
