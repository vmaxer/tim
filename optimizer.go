// Completion: 95% - Peephole optimization implemented and working
package main

import (
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
)

// optimizer.go - Compiler optimization passes
//
// This file contains all optimization transformations applied to the AST
// before code generation. Optimizations include:
// - Constant folding and propagation
// - Strength reduction (expensive ops → cheaper ops)
// - Dead code elimination
// - Function inlining
// - Purity analysis
// - Closure analysis

// optimizerCStructNames holds the cstruct type names of the program being
// optimized, so the inliner can recognize constructor calls.
var optimizerCStructNames map[string]bool

// optimizerCStructDecls maps cstruct name -> declaration (for SROA field info).
var optimizerCStructDecls map[string]*CStructDecl

func isCStructConstructorCall(e Expression) bool {
	if c, ok := e.(*CallExpr); ok {
		return optimizerCStructNames[c.Function]
	}
	return false
}

func optimizeProgram(program *Program) *Program {
	optimizerCStructNames = make(map[string]bool, len(program.CStructs))
	for name := range program.CStructs {
		optimizerCStructNames[name] = true
	}
	optimizerCStructDecls = program.CStructs
	inlineReinlineDepth = 0
	inlineTempCounter = 0

	// Pass 1: Constant folding (2 + 3 → 5)
	for i, stmt := range program.Statements {
		program.Statements[i] = foldConstants(stmt)
	}

	// Pass 2: Constant propagation (x = 5; y = x + 1 → y = 6)
	constMap := make(map[string]*NumberExpr)
	for i, stmt := range program.Statements {
		program.Statements[i] = propagateConstants(stmt, constMap)
	}

	// Pass 3: Dead code elimination (remove unused variables, unreachable code)
	// DISABLED: This was removing unused definitions before sibling files were loaded
	// DCE now runs in the WPO phase (optimizer.go) after all files are combined
	// usedVars := make(map[string]bool)
	// for _, stmt := range program.Statements {
	// 	collectUsedVariables(stmt, usedVars)
	// }
	// newStmts := make([]Statement, 0, len(program.Statements))
	// for _, stmt := range program.Statements {
	// 	if keep := eliminateDeadCode(stmt, usedVars); keep != nil {
	// 		newStmts = append(newStmts, keep)
	// 	}
	// }
	// program.Statements = newStmts

	// Pass 4: Analyze lambda purity (for future memoization)
	pureFunctions := make(map[string]bool) // Track which named functions are pure
	for _, stmt := range program.Statements {
		analyzePurity(stmt, pureFunctions)
	}

	// Pass 4.5: Operator overloading for cstructs. `a + b` on two V-typed operands
	// desugars to `V_add(a, b)` (and `*`→V_mul/V_scale, `-`→V_sub, `/`→V_div) when
	// that function is defined. Runs BEFORE inlining so the operator call inlines
	// like any other. Conservative: only fires when operand types are known from
	// explicit annotations / constructors / casts, so scalar arithmetic is never
	// touched.
	if len(optimizerCStructDecls) > 0 {
		desugarOperatorOverloads(program)
	}

	// Pass 5: Function inlining (substitute small function calls with their bodies)
	inlineCandidates := make(map[string]*LambdaExpr) // Functions that can be inlined
	callCounts := make(map[string]int)               // Number of times each function is called

	// Identify inline candidates
	for _, stmt := range program.Statements {
		collectInlineCandidates(stmt, inlineCandidates)
	}

	// Count call sites for each candidate
	for _, stmt := range program.Statements {
		countCalls(stmt, callCounts)
	}

	// Inline function calls
	for i, stmt := range program.Statements {
		program.Statements[i] = inlineFunctions(stmt, inlineCandidates, callCounts)
	}

	// Pass 6: Constant folding after inlining (fold inlined expressions)
	for i, stmt := range program.Statements {
		program.Statements[i] = foldConstants(stmt)
	}

	// Pass 6b: Scalar replacement of aggregates (SROA). A non-escaping local
	// `p = Struct(a, b, c)` used only via `p.field` is replaced by per-field
	// scalars — no heap allocation, no pointer round-trip, fields stay in
	// registers. This is the dominant win for struct-heavy numeric code (the
	// metaballs v3 temporaries).
	if len(optimizerCStructDecls) > 0 {
		for _, stmt := range program.Statements {
			sroaStmt(stmt)
		}
	}

	// Pass 7: Loop vectorization (convert scalar loops to SIMD)
	for i, stmt := range program.Statements {
		program.Statements[i] = vectorizeLoops(stmt)
	}

	return program
}

// foldConstants performs constant folding on statements
func foldConstants(stmt Statement) Statement {
	switch s := stmt.(type) {
	case *AssignStmt:
		s.Value = foldConstantExpr(s.Value)
		return s
	case *ExpressionStmt:
		s.Expr = foldConstantExpr(s.Expr)
		return s
	case *LoopStmt:
		s.Iterable = foldConstantExpr(s.Iterable)
		for i, st := range s.Body {
			s.Body[i] = foldConstants(st)
		}
		return s
	default:
		return stmt
	}
}

// foldConstantExpr performs constant folding on expressions
func foldConstantExpr(expr Expression) Expression {
	switch e := expr.(type) {
	case *BinaryExpr:
		// Fold left and right first
		e.Left = foldConstantExpr(e.Left)
		e.Right = foldConstantExpr(e.Right)

		// Detect FMA patterns: a * b + c or a * b - c
		// Transform into FMAExpr for later code generation optimization
		if e.Operator == "+" || e.Operator == "-" {
			if mulExpr, ok := e.Left.(*BinaryExpr); ok && mulExpr.Operator == "*" {
				// Pattern: (a * b) + c  or  (a * b) - c
				return &FMAExpr{
					A:        mulExpr.Left,
					B:        mulExpr.Right,
					C:        e.Right,
					IsSub:    e.Operator == "-", // true for FMSUB
					IsNegMul: false,
				}
			}
			// Also check: c + (a * b)
			if e.Operator == "+" {
				if mulExpr, ok := e.Right.(*BinaryExpr); ok && mulExpr.Operator == "*" {
					// Pattern: c + (a * b)
					return &FMAExpr{
						A:        mulExpr.Left,
						B:        mulExpr.Right,
						C:        e.Left,
						IsSub:    false,
						IsNegMul: false,
					}
				}
			}
		}

		// Check if both operands are now constants
		leftNum, leftOk := e.Left.(*NumberExpr)
		rightNum, rightOk := e.Right.(*NumberExpr)

		if leftOk && rightOk {
			// Both are constants - fold them
			var result float64
			switch e.Operator {
			case "+":
				result = leftNum.Value + rightNum.Value
			case "-":
				result = leftNum.Value - rightNum.Value
			case "*":
				result = leftNum.Value * rightNum.Value
			case "/":
				if rightNum.Value == 0 {
					// Don't fold constant division by zero - let runtime handle it
					// This allows error handling with or! operator
					return e
				}
				result = leftNum.Value / rightNum.Value
			case "mod", "%":
				if rightNum.Value == 0 {
					// Don't fold constant modulo by zero - let runtime handle it
					// This allows error handling with or! operator
					return e
				}
				result = math.Mod(leftNum.Value, rightNum.Value)
			default:
				return e // Don't fold comparisons
			}
			return &NumberExpr{Value: result}
		}
		return e

	case *CallExpr:
		// Fold arguments
		for i, arg := range e.Args {
			e.Args[i] = foldConstantExpr(arg)
		}
		return e

	case *RangeExpr:
		// Fold range start and end
		e.Start = foldConstantExpr(e.Start)
		e.End = foldConstantExpr(e.End)
		return e

	case *ListExpr:
		// Fold list elements
		for i, elem := range e.Elements {
			e.Elements[i] = foldConstantExpr(elem)
		}
		return e

	case *MapExpr:
		for i := range e.Keys {
			e.Keys[i] = foldConstantExpr(e.Keys[i])
			e.Values[i] = foldConstantExpr(e.Values[i])
		}
		return e
	case *IndexExpr:
		e.List = foldConstantExpr(e.List)
		e.Index = foldConstantExpr(e.Index)
		return e

	case *LambdaExpr:
		e.Body = foldConstantExpr(e.Body)
		return e

	case *ParallelExpr:
		e.List = foldConstantExpr(e.List)
		e.Operation = foldConstantExpr(e.Operation)
		return e

	case *PipeExpr:
		e.Left = foldConstantExpr(e.Left)
		e.Right = foldConstantExpr(e.Right)
		return e

	case *InExpr:
		e.Value = foldConstantExpr(e.Value)
		e.Container = foldConstantExpr(e.Container)
		return e

	case *LengthExpr:
		e.Operand = foldConstantExpr(e.Operand)
		return e

	case *MatchExpr:
		e.Condition = foldConstantExpr(e.Condition)
		for _, clause := range e.Clauses {
			if clause.Guard != nil {
				clause.Guard = foldConstantExpr(clause.Guard)
			}
			clause.Result = foldConstantExpr(clause.Result)
		}
		if e.DefaultExpr != nil {
			e.DefaultExpr = foldConstantExpr(e.DefaultExpr)
		}
		return e

	default:
		return expr
	}
}

// areExpressionsEqual checks if two expressions are structurally equal
// This is a simple structural comparison, not semantic equivalence
func areExpressionsEqual(e1, e2 Expression) bool {
	if e1 == nil || e2 == nil {
		return e1 == e2
	}

	switch expr1 := e1.(type) {
	case *NumberExpr:
		if expr2, ok := e2.(*NumberExpr); ok {
			return expr1.Value == expr2.Value
		}
	case *IdentExpr:
		if expr2, ok := e2.(*IdentExpr); ok {
			return expr1.Name == expr2.Name
		}
	case *BinaryExpr:
		if expr2, ok := e2.(*BinaryExpr); ok {
			return expr1.Operator == expr2.Operator &&
				areExpressionsEqual(expr1.Left, expr2.Left) &&
				areExpressionsEqual(expr1.Right, expr2.Right)
		}
	case *UnaryExpr:
		if expr2, ok := e2.(*UnaryExpr); ok {
			return expr1.Operator == expr2.Operator &&
				areExpressionsEqual(expr1.Operand, expr2.Operand)
		}
	case *CallExpr:
		if expr2, ok := e2.(*CallExpr); ok {
			if expr1.Function != expr2.Function || len(expr1.Args) != len(expr2.Args) {
				return false
			}
			for i := range expr1.Args {
				if !areExpressionsEqual(expr1.Args[i], expr2.Args[i]) {
					return false
				}
			}
			return true
		}
	}
	return false
}

// invertComparison inverts a comparison operator for not(comparison) optimization
// Returns nil if the expression is not a comparison that can be inverted
func invertComparison(expr *BinaryExpr) Expression {
	var newOp string
	switch expr.Operator {
	case "<":
		newOp = ">="
	case "<=":
		newOp = ">"
	case ">":
		newOp = "<="
	case ">=":
		newOp = "<"
	case "==":
		newOp = "!="
	case "!=":
		newOp = "=="
	default:
		return nil
	}

	return &BinaryExpr{
		Left:     expr.Left,
		Operator: newOp,
		Right:    expr.Right,
	}
}

// isPowerOfTwo checks if a float64 value is a power of 2
func isPowerOfTwo(x float64) bool {
	if x <= 0 {
		return false
	}
	// Check if x is an integer
	if x != math.Floor(x) {
		return false
	}
	// Check if it's a power of 2: x & (x-1) == 0
	ix := int64(x)
	return (ix & (ix - 1)) == 0
}

