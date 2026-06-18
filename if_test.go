package main

import "testing"

func TestIfLexing(t *testing.T) {
	tests := []struct {
		input    string
		expected TokenType
	}{
		{"if", TOKEN_IF},
		{"elif", TOKEN_ELIF},
		{"else", TOKEN_ELSE},
	}

	for _, tt := range tests {
		lexer := &Lexer{input: tt.input}
		token := lexer.NextToken()
		if token.Type != tt.expected {
			t.Fatalf("expected token %v, got %v", tt.expected, token.Type)
		}
	}
}

func TestIfParsing(t *testing.T) {
	parser := NewParser(`main = {
if 1 {
    println(1)
} elif 0 {
    println(2)
} else {
    println(3)
}
}`)
	program := parser.ParseProgram()
	if len(program.Statements) != 1 {
		t.Fatalf("expected 1 top-level statement, got %d", len(program.Statements))
	}

	assign, ok := program.Statements[0].(*AssignStmt)
	if !ok {
		t.Fatalf("expected AssignStmt, got %T", program.Statements[0])
	}
	lambda, ok := assign.Value.(*LambdaExpr)
	if !ok {
		t.Fatalf("expected LambdaExpr main body, got %T", assign.Value)
	}
	body, ok := lambda.Body.(*BlockExpr)
	if !ok {
		t.Fatalf("expected BlockExpr lambda body, got %T", lambda.Body)
	}
	if len(body.Statements) != 1 {
		t.Fatalf("expected 1 statement in main body, got %d", len(body.Statements))
	}

	ifStmt, ok := body.Statements[0].(*IfStmt)
	if !ok {
		t.Fatalf("expected IfStmt, got %T", body.Statements[0])
	}
	if len(ifStmt.Branches) != 2 {
		t.Fatalf("expected 2 branches (if + elif), got %d", len(ifStmt.Branches))
	}
	if len(ifStmt.ElseBody) != 1 {
		t.Fatalf("expected else body with 1 statement, got %d", len(ifStmt.ElseBody))
	}
}

func TestIfCompilation(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		expected string
	}{
		{
			name: "if branch",
			source: `x := 0
if 1 {
    x <- 10
} elif 1 {
    x <- 20
} else {
    x <- 30
}
println(x)
`,
			expected: "10\n",
		},
		{
			name: "elif branch",
			source: `x := 0
if 0 {
    x <- 10
} elif 1 {
    x <- 20
} else {
    x <- 30
}
println(x)
`,
			expected: "20\n",
		},
		{
			name: "else branch",
			source: `x := 0
if 0 {
    x <- 10
} elif 0 {
    x <- 20
} else {
    x <- 30
}
println(x)
`,
			expected: "30\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := compileAndRun(t, tt.source)
			if output != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, output)
			}
		})
	}
}
