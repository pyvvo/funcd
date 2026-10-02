package expr

import (
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/token"
	"github.com/pyvvo/funcd/api/fault"
)

const checkOp = "expr.check"

// kind is the internal static type of a value or sub-expression.
type kind uint8

const (
	kindUnknown kind = iota
	kindString
	kindNumber
	kindInteger
	kindBoolean
	kindArray
	kindObject
)

// typ is a checked node's type: its kind plus, for arrays, the element kind.
type typ struct {
	k     kind
	items kind
	// absent marks an existence probe whose path is missing from the checked document.
	absent bool
}

// checkCtx carries the resolver and the active guard paths (references made exempt
// from the defaults rule by an enclosing `X !== undefined && …`).
type checkCtx struct {
	r      Resolver
	roots  []string
	guards [][]string
	e      *Expr
}

func (c checkCtx) withGuard(path []string) checkCtx {
	g := make([][]string, len(c.guards), len(c.guards)+1)
	copy(g, c.guards)
	c.guards = append(g, path)
	return c
}

func (c checkCtx) exempt(path []string) bool {
	for _, g := range c.guards {
		if isPrefix(g, path) {
			return true
		}
	}
	return false
}

// Check statically validates the expression against the resolver: every reference
// path exists and is typed, every operator/method matches its operand types, only
// the admitted JS subset is used, the defaults rule holds (an optional field must
// declare a default or be `!== undefined`-guarded), and — in Condition mode — the
// whole expression is boolean. It records the referenced roots and defaulted
// references so Eval needs no resolver.
func (e *Expr) Check(r Resolver) error {
	ctx := checkCtx{r: r, roots: r.Roots(), e: e}
	root := e.rootExpr()
	if e.mode == Condition {
		if _, isRef := flattenRef(root); isRef {
			return fault.Invalidf(checkOp, "a condition must be an explicit boolean expression, not a bare reference (use `=== true` for booleans) (position %d)", pos(root))
		}
	}
	t, err := check(root, ctx)
	if err != nil {
		return err
	}
	if e.mode == Condition && t.k != kindBoolean {
		return fault.Invalidf(checkOp, "a condition must be a boolean expression, got %s (position %d)", kindName(t.k), pos(root))
	}
	e.checked = true
	return nil
}

func check(node ast.Expression, ctx checkCtx) (typ, error) {
	switch n := node.(type) {
	case *ast.CallExpression:
		return checkCall(n, ctx)
	case *ast.DotExpression:
		if n.Identifier.Name.String() == "length" {
			lt, err := check(n.Left, ctx)
			if err == nil && (lt.k == kindString || lt.k == kindArray) {
				return typ{k: kindInteger}, nil
			}
		}
		return checkReference(node, ctx)
	case *ast.BracketExpression:
		return checkReference(node, ctx)
	case *ast.Identifier:
		if n.Name.String() == "undefined" {
			return typ{}, fault.Invalidf(checkOp, "`undefined` is only valid in an existence check `x !== undefined` (position %d)", pos(node))
		}
		return checkReference(node, ctx)
	case *ast.BinaryExpression:
		return checkBinary(n, ctx)
	case *ast.UnaryExpression:
		return checkUnary(n, ctx)
	case *ast.ConditionalExpression:
		return checkConditional(n, ctx)
	case *ast.NumberLiteral:
		return typ{k: kindNumber}, nil
	case *ast.StringLiteral:
		return typ{k: kindString}, nil
	case *ast.BooleanLiteral:
		return typ{k: kindBoolean}, nil
	case *ast.ArrayLiteral:
		return checkArrayLiteral(n, ctx)
	case *ast.ObjectLiteral:
		return checkObjectLiteral(n, ctx)
	default:
		return typ{}, fault.Invalidf(checkOp, "unsupported expression %T (position %d)", node, pos(node))
	}
}

