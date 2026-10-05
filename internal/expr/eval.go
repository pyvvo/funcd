package expr

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/token"
	"github.com/pyvvo/funcd/api/fault"
)

const evalOp = "expr.eval"

// Eval evaluates a Select-mode expression against the documents (keyed by matched
// root) and returns the referenced or computed value. It must be called only after
// a successful Check on a Select-mode expression.
func (e *Expr) Eval(docs map[string]json.RawMessage) (json.RawMessage, error) {
	return e.EvalWithin(e.budget(), docs)
}

// EvalWithin is Eval spending b, which it shares with the other evaluations within b (see Budget).
func (e *Expr) EvalWithin(b *Budget, docs map[string]json.RawMessage) (json.RawMessage, error) {
	if !e.checked || e.mode != Select {
		return nil, errUnchecked(evalOp)
	}
	v, err := e.run(docs, b)
	if err != nil {
		return nil, err
	}
	for _, root := range e.roots {
		if !b.counted[root] {
			b.counted[root] = true
			b.result += int64(len(docs[root]))
		}
	}
	if !resultFits(v, &b.result) {
		return nil, fault.Invalidf(evalOp, "expression results are larger than their documents plus %d bytes", resultMargin)
	}
	exported := v.Export()
	// Guard against JS's non-error division: a non-finite Select result is a fault
	// rather than a silent Infinity/NaN.
	if f, ok := exported.(float64); ok && (math.IsInf(f, 0) || math.IsNaN(f)) {
		return nil, fault.Invalidf(evalOp, "expression produced a non-finite number (e.g. division by zero)")
	}
	out, err := json.Marshal(exported)
	if err != nil {
		return nil, fault.Internalf(evalOp, "marshalling result: %v", err)
	}
	return out, nil
}

// EvalBool evaluates a Condition-mode expression and returns its boolean result. It
// must be called only after a successful Check on a Condition-mode expression.
func (e *Expr) EvalBool(docs map[string]json.RawMessage) (bool, error) {
	if !e.checked || e.mode != Condition {
		return false, errUnchecked(evalOp)
	}
	v, err := e.run(docs, e.budget())
	if err != nil {
		return false, err
	}
	return v.ToBoolean(), nil
}

// Budget is what evaluations spend together: the length of the strings their concatenations, literals and
// replaceAll calls build or hold, their running time, and how much larger than their documents their results
// are. Eval and EvalBool spend a fresh Budget per call. A consumer that builds one value from several
// expressions over the same documents (a Sensor action input, ADR-0109) evaluates them within one
// Budget, so splitting an expression into many does not multiply what it may cost. A Budget is not
// safe for concurrent use.
type Budget struct {
	strings float64         // code units left for concatenations, literals and replaceAll calls
	time    time.Duration   // running time left
	result  int64           // result bytes left
	counted map[string]bool // roots whose documents result already allows for
}

// NewBudget returns the budget of one evaluation.
func NewBudget() *Budget {
	return &Budget{strings: maxStringLen, time: evalTimeout, result: resultMargin, counted: map[string]bool{}}
}

// budget returns a fresh Budget for one evaluation of e.
func (e *Expr) budget() *Budget {
	b := NewBudget()
	if e.timeout > 0 {
		b.time = e.timeout
	}
	return b
}

