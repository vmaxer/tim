package main

import (
	"fmt"
	"strings"
	"testing"
)

func lexAll(src string) []Token {
	l := NewLexer(src)
	var toks []Token
	for {
		t := l.NextToken()
		if t.Type == TOKEN_EOF {
			return toks
		}
		toks = append(toks, t)
	}
}

func TestLexerTokens(t *testing.T) {
	tests := []struct {
		src   string
		types []TokenType
		vals  []string
	}{
		{"x &b y", []TokenType{TOKEN_IDENT, TOKEN_AMP_B, TOKEN_IDENT}, nil},
		{"x &bits", []TokenType{TOKEN_IDENT, TOKEN_AMP, TOKEN_IDENT}, []string{"x", "&", "bits"}},
		{"xs |bump", []TokenType{TOKEN_IDENT, TOKEN_PIPE, TOKEN_IDENT}, nil},
		{"x <<= 2", []TokenType{TOKEN_IDENT, TOKEN_SHL_ASSIGN, TOKEN_NUMBER}, nil},
		{"x >>>= 2", []TokenType{TOKEN_IDENT, TOKEN_ROTR_ASSIGN, TOKEN_NUMBER}, nil},
		{"0..<10", []TokenType{TOKEN_NUMBER, TOKEN_DOTDOTLT, TOKEN_NUMBER}, nil},
		{"1..=3", []TokenType{TOKEN_NUMBER, TOKEN_DOTDOT, TOKEN_NUMBER}, nil},
		{"1_000_000 0o17 0b1_01 0xFF 2.5e-3", nil, []string{"1000000", "0o17", "0b101", "0xFF", "2.5e-3"}},
		{"λ größe x2", []TokenType{TOKEN_IDENT, TOKEN_IDENT, TOKEN_IDENT}, []string{"λ", "größe", "x2"}},
		{"break @2", []TokenType{TOKEN_BREAK, TOKEN_AT_LABEL}, []string{"break", "@2"}},
		{"x or! y", []TokenType{TOKEN_IDENT, TOKEN_OR_BANG, TOKEN_IDENT}, nil},
		{"x as! u8", []TokenType{TOKEN_IDENT, TOKEN_AS_BANG, TOKEN_IDENT}, []string{"x", "as!", "u8"}},
		{`"a\\n\t\u{1F600}\x41"`, []TokenType{TOKEN_STRING}, []string{"a\\n\t😀A"}},
		{"a // note\nb", []TokenType{TOKEN_IDENT, TOKEN_NEWLINE, TOKEN_IDENT}, nil},
	}
	for _, tt := range tests {
		toks := lexAll(tt.src)
		for i, want := range tt.types {
			if i >= len(toks) || toks[i].Type != want {
				t.Errorf("%q: token %d is %v, want %v (%v)", tt.src, i, toks[min(i, len(toks)-1)], want, toks)
			}
		}
		for i, want := range tt.vals {
			if i >= len(toks) || toks[i].Value != want {
				t.Errorf("%q: token %d value %q, want %q", tt.src, i, toks[min(i, len(toks)-1)].Value, want)
			}
		}
	}
}

func TestLexerRejects(t *testing.T) {
	for _, src := range []string{"2 ^ 3", "12abc", "0x", "0b2", "_x", "a $ b", "x ¤ y", "`"} {
		l := NewLexer(src)
		for tok := l.NextToken(); tok.Type != TOKEN_EOF; tok = l.NextToken() {
		}
		if len(l.errs.list) == 0 {
			t.Errorf("%q: expected a lexical error", src)
		}
	}
}

