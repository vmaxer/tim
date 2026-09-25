// Tim runtime: every operation on Tim values that is not a machine
// instruction. Freestanding and position independent; build.sh turns it into
// flat blobs for x86_64, arm64 and riscv64 that the compiler embeds.
//
// Values are one 64-bit word. A double that is not a NaN is a number (exact
// when integral with |v| < 2^53, inexact otherwise). NaNs carry the rest:
//
//   0xFFF9 | ptr  big integer          0xFFFD | ptr  map
//   0xFFFA | ptr  rational             0xFFFE | ptr  function
//   0xFFFB | ptr  string               0xFFFF | ptr  C pointer
//   0xFFFC | ptr  list
//   0x7FF8 | code error with a short code ("dv0", "idx", ...)
//   0x7FFC | ptr  error with a message string
//
// Heap objects start with a header word: kind | words << 8, where words
// counts the whole object including the header.

typedef unsigned long long u64;
typedef long long i64;
typedef unsigned int u32;
typedef unsigned char u8;

#define TAG_BIG 0xFFF9ull
#define TAG_RAT 0xFFFAull
#define TAG_STR 0xFFFBull
#define TAG_LIST 0xFFFCull
#define TAG_MAP 0xFFFDull
#define TAG_FN 0xFFFEull
#define TAG_PTR 0xFFFFull
#define TAG_ERR 0x7FF8ull
#define TAG_ERRMSG 0x7FFCull
#define PTR_MASK 0x0000FFFFFFFFFFFFull
#define SIGN_BIT 0x8000000000000000ull
#define TWO53 9007199254740992.0
#define MAX_POW_BITS (1ull << 22)

#define CODE(a, b, c) ((TAG_ERR << 48) | ((u64)(a) << 24) | ((u64)(b) << 16) | ((u64)(c) << 8))
#define ERR_DV0 CODE('d', 'v', '0')
#define ERR_OVF CODE('o', 'v', 'f')
#define ERR_ARG CODE('a', 'r', 'g')
#define ERR_IDX CODE('i', 'd', 'x')
#define ERR_KEY CODE('k', 'e', 'y')
#define ERR_TYP CODE('t', 'y', 'p')
#define ERR_IO CODE('i', 'o', 0)
#define ERR_EOF CODE('e', 'o', 'f')
#define ERR_PRS CODE('p', 'r', 's')
#define ERR_UDF CODE('u', 'd', 'f')

enum { K_RAW = 1, K_BIG, K_RAT, K_STR, K_LIST, K_MAP, K_FN, K_CELL, K_ARR, K_ERR };

enum {
	OP_ADD = 1, OP_SUB, OP_MUL, OP_DIV, OP_MOD, OP_POW,
	OP_LT, OP_LE, OP_GT, OP_GE, OP_EQ, OP_NE,
	OP_BOR, OP_BAND, OP_BXOR, OP_SHL, OP_SHR, OP_BIT, OP_ROTL, OP_ROTR,
};

// OS services supplied by the program's start code.
typedef struct {
	i64 (*write)(i64 fd, const void *buf, u64 n);
	i64 (*read)(i64 fd, void *buf, u64 n);
	i64 (*open)(const char *path, i64 write);
	i64 (*close)(i64 fd);
	void (*exit)(i64 code);
	void *(*pages)(u64 bytes);
	i64 (*random)(void *buf, u64 n);
} OS;

typedef struct {
	const OS *os;
	u64 *heap, *heap_end;
	u64 argc;
	char **argv, **envp;
	u64 outn, inpos, inlen;
	int ineof;
	u64 rng[4];
	int seeded;
	u8 out[4096];
	u8 in[4096];
} R;

void *memcpy(void *d, const void *s, u64 n) {
	u8 *dp = d;
	const u8 *sp = s;
	while (n--)
		*dp++ = *sp++;
	return d;
}

void *memmove(void *d, const void *s, u64 n) {
	u8 *dp = d;
	const u8 *sp = s;
	if (dp < sp) {
		while (n--)
			*dp++ = *sp++;
	} else {
		while (n--)
			dp[n] = sp[n];
	}
	return d;
}

void *memset(void *d, int c, u64 n) {
	u8 *dp = d;
	while (n--)
		*dp++ = (u8)c;
	return d;
}

static int memcmp_(const void *a, const void *b, u64 n) {
	const u8 *x = a, *y = b;
	for (u64 i = 0; i < n; i++)
		if (x[i] != y[i])
			return x[i] < y[i] ? -1 : 1;
	return 0;
}

static u64 strlen_(const char *s) {
	u64 n = 0;
	while (s[n])
		n++;
	return n;
}

static inline u64 bits_of(double d) { u64 u; __builtin_memcpy(&u, &d, 8); return u; }
static inline double double_of(u64 u) { double d; __builtin_memcpy(&d, &u, 8); return d; }
static inline u64 tag_of(u64 v) { return v >> 48; }
static inline u64 *obj(u64 v) { return (u64 *)(v & PTR_MASK); }
static inline u64 box(u64 tag, const void *p) { return (tag << 48) | ((u64)p & PTR_MASK); }
static inline u64 num(double d) { return bits_of(d); }

// Scalar-only helpers: the blob may be loaded at any 16-byte misalignment, so
// avoid packed constant loads for u64 conversion and negation.
static double u64_to_double(u64 x) {
	if ((i64)x >= 0)
		return (double)(i64)x;
	double d = (double)(i64)((x >> 1) | (x & 1));
	return d + d;
}
static inline double neg_d(double x) { return 0.0 - x; }

// Heap

static void fatal(R *r, const char *msg);

static u64 *alloc_obj(R *r, int kind, u64 words) {
	if (r->heap + words > r->heap_end) {
		u64 bytes = words * 8 > (64ull << 20) ? words * 8 : (64ull << 20);
		u64 *p = r->os->pages(bytes);
		if (!p)
			fatal(r, "out of memory");
		r->heap = p;
		r->heap_end = p + bytes / 8;
	}
	u64 *p = r->heap;
	r->heap += words;
	p[0] = (u64)kind | words << 8;
	return p;
}

// raw returns zeroed scratch memory of n bytes.
static void *raw(R *r, u64 n) {
	u64 *p = alloc_obj(r, K_RAW, 1 + (n + 7) / 8);
	memset(p + 1, 0, (n + 7) / 8 * 8);
	return p + 1;
}

// Numbers: magnitudes are little-endian 64-bit limbs, n == 0 means zero.

typedef struct { u64 *d; u64 n; } Mag;
typedef struct { Mag m; int neg; } Int;
typedef struct { Int num; Mag den; u64 nbuf, dbuf; } Rat;

static u64 *new_limbs(R *r, u64 n) { return raw(r, (n ? n : 1) * 8); }

static void mag_norm(Mag *a) {
	while (a->n && a->d[a->n - 1] == 0)
		a->n--;
}

static int mag_cmp(const Mag *a, const Mag *b) {
	if (a->n != b->n)
		return a->n < b->n ? -1 : 1;
	for (u64 i = a->n; i-- > 0;)
		if (a->d[i] != b->d[i])
			return a->d[i] < b->d[i] ? -1 : 1;
	return 0;
}

static int mag_is_one(const Mag *a) { return a->n == 1 && a->d[0] == 1; }

static Mag mag_add(R *c, const Mag *a, const Mag *b) {
	if (a->n < b->n) {
		const Mag *t = a; a = b; b = t;
	}
	Mag r = {new_limbs(c, a->n + 1), a->n + 1};
	u64 carry = 0;
	for (u64 i = 0; i < a->n; i++) {
		u64 x = a->d[i], y = i < b->n ? b->d[i] : 0;
		u64 s = x + y;
		u64 c1 = s < x;
		u64 s2 = s + carry;
		carry = c1 | (s2 < s);
		r.d[i] = s2;
	}
	r.d[a->n] = carry;
	mag_norm(&r);
	return r;
}

// a - b, requires a >= b
static Mag mag_sub(R *c, const Mag *a, const Mag *b) {
	Mag r = {new_limbs(c, a->n), a->n};
	u64 borrow = 0;
	for (u64 i = 0; i < a->n; i++) {
		u64 x = a->d[i], y = i < b->n ? b->d[i] : 0;
		u64 d = x - y;
		u64 b1 = x < y;
		u64 d2 = d - borrow;
		borrow = b1 | (d < borrow);
		r.d[i] = d2;
	}
	mag_norm(&r);
	return r;
}

static inline void mul64(u64 a, u64 b, u64 *hi, u64 *lo) {
	unsigned __int128 p = (unsigned __int128)a * b;
	*lo = (u64)p;
	*hi = (u64)(p >> 64);
}

static Mag mag_mul(R *c, const Mag *a, const Mag *b) {
	if (!a->n || !b->n)
		return (Mag){0, 0};
	Mag r = {new_limbs(c, a->n + b->n), a->n + b->n};
	for (u64 i = 0; i < a->n; i++) {
		u64 carry = 0;
		for (u64 j = 0; j < b->n; j++) {
			u64 hi, lo;
			mul64(a->d[i], b->d[j], &hi, &lo);
			u64 t = r.d[i + j] + lo;
			hi += t < lo;
			u64 t2 = t + carry;
			hi += t2 < t;
			r.d[i + j] = t2;
			carry = hi;
		}
		r.d[i + b->n] = carry;
	}
	mag_norm(&r);
	return r;
}

// (hi:lo) / d with hi < d; returns quotient, stores remainder.
static inline u64 div128(u64 hi, u64 lo, u64 d, u64 *rem) {
#if defined(__x86_64__)
	u64 q, r;
	__asm__("divq %4" : "=a"(q), "=d"(r) : "a"(lo), "d"(hi), "rm"(d));
	*rem = r;
	return q;
#else
	unsigned __int128 n = ((unsigned __int128)hi << 64) | lo;
	u64 q = 0;
	unsigned __int128 r = 0;
	for (int i = 127; i >= 0; i--) {
		r = (r << 1) | ((n >> i) & 1);
		if (r >= d) {
			r -= d;
			if (i < 64)
				q |= 1ull << i;
		}
	}
	*rem = (u64)r;
	return q;
#endif
}

static Mag mag_divmod1(R *c, const Mag *a, u64 d, u64 *rem) {
	Mag q = {new_limbs(c, a->n), a->n};
	u64 r = 0;
	for (u64 i = a->n; i-- > 0;)
		q.d[i] = div128(r, a->d[i], d, &r);
	mag_norm(&q);
	*rem = r;
	return q;
}

static int clz64(u64 x) {
#if defined(__x86_64__) || defined(__aarch64__)
	return x ? __builtin_clzll(x) : 64;
#else
	int n = 0;
	if (!x)
		return 64;
	while (!(x & SIGN_BIT)) {
		x <<= 1;
		n++;
	}
	return n;
#endif
}

// Knuth algorithm D. q = a / b, r = a % b.
static void mag_divmod(R *c, const Mag *a, const Mag *b, Mag *q, Mag *r) {
	if (mag_cmp(a, b) < 0) {
		*q = (Mag){0, 0};
		*r = *a;
		return;
	}
	if (b->n == 1) {
		u64 rem;
		*q = mag_divmod1(c, a, b->d[0], &rem);
		Mag rm = {new_limbs(c, 1), 1};
		rm.d[0] = rem;
		mag_norm(&rm);
		*r = rm;
		return;
	}
	u64 n = b->n, m = a->n - b->n;
	int s = clz64(b->d[n - 1]);
	u64 *vn = new_limbs(c, n);
	u64 *un = new_limbs(c, a->n + 1);
	for (u64 i = n - 1; i > 0; i--)
		vn[i] = (b->d[i] << s) | (s ? b->d[i - 1] >> (64 - s) : 0);
	vn[0] = b->d[0] << s;
	un[a->n] = s ? a->d[a->n - 1] >> (64 - s) : 0;
	for (u64 i = a->n - 1; i > 0; i--)
		un[i] = (a->d[i] << s) | (s ? a->d[i - 1] >> (64 - s) : 0);
	un[0] = a->d[0] << s;

	Mag qq = {new_limbs(c, m + 1), m + 1};
	for (u64 jj = m + 1; jj-- > 0;) {
		u64 j = jj;
		u64 qhat, rhat;
		if (un[j + n] >= vn[n - 1]) {
			qhat = ~0ull;
			rhat = un[j + n - 1] + vn[n - 1];
			int overflow = rhat < vn[n - 1] || un[j + n] > vn[n - 1];
			if (overflow)
				goto mulsub;
		} else {
			qhat = div128(un[j + n], un[j + n - 1], vn[n - 1], &rhat);
		}
		for (;;) {
			u64 hi, lo;
			mul64(qhat, vn[n - 2], &hi, &lo);
			if (hi > rhat || (hi == rhat && lo > un[j + n - 2])) {
				qhat--;
				u64 old = rhat;
				rhat += vn[n - 1];
				if (rhat < old)
					break;
				continue;
			}
			break;
		}
	mulsub:;
		u64 borrow = 0, carry = 0;
		for (u64 i = 0; i < n; i++) {
			u64 hi, lo;
			mul64(qhat, vn[i], &hi, &lo);
			lo += carry;
			hi += lo < carry;
			carry = hi;
			u64 t = un[i + j] - lo;
			u64 b1 = un[i + j] < lo;
			u64 t2 = t - borrow;
			borrow = b1 | (t < borrow);
			un[i + j] = t2;
		}
		u64 t = un[j + n] - carry;
		u64 b1 = un[j + n] < carry;
		u64 t2 = t - borrow;
		borrow = b1 | (t < borrow);
		un[j + n] = t2;
		if (borrow) {
			qhat--;
			u64 cy = 0;
			for (u64 i = 0; i < n; i++) {
				u64 s1 = un[i + j] + vn[i];
				u64 c1 = s1 < vn[i];
				u64 s2 = s1 + cy;
				cy = c1 | (s2 < s1);
				un[i + j] = s2;
			}
			un[j + n] += cy;
		}
		qq.d[j] = qhat;
	}
	mag_norm(&qq);
	*q = qq;
	Mag rr = {new_limbs(c, n), n};
	for (u64 i = 0; i < n; i++)
		rr.d[i] = (un[i] >> s) | (s && i + 1 < n + 1 ? un[i + 1] << (64 - s) : 0);
	mag_norm(&rr);
	*r = rr;
}