// run compiles the program once (cached), binds the root documents (with defaults
// injected) as globals plus the whitelisted helpers, and evaluates. The Budget's time runs from the
// binding, which every evaluation does again.
func (e *Expr) run(docs map[string]json.RawMessage, b *Budget) (goja.Value, error) {
	if b.time <= 0 {
		return nil, errTimeLimit()
	}
	if e.compiled == nil {
		prog, err := goja.Compile("expr", e.inner, true)
		if err != nil {
			return nil, fault.Internalf(evalOp, "compiling checked expression: %v", err)
		}
		e.compiled = prog
	}
	vm := goja.New()
	if err := vm.Set("sum", sumHelper); err != nil {
		return nil, fault.Internalf(evalOp, "binding helper: %v", err)
	}
	if err := bindSearches(vm, &b.strings); err != nil {
		return nil, fault.Internalf(evalOp, "binding string methods: %v", err)
	}
	start := time.Now()
	defer func() { b.time -= time.Since(start) }()
	// A root like "step.stats.output" is member access in JS, so build one nested
	// global per top-level segment (roots sharing a prefix merge).
	globals := map[string]interface{}{}
	c := concat{globals: globals}
	for _, root := range e.roots {
		raw, ok := docs[root]
		if !ok {
			setNested(globals, splitRoot(root), goja.Undefined()) // an absent root is undefined, never null (ADR-0166)
			continue
		}
		val, err := docValue(raw, e.defaultsFor(root))
		if err != nil {
			return nil, err
		}
		setNested(globals, splitRoot(root), val)
		c.docs += float64(len(docs[root]))
	}
	for _, d := range e.defs {
		c.docs += float64(len(d.val))
	}
	for name, val := range globals {
		if err := vm.Set(name, val); err != nil {
			return nil, fault.Internalf(evalOp, "binding root %q: %v", name, err)
		}
	}
	c.sub(e.rootExpr())
	if c.built > b.strings {
		return nil, fault.Invalidf(evalOp, "evaluating expression: its concatenations and literals could hold more than %d characters in total", maxStringLen)
	}
	b.strings -= c.built
	left := b.time - time.Since(start)
	if left <= 0 {
		return nil, errTimeLimit()
	}
	timer := time.AfterFunc(left, func() { vm.Interrupt(timeLimit) })
	v, err := vm.RunProgram(e.compiled)
	timer.Stop()
	if err != nil {
		return nil, fault.Invalidf(evalOp, "evaluating expression: %s", oneLine(err.Error()))
	}
	return v, nil
}

func errTimeLimit() error {
	return fault.Invalidf(evalOp, "evaluating expression: %s", timeLimit)
}

// maxStringLen bounds, in UTF-16 code units, the total length of the strings that the concatenations,
// the array and object literals and the replaceAll calls within one Budget build or hold. Concatenated
// or chained calls multiply a string's length, and a literal keeps a copy per element, so without a
// bound a short expression exhausts the daemon's memory.
const maxStringLen = 1 << 20

// resultMargin bounds, in bytes, how much larger than their documents the Select results within one
// Budget may be: an array or object literal can repeat a document reference any number of times at
// almost no evaluation cost.
const resultMargin = 1 << 20

// evalTimeout bounds the running time within one Budget: the subset has no loops, but every operator
// and method copies or searches its operands, so cost still grows with the data an expression repeats
// (ADR-0095's Runtime.Interrupt backstop). Interrupt acts between instructions, so each native call the
// subset admits runs in time linear in its operands and result.
const evalTimeout = 100 * time.Millisecond

const timeLimit = "evaluation exceeded its time limit"

// concat bounds, before an evaluation runs, the strings its '+' operators build and its array and object
// literals hold over the bound documents: a '+' result is never longer than its two operands together.
type concat struct {
	globals map[string]interface{}
	docs    float64 // the JSON size of the bound documents and defaults
	built   float64 // the bound of every concatenation's result
}

// scalarLen bounds String(v) for a number, a boolean, null or undefined: "-1.7976931348623157e+308".
const scalarLen = 24

// sub bounds x where no '+' concatenates it, so a concatenation there is one of its own: its result
// bound is charged.
func (c *concat) sub(x ast.Expression) (float64, bool) {
	n, str := c.len(x)
	if b, ok := x.(*ast.BinaryExpression); ok && b.Operator == token.PLUS && str {
		c.built += n
	}
	return n, str
}

