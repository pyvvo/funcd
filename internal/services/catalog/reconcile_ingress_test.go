package catalog_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	cataloggw "github.com/green-0-rabbit/funcd/internal/catalog/gateway"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/edge/router"
	"github.com/green-0-rabbit/funcd/internal/provider"
	catalogsvc "github.com/green-0-rabbit/funcd/internal/services/catalog"
	"github.com/green-0-rabbit/funcd/internal/store"
	storemem "github.com/green-0-rabbit/funcd/internal/store/memory"
)

// recordingRoutes is a router.EntrySetter double that holds each source's last-Set slice — the edge
// aggregator seam the catalog reconciler programs its external entry through (ADR-0138).
type recordingRoutes struct {
	mu       sync.Mutex
	bySource map[string][]router.Entry
}

func newRecordingRoutes() *recordingRoutes {
	return &recordingRoutes{bySource: make(map[string][]router.Entry)}
}

func (r *recordingRoutes) Set(_ context.Context, source string, entries []router.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(entries) == 0 {
		delete(r.bySource, source)
		return nil
	}
	r.bySource[source] = entries
	return nil
}

func (r *recordingRoutes) get(source string) []router.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bySource[source]
}

const lakeRouteSource = "catalog/default/lake"

// ingressReconciler builds a CatalogService reconciler with a Ready provider (engine at
// 10.63.0.7:8080), a real node-private PEP proxy Manager, and the given edge aggregator seam.
func ingressReconciler(t *testing.T, st store.Store, routes router.EntrySetter) *catalogsvc.Reconciler {
	t.Helper()
	mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	r, err := catalogsvc.NewReconciler(catalogsvc.ReconcilerDeps{
		Store:    st,
		Provider: &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}},
		ImageFor: func(rt string) string { return "funcd/runtime-" + rt },
		Proxy:    mgr,
		Routes:   routes,
	})
	require.NoError(t, err)
	return r
}

func reconcileLake(t *testing.T, r *catalogsvc.Reconciler) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: "lake"})
	require.NoError(t, err)
}

// TestReconcile_ExternalRouteTargetsProxy covers scenario: catalog-external-route-targets-proxy — a
// CatalogService with spec.ingress, once Ready, programs an edge entry whose Upstream is the PEP
// PROXY URL (http://<Status.Endpoint>), open-auth (the proxy PEPs), never the engine address.
func TestReconcile_ExternalRouteTargetsProxy(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st)
	routes := newRecordingRoutes()
	r := ingressReconciler(t, st, routes)

	cs := mkCatalogService("lake")
	cs.Spec.Ingress = &v1.CatalogIngress{PathPrefix: "/catalog/lake", Host: "lake.example"}
	_, err := st.Create(ctx, cs)
	require.NoError(t, err)

	reconcileLake(t, r)

	got := routes.get(lakeRouteSource)
	require.Len(t, got, 1, "an edge entry is programmed for the exposed catalog")
	require.Equal(t, v1.NamespaceName("default"), got[0].Namespace)
	require.Equal(t, v1.AuthOpen, got[0].Auth, "open at the edge — the PEP proxy authenticates the caller")
	require.Len(t, got[0].Rules, 1)
	require.Equal(t, "/catalog/lake", got[0].Rules[0].Path)
	require.Equal(t, "lake.example", got[0].Host)

	csObj, err := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	proxyEndpoint := csObj.(*v1.CatalogService).Status.Endpoint
	require.Equal(t, "http://"+proxyEndpoint, got[0].Rules[0].Upstream,
		"the entry upstream is the PEP PROXY (Status.Endpoint), NOT the engine address 10.63.0.7:8080")
	require.NotContains(t, got[0].Rules[0].Upstream, "10.63.0.7:8080", "the engine is never the edge upstream (PEP would be bypassed)")
	require.Empty(t, got[0].Rules[0].Function, "an upstream backend names no Function")
}

// TestReconcile_NotExposedNoRoute covers scenario: catalog-not-exposed-no-route — a CatalogService
// with no spec.ingress programs NO edge entry when Ready (back-compat: internal-only).
func TestReconcile_NotExposedNoRoute(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st)
	routes := newRecordingRoutes()
	r := ingressReconciler(t, st, routes)

	_, err := st.Create(ctx, mkCatalogService("lake")) // no spec.ingress
	require.NoError(t, err)

	reconcileLake(t, r)

	require.Empty(t, routes.get(lakeRouteSource), "an unexposed catalog programs no edge entry (internal-only, back-compat)")
}

// TestReconcile_ExternalTeardown covers scenario: catalog-external-teardown — deleting an exposed
// CatalogService retracts its edge entry (the aggregator source is cleared).
func TestReconcile_ExternalTeardown(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st)
	routes := newRecordingRoutes()
	r := ingressReconciler(t, st, routes)

	cs := mkCatalogService("lake")
	cs.Spec.Ingress = &v1.CatalogIngress{PathPrefix: "/catalog/lake"}
	_, err := st.Create(ctx, cs)
	require.NoError(t, err)
	reconcileLake(t, r)
	require.Len(t, routes.get(lakeRouteSource), 1, "exposed ⇒ edge entry present")

	// delete → the edge entry is retracted.
	require.NoError(t, st.Delete(ctx, v1.KindCatalogService.GVK(), "default", "lake", ""))
	reconcileLake(t, r)
	require.Empty(t, routes.get(lakeRouteSource), "a deleted catalog's edge entry is retracted")
}