static Mag mag_gcd(R *c, Mag a, Mag b) {
	while (b.n) {
		Mag q, r;
		mag_divmod(c, &a, &b, &q, &r);
		a = b;
		b = r;
	}
	return a;
}

static Mag mag_from_u64(R *c, u64 v) {
	Mag m = {new_limbs(c, 1), 1};
	m.d[0] = v;
	mag_norm(&m);
	return m;
}

static u64 mag_bitlen(const Mag *a) {
	return a->n ? a->n * 64 - clz64(a->d[a->n - 1]) : 0;
}

static Mag mag_shl(R *c, const Mag *a, u64 k) {
	if (!a->n)
		return *a;
	u64 w = k / 64, s = k % 64;
	Mag r = {new_limbs(c, a->n + w + 1), a->n + w + 1};
	for (u64 i = 0; i < a->n; i++) {
		r.d[i + w] |= a->d[i] << s;
		if (s)
			r.d[i + w + 1] |= a->d[i] >> (64 - s);
	}
	mag_norm(&r);
	return r;
}

static Int int_add(R *c, const Int *a, const Int *b) {
	if (a->neg == b->neg)
		return (Int){mag_add(c, &a->m, &b->m), a->neg};
	int k = mag_cmp(&a->m, &b->m);
	if (k == 0)
		return (Int){{0, 0}, 0};
	if (k > 0)
		return (Int){mag_sub(c, &a->m, &b->m), a->neg};
	return (Int){mag_sub(c, &b->m, &a->m), b->neg};
}

static Int int_mul(R *c, const Int *a, const Int *b) {
	Int r = {mag_mul(c, &a->m, &b->m), a->neg != b->neg};
	if (!r.m.n)
		r.neg = 0;
	return r;
}

static Int int_neg(const Int *a) {
	Int r = *a;
	r.neg = r.m.n ? !r.neg : 0;
	return r;
}

static int int_cmp(const Int *a, const Int *b) {
	if (a->neg != b->neg)
		return a->neg ? -1 : 1;
	int k = mag_cmp(&a->m, &b->m);
	return a->neg ? -k : k;
}

// Truncating division.
static void int_divmod(R *c, const Int *a, const Mag *b, Int *q, Int *r) {
	Mag qm, rm;
	mag_divmod(c, &a->m, b, &qm, &rm);
	*q = (Int){qm, qm.n ? a->neg : 0};
	*r = (Int){rm, rm.n ? a->neg : 0};
}

static int is_small(u64 v) {
	double d = double_of(v);
	if (!(d > -TWO53 && d < TWO53))
		return 0;
	return (double)(i64)d == d;
}

static int is_num(u64 v) {
	double d = double_of(v);
	return d == d || tag_of(v) == TAG_BIG || tag_of(v) == TAG_RAT || v == 0xFFF8000000000000ull ||
	       v == 0x7FF8000000000000ull;
}

static int is_exact(u64 v) {
	u64 t = tag_of(v);
	return t == TAG_BIG || t == TAG_RAT || is_small(v);
}

static int is_err(u64 v) { return (tag_of(v) == TAG_ERR && (v & PTR_MASK)) || tag_of(v) == TAG_ERRMSG; }

static const u64 *read_int(const u64 *p, Int *out) {
	u64 hdr = p[0];
	out->m.n = hdr & 0xFFFFFFFFull;
	out->m.d = (u64 *)(p + 1);
	out->neg = (hdr & SIGN_BIT) != 0;
	return p + 1 + out->m.n;
}

static void load(u64 v, Rat *r) {
	r->den = (Mag){&r->dbuf, 1};
	r->dbuf = 1;
	u64 t = tag_of(v);
	if (t == TAG_BIG) {
		read_int(obj(v) + 1, &r->num);
	} else if (t == TAG_RAT) {
		Int den;
		read_int(read_int(obj(v) + 1, &r->num), &den);
		r->den = den.m;
	} else {
		i64 x = (i64)double_of(v);
		r->nbuf = x < 0 ? (u64)-x : (u64)x;
		r->num = (Int){{&r->nbuf, 1}, x < 0};
		mag_norm(&r->num.m);
	}
}

static u64 *store_int(u64 *p, const Int *a) {
	p[0] = a->m.n | (a->neg ? SIGN_BIT : 0);
	memcpy(p + 1, a->m.d, a->m.n * 8);
	return p + 1 + a->m.n;
}

static u64 encode_int(R *c, const Int *a) {
	if (a->m.n == 0)
		return 0;
	if (a->m.n == 1 && a->m.d[0] < (1ull << 53)) {
		double d = (double)(i64)a->m.d[0];
		return bits_of(a->neg ? neg_d(d) : d);
	}
	u64 *p = alloc_obj(c, K_BIG, 2 + a->m.n);
	store_int(p + 1, a);
	return box(TAG_BIG, p);
}

static u64 encode(R *c, Int num, Mag den) {
	if (!num.m.n)
		return 0;
	if (!mag_is_one(&den)) {
		Mag g = mag_gcd(c, num.m, den);
		if (!mag_is_one(&g)) {
			Mag q, r;
			mag_divmod(c, &num.m, &g, &q, &r);
			num.m = q;
			mag_divmod(c, &den, &g, &q, &r);
			den = q;
		}
	}
	if (mag_is_one(&den))
		return encode_int(c, &num);
	u64 *p = alloc_obj(c, K_RAT, 3 + num.m.n + den.n);
	Int d = {den, 0};
	store_int(store_int(p + 1, &num), &d);
	return box(TAG_RAT, p);
}

static u64 from_i64(R *c, i64 x) {
	if (x > -(i64)(1ll << 53) && x < (i64)(1ll << 53))
		return num((double)x);
	Int v = {mag_from_u64(c, x < 0 ? 0 - (u64)x : (u64)x), x < 0};
	return encode_int(c, &v);
}

// Doubles

static double pow2(i64 e) {
	double r = 1.0;
	while (e > 1000) { r *= double_of(0x7E70000000000000ull); e -= 1000; }
	while (e < -1000) { r *= double_of(0x0170000000000000ull); e += 1000; }
	return r * double_of((u64)(e + 1023) << 52);
}

static double mag_to_double(const Mag *a, i64 *exp) {
	*exp = 0;
	if (!a->n)
		return 0.0;
	u64 hi = a->d[a->n - 1];
	int s = clz64(hi);
	u64 top = hi << s;
	if (s && a->n > 1)
		top |= a->d[a->n - 2] >> (64 - s);
	u64 sticky = 0;
	if (a->n > 1 && (a->d[a->n - 2] << s))
		sticky = 1;
	for (u64 i = 0; i + 2 < a->n && !sticky; i++)
		sticky = a->d[i] != 0;
	*exp = (i64)mag_bitlen(a) - 64;
	return u64_to_double(top | sticky);
}

static double to_double(R *c, u64 v) {
	u64 t = tag_of(v);
	if (t != TAG_BIG && t != TAG_RAT)
		return double_of(v);
	Rat r;
	load(v, &r);
	double d;
	if (mag_is_one(&r.den)) {
		i64 e;
		d = mag_to_double(&r.num.m, &e) * pow2(e);
	} else {
		i64 k = 64 + (i64)mag_bitlen(&r.den) - (i64)mag_bitlen(&r.num.m);
		Mag n = r.num.m, dd = r.den;
		if (k > 0)
			n = mag_shl(c, &n, (u64)k);
		else if (k < 0)
			dd = mag_shl(c, &dd, (u64)-k);
		Mag q, rem;
		mag_divmod(c, &n, &dd, &q, &rem);
		if (rem.n && q.n)
			q.d[0] |= 1;
		i64 e;
		d = mag_to_double(&q, &e) * pow2(e - k);
	}
	return r.num.neg ? neg_d(d) : d;
}

static double trunc_d(double x) {
	if (!(x > -TWO53 && x < TWO53))
		return x;
	return (double)(i64)x;
}

static double floor_d(double x) {
	double t = trunc_d(x);
	return t > x ? t - 1.0 : t;
}

// Portable elementary functions, identical on every architecture.

#define PI 3.14159265358979311600e+00
#define PIO2_1 1.57079632673412561417e+00
#define PIO2_2 6.07710050650619224932e-11
#define PIO2_3 2.02226624879595063154e-21
#define LN2_HI 6.93147180369123816490e-01
#define LN2_LO 1.90821492927058770002e-10
#define LN10 2.30258509299404590109e+00
#define INF_BITS 0x7FF0000000000000ull
#define NAN_BITS 0x7FF8000000000000ull

static double fabs_(double x) { return x < 0 ? neg_d(x) : x; }
static double sqrt_(double x) { return __builtin_sqrt(x); }

// log(x) for x > 0: x = m * 2^e with m in [sqrt(1/2), sqrt(2)), log(m) = 2 atanh(s).
static double log_d(double x) {
	if (x != x || x < 0)
		return double_of(NAN_BITS);
	if (x == 0)
		return neg_d(double_of(INF_BITS));
	if (x == double_of(INF_BITS))
		return x;
	u64 b = bits_of(x);
	i64 e = (i64)((b >> 52) & 0x7FF) - 1023;
	if (e == -1023) {
		x *= 18014398509481984.0; // 2^54, subnormals
		b = bits_of(x);
		e = (i64)((b >> 52) & 0x7FF) - 1023 - 54;
	}
	double m = double_of((b & 0x000FFFFFFFFFFFFFull) | 0x3FF0000000000000ull);
	if (m > 1.4142135623730951) {
		m *= 0.5;
		e++;
	}
	double s = (m - 1.0) / (m + 1.0), s2 = s * s, term = s, sum = 0.0;
	for (int k = 1; k < 40; k += 2) {
		sum += term / k;
		term *= s2;
	}
	return 2.0 * sum + (double)e * LN2_LO + (double)e * LN2_HI;
}

// exp(y) = 2^k * exp(r), |r| <= ln2/2.
static double exp_d(double y) {
	if (y != y)
		return y;
	if (y > 709.8)
		return double_of(INF_BITS);
	if (y < -745.2)
		return 0.0;
	double kd = floor_d(y / 0.6931471805599453 + 0.5);
	double r = (y - kd * LN2_HI) - kd * LN2_LO, term = 1.0, sum = 1.0;
	for (int n = 1; n < 25; n++) {
		term *= r / n;
		sum += term;
	}
	return sum * pow2((i64)kd);
}

// Reduce x to r in [-pi/4, pi/4] with x = k pi/2 + r; returns k mod 4.
static int reduce_pio2(double x, double *r) {
	double k = floor_d(x / (PI / 2) + 0.5);
	*r = ((x - k * PIO2_1) - k * PIO2_2) - k * PIO2_3;
	i64 ki = (i64)(k - 4.0 * floor_d(k / 4.0));
	return (int)ki;
}

static double sin_k(double r) {
	double r2 = r * r, term = r, sum = r;
	for (int n = 1; n < 12; n++) {
		term *= neg_d(r2) / ((2 * n) * (2 * n + 1));
		sum += term;
	}
	return sum;
}

static double cos_k(double r) {
	double r2 = r * r, term = 1.0, sum = 1.0;
	for (int n = 1; n < 12; n++) {
		term *= neg_d(r2) / ((2 * n - 1) * (2 * n));
		sum += term;
	}
	return sum;
}

static double sin_d(double x) {
	if (x != x || fabs_(x) == double_of(INF_BITS))
		return double_of(NAN_BITS);
	double r;
	switch (reduce_pio2(x, &r)) {
	case 0: return sin_k(r);
	case 1: return cos_k(r);
	case 2: return neg_d(sin_k(r));
	default: return neg_d(cos_k(r));
	}
}

