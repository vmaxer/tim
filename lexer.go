package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TokenType identifies a lexical token (see GRAMMAR.md §2).
type TokenType int

const (
	TOKEN_EOF TokenType = iota
	TOKEN_NEWLINE
	TOKEN_IDENT
	TOKEN_NUMBER
	TOKEN_STRING
	TOKEN_FSTRING

	TOKEN_AND
	TOKEN_ARENA
	TOKEN_AS
	TOKEN_BREAK
	TOKEN_CONTINUE
	TOKEN_CSTRUCT
	TOKEN_DEFER
	TOKEN_ELIF
	TOKEN_ELSE
	TOKEN_ERR
	TOKEN_EXPORT
	TOKEN_IF
	TOKEN_IMPORT
	TOKEN_IN
	TOKEN_INF
	TOKEN_NO
	TOKEN_NOT
	TOKEN_OR
	TOKEN_RET
	TOKEN_UNSAFE
	TOKEN_YES

	TOKEN_PLUS       // +
	TOKEN_MINUS      // -
	TOKEN_STAR       // *
	TOKEN_SLASH      // /
	TOKEN_PERCENT    // %
	TOKEN_POWER      // **
	TOKEN_EQ         // ==
	TOKEN_NE         // !=
	TOKEN_LT         // <
	TOKEN_LE         // <=
	TOKEN_GT         // >
	TOKEN_GE         // >=
	TOKEN_AMP        // &
	TOKEN_PIPE       // |
	TOKEN_CARET      // ^
	TOKEN_TILDE      // ~
	TOKEN_SHL        // <<
	TOKEN_SHR        // >>
	TOKEN_ASSIGN     // =
	TOKEN_DEFINE     // :=
	TOKEN_UPDATE     // <-
	TOKEN_PLUS_EQ    // +=
	TOKEN_MINUS_EQ   // -=
	TOKEN_STAR_EQ    // *=
	TOKEN_SLASH_EQ   // /=
	TOKEN_PERCENT_EQ // %=
	TOKEN_ARROW      // ->
	TOKEN_FAT_ARROW  // =>
	TOKEN_DEFAULT    // ~>
	TOKEN_PIPE_FWD   // |>
	TOKEN_OR_BANG    // or!
	TOKEN_RANGE_EX   // ..<
	TOKEN_RANGE_IN   // ..=
	TOKEN_ELLIPSIS   // ...
	TOKEN_HASH       // #
	TOKEN_DOT        // .
	TOKEN_COMMA      // ,
	TOKEN_COLON      // :
	TOKEN_SEMICOLON  // ;
	TOKEN_BANG       // !
	TOKEN_AT         // @
	TOKEN_LPAREN     // (
	TOKEN_RPAREN     // )
	TOKEN_LBRACKET   // [
	TOKEN_RBRACKET   // ]
	TOKEN_LBRACE     // {
	TOKEN_RBRACE     // }
)

var keywords = map[string]TokenType{
	"and": TOKEN_AND, "arena": TOKEN_ARENA, "as": TOKEN_AS, "break": TOKEN_BREAK,
	"continue": TOKEN_CONTINUE, "cstruct": TOKEN_CSTRUCT, "defer": TOKEN_DEFER,
	"elif": TOKEN_ELIF, "else": TOKEN_ELSE, "err": TOKEN_ERR, "export": TOKEN_EXPORT,
	"if": TOKEN_IF, "import": TOKEN_IMPORT, "in": TOKEN_IN, "inf": TOKEN_INF,
	"no": TOKEN_NO, "not": TOKEN_NOT, "or": TOKEN_OR, "ret": TOKEN_RET,
	"unsafe": TOKEN_UNSAFE, "yes": TOKEN_YES,
}

// Operators, longest first so that maximal munch is a prefix scan.
var operators = []struct {
	text string
	typ  TokenType
}{
	{"...", TOKEN_ELLIPSIS}, {"..<", TOKEN_RANGE_EX}, {"..=", TOKEN_RANGE_IN},
	{"**", TOKEN_POWER}, {"==", TOKEN_EQ}, {"!=", TOKEN_NE}, {"<=", TOKEN_LE}, {">=", TOKEN_GE},
	{"<<", TOKEN_SHL}, {">>", TOKEN_SHR}, {":=", TOKEN_DEFINE}, {"<-", TOKEN_UPDATE},
	{"+=", TOKEN_PLUS_EQ}, {"-=", TOKEN_MINUS_EQ}, {"*=", TOKEN_STAR_EQ}, {"/=", TOKEN_SLASH_EQ},
	{"%=", TOKEN_PERCENT_EQ}, {"->", TOKEN_ARROW}, {"=>", TOKEN_FAT_ARROW}, {"~>", TOKEN_DEFAULT},
	{"|>", TOKEN_PIPE_FWD},
	{"+", TOKEN_PLUS}, {"-", TOKEN_MINUS}, {"*", TOKEN_STAR}, {"/", TOKEN_SLASH}, {"%", TOKEN_PERCENT},
	{"<", TOKEN_LT}, {">", TOKEN_GT}, {"&", TOKEN_AMP}, {"|", TOKEN_PIPE}, {"^", TOKEN_CARET},
	{"~", TOKEN_TILDE}, {"=", TOKEN_ASSIGN}, {"#", TOKEN_HASH}, {".", TOKEN_DOT}, {",", TOKEN_COMMA},
	{":", TOKEN_COLON}, {";", TOKEN_SEMICOLON}, {"!", TOKEN_BANG}, {"@", TOKEN_AT},
	{"(", TOKEN_LPAREN}, {")", TOKEN_RPAREN}, {"[", TOKEN_LBRACKET}, {"]", TOKEN_RBRACKET},
	{"{", TOKEN_LBRACE}, {"}", TOKEN_RBRACE},
}

