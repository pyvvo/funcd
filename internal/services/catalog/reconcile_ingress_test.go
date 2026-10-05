package catalog_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	cataloggw "github.com/pyvvo/funcd/internal/catalog/gateway"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/provider"
	catalogsvc "github.com/pyvvo/funcd/internal/services/catalog"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
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

func (r *recordingRoutes) Set(_ context.Context, source string, entries []router.Entry) ([]router.Verdict, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(entries) == 0 {
		delete(r.bySource, source)
		return nil, nil
	}
	r.bySource[source] = entries
	verdicts := make([]router.Verdict, len(entries))
	for i, e := range entries {
		verdicts[i] = router.Verdict{Owner: e.Owner}
	}
	return verdicts, nil
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
	require.Equal(t, router.Owner{Kind: v1.KindCatalogService, Namespace: "default", Name: "lake"}, got[0].Owner)
	require.Equal(t, v1.ConditionTrue, ingressReady(t, st).Status, "the programmed entry reports IngressReady=True")

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
	obj, err := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	_, has := obj.(*v1.CatalogService).Status.Conditions.Get("IngressReady")
	require.False(t, has, "no spec.ingress, no IngressReady")
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

// TestIssue103_NotReadyRetractsEdgeEntry covers scenario: catalog-external-teardown (not-Ready half) — an
// exposed Ready catalog that goes not-Ready on a binding (Bucket deleted → BucketNotFound, Secret gone →
// BindingResolveFailed) retracts its edge entry, like the post-Converge not-Ready branch does.
func TestIssue103_NotReadyRetractsEdgeEntry(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		unbind func(t *testing.T, st store.Store, secrets *fakeSecrets)
	}{
		{
			name:   "bucket-deleted",
			reason: "BucketNotFound",
			unbind: func(t *testing.T, st store.Store, _ *fakeSecrets) {
				require.NoError(t, st.Delete(context.Background(), v1.KindBucket.GVK(), "default", "lakehouse", ""))
			},
		},
		{
			name:   "secret-deleted",
			reason: "BindingResolveFailed",
			unbind: func(_ *testing.T, _ store.Store, secrets *fakeSecrets) {
				secrets.err = errors.New(`secret "quack" not found`)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := store.New(storemem.New())
			seedCatalogBucket(t, st)
			routes := newRecordingRoutes()
			secrets := &fakeSecrets{env: map[string]string{"QUACK_TOKEN": "change-me"}}
			mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, st), nil, nil)
			t.Cleanup(mgr.Shutdown)
			prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
			r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) {
				d.Secrets = secrets
				d.Proxy = mgr
				d.Routes = routes
			})

			cs := mkCatalogService("lake")
			cs.Spec.Ingress = &v1.CatalogIngress{PathPrefix: "/catalog/lake"}
			cs.Spec.Secrets = []v1.ObjectName{"quack"}
			_, err := st.Create(ctx, cs)
			require.NoError(t, err)
			reconcileLake(t, r)
			require.Len(t, routes.get(lakeRouteSource), 1, "exposed + Ready ⇒ edge entry present")

			tc.unbind(t, st, secrets)
			reconcileLake(t, r)

			obj, err := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
			require.NoError(t, err)
			cond, ok := obj.(*v1.CatalogService).Status.Conditions.Get("Ready")
			require.True(t, ok)
			require.Equal(t, v1.ConditionFalse, cond.Status)
			require.Equal(t, tc.reason, cond.Reason)
			require.Empty(t, routes.get(lakeRouteSource), "a not-Ready catalog's edge entry is retracted")
			ing := ingressReady(t, st)
			require.Equal(t, v1.ConditionFalse, ing.Status)
			require.Equal(t, "CatalogNotReady", ing.Reason)
		})
	}
}

func ingressReady(t *testing.T, st store.Store) v1.Condition {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	c, ok := obj.(*v1.CatalogService).Status.Conditions.Get("IngressReady")
	require.True(t, ok, "IngressReady is set")
	return c
}
