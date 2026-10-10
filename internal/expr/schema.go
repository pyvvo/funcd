package expr

import (
	"encoding/json"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
)

const schemaOp = "expr.schema"

// SchemaOption configures NewSchemaResolver.
type SchemaOption func(*schemaConfig)

type schemaConfig struct{ strict bool }

// StrictTypes makes the resolver type a path only from the schema (ADR-0217 Decision 4): each segment must be
// declared under properties with one explicit type, type object for a segment the path reads through; an object
// without properties, or a node typed only by enum, const, a type array, oneOf or $ref, is fault.NotFound, never
// "string".
func StrictTypes() SchemaOption { return func(c *schemaConfig) { c.strict = true } }

// NewSchemaResolver is a Resolver answering path types from JSON-Schema documents, one per root. Each root in
// optional may be absent (ADR-0166). Without StrictTypes it descends `properties`, strict where the schema is
// precise (a declared-properties object with a missing key ⇒ NotFound, catching a misspelling) and permissive where
// the schema is silent about nesting (V1 primitive-only — structural typing is deferred). It decodes each schema
// once, on its first reference, so a check costs one decoding of each schema however many references it resolves.
func NewSchemaResolver(schemas map[string]json.RawMessage, optional map[string]bool, opts ...SchemaOption) Resolver {
	var cfg schemaConfig
	for _, o := range opts {
		o(&cfg)
	}
	return schemaResolver{schemas: schemas, decoded: map[string]*schemaNode{}, optionalRoots: optional, strict: cfg.strict}
}

type schemaResolver struct {
	schemas       map[string]json.RawMessage
	decoded       map[string]*schemaNode
	optionalRoots map[string]bool
	strict        bool
}

// schemaNode is the part of a JSON Schema the resolvers read. A schema that is not an object, or a keyword of the
// wrong type, decodes to its zero value, which reads as a schema silent about the keyword.
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

func (s schemaResolver) Resolve(root string, path []string) (Field, error) {
	raw, ok := s.schemas[root]
	if !ok {
		return Field{}, fault.NotFoundf(schemaOp, "root %q not in scope", root)
	}
	cur, ok := s.decoded[root]
	if !ok {
		cur = &schemaNode{}
		_ = json.Unmarshal(raw, cur)
		s.decoded[root] = cur
	}
	required := true // ADR-0095: a path is required only if every segment is in its parent's `required`
	for i, seg := range path {
		if s.strict && i > 0 && cur != nil && cur.Type != "object" {
			return Field{}, fault.NotFoundf(schemaOp, "field %q is read through %q, which is not of type object", seg, path[i-1])
		}
		if cur == nil || cur.Properties == nil {
			if s.strict {
				return Field{}, fault.NotFoundf(schemaOp, "field %q is not declared under properties", seg)
			}
			// The schema is silent about nesting — V1 can't type deeper; accept permissively.
			return Field{Type: "string", Required: required}, nil
		}
		next, found := cur.Properties[seg]
		if !found {
			return Field{}, fault.NotFoundf(schemaOp, "field %q not in the schema", seg)
		}
		required = required && slices.Contains(cur.Required, seg)
		cur = next
	}
	if s.strict && (cur == nil || cur.Type == "" || (cur.Type == "object" && cur.Properties == nil)) {
		return Field{}, fault.NotFoundf(schemaOp, "the schema gives %q no properties or no single explicit type", root)
	}
	if cur == nil {
		return Field{Required: required}, nil
	}
	if len(path) == 0 && s.optionalRoots[root] {
		// An optional root is never required or defaulted (a default does not stand in for a skipped
		// branch), and its Type is never empty: an empty one reads as absent, and the root's guard would
		// then skip checking what it guards (ADR-0166).
		typ := cur.Type
		if typ == "" {
			typ = "object"
		}
		return Field{Type: typ, Items: cur.Items.Type}, nil
	}
	return Field{Type: cur.Type, Items: cur.Items.Type, Required: required, HasDefault: cur.Default != nil, Default: cur.Default}, nil
}
