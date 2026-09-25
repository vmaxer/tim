package main

import (
	"fmt"
	"math"
	"math/big"
	"os"
	"strconv"
	"strings"
)

// Parser is a recursive-descent parser for the grammar in GRAMMAR.md. It
// produces the AST in ast.go.
type Parser struct {
	toks     []Token
	i        int
	filename string
	source   string
	lexErr   *LexError
	errors   *ErrorCollector
	cstructs map[string]*CStructDecl
	cImports map[string]bool
	scopes   []map[string]bool
	loops    int
	noMatch  int // > 0: `{` after an expression is a body, not a match (if/loop headers)
	noPipe   int // > 0: `|` ends the expression (guard clause results)
	tmp      int
}

// parseBailout unwinds the parser to the enclosing statement after an error.
type parseBailout struct{}

// compilerError aborts code generation with a message (recovered by the compile driver).
func compilerError(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if VerboseMode {
		fmt.Fprintln(os.Stderr, "Error:", msg)
	}
	panic(fmt.Errorf("%s", msg))
}

func NewParser(input string) *Parser { return NewParserWithFilename(input, "<input>") }

func NewParserWithFilename(input, filename string) *Parser {
	toks, lexErr := Lex(input)
	p := &Parser{
		toks:     toks,
		filename: filename,
		source:   input,
		lexErr:   lexErr,
		errors:   NewErrorCollector(10),
		cstructs: map[string]*CStructDecl{},
		cImports: map[string]bool{"c": true, "C": true},
		scopes:   []map[string]bool{{}},
	}
	if lexErr != nil {
		p.toks = []Token{{Type: TOKEN_EOF, Line: lexErr.Line, Column: lexErr.Column}}
	}
	p.errors.SetSourceCode(input)
	return p
}

// ParseProgram parses a whole file, reporting every syntax error it finds.
func (p *Parser) ParseProgram() *Program {
	program := &Program{}
	if p.lexErr != nil {
		p.errors.AddError(SyntaxError(p.lexErr.Msg, SourceLocation{File: p.filename, Line: p.lexErr.Line, Column: p.lexErr.Column, Length: 1}))
	}
	p.skipEnds()
	for !p.at(TOKEN_EOF) && !p.errors.ShouldStop() {
		p.topStatement(program)
		p.skipEnds()
	}
	if p.errors.HasErrors() {
		fmt.Fprintln(os.Stderr, p.errors.Report(true))
		panic(newReportedError(strings.TrimSpace(p.errors.Report(false))))
	}
	program.CStructs = p.cstructs
	uniquifyLocalFunctions(program)
	return optimizeProgram(program)
}

func (p *Parser) topStatement(program *Program) {
	start := p.i
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(parseBailout); !ok {
				panic(r)
			}
			p.syncToStatement(start)
		}
	}()
	stmt := p.statement()
	if exp, ok := stmt.(*ExportStmt); ok {
		if exp.Mode == "*" {
			program.ExportMode = "*"
		} else {
			program.ExportedFuncs = append(program.ExportedFuncs, exp.Functions...)
		}
	} else if stmt != nil {
		program.Statements = append(program.Statements, stmt)
	}
	p.endStatement()
}

// syncToStatement skips to the end of the top-level statement that began at start.
func (p *Parser) syncToStatement(start int) {
	depth := 0
	for k := start; k < p.i; k++ {
		switch p.toks[k].Type {
		case TOKEN_LBRACE:
			depth++
		case TOKEN_RBRACE:
			depth--
		}
	}
	for !p.at(TOKEN_EOF) {
		switch p.cur().Type {
		case TOKEN_LBRACE:
			depth++
		case TOKEN_RBRACE:
			depth--
		case TOKEN_NEWLINE, TOKEN_SEMICOLON:
			if depth <= 0 {
				return
			}
		}
		p.i++
	}
}

// Token helpers.

func (p *Parser) cur() Token { return p.toks[p.i] }

func (p *Parser) peekAt(n int) Token {
	if p.i+n < len(p.toks) {
		return p.toks[p.i+n]
	}
	return p.toks[len(p.toks)-1]
}

func (p *Parser) at(t TokenType) bool { return p.toks[p.i].Type == t }

func (p *Parser) advance() Token {
	t := p.toks[p.i]
	if t.Type != TOKEN_EOF {
		p.i++
	}
	return t
}

func (p *Parser) accept(t TokenType) bool {
	if p.at(t) {
		p.advance()
		return true
	}
	return false
}

func (p *Parser) expect(t TokenType, what string) Token {
	if !p.at(t) {
		p.fail("expected %s, found %s", what, p.cur())
	}
	return p.advance()
}

func (p *Parser) fail(format string, args ...any) {
	t := p.cur()
	length := max(1, len(t.Value))
	if t.Type == TOKEN_NEWLINE || t.Type == TOKEN_EOF {
		length = 1
	}
	p.errors.AddError(SyntaxError(fmt.Sprintf(format, args...), SourceLocation{File: p.filename, Line: t.Line, Column: t.Column, Length: length}))
	panic(parseBailout{})
}

func (p *Parser) skipNewlines() {
	for p.at(TOKEN_NEWLINE) {
		p.advance()
	}
}

func (p *Parser) skipEnds() {
	for p.at(TOKEN_NEWLINE) || p.at(TOKEN_SEMICOLON) {
		p.advance()
	}
}

// endStatement requires a statement terminator (or a closing brace / EOF).
func (p *Parser) endStatement() {
	switch p.cur().Type {
	case TOKEN_NEWLINE, TOKEN_SEMICOLON:
		p.skipEnds()
	case TOKEN_RBRACE, TOKEN_EOF:
	default:
		p.fail("unexpected %s after statement", p.cur())
	}
}

// Scopes, for telling variables from C namespaces.

func (p *Parser) declare(name string) { p.scopes[len(p.scopes)-1][name] = true }
func (p *Parser) pushScope()          { p.scopes = append(p.scopes, map[string]bool{}) }
func (p *Parser) popScope()           { p.scopes = p.scopes[:len(p.scopes)-1] }
func (p *Parser) isNamespace(n string) bool {
	if !p.cImports[n] {
		return false
	}
	for _, s := range p.scopes {
		if s[n] {
			return false
		}
	}
	return true
}

// bitBuiltins are functions that map onto machine operations.
var bitBuiltins = map[string]string{"bit": "?b", "rotl": "<<<b", "rotr": ">>>b"}

func (p *Parser) isDeclared(n string) bool {
	for _, s := range p.scopes {
		if s[n] {
			return true
		}
	}
	return false
}

// nested parses f with the context flags reset, as inside brackets.
func nested[T any](p *Parser, f func() T) T {
	noMatch, noPipe := p.noMatch, p.noPipe
	p.noMatch, p.noPipe = 0, 0
	defer func() { p.noMatch, p.noPipe = noMatch, noPipe }()
	return f()
}

func (p *Parser) tempName() string {
	p.tmp++
	return fmt.Sprintf("tmp·%d", p.tmp)
}

// Statements.