// continues holds the tokens after which a newline does not end a statement.
var continues = map[TokenType]bool{
	TOKEN_PLUS: true, TOKEN_MINUS: true, TOKEN_STAR: true, TOKEN_SLASH: true, TOKEN_PERCENT: true,
	TOKEN_POWER: true, TOKEN_EQ: true, TOKEN_NE: true, TOKEN_LT: true, TOKEN_LE: true, TOKEN_GT: true,
	TOKEN_GE: true, TOKEN_AMP: true, TOKEN_PIPE: true, TOKEN_CARET: true, TOKEN_TILDE: true,
	TOKEN_SHL: true, TOKEN_SHR: true, TOKEN_ASSIGN: true, TOKEN_DEFINE: true, TOKEN_UPDATE: true,
	TOKEN_PLUS_EQ: true, TOKEN_MINUS_EQ: true, TOKEN_STAR_EQ: true, TOKEN_SLASH_EQ: true,
	TOKEN_PERCENT_EQ: true, TOKEN_ARROW: true, TOKEN_FAT_ARROW: true, TOKEN_DEFAULT: true,
	TOKEN_PIPE_FWD: true, TOKEN_OR_BANG: true, TOKEN_RANGE_EX: true, TOKEN_RANGE_IN: true,
	TOKEN_HASH: true, TOKEN_DOT: true, TOKEN_COMMA: true, TOKEN_LPAREN: true, TOKEN_LBRACKET: true,
	TOKEN_LBRACE: true, TOKEN_AND: true, TOKEN_OR: true, TOKEN_NOT: true, TOKEN_AS: true, TOKEN_IN: true,
}

type Token struct {
	Type   TokenType
	Value  string // identifier or keyword text, unescaped string, number text, raw f-string body
	Line   int
	Column int // 1-based, in bytes
}

func (t Token) String() string {
	switch t.Type {
	case TOKEN_EOF:
		return "end of file"
	case TOKEN_NEWLINE:
		return "end of line"
	case TOKEN_STRING:
		return strconv.Quote(t.Value)
	case TOKEN_FSTRING:
		return `f"` + t.Value + `"`
	}
	return "'" + t.Value + "'"
}

// LexError is a lexical error at a source position.
type LexError struct {
	Msg          string
	Line, Column int
}

func (e *LexError) Error() string { return fmt.Sprintf("%d:%d: %s", e.Line, e.Column, e.Msg) }

type lexer struct {
	src          string
	pos          int
	line, col    int
	toks         []Token
	nest         []TokenType // open brackets
	err          *LexError
	nextStartsOp bool
}

// Lex splits Tim source into tokens, deciding which newlines end statements.
func Lex(src string) ([]Token, *LexError) {
	l := &lexer{src: src, line: 1, col: 1}
	for l.err == nil {
		l.skipSpace()
		if l.pos >= len(l.src) {
			break
		}
		l.next()
	}
	if l.err != nil {
		return nil, l.err
	}
	l.emitNewline()
	l.toks = append(l.toks, Token{Type: TOKEN_EOF, Line: l.line, Column: l.col})
	return l.toks, nil
}

func (l *lexer) fail(line, col int, format string, args ...any) {
	if l.err == nil {
		l.err = &LexError{Msg: fmt.Sprintf(format, args...), Line: line, Column: col}
	}
}

func (l *lexer) advance(n int) {
	for range n {
		if l.src[l.pos] == '\n' {
			l.line++
			l.col = 1
		} else if l.src[l.pos]&0xC0 != 0x80 {
			l.col++
		}
		l.pos++
	}
}

