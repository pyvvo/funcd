package workflow

import (
	"encoding/json"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

func sc(s string) json.RawMessage { return json.RawMessage(s) }

// obj builds an object schema with the given required typed fields.
func obj(fields map[string]string, required ...string) json.RawMessage {
	props := map[string]map[string]string{}
	for k, t := range fields {
		props[k] = map[string]string{"type": t}
	}
	b, _ := json.Marshal(map[string]interface{}{"type": "object", "properties": props, "required": required})
	return b
}

// scenario core: the edge check — required-primitive match, missing, mistype, widening, params, void.
func TestCheckEdge(t *testing.T) {
	producer := obj(map[string]string{"rows": "integer", "day": "string"}, "rows", "day")

	if d := checkEdge(producer, obj(map[string]string{"rows": "integer"}, "rows"), nil); len(d) != 0 {
		t.Fatalf("matching edge should pass, got %v", d)
	}
	// missing required field.
	if d := checkEdge(producer, obj(map[string]string{"score": "number"}, "score"), nil); len(d) != 1 || d[0].Field != "score" || d[0].Got != "" {
		t.Fatalf("missing field should be reported, got %v", d)
	}
	// type mismatch (string vs integer).
	if d := checkEdge(producer, obj(map[string]string{"rows": "string"}, "rows"), nil); len(d) != 1 || d[0].Got != "integer" || d[0].Want != "string" {
		t.Fatalf("type mismatch should be reported, got %v", d)
	}
	// integer producer satisfies a number consumer (safe widening).
	if d := checkEdge(producer, obj(map[string]string{"rows": "number"}, "rows"), nil); len(d) != 0 {
		t.Fatalf("integer→number widening should pass, got %v", d)
	}
	// scenario: params-field-not-required-from-parent.
	if d := checkEdge(producer, obj(map[string]string{"region": "string"}, "region"), map[string]bool{"region": true}); len(d) != 0 {
		t.Fatalf("a params-provided field must not be required from the parent, got %v", d)
	}
}

// scenario: void-output-satisfies-void-input.
func TestCheckEdgeVoid(t *testing.T) {
	void := sc(`{"type":"null"}`)
	if d := checkEdge(void, void, nil); len(d) != 0 {
		t.Fatalf("void→void should pass, got %v", d)
	}
	// a void producer cannot satisfy a non-void consumer that requires a field.
	if d := checkEdge(void, obj(map[string]string{"rows": "integer"}, "rows"), nil); len(d) != 1 {
		t.Fatalf("void producer vs required consumer should mismatch, got %v", d)
	}
	// a void consumer requires nothing from any producer.
	if d := checkEdge(obj(map[string]string{"rows": "integer"}, "rows"), void, nil); len(d) != 0 {
		t.Fatalf("void consumer should require nothing, got %v", d)
	}
}

// checkInput — a JSON number satisfies integer or number; a missing/mistyped field is reported.
func TestCheckInput(t *testing.T) {
	schema := obj(map[string]string{"day": "string", "n": "integer"}, "day", "n")
	if d := v1.CheckInput(sc(`{"day":"mon","n":4}`), schema); len(d) != 0 {
		t.Fatalf("a valid input should pass (number satisfies integer), got %v", d)
	}
	if d := v1.CheckInput(sc(`{"day":"mon"}`), schema); len(d) != 1 || d[0].Field != "n" {
		t.Fatalf("missing required field n should be reported, got %v", d)
	}
	if d := v1.CheckInput(sc(`{"day":5,"n":4}`), schema); len(d) != 1 || d[0].Field != "day" || d[0].Got != "number" {
		t.Fatalf("wrong-typed day should be reported, got %v", d)
	}
}

// scenario: multi-root-conflict-schemamismatch (the derive half) + workflow-contract-derived-and-cached.
func TestDeriveWorkflowContract(t *testing.T) {
	// a→b: a is the root, b the leaf.
	rs := newRunState(spec(step("a", ""), step("b", "", "a")))
	contracts := map[v1.ObjectName]v1.WorkflowContract{
		"a": {Input: obj(map[string]string{"day": "string"}, "day"), Output: obj(map[string]string{"rows": "integer"}, "rows")},
		"b": {Input: obj(map[string]string{"rows": "integer"}, "rows"), Output: obj(map[string]string{"score": "number"}, "score")},
	}
	c, err := deriveWorkflowContract(rs, contracts)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if in := v1.ParseSchemaView(c.Input); in.Props["day"] != "string" || len(in.Required) != 1 {
		t.Fatalf("derived input should be the root's {day:string}, got %s", c.Input)
	}
	if out := v1.ParseSchemaView(c.Output); out.Props["score"] != "number" { // single leaf ⇒ verbatim
		t.Fatalf("single-leaf output should be b's output verbatim, got %s", c.Output)
	}

	// two roots requiring the same field at conflicting types ⇒ RootSchemaConflict. ADR-0094's implicit
	// chaining yields a single root from a spec, so the multi-root merge is a defensive guard — force two
	// roots directly to exercise it.
	rs2 := newRunState(spec(step("x", ""), step("y", "")))
	rs2.steps["y"].dependsOn = nil // make y a second root
	conflict := map[v1.ObjectName]v1.WorkflowContract{
		"x": {Input: obj(map[string]string{"id": "string"}, "id")},
		"y": {Input: obj(map[string]string{"id": "integer"}, "id")},
	}
	if _, err := deriveWorkflowContract(rs2, conflict); err == nil {
		t.Fatal("conflicting root requirements must be a RootSchemaConflict")
	}
}

// multi-leaf ⇒ the derived output is the composite keyed by leaf name.
func TestDeriveMultiLeafComposite(t *testing.T) {
	rs := newRunState(spec(step("a", ""), step("b", "", "a"), step("c", "", "a")))
	contracts := map[v1.ObjectName]v1.WorkflowContract{
		"a": {Output: obj(map[string]string{"x": "integer"}, "x")},
		"b": {Output: obj(map[string]string{"y": "integer"}, "y")},
		"c": {Output: obj(map[string]string{"z": "integer"}, "z")},
	}
	c, err := deriveWorkflowContract(rs, contracts)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	out := v1.ParseSchemaView(c.Output)
	if out.Props["b"] != "object" || out.Props["c"] != "object" {
		t.Fatalf("multi-leaf output should compose {b:object, c:object}, got %s", c.Output)
	}
}
