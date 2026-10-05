package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/expr"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
)

// checkWhenConditions type-checks every step's when.condition at RECONCILE against its parents' cached
// OUTPUT schemas + the derived workflow input (ADR-0098), reusing the ADR-0095 goja Condition-mode
// checker — a misspelled or mistyped predicate field is a SchemaMismatch (WhenTypeError) before any run.
func checkWhenConditions(spec v1.WorkflowSpec, rs *runState, contracts map[v1.ObjectName]v1.WorkflowContract, inputSchema json.RawMessage) error {
	for i := range spec.Steps {
		st := &spec.Steps[i]
		if st.When == nil || st.When.Condition == "" {
			continue
		}
		res := whenSchemaResolver(rs.steps[st.Name], contracts, inputSchema)
		ex, perr := expr.Parse(st.When.Condition, expr.Condition)
		if perr != nil {
			return &mismatchError{reason: "WhenTypeError", msg: fmt.Sprintf("step %q when: %v", st.Name, perr)}
		}
		if cerr := ex.Check(res); cerr != nil {
			return &mismatchError{reason: "WhenTypeError", msg: fmt.Sprintf("step %q when: %v", st.Name, cerr)}
		}
	}
	return nil
}

// whenSchemaResolver builds the schema-backed Resolver for one step's when: the roots are `input` (the
// derived workflow input schema) and `step.<parent>.output` for each typed direct parent.
func whenSchemaResolver(n *stepNode, contracts map[v1.ObjectName]v1.WorkflowContract, inputSchema json.RawMessage) schemaResolver {
	schemas := map[string]json.RawMessage{}
	if len(inputSchema) > 0 {
		schemas["input"] = inputSchema
	}
	for _, p := range n.dependsOn {
		if c, ok := contracts[p]; ok && len(c.Output) > 0 {
			schemas["step."+string(p)+".output"] = c.Output
		}
	}
	return schemaResolver{schemas: schemas, decoded: map[string]*schemaNode{}}
}

// schemaResolver is an expr.Resolver answering path types from cached JSON-Schema documents (the
// reconcile-time twin of the runtime docResolver): it descends `properties`, strict where the schema is
// precise (a declared-properties object with a missing key ⇒ NotFound, catching a misspelling) and
// permissive where the schema is silent about nesting (V1 primitive-only — structural typing is deferred).
// It decodes each schema once, on its first reference, so a check costs one decoding of each schema
// however many references it resolves.
type schemaResolver struct {
	schemas map[string]json.RawMessage
	decoded map[string]*schemaNode
}

// schemaNode is the part of a JSON Schema the resolvers read. A schema that is not an object, or a
// keyword of the wrong type, decodes to its zero value, which reads as a schema silent about the keyword.
type schemaNode struct {
	Properties map[string]*schemaNode `json:"properties"`
	Required   []string               `json:"required"`
	Type       string                 `json:"type"`
	Items      struct {
		Type string `json:"type"`
	} `json:"items"`
	Default json.RawMessage `json:"default"`
}

func (s schemaResolver) Roots() []string {
	out := make([]string, 0, len(s.schemas))
	for k := range s.schemas {
		out = append(out, k)
	}
	return out
}

func (s schemaResolver) Resolve(root string, path []string) (expr.Field, error) {
	raw, ok := s.schemas[root]
	if !ok {
		return expr.Field{}, fault.NotFoundf("workflow.when", "root %q not in scope", root)
	}
	cur, ok := s.decoded[root]
	if !ok {
		cur = &schemaNode{}
		_ = json.Unmarshal(raw, cur)
		s.decoded[root] = cur
	}
	required := true // ADR-0095: a path is required only if every segment is in its parent's `required`
	for _, seg := range path {
		if cur == nil || cur.Properties == nil {
			// The schema is silent about nesting — V1 can't type deeper; accept permissively.
			return expr.Field{Type: "string", Required: required}, nil
		}
		next, found := cur.Properties[seg]
		if !found {
			return expr.Field{}, fault.NotFoundf("workflow.when", "field %q not in the schema", seg)
		}
		required = required && slices.Contains(cur.Required, seg)
		cur = next
	}
	if cur == nil {
		return expr.Field{Required: required}, nil
	}
	return expr.Field{Type: cur.Type, Items: cur.Items.Type, Required: required, HasDefault: cur.Default != nil, Default: cur.Default}, nil
}