// skipSpace skips blanks and comments; newlines are handled by next.
func (l *lexer) skipSpace() {
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; {
		case c == ' ' || c == '\t' || c == '\r':
			l.advance(1)
		case strings.HasPrefix(l.src[l.pos:], "//"):
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.advance(1)
			}
		case strings.HasPrefix(l.src[l.pos:], "/*"):
			line, col := l.line, l.col
			end := strings.Index(l.src[l.pos+2:], "*/")
			if end < 0 {
				l.fail(line, col, "unterminated block comment")
				l.pos = len(l.src)
				return
			}
			l.advance(end + 4)
		default:
			return
		}
	}
}

func (l *lexer) emit(t TokenType, value string, line, col int) {
	l.toks = append(l.toks, Token{Type: t, Value: value, Line: line, Column: col})
}

// emitNewline ends the current statement unless the statement cannot end here.
func (l *lexer) emitNewline() {
	if len(l.toks) == 0 {
		return
	}
	last := l.toks[len(l.toks)-1]
	starOp := last.Type == TOKEN_STAR && len(l.toks) > 1 && (l.toks[len(l.toks)-2].Type == TOKEN_EXPORT || l.toks[len(l.toks)-2].Type == TOKEN_AS)
	if last.Type == TOKEN_NEWLINE || continues[last.Type] && !starOp {
		return
	}
	if n := len(l.nest); n > 0 && l.nest[n-1] != TOKEN_LBRACE {
		return
	}
	l.emit(TOKEN_NEWLINE, "\n", last.Line, last.Column+len(last.Value))
}

// continuesNextLine reports whether the next significant line starts with a
// token that continues the previous expression (|>, ., or!, and, or).
func (l *lexer) continuesNextLine() bool {
	save := *l
	defer func() { *l = save }()
	for {
		l.skipSpace()
		if l.pos >= len(l.src) || l.err != nil {
			return false
		}
		if l.src[l.pos] != '\n' {
			break
		}
		l.advance(1)
	}
	rest := l.src[l.pos:]
	if strings.HasPrefix(rest, "|>") || len(rest) > 1 && rest[0] == '.' && isIdentByte(rest[1]) && !(rest[1] >= '0' && rest[1] <= '9') {
		return true
	}
	for _, w := range []string{"or!", "and", "or"} {
		if strings.HasPrefix(rest, w) && (w == "or!" || len(rest) == len(w) || !isIdentByte(rest[len(w)])) {
			return true
		}
	}
	return false
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 0x80 || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func (l *lexer) next() {
	line, col := l.line, l.col
	c := l.src[l.pos]
	rest := l.src[l.pos:]
	switch {
	case c == '\n':
		l.advance(1)
		if !l.continuesNextLine() {
			l.emitNewline()
		}
	case c >= '0' && c <= '9':
		l.number(line, col)
	case c == '"':
		l.advance(1)
		s := l.stringBody(line, col)
		l.emit(TOKEN_STRING, s, line, col)
	case c == 'f' && len(rest) > 1 && rest[1] == '"':
		l.advance(2)
		l.fstring(line, col)
	case c == '_' || c >= 0x80 || unicode.IsLetter(rune(c)):
		start := l.pos
		for l.pos < len(l.src) {
			r, size := utf8.DecodeRuneInString(l.src[l.pos:])
			if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				break
			}
			l.advance(size)
		}
		word := l.src[start:l.pos]
		if word == "or" && l.pos < len(l.src) && l.src[l.pos] == '!' && !strings.HasPrefix(l.src[l.pos:], "!=") {
			l.advance(1)
			l.emit(TOKEN_OR_BANG, "or!", line, col)
			return
		}
		if t, ok := keywords[word]; ok {
			l.emit(t, word, line, col)
			return
		}
		if word == "" {
			r, _ := utf8.DecodeRuneInString(rest)
			l.fail(line, col, "unexpected character %q", r)
			return
		}
		l.emit(TOKEN_IDENT, word, line, col)
	default:
		for _, op := range operators {
			if strings.HasPrefix(rest, op.text) {
				l.advance(len(op.text))
				switch op.typ {
				case TOKEN_LPAREN, TOKEN_LBRACKET, TOKEN_LBRACE:
					l.nest = append(l.nest, op.typ)
				case TOKEN_RPAREN, TOKEN_RBRACKET, TOKEN_RBRACE:
					open := map[TokenType]TokenType{TOKEN_RPAREN: TOKEN_LPAREN, TOKEN_RBRACKET: TOKEN_LBRACKET, TOKEN_RBRACE: TOKEN_LBRACE}[op.typ]
					if n := len(l.nest); n == 0 || l.nest[n-1] != open {
						l.fail(line, col, "unmatched '%s'", op.text)
						return
					}
					l.nest = l.nest[:len(l.nest)-1]
				}
				l.emit(op.typ, op.text, line, col)
				return
			}
		}
		r, _ := utf8.DecodeRuneInString(rest)
		l.fail(line, col, "unexpected character %q", r)
	}
}

