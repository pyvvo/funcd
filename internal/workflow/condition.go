package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
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
// derived workflow input schema) and `step.<parent>.output` for each typed direct parent. A join: any
// step with two or more parents may run with a branch skipped, so its parent roots are optional (ADR-0166).
func whenSchemaResolver(n *stepNode, contracts map[v1.ObjectName]v1.WorkflowContract, inputSchema json.RawMessage) schemaResolver {
	schemas := map[string]json.RawMessage{}
	if len(inputSchema) > 0 {
		schemas["input"] = inputSchema
	}
	var optional map[string]bool
	if effectiveJoin(n.join) == v1.JoinAny && len(n.dependsOn) >= 2 {
		optional = map[string]bool{}
	}
	for _, p := range n.dependsOn {
		if c, ok := contracts[p]; ok && len(c.Output) > 0 {
			root := "step." + string(p) + ".output"
			schemas[root] = c.Output
			if optional != nil {
				optional[root] = true
			}
		}
	}
	return schemaResolver{schemas: schemas, decoded: map[string]*schemaNode{}, optionalRoots: optional}
}

// schemaResolver is an expr.Resolver answering path types from cached JSON-Schema documents (the
// reconcile-time twin of the runtime docResolver): it descends `properties`, strict where the schema is
// precise (a declared-properties object with a missing key ⇒ NotFound, catching a misspelling) and
// permissive where the schema is silent about nesting (V1 primitive-only — structural typing is deferred).
// It decodes each schema once, on its first reference, so a check costs one decoding of each schema
// however many references it resolves.
type schemaResolver struct {
	schemas       map[string]json.RawMessage
	decoded       map[string]*schemaNode
	optionalRoots map[string]bool
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
	if len(path) == 0 && s.optionalRoots[root] {
		// An optional root is never required or defaulted (a default does not stand in for a skipped
		// branch), and its Type is never empty: an empty one reads as absent, and the root's guard would
		// then skip checking what it guards (ADR-0166).
		typ := cur.Type
		if typ == "" {
			typ = "object"
		}
		return expr.Field{Type: typ, Items: cur.Items.Type}, nil
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

// evalWait resolves a builtin wait step's duration (ADR-0096, ADR-0194): a duration string ("30s") or a
// ${{ }} goja Select expression evaluating to one; any other result fails the step naming the grammar.
func (e *Engine) evalWait(n *stepNode, rec *runstate.Record, raw string, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (time.Duration, error) {
	s := raw
	if v1.IsWaitExpression(raw) {
		v, err := e.evalSelect(raw, n, rec, input, outputs)
		if err != nil {
			return 0, err
		}
		// a non-string result is parsed as its JSON text, which the grammar always refuses (it needs a unit)
		if json.Unmarshal(v, &s) != nil || string(v) == "null" {
			s = string(v)
		}
	}
	d, err := v1.ParseDuration(s)
	if err != nil {
		return 0, fault.Wrapf(err, fault.Invalid, engineOp, "wait for step %q", n.name)
	}
	return time.Duration(d), nil
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
// A parent with no recorded output (a skipped branch, or a void parent after a Resume) is an absent root.
func runtimeResolver(n *stepNode, rec *runstate.Record, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) docResolver {
	docs := map[string]json.RawMessage{"input": input}
	var absent []string
	for _, p := range n.dependsOn {
		root := "step." + string(p) + ".output"
		if out, ok := outputs[p]; ok {
			docs[root] = out
		} else {
			absent = append(absent, root)
		}
	}
	var inputSchema json.RawMessage
	if rec.Contract != nil {
		inputSchema = rec.Contract.Input
	}
	return docResolver{docs: docs, decoded: map[string]interface{}{}, schemas: whenSchemaResolver(n, rec.StepContracts, inputSchema), absentRoots: absent}
}

// docResolver is an expr.Resolver that infers field types from actual JSON documents
// (the runtime resolver): each key is an exposed root, and a path's type comes from
// the value found there. Present ⇒ Required (no default); missing with a default in schemas ⇒ that
// defaulted Field, which Eval binds (ADR-0095); else a missing last segment ⇒ the absent expr.Field,
// which only an ADR-0095 `!== undefined` guard may probe; a missing parent ⇒ NotFound. An absent root
// resolves, by itself, to the zero Field and every path under it is NotFound (ADR-0166). It decodes each
// document once, on its first reference, so a check costs one decoding of each document however many
// references it resolves.
type docResolver struct {
	docs        map[string]json.RawMessage
	decoded     map[string]interface{}
	schemas     schemaResolver // per root, the run-pinned schema; none ⇒ no defaults
	absentRoots []string
}

func (r docResolver) Roots() []string {
	out := make([]string, 0, len(r.docs)+len(r.absentRoots))
	for k := range r.docs {
		out = append(out, k)
	}
	return append(out, r.absentRoots...)
}

func (r docResolver) Resolve(root string, path []string) (expr.Field, error) {
	raw, ok := r.docs[root]
	if !ok {
		if len(path) == 0 && slices.Contains(r.absentRoots, root) {
			return expr.Field{}, nil
		}
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