// evalWhen evaluates a step's when.condition (ADR-0095 native-JS boolean) against
// the run input and the step's direct-parent outputs. Roots are `step.<parent>.output`
// and `input`. This is the RUNTIME path: the resolver infers field types from the
// actual documents (the static reconcile-time check against cached contracts is F65's
// job) and binds an absent field to the default its run-pinned schema declares.
// A parse/type/eval error is surfaced as a step failure by the caller.
func (e *Engine) evalWhen(condition string, n *stepNode, rec *runstate.Record, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (bool, error) {
	res := runtimeResolver(n, rec, input, outputs)
	ex, err := expr.Parse(condition, expr.Condition)
	if err != nil {
		return false, fault.Wrapf(err, fault.Invalid, engineOp, "when condition for step %q", n.name)
	}
	if err := ex.Check(res); err != nil {
		return false, fault.Wrapf(err, fault.Invalid, engineOp, "when condition for step %q", n.name)
	}
	ok, err := ex.EvalBool(res.docs)
	if err != nil {
		return false, fault.Wrapf(err, fault.Invalid, engineOp, "evaluating when for step %q", n.name)
	}
	return ok, nil
}

// runBuiltin runs an engine-native step in-process (ADR-0096). A wait blocks the driver for its
// duration (on ctx, so the run-timeout interrupts it) then Succeeds with its flowing input passed
// through verbatim. A pass evaluates its Select expression to the step's output. Neither dispatches.
// It is a normal step — Running then Succeeded; there is no special Waiting state.
func (e *Engine) runBuiltin(ctx context.Context, rec *runstate.Record, st *v1.WorkflowStep, n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) {
	if st.Builtin.Wait != "" {
		d, werr := e.evalWait(n, rec, st.Builtin.Wait, input, outputs)
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
	return e.evalPass(n, rec, st.Builtin.Pass, input, outputs)
}

// evalWait resolves a builtin wait step's duration (ADR-0096): a Go duration string ("30s") or a
// ${{ }} goja Select expression evaluating to a number of seconds (float; sub-second allowed).
func (e *Engine) evalWait(n *stepNode, rec *runstate.Record, raw string, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (time.Duration, error) {
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
	v, err := e.evalSelect(raw, n, rec, input, outputs)
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
func (e *Engine) evalPass(n *stepNode, rec *runstate.Record, raw string, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) {
	return e.evalSelect(raw, n, rec, input, outputs)
}

// evalSelect parses+checks+evaluates a ${{ }} Select expression against the run input + direct-parent
// outputs (the same doc model as when.condition). Shared by dynamic wait and pass.
func (e *Engine) evalSelect(src string, n *stepNode, rec *runstate.Record, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) {
	res := runtimeResolver(n, rec, input, outputs)
	ex, err := expr.Parse(src, expr.Select)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, engineOp, "builtin expression for step %q", n.name)
	}
	if err := ex.Check(res); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, engineOp, "builtin expression for step %q", n.name)
	}
	out, err := ex.Eval(res.docs)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, engineOp, "evaluating builtin for step %q", n.name)
	}
	return out, nil
}

// runtimeResolver builds the runtime doc model of a step's when, wait or pass: the run input and the
// step's direct-parent outputs, with each root's run-pinned schema so an absent field binds its default.
func runtimeResolver(n *stepNode, rec *runstate.Record, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) docResolver {
	docs := map[string]json.RawMessage{"input": input}
	for _, p := range n.dependsOn {
		if out, ok := outputs[p]; ok {
			docs["step."+string(p)+".output"] = out
		}
	}
	var inputSchema json.RawMessage
	if rec.Contract != nil {
		inputSchema = rec.Contract.Input
	}
	return docResolver{docs: docs, decoded: map[string]interface{}{}, schemas: whenSchemaResolver(n, rec.StepContracts, inputSchema)}
}

// docResolver is an expr.Resolver that infers field types from actual JSON documents
// (the runtime resolver): each key is an exposed root, and a path's type comes from
// the value found there. Present ⇒ Required (no default); missing with a default in schemas ⇒ that
// defaulted Field, which Eval binds (ADR-0095); else a missing last segment ⇒ the absent expr.Field,
// which only an ADR-0095 `!== undefined` guard may probe; a missing parent ⇒ NotFound. It decodes each
// document once, on its first reference, so a check costs one decoding of each document however many
// references it resolves.
type docResolver struct {
	docs    map[string]json.RawMessage
	decoded map[string]interface{}
	schemas schemaResolver // per root, the run-pinned schema; none ⇒ no defaults
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
	cur, ok := r.decoded[root]
	if !ok {
		cur = decodeDoc(raw)
		r.decoded[root] = cur
	}
	for i, seg := range path {
		obj, isObj := fieldsOf(cur)
		if !isObj {
			return expr.Field{}, fault.NotFoundf("workflow.resolve", "%q is not an object", seg)
		}
		next, ok := obj[seg]
		if !ok {
			if f, err := r.schemas.Resolve(root, path); err == nil && f.HasDefault {
				return f, nil
			}
			if i == len(path)-1 {
				return expr.Field{}, nil
			}
			return expr.Field{}, fault.NotFoundf("workflow.resolve", "field %q not found", seg)
		}
		cur = next
	}
	return inferField(cur)
}

// unreadable stands, in a decoded document, for a value that holds a number beyond the range of a
// float64, which an evaluation cannot bind: the value has no type, though the fields of an object
// holding one (fields) can still be read.
type unreadable struct {
	fields map[string]interface{}
}

// decodeDoc decodes a document for docResolver: numbers become float64 values and a value that holds
// an out-of-range number becomes unreadable. An invalid document is unreadable as a whole.
func decodeDoc(raw json.RawMessage) interface{} {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v interface{}
	if !json.Valid(raw) || d.Decode(&v) != nil {
		return unreadable{}
	}
	v, _ = readable(v)
	return v
}

// readable converts the numbers within v to float64 values, in place, and reports whether all of them
// are in range; a value holding one that is not is returned as unreadable.
func readable(v interface{}) (interface{}, bool) {
	all := true
	switch t := v.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil {
			return unreadable{}, false
		}
		return f, true
	case []interface{}:
		for i := range t {
			var ok bool
			t[i], ok = readable(t[i])
			all = all && ok
		}
		if !all {
			return unreadable{}, false
		}
	case map[string]interface{}:
		for k, x := range t {
			var ok bool
			t[k], ok = readable(x)
			all = all && ok
		}
		if !all {
			return unreadable{fields: t}, false
		}
	}
	return v, true
}

// fieldsOf returns the fields of a decoded object, and reports whether v is one; null reads as an
// object without fields.
func fieldsOf(v interface{}) (map[string]interface{}, bool) {
	switch t := v.(type) {
	case nil:
		return nil, true
	case map[string]interface{}:
		return t, true
	case unreadable:
		return t.fields, t.fields != nil
	}
	return nil, false
}

// inferField reports the JSON-Schema-style type of a decoded value.
func inferField(v interface{}) (expr.Field, error) {
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
			if el, err := inferField(t[0]); err == nil {
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