func checkBinary(b *ast.BinaryExpression, ctx checkCtx) (typ, error) {
	switch b.Operator {
	case token.EQUAL, token.NOT_EQUAL:
		return typ{}, fault.Invalidf(checkOp, "use === / !== (== and != coerce) (position %d)", pos(b))
	case token.STRICT_EQUAL, token.STRICT_NOT_EQUAL:
		// Existence check: `ref === undefined` / `ref !== undefined`.
		if ref, ok := undefinedOperand(b); ok {
			rt, err := resolveRefExpr(ref, ctx, true)
			if err != nil {
				return typ{}, err
			}
			return typ{k: kindBoolean, absent: rt.absent}, nil
		}
		lt, err := check(b.Left, ctx)
		if err != nil {
			return typ{}, err
		}
		rt, err := check(b.Right, ctx)
		if err != nil {
			return typ{}, err
		}
		if !isScalar(lt.k) || !scalarMatch(lt.k, rt.k) {
			return typ{}, fault.Invalidf(checkOp, "=== / !== need same-type scalars, got %s and %s (position %d)", kindName(lt.k), kindName(rt.k), pos(b))
		}
		return typ{k: kindBoolean}, nil
	case token.LESS, token.GREATER, token.LESS_OR_EQUAL, token.GREATER_OR_EQUAL:
		if err := requireNumeric(b.Left, b.Right, ctx, "comparison"); err != nil {
			return typ{}, err
		}
		return typ{k: kindBoolean}, nil
	case token.LOGICAL_AND, token.LOGICAL_OR:
		lt, err := check(b.Left, ctx)
		if err != nil {
			return typ{}, err
		}
		if lt.k != kindBoolean {
			return typ{}, fault.Invalidf(checkOp, "&& / || need boolean operands (no truthiness), got %s (position %d)", kindName(lt.k), pos(b.Left))
		}
		rctx := ctx
		if b.Operator == token.LOGICAL_AND {
			if gp, ok := guardPath(b.Left); ok {
				if lt.absent {
					return typ{k: kindBoolean}, nil // the guarded path is absent: && short-circuits, the right operand is never read
				}
				rctx = ctx.withGuard(gp)
			}
		}
		rt, err := check(b.Right, rctx)
		if err != nil {
			return typ{}, err
		}
		if rt.k != kindBoolean {
			return typ{}, fault.Invalidf(checkOp, "&& / || need boolean operands, got %s (position %d)", kindName(rt.k), pos(b.Right))
		}
		return typ{k: kindBoolean}, nil
	case token.PLUS:
		lt, err := check(b.Left, ctx)
		if err != nil {
			return typ{}, err
		}
		rt, err := check(b.Right, ctx)
		if err != nil {
			return typ{}, err
		}
		if lt.k == kindString && rt.k == kindString {
			return typ{k: kindString}, nil
		}
		if isNumeric(lt.k) && isNumeric(rt.k) {
			return typ{k: kindNumber}, nil
		}
		return typ{}, fault.Invalidf(checkOp, "+ needs two strings or two numbers, got %s and %s (position %d)", kindName(lt.k), kindName(rt.k), pos(b))
	case token.MINUS, token.MULTIPLY, token.SLASH:
		if err := requireNumeric(b.Left, b.Right, ctx, "arithmetic"); err != nil {
			return typ{}, err
		}
		if b.Operator == token.SLASH {
			if lit, ok := b.Right.(*ast.NumberLiteral); ok {
				if v, ok := numVal(lit); ok && v == 0 {
					return typ{}, fault.Invalidf(checkOp, "division by a literal zero (position %d)", pos(b))
				}
			}
		}
		return typ{k: kindNumber}, nil
	default:
		return typ{}, fault.Invalidf(checkOp, "operator %q is not allowed (position %d)", b.Operator.String(), pos(b))
	}
}

func checkUnary(u *ast.UnaryExpression, ctx checkCtx) (typ, error) {
	switch u.Operator {
	case token.NOT:
		t, err := check(u.Operand, ctx)
		if err != nil {
			return typ{}, err
		}
		if t.k != kindBoolean {
			return typ{}, fault.Invalidf(checkOp, "! needs a boolean operand, got %s (position %d)", kindName(t.k), pos(u))
		}
		return typ{k: kindBoolean}, nil
	case token.MINUS:
		t, err := check(u.Operand, ctx)
		if err != nil {
			return typ{}, err
		}
		if !isNumeric(t.k) {
			return typ{}, fault.Invalidf(checkOp, "unary - needs a number, got %s (position %d)", kindName(t.k), pos(u))
		}
		return typ{k: kindNumber}, nil
	default:
		return typ{}, fault.Invalidf(checkOp, "unary operator %q is not allowed (position %d)", u.Operator.String(), pos(u))
	}
}

func checkConditional(c *ast.ConditionalExpression, ctx checkCtx) (typ, error) {
	tt, err := check(c.Test, ctx)
	if err != nil {
		return typ{}, err
	}
	if tt.k != kindBoolean {
		return typ{}, fault.Invalidf(checkOp, "ternary condition must be boolean, got %s (position %d)", kindName(tt.k), pos(c.Test))
	}
	ct, err := check(c.Consequent, ctx)
	if err != nil {
		return typ{}, err
	}
	at, err := check(c.Alternate, ctx)
	if err != nil {
		return typ{}, err
	}
	if ct.k != at.k {
		return typ{}, fault.Invalidf(checkOp, "ternary branches must be the same type, got %s and %s (position %d)", kindName(ct.k), kindName(at.k), pos(c))
	}
	return ct, nil
}