static double cos_d(double x) {
	if (x != x || fabs_(x) == double_of(INF_BITS))
		return double_of(NAN_BITS);
	double r;
	switch (reduce_pio2(x, &r)) {
	case 0: return cos_k(r);
	case 1: return neg_d(sin_k(r));
	case 2: return neg_d(cos_k(r));
	default: return sin_k(r);
	}
}

static double atan_d(double x) {
	if (x != x)
		return x;
	int neg = x < 0;
	if (neg)
		x = neg_d(x);
	double base = 0;
	int inv = x > 1;
	if (inv)
		x = 1.0 / x;
	if (x > 0.2679491924311227) { // tan(pi/12)
		base = PI / 6;
		x = (x * 1.7320508075688772 - 1.0) / (1.7320508075688772 + x);
	}
	double x2 = x * x, term = x, sum = x;
	for (int n = 1; n < 30; n++) {
		term *= neg_d(x2);
		sum += term / (2 * n + 1);
	}
	double r = base + sum;
	if (inv)
		r = PI / 2 - r;
	return neg ? neg_d(r) : r;
}

static double atan2_d(double y, double x) {
	if (x != x || y != y)
		return double_of(NAN_BITS);
	if (x > 0)
		return atan_d(y / x);
	if (x < 0)
		return y < 0 ? atan_d(y / x) - PI : atan_d(y / x) + PI;
	if (y > 0)
		return PI / 2;
	if (y < 0)
		return neg_d(PI / 2);
	return 0;
}

static double asin_d(double x) {
	if (!(x >= -1 && x <= 1))
		return double_of(NAN_BITS);
	if (x == 1 || x == -1)
		return x * (PI / 2);
	return atan_d(x / sqrt_(1 - x * x));
}

static double pow_d(double x, double y) {
	if (y == 0.0)
		return 1.0;
	if (x == 0.0)
		return y > 0 ? 0.0 : double_of(ERR_DV0);
	if (trunc_d(y) == y && y > -TWO53 && y < TWO53) {
		i64 e = (i64)y;
		u64 n = e < 0 ? (u64)-e : (u64)e;
		double r = 1.0, b = x;
		while (n) {
			if (n & 1)
				r *= b;
			b *= b;
			n >>= 1;
		}
		return e < 0 ? 1.0 / r : r;
	}
	if (x < 0.0)
		return double_of(NAN_BITS);
	return exp_d(y * log_d(x));
}

static double log10_d(double x) {
	double r = log_d(x) / LN10, n = floor_d(r + 0.5);
	if (fabs_(r - n) < 1e-9 && n > -300 && n < 300 && pow_d(10.0, n) == x)
		return n;
	return r;
}

// Arithmetic

static u64 inexact(R *c, u64 op, u64 a, u64 b) {
	double x = to_double(c, a), y = to_double(c, b);
	switch (op) {
	case OP_ADD: return bits_of(x + y);
	case OP_SUB: return bits_of(x - y);
	case OP_MUL: return bits_of(x * y);
	case OP_DIV: return y == 0.0 ? ERR_DV0 : bits_of(x / y);
	case OP_MOD: return y == 0.0 ? ERR_DV0 : bits_of(x - trunc_d(x / y) * y);
	case OP_POW: return bits_of(pow_d(x, y));
	case OP_LT: return num(x < y);
	case OP_LE: return num(x <= y);
	case OP_GT: return num(x > y);
	case OP_GE: return num(x >= y);
	case OP_EQ: return num(x == y);
	case OP_NE: return num(x != y);
	}
	return ERR_ARG;
}

static u64 exact_pow(R *c, Rat *a, Rat *b) {
	if (!mag_is_one(&b->den))
		return bits_of(pow_d(to_double(c, encode(c, a->num, a->den)), to_double(c, encode(c, b->num, b->den))));
	if (!b->num.m.n)
		return num(1.0);
	if (b->num.m.n > 1)
		return ERR_OVF;
	u64 e = b->num.m.d[0];
	Int n = a->num;
	Mag den = a->den;
	if (b->num.neg) {
		if (!n.m.n)
			return ERR_DV0;
		Mag t = n.m;
		n.m = den;
		den = t;
	}
	if (mag_bitlen(&n.m) > 1 && e > MAX_POW_BITS / (mag_bitlen(&n.m) - 1))
		return ERR_OVF;
	if (mag_bitlen(&den) > 1 && e > MAX_POW_BITS / (mag_bitlen(&den) - 1))
		return ERR_OVF;
	Int rn = {mag_from_u64(c, 1), 0};
	Mag rd = mag_from_u64(c, 1);
	Int bn = n;
	Mag bd = den;
	int neg = n.neg && (e & 1);
	bn.neg = 0;
	while (e) {
		if (e & 1) {
			rn = int_mul(c, &rn, &bn);
			rd = mag_mul(c, &rd, &bd);
		}
		e >>= 1;
		if (e) {
			bn = int_mul(c, &bn, &bn);
			bd = mag_mul(c, &bd, &bd);
		}
	}
	rn.neg = neg && rn.m.n;
	return encode(c, rn, rd);
}

// sign of a - b
static int exact_cmp(R *c, Rat *a, Rat *b) {
	Int l = a->num, r = b->num;
	Int bd = {b->den, 0}, ad = {a->den, 0};
	if (!mag_is_one(&b->den))
		l = int_mul(c, &l, &bd);
	if (!mag_is_one(&a->den))
		r = int_mul(c, &r, &ad);
	return int_cmp(&l, &r);
}

static u64 exact(R *c, u64 op, u64 av, u64 bv) {
	Rat a, b;
	load(av, &a);
	load(bv, &b);
	Int ad = {a.den, 0}, bd = {b.den, 0};
	switch (op) {
	case OP_ADD:
	case OP_SUB: {
		Int bn = op == OP_SUB ? int_neg(&b.num) : b.num;
		if (mag_is_one(&a.den) && mag_is_one(&b.den)) {
			Int s = int_add(c, &a.num, &bn);
			return encode_int(c, &s);
		}
		Int l = int_mul(c, &a.num, &bd), r = int_mul(c, &bn, &ad);
		return encode(c, int_add(c, &l, &r), mag_mul(c, &a.den, &b.den));
	}
	case OP_MUL:
		return encode(c, int_mul(c, &a.num, &b.num), mag_mul(c, &a.den, &b.den));
	case OP_DIV: {
		if (!b.num.m.n)
			return ERR_DV0;
		Int n = int_mul(c, &a.num, &bd);
		n.neg = n.m.n ? a.num.neg != b.num.neg : 0;
		return encode(c, n, mag_mul(c, &a.den, &b.num.m));
	}
	case OP_MOD: {
		if (!b.num.m.n)
			return ERR_DV0;
		Int n = int_mul(c, &a.num, &bd);
		Mag d = mag_mul(c, &a.den, &b.num.m);
		n.neg = n.m.n ? a.num.neg != b.num.neg : 0;
		Int q, r;
		int_divmod(c, &n, &d, &q, &r);
		Int qb = int_mul(c, &q, &b.num);
		Int t = int_mul(c, &a.num, &bd);
		Int u = int_mul(c, &qb, &ad);
		Int un = int_neg(&u);
		return encode(c, int_add(c, &t, &un), mag_mul(c, &a.den, &b.den));
	}
	case OP_POW:
		return exact_pow(c, &a, &b);
	case OP_LT: return num(exact_cmp(c, &a, &b) < 0);
	case OP_LE: return num(exact_cmp(c, &a, &b) <= 0);
	case OP_GT: return num(exact_cmp(c, &a, &b) > 0);
	case OP_GE: return num(exact_cmp(c, &a, &b) >= 0);
	case OP_EQ: return num(exact_cmp(c, &a, &b) == 0);
	case OP_NE: return num(exact_cmp(c, &a, &b) != 0);
	}
	return ERR_ARG;
}

enum { RND_FLOOR = 1, RND_CEIL, RND_ROUND, RND_TRUNC, RND_ABS };

static u64 rounding(R *c, int op, u64 v) {
	if (!is_exact(v)) {
		double x = double_of(v);
		if (x != x)
			return v;
		switch (op) {
		case RND_FLOOR: return bits_of(floor_d(x));
		case RND_CEIL: return bits_of(neg_d(floor_d(neg_d(x))));
		case RND_ROUND: return bits_of(x < 0 ? neg_d(floor_d(0.5 - x)) : floor_d(x + 0.5));
		case RND_TRUNC: return bits_of(trunc_d(x));
		case RND_ABS: return bits_of(x < 0 ? neg_d(x) : x);
		}
		return ERR_ARG;
	}
	Rat a;
	load(v, &a);
	if (op == RND_ABS) {
		a.num.neg = 0;
		return encode(c, a.num, a.den);
	}
	if (mag_is_one(&a.den))
		return v;
	Int q, r;
	Int n = a.num;
	if (op == RND_ROUND) {
		Mag twice = mag_shl(c, &n.m, 1);
		Mag d2 = mag_shl(c, &a.den, 1);
		Int t = {mag_add(c, &twice, &a.den), 0};
		int_divmod(c, &t, &d2, &q, &r);
		q.neg = q.m.n ? n.neg : 0;
		return encode_int(c, &q);
	}
	int_divmod(c, &n, &a.den, &q, &r);
	Int one = {mag_from_u64(c, 1), 0};
	if (op == RND_FLOOR && n.neg) {
		Int m1 = int_neg(&one);
		q = int_add(c, &q, &m1);
	} else if (op == RND_CEIL && !n.neg) {
		q = int_add(c, &q, &one);
	}
	return encode_int(c, &q);
}

// to_i64 truncates a number toward zero into an int64 (wrapping big integers).
static i64 to_i64(R *c, u64 v) {
	if (!is_exact(v)) {
		double x = double_of(v);
		if (x != x)
			return 0;
		if (x >= 9223372036854775808.0)
			return x < 18446744073709551616.0 ? (i64)(u64)x : -1;
		if (x < -9223372036854775808.0)
			return (i64)SIGN_BIT;
		return (i64)x;
	}
	Rat a;
	load(v, &a);
	Int q = a.num, rr;
	if (!mag_is_one(&a.den))
		int_divmod(c, &a.num, &a.den, &q, &rr);
	u64 m = q.m.n ? q.m.d[0] : 0;
	return (i64)(q.neg ? 0 - m : m);
}

static u64 bitwise(R *c, u64 op, u64 av, u64 bv) {
	u64 a = (u64)to_i64(c, av), b = (u64)to_i64(c, bv), r = 0;
	switch (op) {
	case OP_BOR: r = a | b; break;
	case OP_BAND: r = a & b; break;
	case OP_BXOR: r = a ^ b; break;
	case OP_SHL: r = b >= 64 ? 0 : a << b; break;
	case OP_SHR: r = b >= 64 ? 0 : a >> b; break;
	case OP_BIT: r = b >= 64 ? 0 : (a >> b) & 1; break;
	case OP_ROTL: b &= 63; r = b ? (a << b) | (a >> (64 - b)) : a; break;
	case OP_ROTR: b &= 63; r = b ? (a >> b) | (a << (64 - b)) : a; break;
	}
	return from_i64(c, (i64)r);
}

// Byte buffers

typedef struct { R *r; u8 *p; u64 n, cap; } Buf;

static Buf buf_new(R *r, u64 cap) { return (Buf){r, raw(r, cap), 0, cap}; }

static void buf_grow(Buf *b, u64 extra) {
	if (b->n + extra <= b->cap)
		return;
	u64 cap = b->cap * 2 + extra;
	u8 *p = raw(b->r, cap);
	memcpy(p, b->p, b->n);
	b->p = p;
	b->cap = cap;
}

static void put(Buf *b, u8 ch) {
	buf_grow(b, 1);
	b->p[b->n++] = ch;
}

static void putn(Buf *b, const void *s, u64 n) {
	buf_grow(b, n);
	memcpy(b->p + b->n, s, n);
	b->n += n;
}

static void puts_(Buf *b, const char *s) { putn(b, s, strlen_(s)); }

static void put_u64(Buf *b, u64 v) {
	char tmp[24];
	int n = 0;
	do {
		tmp[n++] = (char)('0' + v % 10);
		v /= 10;
	} while (v);
	while (n)
		put(b, (u8)tmp[--n]);
}

static void put_hex(Buf *b, u64 v, int upper) {
	const char *d = upper ? "0123456789ABCDEF" : "0123456789abcdef";
	char tmp[16];
	int n = 0;
	do {
		tmp[n++] = d[v & 15];
		v >>= 4;
	} while (v);
	while (n)
		put(b, (u8)tmp[--n]);
}

static void put_mag(R *c, Buf *b, const Mag *a) {
	if (!a->n) {
		put(b, '0');
		return;
	}
	u64 *chunks = new_limbs(c, a->n * 20 + 1);
	u64 k = 0;
	Mag m = *a;
	while (m.n) {
		u64 rem;
		m = mag_divmod1(c, &m, 10000000000000000000ull, &rem);
		chunks[k++] = rem;
	}
	put_u64(b, chunks[--k]);
	while (k) {
		u64 v = chunks[--k];
		char tmp[19];
		for (int i = 18; i >= 0; i--) {
			tmp[i] = (char)('0' + v % 10);
			v /= 10;
		}
		putn(b, tmp, 19);
	}
}