// strengthReduceExpr performs strength reduction and peephole optimization on expressions
// Replaces expensive operations with cheaper equivalent ones:
// - x * 2^n → x << n (multiply by power of 2 → left shift)
// - x / 2^n → x >> n (divide by power of 2 → right shift)
// - x * 0 → 0, x * 1 → x (identity elimination)
// - x + 0, x - 0 → x (identity elimination)
// - x % 2^n → x & (2^n - 1) (modulo by power of 2 → bitwise AND)
// - x == x → true, x != x → false (self-comparison)
// - x < x, x > x → false (self-comparison)
// - Constant comparisons → evaluated result
// - false and x → false, x and false → false (short-circuit)
// - true or x → true, x or true → true (short-circuit)
// - not(true) → false, not(false) → true (constant folding)
// - not(comparison) → inverted comparison (e.g., not(x < y) → x >= y)
// - not(not(x)) → (x != 0) [converts double negation to boolean comparison]
// - (not x) and (not y) → not(x or y) (De Morgan's law - saves one not, preserves short-circuit)
// Note: We don't apply (not x) or (not y) → not(x and y) to preserve short-circuit evaluation
func strengthReduceExpr(expr Expression) Expression {
	if expr == nil {
		return nil
	}

	switch e := expr.(type) {
	case *BinaryExpr:
		// Recursively apply strength reduction to operands first
		e.Left = strengthReduceExpr(e.Left)
		e.Right = strengthReduceExpr(e.Right)

		// Check for patterns we can optimize
		leftNum, leftIsNum := e.Left.(*NumberExpr)
		rightNum, rightIsNum := e.Right.(*NumberExpr)

		switch e.Operator {
		case "*":
			// x * 0 → 0
			if (leftIsNum && leftNum.Value == 0) || (rightIsNum && rightNum.Value == 0) {
				return &NumberExpr{Value: 0}
			}

			// x * 1 → x
			if rightIsNum && rightNum.Value == 1 {
				return e.Left
			}
			if leftIsNum && leftNum.Value == 1 {
				return e.Right
			}

			// x * -1 → -x
			if rightIsNum && rightNum.Value == -1 {
				return &UnaryExpr{Operator: "-", Operand: e.Left}
			}
			if leftIsNum && leftNum.Value == -1 {
				return &UnaryExpr{Operator: "-", Operand: e.Right}
			}

			// x * 2^n → x << n (only for positive integer powers of 2)
			// DISABLED: Infrastructure in place, but context detection needs more work.
			// This optimization only makes sense for integer-heavy code, which is rare in Tim.
			// Users needing integer performance can use unsafe blocks with inline assembly.
			// TODO: Fix context detection if integer optimizations become important.
			/*
				if shouldApplyIntegerOptimization(e.Left, e.Right) {
					if rightIsNum && rightNum.Value > 0 && isPowerOfTwo(rightNum.Value) {
						shift := math.Log2(rightNum.Value)
						return &BinaryExpr{
							Left:     e.Left,
							Operator: "<<",
							Right:    &NumberExpr{Value: shift},
						}
					}
					if leftIsNum && leftNum.Value > 0 && isPowerOfTwo(leftNum.Value) {
						shift := math.Log2(leftNum.Value)
						return &BinaryExpr{
							Left:     e.Right,
							Operator: "<<",
							Right:    &NumberExpr{Value: shift},
						}
					}
				}
			*/

		case "/":
			// 0 / x → 0 (except 0/0 which is undefined)
			if leftIsNum && leftNum.Value == 0 && rightIsNum && rightNum.Value != 0 {
				return &NumberExpr{Value: 0}
			}
			if leftIsNum && leftNum.Value == 0 && !rightIsNum {
				// 0 / x where x is not a constant - assume x != 0 and optimize
				return &NumberExpr{Value: 0}
			}

			// x / x → 1 (for non-zero x)
			if areExpressionsEqual(e.Left, e.Right) {
				// Only safe if we know x != 0
				// For simplicity, don't optimize this - could cause issues with x=0
			}

			// x / 1 → x
			if rightIsNum && rightNum.Value == 1 {
				return e.Left
			}

			// x / -1 → -x
			if rightIsNum && rightNum.Value == -1 {
				return &UnaryExpr{Operator: "-", Operand: e.Left}
			}

			// x / 2^n → x >> n (only for positive powers of 2)
			// DISABLED: Infrastructure in place, but context detection needs more work.
			// See comment above for multiply optimization.
			/*
				if shouldApplyIntegerOptimization(e.Left, e.Right) {
					if rightIsNum && rightNum.Value > 0 && isPowerOfTwo(rightNum.Value) {
						shift := math.Log2(rightNum.Value)
						return &BinaryExpr{
							Left:     e.Left,
							Operator: ">>",
							Right:    &NumberExpr{Value: shift},
						}
					}
				}
			*/

		case "+":
			// x + 0 → x
			if rightIsNum && rightNum.Value == 0 {
				return e.Left
			}
			if leftIsNum && leftNum.Value == 0 {
				return e.Right
			}

		case "-":
			// x - 0 → x
			if rightIsNum && rightNum.Value == 0 {
				return e.Left
			}

			// 0 - x → -x
			if leftIsNum && leftNum.Value == 0 {
				return &UnaryExpr{Operator: "-", Operand: e.Right}
			}

		case "&":
			// x & 0 → 0
			if (leftIsNum && leftNum.Value == 0) || (rightIsNum && rightNum.Value == 0) {
				return &NumberExpr{Value: 0}
			}

		case "|":
			// x | 0 → x
			if rightIsNum && rightNum.Value == 0 {
				return e.Left
			}
			if leftIsNum && leftNum.Value == 0 {
				return e.Right
			}

		case "^":
			// x ^ 0 → x
			if rightIsNum && rightNum.Value == 0 {
				return e.Left
			}
			if leftIsNum && leftNum.Value == 0 {
				return e.Right
			}

		case "<<", ">>":
			// x << 0 → x, x >> 0 → x
			if rightIsNum && rightNum.Value == 0 {
				return e.Left
			}

			// 0 << x → 0, 0 >> x → 0
			if leftIsNum && leftNum.Value == 0 {
				return &NumberExpr{Value: 0}
			}

		case "mod", "%":
			// x % 1 → 0
			if rightIsNum && rightNum.Value == 1 {
				return &NumberExpr{Value: 0}
			}

			// 0 % x → 0
			if leftIsNum && leftNum.Value == 0 {
				return &NumberExpr{Value: 0}
			}

			// x % 2^n → x & (2^n - 1) for positive powers of 2
			// DISABLED: Infrastructure in place, but context detection needs more work.
			// See comment above for multiply optimization.
			/*
				if shouldApplyIntegerOptimization(e.Left, e.Right) {
					if rightIsNum && rightNum.Value > 0 && isPowerOfTwo(rightNum.Value) {
						mask := rightNum.Value - 1
						return &BinaryExpr{
							Left:     e.Left,
							Operator: "&",
							Right:    &NumberExpr{Value: mask},
						}
					}
				}
			*/

		// Peephole optimizations for comparison operators
		case "<", "<=", ">", ">=", "==", "!=":
			// x == x → true (1.0)
			if e.Operator == "==" {
				if areExpressionsEqual(e.Left, e.Right) {
					return &NumberExpr{Value: 1.0}
				}
			}

			// x != x → false (0.0)
			if e.Operator == "!=" {
				if areExpressionsEqual(e.Left, e.Right) {
					return &NumberExpr{Value: 0.0}
				}
			}

			// x < x, x > x → false (0.0)
			if e.Operator == "<" || e.Operator == ">" {
				if areExpressionsEqual(e.Left, e.Right) {
					return &NumberExpr{Value: 0.0}
				}
			}

			// x <= x, x >= x → true (1.0)
			if e.Operator == "<=" || e.Operator == ">=" {
				if areExpressionsEqual(e.Left, e.Right) {
					return &NumberExpr{Value: 1.0}
				}
			}

			// Constant comparisons
			if leftIsNum && rightIsNum {
				var result bool
				switch e.Operator {
				case "<":
					result = leftNum.Value < rightNum.Value
				case "<=":
					result = leftNum.Value <= rightNum.Value
				case ">":
					result = leftNum.Value > rightNum.Value
				case ">=":
					result = leftNum.Value >= rightNum.Value
				case "==":
					result = leftNum.Value == rightNum.Value
				case "!=":
					result = leftNum.Value != rightNum.Value
				}
				if result {
					return &NumberExpr{Value: 1.0}
				}
				return &NumberExpr{Value: 0.0}
			}
		}

		// Peephole optimizations for logical operators (handled via CallExpr for 'and', 'or', 'not')
		return e

	case *UnaryExpr:
		e.Operand = strengthReduceExpr(e.Operand)

		// Double negation: -(-x) → x
		if e.Operator == "-" {
			if inner, ok := e.Operand.(*UnaryExpr); ok && inner.Operator == "-" {
				return inner.Operand
			}
		}

		return e

	case *CallExpr:
		for i, arg := range e.Args {
			e.Args[i] = strengthReduceExpr(arg)
		}

		// Peephole optimizations for logical operators
		// Note: and/or in Tim are boolean operators that return 0 or 1, not value-selecting
		if e.Function == "and" && len(e.Args) == 2 {
			leftNum, leftIsNum := e.Args[0].(*NumberExpr)
			rightNum, rightIsNum := e.Args[1].(*NumberExpr)

			// false and x → false (0.0)
			if leftIsNum && leftNum.Value == 0 {
				return &NumberExpr{Value: 0.0}
			}

			// x and false → false (0.0)
			if rightIsNum && rightNum.Value == 0 {
				return &NumberExpr{Value: 0.0}
			}

			// true and true → true (1.0)
			if leftIsNum && leftNum.Value != 0 && rightIsNum && rightNum.Value != 0 {
				return &NumberExpr{Value: 1.0}
			}

			// De Morgan's law: (not x) and (not y) → not(x or y) [saves one not]
			leftNot, leftIsNot := e.Args[0].(*CallExpr)
			rightNot, rightIsNot := e.Args[1].(*CallExpr)
			if leftIsNot && rightIsNot &&
				leftNot.Function == "not" && len(leftNot.Args) == 1 &&
				rightNot.Function == "not" && len(rightNot.Args) == 1 {
				return &CallExpr{
					Function: "not",
					Args: []Expression{
						&CallExpr{
							Function: "or",
							Args:     []Expression{leftNot.Args[0], rightNot.Args[0]},
						},
					},
				}
			}

			// x and x → (x != 0) ? 1.0 : 0.0 which is essentially bool(x)
			// For simplicity, we don't optimize this case since it requires context
		}

		if e.Function == "or" && len(e.Args) == 2 {
			leftNum, leftIsNum := e.Args[0].(*NumberExpr)
			rightNum, rightIsNum := e.Args[1].(*NumberExpr)

			// true or x → true (1.0)
			if leftIsNum && leftNum.Value != 0 {
				return &NumberExpr{Value: 1.0}
			}

			// x or true → true (1.0)
			if rightIsNum && rightNum.Value != 0 {
				return &NumberExpr{Value: 1.0}
			}

			// false or false → false (0.0)
			if leftIsNum && leftNum.Value == 0 && rightIsNum && rightNum.Value == 0 {
				return &NumberExpr{Value: 0.0}
			}

			// Note: We don't apply De Morgan's law for (not x) or (not y) → not(x and y)
			// because that would prevent short-circuit evaluation.
			// With (not x) or (not y), if (not x) is true, we don't need to evaluate (not y).
			// With not(x and y), we must evaluate both x and y before the and operation.

			// x or x → (x != 0) ? 1.0 : 0.0 which is essentially bool(x)
			// For simplicity, we don't optimize this case since it requires context
		}

		if e.Function == "not" && len(e.Args) == 1 {
			// not(not(x)) → (x != 0) which converts to boolean
			// This is simpler than double negation and produces the same result
			if innerNot, ok := e.Args[0].(*CallExpr); ok && innerNot.Function == "not" && len(innerNot.Args) == 1 {
				// Convert to comparison: x != 0
				return &BinaryExpr{
					Left:     innerNot.Args[0],
					Operator: "!=",
					Right:    &NumberExpr{Value: 0.0},
				}
			}

			// not(constant) → constant
			if argNum, ok := e.Args[0].(*NumberExpr); ok {
				if argNum.Value == 0 {
					return &NumberExpr{Value: 1.0}
				}
				return &NumberExpr{Value: 0.0}
			}

			// not(comparison) → inverted comparison
			if cmp, ok := e.Args[0].(*BinaryExpr); ok {
				inverted := invertComparison(cmp)
				if inverted != nil {
					return inverted
				}
			}
		}

		return e

	case *ListExpr:
		for i, elem := range e.Elements {
			e.Elements[i] = strengthReduceExpr(elem)
		}
		return e

	case *MapExpr:
		for i := range e.Keys {
			e.Keys[i] = strengthReduceExpr(e.Keys[i])
			e.Values[i] = strengthReduceExpr(e.Values[i])
		}
		return e

	case *IndexExpr:
		e.List = strengthReduceExpr(e.List)
		e.Index = strengthReduceExpr(e.Index)
		return e

	case *LambdaExpr:
		e.Body = strengthReduceExpr(e.Body)
		return e

	case *RangeExpr:
		e.Start = strengthReduceExpr(e.Start)
		e.End = strengthReduceExpr(e.End)
		return e

	case *MatchExpr:
		e.Condition = strengthReduceExpr(e.Condition)
		for _, clause := range e.Clauses {
			if clause.Guard != nil {
				clause.Guard = strengthReduceExpr(clause.Guard)
			}
			clause.Result = strengthReduceExpr(clause.Result)
		}
		if e.DefaultExpr != nil {
			e.DefaultExpr = strengthReduceExpr(e.DefaultExpr)
		}
		return e

	case *BlockExpr:
		for i, stmt := range e.Statements {
			e.Statements[i] = strengthReduceStmt(stmt)
		}
		return e

	case *LoopExpr:
		e.Iterable = strengthReduceExpr(e.Iterable)
		for i, stmt := range e.Body {
			e.Body[i] = strengthReduceStmt(stmt)
		}
		return e

	case *PipeExpr:
		e.Left = strengthReduceExpr(e.Left)
		e.Right = strengthReduceExpr(e.Right)
		return e

	case *ParallelExpr:
		e.List = strengthReduceExpr(e.List)
		e.Operation = strengthReduceExpr(e.Operation)
		return e

	case *InExpr:
		e.Value = strengthReduceExpr(e.Value)
		e.Container = strengthReduceExpr(e.Container)
		return e

	case *LengthExpr:
		e.Operand = strengthReduceExpr(e.Operand)
		return e

	case *FMAExpr:
		e.A = strengthReduceExpr(e.A)
		e.B = strengthReduceExpr(e.B)
		e.C = strengthReduceExpr(e.C)
		return e

	default:
		return expr
	}
}

// strengthReduceStmt applies strength reduction to statements
func strengthReduceStmt(stmt Statement) Statement {
	if stmt == nil {
		return nil
	}

	switch s := stmt.(type) {
	case *AssignStmt:
		s.Value = strengthReduceExpr(s.Value)
		return s

	case *ExpressionStmt:
		s.Expr = strengthReduceExpr(s.Expr)
		return s

	case *LoopStmt:
		s.Iterable = strengthReduceExpr(s.Iterable)
		for i, bodyStmt := range s.Body {
			s.Body[i] = strengthReduceStmt(bodyStmt)
		}
		return s

	case *JumpStmt:
		if s.Value != nil {
			s.Value = strengthReduceExpr(s.Value)
		}
		return s

	default:
		return stmt
	}
}

// propagateConstants performs constant propagation on statements
// Tracks immutable variables assigned constant values and substitutes them
func propagateConstants(stmt Statement, constMap map[string]*NumberExpr) Statement {
	switch s := stmt.(type) {
	case *AssignStmt:
		// First propagate constants in the value expression
		s.Value = propagateConstantsExpr(s.Value, constMap)

		// Then fold constants in case propagation enabled new folding opportunities
		s.Value = foldConstantExpr(s.Value)

		// Apply strength reduction after constant folding
		s.Value = strengthReduceExpr(s.Value)

		// If this is an immutable assignment to a number literal, track it
		if !s.Mutable && !s.IsUpdate {
			if numExpr, ok := s.Value.(*NumberExpr); ok {
				// Clone the number expression to avoid mutation issues
				constMap[s.Name] = &NumberExpr{Value: numExpr.Value}
			} else {
				// Variable is not assigned a constant, remove from map
				delete(constMap, s.Name)
			}
		} else {
			// Mutable or update - can't track as constant
			delete(constMap, s.Name)
		}
		return s

	case *ExpressionStmt:
		s.Expr = propagateConstantsExpr(s.Expr, constMap)
		s.Expr = foldConstantExpr(s.Expr)
		s.Expr = strengthReduceExpr(s.Expr)
		return s

	case *LoopStmt:
		s.Iterable = propagateConstantsExpr(s.Iterable, constMap)
		s.Iterable = foldConstantExpr(s.Iterable)
		s.Iterable = strengthReduceExpr(s.Iterable)

		// Loop body creates a new scope - clone const map
		bodyConstMap := make(map[string]*NumberExpr)
		maps.Copy(bodyConstMap, constMap)
		// Remove iterator variable from constants (it changes each iteration)
		delete(bodyConstMap, s.Iterator)

		for i, bodyStmt := range s.Body {
			s.Body[i] = propagateConstants(bodyStmt, bodyConstMap)
		}
		return s

	default:
		return stmt
	}
}

