package expr

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

const strictSchema = `{"type":"object","required":["host"],"properties":{
	"host":{"type":"string"},
	"replicas":{"type":"integer","default":0},
	"flag":{"type":"boolean"},
	"mode":{"enum":["a","b"]},
	"fixed":{"const":3},
	"either":{"type":["string","null"]},
	"choice":{"oneOf":[{"type":"string"},{"type":"integer"}]},
	"linked":{"$ref":"#/$defs/x"},
	"bag":{"type":"object"},
	"labels":{"type":"object","additionalProperties":{"type":"string"}},
	"nested":{"type":"object","properties":{"deep":{"type":"string","default":"d"}}},
	"loose":{"properties":{"k":{"type":"string"}}}
}}`

// ADR-0217 Decision 4: StrictTypes types a path only from properties with one explicit type, never as "string".
func TestStrictTypesRefusesUntypedPaths(t *testing.T) {
	t.Parallel()
	r := NewSchemaResolver(map[string]json.RawMessage{"values": json.RawMessage(strictSchema)}, nil, StrictTypes())
	for _, path := range [][]string{
		{"mode"}, {"fixed"}, {"either"}, {"choice"}, {"linked"}, {"bag"}, {"labels"}, {"labels", "x"},
		{"bag", "x"}, {"loose"}, {"loose", "k"}, {"missing"}, {"host", "x"},
	} {
		f, err := r.Resolve("values", path)
		require.Equal(t, fault.NotFound, fault.KindOf(err), "%v resolved to %+v", path, f)
	}
	f, err := r.Resolve("values", []string{"host"})
	require.NoError(t, err)
	require.Equal(t, Field{Type: "string", Required: true}, f)
	f, err = r.Resolve("values", []string{"nested", "deep"})
	require.NoError(t, err)
	require.Equal(t, Field{Type: "string", HasDefault: true, Default: json.RawMessage(`"d"`)}, f)
	f, err = r.Resolve("values", nil)
	require.NoError(t, err)
	require.Equal(t, "object", f.Type)

	for _, src := range []string{"${{ values.mode }}", "${{ values.bag.x }}", "${{ values.labels.x }}", "${{ values.flag === true }}"} {
		e, err := Parse(src, Select)
		require.NoError(t, err)
		require.Error(t, e.Check(r), src)
	}
	e := mustCheck(t, "${{ values.replicas + 1 }}", Select, r)
	out, err := e.Eval(map[string]json.RawMessage{"values": json.RawMessage(`{"host":"h"}`)})
	require.NoError(t, err)
	require.JSONEq(t, `1`, string(out), "the declared default stands in for the absent leaf")
}

// Without StrictTypes the resolver keeps the workflow behavior: a path the schema is silent about types as string.
func TestSchemaResolverPermissiveByDefault(t *testing.T) {
	t.Parallel()
	r := NewSchemaResolver(map[string]json.RawMessage{"values": json.RawMessage(strictSchema)}, nil)
	f, err := r.Resolve("values", []string{"bag", "x"})
	require.NoError(t, err)
	require.Equal(t, "string", f.Type)
	_, err = r.Resolve("values", []string{"missing"})
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	f, err = r.Resolve("values", []string{"mode"})
	require.NoError(t, err)
	require.Empty(t, f.Type)
}

// ADR-0217 Contracts: Idents reports each reference's root, deduplicated in source order, before Check.
func TestIdents(t *testing.T) {
	t.Parallel()
	for src, want := range map[string][]string{
		"${{ values.a.b }}":                                          {"values"},
		"${{ values.list[0] }}":                                      {"values"},
		"${{ app.name + \"-\" + values.suffix + app.name }}":         {"app", "values"},
		"${{ values.x !== undefined && values.x > 2 }}":              {"values"},
		"${{ true }}":                                                nil,
		"${{ 'a' + \"b\" }}":                                         nil,
		"${{ step.parse.output.ok === true }}":                       {"step"},
		"${{ event.data.image }}":                                    {"event"},
		"${{ values.n.toUpperCase().startsWith(images.api) }}":       {"values", "images"},
		"${{ sum(values.nums) > 0 ? { a: app.name } : { a: 'x' } }}": {"values", "app"},
		"${{ [values.a, input.b] }}":                                 {"values", "input"},
		"${{ !values.on }}":                                          {"values"},
		"${{ undefined === values.x }}":                              {"values"},
	} {
		e, err := Parse(src, Select)
		require.NoError(t, err, src)
		require.Equal(t, want, e.Idents(), src)
	}
}