// shortest_digits writes the fewest decimal digits that read back as x > 0
// (Burger and Dybvig's free-format algorithm) and returns the decimal
// exponent k with x = 0.digits * 10^k.
static int shortest_digits(R *c, double x, char *dig, int *nd) {
	u64 bits = bits_of(x), f = bits & ((1ull << 52) - 1);
	int be = (int)(bits >> 52 & 0x7FF), e;
	if (be) {
		f |= 1ull << 52;
		e = be - 1075;
	} else
		e = -1074;
	int even = !(f & 1), edge = be > 1 && f == 1ull << 52;
	Mag r, s, mp, mm, ten = mag_from_u64(c, 10), one = mag_from_u64(c, 1);
	Mag fm = mag_from_u64(c, f);
	if (e >= 0) {
		Mag be2 = mag_shl(c, &one, (u64)e);
		r = mag_shl(c, &fm, (u64)e + 1 + edge);
		s = mag_from_u64(c, edge ? 4 : 2);
		mp = edge ? mag_shl(c, &be2, 1) : be2;
		mm = be2;
	} else {
		r = mag_shl(c, &fm, 1 + edge);
		s = mag_shl(c, &one, (u64)(1 - e + edge));
		mp = mag_from_u64(c, edge ? 2 : 1);
		mm = one;
	}
	int k = (int)(log_d(x) * 0.43429448190325176 - 1e-10);
	if (log_d(x) > 0)
		k++;
	for (int i = 0; i < (k < 0 ? -k : k); i++) {
		if (k >= 0)
			s = mag_mul(c, &s, &ten);
		else {
			r = mag_mul(c, &r, &ten);
			mp = mag_mul(c, &mp, &ten);
			mm = mag_mul(c, &mm, &ten);
		}
	}
	for (;;) {
		Mag hi = mag_add(c, &r, &mp);
		int t = mag_cmp(&hi, &s);
		if (even ? t >= 0 : t > 0) {
			s = mag_mul(c, &s, &ten);
			k++;
			continue;
		}
		Mag hi10 = mag_mul(c, &hi, &ten);
		t = mag_cmp(&hi10, &s);
		if (even ? t < 0 : t <= 0) {
			r = mag_mul(c, &r, &ten);
			mp = mag_mul(c, &mp, &ten);
			mm = mag_mul(c, &mm, &ten);
			k--;
			continue;
		}
		break;
	}
	int n = 0;
	for (;;) {
		Mag q, rem, r10 = mag_mul(c, &r, &ten);
		mag_divmod(c, &r10, &s, &q, &rem);
		r = rem;
		mp = mag_mul(c, &mp, &ten);
		mm = mag_mul(c, &mm, &ten);
		int d = q.n ? (int)q.d[0] : 0;
		int t1 = mag_cmp(&r, &mm), t2 = 0;
		Mag hi = mag_add(c, &r, &mp);
		t2 = mag_cmp(&hi, &s);
		int low = even ? t1 <= 0 : t1 < 0, high = even ? t2 >= 0 : t2 > 0;
		if (low && high) {
			Mag r2 = mag_shl(c, &r, 1);
			if (mag_cmp(&r2, &s) >= 0)
				d++;
		} else if (high)
			d++;
		dig[n++] = (char)('0' + d);
		if (low || high)
			break;
	}
	*nd = n;
	return k;
}

// put_double writes x as the shortest decimal that reads back as x.
static void put_double(Buf *b, double x) {
	if (x != x) {
		puts_(b, "nan");
		return;
	}
	if (bits_of(x) & SIGN_BIT) {
		if (x == 0) {
			put(b, '0');
			return;
		}
		put(b, '-');
		x = neg_d(x);
	}
	if (x == double_of(INF_BITS)) {
		puts_(b, "inf");
		return;
	}
	if (x < TWO53 && (double)(u64)x == x) {
		put_u64(b, (u64)x);
		return;
	}
	char dig[20];
	int n, k = shortest_digits(b->r, x, dig, &n);
	if (k - 1 < -4 || k - 1 >= 21) {
		put(b, (u8)dig[0]);
		if (n > 1) {
			put(b, '.');
			putn(b, dig + 1, (u64)n - 1);
		}
		int e = k - 1;
		put(b, 'e');
		put(b, e < 0 ? '-' : '+');
		if (e < 0)
			e = -e;
		if (e < 10)
			put(b, '0');
		put_u64(b, (u64)e);
	} else if (k <= 0) {
		puts_(b, "0.");
		for (int i = 0; i < -k; i++)
			put(b, '0');
		putn(b, dig, (u64)n);
	} else if (k >= n) {
		putn(b, dig, (u64)n);
		for (int i = n; i < k; i++)
			put(b, '0');
	} else {
		putn(b, dig, (u64)k);
		put(b, '.');
		putn(b, dig + k, (u64)(n - k));
	}
}

// Exact decimal if the denominator is 2^i 5^j, otherwise num/den.
static void put_rat(R *c, Buf *b, Rat *r) {
	if (r->num.neg)
		put(b, '-');
	Mag d = r->den;
	u64 twos = 0, fives = 0;
	for (;;) {
		u64 rem;
		Mag q = mag_divmod1(c, &d, 2, &rem);
		if (rem)
			break;
		d = q;
		twos++;
	}
	for (;;) {
		u64 rem;
		Mag q = mag_divmod1(c, &d, 5, &rem);
		if (rem)
			break;
		d = q;
		fives++;
	}
	if (!mag_is_one(&d)) {
		put_mag(c, b, &r->num.m);
		put(b, '/');
		put_mag(c, b, &r->den);
		return;
	}
	u64 k = twos > fives ? twos : fives;
	Mag scale = mag_from_u64(c, 1);
	Mag ten = mag_from_u64(c, 10);
	for (u64 i = 0; i < k; i++)
		scale = mag_mul(c, &scale, &ten);
	Mag n = mag_mul(c, &r->num.m, &scale);
	Mag q, rem;
	mag_divmod(c, &n, &r->den, &q, &rem);
	Mag ip, fp;
	mag_divmod(c, &q, &scale, &ip, &fp);
	put_mag(c, b, &ip);
	put(b, '.');
	Buf fb = buf_new(c, k + 32);
	put_mag(c, &fb, &fp);
	for (u64 i = fb.n; i < k; i++)
		put(b, '0');
	while (fb.n && fb.p[fb.n - 1] == '0')
		fb.n--;
	putn(b, fb.p, fb.n);
}

static void put_num(R *c, Buf *b, u64 v) {
	if (tag_of(v) != TAG_BIG && tag_of(v) != TAG_RAT) {
		put_double(b, double_of(v));
		return;
	}
	Rat r;
	load(v, &r);
	if (mag_is_one(&r.den)) {
		if (r.num.neg)
			put(b, '-');
		put_mag(c, b, &r.num.m);
	} else
		put_rat(c, b, &r);
}

// Strings: [header][length][bytes][0]

static u64 str_len(u64 v) { return obj(v)[1]; }
static u8 *str_data(u64 v) { return (u8 *)(obj(v) + 2); }

static u64 str_new(R *r, const void *s, u64 n) {
	u64 *p = alloc_obj(r, K_STR, 2 + (n + 8) / 8);
	p[1] = n;
	u8 *d = (u8 *)(p + 2);
	memcpy(d, s, n);
	d[n] = 0;
	return box(TAG_STR, p);
}

static u64 cstr(R *r, const char *s) { return str_new(r, s, strlen_(s)); }
static u64 buf_str(Buf *b) { return str_new(b->r, b->p, b->n); }

static int str_eq(u64 a, u64 b) {
	return str_len(a) == str_len(b) && !memcmp_(str_data(a), str_data(b), str_len(a));
}

static int str_cmp(u64 a, u64 b) {
	u64 n = str_len(a) < str_len(b) ? str_len(a) : str_len(b);
	int k = memcmp_(str_data(a), str_data(b), n);
	if (k)
		return k;
	return str_len(a) < str_len(b) ? -1 : str_len(a) > str_len(b);
}

// Errors

// Code and message pairs, NUL separated; a table of pointers would need relocations.
static const char errmsgs[] =
	"dv0\0division by zero\0idx\0index out of range\0key\0key not found\0"
	"typ\0wrong type\0arg\0invalid argument\0ovf\0number too large\0"
	"io\0input/output error\0eof\0end of input\0prs\0not a number\0"
	"udf\0undefined\0nil\0null pointer\0mem\0out of memory\0err\0error\0";

static u64 err_new(R *r, const u8 *s, u64 n) {
	int ascii = n > 0 && n <= 4;
	for (u64 i = 0; i < n && ascii; i++)
		ascii = s[i] > ' ' && s[i] < 127;
	if (ascii) {
		u64 code = 0;
		for (u64 i = 0; i < 4; i++)
			code = code << 8 | (i < n ? s[i] : 0);
		return (TAG_ERR << 48) | code;
	}
	u64 m = str_new(r, s, n);
	return box(TAG_ERRMSG, obj(m));
}

static u64 errorf(R *r, const char *msg) { return err_new(r, (const u8 *)msg, strlen_(msg)); }

// err_text is the error's code or message as it was created.
static u64 err_text(R *r, u64 v) {
	if (tag_of(v) == TAG_ERRMSG)
		return box(TAG_STR, obj(v));
	char s[5];
	u64 n = 0;
	for (int i = 3; i >= 0; i--) {
		char ch = (char)((v >> (8 * i)) & 0xFF);
		if (ch)
			s[n++] = ch;
	}
	return str_new(r, s, n);
}

static void put_err(R *r, Buf *b, u64 v) {
	u64 t = err_text(r, v);
	puts_(b, "error: ");
	for (const char *p = errmsgs; p < errmsgs + sizeof errmsgs - 1;) {
		const char *msg = p + strlen_(p) + 1;
		if (str_len(t) == strlen_(p) && !memcmp_(str_data(t), p, str_len(t))) {
			puts_(b, msg);
			return;
		}
		p = msg + strlen_(msg) + 1;
	}
	putn(b, str_data(t), str_len(t));
}

// Lists: [header][length][capacity][items array], items array: [header][values]

#define TAG_CELL 0xFFF8ull

static u64 list_len(u64 v) { return obj(v)[1]; }
static u64 *list_items(u64 v) { return (u64 *)obj(v)[3] + 1; }

static u64 list_new(R *r, u64 cap) {
	u64 *p = alloc_obj(r, K_LIST, 4);
	u64 *a = alloc_obj(r, K_ARR, 1 + (cap ? cap : 1));
	p[1] = 0;
	p[2] = cap ? cap : 1;
	p[3] = (u64)a;
	return box(TAG_LIST, p);
}

static void list_reserve(R *r, u64 v, u64 need) {
	u64 *p = obj(v);
	if (need <= p[2])
		return;
	u64 cap = p[2] * 2 + 4;
	if (cap < need)
		cap = need;
	u64 *a = alloc_obj(r, K_ARR, 1 + cap);
	memcpy(a + 1, list_items(v), p[1] * 8);
	p[2] = cap;
	p[3] = (u64)a;
}

static void list_push(R *r, u64 v, u64 x) {
	list_reserve(r, v, list_len(v) + 1);
	list_items(v)[obj(v)[1]++] = x;
}

static u64 list_of(R *r, const u64 *xs, u64 n) {
	u64 l = list_new(r, n);
	memcpy(list_items(l), xs, n * 8);
	obj(l)[1] = n;
	return l;
}

// Maps: [header][count][used][capacity][entries array][index][mask]; entries
// hold key/value pairs in insertion order, the index maps hashes to entries.

#define TOMB 0x7FF80000000000FFull

static u64 hash_bytes(const u8 *s, u64 n) {
	u64 h = 1469598103934665603ull;
	for (u64 i = 0; i < n; i++)
		h = (h ^ s[i]) * 1099511628211ull;
	return h;
}

static u64 hash_val(u64 v) {
	u64 t = tag_of(v);
	if (t == TAG_STR)
		return hash_bytes(str_data(v), str_len(v));
	if (t == TAG_BIG || t == TAG_RAT) {
		u64 *p = obj(v);
		return hash_bytes((u8 *)(p + 1), ((p[0] >> 8) - 1) * 8);
	}
	if (v == 0x8000000000000000ull)
		v = 0; // -0 == 0
	v ^= v >> 33;
	v *= 0xff51afd7ed558ccdull;
	return v ^ (v >> 33);
}

static int val_eq(R *r, u64 a, u64 b);