// propagateConstantsExpr substitutes variable references with known constant values
func propagateConstantsExpr(expr Expression, constMap map[string]*NumberExpr) Expression {
	switch e := expr.(type) {
	case *IdentExpr:
		// Check if this variable has a known constant value
		if constVal, exists := constMap[e.Name]; exists {
			// Substitute with the constant value
			return &NumberExpr{Value: constVal.Value}
		}
		return e

	case *BinaryExpr:
		e.Left = propagateConstantsExpr(e.Left, constMap)
		e.Right = propagateConstantsExpr(e.Right, constMap)
		return e

	case *CallExpr:
		for i, arg := range e.Args {
			e.Args[i] = propagateConstantsExpr(arg, constMap)
		}
		return e

	case *RangeExpr:
		e.Start = propagateConstantsExpr(e.Start, constMap)
		e.End = propagateConstantsExpr(e.End, constMap)
		return e

	case *ListExpr:
		for i, elem := range e.Elements {
			e.Elements[i] = propagateConstantsExpr(elem, constMap)
		}
		return e

	case *MapExpr:
		for i := range e.Keys {
			e.Keys[i] = propagateConstantsExpr(e.Keys[i], constMap)
			e.Values[i] = propagateConstantsExpr(e.Values[i], constMap)
		}
		return e

	case *IndexExpr:
		e.List = propagateConstantsExpr(e.List, constMap)
		e.Index = propagateConstantsExpr(e.Index, constMap)
		return e

	case *LambdaExpr:
		// Lambda creates new scope - don't propagate outer constants into lambda body
		// (More sophisticated analysis could handle this)
		return e

	case *ParallelExpr:
		e.List = propagateConstantsExpr(e.List, constMap)
		e.Operation = propagateConstantsExpr(e.Operation, constMap)
		return e

	case *PipeExpr:
		e.Left = propagateConstantsExpr(e.Left, constMap)
		e.Right = propagateConstantsExpr(e.Right, constMap)
		return e

	case *InExpr:
		e.Value = propagateConstantsExpr(e.Value, constMap)
		e.Container = propagateConstantsExpr(e.Container, constMap)
		return e

	case *LengthExpr:
		e.Operand = propagateConstantsExpr(e.Operand, constMap)
		return e

	case *MatchExpr:
		e.Condition = propagateConstantsExpr(e.Condition, constMap)
		for _, clause := range e.Clauses {
			if clause.Guard != nil {
				clause.Guard = propagateConstantsExpr(clause.Guard, constMap)
			}
			clause.Result = propagateConstantsExpr(clause.Result, constMap)
		}
		if e.DefaultExpr != nil {
			e.DefaultExpr = propagateConstantsExpr(e.DefaultExpr, constMap)
		}
		return e

	case *BlockExpr:
		// Block creates new scope - clone const map
		blockConstMap := make(map[string]*NumberExpr)
		maps.Copy(blockConstMap, constMap)
		for i, stmt := range e.Statements {
			e.Statements[i] = propagateConstants(stmt, blockConstMap)
		}
		return e

	case *MoveExpr:
		// Don't propagate constants into move expressions
		// The variable must exist at runtime for move semantics to work
		return e

	case *FMAExpr:
		e.A = propagateConstantsExpr(e.A, constMap)
		e.B = propagateConstantsExpr(e.B, constMap)
		e.C = propagateConstantsExpr(e.C, constMap)
		return e

	default:
		return expr
	}
}

// collectUsedVariables walks the AST and tracks which variables are referenced
func collectUsedVariables(stmt Statement, usedVars map[string]bool) {
	switch s := stmt.(type) {
	case *AssignStmt:
		collectUsedVariablesExpr(s.Value, usedVars)
	case *ExpressionStmt:
		collectUsedVariablesExpr(s.Expr, usedVars)
	case *LoopStmt:
		collectUsedVariablesExpr(s.Iterable, usedVars)
		// Mark iterator as used (even if not explicitly referenced)
		usedVars[s.Iterator] = true
		for _, bodyStmt := range s.Body {
			collectUsedVariables(bodyStmt, usedVars)
		}
	}
}

// collectUsedVariablesExpr tracks variable references in expressions
func collectUsedVariablesExpr(expr Expression, usedVars map[string]bool) {
	switch e := expr.(type) {
	case *IdentExpr:
		usedVars[e.Name] = true
	case *BinaryExpr:
		collectUsedVariablesExpr(e.Left, usedVars)
		collectUsedVariablesExpr(e.Right, usedVars)
	case *CallExpr:
		// Mark the function being called as used
		usedVars[e.Function] = true
		for _, arg := range e.Args {
			collectUsedVariablesExpr(arg, usedVars)
		}
	case *RangeExpr:
		collectUsedVariablesExpr(e.Start, usedVars)
		collectUsedVariablesExpr(e.End, usedVars)
	case *ListExpr:
		for _, elem := range e.Elements {
			collectUsedVariablesExpr(elem, usedVars)
		}
	case *MapExpr:
		for i := range e.Keys {
			collectUsedVariablesExpr(e.Keys[i], usedVars)
			collectUsedVariablesExpr(e.Values[i], usedVars)
		}
	case *IndexExpr:
		collectUsedVariablesExpr(e.List, usedVars)
		collectUsedVariablesExpr(e.Index, usedVars)
	case *LambdaExpr:
		collectUsedVariablesExpr(e.Body, usedVars)
	case *ParallelExpr:
		collectUsedVariablesExpr(e.List, usedVars)
		collectUsedVariablesExpr(e.Operation, usedVars)
	case *PipeExpr:
		collectUsedVariablesExpr(e.Left, usedVars)
		collectUsedVariablesExpr(e.Right, usedVars)
	case *InExpr:
		collectUsedVariablesExpr(e.Value, usedVars)
		collectUsedVariablesExpr(e.Container, usedVars)
	case *LengthExpr:
		collectUsedVariablesExpr(e.Operand, usedVars)
	case *MatchExpr:
		collectUsedVariablesExpr(e.Condition, usedVars)
		for _, clause := range e.Clauses {
			if clause.Guard != nil {
				collectUsedVariablesExpr(clause.Guard, usedVars)
			}
			collectUsedVariablesExpr(clause.Result, usedVars)
		}
		if e.DefaultExpr != nil {
			collectUsedVariablesExpr(e.DefaultExpr, usedVars)
		}
	case *BlockExpr:
		for _, stmt := range e.Statements {
			collectUsedVariables(stmt, usedVars)
		}
	case *CastExpr:
		collectUsedVariablesExpr(e.Expr, usedVars)
	case *SliceExpr:
		collectUsedVariablesExpr(e.List, usedVars)
		if e.Start != nil {
			collectUsedVariablesExpr(e.Start, usedVars)
		}
		if e.End != nil {
			collectUsedVariablesExpr(e.End, usedVars)
		}
	case *UnaryExpr:
		collectUsedVariablesExpr(e.Operand, usedVars)
	case *NamespacedIdentExpr:
		// Namespace access like sdl.SDL_Init or data.field
		// For data.field, "data" is a variable that should be marked as used
		// For sdl.SDL_Init, "sdl" is an imported namespace, not a variable
		// We mark it as used - the compiler will handle whether it's a variable or namespace
		usedVars[e.Namespace] = true
	case *FStringExpr:
		// FStringExpr.Parts is []Expression, each part is either StringExpr or an expression
		if VerboseMode {
			debugf("DEBUG: FStringExpr with %d parts\n", len(e.Parts))
			for i, part := range e.Parts {
				fmt.Fprintf(os.Stderr, "  Part %d: %T\n", i, part)
			}
		}
		for _, part := range e.Parts {
			collectUsedVariablesExpr(part, usedVars)
		}
	case *DirectCallExpr:
		collectUsedVariablesExpr(e.Callee, usedVars)
		for _, arg := range e.Args {
			collectUsedVariablesExpr(arg, usedVars)
		}
	case *PostfixExpr:
		collectUsedVariablesExpr(e.Operand, usedVars)
	case *VectorExpr:
		for _, comp := range e.Components {
			collectUsedVariablesExpr(comp, usedVars)
		}
	case *ArenaExpr:
		// ArenaExpr has Body []Statement
		for _, stmt := range e.Body {
			collectUsedVariables(stmt, usedVars)
		}
	case *MultiLambdaExpr:
		// For multi-lambda, collect variables from all lambda bodies
		for _, lambda := range e.Lambdas {
			collectUsedVariablesExpr(lambda.Body, usedVars)
		}
	case *SendExpr:
		// SendExpr has Target and Message
		collectUsedVariablesExpr(e.Target, usedVars)
		collectUsedVariablesExpr(e.Message, usedVars)
	case *ReceiveExpr:
		// ReceiveExpr has Source
		collectUsedVariablesExpr(e.Source, usedVars)
	case *UnsafeExpr:
		// UnsafeExpr has architecture-specific blocks
		for _, stmt := range e.X86_64Block {
			collectUsedVariables(stmt, usedVars)
		}
		for _, stmt := range e.ARM64Block {
			collectUsedVariables(stmt, usedVars)
		}
		for _, stmt := range e.RISCV64Block {
			collectUsedVariables(stmt, usedVars)
		}
	case *LoopExpr:
		for _, stmt := range e.Body {
			collectUsedVariables(stmt, usedVars)
		}
	case *LoopStateExpr:
		// LoopStateExpr doesn't reference variables
	case *JumpExpr:
		// JumpExpr doesn't reference variables directly
	case *FMAExpr:
		collectUsedVariablesExpr(e.A, usedVars)
		collectUsedVariablesExpr(e.B, usedVars)
		collectUsedVariablesExpr(e.C, usedVars)
	}
}

// eliminateDeadCode removes assignments to unused variables
// Returns nil if statement should be removed entirely
func eliminateDeadCode(stmt Statement, usedVars map[string]bool) Statement {
	switch s := stmt.(type) {
	case *AssignStmt:
		// Keep assignments if:
		// 1. Variable is used somewhere
		// 2. Assignment has side effects (contains function call)
		if usedVars[s.Name] || hasSideEffects(s.Value) {
			return s
		}
		// Dead assignment - remove it
		return nil

	case *ExpressionStmt:
		// Always keep expression statements (they might have side effects like printf)
		return s

	case *LoopStmt:
		// Keep loop but eliminate dead code in body
		newBody := make([]Statement, 0, len(s.Body))
		for _, bodyStmt := range s.Body {
			if keep := eliminateDeadCode(bodyStmt, usedVars); keep != nil {
				newBody = append(newBody, keep)
			}
		}
		s.Body = newBody
		return s

	default:
		return stmt
	}
}

// hasSideEffects checks if an expression contains function calls or other side effects
func hasSideEffects(expr Expression) bool {
	switch e := expr.(type) {
	case *CallExpr:
		return true // Function calls have side effects
	case *BinaryExpr:
		return hasSideEffects(e.Left) || hasSideEffects(e.Right)
	case *ListExpr:
		return slices.ContainsFunc(e.Elements, hasSideEffects)
	case *MapExpr:
		for i := range e.Keys {
			if hasSideEffects(e.Keys[i]) || hasSideEffects(e.Values[i]) {
				return true
			}
		}
		return false
	case *IndexExpr:
		return hasSideEffects(e.List) || hasSideEffects(e.Index)
	case *ParallelExpr:
		return true // Parallel operations have side effects
	case *PipeExpr:
		return hasSideEffects(e.Left) || hasSideEffects(e.Right)
	case *MatchExpr:
		if hasSideEffects(e.Condition) {
			return true
		}
		for _, clause := range e.Clauses {
			if clause.Guard != nil && hasSideEffects(clause.Guard) {
				return true
			}
			if hasSideEffects(clause.Result) {
				return true
			}
		}
		if e.DefaultExpr != nil && hasSideEffects(e.DefaultExpr) {
			return true
		}
		return false
	case *BlockExpr:
		// Blocks can have side effects if any statement does
		return true
	case *FMAExpr:
		return hasSideEffects(e.A) || hasSideEffects(e.B) || hasSideEffects(e.C)
	default:
		return false // Literals, identifiers, etc. have no side effects
	}
}

// analyzePurity walks AST and marks lambdas as pure (no side effects, no captured mutables)
func analyzePurity(stmt Statement, pureFunctions map[string]bool) {
	switch s := stmt.(type) {
	case *AssignStmt:
		// Analyze value expression for lambdas
		if lambda, ok := s.Value.(*LambdaExpr); ok {
			// Check if this lambda is pure
			lambda.IsPure = isLambdaPure(lambda, pureFunctions)
			if !s.Mutable {
				// Track named pure functions for call analysis
				pureFunctions[s.Name] = lambda.IsPure
			}
		}
		analyzePurityExpr(s.Value, pureFunctions)
	case *ExpressionStmt:
		analyzePurityExpr(s.Expr, pureFunctions)
	case *LoopStmt:
		analyzePurityExpr(s.Iterable, pureFunctions)
		for _, bodyStmt := range s.Body {
			analyzePurity(bodyStmt, pureFunctions)
		}
	}
}

