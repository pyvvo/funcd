// Package expr is the funcd reference engine (ADR-0095): one typed `${{ … }}`
// expression grammar shared across the platform. The contents of `${{ … }}` are
// native JavaScript, parsed and evaluated by goja (pure-Go ECMAScript); funcd runs
// a type-checker over goja's parser AST that admits only a typed subset and rejects
// the rest at reconcile — so every expression is statically checkable against a
// JSON-Schema contract before anything runs, in a language users already know.
//
// Two modes: Select (a reference or computed value, returned by Eval) and Condition
// (a boolean expression, returned by EvalBool). The lifecycle is always
// Parse → Check → Eval/EvalBool: Parse validates the grammar, Check validates every
// path/type/operator against a context-scoped Resolver, and Eval/EvalBool run the
// compiled program against the actual documents. Eval before a successful Check (or
// in the wrong mode) is a programming error surfaced as a fault.Internal, never a
// panic.
package expr

import (
	"encoding/json"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
	"github.com/green-0-rabbit/funcd/api/fault"
)

// Mode selects the grammar's evaluation mode.
type Mode int

const (
	// Select allows a reference or computed value and returns it from Eval.
	Select Mode = iota
	// Condition requires a boolean expression and returns it from EvalBool.
	Condition
)

// Field is what a Resolver reports for a resolved path: the JSON-Schema type, the
// element type for arrays, whether the field is required, and its declared default.
type Field struct {
	// Type is the JSON-Schema type: "string", "number", "integer", "boolean",
	// "array" or "object".
	Type string
	// Items is the element type when Type == "array"; empty otherwise.
	Items string
	// Required reports whether the field is required by its schema.
	Required bool
	// HasDefault reports whether the schema declares a default for the field.
	HasDefault bool
	// Default is the declared default, substituted at evaluation when absent.
	Default json.RawMessage
}

// Resolver answers path lookups against a consumer's JSON-Schema contracts.
// Implementations are context-scoped: Roots reports the exposed root documents
// (Check matches an expression's leading member segments against it, longest-first)
// and Resolve reports the Field at a path under one of those roots.
type Resolver interface {
	// Roots reports the root documents this resolver exposes.
	Roots() []string
	// Resolve reports the Field at path under root: fault.NotFound when the path is
	// absent from the schema, fault.Invalid for a malformed request.
	Resolve(root string, path []string) (Field, error)
}

// Expr is a parsed (and, after Check, validated) expression. It is safe to Check
// once and then Eval/EvalBool concurrently.
type Expr struct {
	mode     Mode
	inner    string // the JavaScript between ${{ and }}
	program  *ast.Program
	compiled *goja.Program
	checked  bool
	roots    []string
	defs     []defaultBinding // defaulted references collected at Check
}

// defaultBinding records a referenced optional field that carries a schema default,
// so Eval can substitute it when the field is absent.
type defaultBinding struct {
	root string
	path []string
	val  json.RawMessage
}

const parseOp = "expr.parse"

// Parse validates the `${{ … }}` wrapper and parses its JavaScript with goja's
// parser. Grammar errors are positioned fault.Invalid. The whole source must be a
// single `${{ … }}` template — no surrounding text (no string interpolation).
func Parse(src string, mode Mode) (*Expr, error) {
	trimmed := strings.TrimSpace(src)
	if !strings.HasPrefix(trimmed, "${{") || !strings.HasSuffix(trimmed, "}}") || len(trimmed) < 5 {
		return nil, fault.Invalidf(parseOp, "expression must be a single ${{ … }} template")
	}
	inner := strings.TrimSpace(trimmed[3 : len(trimmed)-2])
	if inner == "" {
		return nil, fault.Invalidf(parseOp, "empty expression")
	}
	// A bare object literal (`{a: 1}`) parses as a block statement, not an expression, so wrap a
	// leading-`{` source in parens — the JS idiom (`() => ({…})`). Safe and targeted: a leading `{`
	// is only ever an intended object literal here (a block statement is rejected below anyway). The
	// wrapped form is stored so Check (AST walk) and Eval (compile) stay consistent. ADR-0096 lets a
	// Select `pass` construct objects; ADR-0095's Condition mode still rejects them (checkObjectLiteral).
	if strings.HasPrefix(inner, "{") {
		inner = "(" + inner + ")"
	}
	prog, err := parser.ParseFile(nil, "expr", inner, 0)
	if err != nil {
		return nil, fault.Invalidf(parseOp, "invalid expression: %s", oneLine(err.Error()))
	}
	if len(prog.Body) != 1 {
		return nil, fault.Invalidf(parseOp, "expression must be a single expression, not %d statements", len(prog.Body))
	}
	if _, ok := prog.Body[0].(*ast.ExpressionStatement); !ok {
		return nil, fault.Invalidf(parseOp, "expression must be a value, not a statement")
	}
	return &Expr{mode: mode, inner: inner, program: prog}, nil
}

// Roots reports the distinct root documents the expression references, populated
// after a successful Check (nil before).
func (e *Expr) Roots() []string {
	if !e.checked {
		return nil
	}
	return e.roots
}

func (e *Expr) rootExpr() ast.Expression {
	return e.program.Body[0].(*ast.ExpressionStatement).Expression
}

func errUnchecked(op string) error {
	return fault.Internalf(op, "expression evaluated before a successful Check (or in the wrong mode)")
}

func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
