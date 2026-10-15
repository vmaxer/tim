// Tim container runtime: strings, lists and maps, shared by every backend.
// Included by num.c so both live in one blob behind tim_num.
//
// A container is a pointer whose bits read as a positive denormal, so it never
// collides with a number. Layout (8-byte words, every key and count a float64):
//   string, list: [tag][count] [key 0][value 0] ... with key i == i
//   map:          [tag][count] [key 0][value 0] ... [name 0] ... [name count-1]
// Tags: 1.0 string, 2.0 list, 3.0 map. String values are codepoints. Map keys
// are numbers; a string key is stored as its symbolic hash with the string kept
// as the entry's name (0 for numeric keys).

enum {
	V_STR = 64, V_LEN, V_INDEX, V_EQ, V_NE, V_ADD, V_APPEND, V_GROW, V_CONS,
	V_HEAD, V_TAIL, V_POP, V_SLICE, V_REPEAT, V_RUNES, V_BYTES, V_UTF8,
	V_SET, V_IN, V_KEYAT, V_ERROR, V_ERRSTR, V_KIND, V_TRUTHY, V_KEYS,
	V_VALUES, V_FROM_UTF8, V_RANGE, V_POPCOUNT, V_CLZ, V_CTZ,
	V_SIN, V_COS, V_TAN, V_ASIN, V_ACOS, V_ATAN, V_ATAN2, V_EXP, V_LOG, V_LOG10,
	V_CMP,
};

#define T_STR 1
#define T_LIST 2
#define T_MAP 3
#define T_FUNC 4
#define ABSENT 0x7FFC0000DEAD0001ull
#define MAX_DEPTH 64

static inline u64 dnum(i64 i) { return bits_of((double)i); }
static inline int is_box(u64 v) { return v != 0 && (v >> 52) == 0; }
static inline u64 *cb(u64 v) { return (u64 *)v; }
static inline int raw_tag(u64 v) { return is_box(v) ? (int)double_of(cb(v)[0]) : 0; }
static inline int ctag(u64 v) {
	int t = raw_tag(v);
	return t >= T_STR && t <= T_MAP ? t : 0;
}
static inline u64 clen(u64 v) { return (u64)(i64)double_of(cb(v)[1]); }
static inline u64 ckey(u64 v, u64 i) { return cb(v)[2 + 2 * i]; }
static inline u64 cval(u64 v, u64 i) { return cb(v)[3 + 2 * i]; }
static inline u64 cname(u64 v, u64 i) { return cb(v)[2 + 2 * clen(v) + i]; }

static u64 *new_seq(Ctx *c, int tag, u64 n, u64 cap) {
	u64 *b = alloc_(c, 16 + 16 * (cap ? cap : 1));
	b[0] = dnum(tag);
	b[1] = dnum((i64)n);
	return b;
}

static void seq_set(u64 *b, u64 i, u64 v) {
	b[2 + 2 * i] = dnum((i64)i);
	b[3 + 2 * i] = v;
}

static u64 *new_map(Ctx *c, u64 n) {
	u64 *b = alloc_(c, 16 + 24 * (n ? n : 1));
	b[0] = dnum(T_MAP);
	b[1] = dnum((i64)n);
	return b;
}

typedef struct { Ctx *c; u64 *b; u64 n, cap; int tag; } Seq;

static void seq_init(Seq *s, Ctx *c, int tag, u64 cap) {
	s->c = c;
	s->tag = tag;
	s->n = 0;
	s->cap = cap < 8 ? 8 : cap;
	s->b = new_seq(c, tag, 0, s->cap);
}

static void seq_push(Seq *s, u64 v) {
	if (s->n == s->cap) {
		u64 *nb = new_seq(s->c, s->tag, 0, s->cap * 2);
		memcpy(nb + 2, s->b + 2, s->n * 16);
		s->b = nb;
		s->cap *= 2;
	}
	seq_set(s->b, s->n++, v);
}