// analyzePurityExpr recursively analyzes expressions for lambdas
func analyzePurityExpr(expr Expression, pureFunctions map[string]bool) {
	switch e := expr.(type) {
	case *LambdaExpr:
		e.IsPure = isLambdaPure(e, pureFunctions)
	case *BinaryExpr:
		analyzePurityExpr(e.Left, pureFunctions)
		analyzePurityExpr(e.Right, pureFunctions)
	case *CallExpr:
		for _, arg := range e.Args {
			analyzePurityExpr(arg, pureFunctions)
		}
	case *ListExpr:
		for _, elem := range e.Elements {
			analyzePurityExpr(elem, pureFunctions)
		}
	case *MapExpr:
		for i := range e.Keys {
			analyzePurityExpr(e.Keys[i], pureFunctions)
			analyzePurityExpr(e.Values[i], pureFunctions)
		}
	case *IndexExpr:
		analyzePurityExpr(e.List, pureFunctions)
		analyzePurityExpr(e.Index, pureFunctions)
	case *ParallelExpr:
		analyzePurityExpr(e.List, pureFunctions)
		analyzePurityExpr(e.Operation, pureFunctions)
	case *PipeExpr:
		analyzePurityExpr(e.Left, pureFunctions)
		analyzePurityExpr(e.Right, pureFunctions)
	case *MatchExpr:
		analyzePurityExpr(e.Condition, pureFunctions)
		for _, clause := range e.Clauses {
			if clause.Guard != nil {
				analyzePurityExpr(clause.Guard, pureFunctions)
			}
			analyzePurityExpr(clause.Result, pureFunctions)
		}
		if e.DefaultExpr != nil {
			analyzePurityExpr(e.DefaultExpr, pureFunctions)
		}
	case *BlockExpr:
		for _, stmt := range e.Statements {
			analyzePurity(stmt, pureFunctions)
		}
	case *FMAExpr:
		analyzePurityExpr(e.A, pureFunctions)
		analyzePurityExpr(e.B, pureFunctions)
		analyzePurityExpr(e.C, pureFunctions)
	}
}

// isLambdaPure determines if a lambda is pure (safe to memoize)
// A pure lambda:
// 1. Has no side effects (no I/O, no global state mutation)
// 2. Doesn't capture mutable variables
// 3. Only calls other pure functions
// 4. Is deterministic (same inputs → same outputs)
func isLambdaPure(lambda *LambdaExpr, pureFunctions map[string]bool) bool {
	// Check for basic side effects
	if hasSideEffects(lambda.Body) {
		return false
	}

	// Check if lambda calls any impure functions
	if callsImpureFunctions(lambda.Body, pureFunctions) {
		return false
	}

	// Check if lambda captures external variables (conservatively mark as impure)
	// More sophisticated analysis could track whether captured vars are mutable
	capturedVars := make(map[string]bool)
	collectCapturedVariables(lambda.Body, lambda.Params, capturedVars)
	if len(capturedVars) > 0 {
		// Lambda captures external variables - conservatively mark as impure
		// (Could be enhanced to allow capturing immutable constants)
		return false
	}

	return true
}

// callsImpureFunctions checks if expression calls any functions marked as impure
func callsImpureFunctions(expr Expression, pureFunctions map[string]bool) bool {
	switch e := expr.(type) {
	case *CallExpr:
		// Check if called function is known to be impure
		// Known impure built-ins
		impureBuiltins := map[string]bool{
			"printf": true, "println": true, "print": true,
			"scanf": true, "read": true, "write": true,
		}
		if impureBuiltins[e.Function] {
			return true
		}
		// Check if it's a user function we know is impure
		if isPure, known := pureFunctions[e.Function]; known && !isPure {
			return true
		}
		// Check arguments
		for _, arg := range e.Args {
			if callsImpureFunctions(arg, pureFunctions) {
				return true
			}
		}
		return false
	case *BinaryExpr:
		return callsImpureFunctions(e.Left, pureFunctions) || callsImpureFunctions(e.Right, pureFunctions)
	case *ListExpr:
		for _, elem := range e.Elements {
			if callsImpureFunctions(elem, pureFunctions) {
				return true
			}
		}
		return false
	case *MatchExpr:
		if callsImpureFunctions(e.Condition, pureFunctions) {
			return true
		}
		for _, clause := range e.Clauses {
			if clause.Guard != nil && callsImpureFunctions(clause.Guard, pureFunctions) {
				return true
			}
			if callsImpureFunctions(clause.Result, pureFunctions) {
				return true
			}
		}
		if e.DefaultExpr != nil && callsImpureFunctions(e.DefaultExpr, pureFunctions) {
			return true
		}
		return false
	case *BlockExpr:
		// Conservative: blocks might have impure statements
		return true
	case *FMAExpr:
		return callsImpureFunctions(e.A, pureFunctions) ||
			callsImpureFunctions(e.B, pureFunctions) ||
			callsImpureFunctions(e.C, pureFunctions)
	default:
		return false
	}
}

// collectCapturedVariables finds variables used but not defined in lambda params
func collectCapturedVariables(expr Expression, params []string, captured map[string]bool) {
	// Create param set for quick lookup
	paramSet := make(map[string]bool)
	for _, p := range params {
		paramSet[p] = true
	}

	collectCapturedVarsExpr(expr, paramSet, captured)
}

func collectCapturedVarsExpr(expr Expression, paramSet map[string]bool, captured map[string]bool) {
	switch e := expr.(type) {
	case *IdentExpr:
		// If variable is not a parameter, it's captured from outer scope
		if !paramSet[e.Name] {
			captured[e.Name] = true
		}
	case *LambdaExpr:
		// Nested lambda: extend paramSet with nested lambda's parameters
		// and recursively collect from its body
		nestedParamSet := make(map[string]bool)
		maps.Copy(nestedParamSet, paramSet)
		for _, param := range e.Params {
			nestedParamSet[param] = true
		}
		collectCapturedVarsExpr(e.Body, nestedParamSet, captured)
	case *BinaryExpr:
		collectCapturedVarsExpr(e.Left, paramSet, captured)
		collectCapturedVarsExpr(e.Right, paramSet, captured)
	case *CallExpr:
		for _, arg := range e.Args {
			collectCapturedVarsExpr(arg, paramSet, captured)
		}
	case *ListExpr:
		for _, elem := range e.Elements {
			collectCapturedVarsExpr(elem, paramSet, captured)
		}
	case *MapExpr:
		for i := range e.Keys {
			collectCapturedVarsExpr(e.Keys[i], paramSet, captured)
			collectCapturedVarsExpr(e.Values[i], paramSet, captured)
		}
	case *IndexExpr:
		collectCapturedVarsExpr(e.List, paramSet, captured)
		collectCapturedVarsExpr(e.Index, paramSet, captured)
	case *CastExpr:
		collectCapturedVarsExpr(e.Expr, paramSet, captured)
	case *FieldAccessExpr:
		collectCapturedVarsExpr(e.Object, paramSet, captured)
	case *UnaryExpr:
		collectCapturedVarsExpr(e.Operand, paramSet, captured)
	case *LengthExpr:
		collectCapturedVarsExpr(e.Operand, paramSet, captured)
	case *MatchExpr:
		collectCapturedVarsExpr(e.Condition, paramSet, captured)
		for _, clause := range e.Clauses {
			if clause.Guard != nil {
				collectCapturedVarsExpr(clause.Guard, paramSet, captured)
			}
			collectCapturedVarsExpr(clause.Result, paramSet, captured)
		}
		if e.DefaultExpr != nil {
			collectCapturedVarsExpr(e.DefaultExpr, paramSet, captured)
		}
	case *JumpExpr:
		// Process the value expression of return/jump statements
		if e.Value != nil {
			collectCapturedVarsExpr(e.Value, paramSet, captured)
		}
	case *FMAExpr:
		collectCapturedVarsExpr(e.A, paramSet, captured)
		collectCapturedVarsExpr(e.B, paramSet, captured)
		collectCapturedVarsExpr(e.C, paramSet, captured)
	case *BlockExpr:
		// For blocks, we need to track locally defined variables
		// so they aren't treated as captured
		localParamSet := make(map[string]bool)
		maps.Copy(localParamSet, paramSet)

		// Process each statement in the block
		for _, stmt := range e.Statements {
			switch s := stmt.(type) {
			case *AssignStmt:
				// Recursively check the assignment value (with current param set)
				collectCapturedVarsExpr(s.Value, localParamSet, captured)
				// Then add locally defined variable to param set
				localParamSet[s.Name] = true
			case *ExpressionStmt:
				collectCapturedVarsExpr(s.Expr, localParamSet, captured)
			case *LoopStmt:
				// The iterable is evaluated in the enclosing scope; the body runs
				// with the iterator bound locally. Without this, variables read
				// only inside a loop body were never detected as captured.
				collectCapturedVarsExpr(s.Iterable, localParamSet, captured)
				bodyParamSet := make(map[string]bool)
				maps.Copy(bodyParamSet, localParamSet)
				bodyParamSet[s.Iterator] = true
				collectCapturedVarsExpr(&BlockExpr{Statements: s.Body}, bodyParamSet, captured)
			case *JumpStmt:
				if s.Value != nil {
					collectCapturedVarsExpr(s.Value, localParamSet, captured)
				}
			case *IfStmt:
				// Each branch's condition is evaluated in the enclosing scope; its
				// body runs in a nested scope. Without recursing here, a variable
				// read only inside an `if` body (e.g. a global written via
				// write_u32 under `if mine { ... }`) was never detected as captured,
				// so it never got a global slot and codegen failed.
				for _, br := range s.Branches {
					collectCapturedVarsExpr(br.Condition, localParamSet, captured)
					collectCapturedVarsExpr(&BlockExpr{Statements: br.Body}, localParamSet, captured)
				}
				collectCapturedVarsExpr(&BlockExpr{Statements: s.ElseBody}, localParamSet, captured)
			case *WhileStmt:
				collectCapturedVarsExpr(s.Condition, localParamSet, captured)
				collectCapturedVarsExpr(&BlockExpr{Statements: s.Body}, localParamSet, captured)
			case *ArenaStmt:
				collectCapturedVarsExpr(&BlockExpr{Statements: s.Body}, localParamSet, captured)
			}
		}
	}
}

// analyzeClosure detects and marks closures (lambdas that capture variables from outer scope)
// This must be called during compilation to populate CapturedVars field
// globalVars contains variables that should NOT be captured (they're globally accessible)
func analyzeClosures(stmt Statement, availableVars map[string]bool, globalVars map[string]int) {
	switch s := stmt.(type) {
	case *AssignStmt:
		// Add this variable to available vars
		newAvailableVars := make(map[string]bool)
		maps.Copy(newAvailableVars, availableVars)
		newAvailableVars[s.Name] = true

		// Analyze the value expression
		analyzeClosuresExpr(s.Value, availableVars, globalVars)

	case *ExpressionStmt:
		analyzeClosuresExpr(s.Expr, availableVars, globalVars)

	case *LoopStmt:
		// Add iterator to available vars for loop body
		newAvailableVars := make(map[string]bool)
		maps.Copy(newAvailableVars, availableVars)
		newAvailableVars[s.Iterator] = true

		analyzeClosuresExpr(s.Iterable, availableVars, globalVars)
		for _, bodyStmt := range s.Body {
			analyzeClosures(bodyStmt, newAvailableVars, globalVars)
		}

	case *JumpStmt:
		// Analyze the value expression of return/jump statements
		if s.Value != nil {
			analyzeClosuresExpr(s.Value, availableVars, globalVars)
		}
	}
}

func analyzeClosuresExpr(expr Expression, availableVars map[string]bool, globalVars map[string]int) {
	switch e := expr.(type) {
	case *LambdaExpr:
		// This is a lambda - check if it captures any variables
		captured := make(map[string]bool)
		collectCapturedVariables(e.Body, e.Params, captured)

		if VerboseMode {
			debugf("DEBUG: Lambda analysis - raw captured: %v, availableVars: %v\n", captured, availableVars)
		}

		// Filter captured vars to only include those available in outer scope
		// EXCLUDING global variables (they don't need to be captured)
		var capturedList []string
		for varName := range captured {
			_, isGlobal := globalVars[varName]
			if availableVars[varName] && !isGlobal {
				// Variable is available in outer scope AND not a global
				capturedList = append(capturedList, varName)
			}
		}

		e.CapturedVars = capturedList
		e.IsNestedLambda = len(capturedList) > 0

		if VerboseMode && len(capturedList) > 0 {
			debugf("DEBUG: Found closure with %d captured vars: %v\n", len(capturedList), capturedList)
		}

		// Recursively analyze the lambda body with params added to available vars
		newAvailableVars := make(map[string]bool)
		maps.Copy(newAvailableVars, availableVars)
		for _, param := range e.Params {
			newAvailableVars[param] = true
		}
		analyzeClosuresExpr(e.Body, newAvailableVars, globalVars)

	case *BinaryExpr:
		analyzeClosuresExpr(e.Left, availableVars, globalVars)
		analyzeClosuresExpr(e.Right, availableVars, globalVars)
	case *CallExpr:
		for _, arg := range e.Args {
			analyzeClosuresExpr(arg, availableVars, globalVars)
		}
	case *ListExpr:
		for _, elem := range e.Elements {
			analyzeClosuresExpr(elem, availableVars, globalVars)
		}
	case *MapExpr:
		for i := range e.Keys {
			analyzeClosuresExpr(e.Keys[i], availableVars, globalVars)
			analyzeClosuresExpr(e.Values[i], availableVars, globalVars)
		}
	case *IndexExpr:
		analyzeClosuresExpr(e.List, availableVars, globalVars)
		analyzeClosuresExpr(e.Index, availableVars, globalVars)
	case *MatchExpr:
		analyzeClosuresExpr(e.Condition, availableVars, globalVars)
		for _, clause := range e.Clauses {
			if clause.Guard != nil {
				analyzeClosuresExpr(clause.Guard, availableVars, globalVars)
			}
			analyzeClosuresExpr(clause.Result, availableVars, globalVars)
		}
		if e.DefaultExpr != nil {
			analyzeClosuresExpr(e.DefaultExpr, availableVars, globalVars)
		}
	case *JumpExpr:
		// Analyze the value expression of return/jump statements
		if e.Value != nil {
			analyzeClosuresExpr(e.Value, availableVars, globalVars)
		}
	case *BlockExpr:
		// Create a new scope for the block, accumulating available vars
		blockAvailableVars := make(map[string]bool)
		maps.Copy(blockAvailableVars, availableVars)

		// Process each statement, threading through newly defined variables
		for _, stmt := range e.Statements {
			analyzeClosures(stmt, blockAvailableVars, globalVars)
			// If it's an assignment, add the variable to available vars for subsequent statements
			if assign, ok := stmt.(*AssignStmt); ok {
				blockAvailableVars[assign.Name] = true
			}
		}
	case *UnaryExpr:
		analyzeClosuresExpr(e.Operand, availableVars, globalVars)
	case *ParallelExpr:
		analyzeClosuresExpr(e.List, availableVars, globalVars)
		analyzeClosuresExpr(e.Operation, availableVars, globalVars)
	case *PipeExpr:
		analyzeClosuresExpr(e.Left, availableVars, globalVars)
		analyzeClosuresExpr(e.Right, availableVars, globalVars)
	case *FMAExpr:
		analyzeClosuresExpr(e.A, availableVars, globalVars)
		analyzeClosuresExpr(e.B, availableVars, globalVars)
		analyzeClosuresExpr(e.C, availableVars, globalVars)
	}
}

