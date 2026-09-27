package workflow

import (
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

// deriveWorkflowContract combines the root steps' input schemas (a conflicting primitive on a shared
// required field ⇒ error, RootSchemaConflict) and composes the leaf outputs: a single leaf ⇒ its output
// verbatim, multiple leaves ⇒ the composite keyed by step name (symmetric with the run-output model).
func deriveWorkflowContract(rs *runState, contracts map[v1.ObjectName]v1.WorkflowContract) (v1.WorkflowContract, error) {
	mergedProps := map[string]string{}
	requiredSet := map[string]bool{}
	dialect := ""
	for _, name := range rs.order {
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
				return v1.WorkflowContract{}, &schemaConflict{field: field, a: prev, b: typ}
			}
			mergedProps[field] = typ
		}
		for _, r := range in.Required {
			requiredSet[r] = true
		}
	}
	input := marshalObjectSchema(mergedProps, requiredSet)

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

// schemaConflict is a RootSchemaConflict: two roots require the same field at different primitive types.
type schemaConflict struct {
	field, a, b string
}

func (e *schemaConflict) Error() string {
	return fmt.Sprintf("root steps require field %q at conflicting types %s vs %s", e.field, e.a, e.b)
}

// marshalObjectSchema builds an object JSON Schema from a props map + required set.
func marshalObjectSchema(props map[string]string, required map[string]bool) json.RawMessage {
	p := map[string]map[string]string{}
	for name, typ := range props {
		p[name] = map[string]string{"type": typ}
	}
	req := make([]string, 0, len(required))
	for r := range required {
		req = append(req, r)
	}
	sort.Strings(req)
	b, _ := json.Marshal(map[string]interface{}{"type": "object", "properties": p, "required": req})
	return b
}
