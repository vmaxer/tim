// macOS start code: libSystem through the global offset table.
#include "rt.c"

// The order of the global offset table written by the compiler (core_macho.go).
enum { F_write, F_read, F_open, F_creat, F_close, F_exit, F_mmap, F_getentropy };

#define CALL(o, i, T) ((T)(o)->imports[i])

static i64 os_write(const OS *o, i64 fd, const void *b, u64 n) { return CALL(o, F_write, i64 (*)(int, const void *, u64))((int)fd, b, n); }
static i64 os_read(const OS *o, i64 fd, void *b, u64 n) { return CALL(o, F_read, i64 (*)(int, void *, u64))((int)fd, b, n); }

// open is variadic, and variadic arguments go on the stack on arm64 macOS:
// files are created with creat, which is not.
static i64 os_open(const OS *o, const char *p, i64 w) {
	if (w)
		return CALL(o, F_creat, int (*)(const char *, unsigned short))(p, 0644);
	return CALL(o, F_open, int (*)(const char *, int))(p, 0);
}

static i64 os_close(const OS *o, i64 fd) { return CALL(o, F_close, int (*)(int))((int)fd); }

static void os_exit(const OS *o, i64 c) {
	for (;;)
		CALL(o, F_exit, void (*)(int))((int)c);
}

static void *os_pages(const OS *o, u64 n) {
	void *p = CALL(o, F_mmap, void *(*)(void *, u64, int, int, int, i64))(0, n, 3, 0x1002, -1, 0);
	return p == (void *)-1 ? 0 : p;
}

static i64 os_random(const OS *o, void *b, u64 n) {
	return CALL(o, F_getentropy, int (*)(void *, u64))(b, n) == 0 ? (i64)n : -1;
}

// rt_start is called by the entry point with dyld's arguments to main.
void rt_start(u64 argc, char **argv, char **envp, void *const *imports, u64 (*main)(R *)) {
	OS os;
	os.write = os_write;
	os.read = os_read;
	os.open = os_open;
	os.close = os_close;
	os.exit = os_exit;
	os.pages = os_pages;
	os.random = os_random;
	os.imports = imports;
	R *r = rt_init(&os, argc, argv, envp);
	r->stack_top = (u64 *)__builtin_frame_address(0);
	rt_exit(r, main(r));
}