// collectInlineCandidates identifies lambdas suitable for inlining
// Criteria: immutable, small body (single expression), not in a loop
func collectInlineCandidates(stmt Statement, candidates map[string]*LambdaExpr) {
	switch s := stmt.(type) {
	case *AssignStmt:
		// Only inline immutable assignments to lambdas
		if !s.Mutable && !s.IsUpdate {
			if lambda, ok := s.Value.(*LambdaExpr); ok {
				// Only inline simple lambdas (single expression body, no blocks)
				if !isComplexExpression(lambda.Body) {
					// Store a copy to avoid mutation
					candidates[s.Name] = &LambdaExpr{
						Params:            lambda.Params,
						ParamCStructTypes: lambda.ParamCStructTypes,
						Body:              lambda.Body,
						IsPure:            lambda.IsPure,
					}
				}
			}
		}
	case *LoopStmt:
		// Recursively check loop bodies (but don't inline loop vars)
		for _, bodyStmt := range s.Body {
			collectInlineCandidates(bodyStmt, candidates)
		}
	}
}

// isComplexExpression checks if an expression is too complex to inline
func isComplexExpression(expr Expression) bool {
	switch e := expr.(type) {
	case *BlockExpr:
		return true // Don't inline blocks
	case *MatchExpr:
		// Allow inlining of SMALL, simple match expressions (the common case of
		// tiny predicate/clamp helpers like `(x) -> { | x < 1 => a ~> b }`), which
		// otherwise force a real call in the hottest loops. Reject large or
		// nested-complex matches.
		if len(e.Clauses) > 4 {
			return true
		}
		if e.Condition != nil && isComplexExpression(e.Condition) {
			return true
		}
		for _, c := range e.Clauses {
			if c.Guard != nil && isComplexExpression(c.Guard) {
				return true
			}
			if c.Result != nil && isComplexExpression(c.Result) {
				return true
			}
		}
		if e.DefaultExpr != nil && isComplexExpression(e.DefaultExpr) {
			return true
		}
		return false
	case *ParallelExpr:
		return true // Don't inline parallel operations
	case *CallExpr:
		// Allow simple function calls, but not nested complex calls
		return slices.ContainsFunc(e.Args, isComplexExpression)
	case *BinaryExpr:
		// Allow binary operations
		return isComplexExpression(e.Left) || isComplexExpression(e.Right)
	case *ListExpr:
		// Allow small lists
		if len(e.Elements) > 5 {
			return true
		}
		return slices.ContainsFunc(e.Elements, isComplexExpression)
	default:
		return false // Simple expressions (numbers, idents, etc.) are OK
	}
}

// countCalls counts how many times each function is called in the program
func countCalls(stmt Statement, counts map[string]int) {
	switch s := stmt.(type) {
	case *AssignStmt:
		countCallsExpr(s.Value, counts)
	case *ExpressionStmt:
		countCallsExpr(s.Expr, counts)
	case *LoopStmt:
		countCallsExpr(s.Iterable, counts)
		for _, bodyStmt := range s.Body {
			countCalls(bodyStmt, counts)
		}
	}
}

func countCallsExpr(expr Expression, counts map[string]int) {
	switch e := expr.(type) {
	case *CallExpr:
		counts[e.Function]++
		for _, arg := range e.Args {
			countCallsExpr(arg, counts)
		}
	case *BinaryExpr:
		countCallsExpr(e.Left, counts)
		countCallsExpr(e.Right, counts)
	case *ListExpr:
		for _, elem := range e.Elements {
			countCallsExpr(elem, counts)
		}
	case *MapExpr:
		for i := range e.Keys {
			countCallsExpr(e.Keys[i], counts)
			countCallsExpr(e.Values[i], counts)
		}
	case *IndexExpr:
		countCallsExpr(e.List, counts)
		countCallsExpr(e.Index, counts)
	case *ParallelExpr:
		countCallsExpr(e.List, counts)
		countCallsExpr(e.Operation, counts)
	case *PipeExpr:
		countCallsExpr(e.Left, counts)
		countCallsExpr(e.Right, counts)
	case *MatchExpr:
		countCallsExpr(e.Condition, counts)
		for _, clause := range e.Clauses {
			if clause.Guard != nil {
				countCallsExpr(clause.Guard, counts)
			}
			countCallsExpr(clause.Result, counts)
		}
		if e.DefaultExpr != nil {
			countCallsExpr(e.DefaultExpr, counts)
		}
	case *BlockExpr:
		for _, stmt := range e.Statements {
			countCalls(stmt, counts)
		}
	case *LambdaExpr:
		countCallsExpr(e.Body, counts)
	case *FMAExpr:
		countCallsExpr(e.A, counts)
		countCallsExpr(e.B, counts)
		countCallsExpr(e.C, counts)
	}
}

// inlineFunctions substitutes function calls with their bodies
func inlineFunctions(stmt Statement, candidates map[string]*LambdaExpr, callCounts map[string]int) Statement {
	switch s := stmt.(type) {
	case *AssignStmt:
		s.Value = inlineFunctionsExpr(s.Value, candidates, callCounts)
		return s
	case *ExpressionStmt:
		s.Expr = inlineFunctionsExpr(s.Expr, candidates, callCounts)
		return s
	case *LoopStmt:
		s.Iterable = inlineFunctionsExpr(s.Iterable, candidates, callCounts)
		for i, bodyStmt := range s.Body {
			s.Body[i] = inlineFunctions(bodyStmt, candidates, callCounts)
		}
		return s
	default:
		return stmt
	}
}

func inlineFunctionsExpr(expr Expression, candidates map[string]*LambdaExpr, callCounts map[string]int) Expression {
	switch e := expr.(type) {
	case *CallExpr:
		// First, recursively inline in arguments (process innermost calls first)
		for i, arg := range e.Args {
			e.Args[i] = inlineFunctionsExpr(arg, candidates, callCounts)
		}

		// Then check if this function itself is an inline candidate
		if lambda, isCandidate := candidates[e.Function]; isCandidate {
			// Only inline if:
			// 1. Parameter count matches
			// 2. Called at least once
			if len(e.Args) == len(lambda.Params) && callCounts[e.Function] > 0 {
				// Inline the body, let-binding any non-trivial argument so it is
				// evaluated exactly once (naive substitution would duplicate e.g.
				// `vscale(b,s)` into each `.x/.y/.z` use — quadratic blowup and
				// extra allocations).
				inlined := inlineWithLetBinding(lambda, e.Args)
				// Re-inline the result so nested candidate calls collapse too
				// (e.g. at -> vadd -> vscale -> V), which lets `p = Ctor(...)`
				// form and SROA fire. Depth-bounded to stop runaway recursion on
				// self-recursive one-liner candidates.
				if inlineReinlineDepth < 200 {
					inlineReinlineDepth++
					inlined = inlineFunctionsExpr(inlined, candidates, callCounts)
					inlineReinlineDepth--
				}
				return inlined
			}
		}
		return e
	case *BinaryExpr:
		e.Left = inlineFunctionsExpr(e.Left, candidates, callCounts)
		e.Right = inlineFunctionsExpr(e.Right, candidates, callCounts)
		return e
	case *ListExpr:
		for i, elem := range e.Elements {
			e.Elements[i] = inlineFunctionsExpr(elem, candidates, callCounts)
		}
		return e
	case *MapExpr:
		for i := range e.Keys {
			e.Keys[i] = inlineFunctionsExpr(e.Keys[i], candidates, callCounts)
			e.Values[i] = inlineFunctionsExpr(e.Values[i], candidates, callCounts)
		}
		return e
	case *IndexExpr:
		e.List = inlineFunctionsExpr(e.List, candidates, callCounts)
		e.Index = inlineFunctionsExpr(e.Index, candidates, callCounts)
		return e
	case *ParallelExpr:
		e.List = inlineFunctionsExpr(e.List, candidates, callCounts)
		e.Operation = inlineFunctionsExpr(e.Operation, candidates, callCounts)
		return e
	case *PipeExpr:
		e.Left = inlineFunctionsExpr(e.Left, candidates, callCounts)
		e.Right = inlineFunctionsExpr(e.Right, candidates, callCounts)
		return e
	case *MatchExpr:
		e.Condition = inlineFunctionsExpr(e.Condition, candidates, callCounts)
		for i := range e.Clauses {
			if e.Clauses[i].Guard != nil {
				e.Clauses[i].Guard = inlineFunctionsExpr(e.Clauses[i].Guard, candidates, callCounts)
			}
			e.Clauses[i].Result = inlineFunctionsExpr(e.Clauses[i].Result, candidates, callCounts)
		}
		if e.DefaultExpr != nil {
			e.DefaultExpr = inlineFunctionsExpr(e.DefaultExpr, candidates, callCounts)
		}
		return e
	case *BlockExpr:
		for i, stmt := range e.Statements {
			e.Statements[i] = inlineFunctions(stmt, candidates, callCounts)
		}
		return e
	case *LambdaExpr:
		e.Body = inlineFunctionsExpr(e.Body, candidates, callCounts)
		return e
	case *FMAExpr:
		e.A = inlineFunctionsExpr(e.A, candidates, callCounts)
		e.B = inlineFunctionsExpr(e.B, candidates, callCounts)
		e.C = inlineFunctionsExpr(e.C, candidates, callCounts)
		return e
	case *FieldAccessExpr:
		e.Object = inlineFunctionsExpr(e.Object, candidates, callCounts)
		return e
	case *CastExpr:
		e.Expr = inlineFunctionsExpr(e.Expr, candidates, callCounts)
		return e
	case *UnaryExpr:
		e.Operand = inlineFunctionsExpr(e.Operand, candidates, callCounts)
		return e
	case *LengthExpr:
		e.Operand = inlineFunctionsExpr(e.Operand, candidates, callCounts)
		return e
	case *JumpExpr:
		if e.Value != nil {
			e.Value = inlineFunctionsExpr(e.Value, candidates, callCounts)
		}
		return e
	default:
		return expr
	}
}

// deepCopyExpr creates a deep copy of an expression to avoid AST node sharing
func deepCopyExpr(expr Expression) Expression {
	switch e := expr.(type) {
	case *NumberExpr:
		return &NumberExpr{Value: e.Value}
	case *StringExpr:
		return &StringExpr{Value: e.Value}
	case *IdentExpr:
		return &IdentExpr{Name: e.Name}
	case *BinaryExpr:
		return &BinaryExpr{
			Left:     deepCopyExpr(e.Left),
			Operator: e.Operator,
			Right:    deepCopyExpr(e.Right),
		}
	case *CallExpr:
		newArgs := make([]Expression, len(e.Args))
		for i, arg := range e.Args {
			newArgs[i] = deepCopyExpr(arg)
		}
		return &CallExpr{
			Function: e.Function,
			Args:     newArgs,
		}
	case *ListExpr:
		newElements := make([]Expression, len(e.Elements))
		for i, elem := range e.Elements {
			newElements[i] = deepCopyExpr(elem)
		}
		return &ListExpr{Elements: newElements}
	case *MapExpr:
		newKeys := make([]Expression, len(e.Keys))
		newValues := make([]Expression, len(e.Values))
		for i := range e.Keys {
			newKeys[i] = deepCopyExpr(e.Keys[i])
			newValues[i] = deepCopyExpr(e.Values[i])
		}
		return &MapExpr{Keys: newKeys, Values: newValues}
	case *IndexExpr:
		return &IndexExpr{
			List:  deepCopyExpr(e.List),
			Index: deepCopyExpr(e.Index),
		}
	case *LambdaExpr:
		paramsCopy := make([]string, len(e.Params))
		copy(paramsCopy, e.Params)
		return &LambdaExpr{
			Params: paramsCopy,
			Body:   deepCopyExpr(e.Body),
			IsPure: e.IsPure,
		}
	case *FMAExpr:
		return &FMAExpr{
			A:        deepCopyExpr(e.A),
			B:        deepCopyExpr(e.B),
			C:        deepCopyExpr(e.C),
			IsSub:    e.IsSub,
			IsNegMul: e.IsNegMul,
		}
	default:
		// For other types, return as-is (may need to extend this)
		return expr
	}
}

// substituteParams replaces parameter references with actual arguments
var inlineTempCounter int

// inlineReinlineDepth bounds re-inlining of an inlined body (so nested candidate
// calls collapse) without looping forever on self-recursive one-liner candidates.
var inlineReinlineDepth int

// inlineArgIsTrivial reports whether an argument is safe to substitute directly
// into every use without a binding: a bare variable or literal has no evaluation
// cost and no side effects, so duplicating it is free.
func inlineArgIsTrivial(arg Expression) bool {
	switch a := arg.(type) {
	case *IdentExpr, *NumberExpr, *StringExpr:
		return true
	case *CastExpr:
		// A cast of a trivial value (e.g. `ro as V`) is still cheap to duplicate,
		// and substituting it un-bound avoids wrapping the inlined body in a block
		// — which would hide a `p = Ctor(...)` result from SROA.
		return inlineArgIsTrivial(a.Expr)
	}
	return false
}