func checkArrayLiteral(a *ast.ArrayLiteral, ctx checkCtx) (typ, error) {
	if len(a.Value) == 0 {
		return typ{}, fault.Invalidf(checkOp, "empty array literal is not allowed (position %d)", pos(a))
	}
	var item kind
	for i, el := range a.Value {
		t, err := check(el, ctx)
		if err != nil {
			return typ{}, err
		}
		if !isScalar(t.k) {
			return typ{}, fault.Invalidf(checkOp, "array literal elements must be scalar, got %s (position %d)", kindName(t.k), pos(el))
		}
		if i == 0 {
			item = t.k
		} else if !scalarMatch(item, t.k) {
			return typ{}, fault.Invalidf(checkOp, "array literal elements must share a type (position %d)", pos(el))
		}
	}
	return typ{k: kindArray, items: item}, nil
}

// checkObjectLiteral admits an object literal `{ key: expr, … }` — the value-construction primitive
// for a builtin `pass` step (ADR-0096). Select mode only (a Condition must be an explicit boolean,
// never an object). Keys are names or string literals (not computed); each value is checked in
// context like any subexpression. The node types to kindObject (opaque — pass output isn't re-typed
// against a contract). Mirrors checkArrayLiteral; Eval returns the object via Export() unchanged.
func checkObjectLiteral(o *ast.ObjectLiteral, ctx checkCtx) (typ, error) {
	if ctx.e.mode != Select {
		return typ{}, fault.Invalidf(checkOp, "object literals are only allowed in a select expression (position %d)", pos(o))
	}
	if len(o.Value) == 0 {
		return typ{}, fault.Invalidf(checkOp, "empty object literal is not allowed (position %d)", pos(o))
	}
	for _, p := range o.Value {
		pk, ok := p.(*ast.PropertyKeyed)
		if !ok || pk.Computed {
			return typ{}, fault.Invalidf(checkOp, "object properties must be `key: value` with a literal key (position %d)", pos(p))
		}
		switch pk.Key.(type) {
		case *ast.StringLiteral, *ast.Identifier:
		default:
			return typ{}, fault.Invalidf(checkOp, "object keys must be a name or string literal (position %d)", pos(pk.Value))
		}
		if _, err := check(pk.Value, ctx); err != nil {
			return typ{}, err
		}
	}
	return typ{k: kindObject}, nil
}

func checkCall(c *ast.CallExpression, ctx checkCtx) (typ, error) {
	// Whitelisted free helper: sum(numericArray).
	if id, ok := c.Callee.(*ast.Identifier); ok {
		if id.Name.String() != "sum" {
			return typ{}, fault.Invalidf(checkOp, "function %q is not allowed (position %d)", id.Name.String(), pos(c))
		}
		if len(c.ArgumentList) != 1 {
			return typ{}, fault.Invalidf(checkOp, "sum(...) takes one array argument (position %d)", pos(c))
		}
		at, err := check(c.ArgumentList[0], ctx)
		if err != nil {
			return typ{}, err
		}
		if at.k != kindArray || !isNumeric(at.items) {
			return typ{}, fault.Invalidf(checkOp, "sum(...) needs a numeric array (position %d)", pos(c))
		}
		return typ{k: kindNumber}, nil
	}
	dot, ok := c.Callee.(*ast.DotExpression)
	if !ok {
		return typ{}, fault.Invalidf(checkOp, "only whitelisted method calls are allowed (position %d)", pos(c))
	}
	method := dot.Identifier.Name.String()
	recv, err := check(dot.Left, ctx)
	if err != nil {
		return typ{}, err
	}
	args := c.ArgumentList
	argKinds := make([]kind, len(args))
	for i, a := range args {
		t, err := check(a, ctx)
		if err != nil {
			return typ{}, err
		}
		argKinds[i] = t.k
	}
	fail := func() (typ, error) {
		return typ{}, fault.Invalidf(checkOp, "method %q not allowed on %s with these arguments (position %d)", method, kindName(recv.k), pos(c))
	}
	switch method {
	case "includes":
		if recv.k == kindArray && isScalar(recv.items) && len(argKinds) == 1 && scalarMatch(recv.items, argKinds[0]) {
			return typ{k: kindBoolean}, nil
		}
		if recv.k == kindString && len(argKinds) == 1 && argKinds[0] == kindString {
			return typ{k: kindBoolean}, nil
		}
		return fail()
	case "startsWith", "endsWith":
		if recv.k == kindString && len(argKinds) == 1 && argKinds[0] == kindString {
			return typ{k: kindBoolean}, nil
		}
		return fail()
	case "slice":
		if recv.k == kindString && len(argKinds) == 2 && isNumeric(argKinds[0]) && isNumeric(argKinds[1]) {
			return typ{k: kindString}, nil
		}
		return fail()
	case "toUpperCase", "toLowerCase", "trim":
		if recv.k == kindString && len(argKinds) == 0 {
			return typ{k: kindString}, nil
		}
		return fail()
	case "replaceAll":
		if recv.k == kindString && len(argKinds) == 2 && argKinds[0] == kindString && argKinds[1] == kindString {
			return typ{k: kindString}, nil
		}
		return fail()
	default:
		return typ{}, fault.Invalidf(checkOp, "method %q is not allowed (position %d)", method, pos(c))
	}
}

