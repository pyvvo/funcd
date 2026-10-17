package dataplane_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
)

// ADR-0240 Decision 6: on a host that names a namespace, /function/<name> is served by name in that namespace,
// whatever X-Funcd-Namespace says; elsewhere the header picks it, default when absent.
func TestNamespaceHost_FunctionByNameFollowsTheHost(t *testing.T) {
	warm := map[v1.ObjectName]string{"fleet-api": "u"}
	h, st, _, gotPath := frontDoor(t, warm, nil)
	seedFn(t, st, "staging", "fleet-api")

	for _, c := range []struct {
		host, header string
		want         int
	}{
		{"staging.localhost", "", http.StatusOK},
		{"api.staging.localhost:8081", "prod", http.StatusOK},
		{"localhost", "", http.StatusNotFound},
		{"localhost", "staging", http.StatusOK},
		{"prod.localhost", "staging", http.StatusNotFound},
		{"fleet.localhost", "default", http.StatusNotFound},
		{"example.com", "staging", http.StatusOK},
	} {
		resp := do(t, h, http.MethodPost, c.host, "/function/fleet-api", c.header)
		require.Equal(t, c.want, resp.StatusCode, "host %q header %q", c.host, c.header)
	}
	require.Equal(t, "/", *gotPath)
}

// Another namespace's edge match is ignored for /function on a namespace host; the namespace's own match is served.
func TestNamespaceHost_OtherNamespaceMatchIgnoredUnderFunction(t *testing.T) {
	warm := map[v1.ObjectName]string{"fleet-api": "u", "prod-root": "u", "own": "u"}
	h, st, _, gotPath := frontDoor(t, warm, []router.Entry{
		{Namespace: "prod", Host: "staging.localhost", Rules: []router.CompiledRule{{Path: "/", Function: "prod-root"}}},
		{Namespace: "staging", Host: "own.staging.localhost", Rules: []router.CompiledRule{{Path: "/function/x", Function: "own"}}},
	})
	seedFn(t, st, "staging", "fleet-api")
	seedFn(t, st, "staging", "own")
	seedFn(t, st, "prod", "prod-root")

	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "staging.localhost", "/", "").StatusCode)
	require.Equal(t, "/", *gotPath, "prod's / serves the paths staging does not own")
	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "staging.localhost", "/page", "").StatusCode)
	require.Equal(t, "/page", *gotPath)

	require.Equal(t, http.StatusOK, do(t, h, http.MethodPost, "staging.localhost", "/function/fleet-api/a", "prod").StatusCode)
	require.Equal(t, "/a", *gotPath, "staging's Function by name, not prod's / Route")

	require.Equal(t, http.StatusOK, do(t, h, http.MethodPost, "own.staging.localhost", "/function/x/y", "").StatusCode)
	require.Equal(t, "/y", *gotPath, "staging's own Route under /function is served")
}

// An explicit namespace's /function/<name> on its host is 404 without a wake; an internal call keeps the header.
func TestNamespaceHost_ExplicitAndInternal(t *testing.T) {
	h, st, scaler, _ := frontDoor(t, nil, nil)
	seedNS(t, st, "staging", v1.ExposureExplicit)
	seedFn(t, st, "staging", "fleet-api")
	seedFn(t, st, "prod", "fleet-api")

	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodPost, "staging.localhost", "/function/fleet-api", "prod").StatusCode)
	require.Equal(t, 0, scaler.count(), "no wake")

	req := httptest.NewRequest(http.MethodPost, "http://staging.localhost/function/fleet-api", nil)
	req.Host = "staging.localhost"
	req.Header.Set("X-Funcd-Namespace", "prod")
	req = req.WithContext(dataplane.WithInternal(req.Context()))
	h.ServeHTTP(httptest.NewRecorder(), req)
	require.Equal(t, 1, scaler.count(), "the internal call is addressed by its header to prod's cold Function, which it wakes")
}