// inlineWithLetBinding inlines a candidate lambda at a call site. Trivial args are
// substituted directly; non-trivial args are bound to a fresh local first
// (`__inl_N = arg`) so they are computed once, then the body is emitted as a block
// whose value is the substituted body.
func inlineWithLetBinding(lambda *LambdaExpr, args []Expression) Expression {
	substMap := make(map[string]Expression, len(lambda.Params))
	var bindings []Statement
	for i, param := range lambda.Params {
		arg := args[i]
		// A cstruct-typed param carries the type that makes its `a.x` field
		// accesses work in the body; after substitution the access site is in the
		// caller's scope where the arg may be untyped, so wrap with `as Type`.
		ctype := ""
		if lambda.ParamCStructTypes != nil {
			ctype = lambda.ParamCStructTypes[param]
		}
		wrap := func(e Expression) Expression {
			if ctype != "" {
				return &CastExpr{Expr: e, Type: ctype}
			}
			return e
		}
		switch {
		case isCStructConstructorCall(arg):
			// Substitute the constructor directly. The codegen folds `Ctor(..).f`
			// to the f-th argument (SROA) — and since the fields are distinct
			// expressions, accessing .x/.y/.z does not re-evaluate anything. A cast
			// wrapper would hide the constructor from the fold, so don't add one.
			substMap[param] = arg
		case inlineArgIsTrivial(arg):
			// Cheap to duplicate (a variable read or literal).
			substMap[param] = wrap(arg)
		default:
			// Non-trivial scalar/opaque arg: bind once to avoid re-evaluation.
			inlineTempCounter++
			tmp := fmt.Sprintf("__inl_%d", inlineTempCounter)
			bindings = append(bindings, &AssignStmt{Name: tmp, Value: wrap(arg)})
			substMap[param] = &IdentExpr{Name: tmp}
		}
	}
	body := substituteParamsExpr(lambda.Body, substMap)
	if len(bindings) == 0 {
		return body
	}
	stmts := append(bindings, &ExpressionStmt{Expr: body})
	return &BlockExpr{Statements: stmts}
}

func substituteParams(body Expression, params []string, args []Expression) Expression {
	// Create substitution map
	substMap := make(map[string]Expression)
	for i, param := range params {
		substMap[param] = args[i]
	}

	return substituteParamsExpr(body, substMap)
}

func substituteParamsExpr(expr Expression, substMap map[string]Expression) Expression {
	switch e := expr.(type) {
	case *IdentExpr:
		// Replace parameter with argument (must deep copy to avoid sharing!)
		if replacement, found := substMap[e.Name]; found {
			return deepCopyExpr(replacement)
		}
		return e
	case *BinaryExpr:
		return &BinaryExpr{
			Left:     substituteParamsExpr(e.Left, substMap),
			Operator: e.Operator,
			Right:    substituteParamsExpr(e.Right, substMap),
		}
	case *CallExpr:
		newArgs := make([]Expression, len(e.Args))
		for i, arg := range e.Args {
			newArgs[i] = substituteParamsExpr(arg, substMap)
		}
		// If the callee itself is a parameter being substituted (a function passed
		// as an argument), rewrite the call to target the substituted value.
		if replacement, found := substMap[e.Function]; found && !e.IsCFFI {
			if ident, ok := replacement.(*IdentExpr); ok {
				// Named function value: keep it as a normal named call.
				return &CallExpr{Function: ident.Name, Args: newArgs}
			}
			// Lambda or other callable value: use an indirect (direct) call.
			return &DirectCallExpr{Callee: deepCopyExpr(replacement), Args: newArgs}
		}
		return &CallExpr{
			Function:            e.Function,
			Args:                newArgs,
			MaxRecursionDepth:   e.MaxRecursionDepth,
			NeedsRecursionCheck: e.NeedsRecursionCheck,
			IsCFFI:              e.IsCFFI,
			RawBitcast:          e.RawBitcast,
		}
	case *ListExpr:
		newElements := make([]Expression, len(e.Elements))
		for i, elem := range e.Elements {
			newElements[i] = substituteParamsExpr(elem, substMap)
		}
		return &ListExpr{Elements: newElements}
	case *MapExpr:
		newKeys := make([]Expression, len(e.Keys))
		newValues := make([]Expression, len(e.Values))
		for i := range e.Keys {
			newKeys[i] = substituteParamsExpr(e.Keys[i], substMap)
			newValues[i] = substituteParamsExpr(e.Values[i], substMap)
		}
		return &MapExpr{Keys: newKeys, Values: newValues}
	case *IndexExpr:
		return &IndexExpr{
			List:  substituteParamsExpr(e.List, substMap),
			Index: substituteParamsExpr(e.Index, substMap),
		}
	case *MatchExpr:
		newClauses := make([]*MatchClause, len(e.Clauses))
		for i, clause := range e.Clauses {
			newClause := &MatchClause{
				Guard:  nil,
				Result: substituteParamsExpr(clause.Result, substMap),
			}
			if clause.Guard != nil {
				newClause.Guard = substituteParamsExpr(clause.Guard, substMap)
			}
			newClauses[i] = newClause
		}
		var newDefault Expression
		if e.DefaultExpr != nil {
			newDefault = substituteParamsExpr(e.DefaultExpr, substMap)
		}
		return &MatchExpr{
			Condition:   substituteParamsExpr(e.Condition, substMap),
			Clauses:     newClauses,
			DefaultExpr: newDefault,
		}
	case *LambdaExpr:
		// Don't substitute inside nested lambdas' parameters
		// But do substitute in the body (closure)
		return &LambdaExpr{
			Params: e.Params,
			Body:   substituteParamsExpr(e.Body, substMap),
			IsPure: e.IsPure,
		}
	case *BlockExpr:
		// Substitute in block statements
		newStatements := make([]Statement, len(e.Statements))
		for i, stmt := range e.Statements {
			newStatements[i] = substituteParamsStmt(stmt, substMap)
		}
		return &BlockExpr{Statements: newStatements}
	case *FMAExpr:
		return &FMAExpr{
			A:        substituteParamsExpr(e.A, substMap),
			B:        substituteParamsExpr(e.B, substMap),
			C:        substituteParamsExpr(e.C, substMap),
			IsSub:    e.IsSub,
			IsNegMul: e.IsNegMul,
		}
	case *FieldAccessExpr:
		return &FieldAccessExpr{
			Object:     substituteParamsExpr(e.Object, substMap),
			FieldName:  e.FieldName,
			StructName: e.StructName,
			Offset:     e.Offset,
		}
	case *CastExpr:
		return &CastExpr{
			Expr:       substituteParamsExpr(e.Expr, substMap),
			Type:       e.Type,
			RawBitcast: e.RawBitcast,
		}
	case *UnaryExpr:
		return &UnaryExpr{Operator: e.Operator, Operand: substituteParamsExpr(e.Operand, substMap)}
	case *LengthExpr:
		return &LengthExpr{Operand: substituteParamsExpr(e.Operand, substMap)}
	case *JumpExpr:
		var v Expression
		if e.Value != nil {
			v = substituteParamsExpr(e.Value, substMap)
		}
		return &JumpExpr{Label: e.Label, Value: v, IsBreak: e.IsBreak}
	default:
		// Literals (NumberExpr, StringExpr, etc.) are returned as-is
		return expr
	}
}

func substituteParamsStmt(stmt Statement, substMap map[string]Expression) Statement {
	switch s := stmt.(type) {
	case *AssignStmt:
		return &AssignStmt{
			Name:     s.Name,
			Value:    substituteParamsExpr(s.Value, substMap),
			Mutable:  s.Mutable,
			IsUpdate: s.IsUpdate,
		}
	case *ExpressionStmt:
		return &ExpressionStmt{
			Expr: substituteParamsExpr(s.Expr, substMap),
		}
	case *LoopStmt:
		newBody := make([]Statement, len(s.Body))
		for i, bodyStmt := range s.Body {
			newBody[i] = substituteParamsStmt(bodyStmt, substMap)
		}
		return &LoopStmt{
			Iterator:      s.Iterator,
			Iterable:      substituteParamsExpr(s.Iterable, substMap),
			Body:          newBody,
			MaxIterations: s.MaxIterations,
			NeedsMaxCheck: s.NeedsMaxCheck,
			NumThreads:    s.NumThreads,
		}
	default:
		return stmt
	}
}

// Helper functions for integer strength reduction

// isInUnsafeContext checks if an expression is within an unsafe block
// This is a simple heuristic - we consider expressions to be in unsafe context
// if they contain explicit integer type casts or are within UnsafeExpr
func isInUnsafeContext(expr Expression) bool {
	switch e := expr.(type) {
	case *UnsafeExpr:
		return true
	case *CastExpr:
		// Check if casting to an integer type
		intTypes := []string{"int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64"}
		return slices.Contains(intTypes, e.Type)
	case *BinaryExpr:
		// If either operand is in unsafe context, the whole expression is
		return isInUnsafeContext(e.Left) || isInUnsafeContext(e.Right)
	case *UnaryExpr:
		return isInUnsafeContext(e.Operand)
	default:
		return false
	}
}

// hasIntegerTypeAnnotation checks if an expression has an explicit integer type annotation
func hasIntegerTypeAnnotation(expr Expression) bool {
	switch e := expr.(type) {
	case *CastExpr:
		intTypes := []string{"int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64"}
		if slices.Contains(intTypes, e.Type) {
			return true
		}
		// Check the inner expression too
		return hasIntegerTypeAnnotation(e.Expr)
	case *BinaryExpr:
		// Check both operands
		return hasIntegerTypeAnnotation(e.Left) || hasIntegerTypeAnnotation(e.Right)
	case *UnaryExpr:
		return hasIntegerTypeAnnotation(e.Operand)
	case *IdentExpr:
		// For identifiers, we can't tell from the expression alone
		// This would require type tracking, so we return false
		return false
	default:
		return false
	}
}

// shouldApplyIntegerOptimization determines if integer-only optimizations should be applied
// These optimizations (shift instead of multiply, mask instead of modulo) are only valid
// for integer operations, not float64. We apply them only in unsafe blocks or with explicit
// integer type casts.
func shouldApplyIntegerOptimization(left, right Expression) bool {
	// Check if we're in an unsafe context (unsafe blocks, explicit int casts)
	if isInUnsafeContext(left) || isInUnsafeContext(right) {
		return true
	}

	// Check for explicit integer type annotations
	if hasIntegerTypeAnnotation(left) || hasIntegerTypeAnnotation(right) {
		return true
	}

	return false
}

// ---------------------------------------------------------------------------
// SROA: scalar replacement of non-escaping local aggregates.
//
// Within a block, `p = Struct(a, b, c)` (immutable, p never reassigned, p used
// only as `p.field`) is replaced by per-field scalar locals and every `p.field`
// is rewritten to the matching scalar. This removes the heap allocation and the
// pointer/memory round-trip for struct temporaries. Escape analysis stops at
// nested lambdas (a captured struct local is treated as escaping).
// ---------------------------------------------------------------------------

func sroaScalarName(structVar, field string) string {
	return "__sroa_" + structVar + "_" + field
}

// sroaStmt applies SROA inside any block reachable from stmt (lambda bodies,
// loop bodies, etc.), mutating in place.
func sroaStmt(stmt Statement) {
	switch s := stmt.(type) {
	case *AssignStmt:
		sroaExpr(s.Value)
	case *ExpressionStmt:
		sroaExpr(s.Expr)
	case *LoopStmt:
		sroaExpr(s.Iterable)
		s.Body = sroaBlockStmts(s.Body)
	case *WhileStmt:
		sroaExpr(s.Condition)
		s.Body = sroaBlockStmts(s.Body)
	case *JumpStmt:
		if s.Value != nil {
			sroaExpr(s.Value)
		}
	}
}

// sroaExpr recurses into expressions, applying SROA to any BlockExpr it finds
// (e.g. a lambda body or a block used as a value).
func sroaExpr(expr Expression) {
	switch e := expr.(type) {
	case *LambdaExpr:
		if blk, ok := e.Body.(*BlockExpr); ok {
			blk.Statements = sroaBlockStmts(blk.Statements)
		} else {
			sroaExpr(e.Body)
		}
	case *BlockExpr:
		e.Statements = sroaBlockStmts(e.Statements)
	case *BinaryExpr:
		sroaExpr(e.Left)
		sroaExpr(e.Right)
	case *CallExpr:
		for _, a := range e.Args {
			sroaExpr(a)
		}
	case *DirectCallExpr:
		sroaExpr(e.Callee)
		for _, a := range e.Args {
			sroaExpr(a)
		}
	case *MatchExpr:
		sroaExpr(e.Condition)
		for _, c := range e.Clauses {
			if c.Guard != nil {
				sroaExpr(c.Guard)
			}
			sroaExpr(c.Result)
		}
		if e.DefaultExpr != nil {
			sroaExpr(e.DefaultExpr)
		}
	case *FieldAccessExpr:
		sroaExpr(e.Object)
	case *CastExpr:
		sroaExpr(e.Expr)
	case *UnaryExpr:
		sroaExpr(e.Operand)
	case *FMAExpr:
		sroaExpr(e.A)
		sroaExpr(e.B)
		sroaExpr(e.C)
	}
}

// sroaBlockStmts scalarizes qualifying struct locals within one block's
// statement list (and recurses into nested blocks first).
func sroaBlockStmts(stmts []Statement) []Statement {
	// Recurse into nested blocks first so inner scopes are handled.
	for _, s := range stmts {
		sroaStmt(s)
	}

	out := make([]Statement, 0, len(stmts))
	for i := range stmts {
		as, ok := stmts[i].(*AssignStmt)
		if !ok || as.Mutable || as.IsUpdate {
			out = append(out, stmts[i])
			continue
		}
		ctor, ok := as.Value.(*CallExpr)
		if !ok {
			out = append(out, stmts[i])
			continue
		}
		decl := optimizerCStructDecls[ctor.Function]
		if decl == nil || len(ctor.Args) != len(decl.Fields) {
			out = append(out, stmts[i])
			continue
		}
		// `p` must not escape anywhere in the remaining statements of this block.
		rest := stmts[i+1:]
		if structVarEscapesInStmts(as.Name, rest) {
			out = append(out, stmts[i])
			continue
		}
		// Scalarize: emit one assignment per field, and rewrite p.field uses.
		fieldToScalar := make(map[string]string, len(decl.Fields))
		for fi := range decl.Fields {
			scalar := sroaScalarName(as.Name, decl.Fields[fi].Name)
			fieldToScalar[decl.Fields[fi].Name] = scalar
			out = append(out, &AssignStmt{Name: scalar, Value: ctor.Args[fi]})
		}
		for _, s := range rest {
			rewriteFieldAccessStmt(s, as.Name, fieldToScalar)
		}
	}
	return out
}

