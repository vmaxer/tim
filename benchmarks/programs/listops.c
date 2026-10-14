#include <stdio.h>
#include <stdlib.h>
int main(void) {
	int n = 5000;
	long *xs = malloc(n * sizeof(long));
	for (int i = 0; i < n; i++)
		xs[i] = i;
	long s = 0;
	for (int i = 0; i < n; i++)
		s += xs[i];
	printf("%ld\n", s);
	return 0;
}
