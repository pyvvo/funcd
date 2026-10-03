package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Typed-edge contract checking (ADR-0098, F65). A pure, registry-free layer over the I/O schemas
// resolved from OCI metadata: required-primitive-property subsumption across edges + void rules. The
// input↔doc primitive (v1.CheckInput/v1.FieldDiff/v1.ParseSchemaView) lives on the type so admission can
// reuse it; the edge↔edge + derive logic below is workflow-internal (it needs the run graph).

// edgeCompatible reports whether a producer's primitive type satisfies a required consumer type
// (schema↔schema): equal, or the safe widening integer→number.
func edgeCompatible(got, want string) bool {
	return got == want || (got == "integer" && want == "number")
}

// checkEdge reports the required properties of the consumer schema not satisfied by the producer schema,
// honoring void rules and the params-provided set. Empty ⇒ the edge type-checks. O(required).
func checkEdge(producer, consumer json.RawMessage, providedByParams map[string]bool) []v1.FieldDiff {
	c := v1.ParseSchemaView(consumer)
	if c.IsVoid() {
		return nil // a void consumer requires nothing
	}
	p := v1.ParseSchemaView(producer)
	var diffs []v1.FieldDiff
	for _, field := range c.Required {
		if providedByParams[field] {
			continue // spec.params supplies it — not required from the producer
		}
		got, ok := p.Props[field]
		if !ok {
			diffs = append(diffs, v1.FieldDiff{Field: field, Want: c.Props[field]})
			continue
		}
		if !edgeCompatible(got, c.Props[field]) {
			diffs = append(diffs, v1.FieldDiff{Field: field, Want: c.Props[field], Got: got})
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Field < diffs[j].Field })
	return diffs
}

// compositeSchema builds the fan-in producer schema {properties: {<parent>: <object>}} keyed by parent
// name (the ADR-0094 fan-in input model), each parent value typed "object".
func compositeSchema(parentOutputs map[v1.ObjectName]json.RawMessage) json.RawMessage {
	props := map[string]map[string]string{}
	req := make([]string, 0, len(parentOutputs))
	for name := range parentOutputs {
		props[string(name)] = map[string]string{"type": "object"}
		req = append(req, string(name))
	}
	sort.Strings(req)
	b, _ := json.Marshal(map[string]interface{}{"type": "object", "properties": props, "required": req})
	return b
}

// deriveWorkflowContract combines the root steps' input schemas, keeping each property's default so a
// when: on input binds it (ADR-0095); a conflicting primitive or default on a shared field ⇒ error,
// RootSchemaConflict. It composes the leaf outputs: a single leaf ⇒ its output verbatim, multiple
// leaves ⇒ the composite keyed by step name (symmetric with the run-output model).
// The onFailure handler is outside the DAG, so it is never a root (ADR-0094).
func deriveWorkflowContract(rs *runState, contracts map[v1.ObjectName]v1.WorkflowContract) (v1.WorkflowContract, error) {
	mergedProps := map[string]string{}
	mergedDefaults := map[string]json.RawMessage{}
	requiredSet := map[string]bool{}
	dialect := ""
	for _, name := range rs.dagSteps() {
		n := rs.steps[name]
		if len(n.dependsOn) != 0 {
			continue // not a root
		}
		c, ok := contracts[name]
		if !ok {
			continue // untyped (e.g. a builtin) root contributes nothing
		}
		if dialect == "" {
			dialect = c.Dialect
		}
		in := v1.ParseSchemaView(c.Input)
		for field, typ := range in.Props {
			if prev, seen := mergedProps[field]; seen && prev != typ {
				return v1.WorkflowContract{}, &schemaConflict{field: field, what: "types", a: prev, b: typ}
			}
			mergedProps[field] = typ
		}
		for field, def := range in.Defaults {
			if prev, seen := mergedDefaults[field]; seen && !sameJSONValue(prev, def) {
				return v1.WorkflowContract{}, &schemaConflict{field: field, what: "defaults", a: string(prev), b: string(def)}
			}
			mergedDefaults[field] = def
		}
		for _, r := range in.Required {
			requiredSet[r] = true
		}
	}
	input := marshalObjectSchema(mergedProps, mergedDefaults, requiredSet)

	leaves := rs.leaves()
	var output json.RawMessage
	switch {
	case len(leaves) == 1:
		output = contracts[leaves[0]].Output
	case len(leaves) > 1:
		props := map[string]map[string]string{}
		for _, l := range leaves {
			props[string(l)] = map[string]string{"type": "object"}
		}
		b, _ := json.Marshal(map[string]interface{}{"type": "object", "properties": props})
		output = b
	}
	return v1.WorkflowContract{Dialect: dialect, Input: input, Output: output}, nil
}

// schemaConflict is a RootSchemaConflict: two roots declare the same field with different primitive
// types or different defaults.
type schemaConflict struct {
	field, what, a, b string
}

func (e *schemaConflict) Error() string {
	return fmt.Sprintf("root steps declare field %q with conflicting %s %s vs %s", e.field, e.what, e.a, e.b)
}

// sameJSONValue reports whether two JSON values are the same text, ignoring insignificant whitespace.
func sameJSONValue(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// propSchema is one property of a marshalled object schema.
type propSchema struct {
	Type    string          `json:"type"`
	Default json.RawMessage `json:"default,omitempty"`
}

// marshalObjectSchema builds an object JSON Schema from a props map, their defaults and a required set.
func marshalObjectSchema(props map[string]string, defaults map[string]json.RawMessage, required map[string]bool) json.RawMessage {
	p := map[string]propSchema{}
	for name, typ := range props {
		p[name] = propSchema{Type: typ, Default: defaults[name]}
	}
	req := make([]string, 0, len(required))
	for r := range required {
		req = append(req, r)
	}
	sort.Strings(req)
	b, _ := json.Marshal(map[string]interface{}{"type": "object", "properties": p, "required": req})
	return b
}