// len bounds the length of String(v) for the value v that x evaluates to, and reports whether v may be
// a string, an array or an object, which '+' concatenates rather than adds. A replaceAll result counts
// as empty, since its calls spend the same budget when they run; a case change at most triples a string.
func (c *concat) len(x ast.Expression) (float64, bool) {
	switch n := x.(type) {
	case *ast.StringLiteral:
		return float64(len(n.Value)), true
	case *ast.BinaryExpression:
		if n.Operator == token.PLUS {
			l, ls := c.len(n.Left)
			r, rs := c.len(n.Right)
			if ls || rs {
				return l + r, true
			}
			break
		}
		c.sub(n.Left)
		c.sub(n.Right)
	case *ast.UnaryExpression:
		c.sub(n.Operand)
	case *ast.ConditionalExpression:
		c.sub(n.Test)
		a, as := c.sub(n.Consequent)
		b, bs := c.sub(n.Alternate)
		return max(a, b), as || bs
	case *ast.ArrayLiteral:
		for _, v := range n.Value {
			c.keep(v)
		}
	case *ast.ObjectLiteral:
		for _, p := range n.Value {
			if pk, ok := p.(*ast.PropertyKeyed); ok {
				c.keep(pk.Value)
			}
		}
	case *ast.CallExpression:
		for _, a := range n.ArgumentList {
			c.sub(a)
		}
		if dot, ok := n.Callee.(*ast.DotExpression); ok {
			recv, _ := c.sub(dot.Left)
			switch dot.Identifier.Name.String() {
			case "slice", "trim":
				return recv, true
			case "toUpperCase", "toLowerCase":
				return 3 * recv, true
			case "replaceAll":
				return 0, true
			}
		}
	case *ast.DotExpression:
		if _, ok := flattenRef(n); !ok { // the length of a computed string
			c.sub(n.Left)
			break
		}
		return c.ref(n)
	case *ast.BracketExpression, *ast.Identifier:
		return c.ref(n)
	}
	return scalarLen, false
}

// keep bounds an element of an array or object literal and charges a string it holds, as a '+' result is
// charged: the literal keeps it until the evaluation ends, and goja copies a non-ASCII string when it
// slices or case-changes it, or when Array.prototype.includes compares a document string it read. An
// array or object that the element reads is the bound document, not a copy.
func (c *concat) keep(x ast.Expression) {
	if t, ok := x.(*ast.ConditionalExpression); ok {
		c.sub(t.Test)
		c.keep(t.Consequent)
		c.keep(t.Alternate)
		return
	}
	n, str := c.len(x)
	if segs, ok := flattenRef(x); ok {
		_, str = lookup(c.globals, segs).(string)
	}
	if str {
		c.built += n
	}
}

// ref bounds String(v) for the bound value v that a reference reads. Array.prototype.toString joins
// the elements, at most 8 times as long as their JSON ("{}" becomes "[object Object]").
func (c *concat) ref(x ast.Expression) (float64, bool) {
	segs, ok := flattenRef(x)
	if !ok {
		return scalarLen, false
	}
	switch v := lookup(c.globals, segs).(type) {
	case string:
		return float64(len(v)), true
	case []interface{}:
		return 8 * c.docs, true
	case map[string]interface{}:
		return float64(len("[object Object]")), true
	}
	return scalarLen, false
}

// lookup returns the bound value that a reference reads, or nil when it reads none.
func lookup(globals map[string]interface{}, segs []refSeg) interface{} {
	var v interface{} = globals
	for _, s := range segs {
		switch x := v.(type) {
		case map[string]interface{}:
			key := s.ident
			if s.isIndex {
				key = strconv.Itoa(s.index)
			}
			v = x[key]
		case []interface{}:
			if !s.isIndex || s.index >= len(x) {
				return nil
			}
			v = x[s.index]
		case string:
			if !s.isIndex || s.index >= len(x) {
				return nil
			}
			v = x[:1] // one code unit
		default:
			return nil
		}
	}
	return v
}