static u64 map_new(R *r, u64 cap) {
	if (cap < 4)
		cap = 4;
	u64 slots = 8;
	while (slots < cap * 2)
		slots *= 2;
	u64 *p = alloc_obj(r, K_MAP, 7);
	p[1] = 0;
	p[2] = 0;
	p[3] = cap;
	p[4] = (u64)alloc_obj(r, K_ARR, 1 + 2 * cap);
	p[5] = (u64)raw(r, slots * 4);
	p[6] = slots - 1;
	return box(TAG_MAP, p);
}

static u64 *map_entries(u64 m) { return (u64 *)obj(m)[4] + 1; }
static u64 map_count(u64 m) { return obj(m)[1]; }

// map_find returns the entry index of key k, or -1.
static i64 map_find(R *r, u64 m, u64 k) {
	u64 *p = obj(m);
	u32 *idx = (u32 *)p[5];
	u64 *e = map_entries(m);
	for (u64 i = hash_val(k) & p[6];; i = (i + 1) & p[6]) {
		u32 s = idx[i];
		if (!s)
			return -1;
		if (e[2 * (s - 1)] != TOMB && val_eq(r, e[2 * (s - 1)], k))
			return s - 1;
	}
}

static void map_set(R *r, u64 m, u64 k, u64 v);

static void map_grow(R *r, u64 m) {
	u64 *p = obj(m);
	u64 old = p[2];
	u64 *oe = map_entries(m);
	u64 nm = map_new(r, p[1] * 2 + 4);
	for (u64 i = 0; i < old; i++)
		if (oe[2 * i] != TOMB)
			map_set(r, nm, oe[2 * i], oe[2 * i + 1]);
	memcpy(p + 1, obj(nm) + 1, 6 * 8);
}

static void map_set(R *r, u64 m, u64 k, u64 v) {
	i64 at = map_find(r, m, k);
	if (at >= 0) {
		map_entries(m)[2 * at + 1] = v;
		return;
	}
	u64 *p = obj(m);
	if (p[2] == p[3]) {
		map_grow(r, m);
		p = obj(m);
	}
	u64 e = p[2]++;
	map_entries(m)[2 * e] = k;
	map_entries(m)[2 * e + 1] = v;
	p[1]++;
	u32 *idx = (u32 *)p[5];
	u64 i = hash_val(k) & p[6];
	while (idx[i])
		i = (i + 1) & p[6];
	idx[i] = (u32)(e + 1);
}

// Functions: [header][code][arity | variadic << 32][captures count][captures]
// Cells hold variables that closures capture and update: [header][value]

static u64 cell_new(R *r, u64 v) {
	u64 *p = alloc_obj(r, K_CELL, 2);
	p[1] = v;
	return box(TAG_CELL, p);
}

// Equality and order

static int val_eq(R *r, u64 a, u64 b) {
	if (a == b)
		return !(double_of(a) != double_of(a) && is_num(a) && !is_exact(a));
	u64 ta = tag_of(a), tb = tag_of(b);
	if (is_num(a) && is_num(b)) {
		if (is_exact(a) && is_exact(b))
			return double_of(exact(r, OP_EQ, a, b)) == 1.0;
		return to_double(r, a) == to_double(r, b);
	}
	if (ta != tb)
		return 0;
	if (ta == TAG_STR)
		return str_eq(a, b);
	if (ta == TAG_LIST) {
		if (list_len(a) != list_len(b))
			return 0;
		for (u64 i = 0; i < list_len(a); i++)
			if (!val_eq(r, list_items(a)[i], list_items(b)[i]))
				return 0;
		return 1;
	}
	if (ta == TAG_ERRMSG)
		return str_eq(box(TAG_STR, obj(a)), box(TAG_STR, obj(b)));
	return 0;
}

// val_cmp orders numbers, strings and lists; *ok is cleared for other types.
static int val_cmp(R *r, u64 a, u64 b, int *ok) {
	if (is_num(a) && is_num(b)) {
		if (is_exact(a) && is_exact(b)) {
			Rat x, y;
			load(a, &x);
			load(b, &y);
			return exact_cmp(r, &x, &y);
		}
		double x = to_double(r, a), y = to_double(r, b);
		return x < y ? -1 : x > y;
	}
	u64 ta = tag_of(a);
	if (ta == tag_of(b) && ta == TAG_STR)
		return str_cmp(a, b);
	if (ta == tag_of(b) && ta == TAG_LIST) {
		u64 n = list_len(a) < list_len(b) ? list_len(a) : list_len(b);
		for (u64 i = 0; i < n; i++) {
			int k = val_cmp(r, list_items(a)[i], list_items(b)[i], ok);
			if (k || !*ok)
				return k;
		}
		return list_len(a) < list_len(b) ? -1 : list_len(a) > list_len(b);
	}
	*ok = 0;
	return 0;
}

static const char *type_name(u64 v) {
	if (is_err(v))
		return "error";
	switch (tag_of(v)) {
	case TAG_STR: return "str";
	case TAG_LIST: return "list";
	case TAG_MAP: return "map";
	case TAG_FN: return "fn";
	case TAG_PTR: return "ptr";
	}
	return "num";
}

// Formatting values

static int ident_like(u64 s) {
	u8 *d = str_data(s);
	if (!str_len(s) || (d[0] >= '0' && d[0] <= '9'))
		return 0;
	for (u64 i = 0; i < str_len(s); i++)
		if (!(d[i] == '_' || d[i] >= 0x80 || ((d[i] | 32) >= 'a' && (d[i] | 32) <= 'z') || (d[i] >= '0' && d[i] <= '9')))
			return 0;
	return 1;
}

static void put_quoted(Buf *b, u64 s) {
	put(b, '"');
	for (u64 i = 0; i < str_len(s); i++) {
		u8 ch = str_data(s)[i];
		switch (ch) {
		case '"': puts_(b, "\\\""); break;
		case '\\': puts_(b, "\\\\"); break;
		case '\n': puts_(b, "\\n"); break;
		case '\t': puts_(b, "\\t"); break;
		case '\r': puts_(b, "\\r"); break;
		default:
			if (ch < ' ' || ch == 127) {
				puts_(b, "\\x");
				put(b, (u8)"0123456789abcdef"[ch >> 4]);
				put(b, (u8)"0123456789abcdef"[ch & 15]);
			} else
				put(b, ch);
		}
	}
	put(b, '"');
}

static void put_val(R *r, Buf *b, u64 v, int repr, int depth) {
	if (depth > 64) {
		puts_(b, "...");
		return;
	}
	if (is_err(v)) {
		put_err(r, b, v);
		return;
	}
	switch (tag_of(v)) {
	case TAG_STR:
		if (repr)
			put_quoted(b, v);
		else
			putn(b, str_data(v), str_len(v));
		return;
	case TAG_LIST:
		put(b, '[');
		for (u64 i = 0; i < list_len(v); i++) {
			if (i)
				puts_(b, ", ");
			put_val(r, b, list_items(v)[i], 1, depth + 1);
		}
		put(b, ']');
		return;
	case TAG_MAP: {
		put(b, '{');
		u64 *e = map_entries(v), first = 1;
		for (u64 i = 0; i < obj(v)[2]; i++) {
			if (e[2 * i] == TOMB)
				continue;
			if (!first)
				puts_(b, ", ");
			first = 0;
			if (tag_of(e[2 * i]) == TAG_STR && ident_like(e[2 * i]))
				putn(b, str_data(e[2 * i]), str_len(e[2 * i]));
			else
				put_val(r, b, e[2 * i], 1, depth + 1);
			puts_(b, ": ");
			put_val(r, b, e[2 * i + 1], 1, depth + 1);
		}
		put(b, '}');
		return;
	}
	case TAG_FN:
		puts_(b, "<fn>");
		return;
	case TAG_PTR:
		puts_(b, "ptr(0x");
		put_hex(b, v & PTR_MASK, 0);
		put(b, ')');
		return;
	}
	put_num(r, b, v);
}

static u64 to_str(R *r, u64 v) {
	if (tag_of(v) == TAG_STR)
		return v;
	Buf b = buf_new(r, 32);
	put_val(r, &b, v, 0, 0);
	return buf_str(&b);
}

static u64 utf8_encode(u8 *out, u64 cp);

// Output

static void out_flush(R *r) {
	u64 off = 0;
	while (off < r->outn) {
		i64 n = r->os->write(1, r->out + off, r->outn - off);
		if (n <= 0)
			break;
		off += (u64)n;
	}
	r->outn = 0;
}

static void out_write(R *r, u64 fd, const u8 *s, u64 n) {
	if (fd != 1) {
		out_flush(r);
		while (n) {
			i64 k = r->os->write((i64)fd, s, n);
			if (k <= 0)
				return;
			s += k;
			n -= (u64)k;
		}
		return;
	}
	while (n) {
		if (r->outn == sizeof r->out)
			out_flush(r);
		u64 k = sizeof r->out - r->outn;
		if (k > n)
			k = n;
		memcpy(r->out + r->outn, s, k);
		r->outn += k;
		s += k;
		n -= k;
	}
}

static void fatal(R *r, const char *msg) {
	out_flush(r);
	r->os->write(2, "tim: ", 5);
	r->os->write(2, msg, strlen_(msg));
	r->os->write(2, "\n", 1);
	r->os->exit(70);
}

// The API used by generated code. Every function takes the runtime first.

R *rt_init(const OS *os, u64 argc, char **argv, char **envp) {
	R *r = os->pages((sizeof(R) + 4095) & ~4095ull);
	if (!r) {
		os->write(2, "tim: out of memory\n", 19);
		os->exit(70);
	}
	memset(r, 0, sizeof *r);
	r->os = os;
	r->argc = argc;
	r->argv = argv;
	r->envp = envp;
	return r;
}

void rt_flush(R *r) { out_flush(r); }

void rt_exit(R *r, u64 code) {
	out_flush(r);
	i64 c = is_num(code) ? to_i64(r, code) : (is_err(code) ? 1 : 0);
	r->os->exit(c & 0xFF);
}

static void put_a(Buf *b, u64 v) {
	const char *t = type_name(v);
	puts_(b, t[0] == 'e' ? "an " : "a ");
	puts_(b, t);
}

static u64 type_error(R *r, const char *what, u64 a, u64 b) {
	Buf e = buf_new(r, 64);
	puts_(&e, "cannot ");
	puts_(&e, what);
	put(&e, ' ');
	put_a(&e, a);
	if (b) {
		puts_(&e, " and ");
		put_a(&e, b);
	}
	return err_new(r, e.p, e.n);
}

static const char *op_verb(u64 op) {
	switch (op) {
	case OP_ADD: return "add";
	case OP_SUB: return "subtract";
	case OP_MUL: return "multiply";
	case OP_DIV: return "divide";
	case OP_MOD: return "take the remainder of";
	case OP_POW: return "raise";
	case OP_LT: case OP_LE: case OP_GT: case OP_GE: return "compare";
	}
	return "combine bits of";
}

static u64 list_concat(R *r, u64 a, u64 b) {
	u64 l = list_new(r, list_len(a) + list_len(b));
	memcpy(list_items(l), list_items(a), list_len(a) * 8);
	memcpy(list_items(l) + list_len(a), list_items(b), list_len(b) * 8);
	obj(l)[1] = list_len(a) + list_len(b);
	return l;
}

u64 rt_binop(R *r, u64 op, u64 a, u64 b) {
	if (op == OP_EQ || op == OP_NE)
		return num(val_eq(r, a, b) == (op == OP_EQ));
	if (is_err(a))
		return a;
	if (is_err(b))
		return b;
	u64 ta = tag_of(a), tb = tag_of(b);
	if (is_num(a) && is_num(b)) {
		if (op >= OP_BOR)
			return bitwise(r, op, a, b);
		u64 na = double_of(a) != double_of(a) && is_num(a) && !is_exact(a);
		u64 nb = double_of(b) != double_of(b) && is_num(b) && !is_exact(b);
		if (na || nb)
			return op >= OP_LT ? num(0) : (na ? a : b);
		if (is_exact(a) && is_exact(b))
			return exact(r, op, a, b);
		return inexact(r, op, a, b);
	}
	if (op >= OP_LT && op <= OP_GE) {
		int ok = 1, k = val_cmp(r, a, b, &ok);
		if (!ok)
			return type_error(r, "compare", a, b);
		switch (op) {
		case OP_LT: return num(k < 0);
		case OP_LE: return num(k <= 0);
		case OP_GT: return num(k > 0);
		default: return num(k >= 0);
		}
	}
	if (op == OP_ADD && ta == TAG_STR && tb == TAG_STR) {
		Buf s = buf_new(r, str_len(a) + str_len(b) + 1);
		putn(&s, str_data(a), str_len(a));
		putn(&s, str_data(b), str_len(b));
		return buf_str(&s);
	}
	if (op == OP_ADD && ta == TAG_LIST && tb == TAG_LIST)
		return list_concat(r, a, b);
	if (op == OP_MUL && (ta == TAG_LIST || ta == TAG_STR) && is_num(b)) {
		i64 n = to_i64(r, b);
		if (n < 0)
			n = 0;
		if (ta == TAG_STR) {
			Buf s = buf_new(r, str_len(a) * (u64)n + 1);
			for (i64 i = 0; i < n; i++)
				putn(&s, str_data(a), str_len(a));
			return buf_str(&s);
		}
		u64 l = list_new(r, list_len(a) * (u64)n);
		for (i64 i = 0; i < n; i++)
			memcpy(list_items(l) + (u64)i * list_len(a), list_items(a), list_len(a) * 8);
		obj(l)[1] = list_len(a) * (u64)n;
		return l;
	}
	return type_error(r, op_verb(op), a, b);
}

