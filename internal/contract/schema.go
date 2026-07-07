package contract

import "encoding/json"

// schema is the subset of JSON Schema 2020-12 keywords the profile gate inspects. Unknown
// keywords (title, description, format, minimum, pattern, …) are ignored — they constrain a
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

func (t *typeField) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '[' {
		return json.Unmarshal(b, &t.values)
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	t.values = []string{one}
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