func (l *lexer) number(line, col int) {
	start := l.pos
	digits := func(ok func(byte) bool) {
		for l.pos < len(l.src) && (ok(l.src[l.pos]) || l.src[l.pos] == '_' && l.pos+1 < len(l.src) && ok(l.src[l.pos+1])) {
			l.advance(1)
		}
	}
	dec := func(c byte) bool { return c >= '0' && c <= '9' }
	if l.src[l.pos] == '0' && l.pos+1 < len(l.src) && strings.IndexByte("xXoObB", l.src[l.pos+1]) >= 0 {
		base := map[byte]func(byte) bool{
			'x': func(c byte) bool { return dec(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' },
			'o': func(c byte) bool { return c >= '0' && c <= '7' },
			'b': func(c byte) bool { return c == '0' || c == '1' },
		}[l.src[l.pos+1]|0x20]
		l.advance(2)
		before := l.pos
		digits(base)
		if l.pos == before {
			l.fail(line, col, "malformed number literal %q", l.src[start:l.pos])
			return
		}
	} else {
		digits(dec)
		if l.pos+1 < len(l.src) && l.src[l.pos] == '.' && dec(l.src[l.pos+1]) {
			l.advance(1)
			digits(dec)
		}
		if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
			save := *l
			l.advance(1)
			if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
				l.advance(1)
			}
			if l.pos < len(l.src) && dec(l.src[l.pos]) {
				digits(dec)
			} else {
				*l = save
			}
		}
	}
	if l.pos < len(l.src) && isIdentByte(l.src[l.pos]) {
		l.fail(l.line, l.col, "unexpected %q after number", l.src[l.pos:l.pos+1])
		return
	}
	l.emit(TOKEN_NUMBER, strings.ReplaceAll(l.src[start:l.pos], "_", ""), line, col)
}

// stringBody reads up to the closing quote and returns the unescaped string.
func (l *lexer) stringBody(line, col int) string {
	var b strings.Builder
	for {
		if l.pos >= len(l.src) {
			l.fail(line, col, "unterminated string literal")
			return b.String()
		}
		c := l.src[l.pos]
		if c == '"' {
			l.advance(1)
			return b.String()
		}
		if c != '\\' {
			b.WriteByte(c)
			l.advance(1)
			continue
		}
		s, n, msg := unescape(l.src[l.pos:])
		if msg != "" {
			l.fail(l.line, l.col, "%s", msg)
			return b.String()
		}
		b.WriteString(s)
		l.advance(n)
	}
}

// unescape decodes the escape sequence at the start of s (which begins with a
// backslash), returning its text and length.
func unescape(s string) (string, int, string) {
	if len(s) < 2 {
		return "", 1, "unterminated escape sequence"
	}
	switch s[1] {
	case 'n':
		return "\n", 2, ""
	case 't':
		return "\t", 2, ""
	case 'r':
		return "\r", 2, ""
	case '0':
		return "\x00", 2, ""
	case '\\', '"', '{', '}':
		return s[1:2], 2, ""
	case 'x':
		if len(s) >= 4 {
			if v, err := strconv.ParseUint(s[2:4], 16, 8); err == nil {
				return string([]byte{byte(v)}), 4, ""
			}
		}
		return "", 2, `\x needs two hex digits`
	case 'u':
		if end := strings.IndexByte(s, '}'); len(s) > 3 && s[2] == '{' && end > 3 {
			if v, err := strconv.ParseUint(s[3:end], 16, 32); err == nil && utf8.ValidRune(rune(v)) {
				return string(rune(v)), end + 1, ""
			}
		}
		return "", 2, `\u needs a code point like \u{1F600}`
	}
	return "", 2, fmt.Sprintf("unknown escape sequence \\%c", s[1])
}

// fstring stores the raw body of f"..."; the parser splits and parses it.
func (l *lexer) fstring(line, col int) {
	start := l.pos
	depth := 0
	for {
		if l.pos >= len(l.src) {
			l.fail(line, col, "unterminated f-string literal")
			return
		}
		switch c := l.src[l.pos]; {
		case c == '\\':
			l.advance(min(2, len(l.src)-l.pos))
			continue
		case c == '"' && depth == 0:
			l.emit(TOKEN_FSTRING, l.src[start:l.pos], line, col)
			l.advance(1)
			return
		case c == '"':
			l.advance(1)
			l.stringBody(l.line, l.col)
			continue
		case c == '{':
			if depth == 0 && strings.HasPrefix(l.src[l.pos:], "{{") {
				l.advance(2)
				continue
			}
			depth++
		case c == '}':
			if depth == 0 && strings.HasPrefix(l.src[l.pos:], "}}") {
				l.advance(2)
				continue
			}
			if depth > 0 {
				depth--
			}
		}
		l.advance(1)
	}
}
