package function_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	cataloggw "github.com/pyvvo/funcd/internal/catalog/gateway"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
)

// storeReadyCatalog stores a Ready CatalogService lake whose status.endpoint is the URL of a previous run's proxy.
func storeReadyCatalog(t *testing.T, st store.Store) *v1.CatalogService {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindCatalogService)
	require.True(t, ok)
	cs := obj.(*v1.CatalogService)
	cs.Name, cs.Namespace, cs.ResourceGroup = "lake", "default", "rg1"
	cs.Spec.Blob = []v1.FunctionBlob{{Alias: "lakehouse", Bucket: "lakehouse", Prefix: "gold"}}
	cs.Spec.Catalog = v1.CatalogRef{Bucket: "lakehouse", Prefix: "gold"}
	cs.Status.Phase = v1.PhaseReady
	cs.Status.Endpoint = "127.0.0.1:1"
	_, err := st.Create(context.Background(), cs)
	require.NoError(t, err)
	return cs
}

func bindLake(fn *v1.Function) {
	fn.Spec.Catalogs = []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}}
}

// TestIssue662_ConsumerWaitsForTheLiveCatalogProxy: a restarted daemon binds no catalog listener until the
// CatalogService reconciler's first pass, yet the stored CatalogService is still Ready with the old proxy's URL. A
// consumer pass that runs first must not start a worker with that URL; once a listener is bound, the worker gets its
// URL (ADR-0162 Decision 3).
func TestIssue662_ConsumerWaitsForTheLiveCatalogProxy(t *testing.T) {
	t.Parallel()
	proxies := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, nil), nil, nil)
	t.Cleanup(proxies.Shutdown)
	h := newShimHarness(t, http.StatusOK, false, func(d *function.Deps) { d.CatalogProxies = proxies })
	cs := storeReadyCatalog(t, h.st)
	h.create(t, "reader", bindLake)

	h.reconcile(t, "reader")
	require.Zero(t, h.rt.creates, "no worker starts with the URL of a proxy this daemon does not run")
	require.Equal(t, "CatalogNotReady", h.condition(t, "reader", "Ready").Reason)

	url, err := proxies.Ensure(auth.EntityRef{Type: v1.KindCatalogService, Namespace: "default", Name: "lake"}, "http://127.0.0.1:9", "engine-token")
	require.NoError(t, err)
	require.NotEqual(t, cs.Status.Endpoint, url)
	h.reconcile(t, "reader")
	require.Equal(t, 1, h.rt.creates)
	spec := h.rt.specOf(runtime.NewInstanceID("default", "reader", v1.ObjectName(h.getFn(t, "reader").Status.CurrentRevision), 0))
	require.Equal(t, url, spec.Env["FUNCD_CATALOG_LAKE_URL"], "the worker gets the live listener's URL, not the stored endpoint")
}

// TestResolveCatalogEnvInjectsBoundListener: with CatalogProxies set a consumer is injected the listener bound in this
// run, even one that answers 503 until the engine is targeted; without it, the catalog's status.endpoint.
func TestResolveCatalogEnvInjectsBoundListener(t *testing.T) {
	t.Parallel()
	proxies := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, nil), nil, nil)
	t.Cleanup(proxies.Shutdown)
	_, _, err := proxies.Listen("default", "lake", 0)
	require.NoError(t, err)
	bound, ok := proxies.ProxyURL("default", "lake")
	require.True(t, ok)

	for name, tc := range map[string]struct {
		opts []func(*function.Deps)
		want string
	}{
		"bound-listener": {opts: []func(*function.Deps){func(d *function.Deps) { d.CatalogProxies = proxies }}, want: bound},
		"no-manager":     {want: "127.0.0.1:1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, tc.opts...)
			storeReadyCatalog(t, h.st)
			h.create(t, "reader", bindLake)
			h.reconcile(t, "reader")
			spec := h.rt.specOf(runtime.NewInstanceID("default", "reader", v1.ObjectName(h.getFn(t, "reader").Status.CurrentRevision), 0))
			require.Equal(t, tc.want, spec.Env["FUNCD_CATALOG_LAKE_URL"])
		})
	}
}

// catalogGets counts the CatalogService Gets made through a store.
type catalogGets struct {
	store.Store
	n atomic.Int32
}

func (s *catalogGets) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if gvk == v1.KindCatalogService.GVK() {
		s.n.Add(1)
	}
	return s.Store.Get(ctx, gvk, ns, name)
}

