package expr

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/dop251/goja"
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

// Budget is what evaluations spend together: the length of the strings their replaceAll calls build,
// their running time, and how much larger than their documents their results are. Eval and EvalBool
// spend a fresh Budget per call. A consumer that builds one value from several expressions over the
// same documents (a Sensor action input, ADR-0109) evaluates them within one Budget, so splitting an
// expression into many does not multiply what it may cost. A Budget is not safe for concurrent use.
type Budget struct {
	strings float64         // replaceAll code units left
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
// injected) as globals plus the whitelisted helpers, and evaluates.
func (e *Expr) run(docs map[string]json.RawMessage, b *Budget) (goja.Value, error) {
	if b.time <= 0 {
		return nil, fault.Invalidf(evalOp, "evaluating expression: %s", timeLimit)
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
	if err := boundReplaceAll(vm, &b.strings); err != nil {
		return nil, fault.Internalf(evalOp, "binding replaceAll: %v", err)
	}
	// A root like "step.stats.output" is member access in JS, so build one nested
	// global per top-level segment (roots sharing a prefix merge).
	globals := map[string]interface{}{}
	for _, root := range e.roots {
		val, err := docValue(docs[root], e.defaultsFor(root))
		if err != nil {
			return nil, err
		}
		setNested(globals, splitRoot(root), val)
	}
	for name, val := range globals {
		if err := vm.Set(name, val); err != nil {
			return nil, fault.Internalf(evalOp, "binding root %q: %v", name, err)
		}
	}
	start := time.Now()
	timer := time.AfterFunc(b.time, func() { vm.Interrupt(timeLimit) })
	v, err := vm.RunProgram(e.compiled)
	timer.Stop()
	b.time -= time.Since(start)
	if err != nil {
		return nil, fault.Invalidf(evalOp, "evaluating expression: %s", oneLine(err.Error()))
	}
	return v, nil
}

// maxStringLen bounds, in UTF-16 code units, the total length of the strings that the replaceAll
// calls within one Budget build. Chained or concatenated calls multiply a string's length, so without a
// bound a short expression exhausts the daemon's memory; the total also bounds the calls' CPU time.
const maxStringLen = 1 << 20

// resultMargin bounds, in bytes, how much larger than their documents the Select results within one
// Budget may be: an array or object literal can repeat a document reference any number of times at
// almost no evaluation cost.
const resultMargin = 1 << 20

// evalTimeout bounds the running time within one Budget: the subset has no loops, but an operator such
// as string '+' copies its operands, so cost still grows with the data an expression repeats
// (ADR-0095's Runtime.Interrupt backstop).
const evalTimeout = 100 * time.Millisecond

const timeLimit = "evaluation exceeded its time limit"

// boundReplaceAll wraps String.prototype.replaceAll so that a call checks its result length against
// what is left of the Budget's string length and throws when it does not fit, before goja allocates
// the string.
func boundReplaceAll(vm *goja.Runtime, left *float64) error {
	proto := vm.Get("String").ToObject(vm).Get("prototype").ToObject(vm)
	replaceAll, ok := goja.AssertFunction(proto.Get("replaceAll"))
	if !ok {
		return fault.Internalf(evalOp, "String.prototype.replaceAll is not a function")
	}
	indexOf, ok := goja.AssertFunction(proto.Get("indexOf"))
	if !ok {
		return fault.Internalf(evalOp, "String.prototype.indexOf is not a function")
	}
	return proto.Set("replaceAll", func(call goja.FunctionCall) goja.Value {
		fits, err := replaceAllFits(vm, indexOf, call.This, call.Argument(0), call.Argument(1), *left)
		if err != nil {
			panic(err)
		}
		if !fits {
			panic(vm.NewTypeError("replaceAll calls would build more than %d characters in total", maxStringLen))
		}
		v, err := replaceAll(call.This, call.Arguments...)
		if err != nil {
			panic(err)
		}
		*left -= float64(jsString(vm, v).Length())
		return v
	})
}

// replaceAllFits reports whether s.replaceAll(pat, repl) builds at most limit code units, for string
// arguments, the only ones Check admits. It follows GetSubstitution with no capture groups: $$ writes
// "$", $& the match, $` the text before it and $' the text after it; any other character is written as is.
func replaceAllFits(vm *goja.Runtime, indexOf goja.Callable, s, pat, repl goja.Value, limit float64) (bool, error) {
	r := jsString(vm, repl)
	l, pl := float64(jsString(vm, s).Length()), float64(jsString(vm, pat).Length())
	var fixed, match, before, after float64
	for i := 0; i < r.Length(); i++ {
		if r.CharAt(i) == '$' && i+1 < r.Length() {
			i++
			switch r.CharAt(i) {
			case '$':
				fixed++
			case '&':
				match++
			case '`':
				before++
			case '\'':
				after++
			default:
				fixed++
				i--
			}
			continue
		}
		fixed++
	}
	if pl == 0 { // a match before every code unit and at the end; the text before and after each sum to l(l+1)/2
		return l+(l+1)*fixed+(before+after)*l*(l+1)/2 <= limit, nil
	}
	// At most l/pl matches, each adding at most inc: when that fits, no scan is needed.
	inc := fixed + (match-1)*pl + (before+after)*(l-pl)
	if l+math.Floor(l/pl)*math.Max(inc, 0) <= limit {
		return true, nil
	}
	n := l
	for from := 0.0; ; {
		v, err := indexOf(s, pat, vm.ToValue(from))
		if err != nil {
			return false, err
		}
		pos := float64(v.ToInteger())
		if pos < 0 {
			return n <= limit, nil
		}
		n += fixed + (match-1)*pl + before*pos + after*(l-pos-pl)
		from = pos + pl
		if n-(l-from) > limit { // each of the at most (l-from)/pl matches left removes at most pl
			return false, nil
		}
	}
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
		panic(vm.NewTypeError("replaceAll needs string arguments"))
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
