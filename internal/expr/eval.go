package expr

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/dop251/goja"
	"github.com/green-0-rabbit/funcd/api/fault"
)

const evalOp = "expr.eval"

// Eval evaluates a Select-mode expression against the documents (keyed by matched
// root) and returns the referenced or computed value. It must be called only after
// a successful Check on a Select-mode expression.
func (e *Expr) Eval(docs map[string]json.RawMessage) (json.RawMessage, error) {
	if !e.checked || e.mode != Select {
		return nil, errUnchecked(evalOp)
	}
	v, err := e.run(docs)
	if err != nil {
		return nil, err
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
	v, err := e.run(docs)
	if err != nil {
		return false, err
	}
	return v.ToBoolean(), nil
}

// run compiles the program once (cached), binds the root documents (with defaults
// injected) as globals plus the whitelisted helpers, and evaluates.
func (e *Expr) run(docs map[string]json.RawMessage) (goja.Value, error) {
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
	v, err := vm.RunProgram(e.compiled)
	if err != nil {
		return nil, fault.Invalidf(evalOp, "evaluating expression: %s", oneLine(err.Error()))
	}
	return v, nil
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