u64 rt_neg(R *r, u64 a) {
	if (is_err(a))
		return a;
	if (!is_num(a))
		return type_error(r, "negate", a, 0);
	return rt_binop(r, OP_SUB, 0, a);
}

u64 rt_truthy(R *r, u64 v) {
	(void)r;
	if (is_err(v))
		return 0;
	switch (tag_of(v)) {
	case TAG_STR: return str_len(v) != 0;
	case TAG_LIST: return list_len(v) != 0;
	case TAG_MAP: return map_count(v) != 0;
	case TAG_FN: return 1;
	case TAG_PTR: return (v & PTR_MASK) != 0;
	case TAG_BIG: case TAG_RAT: return 1;
	}
	double d = double_of(v);
	return d == d && d != 0;
}

// rt_failed reports whether or! should take its right side: errors and zero.
u64 rt_failed(R *r, u64 v) {
	(void)r;
	return is_err(v) || v == 0 || v == SIGN_BIT || v == box(TAG_PTR, 0);
}

static int index_of(R *r, u64 key, u64 n, u64 *out) {
	if (!is_num(key) || !is_exact(key))
		return 0;
	i64 i = to_i64(r, key);
	if (i < 0)
		i += (i64)n;
	if (i < 0 || (u64)i >= n)
		return 0;
	*out = (u64)i;
	return 1;
}

u64 rt_index(R *r, u64 v, u64 key) {
	u64 i;
	switch (tag_of(v)) {
	case TAG_LIST:
		return index_of(r, key, list_len(v), &i) ? list_items(v)[i] : ERR_IDX;
	case TAG_STR:
		return index_of(r, key, str_len(v), &i) ? num(str_data(v)[i]) : ERR_IDX;
	case TAG_MAP: {
		i64 at = map_find(r, v, key);
		return at < 0 ? ERR_KEY : map_entries(v)[2 * at + 1];
	}
	}
	if (is_err(v))
		return v;
	return type_error(r, "index", v, 0);
}

u64 rt_setindex(R *r, u64 v, u64 key, u64 x) {
	u64 i;
	switch (tag_of(v)) {
	case TAG_LIST:
		if (!index_of(r, key, list_len(v), &i))
			return ERR_IDX;
		list_items(v)[i] = x;
		return x;
	case TAG_MAP:
		map_set(r, v, key, x);
		return x;
	}
	return type_error(r, "assign into", v, 0);
}

// rt_field reads m.name, where name is a string constant.
u64 rt_field(R *r, u64 v, u64 name) {
	if (tag_of(v) == TAG_MAP)
		return rt_index(r, v, name);
	if (is_err(v))
		return v;
	return type_error(r, "read a field of", v, 0);
}

static void clamp_range(R *r, u64 a, u64 b, u64 flags, u64 n, u64 *lo, u64 *hi) {
	i64 s = flags & 1 ? to_i64(r, a) : 0, e = flags & 2 ? to_i64(r, b) : (i64)n;
	if (s < 0)
		s += (i64)n;
	if (e < 0)
		e += (i64)n;
	if (s < 0)
		s = 0;
	if (e > (i64)n)
		e = (i64)n;
	if (e < s)
		e = s;
	*lo = (u64)s;
	*hi = (u64)e;
}

u64 rt_slice(R *r, u64 v, u64 a, u64 b, u64 flags) {
	u64 lo, hi;
	switch (tag_of(v)) {
	case TAG_LIST:
		clamp_range(r, a, b, flags, list_len(v), &lo, &hi);
		return list_of(r, list_items(v) + lo, hi - lo);
	case TAG_STR:
		clamp_range(r, a, b, flags, str_len(v), &lo, &hi);
		return str_new(r, str_data(v) + lo, hi - lo);
	}
	if (is_err(v))
		return v;
	return type_error(r, "slice", v, 0);
}

u64 rt_len(R *r, u64 v) {
	switch (tag_of(v)) {
	case TAG_LIST: return num((double)list_len(v));
	case TAG_STR: return num((double)str_len(v));
	case TAG_MAP: return num((double)map_count(v));
	}
	if (is_err(v))
		return v;
	return type_error(r, "take the length of", v, 0);
}

static i64 str_find(u64 s, u64 sub, u64 from) {
	u64 n = str_len(s), m = str_len(sub);
	for (u64 i = from; i + m <= n; i++)
		if (!memcmp_(str_data(s) + i, str_data(sub), m))
			return (i64)i;
	return -1;
}

u64 rt_in(R *r, u64 x, u64 c) {
	switch (tag_of(c)) {
	case TAG_LIST:
		for (u64 i = 0; i < list_len(c); i++)
			if (val_eq(r, x, list_items(c)[i]))
				return num(1);
		return num(0);
	case TAG_MAP:
		return num(map_find(r, c, x) >= 0);
	case TAG_STR:
		if (tag_of(x) != TAG_STR)
			return type_error(r, "search a str for", x, 0);
		return num(str_find(c, x, 0) >= 0);
	}
	return type_error(r, "search", c, 0);
}

u64 rt_range(R *r, u64 a, u64 b, u64 inclusive) {
	if (!is_num(a) || !is_num(b))
		return type_error(r, "make a range of", is_num(a) ? b : a, 0);
	i64 s = to_i64(r, a), e = to_i64(r, b) + (inclusive ? 1 : 0);
	u64 n = e > s ? (u64)(e - s) : 0;
	u64 l = list_new(r, n);
	for (u64 i = 0; i < n; i++)
		list_items(l)[i] = from_i64(r, s + (i64)i);
	obj(l)[1] = n;
	return l;
}

// rt_iter returns the list a loop walks: a list itself, a string's bytes or a map's keys.
u64 rt_iter(R *r, u64 v) {
	switch (tag_of(v)) {
	case TAG_LIST:
		return v;
	case TAG_STR: {
		u64 l = list_new(r, str_len(v));
		for (u64 i = 0; i < str_len(v); i++)
			list_items(l)[i] = num(str_data(v)[i]);
		obj(l)[1] = str_len(v);
		return l;
	}
	case TAG_MAP: {
		u64 l = list_new(r, map_count(v));
		u64 *e = map_entries(v);
		for (u64 i = 0; i < obj(v)[2]; i++)
			if (e[2 * i] != TOMB)
				list_push(r, l, e[2 * i]);
		return l;
	}
	}
	return list_new(r, 0);
}

u64 rt_list(R *r, const u64 *xs, u64 n) { return list_of(r, xs, n); }

u64 rt_map(R *r, const u64 *kv, u64 n) {
	u64 m = map_new(r, n);
	for (u64 i = 0; i < n; i++)
		map_set(r, m, kv[2 * i], kv[2 * i + 1]);
	return m;
}

u64 rt_str(R *r, u64 v) { return to_str(r, v); }

u64 rt_concat(R *r, const u64 *parts, u64 n) {
	Buf b = buf_new(r, 64);
	for (u64 i = 0; i < n; i++) {
		if (tag_of(parts[i]) == TAG_STR)
			putn(&b, str_data(parts[i]), str_len(parts[i]));
		else
			put_val(r, &b, parts[i], 0, 0);
	}
	return buf_str(&b);
}

// rt_print writes values separated by spaces, then a newline if asked.
u64 rt_print(R *r, u64 fd, const u64 *vals, u64 n, u64 newline) {
	Buf b = buf_new(r, 128);
	for (u64 i = 0; i < n; i++) {
		if (i)
			put(&b, ' ');
		put_val(r, &b, vals[i], 0, 0);
	}
	if (newline)
		put(&b, '\n');
	out_write(r, fd, b.p, b.n);
	return 0;
}

static void put_fixed(R *r, Buf *b, u64 v, int prec) {
	double x = to_double(r, v);
	if (x != x || fabs_(x) == double_of(INF_BITS)) {
		put_double(b, x);
		return;
	}
	if (x < 0 || (x == 0 && (bits_of(x) & SIGN_BIT))) {
		put(b, '-');
		x = neg_d(x);
	}
	u64 scale = 1;
	for (int i = 0; i < prec; i++)
		scale *= 10;
	double ip = trunc_d(x);
	double fs = (x - ip) * (double)scale;
	u64 f = (u64)fs;
	double rem = fs - (double)f;
	if (rem > 0.5 || (rem == 0.5 && (f & 1)))
		f++;
	if (f >= scale) {
		f -= scale;
		ip += 1;
	}
	if (ip < 18446744073709551616.0)
		put_u64(b, (u64)ip);
	else
		put_double(b, ip);
	if (prec > 0) {
		put(b, '.');
		char tmp[20];
		for (int i = prec - 1; i >= 0; i--) {
			tmp[i] = (char)('0' + f % 10);
			f /= 10;
		}
		putn(b, tmp, (u64)prec);
	}
}

// rt_printf formats with %d %i %u %x %X %f %.Nf %e %g %s %v %c %q %%.
u64 rt_sprintf(R *r, u64 fmt, const u64 *args, u64 n) {
	if (tag_of(fmt) != TAG_STR)
		return type_error(r, "format with", fmt, 0);
	Buf b = buf_new(r, 128);
	const u8 *f = str_data(fmt);
	u64 len = str_len(fmt), ai = 0;
	for (u64 i = 0; i < len; i++) {
		if (f[i] != '%' || i + 1 == len) {
			put(&b, f[i]);
			continue;
		}
		u64 j = i + 1;
		int left = 0, zero = 0, width = 0, prec = -1;
		for (; j < len && (f[j] == '-' || f[j] == '0' || f[j] == '+' || f[j] == ' '); j++) {
			if (f[j] == '-')
				left = 1;
			if (f[j] == '0')
				zero = 1;
		}
		for (; j < len && f[j] >= '0' && f[j] <= '9'; j++)
			width = width * 10 + (f[j] - '0');
		if (j < len && f[j] == '.') {
			prec = 0;
			for (j++; j < len && f[j] >= '0' && f[j] <= '9'; j++)
				prec = prec * 10 + (f[j] - '0');
		}
		while (j < len && (f[j] == 'l' || f[j] == 'h'))
			j++;
		if (j == len)
			break;
		u8 conv = f[j];
		i = j;
		if (conv == '%') {
			put(&b, '%');
			continue;
		}
		u64 v = ai < n ? args[ai++] : ERR_ARG;
		Buf one = buf_new(r, 32);
		switch (conv) {
		case 'd': case 'i': case 'u':
			put_val(r, &one, is_num(v) ? rounding(r, RND_TRUNC, v) : v, 0, 0);
			break;
		case 'x': case 'X':
			put_hex(&one, (u64)to_i64(r, v), conv == 'X');
			break;
		case 'c': {
			u8 enc[4];
			putn(&one, enc, utf8_encode(enc, (u64)to_i64(r, v) & 0x1FFFFF));
			break;
		}
		case 'g':
			put_val(r, &one, v, 0, 0);
			break;
		case 'f': case 'F': case 'e':
			if (!is_num(v))
				put_val(r, &one, v, 0, 0);
			else
				put_fixed(r, &one, v, prec < 0 ? 6 : (prec > 30 ? 30 : prec));
			break;
		case 'q':
			put_val(r, &one, v, 1, 0);
			break;
		default: // s, v
			put_val(r, &one, v, 0, 0);
			if (conv == 's' && prec >= 0 && one.n > (u64)prec)
				one.n = (u64)prec;
		}
		u64 pad = (u64)width > one.n ? (u64)width - one.n : 0;
		if (!left)
			for (u64 k = 0; k < pad; k++)
				put(&b, zero && conv != 's' ? '0' : ' ');
		if (!left && zero && pad && one.n && one.p[0] == '-' && conv != 's') {
			// move the sign in front of the zero padding
			b.p[b.n - pad] = '-';
			putn(&b, one.p + 1, one.n - 1);
			b.p[b.n - one.n] = '0';
		} else
			putn(&b, one.p, one.n);
		if (left)
			for (u64 k = 0; k < pad; k++)
				put(&b, ' ');
	}
	return buf_str(&b);
}

u64 rt_printf(R *r, u64 fd, u64 fmt, const u64 *args, u64 n) {
	u64 s = rt_sprintf(r, fmt, args, n);
	if (is_err(s))
		return s;
	out_write(r, fd, str_data(s), str_len(s));
	return 0;
}

// Functions and closures

u64 rt_closure(R *r, u64 code, u64 arity, const u64 *caps, u64 n) {
	u64 *p = alloc_obj(r, K_FN, 4 + n);
	p[1] = code;
	p[2] = arity;
	p[3] = n;
	memcpy(p + 4, caps, n * 8);
	return box(TAG_FN, p);
}

