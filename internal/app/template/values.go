package template

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/pyvvo/funcd/api/fault"
)

const (
	valuesOp  = "app.values"
	draft2020 = "https://json-schema.org/draft/2020-12/schema"
	schemaURL = "file:///values-schema.json"
)

// ReadValues reads one -f file: one YAML 1.2 document whose top level is a mapping, as JSON; an empty file is {}.
func ReadValues(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a values file the caller names
	if err != nil {
		return nil, fault.Invalidf(valuesOp, "read %s: %v", path, err)
	}
	dec := yamlv3.NewDecoder(bytes.NewReader(data))
	var docs []*yamlv3.Node
	for {
		var doc yamlv3.Node
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fault.Invalidf(valuesOp, "%s: %v", path, err)
		}
		docs = append(docs, &doc)
	}
	switch {
	case len(docs) == 0:
		return json.RawMessage(`{}`), nil
	case len(docs) > 1:
		return nil, fault.Invalidf(valuesOp, "%s holds %d YAML documents, want one", path, len(docs))
	case len(docs[0].Content) == 0:
		return json.RawMessage(`{}`), nil
	case docs[0].Content[0].Kind != yamlv3.MappingNode:
		return nil, fault.Invalidf(valuesOp, "%s: the top level is not a mapping", path)
	}
	raw, err := nodeJSON(docs[0])
	if err != nil {
		return nil, fault.Invalidf(valuesOp, "%s: %v", path, err)
	}
	return raw, nil
}

// mergeValues merges the -f files in order: maps key by key; a list, a scalar or null replaces the earlier value.
func mergeValues(files []json.RawMessage) (json.RawMessage, error) {
	merged := map[string]interface{}{}
	for i, f := range files {
		var m map[string]interface{}
		if err := decodeNumbers(f, &m); err != nil || m == nil {
			return nil, fault.Invalidf(renderOp, "values file %d is not a mapping", i+1)
		}
		mergeInto(merged, m)
	}
	return json.Marshal(merged)
}

