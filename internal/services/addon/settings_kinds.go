package addon

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/expr"
	"github.com/pyvvo/funcd/internal/store"
)

// FieldOf is the value at a field path under spec.config, and whether the config holds it.
func FieldOf(o *v1.AddonObject, path string) (json.RawMessage, bool) {
	segs, ok := v1.ConfigField(path)
	if !ok {
		return nil, false
	}
	cur := configOf(o)
	for _, s := range segs {
		var m map[string]json.RawMessage
		if json.Unmarshal(cur, &m) != nil {
			return nil, false
		}
		if cur, ok = m[s]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// TargetOf is the name an object of settings kind st gives its target, and whether it gives one.
func TargetOf(st *v1.AddonSettings, o *v1.AddonObject) (v1.ObjectName, bool) {
	raw, ok := FieldOf(o, st.Target)
	var name string
	if !ok || json.Unmarshal(raw, &name) != nil || name == "" {
		return "", false
	}
	return v1.ObjectName(name), true
}

// The documents a settings rule reads: object and others always, oldObject on a replace only.
const (
	ruleObject    = "object"
	ruleOldObject = "oldObject"
	ruleOthers    = "others"
)

// ruleSchemas types the rule documents: object and oldObject by the settings schema under spec.config, its envelope
// required so a required config field reads as required (ADR-0166), and others as an array, of which the typed subset
// of ADR-0095 reads only the length.
func ruleSchemas(schema json.RawMessage) map[string]json.RawMessage {
	if len(bytes.TrimSpace(schema)) == 0 {
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	obj := json.RawMessage(`{"type":"object","required":["metadata","spec"],"properties":{"metadata":{"type":"object",` +
		`"required":["name"],"properties":{"name":{"type":"string"}}},"spec":{"type":"object","required":["config"],` +
		`"properties":{"config":` + string(schema) + `}}}}`)
	return map[string]json.RawMessage{ruleObject: obj, ruleOldObject: obj, ruleOthers: json.RawMessage(`{"type":"array","items":{"type":"object"}}`)}
}

// parseRules parses and checks each rule as a condition over the rule documents, which schema types.
func parseRules(st *v1.AddonSettings, schema json.RawMessage) ([]*expr.Expr, error) {
	const op = "services.addon.parseRules"
	resolver := expr.NewSchemaResolver(ruleSchemas(schema), map[string]bool{ruleOldObject: true}, expr.StrictTypes())
	out := make([]*expr.Expr, len(st.Constraints.Rules))
	for i, r := range st.Constraints.Rules {
		e, err := expr.Parse(r.Rule, expr.Condition)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "rule %d", i)
		}
		if err := e.Check(resolver); err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "rule %d", i)
		}
		out[i] = e
	}
	return out, nil
}

// CheckSettings checks a settings kind's constraints against schema, what its image's contract says of an object's
// config (ADR-0358 Decision 4): it types the target as a string required at each level, and each field of unique and
// immutable; each rule checks against the rule documents. A contract that says nothing of it types no rule, and judges
// no field.
func CheckSettings(st *v1.AddonSettings, schema json.RawMessage) error {
	const op = "services.addon.CheckSettings"
	if !says(schema) {
		_, err := parseRules(st, schema)
		return err
	}
	if err := contract.Check(schema); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "%s schema", st.Kind)
	}
	resolver := expr.NewSchemaResolver(schemas(schema), nil, expr.StrictTypes())
	segs, _ := v1.ConfigField(st.Target)
	f, err := resolver.Resolve(configDoc, segs)
	if err != nil || f.Type != "string" || !f.Required {
		return fault.Invalidf(op, "%s target %s is not a string field that the schema requires at each level", st.Kind, st.Target)
	}
	for _, p := range slices.Concat(st.Constraints.Unique, st.Constraints.Immutable) {
		segs, _ := v1.ConfigField(p)
		if _, err := resolver.Resolve(configDoc, segs); err != nil {
			return fault.Invalidf(op, "%s constraint field %s is not a field the schema declares with one type", st.Kind, p)
		}
	}
	_, err = parseRules(st, schema)
	return err
}

// says reports whether a schema says anything: {} takes any value.
func says(schema json.RawMessage) bool {
	s := bytes.TrimSpace(schema)
	return len(s) > 0 && !bytes.Equal(s, []byte(`{}`))
}

