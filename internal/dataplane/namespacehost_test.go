package dataplane_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
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

// ADR-0241 Decision 4: on a namespace host, the namespace's own / rule (an SPA) never takes /function/…, which goes
// by name.
func TestExposure_OwnSlashRuleNeverTakesFunctionPath(t *testing.T) {
	warm := map[v1.ObjectName]string{"fleet-api": "u", "spa": "u"}
	h, st, _, gotPath := frontDoor(t, warm, []router.Entry{
		{Namespace: "staging", Host: "staging.localhost", Rules: []router.CompiledRule{{Path: "/", Function: "spa"}}},
	})
	seedFn(t, st, "staging", "fleet-api")
	seedFn(t, st, "staging", "spa")

	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "staging.localhost", "/vehicles/42", "").StatusCode)
	require.Equal(t, "/vehicles/42", *gotPath)
	require.Equal(t, http.StatusOK, do(t, h, http.MethodPost, "staging.localhost", "/function/fleet-api/x", "").StatusCode)
	require.Equal(t, "/x", *gotPath, "by name, not the SPA's / rule")
}

func seedFnIngress(t *testing.T, st store.Store, ns, name string, ing *v1.Ingress) {
	t.Helper()
	fn := &v1.Function{}
	fn.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), v1.NamespaceName(ns), "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
	fn.Spec.Ingress = ing
	_, err := st.Create(context.Background(), fn)
	require.NoError(t, err)
}

// ADR-0241 Decisions 2 and 4: enabled replaces the explicit gate; off and a custom address close the by-name form
// without a wake, while an internal call still reaches an off Function by name.
func TestExposure_ByNameFollowsEnabled(t *testing.T) {
	h, st, scaler, _ := frontDoor(t, nil, nil)
	seedNS(t, st, "ex", v1.ExposureExplicit)
	seedFnIngress(t, st, "ex", "opened", &v1.Ingress{Enabled: new(true)})
	seedFnIngress(t, st, "ex", "closed", nil)
	seedFnIngress(t, st, "default", "off", &v1.Ingress{Enabled: new(false)})
	seedFnIngress(t, st, "default", "custom", &v1.Ingress{Host: "api.example.com"})

	for _, c := range []struct{ host, path string }{
		{"ex.localhost", "/function/closed"},
		{"localhost", "/function/off"},
		{"localhost", "/function/custom"},
	} {
		require.Equal(t, http.StatusNotFound, do(t, h, http.MethodPost, c.host, c.path, "").StatusCode, c.path)
	}
	require.Equal(t, 0, scaler.count(), "a closed Function wakes nothing")

	do(t, h, http.MethodPost, "ex.localhost", "/function/opened", "")
	require.Equal(t, 1, scaler.count(), "enabled: true opens a Function in an explicit namespace")

	req := httptest.NewRequest(http.MethodPost, "http://localhost/function/off", nil)
	req = req.WithContext(dataplane.WithInternal(req.Context()))
	h.ServeHTTP(httptest.NewRecorder(), req)
	require.Equal(t, 2, scaler.count(), "another Function still calls it by name")
}

// ADR-0241 Decision 4: in an authenticated namespace, a missing or closed Function answers the PEP before 404, so an
// anonymous caller cannot tell names apart; the Function's own ingress.auth sets its stance when it is on.
func TestExposure_ByNameStance(t *testing.T) {
	h, st, scaler, _ := frontDoor(t, nil, nil)
	ns := &v1.Namespace{TypeMeta: v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace}}
	ns.Name = "secure"
	ns.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthAuthenticated}}
	_, err := st.Create(context.Background(), ns)
	require.NoError(t, err)
	seedFnIngress(t, st, "secure", "closed", &v1.Ingress{Enabled: new(false)})
	seedFnIngress(t, st, "default", "guarded", &v1.Ingress{Auth: &v1.RouteAuth{Mode: v1.AuthAuthenticated}})

	for _, path := range []string{"/function/missing", "/function/closed"} {
		require.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodPost, "secure.localhost", path, "").StatusCode, path)
	}
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodPost, "localhost", "/function/missing", "").StatusCode)
	require.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodPost, "localhost", "/function/guarded", "").StatusCode)
	require.Equal(t, 0, scaler.count())
}