u64 rt_cell(R *r, u64 v) { return cell_new(r, v); }

// rt_fn_code returns the code address to call f with argc arguments, or 0.
u64 rt_fn_code(R *r, u64 f, u64 argc) {
	(void)r;
	if (tag_of(f) != TAG_FN)
		return 0;
	u64 *p = obj(f), want = p[2] & 0xFFFFFFFF;
	if (p[2] >> 32 ? argc < want : argc != want)
		return 0;
	return p[1];
}

u64 rt_call_error(R *r, u64 f, u64 argc) {
	if (is_err(f))
		return f;
	if (tag_of(f) != TAG_FN)
		return type_error(r, "call", f, 0);
	Buf b = buf_new(r, 64);
	u64 want = obj(f)[2] & 0xFFFFFFFF;
	puts_(&b, "function takes ");
	if (obj(f)[2] >> 32)
		puts_(&b, "at least ");
	put_u64(&b, want);
	puts_(&b, want == 1 ? " argument but got " : " arguments but got ");
	put_u64(&b, argc);
	return err_new(r, b.p, b.n);
}

u64 rt_error(R *r, u64 v) {
	if (is_err(v))
		return v;
	u64 s = to_str(r, v);
	return err_new(r, str_data(s), str_len(s));
}

// rt_errtext is v.error: the error's code or message, or "" when v is not an error.
u64 rt_errtext(R *r, u64 v) { return is_err(v) ? err_text(r, v) : str_new(r, "", 0); }

// Builtins. A builtin named f is rt_f; variadic ones take an array and a count.

static int want_num(R *r, u64 v, const char *fn, u64 *err) {
	if (is_num(v))
		return 1;
	if (is_err(v)) {
		*err = v;
		return 0;
	}
	Buf b = buf_new(r, 64);
	puts_(&b, fn);
	puts_(&b, " needs a num, not ");
	put_a(&b, v);
	*err = err_new(r, b.p, b.n);
	return 0;
}

static int want_str(R *r, u64 v, const char *fn, u64 *err) {
	if (tag_of(v) == TAG_STR)
		return 1;
	if (is_err(v)) {
		*err = v;
		return 0;
	}
	Buf b = buf_new(r, 64);
	puts_(&b, fn);
	puts_(&b, " needs a str, not ");
	put_a(&b, v);
	*err = err_new(r, b.p, b.n);
	return 0;
}

static int want_list(R *r, u64 v, const char *fn, u64 *err) {
	if (tag_of(v) == TAG_LIST)
		return 1;
	if (is_err(v)) {
		*err = v;
		return 0;
	}
	Buf b = buf_new(r, 64);
	puts_(&b, fn);
	puts_(&b, " needs a list, not ");
	put_a(&b, v);
	*err = err_new(r, b.p, b.n);
	return 0;
}

#define NUM(v, fn) do { u64 e_; if (!want_num(r, v, fn, &e_)) return e_; } while (0)
#define STR(v, fn) do { u64 e_; if (!want_str(r, v, fn, &e_)) return e_; } while (0)
#define LIST(v, fn) do { u64 e_; if (!want_list(r, v, fn, &e_)) return e_; } while (0)

static u64 math1(R *r, u64 v, const char *fn, double (*f)(double)) {
	NUM(v, fn);
	return num(f(to_double(r, v)));
}

static double tan_d(double x) { return sin_d(x) / cos_d(x); }
static double acos_d(double x) { return 1.5707963267948966 - asin_d(x); }

u64 rt_abs(R *r, u64 v) { NUM(v, "abs"); return rounding(r, RND_ABS, v); }
u64 rt_floor(R *r, u64 v) { NUM(v, "floor"); return rounding(r, RND_FLOOR, v); }
u64 rt_ceil(R *r, u64 v) { NUM(v, "ceil"); return rounding(r, RND_CEIL, v); }
u64 rt_round(R *r, u64 v) { NUM(v, "round"); return rounding(r, RND_ROUND, v); }
u64 rt_trunc(R *r, u64 v) { NUM(v, "trunc"); return rounding(r, RND_TRUNC, v); }
u64 rt_sqrt(R *r, u64 v) { return math1(r, v, "sqrt", sqrt_); }
u64 rt_exp(R *r, u64 v) { return math1(r, v, "exp", exp_d); }
u64 rt_log(R *r, u64 v) { return math1(r, v, "log", log_d); }
u64 rt_log10(R *r, u64 v) { return math1(r, v, "log10", log10_d); }
u64 rt_sin(R *r, u64 v) { return math1(r, v, "sin", sin_d); }
u64 rt_cos(R *r, u64 v) { return math1(r, v, "cos", cos_d); }
u64 rt_tan(R *r, u64 v) { return math1(r, v, "tan", tan_d); }
u64 rt_asin(R *r, u64 v) { return math1(r, v, "asin", asin_d); }
u64 rt_acos(R *r, u64 v) { return math1(r, v, "acos", acos_d); }
u64 rt_atan(R *r, u64 v) { return math1(r, v, "atan", atan_d); }
u64 rt_float(R *r, u64 v) { NUM(v, "float"); return num(to_double(r, v)); }

u64 rt_atan2(R *r, u64 y, u64 x) {
	NUM(y, "atan2");
	NUM(x, "atan2");
	return num(atan2_d(to_double(r, y), to_double(r, x)));
}

u64 rt_pow(R *r, u64 a, u64 b) { return rt_binop(r, OP_POW, a, b); }

static u64 extreme(R *r, const u64 *xs, u64 n, int sign, const char *fn) {
	if (n == 1 && tag_of(xs[0]) == TAG_LIST) {
		n = list_len(xs[0]);
		xs = list_items(xs[0]);
	}
	if (!n) {
		Buf b = buf_new(r, 32);
		puts_(&b, fn);
		puts_(&b, " of nothing");
		return err_new(r, b.p, b.n);
	}
	u64 best = xs[0];
	for (u64 i = 1; i < n; i++) {
		int ok = 1, k = val_cmp(r, xs[i], best, &ok);
		if (!ok)
			return type_error(r, "compare", xs[i], best);
		if (k * sign > 0)
			best = xs[i];
	}
	return best;
}

u64 rt_min(R *r, const u64 *xs, u64 n) { return extreme(r, xs, n, -1, "min"); }
u64 rt_max(R *r, const u64 *xs, u64 n) { return extreme(r, xs, n, 1, "max"); }

u64 rt_gcd(R *r, u64 a, u64 b) {
	NUM(a, "gcd");
	NUM(b, "gcd");
	a = rounding(r, RND_ABS, a);
	b = rounding(r, RND_ABS, b);
	if (!is_exact(a) || !is_exact(b) || tag_of(a) == TAG_RAT || tag_of(b) == TAG_RAT)
		return errorf(r, "gcd needs integers");
	while (!(b == 0)) {
		u64 t = rt_binop(r, OP_MOD, a, b);
		a = b;
		b = t;
	}
	return a;
}

static u64 rotl64(u64 x, int k) { return (x << k) | (x >> (64 - k)); }

static u64 next_random(R *r) {
	if (!r->seeded) {
		if (r->os->random(r->rng, sizeof r->rng) != sizeof r->rng)
			r->rng[0] = 0x9E3779B97F4A7C15ull ^ (u64)r;
		if (!(r->rng[0] | r->rng[1] | r->rng[2] | r->rng[3]))
			r->rng[0] = 1;
		r->seeded = 1;
	}
	u64 *s = r->rng, res = rotl64(s[1] * 5, 7) * 9, t = s[1] << 17;
	s[2] ^= s[0];
	s[3] ^= s[1];
	s[1] ^= s[2];
	s[0] ^= s[3];
	s[2] ^= t;
	s[3] = rotl64(s[3], 45);
	return res;
}

u64 rt_random(R *r) { return num((double)(next_random(r) >> 11) * 0x1p-53); }

// Parsing numbers: decimal, 0x/0o/0b, '_' separators, fractions and exponents, all exact.

static u64 parse_digits(R *r, const u8 *s, u64 n, u64 base, u64 *used) {
	u64 v = 0, chunk = 0, scale = 1, i = 0, any = 0;
	for (; i < n; i++) {
		u8 c = s[i];
		u64 d;
		if (c == '_' && any)
			continue;
		if (c >= '0' && c <= '9')
			d = c - '0';
		else if ((c | 32) >= 'a' && (c | 32) <= 'f')
			d = (c | 32) - 'a' + 10;
		else
			break;
		if (d >= base)
			break;
		any = 1;
		chunk = chunk * base + d;
		scale *= base;
		if (scale > (1ull << 52) / 16) {
			v = rt_binop(r, OP_ADD, rt_binop(r, OP_MUL, v, from_i64(r, (i64)scale)), from_i64(r, (i64)chunk));
			chunk = 0;
			scale = 1;
		}
	}
	if (scale > 1)
		v = rt_binop(r, OP_ADD, rt_binop(r, OP_MUL, v, from_i64(r, (i64)scale)), from_i64(r, (i64)chunk));
	*used = any ? i : 0;
	return v;
}

u64 rt_num(R *r, u64 s) {
	if (is_num(s))
		return s;
	STR(s, "num");
	const u8 *p = str_data(s);
	u64 n = str_len(s), i = 0, k;
	while (i < n && (p[i] == ' ' || p[i] == '\t' || p[i] == '\n' || p[i] == '\r'))
		i++;
	while (n > i && (p[n - 1] == ' ' || p[n - 1] == '\t' || p[n - 1] == '\n' || p[n - 1] == '\r'))
		n--;
	int neg = 0;
	if (i < n && (p[i] == '-' || p[i] == '+'))
		neg = p[i++] == '-';
	u64 base = 10;
	if (i + 1 < n && p[i] == '0' && ((p[i + 1] | 32) == 'x' || (p[i + 1] | 32) == 'o' || (p[i + 1] | 32) == 'b')) {
		base = (p[i + 1] | 32) == 'x' ? 16 : (p[i + 1] | 32) == 'o' ? 8 : 2;
		i += 2;
	}
	u64 v = parse_digits(r, p + i, n - i, base, &k);
	if (!k && !(base == 10 && i < n && p[i] == '.'))
		goto bad;
	i += k;
	if (base == 10 && i < n && p[i] == '.') {
		i++;
		u64 f = parse_digits(r, p + i, n - i, 10, &k);
		u64 digits = 0;
		for (u64 j = 0; j < k; j++)
			digits += p[i + j] != '_';
		v = rt_binop(r, OP_ADD, v, rt_binop(r, OP_DIV, f, rt_binop(r, OP_POW, num(10), num((double)digits))));
		i += k;
	}
	if (base == 10 && i < n && (p[i] | 32) == 'e') {
		i++;
		int eneg = 0;
		if (i < n && (p[i] == '-' || p[i] == '+'))
			eneg = p[i++] == '-';
		u64 e = parse_digits(r, p + i, n - i, 10, &k);
		if (!k)
			goto bad;
		i += k;
		v = rt_binop(r, OP_MUL, v, rt_binop(r, OP_POW, num(10), eneg ? rt_neg(r, e) : e));
	}
	if (i != n)
		goto bad;
	return neg ? rt_neg(r, v) : v;
bad:;
	Buf b = buf_new(r, 64);
	puts_(&b, "not a number: ");
	put_quoted(&b, s);
	return err_new(r, b.p, b.n);
}

u64 rt_type(R *r, u64 v) { return cstr(r, type_name(v)); }

// Strings and UTF-8

static u64 utf8_encode(u8 *out, u64 cp) {
	if (cp < 0x80) {
		out[0] = (u8)cp;
		return 1;
	}
	if (cp < 0x800) {
		out[0] = (u8)(0xC0 | cp >> 6);
		out[1] = (u8)(0x80 | (cp & 63));
		return 2;
	}
	if (cp < 0x10000) {
		out[0] = (u8)(0xE0 | cp >> 12);
		out[1] = (u8)(0x80 | (cp >> 6 & 63));
		out[2] = (u8)(0x80 | (cp & 63));
		return 3;
	}
	out[0] = (u8)(0xF0 | cp >> 18);
	out[1] = (u8)(0x80 | (cp >> 12 & 63));
	out[2] = (u8)(0x80 | (cp >> 6 & 63));
	out[3] = (u8)(0x80 | (cp & 63));
	return 4;
}

// utf8_decode reads one code point, taking invalid bytes as U+FFFD.
static u64 utf8_decode(const u8 *s, u64 n, u64 *len) {
	u8 c = s[0];
	u64 need = c < 0x80 ? 0 : c >= 0xF0 && c < 0xF5 ? 3 : c >= 0xE0 ? 2 : c >= 0xC2 && c < 0xE0 ? 1 : 9;
	*len = 1;
	if (!need)
		return c;
	if (need == 9 || need >= n)
		return 0xFFFD;
	u64 cp = c & (0x3F >> need);
	for (u64 i = 1; i <= need; i++) {
		if ((s[i] & 0xC0) != 0x80)
			return 0xFFFD;
		cp = cp << 6 | (s[i] & 63);
	}
	static const u64 lo[] = {0, 0x80, 0x800, 0x10000};
	if (cp < lo[need] || cp > 0x10FFFF || (cp >= 0xD800 && cp < 0xE000))
		return 0xFFFD;
	*len = need + 1;
	return cp;
}

