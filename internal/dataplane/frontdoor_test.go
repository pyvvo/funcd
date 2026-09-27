package dataplane_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// readyEndpoints marks the named functions warm (returning the upstream); any other function is
// cold (not ready) — so a request that reaches the activator for a cold function attempts a wake.
type readyEndpoints struct {
	warm     map[v1.ObjectName]string
	upstream string
}

func (e readyEndpoints) Upstream(_ context.Context, ref activator.FunctionRef) (string, bool, error) {
	if _, ok := e.warm[ref.Name]; ok {
		return e.upstream, true, nil
	}
	return "", false, nil
}

// spyScaler records wake attempts and fails them — a wake means gating did NOT stop the request.
type spyScaler struct {
	mu    sync.Mutex
	calls int
}

func (s *spyScaler) ScaleTo(context.Context, activator.FunctionRef, int) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return fault.Internalf("spyScaler", "wake attempted — the request should have been gated")
}

func (s *spyScaler) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

func seedNS(t *testing.T, st store.Store, name string, mode v1.ExposureMode) {
	t.Helper()
	n := &v1.Namespace{}
	n.TypeMeta = v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace}
	n.Name = v1.ObjectName(name)
	n.Spec.DefaultExposure = mode
	_, err := st.Create(context.Background(), n)
	require.NoError(t, err)
}

func seedFn(t *testing.T, st store.Store, ns, name string) {
	t.Helper()
	fn := &v1.Function{}
	fn.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), v1.NamespaceName(ns), "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
	_, err := st.Create(context.Background(), fn)
	require.NoError(t, err)
}

// frontDoor builds a data-plane handler whose router is programmed with `entries`, an activator
// over `warm` endpoints (others cold), and a spy scaler; returns the handler + store + spy + the
// upstream's last-seen path recorder.
func frontDoor(t *testing.T, warm map[v1.ObjectName]string, entries []router.Entry) (http.Handler, store.Store, *spyScaler, *string) {
	t.Helper()
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(up.Close)
	st := store.New(memory.New())
	scaler := &spyScaler{}
	act, err := activator.New(activator.Deps{Store: st, Endpoints: readyEndpoints{warm: warm, upstream: up.URL}, Scaler: scaler})
	require.NoError(t, err)
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), entries))
	return dataplane.Handler(st, act, rtr, nil, nil, nil), st, scaler, &gotPath
}

func do(t *testing.T, h http.Handler, method, host, path string, nsHeader string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	req.Host = host
	if nsHeader != "" {
		req.Header.Set("X-Funcd-Namespace", nsHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// scenario: explicit-exposure-gates-unrouted — 404 with ZERO activator wake.
func TestScenarioExplicitGatesUnroutedNoWake(t *testing.T) {
	h, st, scaler, _ := frontDoor(t, nil, nil) // no warm fns, no routes
	seedNS(t, st, "team", v1.ExposureExplicit)
	seedFn(t, st, "team", "secret") // cold, exists, but NOT routed

	resp := do(t, h, "GET", "team.example.com", "/function/secret", "team")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, 0, scaler.count(), "an unrouted explicit request must not wake the sandbox")
}

// scenario: explicit-exposure-serves-routed — a matching Route reaches the (warm) function.
func TestScenarioExplicitServesRouted(t *testing.T) {
	warm := map[v1.ObjectName]string{"orders-fn": "u"}
	entries := []router.Entry{{Namespace: "team", Host: "team.example.com", Rules: []router.CompiledRule{
		{Path: "/orders", Function: "orders-fn"},
	}}}
	h, st, _, gotPath := frontDoor(t, warm, entries)
	seedNS(t, st, "team", v1.ExposureExplicit)
	seedFn(t, st, "team", "orders-fn")

	resp := do(t, h, "GET", "team.example.com", "/orders/42", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/42", *gotPath, "solo function is addressed at the prefix-stripped remainder")
}

// scenario: implicit-exposure-backcompat — /function/<name> still serves with no Route.
func TestScenarioImplicitBackcompat(t *testing.T) {
	warm := map[v1.ObjectName]string{"echo": "u"}
	h, st, _, gotPath := frontDoor(t, warm, nil)
	seedNS(t, st, "default", v1.ExposureImplicit)
	seedFn(t, st, "default", "echo")

	resp := do(t, h, "POST", "any", "/function/echo", "default")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/", *gotPath)
}

// scenario: absent-exposure-serves-by-name — a Spec-less namespace serves by name.
func TestScenarioAbsentExposureServesByName(t *testing.T) {
	warm := map[v1.ObjectName]string{"echo": "u"}
	h, st, _, _ := frontDoor(t, warm, nil)
	// NO Namespace object at all → mode resolves to implicit.
	seedFn(t, st, "default", "echo")

	resp := do(t, h, "GET", "any", "/function/echo", "default")
	require.Equal(t, http.StatusOK, resp.StatusCode, "absent defaultExposure normalizes to implicit")
}

// scenario: internal-invoke-unaffected — an internal (fn-to-fn) request is NEVER gated by exposure,
// even for an unrouted function in an explicit namespace (it is addressed by name).
func TestScenarioInternalInvokeUnaffected(t *testing.T) {
	warm := map[v1.ObjectName]string{"worker": "u"}
	h, st, _, gotPath := frontDoor(t, warm, nil) // no routes
	seedNS(t, st, "team", v1.ExposureExplicit)
	seedFn(t, st, "team", "worker")

	// A public request would be 404ed (explicit + unrouted); an internal-marked one is served.
	req := httptest.NewRequest("POST", "http://any/function/worker", nil)
	req.Header.Set("X-Funcd-Namespace", "team")
	req = req.WithContext(dataplane.WithInternal(req.Context()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Result().StatusCode, "internal fn-to-fn invoke is never gated by exposure")
	require.Equal(t, "/", *gotPath)

	// Same request WITHOUT the internal marker (a public request) is gated → 404.
	pub := httptest.NewRequest("POST", "http://any/function/worker", nil)
	pub.Header.Set("X-Funcd-Namespace", "team")
	pubRec := httptest.NewRecorder()
	h.ServeHTTP(pubRec, pub)
	require.Equal(t, http.StatusNotFound, pubRec.Result().StatusCode, "the same request from the public listener IS gated")
}

// scenario: solo-route-strips-prefix already asserted in TestScenarioExplicitServesRouted (/orders/42 → /42).
