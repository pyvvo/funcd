package v1alpha1

import "encoding/json"

// Typed-contract checking primitives (ADR-0098, F65). These live on the type (not in internal/workflow)
// so the control-plane admission — a near-leaf that must not import internal/workflow — can validate a
// WorkflowRun's input against a Workflow's cached contract. The rule is required-primitive-property
// subsumption + void rules; full JSON-Schema/structural subsumption is a documented follow-on.

// FieldDiff is one required consumer field a producer or document does not satisfy. Got == "" ⇒ the
// field is missing; otherwise Got is the actual primitive type and Want the required one.
type FieldDiff struct {
	Field string
	Want  string
	Got   string
}

func (d FieldDiff) String() string {
	if d.Got == "" {
		return "\"" + d.Field + "\" (want " + d.Want + ") is missing"
	}
	return "\"" + d.Field + "\" is " + d.Got + ", want " + d.Want
}

// SchemaView is the F65-relevant subset of a JSON Schema: a type, typed top-level properties, their
// declared defaults (ADR-0095), and the required set. A void schema is type "null" (or empty/absent —
// nothing required).
type SchemaView struct {
	Type     string
	Props    map[string]string          // property name → primitive type
	Defaults map[string]json.RawMessage // property name → declared default, for the properties that have one
	Required []string
}

// ParseSchemaView reads the F65-relevant fields from a JSON Schema (tolerant: unknown keywords ignored).
func ParseSchemaView(raw json.RawMessage) SchemaView {
	var s struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type    string          `json:"type"`
			Default json.RawMessage `json:"default"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &s)
	}
	props := make(map[string]string, len(s.Properties))
	defaults := map[string]json.RawMessage{}
	for name, p := range s.Properties {
		props[name] = p.Type
		if p.Default != nil {
			defaults[name] = p.Default
		}
	}
	return SchemaView{Type: s.Type, Props: props, Defaults: defaults, Required: s.Required}
}

// IsVoid reports whether a schema declares no data (a null-typed or empty schema — ADR-0090 void).
func (s SchemaView) IsVoid() bool {
	return (s.Type == "null" || s.Type == "") && len(s.Props) == 0 && len(s.Required) == 0
}

// CheckInput reports the required properties of the schema not satisfied by an actual JSON document
// (doc↔schema primitive match). A JSON number satisfies integer or number (a doc can't distinguish
// them). Empty ⇒ valid. Used by WorkflowRun admission and the engine's run-start gate (ADR-0098).
func CheckInput(doc, schema json.RawMessage) []FieldDiff {
	s := ParseSchemaView(schema)
	if len(s.Required) == 0 {
		return nil
	}
	got := inferDocTypes(doc)
	var diffs []FieldDiff
	for _, field := range s.Required {
		g, ok := got[field]
		if !ok {
			diffs = append(diffs, FieldDiff{Field: field, Want: s.Props[field]})
			continue
		}
		if !docTypeCompatible(g, s.Props[field]) {
			diffs = append(diffs, FieldDiff{Field: field, Want: s.Props[field], Got: g})
		}
	}
	return diffs
}

// docTypeCompatible compares an inferred document primitive type against a required schema type; numeric
// is one bucket (a JSON number satisfies integer or number).
func docTypeCompatible(got, want string) bool {
	if got == want {
		return true
	}
	numeric := map[string]bool{"integer": true, "number": true}
	return numeric[got] && numeric[want]
}

// inferDocTypes reports the primitive type of each top-level field of a JSON object document.
func inferDocTypes(doc json.RawMessage) map[string]string {
	var obj map[string]json.RawMessage
	if len(doc) == 0 || json.Unmarshal(doc, &obj) != nil {
		return nil
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		out[k] = jsonPrimitive(v)
	}
	return out
}

// jsonPrimitive reports a raw JSON value's primitive type (number for all JSON numbers).
func jsonPrimitive(raw json.RawMessage) string {
	var v interface{}
	if json.Unmarshal(raw, &v) != nil {
		return "unknown"
	}
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	default:
		return "null"
	}
}

// FieldDiffs renders field diffs into one deterministic message (sorted by field).
func FieldDiffs(d []FieldDiff) string {
	if len(d) == 0 {
		return ""
	}
	// simple insertion sort by Field (small n) to avoid a sort import churn.
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j-1].Field > d[j].Field; j-- {
			d[j-1], d[j] = d[j], d[j-1]
		}
	}
	out := d[0].String()
	for _, x := range d[1:] {
		out += "; " + x.String()
	}
	return out
}
