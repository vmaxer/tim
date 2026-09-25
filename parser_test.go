package main

import (
	"fmt"
	"strings"
	"testing"
)

func parseString(t *testing.T, src string) (prog *Program, errMsg string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			errMsg = strings.TrimSpace(strings.TrimPrefix(func() string {
				if e, ok := r.(error); ok {
					return e.Error()
				}
				return ""
			}(), "error: "))
		}
	}()
	p := NewParser(src)
	p.errors.SetSourceCode("")
	var statements []Statement
	p.skipEnds()
	for !p.at(TOKEN_EOF) {
		func() {
			defer func() {
				if r := recover(); r != nil {
					if _, ok := r.(parseBailout); !ok {
						panic(r)
					}
					p.syncToStatement(p.i)
					p.advance()
				}
			}()
			if s := p.statement(); s != nil {
				statements = append(statements, s)
			}
			p.endStatement()
		}()
		p.skipEnds()
	}
	if p.lexErr != nil {
		return nil, p.lexErr.Msg
	}
	if p.errors.HasErrors() {
		return nil, strings.TrimSpace(p.errors.Report(false))
	}
	return &Program{Statements: statements}, ""
}

func TestLexerNewlines(t *testing.T) {
	tests := []struct{ src, want string }{
		{"a = 1\nb = 2", "a = 1 ; b = 2 ;"},
		{"a = 1 +\n  2", "a = 1 + 2 ;"},
		{"f(1,\n  2)", "f ( 1 , 2 ) ;"},
		{"xs\n  |> sum\n  |> println", "xs |> sum |> println ;"},
		{"x = a\n  .b", "x = a . b ;"},
		{"ok = a\n  and b", "ok = a and b ;"},
		{"x = 1 // note\n\n\ny = 2", "x = 1 ; y = 2 ;"},
		{"x = /* a\nb */ 3", "x = 3 ;"},
		{"export *\nx = 1", "export * ; x = 1 ;"},
		{"n = 1_000_000 + 0x_ff", "n = 1000000 + 0xff ;"},
	}
	for _, tt := range tests {
		toks, err := Lex(tt.src)
		if err != nil {
			t.Errorf("Lex(%q): %v", tt.src, err)
			continue
		}
		var parts []string
		for _, tok := range toks[:len(toks)-1] {
			if tok.Type == TOKEN_NEWLINE {
				parts = append(parts, ";")
			} else {
				parts = append(parts, tok.Value)
			}
		}
		if got := strings.Join(parts, " "); got != tt.want {
			t.Errorf("Lex(%q) = %q, want %q", tt.src, got, tt.want)
		}
	}
}

func TestLexerStrings(t *testing.T) {
	toks, err := Lex(`"a\tb\\n\u{e9}\x41\"" f"{x} {{}}"`)
	if err != nil {
		t.Fatal(err)
	}
	if toks[0].Value != "a\tb\\né\x41\"" {
		t.Errorf("string = %q", toks[0].Value)
	}
	if toks[1].Type != TOKEN_FSTRING || toks[1].Value != "{x} {{}}" {
		t.Errorf("f-string = %v %q", toks[1].Type, toks[1].Value)
	}
	for src, want := range map[string]string{
		`"abc`:   "unterminated string literal",
		`"\q"`:   `unknown escape sequence \q`,
		`x = 1a`: `unexpected "a" after number`,
		`x = ¤`:  `unexpected character '¤'`,
		`f(]`:    "unmatched ']'",
	} {
		if _, err := Lex(src); err == nil || !strings.Contains(err.Msg, want) {
			t.Errorf("Lex(%q) error = %v, want %q", src, err, want)
		}
	}
}

func TestParserExpressions(t *testing.T) {
	tests := []struct{ src, want string }{
		{"x = 1 + 2 * 3", "x = (1 + (2 * 3))"},
		{"x = -2 ** 2", "x = (-(2 ** 2))"},
		{"x = 2 ** 3 ** 2", "x = (2 ** (3 ** 2))"},
		{"x = 0 <= i < n", "x = ((0 <= i) and (i < n))"},
		{"x = a in xs and b not in ys", "x = ((a in xs) and (not(b in ys)))"},
		{"x = not a or b", "x = ((nota) or b)"},
		{"x = a | b ^ c & d << 1", "x = (a |b (b ^b (c &b (d <<b 1))))"},
		{"x = ~a", "x = (~ba)"},
		{"x = 1 + 2 as int32", "x = (1 + 2 as int32)"},
		{"x = 0..<n + 1", "x = 0..<(n + 1)"},
		{"x = xs |> map(f) |> sum", "x = sum(map(xs, f))"},
		{"x = a or! b or c", "x = (a or! (b or c))"},
		{"f = x -> x + 1", "f = (x) -> (x + 1)"},
		{"f = (a, b) -> a * b", "f = (a, b) -> (a * b)"},
		{"f(a, b) = a * b", "f = (a, b) -> (a * b)"},
		{"x = xs[1:]", "x = xs[1:]"},
		{"x = #xs + 1", "x = ((#xs) + 1)"},
		{"x = -3", "x = -3"},
		{"x = bit(5, 2)", "x = (5 ?b 2)"},
		{"x = [y * y @ y in ys if y > 1]", "x = { tmp·1 := []; @ y in ys {\n  if (y > 1) { ... }\n}; tmp·1 }"},
	}
	for _, tt := range tests {
		prog, err := parseString(t, tt.src)
		if err != "" {
			t.Errorf("%q: %s", tt.src, err)
			continue
		}
		if got := prog.Statements[0].String(); got != tt.want {
			t.Errorf("%q\n got %s\nwant %s", tt.src, got, tt.want)
		}
	}
}