// structVarEscapesInStmts reports whether `name` is used as anything other than
// `name.field` across the given statements (recursing into nested control flow,
// stopping at nested lambdas where the var would be captured).
func structVarEscapesInStmts(name string, stmts []Statement) bool {
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *AssignStmt:
			if s.Name == name {
				return true // reassignment / redefinition — bail
			}
			if structVarEscapesInExpr(name, s.Value) {
				return true
			}
		case *ExpressionStmt:
			if structVarEscapesInExpr(name, s.Expr) {
				return true
			}
		case *LoopStmt:
			if s.Iterator == name || structVarEscapesInExpr(name, s.Iterable) || structVarEscapesInStmts(name, s.Body) {
				return true
			}
		case *WhileStmt:
			if structVarEscapesInExpr(name, s.Condition) || structVarEscapesInStmts(name, s.Body) {
				return true
			}
		case *JumpStmt:
			if s.Value != nil && structVarEscapesInExpr(name, s.Value) {
				return true
			}
		case *MapUpdateStmt:
			// `p[i] <- v` / field write — treat as escape.
			if s.MapName == name || structVarEscapesInExpr(name, s.Index) || structVarEscapesInExpr(name, s.Value) {
				return true
			}
		default:
			return true // unknown statement form — be conservative
		}
	}
	return false
}

func structVarEscapesInExpr(name string, expr Expression) bool {
	switch e := expr.(type) {
	case nil:
		return false
	case *IdentExpr:
		return e.Name == name // a bare reference is an escape
	case *FieldAccessExpr:
		if id, ok := e.Object.(*IdentExpr); ok && id.Name == name {
			return false // safe: p.field
		}
		return structVarEscapesInExpr(name, e.Object)
	case *BinaryExpr:
		return structVarEscapesInExpr(name, e.Left) || structVarEscapesInExpr(name, e.Right)
	case *UnaryExpr:
		return structVarEscapesInExpr(name, e.Operand)
	case *CastExpr:
		return structVarEscapesInExpr(name, e.Expr)
	case *LengthExpr:
		return structVarEscapesInExpr(name, e.Operand)
	case *CallExpr:
		for _, a := range e.Args {
			if structVarEscapesInExpr(name, a) {
				return true
			}
		}
		return false
	case *DirectCallExpr:
		if structVarEscapesInExpr(name, e.Callee) {
			return true
		}
		for _, a := range e.Args {
			if structVarEscapesInExpr(name, a) {
				return true
			}
		}
		return false
	case *IndexExpr:
		return structVarEscapesInExpr(name, e.List) || structVarEscapesInExpr(name, e.Index)
	case *ListExpr:
		for _, el := range e.Elements {
			if structVarEscapesInExpr(name, el) {
				return true
			}
		}
		return false
	case *MapExpr:
		for i := range e.Keys {
			if structVarEscapesInExpr(name, e.Keys[i]) || structVarEscapesInExpr(name, e.Values[i]) {
				return true
			}
		}
		return false
	case *MatchExpr:
		if structVarEscapesInExpr(name, e.Condition) {
			return true
		}
		for _, c := range e.Clauses {
			if c.Guard != nil && structVarEscapesInExpr(name, c.Guard) {
				return true
			}
			if structVarEscapesInExpr(name, c.Result) {
				return true
			}
		}
		return e.DefaultExpr != nil && structVarEscapesInExpr(name, e.DefaultExpr)
	case *FMAExpr:
		return structVarEscapesInExpr(name, e.A) || structVarEscapesInExpr(name, e.B) || structVarEscapesInExpr(name, e.C)
	case *BlockExpr:
		return structVarEscapesInStmts(name, e.Statements)
	case *JumpExpr:
		return e.Value != nil && structVarEscapesInExpr(name, e.Value)
	case *LambdaExpr:
		// Referenced inside a nested lambda => captured => escape (conservative).
		return structVarEscapesInExpr(name, e.Body)
	case *NumberExpr, *StringExpr:
		return false
	default:
		return true // unknown expr form — be conservative
	}
}

// rewriteFieldAccessStmt replaces every `name.field` with the field's scalar var
// throughout a statement, mutating in place.
func rewriteFieldAccessStmt(stmt Statement, name string, fieldToScalar map[string]string) {
	switch s := stmt.(type) {
	case *AssignStmt:
		s.Value = rewriteFieldAccessExpr(s.Value, name, fieldToScalar)
	case *ExpressionStmt:
		s.Expr = rewriteFieldAccessExpr(s.Expr, name, fieldToScalar)
	case *LoopStmt:
		s.Iterable = rewriteFieldAccessExpr(s.Iterable, name, fieldToScalar)
		for _, b := range s.Body {
			rewriteFieldAccessStmt(b, name, fieldToScalar)
		}
	case *WhileStmt:
		s.Condition = rewriteFieldAccessExpr(s.Condition, name, fieldToScalar)
		for _, b := range s.Body {
			rewriteFieldAccessStmt(b, name, fieldToScalar)
		}
	case *JumpStmt:
		if s.Value != nil {
			s.Value = rewriteFieldAccessExpr(s.Value, name, fieldToScalar)
		}
	case *MapUpdateStmt:
		s.Index = rewriteFieldAccessExpr(s.Index, name, fieldToScalar)
		s.Value = rewriteFieldAccessExpr(s.Value, name, fieldToScalar)
	}
}

func rewriteFieldAccessExpr(expr Expression, name string, fieldToScalar map[string]string) Expression {
	switch e := expr.(type) {
	case *FieldAccessExpr:
		if id, ok := e.Object.(*IdentExpr); ok && id.Name == name {
			if scalar, found := fieldToScalar[e.FieldName]; found {
				return &IdentExpr{Name: scalar}
			}
		}
		e.Object = rewriteFieldAccessExpr(e.Object, name, fieldToScalar)
		return e
	case *BinaryExpr:
		e.Left = rewriteFieldAccessExpr(e.Left, name, fieldToScalar)
		e.Right = rewriteFieldAccessExpr(e.Right, name, fieldToScalar)
		return e
	case *UnaryExpr:
		e.Operand = rewriteFieldAccessExpr(e.Operand, name, fieldToScalar)
		return e
	case *CastExpr:
		e.Expr = rewriteFieldAccessExpr(e.Expr, name, fieldToScalar)
		return e
	case *LengthExpr:
		e.Operand = rewriteFieldAccessExpr(e.Operand, name, fieldToScalar)
		return e
	case *CallExpr:
		for i := range e.Args {
			e.Args[i] = rewriteFieldAccessExpr(e.Args[i], name, fieldToScalar)
		}
		return e
	case *DirectCallExpr:
		e.Callee = rewriteFieldAccessExpr(e.Callee, name, fieldToScalar)
		for i := range e.Args {
			e.Args[i] = rewriteFieldAccessExpr(e.Args[i], name, fieldToScalar)
		}
		return e
	case *IndexExpr:
		e.List = rewriteFieldAccessExpr(e.List, name, fieldToScalar)
		e.Index = rewriteFieldAccessExpr(e.Index, name, fieldToScalar)
		return e
	case *ListExpr:
		for i := range e.Elements {
			e.Elements[i] = rewriteFieldAccessExpr(e.Elements[i], name, fieldToScalar)
		}
		return e
	case *MapExpr:
		for i := range e.Keys {
			e.Keys[i] = rewriteFieldAccessExpr(e.Keys[i], name, fieldToScalar)
			e.Values[i] = rewriteFieldAccessExpr(e.Values[i], name, fieldToScalar)
		}
		return e
	case *MatchExpr:
		e.Condition = rewriteFieldAccessExpr(e.Condition, name, fieldToScalar)
		for _, c := range e.Clauses {
			if c.Guard != nil {
				c.Guard = rewriteFieldAccessExpr(c.Guard, name, fieldToScalar)
			}
			c.Result = rewriteFieldAccessExpr(c.Result, name, fieldToScalar)
		}
		if e.DefaultExpr != nil {
			e.DefaultExpr = rewriteFieldAccessExpr(e.DefaultExpr, name, fieldToScalar)
		}
		return e
	case *FMAExpr:
		e.A = rewriteFieldAccessExpr(e.A, name, fieldToScalar)
		e.B = rewriteFieldAccessExpr(e.B, name, fieldToScalar)
		e.C = rewriteFieldAccessExpr(e.C, name, fieldToScalar)
		return e
	case *BlockExpr:
		for _, s := range e.Statements {
			rewriteFieldAccessStmt(s, name, fieldToScalar)
		}
		return e
	case *JumpExpr:
		if e.Value != nil {
			e.Value = rewriteFieldAccessExpr(e.Value, name, fieldToScalar)
		}
		return e
	}
	return expr
}

// ---------------------------------------------------------------------------
// Operator overloading for cstructs.
//
// `a + b` where a and b are both cstruct type V desugars to `V_add(a, b)` (when
// that function is defined); `*` uses `V_mul` for struct*struct or `V_scale` for
// struct*scalar; `-`→`V_sub`, `/`→`V_div`. Type inference is conservative — an
// operand's type is only known from a typed param, a typed loop var, a struct
// constructor, an `as`/`:` cast, or a call to a struct-returning function — so
// scalar arithmetic is never rewritten.
// ---------------------------------------------------------------------------

var opOverloadSuffix = map[string]string{"+": "add", "-": "sub", "*": "mul", "/": "div"}

// opDesugarMethodCall rewrites a method call `recv.method(args)` to the function
// `Type_method(recv, args)` when recv has a known cstruct type Type and that
// function is defined. Two shapes arrive from the parser: an identifier receiver
// encodes as `CallExpr{Function:"recv.method"}`, while a complex receiver becomes
// `CallExpr{Function:"method", Args:[recv, ...]}` (UFCS). Args are already
// desugared so inner method calls (and operators) have resolved to typed calls.
func opDesugarMethodCall(e *CallExpr, env, retType map[string]string, defined map[string]bool) Expression {
	// Identifier-receiver form: "recv.method".
	if dot := strings.LastIndexByte(e.Function, '.'); dot > 0 {
		recv := e.Function[:dot]
		method := e.Function[dot+1:]
		// recv must be a single cstruct-typed identifier (not a namespace/path).
		if t := env[recv]; t != "" && !strings.ContainsAny(recv, ". ()") {
			fn := t + "_" + method
			if defined[fn] {
				args := append([]Expression{&IdentExpr{Name: recv}}, e.Args...)
				return &CallExpr{Function: fn, Args: args}
			}
		}
		return e
	}
	// UFCS form: `method(recv, ...)` from a complex receiver. Only rewrite when
	// `method` is not itself a defined function but `Type_method` is, so genuine
	// free-function calls are left alone.
	if !defined[e.Function] && len(e.Args) >= 1 {
		if t := opExprStructType(e.Args[0], env, retType); t != "" {
			fn := t + "_" + e.Function
			if defined[fn] {
				return &CallExpr{Function: fn, Args: e.Args}
			}
		}
	}
	return e
}

