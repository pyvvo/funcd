package dataplane_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/activator"
	"github.com/green-0-rabbit/funcd/internal/dataplane"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

// fakeEndpoints returns a fixed ready upstream (the warm path).
type fakeEndpoints struct{ upstream string }

func (f fakeEndpoints) Upstream(_ context.Context, _ activator.FunctionRef) (string, bool, error) {
	return f.upstream, f.upstream != "", nil
}

// noScaler is a never-called Scaler (the warm path does not scale).
type noScaler struct{}

func (noScaler) ScaleTo(_ context.Context, _ activator.FunctionRef, _ int) error { return nil }

func seedFunction(t *testing.T, st store.Store, name string) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Image = "file:///tmp/x"
	_, err := st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func newHandler(t *testing.T, upstream string) (http.Handler, store.Store) {
	t.Helper()
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: upstream}, Scaler: noScaler{}})
	require.NoError(t, err)
	return dataplane.Handler(st, act, nil), st
}

// scenario: http-invokes-warm-function — the data-plane handler resolves /function/<name>
// to a known function and serves it through the activator to the ready upstream.
func TestScenarioDataPlaneServesWarmFunction(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/", r.URL.Path, "the function is addressed at its own root (prefix stripped)")
		_, _ = io.WriteString(w, "handled")
	}))
	t.Cleanup(up.Close)

	h, st := newHandler(t, up.URL)
	seedFunction(t, st, "echo")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/function/echo", strings.NewReader(`{}`))
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "handled", rec.Body.String(), "served through the activator to the upstream")
}

// scenario: unknown-function-404 — a path naming a non-existent function → 404 (a wake never
// targets a phantom).
func TestScenarioDataPlaneUnknownFunction404(t *testing.T) {
	t.Parallel()
	h, _ := newHandler(t, "http://unused")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/function/absent", strings.NewReader(`{}`))
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code, "unknown function → 404 problem+json")
}

// scenario: namespace-header-routes — X-Funcd-Namespace selects the namespace; a bare path
// defaults to "default".
func TestScenarioDataPlaneNamespaceHeader(t *testing.T) {
	t.Parallel()
	h, st := newHandler(t, "http://unused")
	seedFunction(t, st, "echo") // namespace "default"

	// Wrong namespace header → not found.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/function/echo", nil)
	req.Header.Set("X-Funcd-Namespace", "other")
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code, "the header selects the namespace")
}

// scenario: non-function-path-404 — a path outside /function/ is not served.
func TestScenarioDataPlaneNonFunctionPath(t *testing.T) {
	t.Parallel()
	h, _ := newHandler(t, "http://unused")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}