static void seq_ascii(Seq *s, const char *p) {
	while (*p)
		seq_push(s, dnum((unsigned char)*p++));
}

static u64 seq_done(Seq *s) {
	s->b[1] = dnum((i64)s->n);
	return (u64)s->b;
}

static u64 empty_list(Ctx *c) { return (u64)new_seq(c, T_LIST, 0, 0); }

static u64 list_of2(Ctx *c, u64 a, u64 b) {
	u64 *l = new_seq(c, T_LIST, 2, 2);
	seq_set(l, 0, a);
	seq_set(l, 1, b);
	return (u64)l;
}

static u64 v_index(Ctx *c, u64 v, u64 k);

// as_index reads v as an integer index; 0 when it is not one.
static int as_index(u64 v, i64 *out) {
	if (is_box(v) || tag_of(v) == TAG_BIG || tag_of(v) == TAG_RAT)
		return 0;
	double d = double_of(v);
	if (!(d > -TWO53 && d < TWO53))
		return 0;
	i64 i = (i64)d;
	if ((double)i != d)
		return 0;
	*out = i;
	return 1;
}

static u64 hash_str(u64 s) {
	u32 h = 2166136261u;
	for (u64 i = 0, n = clen(s); i < n; i++) {
		h ^= (u32)(i64)double_of(cval(s, i));
		h *= 16777619u;
	}
	return dnum((i64)((h & 0x3FFFFFFFu) | 0x40000000u));
}

// hash_ascii is hash_str for a C string: the key of a named map field.
static u64 hash_ascii(const char *p) {
	u32 h = 2166136261u;
	while (*p) {
		h ^= (unsigned char)*p++;
		h *= 16777619u;
	}
	return dnum((i64)((h & 0x3FFFFFFFu) | 0x40000000u));
}

static u64 map_key(u64 k) { return ctag(k) == T_STR ? hash_str(k) : k; }

static int num_eq(Ctx *c, u64 a, u64 b) {
	if (a == b)
		return !is_nan(a);
	if (is_nan(a) || is_nan(b))
		return 0;
	if (is_exact(a) && is_exact(b))
		return exact(c, OP_EQ, a, b) == bits_of(1.0);
	return double_of(a) == double_of(b) || to_double(c, a) == to_double(c, b);
}

static i64 map_find(Ctx *c, u64 m, u64 key) {
	for (u64 i = 0, n = clen(m); i < n; i++)
		if (ckey(m, i) == key || num_eq(c, ckey(m, i), key))
			return (i64)i;
	return -1;
}

static int deep_eq(Ctx *c, u64 a, u64 b, int depth) {
	if (a == b)
		return !is_nan(a);
	int ta = ctag(a), tb = ctag(b);
	if (!ta && !tb)
		return num_eq(c, a, b);
	if (ta != tb || clen(a) != clen(b) || depth > MAX_DEPTH)
		return 0;
	u64 n = clen(a);
	if (ta == T_MAP) {
		for (u64 i = 0; i < n; i++) {
			i64 j = map_find(c, b, ckey(a, i));
			if (j < 0 || !deep_eq(c, cval(a, i), cval(b, (u64)j), depth + 1))
				return 0;
		}
		return 1;
	}
	for (u64 i = 0; i < n; i++)
		if (!deep_eq(c, cval(a, i), cval(b, i), depth + 1))
			return 0;
	return 1;
}

// cmp_values orders values: numbers numerically, strings and lists element
// by element (shorter first on a tie), and different kinds by kind.
static int cmp_values(Ctx *c, u64 a, u64 b, int depth) {
	int ta = ctag(a), tb = ctag(b);
	if (!ta && !tb) {
		if (num_eq(c, a, b))
			return 0;
		return tim_num(OP_LT, a, b, c->fn, c->ctx) == bits_of(1.0) ? -1 : 1;
	}
	if (ta != tb)
		return ta < tb ? -1 : 1;
	if (ta == T_MAP || depth > MAX_DEPTH)
		return a < b ? -1 : a > b;
	u64 na = clen(a), nb = clen(b);
	for (u64 i = 0; i < na && i < nb; i++) {
		int r = cmp_values(c, cval(a, i), cval(b, i), depth + 1);
		if (r)
			return r;
	}
	return na < nb ? -1 : na > nb;
}

