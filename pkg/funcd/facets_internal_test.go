package funcd

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// scenario: each-provider-serves-its-facet
// The providers' facets pass the check and register on one handler, as each worker's handler does, without the panic a
// ServeMux conflict between two patterns of one facet would raise (ADR-0310 Decision 4); a route of each facet reaches
// its provider's facade, not ServeMux's 404.
func TestFacets_TheProvidersServeKVBlobSQLAndSecret(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	facets := p.facets()
	require.NoError(t, local.CheckFacets(facets...))
	var h http.Handler
	require.NotPanics(t, func() { h = facetHandler(facets...) })
	for _, route := range [][2]string{{http.MethodGet, "/kv/b/k"}, {http.MethodGet, "/blob/b/k"},
		{http.MethodPost, "/sql/db/query"}, {http.MethodGet, "/secret"}} {
		rec := serveRoute(h, route[0], route[1])
		require.Contains(t, rec.Header().Get("Content-Type"), "json", "%s %s reaches its facet: %d %s", route[0], route[1],
			rec.Code, rec.Body.String())
	}
}

// scenario: a-misplaced-facet-fails-the-start
// The Executor's build checks the facets before it opens anything (ADR-0310 Decision 4): two facets on one segment
// fail it with an Invalid error naming the facet, before it makes a socket dir or a listener manager.
func TestExecutorBuild_RefusesTwoFacetsOnOneSegment(t *testing.T) {
	t.Parallel()
	e := &executor{cfg: &config{logger: slog.New(slog.DiscardHandler)}}
	err := e.build(execDeps{facets: []local.Facet{local.KVFacet(nil), local.KVFacet(nil)}})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, `facet "kv"`)
	require.Nil(t, e.sockets, "no local API manager is built")
	require.Empty(t, e.socketTmpDir, "no socket dir is made")
}

// A provider's Facet tests its own facade pointer (ADR-0310 Decision 2): with a nil facade it is the zero Facet, so its
// route answers ServeMux's 404 instead of calling the nil facade.
func TestFacet_ANilFacadeServesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		hook         facetHook
		method, path string
	}{
		{&kvProvider{}, http.MethodGet, "/kv/b/k"},
		{&blobProvider{}, http.MethodGet, "/blob/b/k"},
		{&sqlProvider{}, http.MethodPost, "/sql/db/query"},
		{&secretsProvider{}, http.MethodGet, "/secret"},
	} {
		h := facetHandler(tc.hook.Facet())
		var rec *httptest.ResponseRecorder
		require.NotPanics(t, func() { rec = serveRoute(h, tc.method, tc.path) }, "%s calls its nil facade", tc.hook.Name())
		require.Equal(t, http.StatusNotFound, rec.Code, "%s %s", tc.method, tc.path)
	}
}

// facetHandler is a worker's local API that serves facets and nothing it would need a port for.
func facetHandler(facets ...local.Facet) http.Handler {
	return local.NewHandler(local.Ref{Namespace: "default", Function: "fn"}, nil, nil, nil, nil, slog.New(slog.DiscardHandler),
		local.WithFacets(facets...))
}

// serveRoute serves method path on h, with a body the /sql routes accept.
func serveRoute(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(`{"statements": ["SELECT 1"]}`)))
	return rec
}