func TestGrammarPrograms(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{"chained_comparison", "x := 2\nprintln(1 < x < 3, 1 < x < 2, 0 <= x <= 2 == 1)\n", "1 0 0\n"},
		{"not_binds_loosely", "x := 3\nprintln(not x == 4, not 4 in [1, 2])\n", "1 1\n"},
		{"unary_minus_precedence", "x := 3\nxs := [5, 6]\nprintln(-x ** 2, 2 ** -1, -xs[1])\n", "-9 0.5 -6\n"},
		{"comparison_arms", "f = n -> n {\n    >= 90 => \"A\"\n    in 75..<90 => \"B\"\n    50 => \"fifty\"\n    ~> \"C\"\n}\nprintln(f(95), f(80), f(50), f(10))\n", "A B fifty C\n"},
		{"subject_evaluated_once", "n := 0\nnext = -> {\n    n <- n + 1\n    n\n}\nr = next() { 1 => \"one\" 2 => \"two\" ~> \"other\" }\nprintln(r, n)\n", "one 1\n"},
		{"many_arms", manyArms(14), "v11\n"},
		{"swap", "a := 1\nb := 2\na, b <- b, a\nprintln(a, b)\n", "2 1\n"},
		{"trailing_commas", "f = (a, b) -> a + b\nm := {\n    a: 1,\n    b: 2,\n}\nxs := [\n    1,\n    2,\n]\nprintln(f(1, 2,), m, xs)\n", "3 {a: 1, b: 2} [1, 2]\n"},
		{"return_types", "scale = (v: num, k: num) -> num { v * k }\nfun area(w, h) -> num { w * h }\nprintln(scale(2, 3), area(4, 5))\n", "6 20\n"},
		{"lambda_then_argument", "apply = (f, v) -> f(v)\nprintln(apply((x) -> x * 2, 5))\n", "10\n"},
		{"escapes_once", "println(\"a\\\\nb\")\nprintf(\"x\\\\ty%d\\n\", 3)\n", "a\\nb\nx\\ty3\n"},
		{"string_and_name_keys", "m := {\"a\": 1}\nn := {a: 1}\nprintln(m.a, n[\"a\"])\n", "1 1\n"},
		{"integer_conversions", "n := 300\nbig := 2 ** 70 + 7\nf := 3.7\nprintln(300 as! uint8, -1 as! uint8, 200 as! int8, 3.7 as! int8, -2.5 as! int8, 255 as uint8)\nprintln((n as uint8).error, n as! int8, big as! uint8, (big as int64).error, (n as uint8) or! -1, (f as int8).error, (7 / 2).error)\n", "44 255 -56 4 -3 255\novf 44 7 ovf -1 arg \n"},
		{"float_conversions", "t := 3\nodd := 2 ** 70 + 1\nprintln(1 / t as! float64, (1 / t as float64).error, 1 / 2 as float64, (odd as float64).error, 0.5 as float32, 16777217 as! float32)\nprintln(1 / 3 as! float64 < 0.34, 7 / 2 as! int8, -1 as! uint8 == 255)\n", "0.333333 arg 0.5 arg 0.5 16777216\n1 4 1\n"},
		{"inlined_membership", "c = y -> y in 5..<10\nprintln(c(6), c(10))\n", "1 0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := runTimSource(t, tt.name, tt.src)
			if out != tt.want {
				t.Errorf("got %q, want %q", out, tt.want)
			}
		})
	}
}

func manyArms(n int) string {
	var b strings.Builder
	b.WriteString("x := 11\nr = x {\n")
	for i := range n {
		fmt.Fprintf(&b, "    %d => \"v%d\"\n", i, i)
	}
	b.WriteString("    ~> \"other\"\n}\nprintln(r)\n")
	return b.String()
}

func TestRemovedSyntaxErrors(t *testing.T) {
	tests := []struct{ name, src, msg string }{
		{"caret_power", "x := 2 ^ 3\n", "use ** for powers"},
		{"bang_prefix", "x := 1\ny := !x\n", "use 'not'"},
		{"ret_at", "for i in 0..<3 {\n    i == 1 { ret @ }\n}\n", "'break'"},
		{"arrow_arm", "x := 1\nr = x { 1 -> 2 ~> 3 }\n", "match arms use '=>'"},
		{"prefix_increment", "x := 1\ny := ++x\n", "statement suffix"},
		{"underscore_ident", "_x := 1\n", "start with a letter"},
		{"literal_out_of_range", "x := 300 as uint8\n", "does not fit in uint8"},
		{"negative_unsigned", "x := -1 as uint64\n", "does not fit in uint64"},
		{"literal_fraction", "x := 3.7 as int32\n", "use as! to round it"},
		{"wrap_non_integer", "x := 3 as! string\n", "as! converts to integer and float types"},
		{"literal_inexact_float", "x := 0.1 as float64\n", "0.1 is not exactly a float64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compileErrorTimSource(t, tt.name, tt.src, tt.msg)
		})
	}
}

func TestUnquotedImportSource(t *testing.T) {
	p := NewParser("import github.com/user/geometry@v1.2.0 as geo\nprintln(1)\n")
	prog := p.ParseProgram()
	imp, ok := prog.Statements[0].(*ImportStmt)
	if !ok || imp.URL != "github.com/user/geometry" || imp.Version != "v1.2.0" || imp.Alias != "geo" {
		t.Fatalf("got %#v", prog.Statements[0])
	}
	if len(prog.Statements) != 2 {
		t.Fatalf("expected the statement after the import to parse, got %d statements", len(prog.Statements))
	}
}