static void fmt_value(Seq *s, u64 v, int depth);

static void fmt_str(Seq *s, u64 str) {
	for (u64 i = 0, n = clen(str); i < n; i++)
		seq_push(s, cval(str, i));
}

// is_fn recognizes closures: tagged [4.0][code]... objects, and untagged
// [code][environment] pairs whose environment is NULL or follows the pair.
static int is_fn(u64 v) {
	if (!is_box(v))
		return 0;
	if (raw_tag(v) == T_FUNC)
		return 1;
	u64 code = cb(v)[0], env = cb(v)[1];
	return code && (code >> 52) == 0 && (env == 0 || env == v + 16);
}

static void fmt_value(Seq *s, u64 v, int depth) {
	int t = ctag(v);
	if (!t && is_fn(v)) {
		seq_ascii(s, "<function>");
		return;
	}
	if (!t) {
		fmt_str(s, to_string(s->c, v));
		return;
	}
	if (t == T_STR) {
		fmt_str(s, v);
		return;
	}
	if (depth > MAX_DEPTH) {
		seq_ascii(s, "...");
		return;
	}
	seq_push(s, dnum(t == T_MAP ? '{' : '['));
	for (u64 i = 0, n = clen(v); i < n; i++) {
		if (i)
			seq_ascii(s, ", ");
		if (t == T_MAP) {
			u64 name = cname(v, i);
			if (ctag(name) == T_STR)
				fmt_str(s, name);
			else
				fmt_value(s, ckey(v, i), depth + 1);
			seq_ascii(s, ": ");
		}
		fmt_value(s, cval(v, i), depth + 1);
	}
	seq_push(s, dnum(t == T_MAP ? '}' : ']'));
}

static u64 v_str(Ctx *c, u64 v) {
	int t = ctag(v);
	if (t == T_STR)
		return v;
	if (!t && !is_fn(v))
		return to_string(c, v);
	Seq s;
	seq_init(&s, c, T_STR, 16);
	fmt_value(&s, v, 0);
	return seq_done(&s);
}

static u64 utf8_put(unsigned char *p, u64 r) {
	if (r > 0x10FFFF || (r >= 0xD800 && r <= 0xDFFF))
		r = 0xFFFD;
	if (r < 0x80) {
		p[0] = (unsigned char)r;
		return 1;
	}
	if (r < 0x800) {
		p[0] = (unsigned char)(0xC0 | r >> 6);
		p[1] = (unsigned char)(0x80 | (r & 0x3F));
		return 2;
	}
	if (r < 0x10000) {
		p[0] = (unsigned char)(0xE0 | r >> 12);
		p[1] = (unsigned char)(0x80 | (r >> 6 & 0x3F));
		p[2] = (unsigned char)(0x80 | (r & 0x3F));
		return 3;
	}
	p[0] = (unsigned char)(0xF0 | r >> 18);
	p[1] = (unsigned char)(0x80 | (r >> 12 & 0x3F));
	p[2] = (unsigned char)(0x80 | (r >> 6 & 0x3F));
	p[3] = (unsigned char)(0x80 | (r & 0x3F));
	return 4;
}

static u64 rune_at(u64 s, u64 i) {
	double d = double_of(cval(s, i));
	return d >= 0 && d < 4294967296.0 ? (u64)d : 0xFFFD;
}

// v_utf8 renders any value as [byte count][bytes...], ready for write(2).
static u64 v_utf8(Ctx *c, u64 v) {
	u64 s = v_str(c, v), n = clen(s);
	u64 *out = alloc_(c, 8 + 4 * n + 1);
	unsigned char *p = (unsigned char *)(out + 1);
	u64 k = 0;
	for (u64 i = 0; i < n; i++)
		k += utf8_put(p + k, rune_at(s, i));
	out[0] = k;
	return (u64)out;
}

