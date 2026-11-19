package addon

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/expr"
)

// configDoc is the document an Addon's env expressions read: the object's spec.config (ADR-0332 Decision 4).
const configDoc = "config"

// restoreDoc is the document restore.env reads besides config, and restoreSchema its type: restore.file, the copy's
// path in the engine (ADR-0333 Decision 1).
const (
	restoreDoc    = "restore"
	restoreSchema = `{"type":"object","properties":{"file":{"type":"string"}},"required":["file"],"additionalProperties":false}`
)

// CheckEnv checks that each env value is a literal or one ${{ }} expression over config that the Addon's config schema
// types (ADR-0332 Decision 4), when the Addon is applied. The reconciler checks and evaluates them again per object,
// against the schema of that pass.
func CheckEnv(env map[string]string, schema json.RawMessage) error {
	_, err := parseEnv(env, schemas(schema))
	return err
}

// CheckRestoreEnv checks restore.env as CheckEnv checks env, its expressions reading config and restore.file (ADR-0333
// Decision 1); nil checks nothing.
func CheckRestoreEnv(restore *v1.AddonRestore, schema json.RawMessage) error {
	if restore == nil {
		return nil
	}
	_, err := parseEnv(restore.Env, restoreSchemas(schema))
	return err
}

// RenderEnv evaluates vars over docs, each document typed by its schema in schemas: a literal as it is, an expression
// as its string, or as its JSON when it is no string. The first value that does not check or evaluate fails it.
func RenderEnv(vars map[string]string, docs, schemas map[string]json.RawMessage) (map[string]string, error) {
	const op = "services.addon.RenderEnv"
	parsed, err := parseEnv(vars, schemas)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(vars))
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		e, ok := parsed[k]
		if !ok {
			out[k] = vars[k]
			continue
		}
		raw, err := e.Eval(docs)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "env %s", k)
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			out[k] = s
		} else {
			out[k] = string(raw)
		}
	}
	return out, nil
}

// parseEnv parses and checks every value that holds an expression, by key; a literal has no entry.
func parseEnv(vars map[string]string, schemas map[string]json.RawMessage) (map[string]*expr.Expr, error) {
	const op = "services.addon.parseEnv"
	resolver := expr.NewSchemaResolver(schemas, nil, expr.StrictTypes())
	out := map[string]*expr.Expr{}
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		v := vars[k]
		if !strings.Contains(v, "${{") {
			continue
		}
		e, err := expr.Parse(v, expr.Select)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "env %s: a value is a literal or a single ${{ }} expression", k)
		}
		if err := e.Check(resolver); err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "env %s", k)
		}
		out[k] = e
	}
	return out, nil
}

// restoreSchemas types restore.env's documents: config by the Addon's schema, and restore.
func restoreSchemas(config json.RawMessage) map[string]json.RawMessage {
	s := schemas(config)
	s[restoreDoc] = json.RawMessage(restoreSchema)
	return s
}

// schemas types the config document by an Addon's schema, an empty one when it declares none, so an expression reads
// only what the schema declares.
func schemas(config json.RawMessage) map[string]json.RawMessage {
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	return map[string]json.RawMessage{configDoc: config}
}