u64 rt_chr(R *r, u64 v) {
	NUM(v, "chr");
	i64 cp = to_i64(r, v);
	if (cp < 0 || cp > 0x10FFFF || (cp >= 0xD800 && cp < 0xE000))
		return errorf(r, "chr needs a code point");
	u8 b[4];
	return str_new(r, b, utf8_encode(b, (u64)cp));
}

u64 rt_ord(R *r, u64 s) {
	STR(s, "ord");
	if (!str_len(s))
		return errorf(r, "ord of an empty string");
	u64 k;
	return num((double)utf8_decode(str_data(s), str_len(s), &k));
}

u64 rt_bytes(R *r, u64 s) { STR(s, "bytes"); return rt_iter(r, s); }

u64 rt_runes(R *r, u64 s) {
	STR(s, "runes");
	u64 l = list_new(r, str_len(s)), k;
	for (u64 i = 0; i < str_len(s); i += k)
		list_push(r, l, num((double)utf8_decode(str_data(s) + i, str_len(s) - i, &k)));
	return l;
}

static u64 map_bytes(R *r, u64 s, const char *fn, int upper) {
	STR(s, fn);
	u64 t = str_new(r, str_data(s), str_len(s));
	u8 *d = str_data(t);
	for (u64 i = 0; i < str_len(t); i++)
		if (upper ? d[i] >= 'a' && d[i] <= 'z' : d[i] >= 'A' && d[i] <= 'Z')
			d[i] ^= 32;
	return t;
}

u64 rt_upper(R *r, u64 s) { return map_bytes(r, s, "upper", 1); }
u64 rt_lower(R *r, u64 s) { return map_bytes(r, s, "lower", 0); }

static int space(u8 c) { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'; }

u64 rt_trim(R *r, u64 s) {
	STR(s, "trim");
	u64 a = 0, b = str_len(s);
	while (a < b && space(str_data(s)[a]))
		a++;
	while (b > a && space(str_data(s)[b - 1]))
		b--;
	return str_new(r, str_data(s) + a, b - a);
}

u64 rt_split(R *r, u64 s, u64 sep) {
	STR(s, "split");
	STR(sep, "split");
	u64 l = list_new(r, 4), start = 0;
	if (!str_len(sep)) {
		u64 k;
		for (u64 i = 0; i < str_len(s); i += k) {
			utf8_decode(str_data(s) + i, str_len(s) - i, &k);
			list_push(r, l, str_new(r, str_data(s) + i, k));
		}
		return l;
	}
	for (i64 at; (at = str_find(s, sep, start)) >= 0; start = (u64)at + str_len(sep))
		list_push(r, l, str_new(r, str_data(s) + start, (u64)at - start));
	list_push(r, l, str_new(r, str_data(s) + start, str_len(s) - start));
	return l;
}

u64 rt_join(R *r, u64 xs, u64 sep) {
	LIST(xs, "join");
	STR(sep, "join");
	Buf b = buf_new(r, 64);
	for (u64 i = 0; i < list_len(xs); i++) {
		if (i)
			putn(&b, str_data(sep), str_len(sep));
		u64 x = list_items(xs)[i];
		if (tag_of(x) == TAG_STR)
			putn(&b, str_data(x), str_len(x));
		else
			put_val(r, &b, x, 0, 0);
	}
	return buf_str(&b);
}

u64 rt_replace(R *r, u64 s, u64 old, u64 new) {
	STR(s, "replace");
	STR(old, "replace");
	STR(new, "replace");
	if (!str_len(old))
		return s;
	Buf b = buf_new(r, str_len(s) + 1);
	u64 start = 0;
	for (i64 at; (at = str_find(s, old, start)) >= 0; start = (u64)at + str_len(old)) {
		putn(&b, str_data(s) + start, (u64)at - start);
		putn(&b, str_data(new), str_len(new));
	}
	putn(&b, str_data(s) + start, str_len(s) - start);
	return buf_str(&b);
}

u64 rt_starts_with(R *r, u64 s, u64 p) {
	STR(s, "starts_with");
	STR(p, "starts_with");
	return num(str_len(p) <= str_len(s) && !memcmp_(str_data(s), str_data(p), str_len(p)));
}

u64 rt_ends_with(R *r, u64 s, u64 p) {
	STR(s, "ends_with");
	STR(p, "ends_with");
	return num(str_len(p) <= str_len(s) && !memcmp_(str_data(s) + str_len(s) - str_len(p), str_data(p), str_len(p)));
}

u64 rt_find(R *r, u64 c, u64 x) {
	if (tag_of(c) == TAG_LIST) {
		for (u64 i = 0; i < list_len(c); i++)
			if (val_eq(r, list_items(c)[i], x))
				return num((double)i);
		return num(-1);
	}
	STR(c, "find");
	STR(x, "find");
	return num((double)str_find(c, x, 0));
}

// Lists and maps

u64 rt_push(R *r, u64 xs, u64 x) {
	LIST(xs, "push");
	list_push(r, xs, x);
	return xs;
}

u64 rt_pop(R *r, u64 xs) {
	LIST(xs, "pop");
	if (!list_len(xs))
		return errorf(r, "pop from an empty list");
	return list_items(xs)[--obj(xs)[1]];
}

static u64 map_part(R *r, u64 m, int which, const char *fn) {
	if (tag_of(m) != TAG_MAP)
		return is_err(m) ? m : type_error(r, fn, m, 0);
	u64 l = list_new(r, map_count(m));
	u64 *e = map_entries(m);
	for (u64 i = 0; i < obj(m)[2]; i++)
		if (e[2 * i] != TOMB)
			list_push(r, l, e[2 * i + which]);
	return l;
}

u64 rt_keys(R *r, u64 m) { return map_part(r, m, 0, "take the keys of"); }
u64 rt_values(R *r, u64 m) { return map_part(r, m, 1, "take the values of"); }

u64 rt_remove(R *r, u64 m, u64 k) {
	if (tag_of(m) == TAG_LIST) {
		u64 i;
		if (!index_of(r, k, list_len(m), &i))
			return ERR_IDX;
		u64 x = list_items(m)[i];
		memmove(list_items(m) + i, list_items(m) + i + 1, (list_len(m) - i - 1) * 8);
		obj(m)[1]--;
		return x;
	}
	if (tag_of(m) != TAG_MAP)
		return is_err(m) ? m : type_error(r, "remove from", m, 0);
	i64 at = map_find(r, m, k);
	if (at < 0)
		return ERR_KEY;
	u64 *e = map_entries(m), v = e[2 * at + 1];
	e[2 * at] = TOMB;
	e[2 * at + 1] = 0;
	obj(m)[1]--;
	return v;
}

// merge sorts idx[0..n) by keys, stable; returns 0 when two keys cannot be compared.
static int sort_idx(R *r, u64 *idx, u64 *tmp, const u64 *keys, u64 n) {
	if (n < 2)
		return 1;
	u64 h = n / 2;
	if (!sort_idx(r, idx, tmp, keys, h) || !sort_idx(r, idx + h, tmp, keys, n - h))
		return 0;
	u64 i = 0, j = h, k = 0;
	while (i < h && j < n) {
		int ok = 1, c = val_cmp(r, keys[idx[j]], keys[idx[i]], &ok);
		if (!ok)
			return 0;
		tmp[k++] = c < 0 ? idx[j++] : idx[i++];
	}
	while (i < h)
		tmp[k++] = idx[i++];
	while (j < n)
		tmp[k++] = idx[j++];
	memcpy(idx, tmp, n * 8);
	return 1;
}

// rt_sort_by returns xs ordered by the matching keys; sort(xs) passes xs as its own keys.
u64 rt_sort_by(R *r, u64 xs, u64 keys) {
	LIST(xs, "sort");
	LIST(keys, "sort");
	u64 n = list_len(xs);
	if (list_len(keys) != n)
		return errorf(r, "sort keys do not match the list");
	u64 *idx = raw(r, n * 8 + 8), *tmp = raw(r, n * 8 + 8);
	for (u64 i = 0; i < n; i++)
		idx[i] = i;
	if (!sort_idx(r, idx, tmp, list_items(keys), n))
		return errorf(r, "cannot sort values of different types");
	u64 l = list_new(r, n);
	for (u64 i = 0; i < n; i++)
		list_items(l)[i] = list_items(xs)[idx[i]];
	obj(l)[1] = n;
	return l;
}

u64 rt_sort(R *r, u64 xs) { return rt_sort_by(r, xs, xs); }

u64 rt_reverse(R *r, u64 v) {
	if (tag_of(v) == TAG_STR) {
		u64 n = str_len(v), t = str_new(r, str_data(v), n), k;
		for (u64 i = 0; i < n; i += k) {
			utf8_decode(str_data(v) + i, n - i, &k);
			memcpy(str_data(t) + n - i - k, str_data(v) + i, k);
		}
		return t;
	}
	LIST(v, "reverse");
	u64 n = list_len(v), l = list_new(r, n);
	for (u64 i = 0; i < n; i++)
		list_items(l)[i] = list_items(v)[n - 1 - i];
	obj(l)[1] = n;
	return l;
}

u64 rt_sum(R *r, u64 xs) {
	LIST(xs, "sum");
	u64 s = 0;
	for (u64 i = 0; i < list_len(xs); i++)
		s = rt_binop(r, OP_ADD, s, list_items(xs)[i]);
	return s;
}

u64 rt_zip(R *r, u64 a, u64 b) {
	LIST(a, "zip");
	LIST(b, "zip");
	u64 n = list_len(a) < list_len(b) ? list_len(a) : list_len(b), l = list_new(r, n);
	for (u64 i = 0; i < n; i++) {
		u64 pair[2] = {list_items(a)[i], list_items(b)[i]};
		list_items(l)[i] = list_of(r, pair, 2);
	}
	obj(l)[1] = n;
	return l;
}

u64 rt_enumerate(R *r, u64 a) {
	a = rt_iter(r, a);
	u64 n = list_len(a), l = list_new(r, n);
	for (u64 i = 0; i < n; i++) {
		u64 pair[2] = {num((double)i), list_items(a)[i]};
		list_items(l)[i] = list_of(r, pair, 2);
	}
	obj(l)[1] = n;
	return l;
}

// The process, files and input

u64 rt_args(R *r) {
	u64 l = list_new(r, r->argc);
	for (u64 i = 0; i < r->argc; i++)
		list_push(r, l, cstr(r, r->argv[i]));
	return l;
}

u64 rt_env(R *r, u64 name) {
	STR(name, "env");
	for (char **e = r->envp; e && *e; e++) {
		u64 n = str_len(name);
		if (!memcmp_(*e, str_data(name), n) && (*e)[n] == '=')
			return cstr(r, *e + n + 1);
	}
	return ERR_UDF;
}

static int refill(R *r) {
	if (r->ineof)
		return 0;
	out_flush(r);
	i64 n = r->os->read(0, r->in, sizeof r->in);
	if (n <= 0) {
		r->ineof = 1;
		return 0;
	}
	r->inpos = 0;
	r->inlen = (u64)n;
	return 1;
}

u64 rt_readln(R *r) {
	Buf b = buf_new(r, 64);
	int any = 0;
	for (;;) {
		if (r->inpos == r->inlen && !refill(r))
			break;
		any = 1;
		u8 c = r->in[r->inpos++];
		if (c == '\n')
			break;
		put(&b, c);
	}
	if (!any)
		return ERR_EOF;
	if (b.n && b.p[b.n - 1] == '\r')
		b.n--;
	return buf_str(&b);
}

u64 rt_read_file(R *r, u64 path) {
	STR(path, "read_file");
	i64 fd = r->os->open((const char *)str_data(path), 0);
	if (fd < 0)
		return ERR_IO;
	Buf b = buf_new(r, 4096);
	for (;;) {
		buf_grow(&b, 4096);
		i64 n = r->os->read(fd, b.p + b.n, b.cap - b.n);
		if (n < 0) {
			r->os->close(fd);
			return ERR_IO;
		}
		if (!n)
			break;
		b.n += (u64)n;
	}
	r->os->close(fd);
	return buf_str(&b);
}

u64 rt_write_file(R *r, u64 path, u64 data) {
	STR(path, "write_file");
	u64 s = to_str(r, data);
	i64 fd = r->os->open((const char *)str_data(path), 1);
	if (fd < 0)
		return ERR_IO;
	for (u64 off = 0; off < str_len(s);) {
		i64 n = r->os->write(fd, str_data(s) + off, str_len(s) - off);
		if (n <= 0) {
			r->os->close(fd);
			return ERR_IO;
		}
		off += (u64)n;
	}
	r->os->close(fd);
	return num((double)str_len(s));
}
