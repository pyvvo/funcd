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

// pooledEndpoints serves every function warm from one pool worker, which addresses a member at
// /function/<name> (ADR-0046).
type pooledEndpoints struct{ worker string }

func (e pooledEndpoints) Upstream(_ context.Context, ref activator.FunctionRef) (string, bool, error) {
	return e.worker + "/function/" + string(ref.Name), true, nil
}

// A Route to a pooled member keeps every request under that member's /function/<name> path.
func TestRouteToPooledFunctionStaysUnderItsMember(t *testing.T) {
	var gotPath string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(worker.Close)
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: pooledEndpoints{worker: worker.URL}, Scaler: &spyScaler{}})
	require.NoError(t, err)
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{Namespace: "team", Host: "app.test", Rules: []router.CompiledRule{
		{Path: "/", Function: "api"},
	}}, {Namespace: "team", Host: "orders.test", Rules: []router.CompiledRule{
		{Path: "/orders", Function: "api"},
	}}}))
	h := dataplane.Handler(st, act, rtr, nil, nil, nil, 0, nil)
	seedNS(t, st, "team", v1.ExposureExplicit)
	seedFn(t, st, "team", "api")
	seedFn(t, st, "team", "api-admin")

	for _, tc := range []struct{ method, host, path, want string }{
		{"POST", "app.test", "/", "/function/api"},
		{"POST", "app.test", "/-admin", "/function/api/-admin"},
		{"GET", "app.test", "/x/y", "/function/api/x/y"},
		{"POST", "app.test", "/../api-admin", "/function/api/api-admin"},
		{"POST", "app.test", "/%2e%2e/api-admin", "/function/api/api-admin"},
		{"POST", "orders.test", "/orders/../api-admin", "/function/api/api-admin"},
	} {
		gotPath = ""
		resp := do(t, h, tc.method, tc.host, tc.path, "")
		require.Equal(t, http.StatusOK, resp.StatusCode, tc.path)
		require.Equal(t, tc.want, gotPath, "%s %s must reach the routed member only", tc.method, tc.path)
	}
}
