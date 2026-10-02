package controlplane

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

const schemaPrefix = "#/components/schemas/"

// inlineRegistry derives each schema in the shape encoding/json writes the type. huma flattens an
// embedded struct only when it has no json tag, so a `json:",inline"` one (TypeMeta, Status, ObjectRef)
// became a nested, required property that the server never emits and a flat body could not satisfy.
type inlineRegistry struct {
	huma.Registry
	done map[string]bool
}

func newInlineRegistry() *inlineRegistry {
	return &inlineRegistry{Registry: huma.NewMapRegistry(schemaPrefix, huma.DefaultSchemaNamer), done: map[string]bool{}}
}

// Schema registers t (and every type it reaches) through the wrapped registry, then flattens the new schemas.
func (r *inlineRegistry) Schema(t reflect.Type, allowRef bool, hint string) *huma.Schema {
	s := r.Registry.Schema(t, allowRef, hint)
	for name := range r.Map() {
		r.flatten(name)
	}
	return s
}

// MarshalJSON serializes the components the way huma's own registry does.
func (r *inlineRegistry) MarshalJSON() ([]byte, error) { return json.Marshal(r.Map()) }

// flatten replaces each `json:",inline"` property of the named struct schema with the embedded schema's
// properties and required names, as encoding/json promotes them (an outer field of the same name wins).
func (r *inlineRegistry) flatten(name string) {
	if r.done[name] {
		return
	}
	r.done[name] = true
	s, t := r.Map()[name], r.TypeFromRef(schemaPrefix+name)
	if s == nil || t == nil || t.Kind() != reflect.Struct {
		return
	}
	changed := false
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if jsonName, _, _ := strings.Cut(tag, ","); !f.Anonymous || tag == "" || tag == "-" || jsonName != "" {
			continue
		}
		embedded := s.Properties[f.Name]
		if embedded == nil {
			continue
		}
		if embedded.Ref != "" {
			r.flatten(strings.TrimPrefix(embedded.Ref, schemaPrefix))
			if embedded = r.SchemaFromRef(embedded.Ref); embedded == nil {
				continue
			}
		}
		delete(s.Properties, f.Name)
		promoted := make([]string, 0, len(embedded.Required))
		for _, req := range embedded.Required {
			if _, shadowed := s.Properties[req]; !shadowed {
				promoted = append(promoted, req)
			}
		}
		if at := slices.Index(s.Required, f.Name); at >= 0 {
			s.Required = slices.Replace(s.Required, at, at+1, promoted...)
		} else {
			s.Required = append(s.Required, promoted...)
		}
		for prop, ps := range embedded.Properties {
			if _, shadowed := s.Properties[prop]; !shadowed {
				s.Properties[prop] = ps
			}
		}
		changed = true
	}
	if changed {
		s.PrecomputeMessages()
	}
}
