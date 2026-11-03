package local

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// facetOf is a facet on segment that registers patterns.
func facetOf(segment string, patterns ...string) Facet {
	return Facet{segment: segment, register: func(mux facetMux, _ Ref, _ *slog.Logger) {
		for _, p := range patterns {
			mux.HandleFunc(p, func(http.ResponseWriter, *http.Request) {})
		}
	}}
}

// scenario: a-misplaced-facet-fails-the-start
// CheckFacets refuses a facet that could take another facet's paths or the built-in routes' (ADR-0310 Decision 4),
// with an Invalid error naming the facet and the pattern or segment, and it ignores the zero facet.
func TestCheckFacets(t *testing.T) {
	t.Parallel()
	facets := []Facet{KVFacet(nil), BlobFacet(nil), SQLFacet(nil), SecretFacet(nil)}
	require.NoError(t, CheckFacets(facets...), "each facet owns its segment")
	require.NoError(t, CheckFacets(append(facets, Facet{}, Facet{})...), "the zero facet is ignored")
	require.NoError(t, CheckFacets(facetOf("ai", "POST /ai/v1/chat/completions", "/ai/v1/models", "GET\t/ai")),
		"a pattern's method is optional")
	for _, tc := range []struct {
		name   string
		facets []Facet
		named  []string
	}{
		{"an empty segment", []Facet{facetOf("", "GET /")}, []string{`facet ""`}},
		{"a segment with /", []Facet{KVFacet(nil), facetOf("kv/sub", "GET /kv/sub/{x}")}, []string{`"kv/sub"`}},
		{"a segment with {", []Facet{facetOf("{x}", "GET /{x}/a")}, []string{`"{x}"`}},
		{"a segment with }", []Facet{facetOf("x}", "GET /x}/a")}, []string{`"x}"`}},
		{"a pattern outside its segment", []Facet{facetOf("secret", "GET /secretx/{key}")},
			[]string{`"secret"`, `"GET /secretx/{key}"`}},
		{"another facet's more specific pattern", []Facet{KVFacet(nil), facetOf("secret", "GET /secret", "GET /kv/{binding}/stats")},
			[]string{`"secret"`, `"GET /kv/{binding}/stats"`}},
		{"the root subtree", []Facet{facetOf("secret", "/")}, []string{`"secret"`, `"/"`}},
		{"a host-qualified pattern", []Facet{facetOf("kv", "GET local/kv/{binding}")}, []string{`"kv"`, `"GET local/kv/{binding}"`}},
		{"two facets on one segment", []Facet{KVFacet(nil), BlobFacet(nil), KVFacet(nil)}, []string{`facet "kv"`}},
		{"invoke", []Facet{facetOf("invoke", "POST /invoke/{alias}/x")}, []string{`facet "invoke"`}},
		{"fetch", []Facet{facetOf("fetch", "GET /fetch/{alias}")}, []string{`facet "fetch"`}},
		{"health", []Facet{facetOf("health", "GET /health/status")}, []string{`facet "health"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := CheckFacets(tc.facets...)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			for _, n := range tc.named {
				require.Contains(t, err.Error(), n)
			}
		})
	}
}