func TestParserBlocks(t *testing.T) {
	tests := []struct{ src, want string }{
		{"m = {}", "m = {}"},
		{"m = {\"a b\": 1, 2: 3}", `m = {"a b": 1, 2: 3}`},
		{"f = { x: num = 1\n x }", "f = () -> { x = 1; x }"},
		{"f = { println(1) }", "f = () -> { println(1) }"},
		{"s = {\n | x > 0 => 1\n ~> 0\n}", "s = 1 { (x > 0) -> 1 ~> 0 }"},
		{"k = n {\n 0 => \"z\"\n 1..<9 => \"s\"\n ~> \"l\"\n}", `k = n { (n == 0) -> "z" ((n >= 1) and (n < 9)) -> "s" ~> "l" }`},
		{"k = f(n) { 0 => 1 ~> 2 }", "k = { tmp·1 = f(n); tmp·1 { (tmp·1 == 0) -> 1 ~> 2 } }"},
		{"n > 1 { println(n) }", "(n > 1) { -> { println(n) } }"},
		{"ok = x > 0 { => 1 ~> 2 }", "ok = (x > 0) { -> 1 ~> 2 }"},
		{"c(x) = {\n | x < 0 => ret 0\n x\n}", "c = (x) -> { if (x < 0) { ... }; x }"},
		{"v = if a { 1 } elif b { 2 } else { 3 }", "v = 1 { a -> { 1 } b -> { 2 } ~> { 3 } }"},
	}
	for _, tt := range tests {
		prog, err := parseString(t, tt.src)
		if err != "" {
			t.Errorf("%q: %s", tt.src, err)
			continue
		}
		if got := prog.Statements[0].String(); got != tt.want {
			t.Errorf("%q\n got %s\nwant %s", tt.src, got, tt.want)
		}
	}
}

func TestParserStatements(t *testing.T) {
	tests := []struct {
		src  string
		kind string
	}{
		{"x := 1", "*main.AssignStmt"},
		{"x <- 2", "*main.AssignStmt"},
		{"x += 2", "*main.AssignStmt"},
		{"a, b = pair", "*main.MultipleAssignStmt"},
		{"xs[0] <- 1", "*main.MapUpdateStmt"},
		{"buf[4] <- 1 as uint8", "*main.ExpressionStmt"},
		{"p.x <- 1", "*main.FieldUpdateStmt"},
		{"grid[i][j] <- 1", "*main.IndexUpdateStmt"},
		{"@ { }", "*main.WhileStmt"},
		{"@ i in 0..<3 ! 10 { }", "*main.LoopStmt"},
		{"@ n > 0 { }", "*main.WhileStmt"},
		{"if a { } else { }", "*main.IfStmt"},
		{"defer close(f)", "*main.DeferStmt"},
		{"import sdl3 as sdl", "*main.CImportStmt"},
		{"import github.com/u/r@v1.0.0 as r", "*main.ImportStmt"},
		{"cstruct P { x, y: f64 }", "*main.CStructDecl"},
	}
	for _, tt := range tests {
		prog, err := parseString(t, tt.src)
		if err != "" {
			t.Errorf("%q: %s", tt.src, err)
			continue
		}
		if got := typeName(prog.Statements[0]); got != tt.kind {
			t.Errorf("%q parsed as %s, want %s", tt.src, got, tt.kind)
		}
	}
}

func typeName(v any) string { return fmt.Sprintf("%T", v) }

func TestParserErrors(t *testing.T) {
	tests := []struct{ src, want string }{
		{"x = 1 +", "expected an expression after '+'"},
		{"x = (1, 2", "expected ')'"},
		{"break", "'break' outside a loop"},
		{"@ { break @2 }", "there is no loop @2 here"},
		{"x = xs[]", "empty index"},
		{"f = -> 1", "a lambda needs parameters"},
		{"cstruct P { x: bogus }", "unknown field type 'bogus'"},
		{"k = n { 0 => 1\n ~> 2\n ~> 3 }", "only one '~>' arm"},
		{"elif x { }", "'elif' without a preceding 'if'"},
		{"s = f\"{}\"", "empty '{}' in f-string"},
		{"x = 1 2", "unexpected '2' after statement"},
	}
	for _, tt := range tests {
		_, err := parseString(t, tt.src)
		if !strings.Contains(err, tt.want) {
			t.Errorf("%q: error %q, want %q", tt.src, err, tt.want)
		}
	}
}

func TestParserReportsSeveralErrors(t *testing.T) {
	_, err := parseString(t, "x = 1 +\ny = 2\nz = (\n")
	if n := strings.Count(err, "error"); n < 2 {
		t.Errorf("want at least two errors, got %d: %s", n, err)
	}
}