func mergeInto(dst, src map[string]interface{}) {
	for k, v := range src {
		if sm, ok := v.(map[string]interface{}); ok {
			if dm, ok := dst[k].(map[string]interface{}); ok {
				mergeInto(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

func decodeNumbers(raw json.RawMessage, v interface{}) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d.Decode(v)
}

// validateValues validates the merged values against the closed values schema (Decision 3); it fills nothing.
func validateValues(schema, values json.RawMessage) error {
	c, err := valuesCompiler(schema)
	if err != nil {
		return err
	}
	return validateAt(c, "", values, "values do not match valuesSchema")
}

// validateKeys validates only the given top-level values, each against its subschema (ADR-0218 Decision 4).
func validateKeys(schema, values json.RawMessage, keys []string) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(values, &top); err != nil {
		return fault.Internalf(renderOp, "decode the merged values: %v", err)
	}
	c, err := valuesCompiler(schema)
	if err != nil {
		return err
	}
	for _, k := range keys {
		raw, ok := top[k]
		if !ok {
			continue
		}
		ptr := "#/properties/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(k)
		if err := validateAt(c, ptr, raw, "values do not match valuesSchema at /"+k); err != nil {
			return err
		}
	}
	return nil
}

// valuesCompiler is the compiler of the closed values schema: draft 2020-12 only, with no non-local $ref.
func valuesCompiler(schema json.RawMessage) (*jsonschema.Compiler, error) {
	var doc interface{}
	if err := decodeNumbers(schema, &doc); err != nil {
		return nil, fault.Invalidf(renderOp, "valuesSchema is not JSON: %v", err)
	}
	if m, ok := doc.(map[string]interface{}); ok {
		if s, ok := m["$schema"].(string); ok && strings.TrimSuffix(s, "#") != draft2020 {
			return nil, fault.Invalidf(renderOp, "valuesSchema: $schema %q is not draft 2020-12", s)
		}
	}
	closeSchema(doc, true)
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(localOnly{})
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, fault.Invalidf(renderOp, "valuesSchema: %v", err)
	}
	return c, nil
}

// validateAt validates instance against the schema at fragment ptr of the values schema ("" for the root).
func validateAt(c *jsonschema.Compiler, ptr string, instance json.RawMessage, refused string) error {
	sch, err := c.Compile(schemaURL + ptr)
	if err != nil {
		return fault.Invalidf(renderOp, "valuesSchema: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(instance))
	if err != nil {
		return fault.Internalf(renderOp, "decode the merged values: %v", err)
	}
	var verr *jsonschema.ValidationError
	if err := sch.Validate(inst); errors.As(err, &verr) {
		return fault.Invalidf(renderOp, "%s: %s", refused, strings.Join(leaves(verr, nil), "; "))
	} else if err != nil {
		return fault.Invalidf(renderOp, "validate values: %v", err)
	}
	return nil
}

// localOnly is the schema loader: every $ref must point inside valuesSchema.
type localOnly struct{}

func (localOnly) Load(url string) (interface{}, error) {
	return nil, fault.Invalidf(renderOp, "valuesSchema: $ref %s is not local to valuesSchema", url)
}

// leaves renders each innermost validation error as its keyword, its instance location and its message.
func leaves(e *jsonschema.ValidationError, out []string) []string {
	if len(e.Causes) > 0 {
		for _, c := range e.Causes {
			out = leaves(c, out)
		}
		return out
	}
	at := "at '/" + strings.Join(e.InstanceLocation, "/") + "': "
	if _, ok := e.ErrorKind.(*kind.FalseSchema); ok && strings.HasSuffix(e.SchemaURL, "/unevaluatedProperties") {
		return append(out, "unevaluatedProperties "+at+"valuesSchema does not declare this value")
	}
	leaf := &jsonschema.ValidationError{SchemaURL: e.SchemaURL, InstanceLocation: e.InstanceLocation, ErrorKind: e.ErrorKind}
	if kw := e.ErrorKind.KeywordPath(); len(kw) > 0 {
		return append(out, kw[len(kw)-1]+" "+leaf.Error())
	}
	return append(out, leaf.Error())
}

// closeSchema adds unevaluatedProperties: false where Decision 3 says: to the root and to every subschema reached
// through properties, items or prefixItems that has properties, type: object or a $ref, unless it sets
// additionalProperties or unevaluatedProperties; inside a $defs entry it walks the same way but skips the entry
// itself, which the schema holding its $ref covers. Keys declared under if/then/allOf/$ref then count as declared.
func closeSchema(v interface{}, here bool) {
	s, ok := v.(map[string]interface{})
	if !ok {
		return
	}
	_, setsAdditional := s["additionalProperties"]
	_, setsUnevaluated := s["unevaluatedProperties"]
	if here && !setsAdditional && !setsUnevaluated {
		s["unevaluatedProperties"] = false
	}
	var subs []interface{}
	if props, ok := s["properties"].(map[string]interface{}); ok {
		for _, p := range props {
			subs = append(subs, p)
		}
	}
	if items, ok := s["items"].(map[string]interface{}); ok {
		subs = append(subs, items)
	}
	if prefix, ok := s["prefixItems"].([]interface{}); ok {
		subs = append(subs, prefix...)
	}
	for _, sub := range subs {
		closeSchema(sub, objectLike(sub))
	}
	if defs, ok := s["$defs"].(map[string]interface{}); ok {
		for _, d := range defs {
			closeSchema(d, false)
		}
	}
}

// objectLike reports a subschema with properties, type: object or a $ref.
func objectLike(v interface{}) bool {
	s, ok := v.(map[string]interface{})
	if !ok {
		return false
	}
	_, props := s["properties"]
	_, ref := s["$ref"]
	return props || ref || s["type"] == "object"
}