// AdmitSettings checks a write of an object of settings kind st against its constraints (ADR-0340 Decision 4): its
// immutable fields against old, the counts and unique fields against others, the namespace's other objects of the
// kind, and each rule, which schema types; a nil one, of a contract that is not read, lets no rule judge it (ADR-0358
// Decision 5). The caller holds the namespace's admission lock.
func AdmitSettings(st *v1.AddonSettings, schema json.RawMessage, o, old *v1.AddonObject, others []*v1.AddonObject) error {
	const op = "admission.addon-settings"
	c := st.Constraints
	if old != nil {
		for _, p := range c.Immutable {
			was, _ := FieldOf(old, p)
			is, _ := FieldOf(o, p)
			if !jsonEqual(was, is) {
				return fault.Invalidf(op, "%s of a %s is immutable: it was %s", p, st.Kind, cmp.Or(string(was), "unset"))
			}
		}
	}
	target, _ := TargetOf(st, o)
	var sameTarget []string
	for _, x := range others {
		if t, ok := TargetOf(st, x); ok && t == target {
			sameTarget = append(sameTarget, string(x.Name))
		}
	}
	if n := c.Count.PerTarget; n > 0 && len(sameTarget) >= n {
		return fault.Invalidf(op, "count.perTarget of %s allows %d per target, and %s %q already has %s",
			st.Kind, n, st.Target, target, strings.Join(sameTarget, ", "))
	}
	if n := c.Count.PerNamespace; n > 0 && len(others) >= n {
		return fault.Invalidf(op, "count.perNamespace of %s allows %d per namespace, and %s holds %d", st.Kind, n, o.Namespace, len(others))
	}
	for _, p := range c.Unique {
		v, ok := FieldOf(o, p)
		if !ok {
			continue
		}
		for _, x := range others {
			if w, ok := FieldOf(x, p); ok && jsonEqual(v, w) {
				return fault.Invalidf(op, "%s of a %s is unique, and %s already holds %s", p, st.Kind, x.Name, v)
			}
		}
	}
	if schema == nil {
		return nil
	}
	rules, err := parseRules(st, schema)
	if err != nil {
		return err
	}
	docs, err := ruleDocs(o, old, others)
	if err != nil {
		return err
	}
	for i, e := range rules {
		ok, err := e.EvalBool(docs)
		if err != nil {
			return fault.Wrapf(err, fault.Invalid, op, "rule %d", i)
		}
		if !ok {
			return fault.Invalidf(op, "%s", st.Constraints.Rules[i].Message)
		}
	}
	return nil
}

// ruleDoc is the shape a rule reads of an object.
type ruleDoc struct {
	Metadata struct {
		Name v1.ObjectName `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Config json.RawMessage `json:"config"`
	} `json:"spec"`
}

func docOf(o *v1.AddonObject) ruleDoc {
	var d ruleDoc
	d.Metadata.Name, d.Spec.Config = o.Name, configOf(o)
	return d
}

func ruleDocs(o, old *v1.AddonObject, others []*v1.AddonObject) (map[string]json.RawMessage, error) {
	const op = "services.addon.ruleDocs"
	list := make([]ruleDoc, len(others))
	for i, x := range others {
		list[i] = docOf(x)
	}
	object, err := json.Marshal(docOf(o))
	if err != nil {
		return nil, fault.Internalf(op, "marshal object: %v", err)
	}
	rest, err := json.Marshal(list)
	if err != nil {
		return nil, fault.Internalf(op, "marshal others: %v", err)
	}
	docs := map[string]json.RawMessage{ruleObject: object, ruleOthers: rest}
	if old != nil {
		prev, err := json.Marshal(docOf(old))
		if err != nil {
			return nil, fault.Internalf(op, "marshal oldObject: %v", err)
		}
		docs[ruleOldObject] = prev
	}
	return docs, nil
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	if json.Compact(&x, a) != nil || json.Compact(&y, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(x.Bytes(), y.Bytes())
}

// settingsKindOf is the Addon and the settings kind of an object's kind, or nils when no Addon adds the kind as one.
func (r *Reconciler) settingsKindOf(ctx context.Context, kind string) (*v1.Addon, *v1.AddonSettings, error) {
	list, err := r.store.List(ctx, v1.KindAddon.GVK(), store.ListOptions{})
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.KindOf(err), "services.addon.settingsKindOf", "list Addons")
	}
	for _, item := range list.Items {
		if a, ok := item.(*v1.Addon); ok {
			if st := a.Spec.SettingsKind(kind); st != nil {
				return a, st, nil
			}
		}
	}
	return nil, nil, nil
}
