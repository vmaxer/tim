#include "harness_os.h"

static R *r;
static void show(u64 v) { rt_print(r, 1, &v, 1, 1); }
static u64 S(const char *s) { return cstr(r, s); }
static u64 N(double d) { return num(d); }

int main(int argc, char **argv, char **envp) {
	r = rt_init(&os, (u64)argc, argv, envp);
	show(rt_binop(r, OP_ADD, rt_num(r, S("0.1")), rt_num(r, S("0.2"))));
	show(rt_binop(r, OP_EQ, rt_binop(r, OP_ADD, rt_num(r, S("0.1")), rt_num(r, S("0.2"))), rt_num(r, S("0.3"))));
	show(rt_binop(r, OP_DIV, N(7), N(2)));
	show(rt_binop(r, OP_DIV, N(1), N(3)));
	show(rt_binop(r, OP_POW, N(2), N(100)));
	show(rt_binop(r, OP_DIV, N(1), N(0)));
	show(rt_binop(r, OP_SUB, S("a"), N(1)));
	show(rt_binop(r, OP_ADD, S("ab"), S("cd")));
	show(rt_binop(r, OP_MUL, S("ab"), N(3)));
	show(rt_sqrt(r, N(2)));
	show(rt_sqrt(r, N(16)));
	show(rt_sin(r, N(1)));
	show(rt_num(r, S("1e-9")));
	show(rt_num(r, S("0x_ff")));
	show(rt_num(r, S("12abc")));
	show(rt_num(r, S("-1_000_000_000_000_000_000_000")));
	u64 xs[] = {N(3), S("x"), N(1)};
	u64 l = rt_list(r, xs, 3);
	show(l);
	show(rt_sort(r, l));
	u64 ys[] = {N(3), N(-1), N(2), N(2.5)};
	u64 l2 = rt_list(r, ys, 4);
	show(rt_sort(r, l2));
	show(rt_index(r, l2, N(-1)));
	show(rt_index(r, l2, N(9)));
	show(rt_slice(r, l2, N(1), 0, 1));
	show(rt_sum(r, l2));
	show(rt_max(r, ys, 4));
	u64 kv[] = {S("a"), N(1), S("b c"), l2, N(3), S("three")};
	u64 m = rt_map(r, kv, 3);
	show(m);
	show(rt_index(r, m, S("b c")));
	show(rt_index(r, m, S("zz")));
	show(rt_remove(r, m, S("a")));
	show(rt_keys(r, m));
	show(rt_in(r, N(3), m));
	show(rt_split(r, S("a,b,,c"), S(",")));
	show(rt_join(r, rt_split(r, S("héllo"), S("")), S("|")));
	show(rt_runes(r, S("hé€😀")));
	show(rt_reverse(r, S("hé€😀")));
	show(rt_upper(r, S("héllo")));
	show(rt_replace(r, S("a-b-c"), S("-"), S("--")));
	show(rt_trim(r, S("  x y \n")));
	show(rt_chr(r, N(0x1F600)));
	show(rt_ord(r, S("€")));
	u64 fa[] = {N(42), rt_binop(r, OP_DIV, N(1), N(3)), S("hi"), N(-7), N(255), N(3.14159)};
	show(rt_sprintf(r, S("[%5d] [%.3f] [%-4s] [%05d] [%x] [%g] %%"), fa, 6));
	show(rt_range(r, N(0), N(5), 0));
	show(rt_range(r, N(1), N(3), 1));
	show(rt_len(r, S("héllo")));
	show(rt_len(r, N(3)));
	show(rt_binop(r, OP_LT, N(1), S("a")));
	show(rt_binop(r, OP_BXOR, N(6), N(3)));
	show(rt_binop(r, OP_MOD, N(-7), N(3)));
	show(rt_error(r, S("custom failure")));
	show(rt_errtext(r, rt_binop(r, OP_DIV, N(1), N(0))));
	show(rt_gcd(r, N(12), N(18)));
	show(rt_enumerate(r, S("ab")));
	show(rt_zip(r, l, l2));
	u64 big = rt_binop(r, OP_POW, N(3), N(200));
	show(rt_binop(r, OP_MOD, big, N(1000007)));
	show(rt_floor(r, rt_binop(r, OP_DIV, N(-7), N(2))));
	show(rt_round(r, N(2.5)));
	show(rt_type(r, m));
	u64 rnd = rt_random(r);
	show(rt_binop(r, OP_LT, rnd, N(1)));
	show(rt_float(r, rt_binop(r, OP_DIV, N(1), N(3))));
	u64 pa[] = {S("x"), N(1), l};
	rt_print(r, 1, pa, 3, 1);
	rt_flush(r);
	return 0;
}
