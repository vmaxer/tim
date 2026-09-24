// Tim numeric runtime: exact integers of any size, exact rationals and inexact
// float64, behind a single entry point. Freestanding; built into a flat
// position-independent blob by build.sh and embedded by the compiler.
//
// Value encoding (one 64-bit word):
//   float64 that is integral with |v| < 2^53  exact small integer
//   0xFFF9 << 48 | ptr                         exact big integer
//   0xFFFA << 48 | ptr                         exact rational
//   any other float64                          inexact number (NaN = error)
//
// Heap/rodata layouts (8-byte words):
//   big integer: [n | sign<<63] [limb 0] ... [limb n-1]
//   rational:    numerator as big integer, then denominator as big integer

typedef unsigned long long u64;
typedef long long i64;
typedef unsigned int u32;
typedef void *(*alloc_fn)(void *ctx, u64 size);

#define TAG_BIG 0xFFF9ull
#define TAG_RAT 0xFFFAull
#define PTR_MASK 0x0000FFFFFFFFFFFFull
#define ERR_DV0 0x7FF8000064763000ull
#define ERR_OVF 0x7FF800006F766600ull
#define ERR_ARG 0x7FF8000061726700ull
#define SIGN_BIT 0x8000000000000000ull
#define TWO53 9007199254740992.0
#define MAX_POW_BITS (1ull << 22)

enum {
	OP_ADD = 1, OP_SUB, OP_MUL, OP_DIV, OP_MOD, OP_POW,
	OP_LT, OP_LE, OP_GT, OP_GE, OP_EQ, OP_NE,
	OP_STR, OP_FLOAT, OP_FLOOR, OP_CEIL, OP_ROUND, OP_TRUNC, OP_ABS,
	OP_TO_I64, OP_FROM_I64, OP_FROM_U64, OP_EXACT,
};

void *memcpy(void *d, const void *s, u64 n) {
	unsigned char *dp = d;
	const unsigned char *sp = s;
	while (n--)
		*dp++ = *sp++;
	return d;
}

void *memset(void *d, int c, u64 n) {
	unsigned char *dp = d;
	while (n--)
		*dp++ = (unsigned char)c;
	return d;
}

static inline u64 bits_of(double d) { u64 u; __builtin_memcpy(&u, &d, 8); return u; }
static inline double double_of(u64 u) { double d; __builtin_memcpy(&d, &u, 8); return d; }

// Scalar-only helpers: the blob may load at any 16-byte misalignment, so avoid
// the packed SSE constant loads the compiler uses for u64 conversion and fneg.
static double u64_to_double(u64 x) {
	if ((i64)x >= 0)
		return (double)(i64)x;
	double d = (double)(i64)((x >> 1) | (x & 1));
	return d + d;
}
static inline double neg_d(double x) { return 0.0 - x; }

typedef struct { alloc_fn fn; void *ctx; } Ctx;

static void *alloc_(Ctx *c, u64 n) { return c->fn(c->ctx, n); }
static u64 *new_limbs(Ctx *c, u64 n) { return alloc_(c, (n ? n : 1) * 8); }

// Magnitude: little-endian 64-bit limbs, n == 0 means zero, d[n-1] != 0.
typedef struct { u64 *d; u64 n; } Mag;
typedef struct { Mag m; int neg; } Int;
typedef struct { Int num; Mag den; u64 nbuf, dbuf; } Rat;

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