// from_utf8 decodes a NUL-terminated UTF-8 string.
static u64 from_utf8(Ctx *c, const unsigned char *p) {
	u64 n = 0;
	while (p[n])
		n++;
	u64 *s = new_seq(c, T_STR, 0, n);
	u64 k = 0;
	for (u64 i = 0; i < n;) {
		u64 r = p[i], len = 1;
		if (r >= 0xF0 && i + 3 < n) {
			r = (r & 7) << 18 | (u64)(p[i + 1] & 0x3F) << 12 | (u64)(p[i + 2] & 0x3F) << 6 | (p[i + 3] & 0x3F);
			len = 4;
		} else if (r >= 0xE0 && i + 2 < n) {
			r = (r & 15) << 12 | (u64)(p[i + 1] & 0x3F) << 6 | (p[i + 2] & 0x3F);
			len = 3;
		} else if (r >= 0xC0 && i + 1 < n) {
			r = (r & 31) << 6 | (p[i + 1] & 0x3F);
			len = 2;
		}
		seq_set(s, k++, dnum((i64)r));
		i += len;
	}
	s[1] = dnum((i64)k);
	return (u64)s;
}

static u64 seq_copy(Ctx *c, u64 v, u64 from, u64 to, u64 extra, u64 cap) {
	u64 n = to - from;
	u64 *b = new_seq(c, ctag(v), n, cap > n + extra ? cap : n + extra);
	for (u64 i = 0; i < n; i++)
		seq_set(b, i, cval(v, from + i));
	return (u64)b;
}

static u64 concat(Ctx *c, u64 a, u64 b, int tag) {
	u64 na = clen(a), nb = clen(b);
	u64 *r = new_seq(c, tag, na + nb, na + nb);
	for (u64 i = 0; i < na; i++)
		seq_set(r, i, cval(a, i));
	for (u64 i = 0; i < nb; i++)
		seq_set(r, na + i, cval(b, i));
	return (u64)r;
}

static u64 map_with(Ctx *c, u64 m, u64 key, u64 name, u64 val) {
	u64 n = ctag(m) == T_MAP ? clen(m) : 0;
	u64 *r = new_map(c, n + 1);
	for (u64 i = 0; i < n; i++) {
		r[2 + 2 * i] = ckey(m, i);
		r[3 + 2 * i] = cval(m, i);
		r[2 + 2 * (n + 1) + i] = cname(m, i);
	}
	r[2 + 2 * n] = key;
	r[3 + 2 * n] = val;
	r[2 + 2 * (n + 1) + n] = name;
	return (u64)r;
}

static u64 v_append(Ctx *c, u64 l, u64 x, u64 cap) {
	int t = ctag(l);
	if (t == T_STR) {
		if (ctag(x) == T_STR)
			return concat(c, l, x, T_STR);
		if (!is_box(x))
			t = T_STR;
	} else if (t != T_LIST) {
		l = empty_list(c);
		t = T_LIST;
	}
	u64 n = clen(l);
	u64 *b = (u64 *)seq_copy(c, l, 0, n, 1, cap);
	b[0] = dnum(t);
	seq_set(b, n, x);
	b[1] = dnum((i64)n + 1);
	return (u64)b;
}

static u64 pow2_at_least(u64 n) {
	u64 p = 4;
	while (p < n)
		p <<= 1;
	return p;
}

