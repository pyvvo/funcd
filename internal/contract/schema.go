package contract

import (
	"encoding/json"
	"errors"
)

// schema is the subset of JSON Schema 2020-12 keywords the profile gate inspects. Unknown
// keywords (title, description, minimum, pattern, …) are ignored — they constrain a
// supported type but never change whether it is in profile.
type schema struct {
	Type                 typeField          `json:"type,omitempty"`
	Properties           map[string]*schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties addlProps          `json:"additionalProperties,omitempty"`
	Items                *schema            `json:"items,omitempty"`
	Enum                 []json.RawMessage  `json:"enum,omitempty"`
	OneOf                []*schema          `json:"oneOf,omitempty"`
	AnyOf                []*schema          `json:"anyOf,omitempty"`
	AllOf                []*schema          `json:"allOf,omitempty"`
	Not                  *schema            `json:"not,omitempty"`
	If                   *schema            `json:"if,omitempty"`
	Then                 *schema            `json:"then,omitempty"`
	Else                 *schema            `json:"else,omitempty"`
	Ref                  string             `json:"$ref,omitempty"`
	Format               string             `json:"format,omitempty"`
	Discriminator        *discriminator     `json:"discriminator,omitempty"`
}

// discriminator marks a tagged union (OpenAPI/JSON-Schema discriminator object).
type discriminator struct {
	PropertyName string `json:"propertyName,omitempty"`
}

// isEmpty reports the `Json` (any) form: an object schema with no profile-relevant keyword.
func (s *schema) isEmpty() bool {
	return len(s.Type.values) == 0 && len(s.Properties) == 0 && s.AdditionalProperties.kind == apAbsent &&
		s.Items == nil && len(s.Enum) == 0 && len(s.OneOf) == 0 && len(s.AnyOf) == 0 &&
		len(s.AllOf) == 0 && s.Not == nil && s.If == nil && s.Then == nil && s.Else == nil && s.Ref == ""
}

// primaryType returns the first non-"null" type, or "" when no type keyword is present.
func (s *schema) primaryType() string {
	for _, t := range s.Type.values {
		if t != "null" {
			return t
		}
	}
	if len(s.Type.values) > 0 {
		return s.Type.values[0] // only "null"
	}
	return ""
}

// typeField captures JSON Schema's `type`, which is a string OR an array of strings.
type typeField struct{ values []string }

// inProfile reports whether `type` is absent, one profile type, or the nullable list of one profile type
// plus "null"; any other list is an untagged union (ADR-0058).
func (t typeField) inProfile() bool {
	nulls := 0
	for _, n := range t.values {
		switch n {
		case "null":
			nulls++
		case "object", "array", "string", "integer", "number", "boolean":
		default:
			return false
		}
	}
	return len(t.values) <= 1 || (len(t.values) == 2 && nulls == 1)
}

// errNullType rejects a JSON null type value (an unquoted YAML `type: null`): it is not JSON Schema, and
// dropping it would turn the side into the `Json` (any) form.
var errNullType = errors.New(`"type" is JSON null, not a type name; a void side is {"type":"null"} (quote "null" in YAML)`)

func (t *typeField) UnmarshalJSON(b []byte) error {
	var names []*string
	if len(b) > 0 && b[0] == '[' {
		if err := json.Unmarshal(b, &names); err != nil {
			return err
		}
	} else {
		var one *string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		names = []*string{one}
	}
	for _, n := range names {
		if n == nil {
			return errNullType
		}
		t.values = append(t.values, *n)
	}
	return nil
}

// apKind is the shape of `additionalProperties`: absent, a boolean, or a schema.
type apKind int

const (
	apAbsent apKind = iota
	apBool
	apSchema
)

// addlProps captures `additionalProperties`, which is a boolean OR a schema OR absent.
type addlProps struct {
	kind   apKind
	Bool   bool
	Schema *schema
}

func (a *addlProps) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		a.kind = apAbsent
		return nil
	}
	if string(b) == "true" || string(b) == "false" {
		a.kind = apBool
		a.Bool = string(b) == "true"
		return nil
	}
	a.kind = apSchema
	a.Schema = &schema{}
	return json.Unmarshal(b, a.Schema)
}

// isFalse reports additionalProperties:false (a closed record).
func (a addlProps) isFalse() bool { return a.kind == apBool && !a.Bool }

// isSchema reports additionalProperties:<schema> (a typed map).
func (a addlProps) isSchema() bool { return a.kind == apSchema }