func requireNumeric(l, r ast.Expression, ctx checkCtx, what string) error {
	lt, err := check(l, ctx)
	if err != nil {
		return err
	}
	rt, err := check(r, ctx)
	if err != nil {
		return err
	}
	if !isNumeric(lt.k) || !isNumeric(rt.k) {
		return fault.Invalidf(checkOp, "%s needs numbers, got %s and %s (position %d)", what, kindName(lt.k), kindName(rt.k), pos(l))
	}
	return nil
}

// checkReference resolves a member/index chain against the resolver and applies the
// defaults rule.
func checkReference(node ast.Expression, ctx checkCtx) (typ, error) {
	return resolveRefExpr(node, ctx, false)
}

// resolveRefExpr flattens node to a reference, resolves it, and (unless exempt or
// existence-probed) enforces the defaults rule. It records the matched root and any
// default binding on the Expr.
func resolveRefExpr(node ast.Expression, ctx checkCtx, existenceProbe bool) (typ, error) {
	segs, ok := flattenRef(node)
	if !ok {
		return typ{}, fault.Invalidf(checkOp, "unsupported reference expression (position %d)", pos(node))
	}
	rootLen := matchRoot(segs, ctx.roots)
	if rootLen == 0 {
		return typ{}, fault.Invalidf(checkOp, "reference %q is not rooted in this context (position %d)", segString(segs), pos(node))
	}
	root := strings.Join(identsOf(segs[:rootLen]), ".")
	var path []string
	idx := rootLen
	for ; idx < len(segs) && !segs[idx].isIndex; idx++ {
		path = append(path, segs[idx].ident)
	}
	unknown := func() (typ, error) {
		return typ{}, fault.Invalidf(checkOp, "unknown field %q under %q (position %d)", strings.Join(path, "."), root, pos(node))
	}
	field, err := ctx.r.Resolve(root, path)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return unknown()
		}
		return typ{}, fault.Wrapf(err, fault.Invalid, checkOp, "resolving %q", root)
	}
	if field.absent() {
		if !existenceProbe || idx < len(segs) {
			return unknown()
		}
		addRoot(ctx.e, root)
		return typ{absent: true}, nil
	}
	k, err := fieldKind(field.Type, pos(node))
	if err != nil {
		return typ{}, err
	}
	items := kindUnknown
	if k == kindArray {
		items, _ = fieldKind(field.Items, pos(node))
	}
	for ; idx < len(segs); idx++ {
		if k != kindArray {
			return typ{}, fault.Invalidf(checkOp, "index applied to a non-array %s (position %d)", kindName(k), pos(node))
		}
		k, items = items, kindUnknown
	}
	// Record the root (dedup) and any default binding.
	addRoot(ctx.e, root)
	if field.HasDefault {
		ctx.e.defs = append(ctx.e.defs, defaultBinding{root: root, path: path, val: field.Default})
	}
	// Defaults rule (skipped for an existence probe or a guarded reference).
	if !existenceProbe && !field.Required && !field.HasDefault && !ctx.exempt(identsOf(segs)) {
		return typ{}, fault.Invalidf(checkOp, "optional field %q must declare a default or be guarded with `!== undefined` (position %d)", segString(segs), pos(node))
	}
	return typ{k: k, items: items}, nil
}

// --- reference flattening ---