static u64 v_add(Ctx *c, u64 a, u64 b) {
	int ta = ctag(a), tb = ctag(b);
	if (!ta && !tb)
		return tim_num(OP_ADD, a, b, c->fn, c->ctx);
	if (ta == T_STR || tb == T_STR)
		return concat(c, v_str(c, a), v_str(c, b), T_STR);
	if (ta == T_LIST && tb == T_LIST)
		return concat(c, a, b, T_LIST);
	if (ta == T_LIST)
		return v_append(c, a, b, 0);
	if (tb == T_LIST) {
		u64 n = clen(b);
		u64 *r = new_seq(c, T_LIST, n + 1, n + 1);
		seq_set(r, 0, a);
		for (u64 i = 0; i < n; i++)
			seq_set(r, i + 1, cval(b, i));
		return (u64)r;
	}
	if (ta == T_MAP && tb == T_MAP) {
		u64 r = a;
		for (u64 i = 0, n = clen(b); i < n; i++) {
			i64 j = map_find(c, r, ckey(b, i));
			if (j >= 0) {
				if (r == a) {
					u64 *cp = new_map(c, clen(a));
					memcpy(cp + 2, cb(a) + 2, clen(a) * 24);
					r = (u64)cp;
				}
				cb(r)[3 + 2 * j] = cval(b, i);
			} else
				r = map_with(c, r, ckey(b, i), cname(b, i), cval(b, i));
		}
		return r;
	}
	return ta ? a : b;
}

static u64 v_index(Ctx *c, u64 v, u64 k) {
	int t = ctag(v);
	if (t == T_MAP) {
		i64 j = map_find(c, v, map_key(k));
		return j < 0 ? 0 : cval(v, (u64)j);
	}
	i64 i;
	if (!t || !as_index(k, &i))
		return 0;
	return i >= 0 && (u64)i < clen(v) ? cval(v, (u64)i) : 0;
}

static u64 v_set(Ctx *c, u64 v, const u64 *kv) {
	int t = ctag(v);
	u64 k = kv[0], x = kv[1];
	if (t == T_MAP || !t) {
		u64 key = map_key(k);
		i64 j = t ? map_find(c, v, key) : -1;
		if (j >= 0) {
			cb(v)[3 + 2 * j] = x;
			return v;
		}
		return map_with(c, v, key, ctag(k) == T_STR ? k : 0, x);
	}
	i64 i;
	if (!as_index(k, &i))
		return v;
	if (i >= 0 && (u64)i < clen(v))
		cb(v)[3 + 2 * i] = x;
	else if ((u64)i == clen(v))
		return v_append(c, v, x, 0);
	return v;
}

static i64 clamp_index(u64 v, i64 n, i64 dflt, i64 lo, i64 hi) {
	i64 i;
	if (v == ABSENT || !as_index(v, &i))
		return dflt;
	if (i < 0)
		i += n;
	return i < lo ? lo : i > hi ? hi : i;
}

static u64 v_slice(Ctx *c, u64 v, const u64 *a) {
	int t = ctag(v);
	if (t != T_STR && t != T_LIST)
		return empty_list(c);
	i64 n = (i64)clen(v), step = 1;
	if (a[2] != ABSENT && (!as_index(a[2], &step) || step == 0))
		step = 1;
	i64 start, stop;
	if (step > 0) {
		start = clamp_index(a[0], n, 0, 0, n);
		stop = clamp_index(a[1], n, n, 0, n);
	} else {
		start = clamp_index(a[0], n, n - 1, -1, n - 1);
		stop = clamp_index(a[1], n, -1, -1, n - 1);
		if (a[1] != ABSENT) {
			i64 e;
			if (as_index(a[1], &e) && e < 0 && e + n < 0)
				stop = -1;
		}
	}
	u64 count = 0;
	if (step > 0 && stop > start)
		count = (u64)((stop - start + step - 1) / step);
	else if (step < 0 && start > stop)
		count = (u64)((start - stop - step - 1) / -step);
	u64 *b = new_seq(c, t, count, count);
	for (u64 i = 0; i < count; i++)
		seq_set(b, i, cval(v, (u64)(start + (i64)i * step)));
	return (u64)b;
}

static u64 v_repeat(Ctx *c, u64 v, u64 times) {
	int t = ctag(v);
	i64 k;
	if (t != T_STR && t != T_LIST)
		return tim_num(OP_MUL, v, times, c->fn, c->ctx);
	if (!as_index(times, &k) || k < 0)
		k = 0;
	u64 n = clen(v);
	u64 *b = new_seq(c, t, n * (u64)k, n * (u64)k);
	for (u64 r = 0; r < (u64)k; r++)
		for (u64 i = 0; i < n; i++)
			seq_set(b, r * n + i, cval(v, i));
	return (u64)b;
}

