package main

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type TokenType int

const (
	TOKEN_EOF TokenType = iota
	TOKEN_ILLEGAL
	TOKEN_IDENT
	TOKEN_NUMBER
	TOKEN_STRING
	TOKEN_FSTRING

	TOKEN_NEWLINE
	TOKEN_SEMICOLON
	TOKEN_COMMA
	TOKEN_COLON
	TOKEN_DOT
	TOKEN_LPAREN
	TOKEN_RPAREN
	TOKEN_LBRACE
	TOKEN_RBRACE
	TOKEN_LBRACKET
	TOKEN_RBRACKET

	TOKEN_PLUS
	TOKEN_MINUS
	TOKEN_STAR
	TOKEN_SLASH
	TOKEN_MOD
	TOKEN_POWER
	TOKEN_INCREMENT
	TOKEN_DECREMENT
	TOKEN_HASH

	TOKEN_EQUALS
	TOKEN_COLON_EQUALS
	TOKEN_LEFT_ARROW
	TOKEN_PLUS_EQUALS
	TOKEN_MINUS_EQUALS
	TOKEN_STAR_EQUALS
	TOKEN_SLASH_EQUALS
	TOKEN_MOD_EQUALS
	TOKEN_POWER_EQUALS
	TOKEN_SHL_ASSIGN
	TOKEN_SHR_ASSIGN
	TOKEN_ROTL_ASSIGN
	TOKEN_ROTR_ASSIGN

	TOKEN_EQ
	TOKEN_NE
	TOKEN_LT
	TOKEN_GT
	TOKEN_LE
	TOKEN_GE

	TOKEN_ARROW
	TOKEN_FAT_ARROW
	TOKEN_DEFAULT_ARROW
	TOKEN_PIPE
	TOKEN_PIPEPIPE
	TOKEN_LTGT
	TOKEN_COLONCOLON
	TOKEN_DOTDOT
	TOKEN_DOTDOTLT
	TOKEN_ELLIPSIS
	TOKEN_QUESTION
	TOKEN_RANDOM
	TOKEN_BANG
	TOKEN_AMP
	TOKEN_AT
	TOKEN_AT_LABEL
	TOKEN_UNDERSCORE

	TOKEN_AMP_B
	TOKEN_PIPE_B
	TOKEN_CARET_B
	TOKEN_TILDE_B
	TOKEN_QUESTION_B
	TOKEN_LTLT
	TOKEN_GTGT
	TOKEN_LTLTLT
	TOKEN_GTGTGT

	TOKEN_AND
	TOKEN_OR
	TOKEN_XOR
	TOKEN_NOT
	TOKEN_OR_BANG
	TOKEN_AS_BANG
	TOKEN_IN
	TOKEN_AS
	TOKEN_YES
	TOKEN_NO
	TOKEN_INF
	TOKEN_BOOL
	TOKEN_RET
	TOKEN_ERR
	TOKEN_FUN
	TOKEN_IF
	TOKEN_ELIF
	TOKEN_ELSE
	TOKEN_FOR
	TOKEN_WHILE
	TOKEN_BREAK
	TOKEN_CONTINUE
	TOKEN_DEFER
	TOKEN_SHADOW
	TOKEN_WITH
	TOKEN_ARENA
	TOKEN_UNSAFE
	TOKEN_SYSCALL
	TOKEN_CSTRUCT
	TOKEN_PACKED
	TOKEN_ALIGNED
	TOKEN_SPAWN
	TOKEN_IMPORT
	TOKEN_EXPORT
)

const (
	UnconditionalJumpSize = 5
	ConditionalJumpSize   = 6
	StackSlotSize         = 8
	ByteMask              = 0xFF
)

type Token struct {
	Type   TokenType
	Value  string
	Line   int
	Column int
}

