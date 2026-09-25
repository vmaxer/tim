#include "harness_os.h"
#include <stdio.h>
#include <stdlib.h>
int main(int argc, char **argv, char **envp) {
	R *r = rt_init(&os, (u64)argc, argv, envp);
	char line[64];
	while (fgets(line, sizeof line, stdin)) {
		u64 v = strtoull(line, 0, 16);
		rt_print(r, 1, &v, 1, 1);
	}
	rt_flush(r);
}