static u64 v_bytes(Ctx *c, u64 s) {
	u64 *buf = (u64 *)v_utf8(c, s);
	const unsigned char *p = (const unsigned char *)(buf + 1);
	u64 *b = new_seq(c, T_LIST, buf[0], buf[0]);
	for (u64 i = 0; i < buf[0]; i++)
		seq_set(b, i, dnum(p[i]));
	return (u64)b;
}

static u64 v_in(Ctx *c, u64 x, u64 v) {
	int t = ctag(v);
	if (t == T_MAP)
		return map_find(c, v, map_key(x)) >= 0;
	if (!t)
		return num_eq(c, x, v);
	if (t == T_STR && ctag(x) == T_STR) {
		u64 n = clen(v), m = clen(x);
		for (u64 i = 0; i + m <= n; i++) {
			u64 j = 0;
			while (j < m && cval(v, i + j) == cval(x, j))
				j++;
			if (j == m)
				return 1;
		}
		return 0;
	}
	for (u64 i = 0, n = clen(v); i < n; i++)
		if (deep_eq(c, x, cval(v, i), 0))
			return 1;
	return 0;
}

static u64 v_error(u64 s) {
	u64 code = 0;
	if (ctag(s) == T_STR)
		for (u64 i = 0; i < 3 && i < clen(s); i++)
			code |= (rune_at(s, i) & 0xFF) << (24 - 8 * i);
	return 0x7FF8000000000000ull | code;
}

static u64 v_errstr(Ctx *c, u64 v) {
	Seq s;
	seq_init(&s, c, T_STR, 4);
	if (is_nan(v) && !is_box(v))
		for (int sh = 24; sh >= 0; sh -= 8) {
			u64 ch = v >> sh & 0xFF;
			if (!ch)
				break;
			seq_push(&s, dnum((i64)ch));
		}
	return seq_done(&s);
}

static u64 v_truthy(u64 v) {
	if (is_box(v))
		return 1;
	if (tag_of(v) == TAG_BIG || tag_of(v) == TAG_RAT)
		return 1;
	double d = double_of(v);
	return d == d ? d != 0 : 0;
}

static u64 v_keys(Ctx *c, u64 v, int values) {
	int t = ctag(v);
	u64 n = t ? clen(v) : 0;
	u64 *b = new_seq(c, T_LIST, n, n);
	for (u64 i = 0; i < n; i++) {
		u64 x;
		if (values)
			x = cval(v, i);
		else if (t != T_MAP)
			x = dnum((i64)i);
		else
			x = ctag(cname(v, i)) == T_STR ? cname(v, i) : ckey(v, i);
		seq_set(b, i, x);
	}
	return (u64)b;
}

static u64 v_range(Ctx *c, u64 start, const u64 *a) {
	double lo = to_double(c, start), hi = to_double(c, a[0]), step = to_double(c, a[1]);
	int incl = a[2] != 0;
	Seq s;
	seq_init(&s, c, T_LIST, 8);
	if (!(step != 0) || lo != lo || hi != hi)
		return seq_done(&s);
	for (double x = lo; step > 0 ? (incl ? x <= hi : x < hi) : (incl ? x >= hi : x > hi); x += step)
		seq_push(&s, bits_of(x));
	return seq_done(&s);
}

static u64 v_bits(Ctx *c, u64 op, u64 v) {
	u64 x = tim_num(OP_TO_I64, v, 0, c->fn, c->ctx), n = 0;
	if (op == V_POPCOUNT)
		for (; x; x &= x - 1)
			n++;
	else if (op == V_CLZ) {
		if (!x)
			return dnum(64);
		while (!(x & SIGN_BIT)) {
			x <<= 1;
			n++;
		}
	} else {
		if (!x)
			return dnum(64);
		while (!(x & 1)) {
			x >>= 1;
			n++;
		}
	}
	return dnum((i64)n);
}