func desugarOperatorOverloads(program *Program) {
	// Names of all defined functions, and the cstruct type each returns (if any).
	defined := make(map[string]bool)
	retType := make(map[string]string)
	var lambdas []*LambdaExpr
	var names []string
	for _, stmt := range program.Statements {
		if as, ok := stmt.(*AssignStmt); ok {
			if lam, ok := as.Value.(*LambdaExpr); ok {
				defined[as.Name] = true
				lambdas = append(lambdas, lam)
				names = append(names, as.Name)
			}
		}
	}
	// Top-level cstruct-typed variables (e.g. `sun := V(...)`, and locals defined
	// inside the program's top-level while/arena blocks): their types must be
	// visible so operators/methods on them resolve. opCollectLocalTypesStmt
	// recurses into loop/while/arena/if bodies.
	collectGlobals := func() map[string]string {
		env := make(map[string]string)
		for _, stmt := range program.Statements {
			if as, ok := stmt.(*AssignStmt); ok {
				if _, isLam := as.Value.(*LambdaExpr); isLam {
					continue
				}
			}
			opCollectLocalTypesStmt(stmt, env, retType)
		}
		return env
	}
	// First pass seeds constructor/cast-typed globals (which need no retType) so
	// the fixpoint can resolve functions that reference them (e.g. `sun`).
	globalEnv := collectGlobals()

	// Compute return types to a fixpoint: a method like `V.norm` returns whatever
	// `self.scale(...)` returns, so a function's result type can depend on another
	// function's. Iterating until stable (bounded by the function count) resolves
	// these chains regardless of definition order.
	for pass := 0; pass <= len(lambdas); pass++ {
		changed := false
		for i, lam := range lambdas {
			if t := opLambdaReturnType(lam, globalEnv, retType); t != retType[names[i]] {
				retType[names[i]] = t
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	// Re-collect now that retType is known, so top-level locals derived from
	// struct-returning calls (e.g. `fwd := (a - b).norm()`) are typed too.
	globalEnv = collectGlobals()

	process := func(lam *LambdaExpr) {
		env := make(map[string]string)
		maps.Copy(env, globalEnv)
		maps.Copy(env, lam.ParamCStructTypes)
		opCollectLocalTypes(lam.Body, env, retType)
		// Assign back: a single-expression body whose outermost node is itself an
		// operator or method call must be replaced, not just mutated in place.
		lam.Body = opDesugarExpr(lam.Body, env, retType, defined)
	}

	for _, stmt := range program.Statements {
		if as, ok := stmt.(*AssignStmt); ok {
			if lam, ok := as.Value.(*LambdaExpr); ok {
				process(lam)
				continue
			}
		}
		// Top-level non-function statement: desugar with the global env so
		// operators on cstruct-typed globals resolve.
		opDesugarStmt(stmt, globalEnv, retType, defined)
	}
}

// opLambdaReturnType returns the cstruct type a lambda's body evaluates to (its
// final expression: a constructor, cast, operator result, or call to a
// struct-returning func). It evaluates in an env built from the lambda's typed
// params (including the implicit `self` of a method) so receiver-typed method
// calls and operators in the body resolve.
func opLambdaReturnType(lam *LambdaExpr, globalEnv, retType map[string]string) string {
	env := make(map[string]string)
	maps.Copy(env, globalEnv)
	maps.Copy(env, lam.ParamCStructTypes)
	opCollectLocalTypes(lam.Body, env, retType)
	if b, ok := lam.Body.(*BlockExpr); ok {
		return opBlockReturnType(b.Statements, env, retType)
	}
	return opExprStructType(lam.Body, env, retType)
}

// opBlockReturnType returns the cstruct type a statement block evaluates to: the
// type of its final statement's value. A trailing `if`/`elif`/`else` (which the
// parser lowers to an IfStmt, not an expression) is followed into each arm, since
// a struct-returning function may end in one (e.g. `V.norm`'s normalize-or-self).
func opBlockReturnType(stmts []Statement, env, retType map[string]string) string {
	if len(stmts) == 0 {
		return ""
	}
	switch s := stmts[len(stmts)-1].(type) {
	case *ExpressionStmt:
		return opExprStructType(s.Expr, env, retType)
	case *JumpStmt:
		if s.Value != nil {
			return opExprStructType(s.Value, env, retType)
		}
	case *IfStmt:
		for _, br := range s.Branches {
			if t := opBlockReturnType(br.Body, env, retType); t != "" {
				return t
			}
		}
		return opBlockReturnType(s.ElseBody, env, retType)
	}
	return ""
}

// opExprStructType infers the cstruct type of an expression given a local type
// env and the function-return-type map (both may be nil). It understands
// constructors, casts, typed locals/params, nested cstruct fields (`b.c`),
// operator expressions that desugar to struct-returning methods (`a + b`,
// `v * s`), and method calls whose function returns a struct (`p.norm()`).
func opExprStructType(expr Expression, env, retType map[string]string) string {
	switch e := expr.(type) {
	case *CastExpr:
		if optimizerCStructNames[e.Type] {
			return e.Type
		}
	case *CallExpr:
		if optimizerCStructNames[e.Function] {
			return e.Function // a constructor
		}
		// A method call still in `recv.method(...)` form: resolve the receiver's
		// type and look up the corresponding `Type_method` return type.
		if dot := strings.LastIndexByte(e.Function, '.'); dot > 0 && env != nil && retType != nil {
			recv := e.Function[:dot]
			if t := env[recv]; t != "" && !strings.ContainsAny(recv, ". ()") {
				return retType[t+"_"+e.Function[dot+1:]]
			}
		}
		// UFCS form `method(recv, ...)`: if `recv` is struct-typed, the call may
		// resolve to `Type_method`.
		if retType != nil && len(e.Args) >= 1 {
			if t := opExprStructType(e.Args[0], env, retType); t != "" {
				if rt, ok := retType[t+"_"+e.Function]; ok {
					return rt
				}
			}
		}
		// Plain free-function call.
		if retType != nil {
			return retType[e.Function]
		}
	case *FieldAccessExpr:
		// Nested cstruct field: `b.c` where b: Ball and Ball has `c: V`.
		ot := e.StructName
		if ot == "" {
			ot = opExprStructType(e.Object, env, retType)
		}
		if decl := optimizerCStructDecls[ot]; decl != nil {
			for _, f := range decl.Fields {
				if f.Name == e.FieldName {
					return f.StructName // "" for scalar fields
				}
			}
		}
	case *BinaryExpr:
		// An operator that desugars to a struct-returning method. Mirror the
		// resolution in opDesugarExpr to predict the result type.
		suffix, ok := opOverloadSuffix[e.Operator]
		if !ok {
			return ""
		}
		lt := opExprStructType(e.Left, env, retType)
		rt := opExprStructType(e.Right, env, retType)
		if lt != "" && lt == rt {
			return retType[lt+"_"+suffix]
		}
		if e.Operator == "*" {
			if lt != "" && rt == "" {
				return retType[lt+"_scale"]
			}
			if rt != "" && lt == "" {
				return retType[rt+"_scale"]
			}
		}
	case *MatchExpr:
		// `if c { a } else { b }` / guard match: every arm evaluates to the same
		// type, so the result type is that of any arm that resolves to a cstruct.
		for _, c := range e.Clauses {
			if c.Result != nil {
				if t := opExprStructType(c.Result, env, retType); t != "" {
					return t
				}
			}
		}
		if e.DefaultExpr != nil {
			return opExprStructType(e.DefaultExpr, env, retType)
		}
	case *BlockExpr:
		// A block evaluates to its final expression.
		if len(e.Statements) > 0 {
			if es, ok := e.Statements[len(e.Statements)-1].(*ExpressionStmt); ok {
				return opExprStructType(es.Expr, env, retType)
			}
		}
	case *FMAExpr:
		// Pass 1 may fuse `a*b + c` into an FMAExpr before operator overloading
		// runs. If any operand is cstruct-typed this is really a chain of method
		// calls (`a.scale(b).add(c)` etc.); infer its type from the equivalent
		// `(a*b) <op> c` BinaryExpr so the desugar can later un-fuse it.
		return opExprStructType(fmaToBinary(e), env, retType)
	case *IdentExpr:
		if env != nil {
			return env[e.Name]
		}
	}
	return ""
}

// fmaToBinary reconstructs the `(A*B) <op> C` BinaryExpr an FMAExpr was fused
// from, so the operator-overload machinery can treat it like any other operator
// expression (for cstruct type inference and un-fusing into method calls).
func fmaToBinary(e *FMAExpr) Expression {
	mul := &BinaryExpr{Left: e.A, Operator: "*", Right: e.B}
	switch {
	case !e.IsNegMul && !e.IsSub: // A*B + C
		return &BinaryExpr{Left: mul, Operator: "+", Right: e.C}
	case !e.IsNegMul && e.IsSub: // A*B - C
		return &BinaryExpr{Left: mul, Operator: "-", Right: e.C}
	case e.IsNegMul && !e.IsSub: // C - A*B
		return &BinaryExpr{Left: e.C, Operator: "-", Right: mul}
	default: // -A*B - C  ==  (0 - C) - A*B
		zero := &NumberExpr{Value: 0}
		return &BinaryExpr{Left: &BinaryExpr{Left: zero, Operator: "-", Right: e.C}, Operator: "-", Right: mul}
	}
}

// opCollectLocalTypes scans a function body (recursing into nested control flow
// but not nested lambdas) and records the cstruct type of struct-typed locals
// and typed loop variables into env.
func opCollectLocalTypes(expr Expression, env, retType map[string]string) {
	switch e := expr.(type) {
	case *BlockExpr:
		for _, stmt := range e.Statements {
			opCollectLocalTypesStmt(stmt, env, retType)
		}
	default:
		// single-expression body: nothing to collect
	}
}

func opCollectLocalTypesStmt(stmt Statement, env, retType map[string]string) {
	switch s := stmt.(type) {
	case *AssignStmt:
		if !s.IsUpdate {
			if t := opExprStructType(s.Value, env, retType); t != "" {
				env[s.Name] = t
			}
		}
	case *LoopStmt:
		if optimizerCStructNames[s.IteratorType] {
			env[s.Iterator] = s.IteratorType
		}
		for _, b := range s.Body {
			opCollectLocalTypesStmt(b, env, retType)
		}
	case *WhileStmt:
		for _, b := range s.Body {
			opCollectLocalTypesStmt(b, env, retType)
		}
	case *ArenaStmt:
		for _, b := range s.Body {
			opCollectLocalTypesStmt(b, env, retType)
		}
	case *IfStmt:
		for _, br := range s.Branches {
			for _, b := range br.Body {
				opCollectLocalTypesStmt(b, env, retType)
			}
		}
		for _, b := range s.ElseBody {
			opCollectLocalTypesStmt(b, env, retType)
		}
	}
}

func opDesugarStmt(stmt Statement, env, retType map[string]string, defined map[string]bool) {
	switch s := stmt.(type) {
	case *AssignStmt:
		s.Value = opDesugarExpr(s.Value, env, retType, defined)
	case *ExpressionStmt:
		s.Expr = opDesugarExpr(s.Expr, env, retType, defined)
	case *LoopStmt:
		s.Iterable = opDesugarExpr(s.Iterable, env, retType, defined)
		for _, b := range s.Body {
			opDesugarStmt(b, env, retType, defined)
		}
	case *WhileStmt:
		s.Condition = opDesugarExpr(s.Condition, env, retType, defined)
		for _, b := range s.Body {
			opDesugarStmt(b, env, retType, defined)
		}
	case *JumpStmt:
		if s.Value != nil {
			s.Value = opDesugarExpr(s.Value, env, retType, defined)
		}
	case *MapUpdateStmt:
		s.Index = opDesugarExpr(s.Index, env, retType, defined)
		s.Value = opDesugarExpr(s.Value, env, retType, defined)
	case *ArenaStmt:
		for _, b := range s.Body {
			opDesugarStmt(b, env, retType, defined)
		}
	case *IfStmt:
		for i := range s.Branches {
			s.Branches[i].Condition = opDesugarExpr(s.Branches[i].Condition, env, retType, defined)
			for _, b := range s.Branches[i].Body {
				opDesugarStmt(b, env, retType, defined)
			}
		}
		for _, b := range s.ElseBody {
			opDesugarStmt(b, env, retType, defined)
		}
	}
}

func opDesugarExpr(expr Expression, env, retType map[string]string, defined map[string]bool) Expression {
	switch e := expr.(type) {
	case *BinaryExpr:
		e.Left = opDesugarExpr(e.Left, env, retType, defined)
		e.Right = opDesugarExpr(e.Right, env, retType, defined)
		suffix, ok := opOverloadSuffix[e.Operator]
		if !ok {
			return e
		}
		lt := opExprStructType(e.Left, env, retType)
		rt := opExprStructType(e.Right, env, retType)
		// struct OP struct (same type): T_<op>
		if lt != "" && lt == rt {
			fn := lt + "_" + suffix
			if defined[fn] {
				return &CallExpr{Function: fn, Args: []Expression{e.Left, e.Right}}
			}
			return e
		}
		// struct * scalar  or  scalar * struct: T_scale(struct, scalar)
		if e.Operator == "*" {
			if lt != "" && rt == "" {
				if fn := lt + "_scale"; defined[fn] {
					return &CallExpr{Function: fn, Args: []Expression{e.Left, e.Right}}
				}
			} else if rt != "" && lt == "" {
				if fn := rt + "_scale"; defined[fn] {
					return &CallExpr{Function: fn, Args: []Expression{e.Right, e.Left}}
				}
			}
		}
		return e
	case *CallExpr:
		for i := range e.Args {
			e.Args[i] = opDesugarExpr(e.Args[i], env, retType, defined)
		}
		return opDesugarMethodCall(e, env, retType, defined)
	case *DirectCallExpr:
		e.Callee = opDesugarExpr(e.Callee, env, retType, defined)
		for i := range e.Args {
			e.Args[i] = opDesugarExpr(e.Args[i], env, retType, defined)
		}
		return e
	case *UnaryExpr:
		e.Operand = opDesugarExpr(e.Operand, env, retType, defined)
		return e
	case *CastExpr:
		e.Expr = opDesugarExpr(e.Expr, env, retType, defined)
		return e
	case *LengthExpr:
		e.Operand = opDesugarExpr(e.Operand, env, retType, defined)
		return e
	case *FieldAccessExpr:
		e.Object = opDesugarExpr(e.Object, env, retType, defined)
		return e
	case *IndexExpr:
		e.List = opDesugarExpr(e.List, env, retType, defined)
		e.Index = opDesugarExpr(e.Index, env, retType, defined)
		return e
	case *ListExpr:
		for i := range e.Elements {
			e.Elements[i] = opDesugarExpr(e.Elements[i], env, retType, defined)
		}
		return e
	case *MapExpr:
		for i := range e.Keys {
			e.Keys[i] = opDesugarExpr(e.Keys[i], env, retType, defined)
			e.Values[i] = opDesugarExpr(e.Values[i], env, retType, defined)
		}
		return e
	case *MatchExpr:
		e.Condition = opDesugarExpr(e.Condition, env, retType, defined)
		for _, c := range e.Clauses {
			if c.Guard != nil {
				c.Guard = opDesugarExpr(c.Guard, env, retType, defined)
			}
			c.Result = opDesugarExpr(c.Result, env, retType, defined)
		}
		if e.DefaultExpr != nil {
			e.DefaultExpr = opDesugarExpr(e.DefaultExpr, env, retType, defined)
		}
		return e
	case *FMAExpr:
		// If this fused multiply-add is really a cstruct operator chain (the FMA
		// pass ran before operator overloading), un-fuse it back to `(a*b) <op> c`
		// and desugar that into method calls. Float FMA on cstruct pointer bits
		// would be silently wrong, so this must fire whenever a cstruct is involved.
		if opExprStructType(e, env, retType) != "" {
			return opDesugarExpr(fmaToBinary(e), env, retType, defined)
		}
		e.A = opDesugarExpr(e.A, env, retType, defined)
		e.B = opDesugarExpr(e.B, env, retType, defined)
		e.C = opDesugarExpr(e.C, env, retType, defined)
		return e
	case *BlockExpr:
		// A nested lambda's body or block: process its statements with the same
		// env (locals already collected for the enclosing function).
		for _, s := range e.Statements {
			opDesugarStmt(s, env, retType, defined)
		}
		return e
	case *LambdaExpr:
		// A nested lambda — process it with its own env (its typed params + the
		// outer env, so it can still see outer struct vars it captures).
		inner := make(map[string]string)
		maps.Copy(inner, env)
		maps.Copy(inner, e.ParamCStructTypes)
		opCollectLocalTypes(e.Body, inner, retType)
		e.Body = opDesugarExpr(e.Body, inner, retType, defined)
		return e
	}
	return expr
}
