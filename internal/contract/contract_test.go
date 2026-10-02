package contract_test

import (
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/contract"
)

// scenario: type-is-source-of-truth / the def — every SUPPORTED construct is accepted.
func TestCheckAcceptsSupportedProfile(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"json (empty schema = any)":       `{}`,
		"string":                          `{"type":"string"}`,
		"string with format + pattern":    `{"type":"string","format":"email","pattern":"^.+@.+$","minLength":3}`,
		"integer int32 + range":           `{"type":"integer","format":"int32","minimum":0,"maximum":99}`,
		"number":                          `{"type":"number"}`,
		"boolean":                         `{"type":"boolean"}`,
		"string enum":                     `{"enum":["a","b","c"]}`,
		"closed record":                   `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`,
		"closed record, optional field":   `{"type":"object","properties":{"id":{"type":"string"},"note":{"type":"string"}},"required":["id"],"additionalProperties":false}`,
		"nullable scalar":                 `{"type":["string","null"]}`,
		"array of scalars":                `{"type":"array","items":{"type":"string"}}`,
		"array of closed records":         `{"type":"array","items":{"type":"object","properties":{"x":{"type":"integer"}},"additionalProperties":false}}`,
		"typed map (Record<string,T>)":    `{"type":"object","additionalProperties":{"type":"number"}}`,
		"json field inside closed record": `{"type":"object","properties":{"id":{"type":"string"},"payload":{}},"required":["id"],"additionalProperties":false}`,
		"discriminated union": `{"oneOf":[
			{"type":"object","properties":{"kind":{"const":"a"},"id":{"type":"string"}},"additionalProperties":false},
			{"type":"object","properties":{"kind":{"const":"b"},"reason":{"type":"string"}},"additionalProperties":false}
		],"discriminator":{"propertyName":"kind"}}`,
	}
	for name, js := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := contract.Check([]byte(js)); err != nil {
				t.Fatalf("expected in-profile, got error: %v", err)
			}
		})
	}
}

// scenario: out-of-profile-rejected — every FORBIDDEN construct fails with a fault.Invalid.
func TestCheckRejectsOutOfProfile(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"open record (additionalProperties true)":   `{"type":"object","properties":{"id":{"type":"string"}},"additionalProperties":true}`,
		"open record (additionalProperties absent)": `{"type":"object","properties":{"id":{"type":"string"}}}`,
		"non-discriminated oneOf":                   `{"oneOf":[{"type":"string"},{"type":"integer"}]}`,
		"anyOf":                                     `{"anyOf":[{"type":"string"},{"type":"integer"}]}`,
		"allOf":                                     `{"allOf":[{"type":"object","additionalProperties":false}]}`,
		"not":                                       `{"not":{"type":"string"}}`,
		"if/then/else":                              `{"if":{"type":"string"},"then":{"type":"string"}}`,
		"external/recursive $ref":                   `{"type":"object","properties":{"next":{"$ref":"#"}},"additionalProperties":false}`,
		"nested open record":                        `{"type":"object","properties":{"inner":{"type":"object","properties":{"x":{"type":"string"}},"additionalProperties":true}},"additionalProperties":false}`,
		"open record inside array":                  `{"type":"array","items":{"type":"object","additionalProperties":true}}`,
		"mixed map + properties":                    `{"type":"object","properties":{"id":{"type":"string"}},"additionalProperties":{"type":"string"}}`,
	}
	for name, js := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := contract.Check([]byte(js))
			if err == nil {
				t.Fatalf("expected out-of-profile rejection, got nil")
			}
			if fault.KindOf(err) != fault.Invalid {
				t.Fatalf("expected fault.Invalid, got kind %v (%v)", fault.KindOf(err), err)
			}
		})
	}
}

// scenario: json-input-accepts-anything — an empty contract / no contract is the `Json` form.
func TestCheckTreatsEmptyAsJson(t *testing.T) {
	t.Parallel()
	for _, js := range []string{``, `{}`, `   `} {
		if err := contract.Check([]byte(js)); err != nil {
			t.Fatalf("empty/Json contract %q should be in profile, got %v", js, err)
		}
	}
}

// recursive types are forbidden (they require $ref, which is rejected) — the def discipline.
func TestCheckRejectsRecursiveViaRef(t *testing.T) {
	t.Parallel()
	// a tree node referencing itself
	js := `{"type":"object","properties":{"value":{"type":"string"},"children":{"type":"array","items":{"$ref":"#"}}},"additionalProperties":false}`
	if err := contract.Check([]byte(js)); err == nil {
		t.Fatal("recursive type (via $ref) must be rejected")
	}
}

// Issue #65: an unquoted YAML `type: null` becomes the JSON null type value, which is not JSON Schema
// (no runtime validator compiles it); it must not pass as the `Json` (any) form. The void side is the
// string "null" (ADR-0090), which stays accepted.
func TestIssue65_CheckRejectsNullTypeValue(t *testing.T) {
	t.Parallel()
	for name, js := range map[string]string{
		"root type null":          `{"type":null}`,
		"null entry in type list": `{"type":["string",null]}`,
		"nested field type null":  `{"type":"object","properties":{"x":{"type":null}},"additionalProperties":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := contract.Check([]byte(js))
			if err == nil {
				t.Fatalf("contract %s must be rejected, got nil", js)
			}
			if fault.KindOf(err) != fault.Invalid {
				t.Fatalf("expected fault.Invalid, got kind %v (%v)", fault.KindOf(err), err)
			}
		})
	}
	if err := contract.Check([]byte(`{"type":"null"}`)); err != nil {
		t.Fatalf(`void side {"type":"null"} must stay in profile, got %v`, err)
	}
}

func TestCheckRejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	if err := contract.Check([]byte(`{not json`)); err == nil || fault.KindOf(err) != fault.Invalid {
		t.Fatalf("malformed JSON should be a fault.Invalid, got %v", err)
	}
}