#if defined(__x86_64__)
static u64 v_math(Ctx *c, u64 op, u64 a, u64 b) {
	(void)c, (void)op, (void)a, (void)b;
	return 0x7FF8000000000000ull;
}
#else
#define PI 3.14159265358979323846
#define PIO2_1 1.57079632673412561417e+00
#define PIO2_2 6.07710050650619224932e-11
#define PIO2_3 2.02226624879595063154e-21

static double sqrt_d(double x) {
	double r;
#if defined(__aarch64__)
	__asm__("fsqrt %d0, %d1" : "=w"(r) : "w"(x));
#else
	__asm__("fsqrt.d %0, %1" : "=f"(r) : "f"(x));
#endif
	return r;
}

static double sin_poly(double x) {
	double x2 = x * x, term = x, sum = x;
	for (int k = 1; k < 12; k++) {
		term *= -x2 / (double)((2 * k) * (2 * k + 1));
		sum += term;
	}
	return sum;
}

static double cos_poly(double x) {
	double x2 = x * x, term = 1.0, sum = 1.0;
	for (int k = 1; k < 12; k++) {
		term *= -x2 / (double)((2 * k - 1) * (2 * k));
		sum += term;
	}
	return sum;
}

static double sincos_d(double x, int want_cos) {
	if (x != x || x - x != 0)
		return double_of(0x7FF8000000000000ull);
	double kd = floor_d(x * 0.63661977236758134308 + 0.5);
	double r = ((x - kd * PIO2_1) - kd * PIO2_2) - kd * PIO2_3;
	i64 q = (i64)kd + want_cos;
	switch (q & 3) {
	case 0: return sin_poly(r);
	case 1: return cos_poly(r);
	case 2: return -sin_poly(r);
	default: return -cos_poly(r);
	}
}

static double atan_d(double x) {
	if (x != x)
		return x;
	int neg = x < 0;
	if (neg)
		x = -x;
	int inv = x > 1;
	if (inv)
		x = 1 / x;
	int halvings = 0;
	while (x > 0.1) {
		x = x / (1.0 + sqrt_d(1.0 + x * x));
		halvings++;
	}
	double x2 = x * x, term = x, sum = x;
	for (int k = 1; k < 20; k++) {
		term *= -x2;
		sum += term / (double)(2 * k + 1);
	}
	sum *= (double)(1 << halvings);
	if (inv)
		sum = PI / 2 - sum;
	return neg ? -sum : sum;
}

static double atan2_d(double y, double x) {
	if (x > 0)
		return atan_d(y / x);
	if (x < 0)
		return y >= 0 ? atan_d(y / x) + PI : atan_d(y / x) - PI;
	if (y > 0)
		return PI / 2;
	if (y < 0)
		return -PI / 2;
	return 0;
}

static u64 v_math(Ctx *c, u64 op, u64 a, u64 b) {
	double x = to_double(c, a), r;
	switch (op) {
	case V_SIN: r = sincos_d(x, 0); break;
	case V_COS: r = sincos_d(x, 1); break;
	case V_TAN: r = sincos_d(x, 0) / sincos_d(x, 1); break;
	case V_ASIN: r = x < -1 || x > 1 ? double_of(0x7FF8000000000000ull) : atan2_d(x, sqrt_d(1 - x * x)); break;
	case V_ACOS: r = x < -1 || x > 1 ? double_of(0x7FF8000000000000ull) : PI / 2 - atan2_d(x, sqrt_d(1 - x * x)); break;
	case V_ATAN: r = atan_d(x); break;
	case V_ATAN2: r = atan2_d(x, to_double(c, b)); break;
	case V_EXP: r = exp_d(x); break;
	case V_LOG: r = x > 0 ? log_d(x) : x == 0 ? double_of(0xFFF0000000000000ull) : double_of(0x7FF8000000000000ull); break;
	default: r = x > 0 ? log_d(x) / 2.30258509299404568402 : x == 0 ? double_of(0xFFF0000000000000ull) : double_of(0x7FF8000000000000ull); break;
	}
	return bits_of(r);
}
#endif

