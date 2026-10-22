package controlplane_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

const bindingsMeta = `"metadata":{"name":"x","namespace":"team-a","resourceGroup":"rg1"}`

// TestScenario_binding_kind_refused covers scenario: binding-kind-refused (ADR-0246) — a kind accepts only the
// bindings something reads: a step's bindings.links and a Lake's bindings.kv or bindings.sql are refused at
// apply, naming the field.
func TestScenario_binding_kind_refused(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	for name, c := range map[string]struct{ plural, body, field string }{
		"step links": {"workflows", `{"apiVersion":"funcd.io/v1alpha1","kind":"Workflow",` + bindingsMeta +
			`,"spec":{"steps":[{"name":"s","function":{"image":"oci:s","bindings":{"links":[{"alias":"a","target":"b"}]}}}]}}`, "links"},
		"lake kv": {"lakes", `{"apiVersion":"funcd.io/v1alpha1","kind":"Lake",` + bindingsMeta +
			`,"spec":{"location":{"bucket":"b","prefix":"p"},"bindings":{"blob":[],"kv":[{"alias":"a","store":"s","table":"t"}]}}}`, "kv"},
		"lake sql": {"lakes", `{"apiVersion":"funcd.io/v1alpha1","kind":"Lake",` + bindingsMeta +
			`,"spec":{"location":{"bucket":"b","prefix":"p"},"bindings":{"blob":[],"sql":[{"alias":"a","database":"d"}]}}}`, "sql"},
	} {
		rec := do(t, srv, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/team-a/"+c.plural, devToken, []byte(c.body))
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", name, rec.Body.String())
		require.Contains(t, rec.Body.String(), c.field, name)
	}
}

// TestScenario_old_field_refused covers scenario: old-field-refused (ADR-0246) — every binding field moved under
// spec.bindings is refused at its old place by the closed schema, naming the field: a Function's seven, an image
// step's six and a standalone Lake's three, and a Lake's bindings.lake and bindings.links.
func TestScenario_old_field_refused(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	old := map[string]string{
		"kv":      `"kv":[{"alias":"c","store":"counters","table":"counts"}]`,
		"blob":    `"blob":[{"alias":"f","bucket":"files","prefix":"p"}]`,
		"sql":     `"sql":[{"alias":"db","database":"notes"}]`,
		"lakes":   `"lakes":[{"alias":"lake","lake":"lake"}]`,
		"config":  `"config":["settings"]`,
		"secrets": `"secrets":["api-keys"]`,
	}
	function := func(field string) string {
		return `{"apiVersion":"funcd.io/v1alpha1","kind":"Function",` + bindingsMeta +
			`,"spec":{"runtime":"nodejs22","handler":"handle","image":"oci:x",` + field + `}}`
	}
	step := func(field string) string {
		return `{"apiVersion":"funcd.io/v1alpha1","kind":"Workflow",` + bindingsMeta +
			`,"spec":{"steps":[{"name":"s","function":{"image":"oci:s",` + field + `}}]}}`
	}
	lake := func(field string) string {
		return `{"apiVersion":"funcd.io/v1alpha1","kind":"Lake",` + bindingsMeta +
			`,"spec":{"location":{"bucket":"b","prefix":"p"},"bindings":{"blob":[]},` + field + `}}`
	}
	type shape struct{ plural, body, field string }
	shapes := map[string]shape{
		"function links": {"functions", function(`"links":[{"alias":"greeter","target":"greeter"}]`), "links"},
		"lake bindings.lake": {"lakes", `{"apiVersion":"funcd.io/v1alpha1","kind":"Lake",` + bindingsMeta +
			`,"spec":{"location":{"bucket":"b","prefix":"p"},"bindings":{"blob":[],"lake":[{"alias":"l","lake":"l"}]}}}`, "lake"},
		"lake bindings.links": {"lakes", `{"apiVersion":"funcd.io/v1alpha1","kind":"Lake",` + bindingsMeta +
			`,"spec":{"location":{"bucket":"b","prefix":"p"},"bindings":{"blob":[],"links":[{"alias":"a","target":"b"}]}}}`, "links"},
	}
	for field, body := range old {
		shapes["function "+field] = shape{"functions", function(body), field}
		shapes["step "+field] = shape{"workflows", step(body), field}
	}
	for _, field := range []string{"blob", "config", "secrets"} {
		shapes["lake "+field] = shape{"lakes", lake(old[field]), field}
	}
	for name, c := range shapes {
		rec := do(t, srv, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/team-a/"+c.plural, devToken, []byte(c.body))
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", name, rec.Body.String())
		require.Contains(t, rec.Body.String(), c.field, name)
	}
}
