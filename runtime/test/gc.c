#include "harness_os.h"
#include <stdio.h>

int main(int argc, char **argv, char **envp) {
	R *r = rt_init(&os, (u64)argc, argv, envp);
	u64 *g = rt_globals(r, 2);
	g[0] = rt_map(r, 0, 0);
	u64 keep = list_new(r, 0);
	for (int i = 0; i < 20000; i++) {
		Buf b = buf_new(r, 16);
		puts_(&b, "key");
		put_u64(&b, (u64)i);
		u64 k = buf_str(&b);
		map_set(r, g[0], k, num(i));
		if (i % 7 == 0)
			list_push(r, keep, k);
	}
	u64 total = 0;
	for (int round = 0; round < 200; round++) {
		u64 l = list_new(r, 0);
		for (int i = 0; i < 20000; i++)
			list_push(r, l, rt_str(r, num(i)));
		total += list_len(l);
	}
	int bad = 0;
	for (int i = 0; i < 20000; i++) {
		char s[32];
		int n = snprintf(s, sizeof s, "key%d", i);
		u64 v = rt_index(r, g[0], str_new(r, s, (u64)n));
		if (v != num(i))
			bad++;
	}
	for (u64 i = 0; i < list_len(keep); i++) {
		char s[32];
		int n = snprintf(s, sizeof s, "key%llu", i * 7);
		if (!str_eq(list_items(keep)[i], str_new(r, s, (u64)n)))
			bad++;
	}
	u64 used = 0;
	for (u64 i = 0; i < r->nchunks; i++)
		used += (u64)(r->chunks[i].end - r->chunks[i].start);
	printf("allocated %llu strings, bad=%d, chunks=%llu (%llu MiB)\n", total, bad, r->nchunks, used * 8 >> 20);
	return bad != 0;
}