// TestSteadyStateChecksBoundCatalogs: a steady pass reads no catalog without spec.catalogs and one per binding with
// them, writes nothing, and turns into the full pass, which shows CatalogNotReady, once the bound catalog is not Ready.
func TestSteadyStateChecksBoundCatalogs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newCatalogWorld(t)
	gets := &catalogGets{Store: w.st}
	w.st = gets
	r := w.run(t, false)
	readyPair(t, r)
	w.applyFunction(t, "plain", false)
	r.fnPassStored(t, "plain")

	steady := func(name string, wantGets int32) {
		t.Helper()
		rv := w.function(t, name).ResourceVersion
		gets.n.Store(0)
		res, err := r.fn.Reconcile(ctx, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
		require.NoError(t, err)
		require.Equal(t, testPeriod, res.RequeueAfter)
		require.Equal(t, wantGets, gets.n.Load())
		require.Equal(t, rv, w.function(t, name).ResourceVersion, "a steady pass writes nothing")
	}
	steady("plain", 0)
	steady("reader", 1)

	cs := w.catalog(t)
	cs.Status.Phase = v1.PhasePending
	_, err := w.st.Update(ctx, cs)
	require.NoError(t, err)
	_, err = r.fn.Reconcile(ctx, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "reader"})
	require.NoError(t, err)
	rr, ok := w.function(t, "reader").Status.Conditions.Get("RevisionReady")
	require.True(t, ok)
	require.Equal(t, "CatalogNotReady", rr.Reason, "a not-Ready bound catalog takes the full pass")

	require.NoError(t, w.st.Delete(ctx, v1.KindCatalogService.GVK(), "default", "lake", ""))
	_, err = r.fn.Reconcile(ctx, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "reader"})
	require.NoError(t, err)
	rr, ok = w.function(t, "reader").Status.Conditions.Get("RevisionReady")
	require.True(t, ok)
	require.Equal(t, "CatalogNotReady", rr.Reason, "a missing bound catalog shows CatalogNotReady")
}

// TestMapCatalogServiceMapsConsumers: a CatalogService event maps to the Functions of its namespace that bind it.
func TestMapCatalogServiceMapsConsumers(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false)
	cs := storeReadyCatalog(t, h.st)
	h.create(t, "reader", bindLake)
	h.create(t, "other", func(fn *v1.Function) {
		fn.Spec.Catalogs = []v1.FunctionCatalog{{Alias: "sea", Catalog: "sea"}}
	})
	h.createFn(t, "plain")

	require.Equal(t, []controller.Request{{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "reader"}},
		h.r.MapCatalogService(context.Background(), cs))
}

// ADR-0162 Scope, the pooled gap: a pooled consumer whose catalog goes not Ready after it served stops serving. The
// gate fails with CatalogNotReady, and gateFailed counts only workers named after the Function, so the pool worker,
// though it runs and the member's /health/members entry reads ready, is not counted: the member turns Pending with
// Ready=False and no replica, and is not handed out. It pins today's behavior; #690 inverts it.
func TestPooledConsumerStopsServingOnCatalogNotReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	proxies := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, nil), nil, nil)
	t.Cleanup(proxies.Shutdown)
	_, _, err := proxies.Listen("default", "lake", 0)
	require.NoError(t, err)
	h := newShimHarness(t, http.StatusOK, false, withPeriod, withNodePool, func(d *function.Deps) { d.CatalogProxies = proxies })
	storeReadyCatalog(t, h.st)
	h.create(t, "member", func(fn *v1.Function) {
		bindLake(fn)
		fn.Spec.Pooling.Worker = "w1"
	})
	h.reconcile(t, "member")
	fn := h.getFn(t, "member")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, 1, fn.Status.Replicas)
	_, ready := h.upstream(t, "member")
	require.True(t, ready)

	obj, err := h.st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	cs := obj.(*v1.CatalogService)
	cs.Status.Phase = v1.PhasePending
	_, err = h.st.Update(ctx, cs)
	require.NoError(t, err)
	h.reconcile(t, "member")

	h.requireNotServing(t, "member", v1.PhasePending, "CatalogNotReady")
	_, ready = h.upstream(t, "member")
	require.False(t, ready, "the member is not handed out")
	insts, err := h.rt.List(ctx, "default")
	require.NoError(t, err)
	require.Len(t, insts, 1)
	require.Empty(t, insts[0].Revision, "the one worker is the pool worker")
	require.Equal(t, runtime.StateRunning, insts[0].State, "the pool worker still runs")
	require.True(t, insts[0].Listened)
}