// bindSearches replaces String.prototype.includes and replaceAll, the admitted methods that search a
// string, with searches in linear time: goja's own search on a non-ASCII string compares the whole
// pattern at every position, and the deadline cannot stop a native call. replaceAll also throws, before
// it builds its result, when the result does not fit what is left of the Budget's string length.
func bindSearches(vm *goja.Runtime, left *float64) error {
	proto := vm.Get("String").ToObject(vm).Get("prototype").ToObject(vm)
	if err := proto.Set("includes", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(newSearch(jsString(vm, call.Argument(0))).index(jsString(vm, call.This), 0) >= 0)
	}); err != nil {
		return err
	}
	return proto.Set("replaceAll", func(call goja.FunctionCall) goja.Value {
		return replaceAll(vm, jsString(vm, call.This), jsString(vm, call.Argument(0)), jsString(vm, call.Argument(1)), left)
	})
}

// replaceAll is s.replaceAll(pat, repl) for string arguments, the only ones Check admits. It follows
// GetSubstitution with no capture groups: $$ writes "$", $& the match, $` the text before it and $' the
// text after it; any other character is written as is. It spends the result length from left, and
// throws, before it builds the result, when it does not fit.
func replaceAll(vm *goja.Runtime, s, pat, repl goja.String, left *float64) goja.Value {
	m, pieces := newSearch(pat), substitution(repl, pat.Length() == 0)
	l, pl := s.Length(), pat.Length()
	// each passes the parts of the result to part in order, and stops when part returns false.
	each := func(part func(src goja.String, from, to int) bool) bool {
		last := 0
		for p := m.index(s, 0); p >= 0; p = m.index(s, p+max(pl, 1)) {
			if !part(s, last, p) {
				return false
			}
			for _, pc := range pieces {
				var ok bool
				switch pc.kind {
				case '&':
					ok = part(s, p, p+pl)
				case '`':
					ok = part(s, 0, p)
				case '\'':
					ok = part(s, min(p+pl, l), l)
				default:
					ok = part(repl, pc.from, pc.to)
				}
				if !ok {
					return false
				}
			}
			last = p + pl
		}
		return part(s, last, l)
	}
	var n float64
	if !each(func(_ goja.String, from, to int) bool { n += float64(to - from); return n <= *left }) {
		panic(vm.NewTypeError("replaceAll calls would build more than %d characters in total", maxStringLen))
	}
	var out goja.StringBuilder
	out.Grow(int(n))
	each(func(src goja.String, from, to int) bool { out.WriteSubstring(src, from, to); return true })
	*left -= n
	return out.String()
}

// piece is one part of a replacement: a literal run of it (kind 0, from:to), or $&, $` or $', whose
// kind is the character after the $.
type piece struct {
	kind     uint16
	from, to int
}

// substitution splits a replacement into its pieces. An empty pattern matches empty text, so its $&
// pieces, which would write nothing at every match, are dropped.
func substitution(r goja.String, emptyPattern bool) []piece {
	var out []piece
	lit, rl := 0, r.Length()
	flush := func(to int) {
		if to > lit {
			out = append(out, piece{from: lit, to: to})
		}
	}
	for i := 0; i+1 < rl; i++ {
		if r.CharAt(i) != '$' {
			continue
		}
		switch c := r.CharAt(i + 1); c {
		case '$':
			flush(i + 1)
		case '&', '`', '\'':
			flush(i)
			if c != '&' || !emptyPattern {
				out = append(out, piece{kind: c})
			}
		default:
			continue
		}
		lit = i + 2
		i++
	}
	flush(rl)
	return out
}

// search finds a pattern in strings in time linear in their lengths (Knuth-Morris-Pratt over UTF-16
// code units).
type search struct {
	pat  []uint16
	fail []int // fail[i] is the length of the longest proper prefix of pat[:i+1] that is also its suffix
}