static u64 tim_val(Ctx *c, u64 op, u64 a, u64 b) {
	switch (op) {
	case V_STR:
		return v_str(c, a);
	case V_LEN:
		return dnum(ctag(a) ? (i64)clen(a) : 1);
	case V_INDEX:
		return v_index(c, a, b);
	case V_EQ:
		return dnum(deep_eq(c, a, b, 0));
	case V_NE:
		return dnum(!deep_eq(c, a, b, 0));
	case V_ADD:
		return v_add(c, a, b);
	case V_APPEND:
		return v_append(c, a, b, 0);
	case V_GROW: {
		int t = ctag(a);
		if (t && t != T_LIST)
			return v_append(c, a, b, 0);
		u64 n = t ? clen(a) : 0;
		return v_append(c, a, b, pow2_at_least(n + 1));
	}
	case V_CONS: {
		if (ctag(b) != T_LIST)
			return list_of2(c, a, b);
		return v_add(c, a, b);
	}
	case V_HEAD: {
		int t = ctag(a);
		if (!t)
			return a;
		return clen(a) ? cval(a, 0) : empty_list(c);
	}
	case V_TAIL: {
		int t = ctag(a);
		if (t != T_LIST && t != T_STR)
			return empty_list(c);
		return clen(a) ? seq_copy(c, a, 1, clen(a), 0, 0) : empty_list(c);
	}
	case V_POP: {
		if (ctag(a) != T_LIST || !clen(a))
			return list_of2(c, empty_list(c), 0x7FF8000000000000ull);
		u64 n = clen(a);
		return list_of2(c, seq_copy(c, a, 0, n - 1, 0, 0), cval(a, n - 1));
	}
	case V_SLICE:
		return v_slice(c, a, (const u64 *)b);
	case V_REPEAT:
		return v_repeat(c, a, b);
	case V_RUNES: {
		if (ctag(a) == T_MAP)
			return v_index(c, a, hash_ascii("runes"));
		u64 s = v_str(c, a);
		u64 *r = (u64 *)seq_copy(c, s, 0, clen(s), 0, 0);
		r[0] = dnum(T_LIST);
		return (u64)r;
	}
	case V_BYTES:
		if (ctag(a) == T_MAP)
			return v_index(c, a, hash_ascii("bytes"));
		return v_bytes(c, a);
	case V_UTF8:
		return v_utf8(c, a);
	case V_FROM_UTF8:
		return from_utf8(c, (const unsigned char *)a);
	case V_SET:
		return v_set(c, a, (const u64 *)b);
	case V_IN:
		return dnum((i64)v_in(c, a, b));
	case V_KEYAT: {
		i64 i;
		if (ctag(a) != T_MAP || !as_index(b, &i) || i < 0 || (u64)i >= clen(a))
			return b;
		u64 name = cname(a, (u64)i);
		return ctag(name) == T_STR ? name : ckey(a, (u64)i);
	}
	case V_ERROR:
		return v_error(a);
	case V_ERRSTR:
		return v_errstr(c, a);
	case V_KIND:
		return dnum(ctag(a));
	case V_TRUTHY:
		return dnum((i64)v_truthy(a));
	case V_KEYS:
		return v_keys(c, a, 0);
	case V_VALUES:
		return v_keys(c, a, 1);
	case V_RANGE:
		return v_range(c, a, (const u64 *)b);
	case V_CMP:
		return dnum(cmp_values(c, a, b, 0));
	case V_POPCOUNT:
	case V_CLZ:
	case V_CTZ:
		return v_bits(c, op, a);
	}
	if (op >= V_SIN && op <= V_LOG10)
		return v_math(c, op, a, b);
	return 0;
}
