#include <stdio.h>
int main(void) {
	long sum = 0;
	for (int i = 0; i < 1000; i++)
		for (int j = 0; j < 1000; j++)
			sum += (long)i * j;
	printf("%ld\n", sum);
	return 0;
}
