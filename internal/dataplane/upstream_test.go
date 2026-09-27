package dataplane_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// TestScenarioDataPlaneServesUpstreamBackend covers ADR-0138: a matched Upstream (node-private
// reverse-proxy) edge entry is reverse-proxied to the in-daemon target — the catalog::query PEP proxy
// path — with the matched prefix stripped so the upstream is addressed at its own root, and NO
// activator hop / Function resolve.
func TestScenarioDataPlaneServesUpstreamBackend(t *testing.T) {
	t.Parallel()
	// the "PEP proxy" stub records the path it was addressed at and answers 403 (its fail-closed).
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "denied")
	}))
	t.Cleanup(up.Close)

	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default",
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: "/catalog/lake", Upstream: up.URL}},
	}}))

	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, rtr, nil, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog/lake/db", nil))

	require.Equal(t, http.StatusForbidden, rec.Code, "the upstream's own response (fail-closed 403) is proxied back verbatim")
	require.Equal(t, "/db", gotPath, "the matched prefix /catalog/lake is stripped — the upstream is addressed at its root")
}

// TestScenarioDataPlaneUpstreamUnreachable covers the 502 path: a programmed Upstream that can't be
// dialed yields a fault (not a panic), so a rebinding proxy degrades gracefully.
func TestScenarioDataPlaneUpstreamUnreachable(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default",
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: "/catalog/lake", Upstream: "http://127.0.0.1:1"}}, // nothing listens
	}}))
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, rtr, nil, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog/lake", nil))
	require.GreaterOrEqual(t, rec.Code, 500, "an unreachable upstream is a 5xx, not a panic")
}