var keywords = map[string]TokenType{
	"and":      TOKEN_AND,
	"or":       TOKEN_OR,
	"xor":      TOKEN_XOR,
	"not":      TOKEN_NOT,
	"in":       TOKEN_IN,
	"as":       TOKEN_AS,
	"yes":      TOKEN_YES,
	"true":     TOKEN_YES,
	"no":       TOKEN_NO,
	"false":    TOKEN_NO,
	"inf":      TOKEN_INF,
	"bool":     TOKEN_BOOL,
	"ret":      TOKEN_RET,
	"return":   TOKEN_RET,
	"err":      TOKEN_ERR,
	"fun":      TOKEN_FUN,
	"if":       TOKEN_IF,
	"elif":     TOKEN_ELIF,
	"else":     TOKEN_ELSE,
	"for":      TOKEN_FOR,
	"while":    TOKEN_WHILE,
	"break":    TOKEN_BREAK,
	"continue": TOKEN_CONTINUE,
	"defer":    TOKEN_DEFER,
	"shadow":   TOKEN_SHADOW,
	"with":     TOKEN_WITH,
	"arena":    TOKEN_ARENA,
	"unsafe":   TOKEN_UNSAFE,
	"syscall":  TOKEN_SYSCALL,
	"cstruct":  TOKEN_CSTRUCT,
	"packed":   TOKEN_PACKED,
	"aligned":  TOKEN_ALIGNED,
	"spawn":    TOKEN_SPAWN,
	"import":   TOKEN_IMPORT,
	"export":   TOKEN_EXPORT,
}

type operator struct {
	text string
	typ  TokenType
	word bool
}

var operators = []operator{
	{"<<<=", TOKEN_ROTL_ASSIGN, false},
	{">>>=", TOKEN_ROTR_ASSIGN, false},
	{"<<=", TOKEN_SHL_ASSIGN, false},
	{">>=", TOKEN_SHR_ASSIGN, false},
	{"**=", TOKEN_POWER_EQUALS, false},
	{"<<<", TOKEN_LTLTLT, false},
	{">>>", TOKEN_GTGTGT, false},
	{"...", TOKEN_ELLIPSIS, false},
	{"..<", TOKEN_DOTDOTLT, false},
	{"..=", TOKEN_DOTDOT, false},
	{"..", TOKEN_DOTDOT, false},
	{"**", TOKEN_POWER, false},
	{"++", TOKEN_INCREMENT, false},
	{"--", TOKEN_DECREMENT, false},
	{"+=", TOKEN_PLUS_EQUALS, false},
	{"-=", TOKEN_MINUS_EQUALS, false},
	{"*=", TOKEN_STAR_EQUALS, false},
	{"/=", TOKEN_SLASH_EQUALS, false},
	{"%=", TOKEN_MOD_EQUALS, false},
	{"->", TOKEN_ARROW, false},
	{"=>", TOKEN_FAT_ARROW, false},
	{"~>", TOKEN_DEFAULT_ARROW, false},
	{"<-", TOKEN_LEFT_ARROW, false},
	{"<>", TOKEN_LTGT, false},
	{"<<", TOKEN_LTLT, false},
	{">>", TOKEN_GTGT, false},
	{"<=", TOKEN_LE, false},
	{">=", TOKEN_GE, false},
	{"==", TOKEN_EQ, false},
	{"!=", TOKEN_NE, false},
	{":=", TOKEN_COLON_EQUALS, false},
	{"::", TOKEN_COLONCOLON, false},
	{"||", TOKEN_PIPEPIPE, false},
	{"??", TOKEN_RANDOM, false},
	{"&b", TOKEN_AMP_B, true},
	{"|b", TOKEN_PIPE_B, true},
	{"^b", TOKEN_CARET_B, true},
	{"~b", TOKEN_TILDE_B, true},
	{"?b", TOKEN_QUESTION_B, true},
	{"+", TOKEN_PLUS, false},
	{"-", TOKEN_MINUS, false},
	{"*", TOKEN_STAR, false},
	{"/", TOKEN_SLASH, false},
	{"%", TOKEN_MOD, false},
	{"=", TOKEN_EQUALS, false},
	{"<", TOKEN_LT, false},
	{">", TOKEN_GT, false},
	{"|", TOKEN_PIPE, false},
	{"&", TOKEN_AMP, false},
	{"!", TOKEN_BANG, false},
	{"?", TOKEN_QUESTION, false},
	{"#", TOKEN_HASH, false},
	{"@", TOKEN_AT, false},
	{":", TOKEN_COLON, false},
	{".", TOKEN_DOT, false},
	{",", TOKEN_COMMA, false},
	{";", TOKEN_SEMICOLON, false},
	{"(", TOKEN_LPAREN, false},
	{")", TOKEN_RPAREN, false},
	{"{", TOKEN_LBRACE, false},
	{"}", TOKEN_RBRACE, false},
	{"[", TOKEN_LBRACKET, false},
	{"]", TOKEN_RBRACKET, false},
}

