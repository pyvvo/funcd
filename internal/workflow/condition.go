package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/expr"
)

// evalWhen evaluates a step's when.condition (ADR-0095 native-JS boolean) against
// the run input and the step's direct-parent outputs. Roots are `step.<parent>.output`
// and `input`. This is the RUNTIME path: the resolver infers field types from the
// actual documents (the static reconcile-time check against cached contracts is F65's
// job). A parse/type/eval error is surfaced as a step failure by the caller.
func (e *Engine) evalWhen(condition string, n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (bool, error) {
	docs := map[string]json.RawMessage{"input": input}
	for _, p := range n.dependsOn {
		if out, ok := outputs[p]; ok {
			docs["step."+string(p)+".output"] = out
		}
	}
	ex, err := expr.Parse(condition, expr.Condition)
	if err != nil {
		return false, fault.Wrapf(err, fault.Invalid, engineOp, "when condition for step %q", n.name)
	}
	if err := ex.Check(docResolver{docs}); err != nil {
		return false, fault.Wrapf(err, fault.Invalid, engineOp, "when condition for step %q", n.name)
	}
	ok, err := ex.EvalBool(docs)
	if err != nil {
		return false, fault.Wrapf(err, fault.Invalid, engineOp, "evaluating when for step %q", n.name)
	}
	return ok, nil
}

// runBuiltin runs an engine-native step in-process (ADR-0096). A wait blocks the driver for its
// duration (on ctx, so the run-timeout interrupts it) then Succeeds with its flowing input passed
// through verbatim. A pass evaluates its Select expression to the step's output. Neither dispatches.
// It is a normal step — Running then Succeeded; there is no special Waiting state.
func (e *Engine) runBuiltin(ctx context.Context, st *v1.WorkflowStep, n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) {
	if st.Builtin.Wait != "" {
		d, werr := e.evalWait(n, st.Builtin.Wait, input, outputs)
		if werr != nil {
			return nil, werr
		}
		if d > 0 {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done(): // the run-timeout (or a driver cancel) interrupts the wait
				return nil, ctx.Err()
			case <-t.C:
			}
		}
		return e.flowingInput(n, input, outputs), nil // pass the flowing input through (verbatim)
	}
	return e.evalPass(n, st.Builtin.Pass, input, outputs)
}

// evalWait resolves a builtin wait step's duration (ADR-0096): a Go duration string ("30s") or a
// ${{ }} goja Select expression evaluating to a number of seconds (float; sub-second allowed).
func (e *Engine) evalWait(n *stepNode, raw string, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (time.Duration, error) {
	if !strings.HasPrefix(strings.TrimSpace(raw), "${{") {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fault.Invalidf(engineOp, "wait %q for step %q is not a duration: %v", raw, n.name, err)
		}
		if d < 0 {
			return 0, fault.Invalidf(engineOp, "wait for step %q must be non-negative", n.name)
		}
		return d, nil
	}
	v, err := e.evalSelect(raw, n, input, outputs)
	if err != nil {
		return 0, err
	}
	var secs float64
	if uerr := json.Unmarshal(v, &secs); uerr != nil {
		return 0, fault.Invalidf(engineOp, "wait expression for step %q must evaluate to a number of seconds", n.name)
	}
	if secs < 0 {
		return 0, fault.Invalidf(engineOp, "wait for step %q must be non-negative", n.name)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// evalPass evaluates a builtin pass step's Select expression → its output (ADR-0096; no dispatch).
func (e *Engine) evalPass(n *stepNode, raw string, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) {
	return e.evalSelect(raw, n, input, outputs)
}

// evalSelect parses+checks+evaluates a ${{ }} Select expression against the run input + direct-parent
// outputs (the same doc model as when.condition). Shared by dynamic wait and pass.
func (e *Engine) evalSelect(src string, n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) {
	docs := map[string]json.RawMessage{"input": input}
	for _, p := range n.dependsOn {
		if out, ok := outputs[p]; ok {
			docs["step."+string(p)+".output"] = out
		}
	}
	ex, err := expr.Parse(src, expr.Select)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, engineOp, "builtin expression for step %q", n.name)
	}
	if err := ex.Check(docResolver{docs}); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, engineOp, "builtin expression for step %q", n.name)
	}
	out, err := ex.Eval(docs)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, engineOp, "evaluating builtin for step %q", n.name)
	}
	return out, nil
}

// docResolver is an expr.Resolver that infers field types from actual JSON documents
// (the runtime resolver): each key is an exposed root, and a path's type comes from
// the value found there. Present ⇒ Required (no default); absent ⇒ NotFound.
type docResolver struct {
	docs map[string]json.RawMessage
}

func (r docResolver) Roots() []string {
	out := make([]string, 0, len(r.docs))
	for k := range r.docs {
		out = append(out, k)
	}
	return out
}

func (r docResolver) Resolve(root string, path []string) (expr.Field, error) {
	raw, ok := r.docs[root]
	if !ok {
		return expr.Field{}, fault.NotFoundf("workflow.resolve", "root %q not in scope", root)
	}
	cur := raw
	for _, seg := range path {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return expr.Field{}, fault.NotFoundf("workflow.resolve", "%q is not an object", seg)
		}
		next, ok := obj[seg]
		if !ok {
			return expr.Field{}, fault.NotFoundf("workflow.resolve", "field %q not found", seg)
		}
		cur = next
	}
	return inferField(cur)
}

// inferField reports the JSON-Schema-style type of a value.
func inferField(raw json.RawMessage) (expr.Field, error) {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return expr.Field{}, fault.NotFoundf("workflow.resolve", "unreadable value")
	}
	f := expr.Field{Required: true}
	switch t := v.(type) {
	case string:
		f.Type = "string"
	case bool:
		f.Type = "boolean"
	case float64:
		f.Type = "number"
	case []interface{}:
		f.Type = "array"
		if len(t) > 0 {
			b, _ := json.Marshal(t[0])
			if el, err := inferField(b); err == nil {
				f.Items = el.Type
			}
		}
	case map[string]interface{}:
		f.Type = "object"
	default:
		return expr.Field{}, fault.NotFoundf("workflow.resolve", "null or unsupported value")
	}
	return f, nil
}
