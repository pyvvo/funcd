//go:build e2e

package funcd_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/bus/nats"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// scenario: explicit-exposure-gates-unrouted + serves-routed (e2e, F79/ADR-0110) — a real funcd
// with the Route reconciler wired into the controller and the data-plane front door reading the
// programmed edge router. Proves the WIRING end to end: apply Namespace(explicit)+Function+Route,
// the Route reaches Ready (reconciler → router), and the live data-plane listener gates by exposure.
// No artifact is deployed — the Route reconciler only requires the backend Function to EXIST, and
// the gating decision (404 vs resolve) needs no running function.
func TestScenarioE2ERouteExposureGating(t *testing.T) {
	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	ctx := context.Background()

	// Seed the resources directly (the cluster-scoped Namespace is off-limits to dev-auth over the
	// SDK; the controller reconciles pre-existing objects on startup).
	st := store.New(memory.New())
	seed(t, st, &v1.Namespace{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace},
		ObjectMeta: v1.ObjectMeta{Name: "team"},
		Spec:       v1.NamespaceSpec{DefaultExposure: v1.ExposureExplicit},
	})
	fn := &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "orders-fn", Namespace: "team", ResourceGroup: "rg1"},
	}
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
	seed(t, st, fn)
	seed(t, st, &v1.Route{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute},
		ObjectMeta: v1.ObjectMeta{Name: "orders", Namespace: "team", ResourceGroup: "rg1"},
		Spec: v1.RouteSpec{Host: "team.example.com", Rules: []v1.RouteRule{
			{Path: "/orders", Backend: v1.RouteBackend{Function: "orders-fn"}},
		}},
	})

	p, err := funcd.New(
		funcd.WithBlob(bucket), funcd.WithBus(messaging),
		funcd.WithStore(st), funcd.WithRuntime(process.New()),
		funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"), funcd.WithDevAuth(funcd.DevToken, "default"),
		funcd.WithArtifactStore(t.TempDir()),
	)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })

	// The Route reaches Ready → the reconciler ran (in the controller) and programmed the edge
	// router. Poll the store directly (the data plane is unauthenticated; dev-auth is default-scoped).
	require.Eventually(t, func() bool {
		obj, err := st.Get(ctx, v1.KindRoute.GVK(), "team", "orders")
		if err != nil {
			return false
		}
		cnd, ok := obj.(*v1.Route).Status.Conditions.Get("Ready")
		return ok && cnd.Status == v1.ConditionTrue
	}, 15*time.Second, 100*time.Millisecond, "route programmed")

	base := "http://" + p.DataPlaneAddr()

	// Unrouted path in the explicit namespace → 404 (default-deny ingress).
	resp, err := http.Get(base + "/function/orders-fn")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "unrouted explicit request is gated")

	// Routed host/path → the router RESOLVES (forwards to the activator). The function isn't
	// deployed, so it is NOT 200 — but it must NOT be the 404 the unrouted path gets, proving the
	// route matched and the request reached the serving path.
	req, _ := http.NewRequest("GET", base+"/orders", nil)
	req.Host = "team.example.com"
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp2.Body.Close()
	require.NotEqual(t, http.StatusNotFound, resp2.StatusCode, "a matched route resolves past the exposure gate")
}

func seed(t *testing.T, st store.Store, obj v1.Object) {
	t.Helper()
	_, err := st.Create(context.Background(), obj)
	require.NoError(t, err)
}
