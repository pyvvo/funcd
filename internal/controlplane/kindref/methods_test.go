package kindref

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/reference"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// scenario: method-shows-its-errors
// Under each language's KV table, put has one row for each cause of its endpoint that it surfaces, with the status,
// the problem type, when and what to do, in the order of their statuses; get has no 404 row, since its shim returns
// null for it; and the table says once that a method calling funcd can fail with 500.
func TestScenarioMethodShowsItsErrors(t *testing.T) {
	pages, err := Render(controlplane.SpecAPI().OpenAPI())
	require.NoError(t, err)
	kv := string(pages["develop/services/kv/spec.md"])
	endpoints := local.Endpoints()
	put := endpoints[slices.IndexFunc(endpoints, func(e local.Endpoint) bool { return e.Pattern == "PUT /kv/{binding}/{key...}" })]
	for _, lang := range []string{"TypeScript", "Python"} {
		table := section(t, kv, "#### "+lang)
		surfaced := slices.DeleteFunc(slices.Clone(put.Causes), func(c *fault.KnownCause) bool {
			return slices.Contains(methods[lang]["kv.put"].Except, c)
		})
		slices.SortStableFunc(surfaced, func(a, b *fault.KnownCause) int { return statusOf(a) - statusOf(b) })
		var want []string
		for _, c := range surfaced {
			p := fault.ToProblem(c.Errorf("", ""))
			want = append(want, "| `put` | "+strconv.Itoa(p.Status)+" | `"+p.Type+"` | "+reference.Cell(c.When())+" | "+
				reference.Cell(c.Fix())+" |")
		}
		require.Len(t, want, 13, "%s: put surfaces 13 of its endpoint's causes", lang)
		require.Equal(t, want, rowsOf(table, "put"), "%s: one row per cause that put surfaces", lang)
		require.NotEmpty(t, rowsOf(table, "get"), "%s: get has rows", lang)
		for _, row := range rowsOf(table, "get") {
			require.NotContains(t, row, "| 404 |", "%s: get returns null for a missing key", lang)
		}
		require.Equal(t, 1, strings.Count(table, "can also fail with the status 500 and the problem type "+
			"`urn:funcd:problem:internal`"), "%s: the 500 sentence, once", lang)
	}
	require.Contains(t, kv, "| `compareAndSet` | 400 | `urn:funcd:problem:invalid` | The version is empty",
		"a cause that only a condition's method provokes shows under it")
	require.NotContains(t, kv, "If-None-Match is sent on more than one line", "a raw request's cause shows under no method")
	for _, page := range []string{"develop/services/lake/spec.md", "develop/workloads/configuration/spec.md"} {
		require.NotContains(t, string(pages[page]), "status 500", "%s: no method there calls funcd", page)
	}
}

// rowsOf returns the error rows of method in a Handler API table.
func rowsOf(table, method string) []string {
	var rows []string
	for line := range strings.Lines(table) {
		if line = strings.TrimSuffix(line, "\n"); strings.HasPrefix(line, "| `"+method+"` | ") {
			rows = append(rows, line)
		}
	}
	return rows
}

// scenario: every-method-mapped
// Every method of both shims has an entry in the table of methods, so the Spec generation passes; a method with no
// entry, an entry that names an endpoint the local API lacks or excepts a cause its endpoint does not declare, and an
// entry for a method the shim does not have fail it, named.
func TestScenarioEveryMethodMapped(t *testing.T) {
	_, err := Render(controlplane.SpecAPI().OpenAPI())
	require.NoError(t, err, "the table maps every method of both shims")

	api := []byte(`{"clients": [{"name": "kv", "doc": "The tables.", "methods": [
		{"name": "get", "signature": "get(key: string): string", "doc": "Reads."},
		{"name": "put", "signature": "put(key: string): void", "doc": "Writes."}]}]}`)
	endpoints := []local.Endpoint{{Pattern: "GET /kv/{binding}/{key...}", Causes: []*fault.KnownCause{local.ErrKeyNotFound}}}
	read := methodEndpoint{Endpoint: "GET /kv/{binding}/{key...}", Except: []*fault.KnownCause{local.ErrKeyNotFound}}
	for name, tc := range map[string]struct {
		table map[string]methodEndpoint
		err   string
	}{
		"mapped":                 {map[string]methodEndpoint{"kv.get": read, "kv.put": {}}, ""},
		"a method with no entry": {map[string]methodEndpoint{"kv.get": read}, "the TypeScript method kv.put has no entry"},
		"an endpoint the local API lacks": {map[string]methodEndpoint{"kv.get": read,
			"kv.put": {Endpoint: "PUT /kv/{binding}/{key...}"}}, `calls "PUT /kv/{binding}/{key...}", which is no endpoint`},
		"a cause the endpoint does not declare": {map[string]methodEndpoint{"kv.put": {},
			"kv.get": {Endpoint: "GET /kv/{binding}/{key...}", Except: []*fault.KnownCause{local.ErrKeyAbsent}}},
			"kv.get excepts local.key-absent, which GET /kv/{binding}/{key...} does not declare"},
		"a method the shim does not have": {map[string]methodEndpoint{"kv.get": read, "kv.put": {}, "kv.gone": {}},
			"maps the TypeScript method kv.gone, which the shim does not have"},
	} {
		docs := handlerDocs{langs: []language{{"TypeScript", api}},
			methods: map[string]map[string]methodEndpoint{"TypeScript": tc.table}, endpoints: endpoints}
		_, err := docs.section([]string{"kv"})
		if tc.err == "" {
			require.NoError(t, err, name)
			continue
		}
		require.ErrorContains(t, err, tc.err, name)
	}
}
