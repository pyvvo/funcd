package dataplane_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/activator"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
	"github.com/green-0-rabbit/funcd/internal/dataplane"
	"github.com/green-0-rabbit/funcd/internal/edge/authn"
	"github.com/green-0-rabbit/funcd/internal/edge/router"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

type creds map[string]auth.Identity

func (c creds) Lookup(_ context.Context, t string) (auth.Identity, error) {
	if id, ok := c[t]; ok {
		return id, nil
	}
	return auth.Identity{}, http.ErrNoCookie
}

// authDoor builds a data-plane handler with the F77 Enforcer wired (fake creds + real RBAC) and a
// spy scaler; the router carries `entries`.
func authDoor(t *testing.T, warm map[v1.ObjectName]string, entries []router.Entry, tokens creds) (http.Handler, store.Store, *spyScaler) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(up.Close)
	st := store.New(memory.New())
	scaler := &spyScaler{}
	act, err := activator.New(activator.Deps{Store: st, Endpoints: readyEndpoints{warm: warm, upstream: up.URL}, Scaler: scaler})
	require.NoError(t, err)
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), entries))
	enf, err := authn.New(authn.Deps{Creds: tokens, Authz: rbac.New()})
	require.NoError(t, err)
	return dataplane.Handler(st, act, rtr, enf, nil), st, scaler
}

func seedNSAuth(t *testing.T, st store.Store, name string, exposure v1.ExposureMode, mode v1.AuthMode) {
	t.Helper()
	n := &v1.Namespace{}
	n.TypeMeta = v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace}
	n.Name = v1.ObjectName(name)
	n.Spec.DefaultExposure = exposure
	n.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: mode}}
	_, err := st.Create(context.Background(), n)
	require.NoError(t, err)
}

func authReq(t *testing.T, h http.Handler, path, ns, bearer string) *http.Response {
	t.Helper()
	r := httptest.NewRequest("POST", "http://any"+path, nil)
	r.Header.Set("X-Funcd-Namespace", ns)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Result()
}

// scenario: authn-required-401-no-wake — the 401 precedes store.Get (no enumeration oracle) AND the
// activator (zero wake). Proven by requesting a NONEXISTENT function in an authenticated namespace:
// the PEP-first ordering yields 401, not the 404 a store miss would give.
func TestScenarioAuthnRequired401NoWake(t *testing.T) {
	h, st, scaler := authDoor(t, nil, nil, creds{})
	seedNSAuth(t, st, "team", v1.ExposureImplicit, v1.AuthAuthenticated)
	// NOTE: no Function seeded — the PEP must reject before store.Get.

	resp := authReq(t, h, "/function/ghost", "team", "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "401 pre-empts the 404 a missing function would give (PEP before store.Get)")
	require.Equal(t, 0, scaler.count(), "an unauthenticated request never wakes a sandbox")
}

// scenario: valid-token-invokes — a namespace-scoped token reaches the (warm) function.
func TestScenarioValidTokenServes(t *testing.T) {
	warm := map[v1.ObjectName]string{"api": "u"}
	h, st, _ := authDoor(t, warm, nil, creds{"tok": {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team"}}})
	seedNSAuth(t, st, "team", v1.ExposureImplicit, v1.AuthAuthenticated)
	seedFn(t, st, "team", "api")

	resp := authReq(t, h, "/function/api", "team", "tok")
	require.Equal(t, http.StatusOK, resp.StatusCode, "a valid namespace-scoped token is served")
}

// scenario: authed-but-unauthorized-403 — a valid token scoped elsewhere is denied, no wake.
func TestScenarioAuthedUnauthorized403(t *testing.T) {
	h, st, scaler := authDoor(t, nil, nil, creds{"tok": {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"other"}}})
	seedNSAuth(t, st, "team", v1.ExposureImplicit, v1.AuthAuthenticated)
	seedFn(t, st, "team", "api")

	resp := authReq(t, h, "/function/api", "team", "tok")
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "authenticated but not scoped to the target ns ⇒ 403")
	require.Equal(t, 0, scaler.count(), "a 403 never wakes a sandbox")
}

// scenario: route-open-overrides-namespace — a Route `open` un-gates a path in an authenticated namespace.
func TestScenarioRouteOpenOverridesNamespace(t *testing.T) {
	warm := map[v1.ObjectName]string{"pub": "u"}
	entries := []router.Entry{{Namespace: "team", Auth: v1.AuthOpen, Rules: []router.CompiledRule{{Path: "/pub", Function: "pub"}}}}
	h, st, _ := authDoor(t, warm, entries, creds{})
	seedNSAuth(t, st, "team", v1.ExposureImplicit, v1.AuthAuthenticated) // namespace default: authenticated
	seedFn(t, st, "team", "pub")

	resp := authReq(t, h, "/pub", "team", "") // no token, but the Route is open
	require.Equal(t, http.StatusOK, resp.StatusCode, "a Route open overrides the namespace authenticated default")
}

// scenario: authn-disabled-passthrough — no PEP wired + no authenticated stance ⇒ served anonymously.
func TestScenarioAuthnDisabledPassthrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(up.Close)
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: readyEndpoints{warm: map[v1.ObjectName]string{"api": "u"}, upstream: up.URL}, Scaler: &spyScaler{}})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, router.New(), nil, nil) // nil enforcer
	seedNSAuth(t, st, "team", v1.ExposureImplicit, v1.AuthOpen)       // open stance
	seedFn(t, st, "team", "api")

	resp := authReq(t, h, "/function/api", "team", "") // no bearer
	require.Equal(t, http.StatusOK, resp.StatusCode, "open stance + nil enforcer ⇒ anonymous served (back-compat)")
}

// scenario: internal-bypasses-authn — an internal fn-to-fn request is NEVER edge-authenticated, even
// for an authenticated-stance namespace (it is already authorized by the ADR-0075 invoke PEP).
func TestScenarioInternalBypassesAuthn(t *testing.T) {
	warm := map[v1.ObjectName]string{"api": "u"}
	h, st, _ := authDoor(t, warm, nil, creds{}) // enforcer wired, but NO valid tokens
	seedNSAuth(t, st, "team", v1.ExposureImplicit, v1.AuthAuthenticated)
	seedFn(t, st, "team", "api")

	// A public request with no bearer would be 401; an internal-marked one bypasses the edge PEP.
	req := httptest.NewRequest("POST", "http://any/function/api", nil)
	req.Header.Set("X-Funcd-Namespace", "team")
	req = req.WithContext(dataplane.WithInternal(req.Context()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Result().StatusCode, "internal fn-to-fn invoke bypasses the edge authn PEP")
}

// nil-Enforcer fail-closed: an authenticated stance with no PEP wired ⇒ 401 (not a silent pass).
func TestNilEnforcerFailsClosed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(up.Close)
	st := store.New(memory.New())
	scaler := &spyScaler{}
	act, err := activator.New(activator.Deps{Store: st, Endpoints: readyEndpoints{warm: map[v1.ObjectName]string{"api": "u"}, upstream: up.URL}, Scaler: scaler})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, router.New(), nil, nil) // NIL enforcer
	seedNSAuth(t, st, "team", v1.ExposureImplicit, v1.AuthAuthenticated)
	seedFn(t, st, "team", "api")

	resp := authReq(t, h, "/function/api", "team", "tok")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "authenticated stance + nil Enforcer ⇒ fail-closed 401")
}