type refSeg struct {
	ident   string
	index   int
	isIndex bool
}

// flattenRef turns a member/index chain (Identifier / DotExpression / numeric
// BracketExpression) into ordered segments; ok is false for anything else.
func flattenRef(node ast.Expression) ([]refSeg, bool) {
	switch n := node.(type) {
	case *ast.Identifier:
		return []refSeg{{ident: n.Name.String()}}, true
	case *ast.DotExpression:
		base, ok := flattenRef(n.Left)
		if !ok {
			return nil, false
		}
		return append(base, refSeg{ident: n.Identifier.Name.String()}), true
	case *ast.BracketExpression:
		base, ok := flattenRef(n.Left)
		if !ok {
			return nil, false
		}
		lit, ok := n.Member.(*ast.NumberLiteral)
		if !ok {
			return nil, false
		}
		v, ok := numVal(lit)
		if !ok || v < 0 || v != float64(int(v)) {
			return nil, false
		}
		return append(base, refSeg{index: int(v), isIndex: true}), true
	default:
		return nil, false
	}
}

func matchRoot(segs []refSeg, roots []string) int {
	maxLen := 0
	for maxLen < len(segs) && !segs[maxLen].isIndex {
		maxLen++
	}
	for l := maxLen; l >= 1; l-- {
		candidate := strings.Join(identsOf(segs[:l]), ".")
		for _, r := range roots {
			if r == candidate {
				return l
			}
		}
	}
	return 0
}

// undefinedOperand returns the non-undefined operand of a strict (in)equality when
// the other side is the `undefined` identifier.
func undefinedOperand(b *ast.BinaryExpression) (ast.Expression, bool) {
	if isUndefined(b.Right) {
		return b.Left, true
	}
	if isUndefined(b.Left) {
		return b.Right, true
	}
	return nil, false
}

func isUndefined(node ast.Expression) bool {
	id, ok := node.(*ast.Identifier)
	return ok && id.Name.String() == "undefined"
}

// guardPath returns the reference path of an `X !== undefined` guard expression.
func guardPath(node ast.Expression) ([]string, bool) {
	b, ok := node.(*ast.BinaryExpression)
	if !ok || b.Operator != token.STRICT_NOT_EQUAL {
		return nil, false
	}
	ref, ok := undefinedOperand(b)
	if !ok {
		return nil, false
	}
	segs, ok := flattenRef(ref)
	if !ok {
		return nil, false
	}
	return identsOf(segs), true
}

// --- small helpers ---

func fieldKind(t string, p int) (kind, error) {
	switch t {
	case "string":
		return kindString, nil
	case "number":
		return kindNumber, nil
	case "integer":
		return kindInteger, nil
	case "boolean":
		return kindBoolean, nil
	case "array":
		return kindArray, nil
	case "object":
		return kindObject, nil
	default:
		return kindUnknown, fault.Invalidf(checkOp, "unsupported schema type %q (position %d)", t, p)
	}
}

func isNumeric(k kind) bool { return k == kindNumber || k == kindInteger }
func isScalar(k kind) bool {
	return k == kindString || k == kindNumber || k == kindInteger || k == kindBoolean
}

func scalarMatch(a, b kind) bool {
	if isNumeric(a) {
		return isNumeric(b)
	}
	return a == b
}

func kindName(k kind) string {
	switch k {
	case kindString:
		return "string"
	case kindNumber:
		return "number"
	case kindInteger:
		return "integer"
	case kindBoolean:
		return "boolean"
	case kindArray:
		return "array"
	case kindObject:
		return "object"
	default:
		return "unknown"
	}
}

func isPrefix(prefix, path []string) bool {
	if len(prefix) > len(path) {
		return false
	}
	for i := range prefix {
		if prefix[i] != path[i] {
			return false
		}
	}
	return true
}

func identsOf(segs []refSeg) []string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if !s.isIndex {
			out = append(out, s.ident)
		}
	}
	return out
}

func segString(segs []refSeg) string {
	var b strings.Builder
	for i, s := range segs {
		if s.isIndex {
			b.WriteString("[…]")
			continue
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s.ident)
	}
	return b.String()
}

func addRoot(e *Expr, root string) {
	for _, r := range e.roots {
		if r == root {
			return
		}
	}
	e.roots = append(e.roots, root)
}

func numVal(n *ast.NumberLiteral) (float64, bool) {
	switch v := n.Value.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case int:
		return float64(v), true
	default:
		return 0, false
	}
}

func pos(node ast.Node) int { return int(node.Idx0()) }