var illegalHints = map[string]string{
	"^": "'^' is not an operator: use ** for powers and ^b for bitwise xor",
}

type Lexer struct {
	input     string
	pos       int
	line      int
	lineStart int
	// unterminatedString records the opening quote of a string literal that
	// reached EOF, so the parser reports it instead of a symptom at EOF.
	unterminatedString *Token
	errs               *lexErrors
}

type lexError struct {
	tok Token
	msg string
}

// lexErrors is shared by a lexer and its clones, so a bad token seen during
// lookahead is reported once.
type lexErrors struct {
	seen     map[[2]int]bool
	list     []lexError
	reported int
}

type LexerState struct {
	pos, line, lineStart int
}

func NewLexer(input string) *Lexer {
	l := &Lexer{input: input, line: 1, errs: &lexErrors{seen: map[[2]int]bool{}}}
	if strings.HasPrefix(input, "#!") {
		l.pos = strings.IndexByte(input, '\n')
		if l.pos < 0 {
			l.pos = len(input)
		}
	}
	return l
}

func (l *Lexer) save() LexerState { return LexerState{l.pos, l.line, l.lineStart} }

func (l *Lexer) restore(s LexerState) { l.pos, l.line, l.lineStart = s.pos, s.line, s.lineStart }

func (l *Lexer) clone() *Lexer {
	c := *l
	c.unterminatedString = nil
	return &c
}

func (l *Lexer) column() int { return l.pos - l.lineStart + 1 }

func (l *Lexer) newline(at int) {
	l.line++
	l.lineStart = at + 1
}

func (l *Lexer) skipSpaceAndComments() {
	for l.pos < len(l.input) {
		switch c := l.input[l.pos]; {
		case c == ' ' || c == '\t' || c == '\r':
			l.pos++
		case strings.HasPrefix(l.input[l.pos:], "//"):
			for l.pos < len(l.input) && l.input[l.pos] != '\n' {
				l.pos++
			}
		default:
			return
		}
	}
}

func isIdentStart(r rune) bool { return unicode.IsLetter(r) }

