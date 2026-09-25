// Windows start code: kernel32 and advapi32 through the import address table.
#include "rt.c"

#if defined(__x86_64__)
#define WINAPI __attribute__((ms_abi))
#else
#define WINAPI
#endif

typedef void *H;
typedef unsigned int DWORD;

// The order of the import address table written by the compiler (core_pe.go).
enum { GetStdHandle, WriteFile, ReadFile, CreateFileA, CloseHandle, ExitProcess, VirtualAlloc, GetCommandLineA, GetEnvironmentStringsA, RtlGenRandom = 10 };

#define CALL(o, i, T) ((T)(o)->imports[i])

static H handle(const OS *o, i64 fd) {
	if (fd >= 0 && fd <= 2)
		return CALL(o, GetStdHandle, H(WINAPI *)(DWORD))((DWORD)(-10 - fd));
	return (H)fd;
}

static i64 os_write(const OS *o, i64 fd, const void *b, u64 n) {
	DWORD done = 0;
	if (!CALL(o, WriteFile, int(WINAPI *)(H, const void *, DWORD, DWORD *, void *))(handle(o, fd), b, (DWORD)n, &done, 0))
		return -1;
	return done;
}

static i64 os_read(const OS *o, i64 fd, void *b, u64 n) {
	DWORD done = 0;
	if (!CALL(o, ReadFile, int(WINAPI *)(H, void *, DWORD, DWORD *, void *))(handle(o, fd), b, (DWORD)n, &done, 0))
		return fd == 0 ? 0 : -1; // a closed pipe on stdin is the end of input
	return done;
}

static i64 os_open(const OS *o, const char *p, i64 w) {
	H h = CALL(o, CreateFileA, H(WINAPI *)(const char *, DWORD, DWORD, void *, DWORD, DWORD, H))(
		p, w ? 0x40000000 : 0x80000000, 1, 0, w ? 2 : 3, 0x80, 0);
	return (i64)h;
}

static i64 os_close(const OS *o, i64 fd) { return CALL(o, CloseHandle, int(WINAPI *)(H))((H)fd) ? 0 : -1; }

static void os_exit(const OS *o, i64 c) {
	for (;;)
		CALL(o, ExitProcess, void(WINAPI *)(DWORD))((DWORD)c);
}

static void *os_pages(const OS *o, u64 n) {
	return CALL(o, VirtualAlloc, void *(WINAPI *)(void *, u64, DWORD, DWORD))(0, n, 0x3000, 4);
}

static i64 os_random(const OS *o, void *b, u64 n) {
	return CALL(o, RtlGenRandom, unsigned char(WINAPI *)(void *, DWORD))(b, (DWORD)n) ? (i64)n : -1;
}

// split_args splits a command line the way the C runtime does, simplified:
// spaces separate arguments, double quotes group them, \" is a quote.
static char **split_args(const OS *o, const char *cmd, u64 *argc) {
	u64 len = strlen_(cmd);
	char **argv = os_pages(o, (len / 2 + 2) * sizeof(char *) + len + 1);
	char *out = (char *)(argv + len / 2 + 2);
	u64 n = 0;
	const char *p = cmd;
	for (;;) {
		while (*p == ' ' || *p == '\t')
			p++;
		if (!*p)
			break;
		argv[n++] = out;
		int quoted = 0;
		for (; *p && (quoted || (*p != ' ' && *p != '\t')); p++) {
			if (*p == '\\' && p[1] == '"')
				*out++ = *++p;
			else if (*p == '"')
				quoted = !quoted;
			else
				*out++ = *p;
		}
		*out++ = 0;
	}
	argv[n] = 0;
	*argc = n;
	return argv;
}

static char **split_env(const OS *o, const char *block) {
	u64 n = 0;
	for (const char *p = block; *p; p += strlen_(p) + 1)
		n++;
	char **envp = os_pages(o, (n + 1) * sizeof(char *));
	n = 0;
	for (const char *p = block; *p; p += strlen_(p) + 1)
		if (*p != '=') // skip the hidden =C:=C:\ entries
			envp[n++] = (char *)p;
	envp[n] = 0;
	return envp;
}

// rt_start is the program entry: imports is the import address table.
void rt_start(void *const *imports, u64 (*main)(R *)) {
	OS os;
	os.write = os_write;
	os.read = os_read;
	os.open = os_open;
	os.close = os_close;
	os.exit = os_exit;
	os.pages = os_pages;
	os.random = os_random;
	os.imports = imports;
	u64 argc;
	char **argv = split_args(&os, CALL(&os, GetCommandLineA, const char *(WINAPI *)(void))(), &argc);
	char **envp = split_env(&os, CALL(&os, GetEnvironmentStringsA, const char *(WINAPI *)(void))());
	R *r = rt_init(&os, argc, argv, envp);
	r->stack_top = (u64 *)__builtin_frame_address(0);
	rt_exit(r, main(r));
}
