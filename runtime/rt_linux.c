// Linux start code: raw system calls, no libc.
#include "rt.c"

#if defined(__x86_64__)
enum { SYS_read = 0, SYS_write = 1, SYS_close = 3, SYS_mmap = 9, SYS_exit_group = 231, SYS_openat = 257, SYS_getrandom = 318 };

static i64 sys6(i64 n, i64 a, i64 b, i64 c, i64 d, i64 e, i64 f) {
	register i64 r10 __asm__("r10") = d, r8 __asm__("r8") = e, r9 __asm__("r9") = f;
	i64 ret;
	__asm__ volatile("syscall" : "=a"(ret) : "a"(n), "D"(a), "S"(b), "d"(c), "r"(r10), "r"(r8), "r"(r9) : "rcx", "r11", "memory");
	return ret;
}
#elif defined(__aarch64__)
enum { SYS_openat = 56, SYS_close = 57, SYS_read = 63, SYS_write = 64, SYS_exit_group = 94, SYS_mmap = 222, SYS_getrandom = 278 };

static i64 sys6(i64 n, i64 a, i64 b, i64 c, i64 d, i64 e, i64 f) {
	register i64 x8 __asm__("x8") = n, x0 __asm__("x0") = a, x1 __asm__("x1") = b, x2 __asm__("x2") = c,
		     x3 __asm__("x3") = d, x4 __asm__("x4") = e, x5 __asm__("x5") = f;
	__asm__ volatile("svc #0" : "+r"(x0) : "r"(x8), "r"(x1), "r"(x2), "r"(x3), "r"(x4), "r"(x5) : "memory");
	return x0;
}
#elif defined(__riscv)
enum { SYS_openat = 56, SYS_close = 57, SYS_read = 63, SYS_write = 64, SYS_exit_group = 94, SYS_mmap = 222, SYS_getrandom = 278 };

static i64 sys6(i64 n, i64 a, i64 b, i64 c, i64 d, i64 e, i64 f) {
	register i64 a7 __asm__("a7") = n, a0 __asm__("a0") = a, a1 __asm__("a1") = b, a2 __asm__("a2") = c,
		     a3 __asm__("a3") = d, a4 __asm__("a4") = e, a5 __asm__("a5") = f;
	__asm__ volatile("ecall" : "+r"(a0) : "r"(a7), "r"(a1), "r"(a2), "r"(a3), "r"(a4), "r"(a5) : "memory");
	return a0;
}
#endif

static i64 os_write(i64 fd, const void *b, u64 n) { return sys6(SYS_write, fd, (i64)b, (i64)n, 0, 0, 0); }
static i64 os_read(i64 fd, void *b, u64 n) { return sys6(SYS_read, fd, (i64)b, (i64)n, 0, 0, 0); }
static i64 os_open(const char *p, i64 w) { return sys6(SYS_openat, -100, (i64)p, w ? 01 | 0100 | 01000 : 0, 0644, 0, 0); }
static i64 os_close(i64 fd) { return sys6(SYS_close, fd, 0, 0, 0, 0, 0); }
static void os_exit(i64 c) {
	for (;;)
		sys6(SYS_exit_group, c, 0, 0, 0, 0, 0);
}
static void *os_pages(u64 n) {
	i64 p = sys6(SYS_mmap, 0, (i64)n, 3, 0x22, -1, 0);
	return p < 0 && p > -4096 ? 0 : (void *)p;
}
static i64 os_random(void *b, u64 n) { return sys6(SYS_getrandom, (i64)b, (i64)n, 0, 0, 0, 0); }

// rt_start is the program entry: sp points at argc, argv and envp.
void rt_start(u64 *sp, u64 (*main)(R *)) {
	OS os;
	os.write = os_write;
	os.read = os_read;
	os.open = os_open;
	os.close = os_close;
	os.exit = os_exit;
	os.pages = os_pages;
	os.random = os_random;
	char **argv = (char **)(sp + 1);
	R *r = rt_init(&os, sp[0], argv, argv + sp[0] + 1);
	rt_exit(r, main(r));
}