static Mag mag_add(Ctx *c, const Mag *a, const Mag *b) {
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
static Mag mag_sub(Ctx *c, const Mag *a, const Mag *b) {
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

static Mag mag_mul(Ctx *c, const Mag *a, const Mag *b) {
	if (!a->n || !b->n)
		return (Mag){0, 0};
	Mag r = {new_limbs(c, a->n + b->n), a->n + b->n};
	memset(r.d, 0, r.n * 8);
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

static Mag mag_divmod1(Ctx *c, const Mag *a, u64 d, u64 *rem) {
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
static void mag_divmod(Ctx *c, const Mag *a, const Mag *b, Mag *q, Mag *r) {
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

static Mag mag_gcd(Ctx *c, Mag a, Mag b) {
	while (b.n) {
		Mag q, r;
		mag_divmod(c, &a, &b, &q, &r);
		a = b;
		b = r;
	}
	return a;
}

static Mag mag_from_u64(Ctx *c, u64 v) {
	Mag m = {new_limbs(c, 1), 1};
	m.d[0] = v;
	mag_norm(&m);
	return m;
}

static u64 mag_bitlen(const Mag *a) {
	return a->n ? a->n * 64 - clz64(a->d[a->n - 1]) : 0;
}

static Mag mag_shl(Ctx *c, const Mag *a, u64 k) {
	if (!a->n)
		return *a;
	u64 w = k / 64, s = k % 64;
	Mag r = {new_limbs(c, a->n + w + 1), a->n + w + 1};
	memset(r.d, 0, r.n * 8);
	for (u64 i = 0; i < a->n; i++) {
		r.d[i + w] |= a->d[i] << s;
		if (s)
			r.d[i + w + 1] |= a->d[i] >> (64 - s);
	}
	mag_norm(&r);
	return r;
}

// Integers

static Int int_add(Ctx *c, const Int *a, const Int *b) {
	if (a->neg == b->neg)
		return (Int){mag_add(c, &a->m, &b->m), a->neg};
	int k = mag_cmp(&a->m, &b->m);
	if (k == 0)
		return (Int){{0, 0}, 0};
	if (k > 0)
		return (Int){mag_sub(c, &a->m, &b->m), a->neg};
	return (Int){mag_sub(c, &b->m, &a->m), b->neg};
}

static Int int_mul(Ctx *c, const Int *a, const Int *b) {
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
static void int_divmod(Ctx *c, const Int *a, const Mag *b, Int *q, Int *r) {
	Mag qm, rm;
	mag_divmod(c, &a->m, b, &qm, &rm);
	*q = (Int){qm, qm.n ? a->neg : 0};
	*r = (Int){rm, rm.n ? a->neg : 0};
}

// Value encoding

static inline u64 tag_of(u64 v) { return v >> 48; }
static inline const u64 *ptr_of(u64 v) { return (const u64 *)(v & PTR_MASK); }

static int is_small(u64 v) {
	double d = double_of(v);
	if (!(d > -TWO53 && d < TWO53))
		return 0;
	return (double)(i64)d == d;
}

static int is_exact(u64 v) {
	u64 t = tag_of(v);
	return t == TAG_BIG || t == TAG_RAT || is_small(v);
}

static int is_nan(u64 v) {
	double d = double_of(v);
	return d != d && tag_of(v) != TAG_BIG && tag_of(v) != TAG_RAT;
}

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
		read_int(ptr_of(v), &r->num);
	} else if (t == TAG_RAT) {
		Int den;
		read_int(read_int(ptr_of(v), &r->num), &den);
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

static u64 box(u64 tag, const void *p) { return (tag << 48) | ((u64)p & PTR_MASK); }

static u64 encode_int(Ctx *c, const Int *a) {
	if (a->m.n == 0)
		return 0;
	if (a->m.n == 1 && a->m.d[0] < (1ull << 53)) {
		double d = (double)(i64)a->m.d[0];
		return bits_of(a->neg ? neg_d(d) : d);
	}
	u64 *p = alloc_(c, 8 + a->m.n * 8);
	store_int(p, a);
	return box(TAG_BIG, p);
}

static u64 encode(Ctx *c, Int num, Mag den) {
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
	u64 *p = alloc_(c, 16 + (num.m.n + den.n) * 8);
	Int d = {den, 0};
	store_int(store_int(p, &num), &d);
	return box(TAG_RAT, p);
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

static double to_double(Ctx *c, u64 v) {
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

#if !defined(__x86_64__)
#define LN2_HI 6.93147180369123816490e-01
#define LN2_LO 1.90821492927058770002e-10

// log(x) for x > 0: x = m * 2^e with m in [sqrt(1/2), sqrt(2)), log(m) = 2 atanh(s).
static double log_d(double x) {
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
	if (y > 709.8)
		return double_of(0x7FF0000000000000ull);
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
#endif

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
#if defined(__x86_64__)
	double r;
	__asm__(
		"fyl2x\n\t"
		"fld %%st(0)\n\t"
		"frndint\n\t"
		"fxch %%st(1)\n\t"
		"fsub %%st(1), %%st\n\t"
		"f2xm1\n\t"
		"fld1\n\t"
		"faddp %%st, %%st(1)\n\t"
		"fscale\n\t"
		"fstp %%st(1)\n\t"
		: "=t"(r)
		: "0"(x), "u"(y)
		: "st(1)");
	return r;
#else
	if (x < 0.0)
		return double_of(0x7FF8000000000000ull);
	return exp_d(y * log_d(x));
#endif
}

static u64 inexact(Ctx *c, u64 op, u64 a, u64 b) {
	double x = to_double(c, a), y = to_double(c, b);
	switch (op) {
	case OP_ADD: return bits_of(x + y);
	case OP_SUB: return bits_of(x - y);
	case OP_MUL: return bits_of(x * y);
	case OP_DIV: return y == 0.0 ? ERR_DV0 : bits_of(x / y);
	case OP_MOD: return y == 0.0 ? ERR_DV0 : bits_of(x - trunc_d(x / y) * y);
	case OP_POW: return bits_of(pow_d(x, y));
	case OP_LT: return bits_of(x < y);
	case OP_LE: return bits_of(x <= y);
	case OP_GT: return bits_of(x > y);
	case OP_GE: return bits_of(x >= y);
	case OP_EQ: return bits_of(x == y);
	case OP_NE: return bits_of(x != y);
	}
	return ERR_ARG;
}

// Exact arithmetic

static u64 exact_pow(Ctx *c, Rat *a, Rat *b) {
	if (!mag_is_one(&b->den))
		return bits_of(pow_d(to_double(c, encode(c, a->num, a->den)), to_double(c, encode(c, b->num, b->den))));
	if (!b->num.m.n)
		return bits_of(1.0);
	if (b->num.m.n > 1)
		return ERR_OVF;
	u64 e = b->num.m.d[0];
	Int num = a->num;
	Mag den = a->den;
	if (b->num.neg) {
		if (!num.m.n)
			return ERR_DV0;
		Mag t = num.m;
		num.m = den;
		den = t;
	}
	if (mag_bitlen(&num.m) > 1 && e > MAX_POW_BITS / (mag_bitlen(&num.m) - 1))
		return ERR_OVF;
	if (mag_bitlen(&den) > 1 && e > MAX_POW_BITS / (mag_bitlen(&den) - 1))
		return ERR_OVF;
	Int rn = {mag_from_u64(c, 1), 0};
	Mag rd = mag_from_u64(c, 1);
	Int bn = num;
	Mag bd = den;
	int neg = num.neg && (e & 1);
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

static Int int_from_mag(Mag m, int neg) { return (Int){m, m.n ? neg : 0}; }

// sign of a - b
static int exact_cmp(Ctx *c, Rat *a, Rat *b) {
	Int l = a->num, r = b->num;
	Int bd = {b->den, 0}, ad = {a->den, 0};
	if (!mag_is_one(&b->den))
		l = int_mul(c, &l, &bd);
	if (!mag_is_one(&a->den))
		r = int_mul(c, &r, &ad);
	return int_cmp(&l, &r);
}

static u64 exact(Ctx *c, u64 op, u64 av, u64 bv) {
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
	case OP_LT: return bits_of(exact_cmp(c, &a, &b) < 0);
	case OP_LE: return bits_of(exact_cmp(c, &a, &b) <= 0);
	case OP_GT: return bits_of(exact_cmp(c, &a, &b) > 0);
	case OP_GE: return bits_of(exact_cmp(c, &a, &b) >= 0);
	case OP_EQ: return bits_of(exact_cmp(c, &a, &b) == 0);
	case OP_NE: return bits_of(exact_cmp(c, &a, &b) != 0);
	}
	return ERR_ARG;
}

static u64 rounding(Ctx *c, u64 op, u64 v) {
	if (!is_exact(v)) {
		double x = double_of(v);
		if (x != x)
			return v;
		switch (op) {
		case OP_FLOOR: return bits_of(floor_d(x));
		case OP_CEIL: return bits_of(neg_d(floor_d(neg_d(x))));
		case OP_ROUND: return bits_of(x < 0 ? neg_d(floor_d(0.5 - x)) : floor_d(x + 0.5));
		case OP_TRUNC: return bits_of(trunc_d(x));
		case OP_ABS: return bits_of(x < 0 ? neg_d(x) : x);
		}
		return ERR_ARG;
	}
	Rat a;
	load(v, &a);
	if (op == OP_ABS) {
		a.num.neg = 0;
		return encode(c, a.num, a.den);
	}
	if (mag_is_one(&a.den))
		return v;
	Int q, r;
	Int num = a.num;
	if (op == OP_ROUND) {
		Mag twice = mag_shl(c, &num.m, 1);
		Mag d2 = mag_shl(c, &a.den, 1);
		Int t = {mag_add(c, &twice, &a.den), 0};
		int_divmod(c, &t, &d2, &q, &r);
		q.neg = q.m.n ? num.neg : 0;
		return encode_int(c, &q);
	}
	int_divmod(c, &num, &a.den, &q, &r);
	Int one = {mag_from_u64(c, 1), 0};
	if (op == OP_FLOOR && num.neg) {
		Int m1 = int_neg(&one);
		q = int_add(c, &q, &m1);
	} else if (op == OP_CEIL && !num.neg) {
		q = int_add(c, &q, &one);
	}
	return encode_int(c, &q);
}

// Formatting

typedef struct { char *p; u64 n; } Buf;

static void put(Buf *b, char ch) { b->p[b->n++] = ch; }
static void puts_(Buf *b, const char *s) { while (*s) put(b, *s++); }

static void put_u64(Buf *b, u64 v) {
	char tmp[24];
	int n = 0;
	do {
		tmp[n++] = (char)('0' + v % 10);
		v /= 10;
	} while (v);
	while (n)
		put(b, tmp[--n]);
}

static void put_mag(Ctx *c, Buf *b, const Mag *a) {
	if (!a->n) {
		put(b, '0');
		return;
	}
	u64 cap = a->n * 20 + 1;
	u64 *chunks = new_limbs(c, cap);
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
		for (int i = 0; i < 19; i++)
			put(b, tmp[i]);
	}
}

static void put_double(Buf *b, double x) {
	if (x != x) {
		puts_(b, "nan");
		return;
	}
	if (x < 0) {
		put(b, '-');
		x = neg_d(x);
	}
	if (x == double_of(0x7FF0000000000000ull)) {
		puts_(b, "inf");
		return;
	}
	if (x >= 9223372036854775808.0 || (x != 0 && x < 1e-4)) {
		int e = 0;
		while (x >= 10.0) { x /= 10.0; e++; }
		while (x < 1.0) { x *= 10.0; e--; }
		u64 m = (u64)(x * 1e6 + 0.5);
		if (m >= 10000000) {
			m = 1000000;
			e++;
		}
		char dig[7];
		for (int i = 6; i >= 0; i--) {
			dig[i] = (char)('0' + m % 10);
			m /= 10;
		}
		int last = 6;
		while (last > 0 && dig[last] == '0')
			last--;
		put(b, dig[0]);
		if (last > 0) {
			put(b, '.');
			for (int i = 1; i <= last; i++)
				put(b, dig[i]);
		}
		put(b, 'e');
		put(b, e < 0 ? '-' : '+');
		if (e < 0)
			e = -e;
		if (e < 10)
			put(b, '0');
		put_u64(b, (u64)e);
		return;
	}
	u64 ip = (u64)x;
	double frac = x - (double)(i64)ip;
	u64 f = (u64)(frac * 1e6 + 0.5);
	if (f >= 1000000) {
		ip++;
		f -= 1000000;
	}
	put_u64(b, ip);
	if (f) {
		char dig[6];
		for (int i = 5; i >= 0; i--) {
			dig[i] = (char)('0' + f % 10);
			f /= 10;
		}
		int last = 5;
		while (dig[last] == '0')
			last--;
		put(b, '.');
		for (int i = 0; i <= last; i++)
			put(b, dig[i]);
	}
}

// Exact decimal if the denominator is 2^i 5^j, otherwise num/den.
static void put_rat(Ctx *c, Buf *b, Rat *r) {
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
	Buf fb = {alloc_(c, k + 32), 0};
	put_mag(c, &fb, &fp);
	for (u64 i = fb.n; i < k; i++)
		put(b, '0');
	while (fb.n && fb.p[fb.n - 1] == '0')
		fb.n--;
	for (u64 i = 0; i < fb.n; i++)
		put(b, fb.p[i]);
}

static u64 to_string(Ctx *c, u64 v) {
	u64 cap = 64;
	Rat r;
	int ex = tag_of(v) == TAG_BIG || tag_of(v) == TAG_RAT;
	if (ex) {
		load(v, &r);
		cap += (r.num.m.n + r.den.n) * 40;
		if (!mag_is_one(&r.den))
			cap += mag_bitlen(&r.den) + 8;
	}
	Buf b = {alloc_(c, cap), 0};
	if (!ex)
		put_double(&b, double_of(v));
	else if (mag_is_one(&r.den)) {
		if (r.num.neg)
			put(&b, '-');
		put_mag(c, &b, &r.num.m);
	} else
		put_rat(c, &b, &r);
	u64 *s = alloc_(c, 8 + b.n * 16);
	s[0] = bits_of((double)(i64)b.n);
	for (u64 i = 0; i < b.n; i++) {
		s[1 + 2 * i] = i;
		s[2 + 2 * i] = bits_of((double)(i64)(unsigned char)b.p[i]);
	}
	return (u64)s;
}

static u64 to_i64(Ctx *c, u64 v) {
	if (!is_exact(v)) {
		double x = double_of(v);
		if (x != x)
			return 0;
		if (x >= 9223372036854775808.0)
			return x < 18446744073709551616.0 ? (u64)x : ~0ull;
		if (x < -9223372036854775808.0)
			return SIGN_BIT;
		return (u64)(i64)x;
	}
	Rat a;
	load(v, &a);
	Int q = a.num, r;
	if (!mag_is_one(&a.den))
		int_divmod(c, &a.num, &a.den, &q, &r);
	u64 m = q.m.n ? q.m.d[0] : 0;
	return q.neg ? 0 - m : m;
}

u64 tim_num(u64 op, u64 a, u64 b, alloc_fn alloc, void *ctx) {
	Ctx c = {alloc, ctx};
	switch (op) {
	case OP_STR: return to_string(&c, a);
	case OP_FLOAT: return bits_of(to_double(&c, a));
	case OP_TO_I64: return to_i64(&c, a);
	case OP_FROM_I64:
	case OP_FROM_U64: {
		int neg = op == OP_FROM_I64 && (i64)a < 0;
		Int v = {mag_from_u64(&c, neg ? 0 - a : a), neg};
		return encode_int(&c, &v);
	}
	case OP_EXACT:
		return bits_of((double)is_exact(a));
	case OP_FLOOR:
	case OP_CEIL:
	case OP_ROUND:
	case OP_TRUNC:
	case OP_ABS:
		return rounding(&c, op, a);
	}
	if (is_nan(a) || is_nan(b)) {
		if (op == OP_NE)
			return bits_of(1.0);
		if (op >= OP_LT && op <= OP_EQ)
			return 0;
		return is_nan(a) ? a : b;
	}
	if (is_exact(a) && is_exact(b))
		return exact(&c, op, a, b);
	return inexact(&c, op, a, b);
}
