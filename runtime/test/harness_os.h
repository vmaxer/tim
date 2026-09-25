#define _GNU_SOURCE
#include "../rt.c"
#include <fcntl.h>
#include <sys/mman.h>
#include <sys/random.h>
#include <unistd.h>

static i64 os_write(i64 fd, const void *b, u64 n) { return write((int)fd, b, n); }
static i64 os_read(i64 fd, void *b, u64 n) { return read((int)fd, b, n); }
static i64 os_open(const char *p, i64 w) { return open(p, w ? O_WRONLY | O_CREAT | O_TRUNC : O_RDONLY, 0644); }
static i64 os_close(i64 fd) { return close((int)fd); }
static void os_exit(i64 c) { _exit((int)c); }
static void *os_pages(u64 n) {
	void *p = mmap(0, n, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	return p == MAP_FAILED ? 0 : p;
}
static i64 os_random(void *b, u64 n) { return getrandom(b, n, 0); }
static const OS os = {os_write, os_read, os_open, os_close, os_exit, os_pages, os_random};