func isIdentPart(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func (l *Lexer) runeAt(i int) rune {
	if i >= len(l.input) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(l.input[i:])
	return r
}

func (l *Lexer) NextToken() Token {
	l.skipSpaceAndComments()
	tok := Token{Line: l.line, Column: l.column()}
	if l.pos >= len(l.input) {
		tok.Type = TOKEN_EOF
		return tok
	}
	start := l.pos
	c := l.input[l.pos]
	switch {
	case c == '\n':
		l.newline(l.pos)
		l.pos++
		tok.Type = TOKEN_NEWLINE
		return tok
	case c == '"':
		return l.lexString(tok, false)
	case c >= '0' && c <= '9':
		return l.lexNumber(tok)
	}
	if r := l.runeAt(l.pos); isIdentStart(r) {
		for l.pos < len(l.input) && isIdentPart(l.runeAt(l.pos)) {
			_, n := utf8.DecodeRuneInString(l.input[l.pos:])
			l.pos += n
		}
		tok.Value = l.input[start:l.pos]
		if tok.Value == "f" && l.pos < len(l.input) && l.input[l.pos] == '"' {
			return l.lexString(tok, true)
		}
		if (tok.Value == "or" || tok.Value == "as") && l.pos < len(l.input) && l.input[l.pos] == '!' {
			l.pos++
			tok.Type = TOKEN_OR_BANG
			if tok.Value == "as" {
				tok.Type = TOKEN_AS_BANG
			}
			tok.Value += "!"
			return tok
		}
		tok.Type = TOKEN_IDENT
		if kw, ok := keywords[tok.Value]; ok {
			tok.Type = kw
		}
		return tok
	}
	if c == '_' {
		l.pos++
		if isIdentPart(l.runeAt(l.pos)) {
			for l.pos < len(l.input) && isIdentPart(l.runeAt(l.pos)) {
				l.pos++
			}
			return l.illegal(tok, "identifiers start with a letter, not '_'")
		}
		tok.Type, tok.Value = TOKEN_UNDERSCORE, "_"
		return tok
	}
	if c == '@' && l.pos+1 < len(l.input) && l.input[l.pos+1] >= '0' && l.input[l.pos+1] <= '9' {
		l.pos++
		for l.pos < len(l.input) && l.input[l.pos] >= '0' && l.input[l.pos] <= '9' {
			l.pos++
		}
		tok.Type, tok.Value = TOKEN_AT_LABEL, l.input[start:l.pos]
		return tok
	}
	rest := l.input[l.pos:]
	for _, op := range operators {
		if !strings.HasPrefix(rest, op.text) {
			continue
		}
		if op.word && isIdentPart(l.runeAt(l.pos+len(op.text))) {
			continue
		}
		l.pos += len(op.text)
		tok.Type, tok.Value = op.typ, op.text
		return tok
	}
	_, n := utf8.DecodeRuneInString(rest)
	l.pos += n
	if c == '^' {
		return l.illegal(tok, "'^' is not an operator: use ** for powers and ^b for bitwise xor")
	}
	return l.illegal(tok, "unexpected character '"+l.input[start:l.pos]+"'")
}

func (l *Lexer) illegal(tok Token, msg string) Token {
	tok.Type = TOKEN_ILLEGAL
	tok.Value = l.input[l.lineStart+tok.Column-1 : l.pos]
	if key := [2]int{tok.Line, tok.Column}; !l.errs.seen[key] {
		l.errs.seen[key] = true
		l.errs.list = append(l.errs.list, lexError{tok, msg})
	}
	return tok
}

// forgetErrors drops the unreported errors on line between columns from and
// to, for text that turned out not to be code.
func (l *Lexer) forgetErrors(line, from, to int) {
	e := l.errs
	kept := e.list[:e.reported]
	for _, x := range e.list[e.reported:] {
		if x.tok.Line != line || x.tok.Column < from || x.tok.Column >= to {
			kept = append(kept, x)
		}
	}
	e.list = kept
}

// takeErrors returns the unreported lexical errors at or before line:column.
func (l *Lexer) takeErrors(line, column int) []lexError {
	e := l.errs
	var out []lexError
	for e.reported < len(e.list) {
		t := e.list[e.reported].tok
		if t.Line > line || t.Line == line && t.Column > column {
			break
		}
		out = append(out, e.list[e.reported])
		e.reported++
	}
	return out
}

func (l *Lexer) lexNumber(tok Token) Token {
	start := l.pos
	digits := func(ok func(byte) bool) int {
		n := 0
		for l.pos < len(l.input) && (ok(l.input[l.pos]) || l.input[l.pos] == '_' && n > 0) {
			if l.input[l.pos] != '_' {
				n++
			}
			l.pos++
		}
		return n
	}
	dec := func(b byte) bool { return b >= '0' && b <= '9' }
	if l.input[l.pos] == '0' && l.pos+1 < len(l.input) {
		var ok func(byte) bool
		switch l.input[l.pos+1] {
		case 'x', 'X':
			ok = isHexDigit
		case 'b', 'B':
			ok = func(b byte) bool { return b == '0' || b == '1' }
		case 'o', 'O':
			ok = func(b byte) bool { return b >= '0' && b <= '7' }
		}
		if ok != nil {
			l.pos += 2
			if digits(ok) == 0 || isIdentPart(l.runeAt(l.pos)) {
				for isIdentPart(l.runeAt(l.pos)) {
					l.pos++
				}
				return l.illegal(tok, "malformed number literal '"+l.input[start:l.pos]+"'")
			}
			return l.number(tok, start)
		}
	}
	digits(dec)
	if l.pos+1 < len(l.input) && l.input[l.pos] == '.' && dec(l.input[l.pos+1]) {
		l.pos++
		digits(dec)
	}
	if l.pos < len(l.input) && (l.input[l.pos] == 'e' || l.input[l.pos] == 'E') {
		p := l.pos + 1
		if p < len(l.input) && (l.input[p] == '+' || l.input[p] == '-') {
			p++
		}
		if p < len(l.input) && dec(l.input[p]) {
			l.pos = p
			digits(dec)
		}
	}
	if isIdentStart(l.runeAt(l.pos)) {
		for isIdentPart(l.runeAt(l.pos)) {
			l.pos++
		}
		return l.illegal(tok, "malformed number literal '"+l.input[start:l.pos]+"'")
	}
	return l.number(tok, start)
}

func (l *Lexer) number(tok Token, start int) Token {
	tok.Type = TOKEN_NUMBER
	tok.Value = strings.ReplaceAll(l.input[start:l.pos], "_", "")
	return tok
}

func (l *Lexer) lexString(tok Token, interp bool) Token {
	l.pos++
	body := l.pos
	end := stringBodyEnd(l.input, body, interp)
	for i := body; i < end; i++ {
		if l.input[i] == '\n' {
			l.newline(i)
		}
	}
	l.pos = end
	if end >= len(l.input) {
		if l.unterminatedString == nil {
			t := tok
			t.Type = TOKEN_STRING
			if interp {
				t.Type = TOKEN_FSTRING
			}
			l.unterminatedString = &t
		}
	} else {
		l.pos++
	}
	if interp {
		tok.Type, tok.Value = TOKEN_FSTRING, l.input[body:end]
	} else {
		tok.Type, tok.Value = TOKEN_STRING, processEscapeSequences(l.input[body:end])
	}
	return tok
}

// stringBodyEnd returns the index of the quote that closes a string body
// starting at i (len(s) if unterminated). In f-strings (interp), quotes inside
// {interpolations} belong to nested strings, not to this one.
func stringBodyEnd(s string, i int, interp bool) int {
	for i < len(s) {
		switch c := s[i]; {
		case c == '\\':
			i += 2
		case c == '"':
			return i
		case interp && c == '{':
			if i+1 < len(s) && s[i+1] == '{' {
				i += 2
				continue
			}
			i = interpolationEnd(s, i+1) + 1
		default:
			i++
		}
	}
	return len(s)
}

// interpolationEnd returns the index of the '}' closing an f-string
// interpolation whose expression starts at i, skipping nested strings.
func interpolationEnd(s string, i int) int {
	depth := 1
	for ; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return i
			}
		case '"':
			nested := i > 0 && s[i-1] == 'f' && (i < 2 || !isIdentByte(s[i-2]))
			i = stringBodyEnd(s, i+1, nested)
		}
	}
	return len(s)
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isHexDigit(ch byte) bool {
	return (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
}

var simpleEscapes = map[byte]rune{
	'n': '\n', 't': '\t', 'r': '\r', '0': 0, 'e': 0x1b, 'a': 7, 'b': 8, 'f': 12, 'v': 11,
	'\\': '\\', '"': '"', '\'': '\'', '{': '{', '}': '}',
}

// processEscapeSequences decodes \n \t \r \0 \e \a \b \f \v \\ \" \' \{ \}
// \xHH \uHHHH and \u{H...}; other backslashes are kept as written.
func processEscapeSequences(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		e := s[i+1]
		if r, ok := simpleEscapes[e]; ok {
			b.WriteRune(r)
			i++
			continue
		}
		hex, width := "", 0
		switch {
		case e == 'x' && i+4 <= len(s):
			hex, width = s[i+2:i+4], 4
		case e == 'u' && i+2 < len(s) && s[i+2] == '{':
			if j := strings.IndexByte(s[i+3:], '}'); j > 0 {
				hex, width = s[i+3:i+3+j], j+4
			}
		case e == 'u' && i+6 <= len(s):
			hex, width = s[i+2:i+6], 6
		}
		if v, err := strconv.ParseUint(hex, 16, 32); hex != "" && err == nil && v <= unicode.MaxRune {
			b.WriteRune(rune(v))
			i += width - 1
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