func (p *Parser) statement() Statement {
	switch p.cur().Type {
	case TOKEN_IMPORT:
		return p.importStmt()
	case TOKEN_EXPORT:
		return p.exportStmt()
	case TOKEN_CSTRUCT:
		return p.cstructDecl()
	case TOKEN_AT:
		return p.loop()
	case TOKEN_BREAK, TOKEN_CONTINUE:
		return p.jump()
	case TOKEN_RET, TOKEN_ERR:
		return p.ret()
	case TOKEN_DEFER:
		p.advance()
		return &DeferStmt{Call: p.expr()}
	case TOKEN_ARENA:
		p.advance()
		return &ArenaStmt{Body: p.blockStmts()}
	case TOKEN_IF:
		return p.ifStmt()
	case TOKEN_ELIF, TOKEN_ELSE:
		p.fail("'%s' without a preceding 'if'", p.cur().Value)
	case TOKEN_IDENT:
		if s := p.definition(); s != nil {
			return s
		}
		if s := p.binding(); s != nil {
			return s
		}
		if s := p.update(); s != nil {
			return s
		}
	}
	return &ExpressionStmt{Expr: p.expr()}
}

func (p *Parser) importStmt() Statement {
	p.advance()
	var source strings.Builder
	switch t := p.cur(); t.Type {
	case TOKEN_STRING, TOKEN_IDENT, TOKEN_DOT, TOKEN_SLASH:
		source.WriteString(p.advance().Value)
		for {
			switch p.cur().Type {
			case TOKEN_DOT, TOKEN_SLASH, TOKEN_IDENT, TOKEN_NUMBER, TOKEN_AT, TOKEN_MINUS, TOKEN_COLON:
				source.WriteString(p.advance().Value)
				continue
			}
			break
		}
	default:
		p.fail("expected a library, path or repository after 'import', found %s", t)
	}
	alias := ""
	if p.accept(TOKEN_AS) {
		if p.at(TOKEN_STAR) {
			alias = p.advance().Value
		} else {
			alias = p.expect(TOKEN_IDENT, "an alias after 'as'").Value
		}
	} else {
		alias = deriveAliasFromSource(source.String())
	}
	src := source.String()
	spec, err := ParseImportSource(src)
	if err != nil {
		p.fail("invalid import source: %v", err)
	}
	if strings.HasSuffix(src, ".so") || strings.Contains(src, ".so.") ||
		strings.HasSuffix(src, ".dll") || strings.HasSuffix(src, ".dylib") {
		p.cImports[alias] = true
		name := src[strings.LastIndexAny(src, `/\`)+1:]
		return &CImportStmt{Library: name, Alias: alias, SoPath: src}
	}
	if spec.IsLocal || spec.Version != "" || isGitURL(src) || strings.ContainsAny(src, `/\`) {
		return &ImportStmt{URL: spec.Source, Version: spec.Version, Alias: alias}
	}
	p.cImports[alias] = true
	return &CImportStmt{Library: src, Alias: alias}
}

func (p *Parser) exportStmt() Statement {
	p.advance()
	if p.accept(TOKEN_STAR) {
		return &ExportStmt{Mode: "*"}
	}
	var names []string
	for p.at(TOKEN_IDENT) {
		names = append(names, p.advance().Value)
		p.accept(TOKEN_COMMA)
	}
	if len(names) == 0 {
		p.fail("expected '*' or function names after 'export'")
	}
	return &ExportStmt{Functions: names}
}

var cTypeAliases = map[string]string{
	"i8": "int8", "i16": "int16", "i32": "int32", "i64": "int64",
	"u8": "uint8", "u16": "uint16", "u32": "uint32", "u64": "uint64",
	"f32": "float32", "f64": "float64",
}

func (p *Parser) cstructDecl() Statement {
	p.advance()
	decl := &CStructDecl{Name: p.expect(TOKEN_IDENT, "a struct name").Value}
	for p.at(TOKEN_IDENT) {
		switch p.cur().Value {
		case "packed":
			p.advance()
			decl.Packed = true
		case "aligned":
			p.advance()
			p.expect(TOKEN_LPAREN, "'('")
			n, err := strconv.Atoi(p.expect(TOKEN_NUMBER, "an alignment").Value)
			if err != nil || n <= 0 {
				p.fail("alignment must be a positive integer")
			}
			decl.Align = n
			p.expect(TOKEN_RPAREN, "')'")
		default:
			p.fail("unexpected %s in cstruct header", p.cur())
		}
	}
	p.expect(TOKEN_LBRACE, "'{'")
	p.skipEnds()
	for !p.at(TOKEN_RBRACE) {
		names := []string{p.expect(TOKEN_IDENT, "a field name").Value}
		for p.accept(TOKEN_COMMA) {
			names = append(names, p.expect(TOKEN_IDENT, "a field name").Value)
		}
		p.expect(TOKEN_COLON, "':' and a field type")
		typeTok := p.expect(TOKEN_IDENT, "a field type")
		ctype, nested := typeTok.Value, ""
		if canon, ok := cTypeAliases[ctype]; ok {
			ctype = canon
		}
		switch ctype {
		case "int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64", "float32", "float64", "ptr", "cstr":
		default:
			if _, ok := p.cstructs[ctype]; !ok {
				p.i--
				p.fail("unknown field type '%s' (use int8..uint64, float32, float64, ptr, cstr or a cstruct)", ctype)
			}
			ctype, nested = "ptr", typeTok.Value
		}
		for _, n := range names {
			decl.Fields = append(decl.Fields, CStructField{Name: n, Type: ctype, StructName: nested})
		}
		p.accept(TOKEN_COMMA)
		p.skipEnds()
	}
	p.expect(TOKEN_RBRACE, "'}'")
	decl.CalculateStructLayout()
	p.cstructs[decl.Name] = decl
	return decl
}

// definition parses `name(params) = body` and `Type.name(params) = body`.
func (p *Parser) definition() Statement {
	j := p.i + 1
	recv := ""
	if p.peekAt(1).Type == TOKEN_DOT && p.peekAt(2).Type == TOKEN_IDENT && p.peekAt(3).Type == TOKEN_LPAREN {
		recv = p.cur().Value
		j = p.i + 3
	}
	if p.toks[j].Type != TOKEN_LPAREN {
		return nil
	}
	close := p.matching(j)
	if close < 0 {
		return nil
	}
	k := close + 1
	if p.toks[k].Type == TOKEN_COLON && p.toks[k+1].Type == TOKEN_IDENT {
		k += 2
	}
	if p.toks[k].Type != TOKEN_ASSIGN {
		return nil
	}
	name := p.advance().Value
	if recv != "" {
		p.advance()
		name = recv + "_" + p.advance().Value
	}
	p.declare(name)
	lambda := p.paramList()
	if recv != "" {
		if len(lambda.Params) == 0 {
			p.fail("a method needs a receiver parameter, e.g. %s.%s(self)", recv, strings.TrimPrefix(name, recv+"_"))
		}
		if lambda.ParamCStructTypes == nil {
			lambda.ParamCStructTypes = map[string]string{}
		}
		lambda.ParamCStructTypes[lambda.Params[0]] = recv
	}
	if p.accept(TOKEN_COLON) {
		lambda.ReturnType = p.typeName()
	}
	p.expect(TOKEN_ASSIGN, "'='")
	p.skipNewlines()
	lambda.Body = p.functionBody(lambda)
	return &AssignStmt{Name: name, Value: lambda}
}

// matching returns the index of the bracket closing the one at open, or -1.
func (p *Parser) matching(open int) int {
	depth := 0
	for k := open; k < len(p.toks); k++ {
		switch p.toks[k].Type {
		case TOKEN_LPAREN, TOKEN_LBRACKET, TOKEN_LBRACE:
			depth++
		case TOKEN_RPAREN, TOKEN_RBRACKET, TOKEN_RBRACE:
			depth--
			if depth == 0 {
				return k
			}
		case TOKEN_EOF:
			return -1
		}
	}
	return -1
}

// paramList parses `( [param {, param}] )` into a lambda without a body.
func (p *Parser) paramList() *LambdaExpr {
	lambda := &LambdaExpr{Params: []string{}}
	p.expect(TOKEN_LPAREN, "'('")
	for !p.at(TOKEN_RPAREN) {
		if lambda.VariadicParam != "" {
			p.fail("the variadic parameter must be the last one")
		}
		name := p.expect(TOKEN_IDENT, "a parameter name").Value
		if p.accept(TOKEN_COLON) {
			t := p.cur().Value
			if _, ok := p.cstructs[t]; ok {
				p.advance()
				if lambda.ParamCStructTypes == nil {
					lambda.ParamCStructTypes = map[string]string{}
				}
				lambda.ParamCStructTypes[name] = t
			} else {
				if lambda.ParamTypes == nil {
					lambda.ParamTypes = map[string]*TimType{}
				}
				lambda.ParamTypes[name] = p.typeName()
			}
		}
		if p.accept(TOKEN_ELLIPSIS) {
			lambda.VariadicParam = name
		} else {
			lambda.Params = append(lambda.Params, name)
		}
		if !p.accept(TOKEN_COMMA) {
			break
		}
	}
	p.expect(TOKEN_RPAREN, "')'")
	return lambda
}

// functionBody parses a lambda or definition body in a new scope.
func (p *Parser) functionBody(lambda *LambdaExpr) Expression {
	p.pushScope()
	defer p.popScope()
	for _, n := range lambda.Params {
		p.declare(n)
	}
	if lambda.VariadicParam != "" {
		p.declare(lambda.VariadicParam)
	}
	loops := p.loops
	p.loops = 0
	defer func() { p.loops = loops }()
	return nested(p, p.expr)
}

var nativeTypes = map[string]TypeKind{
	"num": TypeNumber, "str": TypeString, "list": TypeList, "map": TypeMap, "bool": TypeBoolean,
}

var cTypes = map[string]*TimType{
	"cstring": {Kind: TypeCString, CType: "char*"}, "cptr": {Kind: TypeCPointer, CType: "void*"},
	"cint": {Kind: TypeCInt, CType: "int"}, "clong": {Kind: TypeCLong, CType: "long"},
	"cfloat": {Kind: TypeCFloat, CType: "float"}, "cdouble": {Kind: TypeCDouble, CType: "double"},
	"cbool": {Kind: TypeCBool, CType: "bool"}, "cvoid": {Kind: TypeCVoid},
}

// typeName parses a type annotation.
func (p *Parser) typeName() *TimType {
	t := p.expect(TOKEN_IDENT, "a type")
	if k, ok := nativeTypes[t.Value]; ok {
		return &TimType{Kind: k}
	}
	if t.Value == "fn" {
		return &TimType{Kind: TypeUnknown, CType: "fn"}
	}
	if ct, ok := cTypes[t.Value]; ok {
		c := *ct
		return &c
	}
	if _, ok := p.cstructs[t.Value]; ok {
		return &TimType{Kind: TypeCPointer, CType: t.Value}
	}
	p.i--
	p.fail("unknown type '%s'", t.Value)
	return nil
}

// binding parses `x = e`, `x := e`, `x: T = e` and `a, b = e`.
func (p *Parser) binding() Statement {
	names := []string{p.cur().Value}
	k := p.i + 1
	for p.toks[k].Type == TOKEN_COMMA && p.toks[k+1].Type == TOKEN_IDENT {
		names = append(names, p.toks[k+1].Value)
		k += 2
	}
	annotated := false
	if len(names) == 1 && p.toks[k].Type == TOKEN_COLON && p.toks[k+1].Type == TOKEN_IDENT &&
		(p.toks[k+2].Type == TOKEN_ASSIGN || p.toks[k+2].Type == TOKEN_DEFINE) {
		annotated = true
		k += 2
	}
	op := p.toks[k].Type
	if op != TOKEN_ASSIGN && op != TOKEN_DEFINE {
		return nil
	}
	p.i += 1 + 2*(len(names)-1)
	var ann *TimType
	if annotated {
		p.advance()
		ann = p.typeName()
	}
	p.advance()
	p.skipNewlines()
	mutable := op == TOKEN_DEFINE
	for _, n := range names {
		p.declare(n)
	}
	start := p.i
	value := p.expr()
	if len(names) > 1 {
		return &MultipleAssignStmt{Names: names, Value: value, Mutable: mutable}
	}
	if b, ok := value.(*BlockExpr); ok && p.toks[start].Type == TOKEN_LBRACE && p.matching(start) == p.i-1 {
		value = &LambdaExpr{Params: []string{}, Body: b}
	}
	return &AssignStmt{Name: names[0], Value: value, Mutable: mutable, TypeAnnotation: ann}
}

var compoundOps = map[TokenType]string{
	TOKEN_PLUS_EQ: "+", TOKEN_MINUS_EQ: "-", TOKEN_STAR_EQ: "*", TOKEN_SLASH_EQ: "/", TOKEN_PERCENT_EQ: "%",
}

// update parses `place <- e` and `place op= e`.
func (p *Parser) update() Statement {
	k := p.i + 1
	for {
		switch p.toks[k].Type {
		case TOKEN_LBRACKET:
			if k = p.matching(k); k < 0 {
				return nil
			}
			k++
			continue
		case TOKEN_DOT:
			if p.toks[k+1].Type == TOKEN_IDENT {
				k += 2
				continue
			}
		}
		break
	}
	opTok := p.toks[k].Type
	_, compound := compoundOps[opTok]
	if opTok != TOKEN_UPDATE && !compound {
		return nil
	}
	name := p.advance().Value
	var place Expression = &IdentExpr{Name: name}
	var last func(Expression) Statement
	for p.i < k {
		if p.accept(TOKEN_DOT) {
			field := p.advance().Value
			obj := place
			place = &FieldAccessExpr{Object: obj, FieldName: field, Offset: -1}
			last = func(v Expression) Statement { return &FieldUpdateStmt{Object: obj, Field: field, Value: v} }
			continue
		}
		p.advance()
		idx := nested(p, p.expr)
		p.expect(TOKEN_RBRACKET, "']'")
		obj := place
		place = &IndexExpr{List: obj, Index: idx}
		last = func(v Expression) Statement {
			ident, ok := obj.(*IdentExpr)
			if !ok {
				return &IndexUpdateStmt{Target: obj, Index: idx, Value: v}
			}
			if c, ok := v.(*CastExpr); ok {
				if short, ok := map[string]string{"int8": "i8", "int16": "i16", "int32": "i32", "int64": "i64",
					"uint8": "u8", "uint16": "u16", "uint32": "u32", "uint64": "u64", "float32": "f32", "float64": "f64"}[c.Type]; ok {
					return &ExpressionStmt{Expr: &CallExpr{Function: "write_" + short, Args: []Expression{ident, &CastExpr{Expr: idx, Type: "int32"}, c.Expr}}}
				}
			}
			return &MapUpdateStmt{MapName: ident.Name, Index: idx, Value: v}
		}
	}
	p.advance()
	p.skipNewlines()
	value := p.expr()
	if compound {
		value = &BinaryExpr{Left: place, Operator: compoundOps[opTok], Right: value}
	}
	if last == nil {
		return &AssignStmt{Name: name, Value: value, Mutable: true, IsUpdate: true}
	}
	return last(value)
}

// loop parses `@ [spec] [! bound] block`.
func (p *Parser) loop() Statement {
	p.advance()
	var iterator, iterType string
	var iterable, cond Expression
	switch {
	case p.at(TOKEN_LBRACE):
	case p.at(TOKEN_IDENT) && p.peekAt(1).Type == TOKEN_IN,
		p.at(TOKEN_IDENT) && p.peekAt(1).Type == TOKEN_COLON && p.peekAt(2).Type == TOKEN_IDENT && p.peekAt(3).Type == TOKEN_IN:
		iterator = p.advance().Value
		if p.accept(TOKEN_COLON) {
			iterType = p.advance().Value
		}
		p.advance()
		p.noMatch++
		iterable = p.expr()
		p.noMatch--
	default:
		p.noMatch++
		cond = p.expr()
		p.noMatch--
	}
	maxIter, bounded := int64(math.MaxInt64), false
	if p.accept(TOKEN_BANG) {
		bounded = true
		switch t := p.cur(); t.Type {
		case TOKEN_INF:
			p.advance()
			bounded = false
		case TOKEN_NUMBER:
			n, err := strconv.ParseInt(t.Value, 0, 64)
			if err != nil || n < 1 {
				p.fail("a loop bound must be a positive integer")
			}
			maxIter = n
			p.advance()
		default:
			p.fail("expected a number after '!', found %s", t)
		}
	}
	p.pushScope()
	defer p.popScope()
	if iterator != "" {
		p.declare(iterator)
	}
	p.loops++
	body := p.blockStmts()
	p.loops--
	if iterable != nil {
		return &LoopStmt{Iterator: iterator, IteratorType: iterType, Iterable: iterable, Body: body, MaxIterations: maxIter, NeedsMaxCheck: bounded}
	}
	if cond == nil {
		cond = &NumberExpr{Value: 1}
	}
	return &WhileStmt{Condition: cond, Body: body, MaxIterations: maxIter}
}

func (p *Parser) jump() Statement {
	isBreak := p.advance().Type == TOKEN_BREAK
	word := map[bool]string{true: "break", false: "continue"}[isBreak]
	if p.loops == 0 {
		p.i--
		p.fail("'%s' outside a loop", word)
	}
	label := -1
	if p.accept(TOKEN_AT) {
		t := p.expect(TOKEN_NUMBER, "a loop number after '@'")
		n, err := strconv.Atoi(t.Value)
		if err != nil || n < 1 || n > p.loops {
			p.i--
			p.fail("there is no loop @%s here (loops are numbered 1..%d from the outermost)", t.Value, p.loops)
		}
		label = n
	}
	return &JumpStmt{IsBreak: isBreak, Label: label}
}

func (p *Parser) endsValue() bool {
	switch p.cur().Type {
	case TOKEN_NEWLINE, TOKEN_SEMICOLON, TOKEN_RBRACE, TOKEN_EOF, TOKEN_DEFAULT:
		return true
	case TOKEN_PIPE:
		return p.noPipe > 0
	}
	return false
}

func (p *Parser) ret() Statement {
	isErr := p.advance().Type == TOKEN_ERR
	var value Expression
	if !p.endsValue() {
		value = p.expr()
	}
	if isErr {
		if value == nil {
			value = &StringExpr{Value: "err"}
		}
		value = &CallExpr{Function: "error", Args: []Expression{value}}
	}
	return &JumpStmt{IsBreak: true, Value: value}
}

func (p *Parser) ifStmt() Statement {
	stmt := &IfStmt{}
	for p.at(TOKEN_IF) || p.at(TOKEN_ELIF) && len(stmt.Branches) > 0 {
		p.advance()
		p.noMatch++
		cond := p.expr()
		p.noMatch--
		stmt.Branches = append(stmt.Branches, IfBranch{Condition: cond, Body: p.scopedBlockStmts()})
		if !p.continuesIf() {
			return stmt
		}
		if p.at(TOKEN_ELSE) {
			p.advance()
			stmt.ElseBody = p.scopedBlockStmts()
			return stmt
		}
	}
	return stmt
}

// continuesIf skips line breaks before an elif/else that continues an if.
func (p *Parser) continuesIf() bool {
	k := p.i
	for p.toks[k].Type == TOKEN_NEWLINE {
		k++
	}
	if p.toks[k].Type == TOKEN_ELIF || p.toks[k].Type == TOKEN_ELSE {
		p.i = k
		return true
	}
	return false
}

func (p *Parser) scopedBlockStmts() []Statement {
	p.pushScope()
	defer p.popScope()
	return p.blockStmts()
}

// blockStmts parses `{ stmt ... }`.
func (p *Parser) blockStmts() []Statement {
	p.expect(TOKEN_LBRACE, "'{'")
	return nested(p, func() []Statement {
		var stmts []Statement
		p.skipEnds()
		for !p.at(TOKEN_RBRACE) {
			if p.at(TOKEN_EOF) {
				p.fail("expected '}' to close the block")
			}
			if p.at(TOKEN_PIPE) || p.at(TOKEN_DEFAULT) {
				stmts = append(stmts, p.guardLines()...)
				continue
			}
			if s := p.statement(); s != nil {
				stmts = append(stmts, s)
			}
			p.endStatement()
		}
		p.advance()
		return stmts
	})
}

// guardLines parses `| cond => result` lines inside a block. When they end the
// block they form a guard match that is the block's value; otherwise each one
// is `if cond { result }`.
func (p *Parser) guardLines() []Statement {
	m := &MatchExpr{Condition: &NumberExpr{Value: 1}, DefaultExpr: &NumberExpr{}}
	for p.at(TOKEN_PIPE) {
		p.advance()
		guard := nested(p, p.expr)
		p.expect(TOKEN_FAT_ARROW, "'=>' after the guard")
		m.Clauses = append(m.Clauses, &MatchClause{Guard: guard, Result: p.armResult()})
		p.skipEnds()
	}
	if p.accept(TOKEN_DEFAULT) {
		m.DefaultExplicit = true
		m.DefaultExpr = p.armResult()
		p.skipEnds()
		if !p.at(TOKEN_RBRACE) {
			p.fail("'~>' must be the last line of the block")
		}
	}
	if p.at(TOKEN_RBRACE) {
		return []Statement{&ExpressionStmt{Expr: m}}
	}
	var stmts []Statement
	for _, c := range m.Clauses {
		body := []Statement{&ExpressionStmt{Expr: c.Result}}
		if b, ok := c.Result.(*BlockExpr); ok {
			body = b.Statements
		}
		stmts = append(stmts, &IfStmt{Branches: []IfBranch{{Condition: c.Guard, Body: body}}})
	}
	return stmts
}

// Expressions, from lowest to highest precedence.

func (p *Parser) expr() Expression {
	if l := p.lambda(); l != nil {
		return l
	}
	e := p.pipe()
	for p.at(TOKEN_LBRACE) && p.noMatch == 0 {
		e = p.matchBlock(e)
	}
	return e
}

// lambda parses `x -> body` and `(params) -> body`.
func (p *Parser) lambda() Expression {
	var lambda *LambdaExpr
	switch {
	case p.at(TOKEN_IDENT) && p.peekAt(1).Type == TOKEN_ARROW:
		lambda = &LambdaExpr{Params: []string{p.advance().Value}}
	case p.at(TOKEN_LPAREN):
		close := p.matching(p.i)
		if close < 0 || p.toks[close+1].Type != TOKEN_ARROW {
			return nil
		}
		lambda = p.paramList()
	default:
		return nil
	}
	p.expect(TOKEN_ARROW, "'->'")
	p.skipNewlines()
	lambda.Body = p.functionBody(lambda)
	return lambda
}

func (p *Parser) pipe() Expression {
	left := p.orBang()
	for p.accept(TOKEN_PIPE_FWD) {
		p.skipNewlines()
		switch right := p.orBang().(type) {
		case *CallExpr:
			right.Args = append([]Expression{left}, right.Args...)
			left = right
		case *DirectCallExpr:
			right.Args = append([]Expression{left}, right.Args...)
			left = right
		case *IdentExpr:
			left = &CallExpr{Function: right.Name, Args: []Expression{left}}
		default:
			left = &DirectCallExpr{Callee: right, Args: []Expression{left}}
		}
	}
	return left
}

func (p *Parser) orBang() Expression {
	left := p.or()
	for p.accept(TOKEN_OR_BANG) {
		p.skipNewlines()
		left = &BinaryExpr{Left: left, Operator: "or!", Right: p.or()}
	}
	return left
}

func (p *Parser) or() Expression {
	left := p.and()
	for p.accept(TOKEN_OR) {
		left = &BinaryExpr{Left: left, Operator: "or", Right: p.and()}
	}
	return left
}

func (p *Parser) and() Expression {
	left := p.not()
	for p.accept(TOKEN_AND) {
		left = &BinaryExpr{Left: left, Operator: "and", Right: p.not()}
	}
	return left
}

func (p *Parser) not() Expression {
	if p.accept(TOKEN_NOT) {
		return &UnaryExpr{Operator: "not", Operand: p.not()}
	}
	return p.compare()
}

var compareOps = map[TokenType]string{
	TOKEN_EQ: "==", TOKEN_NE: "!=", TOKEN_LT: "<", TOKEN_LE: "<=", TOKEN_GT: ">", TOKEN_GE: ">=",
}

// compare parses comparison chains: a < b <= c is (a < b) and (b <= c).
func (p *Parser) compare() Expression {
	left := p.rangeExpr()
	var result Expression
	for {
		var cmp Expression
		switch t := p.cur().Type; {
		case compareOps[t] != "":
			p.advance()
			right := p.rangeExpr()
			cmp = &BinaryExpr{Left: left, Operator: compareOps[t], Right: right}
			left = right
		case t == TOKEN_IN:
			p.advance()
			right := p.rangeExpr()
			cmp = &InExpr{Value: left, Container: right}
			left = right
		case t == TOKEN_NOT && p.peekAt(1).Type == TOKEN_IN:
			p.i += 2
			right := p.rangeExpr()
			cmp = &UnaryExpr{Operator: "not", Operand: &InExpr{Value: left, Container: right}}
			left = right
		default:
			if result == nil {
				return left
			}
			return result
		}
		if result == nil {
			result = cmp
		} else {
			result = &BinaryExpr{Left: result, Operator: "and", Right: cmp}
		}
	}
}

func (p *Parser) rangeExpr() Expression {
	start := p.bitOr()
	if p.at(TOKEN_RANGE_EX) || p.at(TOKEN_RANGE_IN) {
		inclusive := p.advance().Type == TOKEN_RANGE_IN
		return &RangeExpr{Start: start, End: p.bitOr(), Inclusive: inclusive}
	}
	return start
}

func (p *Parser) binary(next func() Expression, ops map[TokenType]string) Expression {
	left := next()
	for {
		op, ok := ops[p.cur().Type]
		if !ok || op == "|b" && p.noPipe > 0 {
			return left
		}
		p.advance()
		left = &BinaryExpr{Left: left, Operator: op, Right: next()}
	}
}

func (p *Parser) bitOr() Expression {
	return p.binary(p.bitXor, map[TokenType]string{TOKEN_PIPE: "|b"})
}
func (p *Parser) bitXor() Expression {
	return p.binary(p.bitAnd, map[TokenType]string{TOKEN_CARET: "^b"})
}
func (p *Parser) bitAnd() Expression {
	return p.binary(p.shift, map[TokenType]string{TOKEN_AMP: "&b"})
}
func (p *Parser) shift() Expression {
	return p.binary(p.sum, map[TokenType]string{TOKEN_SHL: "<<b", TOKEN_SHR: ">>b"})
}
func (p *Parser) sum() Expression {
	return p.binary(p.product, map[TokenType]string{TOKEN_PLUS: "+", TOKEN_MINUS: "-"})
}
func (p *Parser) product() Expression {
	return p.binary(p.cast, map[TokenType]string{TOKEN_STAR: "*", TOKEN_SLASH: "/", TOKEN_PERCENT: "%"})
}

var castTypes = map[string]bool{
	"int8": true, "int16": true, "int32": true, "int64": true,
	"uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"float32": true, "float64": true, "cstr": true, "cstring": true, "cptr": true, "ptr": true,
	"num": true, "str": true, "list": true, "map": true, "bool": true, "cbool": true,
	"cint": true, "clong": true, "cfloat": true, "cdouble": true,
}

func (p *Parser) cast() Expression {
	e := p.unary()
	for p.accept(TOKEN_AS) {
		t := p.expect(TOKEN_IDENT, "a type after 'as'").Value
		if canon, ok := cTypeAliases[t]; ok {
			t = canon
		}
		e = &CastExpr{Expr: e, Type: t}
	}
	return e
}

func (p *Parser) unary() Expression {
	switch p.cur().Type {
	case TOKEN_MINUS:
		p.advance()
		operand := p.unary()
		if n, ok := operand.(*NumberExpr); ok {
			if r, exact := n.Rat(); exact {
				return newExactNumber(new(big.Rat).Neg(r))
			}
			return &NumberExpr{Value: -n.Value}
		}
		return &UnaryExpr{Operator: "-", Operand: operand}
	case TOKEN_TILDE:
		p.advance()
		return &UnaryExpr{Operator: "~b", Operand: p.unary()}
	case TOKEN_HASH:
		p.advance()
		return &UnaryExpr{Operator: "#", Operand: p.unary()}
	}
	return p.power()
}

func (p *Parser) power() Expression {
	base := p.postfix()
	if p.accept(TOKEN_POWER) {
		return &BinaryExpr{Left: base, Operator: "**", Right: p.unary()}
	}
	return base
}

func (p *Parser) args() []Expression {
	p.expect(TOKEN_LPAREN, "'('")
	args := nested(p, func() []Expression {
		var args []Expression
		for !p.at(TOKEN_RPAREN) {
			args = append(args, p.expr())
			if !p.accept(TOKEN_COMMA) {
				break
			}
		}
		return args
	})
	p.expect(TOKEN_RPAREN, "')' after arguments")
	if args == nil {
		args = []Expression{}
	}
	return args
}

func (p *Parser) postfix() Expression {
	e := p.primary()
	for {
		switch p.cur().Type {
		case TOKEN_LPAREN:
			args := p.args()
			if id, ok := e.(*IdentExpr); ok {
				op, isBitOp := bitBuiltins[id.Name]
				switch {
				case p.isDeclared(id.Name):
					e = &CallExpr{Function: id.Name, Args: args}
				case id.Name == "vec2" && len(args) == 2, id.Name == "vec4" && len(args) == 4:
					e = &VectorExpr{Components: args, Size: len(args)}
				case isBitOp && len(args) == 2:
					e = &BinaryExpr{Left: args[0], Operator: op, Right: args[1]}
				case id.Name == "random" && len(args) == 0:
					e = &RandomExpr{}
				default:
					e = &CallExpr{Function: id.Name, Args: args}
				}
			} else {
				e = &DirectCallExpr{Callee: e, Args: args}
			}
		case TOKEN_LBRACKET:
			e = p.indexOrSlice(e)
		case TOKEN_DOT:
			e = p.member(e)
		default:
			return e
		}
	}
}

func (p *Parser) indexOrSlice(list Expression) Expression {
	p.advance()
	return nested(p, func() Expression {
		if p.at(TOKEN_RBRACKET) {
			p.fail("empty index")
		}
		var start Expression
		if !p.at(TOKEN_COLON) {
			start = p.expr()
		}
		if p.accept(TOKEN_RBRACKET) {
			if start == nil {
				p.fail("empty index")
			}
			return &IndexExpr{List: list, Index: start}
		}
		p.expect(TOKEN_COLON, "']' or ':'")
		var end Expression
		if !p.at(TOKEN_RBRACKET) {
			end = p.expr()
		}
		p.expect(TOKEN_RBRACKET, "']'")
		return &SliceExpr{List: list, Start: start, End: end}
	})
}

// member parses `.name` after an expression: C namespaces, cstruct
// metadata, method calls, `.error` and field access.
func (p *Parser) member(obj Expression) Expression {
	p.advance()
	field := p.expect(TOKEN_IDENT, "a field or method name after '.'").Value
	id, isIdent := obj.(*IdentExpr)
	if isIdent {
		if decl, ok := p.cstructs[id.Name]; ok {
			if field == "size" {
				return &NumberExpr{Value: float64(decl.Size)}
			}
			for _, f := range decl.Fields {
				if f.Name == field {
					p.expect(TOKEN_DOT, "'.offset'")
					if p.expect(TOKEN_IDENT, "'offset'").Value != "offset" {
						p.i--
						p.fail("expected 'offset' after %s.%s.", decl.Name, field)
					}
					return &NumberExpr{Value: float64(f.Offset)}
				}
			}
			p.i--
			p.fail("cstruct %s has no field '%s'", decl.Name, field)
		}
		if p.isNamespace(id.Name) {
			if p.at(TOKEN_LPAREN) {
				args := p.args()
				if id.Name == "c" || id.Name == "C" {
					return &CallExpr{Function: field, Args: args, IsCFFI: true}
				}
				return &CallExpr{Function: id.Name + "." + field, Args: args}
			}
			return &NamespacedIdentExpr{Namespace: id.Name, Name: field}
		}
	}
	if p.at(TOKEN_LPAREN) {
		args := p.args()
		if isIdent {
			return &CallExpr{Function: id.Name + "." + field, Args: args}
		}
		return &CallExpr{Function: field, Args: append([]Expression{obj}, args...)}
	}
	if field == "error" {
		return &CallExpr{Function: "_error_code_extract", Args: []Expression{obj}}
	}
	return &FieldAccessExpr{Object: obj, FieldName: field, Offset: -1}
}

func (p *Parser) primary() Expression {
	t := p.cur()
	switch t.Type {
	case TOKEN_NUMBER:
		p.advance()
		n, err := parseNumber(t.Value)
		if err != nil {
			p.i--
			p.fail("%v", err)
		}
		return n
	case TOKEN_STRING:
		p.advance()
		return &StringExpr{Value: t.Value}
	case TOKEN_FSTRING:
		p.advance()
		return p.fstring(t)
	case TOKEN_YES, TOKEN_NO:
		p.advance()
		return &BooleanExpr{Value: t.Type == TOKEN_YES}
	case TOKEN_INF:
		p.advance()
		return &NumberExpr{Value: math.Inf(1)}
	case TOKEN_IDENT:
		p.advance()
		return &IdentExpr{Name: t.Value}
	case TOKEN_LPAREN:
		p.advance()
		e := nested(p, func() Expression {
			p.skipNewlines()
			return p.expr()
		})
		p.expect(TOKEN_RPAREN, "')'")
		return e
	case TOKEN_LBRACKET:
		return p.list()
	case TOKEN_LBRACE:
		return p.brace()
	case TOKEN_IF:
		return p.ifExpr()
	case TOKEN_UNSAFE:
		return p.unsafe()
	case TOKEN_ARENA:
		p.advance()
		return &ArenaExpr{Body: p.scopedBlockStmts()}
	case TOKEN_ARROW:
		p.fail("a lambda needs parameters: write () -> ... for none")
	}
	if p.i > 0 && continues[p.toks[p.i-1].Type] && p.toks[p.i-1].Type != TOKEN_LPAREN && p.toks[p.i-1].Type != TOKEN_LBRACKET {
		p.fail("expected an expression after '%s', found %s", p.toks[p.i-1].Value, t)
	}
	p.fail("expected an expression, found %s", t)
	return nil
}

func (p *Parser) list() Expression {
	p.advance()
	return nested(p, func() Expression {
		var elems []Expression
		for !p.at(TOKEN_RBRACKET) {
			elems = append(elems, p.expr())
			if len(elems) == 1 && p.at(TOKEN_AT) {
				return p.comprehension(elems[0])
			}
			if !p.accept(TOKEN_COMMA) {
				break
			}
		}
		p.expect(TOKEN_RBRACKET, "']' or ','")
		if elems == nil {
			elems = []Expression{}
		}
		return &ListExpr{Elements: elems}
	})
}

// comprehension parses the rest of [e @ x in xs if cond] into a block that
// builds the list with a loop.
func (p *Parser) comprehension(elem Expression) Expression {
	p.advance()
	iterator := p.expect(TOKEN_IDENT, "a loop variable").Value
	p.expect(TOKEN_IN, "'in'")
	iterable := p.expr()
	var cond Expression
	if p.accept(TOKEN_IF) {
		cond = p.expr()
	}
	p.expect(TOKEN_RBRACKET, "']'")
	acc := p.tempName()
	var add Statement = &AssignStmt{Name: acc, Mutable: true, IsUpdate: true,
		Value: &BinaryExpr{Left: &IdentExpr{Name: acc}, Operator: "+", Right: &ListExpr{Elements: []Expression{elem}}}}
	if cond != nil {
		add = &IfStmt{Branches: []IfBranch{{Condition: cond, Body: []Statement{add}}}}
	}
	return &BlockExpr{Statements: []Statement{
		&AssignStmt{Name: acc, Mutable: true, Value: &ListExpr{Elements: []Expression{}}},
		&LoopStmt{Iterator: iterator, Iterable: iterable, Body: []Statement{add}, MaxIterations: math.MaxInt64},
		&ExpressionStmt{Expr: &IdentExpr{Name: acc}},
	}}
}

// brace parses a `{` in expression position: a map, a guard match or a block.
func (p *Parser) brace() Expression {
	k := p.i + 1
	for p.toks[k].Type == TOKEN_NEWLINE {
		k++
	}
	first, second := p.toks[k], p.toks[k+1]
	switch {
	case first.Type == TOKEN_RBRACE:
		p.i = k + 1
		return &MapExpr{}
	case (first.Type == TOKEN_IDENT || first.Type == TOKEN_STRING || first.Type == TOKEN_NUMBER) && second.Type == TOKEN_COLON &&
		!(first.Type == TOKEN_IDENT && p.toks[k+2].Type == TOKEN_IDENT && (p.toks[k+3].Type == TOKEN_ASSIGN || p.toks[k+3].Type == TOKEN_DEFINE)):
		return p.mapLiteral()
	}
	stmts := p.scopedBlockStmts()
	if first.Type == TOKEN_PIPE && len(stmts) == 1 {
		if es, ok := stmts[0].(*ExpressionStmt); ok {
			if m, ok := es.Expr.(*MatchExpr); ok {
				return m
			}
		}
	}
	return &BlockExpr{Statements: stmts}
}

func (p *Parser) mapLiteral() Expression {
	p.advance()
	m := &MapExpr{}
	nested(p, func() Expression {
		p.skipNewlines()
		for !p.at(TOKEN_RBRACE) {
			var key Expression
			switch t := p.advance(); t.Type {
			case TOKEN_IDENT:
				key = &NumberExpr{Value: float64(hashStringKey(t.Value))}
			case TOKEN_STRING:
				key = &StringExpr{Value: t.Value}
			case TOKEN_NUMBER:
				n, err := parseNumber(t.Value)
				if err != nil {
					p.fail("%v", err)
				}
				key = n
			default:
				p.i--
				p.fail("expected a map key (a name, string or number), found %s", t)
			}
			p.expect(TOKEN_COLON, "':' after the map key")
			p.skipNewlines()
			m.Keys = append(m.Keys, key)
			m.Values = append(m.Values, p.expr())
			p.skipNewlines()
			if !p.accept(TOKEN_COMMA) {
				break
			}
			p.skipNewlines()
		}
		return nil
	})
	p.expect(TOKEN_RBRACE, "'}' or ',' in map literal")
	return m
}

// matchBlock parses `subject { ... }`: arms, or statements run when the
// subject is truthy.
func (p *Parser) matchBlock(subject Expression) Expression {
	open := p.i
	close := p.matching(open)
	hasArms := false
	depth := 0
	for k := open + 1; close > 0 && k < close; k++ {
		switch p.toks[k].Type {
		case TOKEN_LPAREN, TOKEN_LBRACKET, TOKEN_LBRACE:
			depth++
		case TOKEN_RPAREN, TOKEN_RBRACKET, TOKEN_RBRACE:
			depth--
		case TOKEN_FAT_ARROW, TOKEN_DEFAULT:
			if depth == 0 {
				hasArms = true
			}
		}
	}
	if !hasArms {
		body := &BlockExpr{Statements: p.scopedBlockStmts()}
		return &MatchExpr{Condition: subject, Clauses: []*MatchClause{{Result: body}}, DefaultExpr: &NumberExpr{}}
	}
	p.advance()
	if !hasCall(subject) {
		return p.arms(subject, false)
	}
	tmp := p.tempName()
	m := p.arms(&IdentExpr{Name: tmp}, false)
	return &BlockExpr{Statements: []Statement{
		&AssignStmt{Name: tmp, Value: subject},
		&ExpressionStmt{Expr: m},
	}}
}

// arms parses match arms after the opening brace.
func (p *Parser) arms(subject Expression, guards bool) *MatchExpr {
	m := &MatchExpr{Condition: subject, DefaultExpr: &NumberExpr{}}
	nested(p, func() Expression {
		p.skipEnds()
		for !p.at(TOKEN_RBRACE) {
			switch {
			case p.accept(TOKEN_DEFAULT):
				if m.DefaultExplicit {
					p.i--
					p.fail("a match has only one '~>' arm")
				}
				m.DefaultExplicit = true
				m.DefaultExpr = p.armResult()
			case p.accept(TOKEN_FAT_ARROW):
				m.Clauses = append(m.Clauses, &MatchClause{Result: p.armResult()})
			case p.accept(TOKEN_PIPE):
				guard := p.expr()
				p.expect(TOKEN_FAT_ARROW, "'=>' after the guard")
				m.Clauses = append(m.Clauses, &MatchClause{Guard: guard, Result: p.armResult()})
			default:
				if guards {
					p.fail("expected '| condition =>' or '~>' in a guard match, found %s", p.cur())
				}
				pattern := p.expr()
				p.expect(TOKEN_FAT_ARROW, "'=>' after the pattern")
				m.Clauses = append(m.Clauses, &MatchClause{Guard: matchesPattern(subject, pattern), Result: p.armResult()})
			}
			p.skipEnds()
		}
		return nil
	})
	p.expect(TOKEN_RBRACE, "'}'")
	return m
}

func matchesPattern(subject, pattern Expression) Expression {
	if r, ok := pattern.(*RangeExpr); ok {
		op := "<"
		if r.Inclusive {
			op = "<="
		}
		return &BinaryExpr{Operator: "and",
			Left:  &BinaryExpr{Left: subject, Operator: ">=", Right: r.Start},
			Right: &BinaryExpr{Left: subject, Operator: op, Right: r.End}}
	}
	return &BinaryExpr{Left: subject, Operator: "==", Right: pattern}
}

// armResult parses what follows => or ~>: an expression, a block, or a
// ret/err/break/continue statement.
func (p *Parser) armResult() Expression {
	p.skipNewlines()
	switch p.cur().Type {
	case TOKEN_RET, TOKEN_ERR, TOKEN_BREAK, TOKEN_CONTINUE:
		p.noPipe++
		defer func() { p.noPipe-- }()
		return &BlockExpr{Statements: []Statement{p.statement()}}
	}
	p.noPipe++
	defer func() { p.noPipe-- }()
	return p.expr()
}

func (p *Parser) ifExpr() Expression {
	m := &MatchExpr{Condition: &NumberExpr{Value: 1}, DefaultExpr: &NumberExpr{}}
	for p.at(TOKEN_IF) || p.at(TOKEN_ELIF) && len(m.Clauses) > 0 {
		p.advance()
		p.noMatch++
		cond := p.expr()
		p.noMatch--
		m.Clauses = append(m.Clauses, &MatchClause{Guard: cond, Result: &BlockExpr{Statements: p.scopedBlockStmts()}})
		if !p.continuesIf() {
			return m
		}
		if p.accept(TOKEN_ELSE) {
			m.DefaultExpr = &BlockExpr{Statements: p.scopedBlockStmts()}
			m.DefaultExplicit = true
			return m
		}
	}
	return m
}

// fstring splits an f-string body into literal text and parsed expressions.
func (p *Parser) fstring(t Token) Expression {
	raw := t.Value
	var parts []Expression
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			parts = append(parts, &StringExpr{Value: text.String()})
			text.Reset()
		}
	}
	for i := 0; i < len(raw); {
		switch c := raw[i]; {
		case c == '\\':
			s, n, msg := unescape(raw[i:])
			if msg != "" {
				p.i--
				p.fail("%s", msg)
			}
			text.WriteString(s)
			i += n
		case strings.HasPrefix(raw[i:], "{{"), strings.HasPrefix(raw[i:], "}}"):
			text.WriteByte(c)
			i += 2
		case c == '{':
			end := i + 1
			for depth := 1; end < len(raw); end++ {
				if raw[end] == '"' {
					for end++; end < len(raw) && raw[end] != '"'; end++ {
						if raw[end] == '\\' {
							end++
						}
					}
					continue
				}
				if raw[end] == '{' {
					depth++
				} else if raw[end] == '}' {
					if depth--; depth == 0 {
						break
					}
				}
			}
			flush()
			parts = append(parts, p.subExpression(raw[i+1:end], t))
			i = end + 1
		case c == '}':
			p.i--
			p.fail("unmatched '}' in f-string (write '}}' for a brace)")
		default:
			text.WriteByte(c)
			i++
		}
	}
	flush()
	if len(parts) == 0 {
		return &StringExpr{}
	}
	if s, ok := parts[0].(*StringExpr); ok && len(parts) == 1 {
		return s
	}
	return &FStringExpr{Parts: parts}
}

// subExpression parses the source of an f-string interpolation.
func (p *Parser) subExpression(src string, at Token) Expression {
	toks, lexErr := Lex(src)
	if lexErr != nil || strings.TrimSpace(src) == "" {
		p.i--
		if lexErr != nil {
			p.fail("in f-string: %s", lexErr.Msg)
		}
		p.fail("empty '{}' in f-string")
	}
	for i := range toks {
		toks[i].Line, toks[i].Column = at.Line, at.Column
	}
	sub := *p
	sub.toks, sub.i = toks, 0
	e := sub.expr()
	sub.skipNewlines()
	if !sub.at(TOKEN_EOF) {
		p.i--
		p.fail("unexpected %s in f-string expression", sub.cur())
	}
	p.tmp = sub.tmp
	return e
}

// unsafe parses `unsafe [T] {x86_64} {arm64} {riscv64} [as T]`.
func (p *Parser) unsafe() Expression {
	p.advance()
	ret := "uint64"
	if p.at(TOKEN_IDENT) && castTypes[p.cur().Value] {
		ret = p.advance().Value
	}
	var blocks [3][]Statement
	for i := range blocks {
		blocks[i] = p.unsafeBlock()
	}
	if p.accept(TOKEN_AS) {
		ret = p.expect(TOKEN_IDENT, "a type").Value
	}
	return &UnsafeExpr{
		X86_64Block: blocks[0], ARM64Block: blocks[1], RISCV64Block: blocks[2],
		X86_64Return:  &UnsafeReturnStmt{Register: "rax", AsType: ret},
		ARM64Return:   &UnsafeReturnStmt{Register: "x0", AsType: ret},
		RISCV64Return: &UnsafeReturnStmt{Register: "a0", AsType: ret},
	}
}

var unsafeOps = map[TokenType]string{
	TOKEN_PLUS: "+", TOKEN_MINUS: "-", TOKEN_STAR: "*", TOKEN_SLASH: "/", TOKEN_PERCENT: "%",
	TOKEN_AMP: "&", TOKEN_PIPE: "|", TOKEN_CARET: "^b", TOKEN_SHL: "<<", TOKEN_SHR: ">>",
}

func (p *Parser) unsafeBlock() []Statement {
	p.expect(TOKEN_LBRACE, "'{' for an architecture block")
	var stmts []Statement
	p.skipEnds()
	for !p.accept(TOKEN_RBRACE) {
		switch {
		case p.at(TOKEN_IDENT) && p.cur().Value == "syscall":
			p.advance()
			stmts = append(stmts, &SyscallStmt{})
		case p.at(TOKEN_LBRACKET):
			reg, off := p.unsafeAddress()
			p.expect(TOKEN_UPDATE, "'<-'")
			var value any
			if p.at(TOKEN_NUMBER) {
				value = p.unsafeNumber()
			} else {
				value = p.expect(TOKEN_IDENT, "a register or number").Value
			}
			size := "uint64"
			if p.accept(TOKEN_AS) {
				size = p.expect(TOKEN_IDENT, "a type").Value
			}
			stmts = append(stmts, &MemoryStore{Size: size, Address: reg, Offset: off, Value: value})
		default:
			reg := p.expect(TOKEN_IDENT, "a register, memory address or 'syscall'").Value
			p.expect(TOKEN_UPDATE, "'<-'")
			stmts = append(stmts, &RegisterAssignStmt{Register: reg, Value: p.unsafeValue()})
		}
		p.endStatement()
	}
	return stmts
}

func (p *Parser) unsafeNumber() *NumberExpr {
	n, err := parseNumber(p.advance().Value)
	if err != nil {
		p.i--
		p.fail("%v", err)
	}
	return n
}

func (p *Parser) unsafeAddress() (string, int64) {
	p.expect(TOKEN_LBRACKET, "'['")
	reg := p.expect(TOKEN_IDENT, "a register").Value
	var off int64
	if p.at(TOKEN_PLUS) || p.at(TOKEN_MINUS) {
		neg := p.advance().Type == TOKEN_MINUS
		off = int64(p.unsafeNumber().Value)
		if neg {
			off = -off
		}
	}
	p.expect(TOKEN_RBRACKET, "']'")
	return reg, off
}

func (p *Parser) unsafeValue() any {
	if p.at(TOKEN_LBRACKET) {
		reg, off := p.unsafeAddress()
		size := "uint64"
		if p.accept(TOKEN_AS) {
			size = p.expect(TOKEN_IDENT, "a type").Value
		}
		return &MemoryLoad{Size: size, Address: reg, Offset: off}
	}
	if p.accept(TOKEN_TILDE) {
		return &RegisterOp{Operator: "~b", Right: p.expect(TOKEN_IDENT, "a register").Value}
	}
	var left string
	var imm *NumberExpr
	if p.at(TOKEN_NUMBER) {
		imm = p.unsafeNumber()
	} else {
		left = p.expect(TOKEN_IDENT, "a register, variable or number").Value
	}
	if p.accept(TOKEN_AS) {
		t := p.expect(TOKEN_IDENT, "a type").Value
		if imm != nil {
			return &CastExpr{Expr: imm, Type: t}
		}
		return &CastExpr{Expr: &IdentExpr{Name: left}, Type: t}
	}
	if op, ok := unsafeOps[p.cur().Type]; ok {
		if imm != nil {
			p.fail("the left operand of a register operation must be a register")
		}
		p.advance()
		var right any
		if p.at(TOKEN_NUMBER) {
			right = p.unsafeNumber()
		} else {
			right = p.expect(TOKEN_IDENT, "a register or number").Value
		}
		return &RegisterOp{Left: left, Operator: op, Right: right}
	}
	if imm != nil {
		return imm
	}
	if isRegisterName(left) {
		return left
	}
	return &IdentExpr{Name: left}
}

var registerNames = map[string]bool{}

func init() {
	for _, group := range []string{
		"rax rbx rcx rdx rsi rdi rbp rsp r8 r9 r10 r11 r12 r13 r14 r15 eax ebx ecx edx esi edi ebp esp " +
			"ax bx cx dx si di bp sp al bl cl dl sil dil bpl spl stack",
		"xmm0 xmm1 xmm2 xmm3 xmm4 xmm5 xmm6 xmm7 xmm8 xmm9 xmm10 xmm11 xmm12 xmm13 xmm14 xmm15",
		"x0 x1 x2 x3 x4 x5 x6 x7 x8 x9 x10 x11 x12 x13 x14 x15 x16 x17 x18 x19 x20 x21 x22 x23 x24 x25 " +
			"x26 x27 x28 x29 x30 xzr lr w0 w1 w2 w3 w4 w5 w6 w7 v0 v1 v2 v3 v4 v5 v6 v7",
		"a0 a1 a2 a3 a4 a5 a6 a7 t0 t1 t2 t3 t4 t5 t6 s0 s1 s2 s3 s4 s5 s6 s7 s8 s9 s10 s11 ra gp tp " +
			"fa0 fa1 fa2 fa3 fa4 fa5 fa6 fa7",
	} {
		for _, r := range strings.Fields(group) {
			registerNames[r] = true
		}
	}
}

func isRegisterName(name string) bool { return registerNames[name] }

// hasCall reports whether evaluating e may call a function, so it must not be
// evaluated more than once.
func hasCall(e Expression) bool {
	switch e := e.(type) {
	case nil, *NumberExpr, *StringExpr, *BooleanExpr, *IdentExpr, *NamespacedIdentExpr:
		return false
	case *BinaryExpr:
		return hasCall(e.Left) || hasCall(e.Right)
	case *UnaryExpr:
		return hasCall(e.Operand)
	case *CastExpr:
		return hasCall(e.Expr)
	case *FieldAccessExpr:
		return hasCall(e.Object)
	case *IndexExpr:
		return hasCall(e.List) || hasCall(e.Index)
	case *InExpr:
		return hasCall(e.Value) || hasCall(e.Container)
	case *RangeExpr:
		return hasCall(e.Start) || hasCall(e.End)
	}
	return true
}
