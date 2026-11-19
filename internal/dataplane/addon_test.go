package dataplane_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// An add-on entry forwards the cleaned request path whole, its base path kept, and answers 404 itself to a path with a
// .. segment, outside the base path, or under a private path however it is spelled (ADR-0332 Decision 5).
func TestAddonEntryKeepsItsPathAndRefusesPrivatePaths(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []string
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(engine.Close)

	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default", Host: "login.example.com", Auth: v1.AuthOpen,
		Rules: []router.CompiledRule{{Path: "/auth/v1", Upstream: engine.URL, KeepPath: true,
			PrivatePaths: []string{"/auth/v1/clients_dyn"}}},
	}}, nil))
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, rtr, "", nil, nil, nil, 0, nil)

	call := func(method, target string) int {
		req := httptest.NewRequest(method, target, nil)
		req.Host = "login.example.com"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusTeapot, call(http.MethodGet, "/auth/v1/ping"))
	require.Equal(t, http.StatusTeapot, call(http.MethodGet, "/auth/v1//./oidc/authorize"))
	for _, p := range []string{"/auth/v1/clients_dyn", "/auth/v1//clients_dyn", "/auth/v1/./clients_dyn",
		"/auth/v1/clients_dyn/x", "/auth/v1/%2e%2e/admin", "/auth/v1/x/../clients_dyn"} {
		require.Equal(t, http.StatusNotFound, call(http.MethodPost, p), p)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"/auth/v1/ping", "/auth/v1/oidc/authorize"}, got, "the engine gets the cleaned path, kept")
}