func newSearch(p goja.String) search {
	m := search{pat: make([]uint16, p.Length()), fail: make([]int, p.Length())}
	for i := range m.pat {
		m.pat[i] = p.CharAt(i)
	}
	for i, k := 1, 0; i < len(m.pat); i++ {
		for k > 0 && m.pat[i] != m.pat[k] {
			k = m.fail[k-1]
		}
		if m.pat[i] == m.pat[k] {
			k++
		}
		m.fail[i] = k
	}
	return m
}

// index returns the first position at or after from where the pattern occurs in s, or -1.
func (m search) index(s goja.String, from int) int {
	l := s.Length()
	if len(m.pat) == 0 {
		if from <= l {
			return from
		}
		return -1
	}
	for i, k := from, 0; i < l; i++ {
		c := s.CharAt(i)
		for k > 0 && c != m.pat[k] {
			k = m.fail[k-1]
		}
		if c == m.pat[k] {
			k++
		}
		if k == len(m.pat) {
			return i - k + 1
		}
	}
	return -1
}

// resultFits spends the JSON size of a Select result from left and reports whether it fits, before
// Export and json.Marshal copy it. It counts a string's UTF-16 code units, a lower bound of its encoded
// bytes, and stops once left is spent.
func resultFits(v goja.Value, left *int64) bool {
	var walk func(goja.Value) bool
	walk = func(v goja.Value) bool {
		switch x := v.(type) {
		case goja.String:
			*left -= int64(x.Length()) + 2
		case *goja.Object:
			keys := x.Keys()
			*left -= int64(max(len(keys), 1)) + 1 // brackets and commas
			arr := x.ClassName() == "Array"
			for _, k := range keys {
				if !arr {
					*left -= int64(len(k)) + 3
				}
				if *left < 0 || !walk(x.Get(k)) {
					return false
				}
			}
		default:
			*left--
		}
		return *left >= 0
	}
	return walk(v)
}

func jsString(vm *goja.Runtime, v goja.Value) goja.String {
	s, ok := v.ToString().(goja.String)
	if !ok {
		panic(vm.NewTypeError("string methods need string arguments"))
	}
	return s
}

// sumHelper totals a numeric JS array; goja converts the argument to []float64.
func sumHelper(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s
}

func (e *Expr) defaultsFor(root string) []defaultBinding {
	var out []defaultBinding
	for _, d := range e.defs {
		if d.root == root {
			out = append(out, d)
		}
	}
	return out
}

// docValue unmarshals a root document and injects any declared defaults for absent
// fields, returning a value goja can bind as a global.
func docValue(raw json.RawMessage, defaults []defaultBinding) (interface{}, error) {
	var v interface{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fault.Invalidf(evalOp, "invalid document JSON: %v", err)
		}
	}
	for _, d := range defaults {
		var dv interface{}
		if err := json.Unmarshal(d.val, &dv); err != nil {
			return nil, fault.Internalf(evalOp, "invalid default JSON: %v", err)
		}
		v = injectDefault(v, d.path, dv)
	}
	return v, nil
}

// splitRoot splits a dotted root ("step.stats.output") into its segments.
func splitRoot(root string) []string {
	return strings.Split(root, ".")
}

// setNested places val under the segment path in the shared globals tree, creating
// intermediate objects as needed.
func setNested(globals map[string]interface{}, segs []string, val interface{}) {
	cur := globals
	for i, s := range segs {
		if i == len(segs)-1 {
			cur[s] = val
			return
		}
		next, ok := cur[s].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			cur[s] = next
		}
		cur = next
	}
}

// injectDefault sets val at path in the object tree only if that leaf is absent,
// creating intermediate objects as needed. It returns the (possibly new) root.
func injectDefault(root interface{}, path []string, val interface{}) interface{} {
	if len(path) == 0 {
		return root
	}
	m, ok := root.(map[string]interface{})
	if !ok {
		m = map[string]interface{}{}
	}
	if len(path) == 1 {
		if _, present := m[path[0]]; !present {
			m[path[0]] = val
		}
		return m
	}
	m[path[0]] = injectDefault(m[path[0]], path[1:], val)
	return m
}
