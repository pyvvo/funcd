package route_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/route"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func setup(t *testing.T) (context.Context, store.Store, router.Router, *route.Reconciler) {
	t.Helper()
	ctx, st, rtr, _, rec := setupAggregator(t, nil)
	return ctx, st, rtr, rec
}

// setupAggregator wires the reconciler to an edge aggregator over rtr (ADR-0138) that reads namespace modes
// from st (ADR-0176); the test Resolves against rtr, the live table the aggregator Programs.
func setupAggregator(t *testing.T, notify router.NotifyFunc) (context.Context, store.Store, router.Router, *router.Aggregator, *route.Reconciler) {
	t.Helper()
	st := store.New(memory.New())
	rtr := router.New()
	agg := router.NewAggregator(rtr, func(ctx context.Context, ns v1.NamespaceName) (v1.ExposureMode, error) {
		obj, err := st.Get(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
		if err != nil {
			return "", err
		}
		return obj.(*v1.Namespace).Spec.DefaultExposure, nil
	}, notify, nil)
	rec, err := route.NewReconciler(route.Deps{Store: st, Routes: agg})
	require.NoError(t, err)
	return context.Background(), st, rtr, agg, rec
}

func seedFunction(t *testing.T, st store.Store, ns v1.NamespaceName, name string) {
	t.Helper()
	fn := &v1.Function{}
	fn.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
	_, err := st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func seedNamespace(t *testing.T, st store.Store, name string, mode v1.ExposureMode) {
	t.Helper()
	n := &v1.Namespace{}
	n.TypeMeta = v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace}
	n.Name = v1.ObjectName(name)
	n.Spec.DefaultExposure = mode
	_, err := st.Create(context.Background(), n)
	require.NoError(t, err)
}

func seedRoute(t *testing.T, st store.Store, ns v1.NamespaceName, name, host, path, fn string) {
	t.Helper()
	r := &v1.Route{}
	r.TypeMeta = v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}
	r.Name, r.Namespace, r.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	r.Spec = v1.RouteSpec{Host: host, Rules: []v1.RouteRule{{Path: path, Backend: v1.RouteBackend{Function: v1.ObjectName(fn)}}}}
	_, err := st.Create(context.Background(), r)
	require.NoError(t, err)
}

func reconcile(t *testing.T, rec *route.Reconciler, ns v1.NamespaceName, name string) {
	t.Helper()
	_, err := rec.Reconcile(context.Background(), controller.Request{GVK: v1.KindRoute.GVK(), Namespace: ns, Name: v1.ObjectName(name)})
	require.NoError(t, err)
}

func readyCond(t *testing.T, st store.Store, ns v1.NamespaceName, name string) (v1.ConditionStatus, string) {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindRoute.GVK(), ns, v1.ObjectName(name))
	require.NoError(t, err)
	c, ok := obj.(*v1.Route).Status.Conditions.Get("Ready")
	require.True(t, ok, "Ready condition set")
	return c.Status, c.Reason
}

func seedBucket(t *testing.T, st store.Store, ns v1.NamespaceName, name string) {
	t.Helper()
	b := &v1.Bucket{}
	b.TypeMeta = v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}
	b.Name, b.Namespace, b.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	_, err := st.Create(context.Background(), b)
	require.NoError(t, err)
}

func seedStaticRoute(t *testing.T, st store.Store, ns v1.NamespaceName, name, host, path, bucket string) {
	t.Helper()
	r := &v1.Route{}
	r.TypeMeta = v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}
	r.Name, r.Namespace, r.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	r.Spec = v1.RouteSpec{Host: host, Rules: []v1.RouteRule{{
		Path:    path,
		Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: v1.ObjectName(bucket), Prefix: "bi/", Index: "index.html"}},
	}}}
	_, err := st.Create(context.Background(), r)
	require.NoError(t, err)
}

// scenario: bucket-not-found-not-ready (ADR-0120, F82) — a static Route whose Bucket is missing is
// NotReady (BucketNotFound) and not programmed; once the Bucket exists it becomes Ready + programmed.
func TestScenarioBucketNotFoundNotReady(t *testing.T) {
	ctx, st, rtr, rec := setup(t)
	seedStaticRoute(t, st, "default", "bi", "", "/", "reports") // no such Bucket yet
	reconcile(t, rec, "default", "bi")

	status, reason := readyCond(t, st, "default", "bi")
	require.Equal(t, v1.ConditionFalse, status)
	require.Equal(t, "BucketNotFound", reason)
	_, ok := rtr.Resolve("any", "/", "GET")
	require.False(t, ok, "a NotReady static route is not programmed")

	// Create the Bucket → the static Route becomes Ready + programmed with its Static backend.
	seedBucket(t, st, "default", "reports")
	reconcile(t, rec, "default", "bi")
	status, _ = readyCond(t, st, "default", "bi")
	require.Equal(t, v1.ConditionTrue, status)
	m, ok := rtr.Resolve("any", "/anything", "GET") // a "/" static route is a catch-all subtree (ADR-0120)
	require.True(t, ok, "a Ready static route is programmed")
	require.NotNil(t, m.Static, "the compiled match carries the static backend")
	require.Equal(t, v1.ObjectName("reports"), m.Static.Bucket)
	_ = ctx
}

// scenario: route-reconciles-ready
func TestScenarioRouteReconcilesReady(t *testing.T) {
	ctx, st, rtr, rec := setup(t)
	seedFunction(t, st, "default", "orders-fn")
	seedRoute(t, st, "default", "orders", "", "/orders", "orders-fn")
	reconcile(t, rec, "default", "orders")

	status, _ := readyCond(t, st, "default", "orders")
	require.Equal(t, v1.ConditionTrue, status)
	m, ok := rtr.Resolve("any", "/orders", "GET")
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("orders-fn"), m.Function)
	_ = ctx
}

// scenario: route-not-ready-missing-backend
func TestScenarioRouteNotReadyMissingBackend(t *testing.T) {
	_, st, rtr, rec := setup(t)
	seedRoute(t, st, "default", "orphan", "", "/x", "ghost-fn") // no such Function
	reconcile(t, rec, "default", "orphan")

	status, reason := readyCond(t, st, "default", "orphan")
	require.Equal(t, v1.ConditionFalse, status)
	require.Equal(t, "BackendNotFound", reason)
	_, ok := rtr.Resolve("any", "/x", "GET")
	require.False(t, ok, "a NotReady route is not programmed")
}

// scenario: route-delete-unroutes
func TestScenarioRouteDeleteUnroutes(t *testing.T) {
	ctx, st, rtr, rec := setup(t)
	seedFunction(t, st, "default", "orders-fn")
	seedRoute(t, st, "default", "orders", "", "/orders", "orders-fn")
	reconcile(t, rec, "default", "orders")
	_, ok := rtr.Resolve("any", "/orders", "GET")
	require.True(t, ok)

	obj, err := st.Get(ctx, v1.KindRoute.GVK(), "default", "orders")
	require.NoError(t, err)
	require.NoError(t, st.Delete(ctx, v1.KindRoute.GVK(), "default", "orders", obj.GetObjectMeta().ResourceVersion))
	reconcile(t, rec, "default", "orders") // reconcile the (now-deleted) route
	_, ok = rtr.Resolve("any", "/orders", "GET")
	require.False(t, ok, "a deleted route is reprogrammed out of the table")
}

// scenario: cross-namespace-host-isolation — HostRequired + RouteConflict determinism
func TestScenarioCrossNamespaceHostIsolation(t *testing.T) {
	ctx, st, rtr, rec := setup(t)
	seedNamespace(t, st, "team-a", v1.ExposureExplicit)
	seedNamespace(t, st, "team-b", v1.ExposureExplicit)
	seedFunction(t, st, "team-a", "a-fn")
	seedFunction(t, st, "team-b", "b-fn")

	// Same path, DIFFERENT hosts → both Ready, isolated.
	seedRoute(t, st, "team-a", "r", "a.example.com", "/orders", "a-fn")
	seedRoute(t, st, "team-b", "r", "b.example.com", "/orders", "b-fn")
	reconcile(t, rec, "team-a", "r")
	reconcile(t, rec, "team-b", "r")
	sa, _ := readyCond(t, st, "team-a", "r")
	sb, _ := readyCond(t, st, "team-b", "r")
	require.Equal(t, v1.ConditionTrue, sa)
	require.Equal(t, v1.ConditionTrue, sb)
	ma, _ := rtr.Resolve("a.example.com", "/orders", "GET")
	require.Equal(t, v1.NamespaceName("team-a"), ma.Namespace)
	mb, _ := rtr.Resolve("b.example.com", "/orders", "GET")
	require.Equal(t, v1.NamespaceName("team-b"), mb.Namespace, "namespaces cannot shadow each other")

	// HostRequired: an explicit-mode route with empty host is NotReady.
	seedFunction(t, st, "team-a", "hostless-fn")
	seedRoute(t, st, "team-a", "hostless", "", "/hostless", "hostless-fn")
	reconcile(t, rec, "team-a", "hostless")
	s, reason := readyCond(t, st, "team-a", "hostless")
	require.Equal(t, v1.ConditionFalse, s)
	require.Equal(t, "HostRequired", reason)

	// RouteConflict: two Ready routes claiming the same (host, path, method) → one wins deterministically.
	seedRoute(t, st, "team-a", "dup1", "same.example.com", "/dup", "a-fn")
	seedRoute(t, st, "team-b", "dup2", "same.example.com", "/dup", "b-fn")
	reconcile(t, rec, "team-a", "dup1")
	reconcile(t, rec, "team-b", "dup2")
	s1, _ := readyCond(t, st, "team-a", "dup1")
	s2, r2 := readyCond(t, st, "team-b", "dup2")
	require.Equal(t, v1.ConditionTrue, s1, "the (namespace,name)-first route wins")
	require.Equal(t, v1.ConditionFalse, s2)
	require.Equal(t, "RouteConflict", r2)
	_ = ctx
}

func deleteObject(t *testing.T, st store.Store, gvk v1.GroupVersionKind, ns v1.NamespaceName, name string) {
	t.Helper()
	obj, err := st.Get(context.Background(), gvk, ns, v1.ObjectName(name))
	require.NoError(t, err)
	require.NoError(t, st.Delete(context.Background(), gvk, ns, v1.ObjectName(name), obj.GetObjectMeta().ResourceVersion))
}

// Issue 107: every reconcile writes the status of every Route it evaluated, and a live Route is
// requeued, since no watch enqueues a Route when a sibling Route or a backend Function/Bucket changes.
func TestIssue107_RouteStatusFollowsEvaluation(t *testing.T) {
	ctx, st, rtr, rec := setup(t)
	seedFunction(t, st, "default", "fa")
	seedFunction(t, st, "default", "fb")
	resolves := func(path string) (v1.ObjectName, bool) {
		m, ok := rtr.Resolve("gq.test", path, "POST")
		return m.Function, ok
	}
	reconcileResult := func(name string) controller.Result {
		t.Helper()
		res, err := rec.Reconcile(ctx, controller.Request{GVK: v1.KindRoute.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
		require.NoError(t, err)
		return res
	}

	// (a) the RouteConflict loser takes over, and says so, once the winner is deleted.
	seedRoute(t, st, "default", "r-a", "gq.test", "/x", "fa")
	reconcile(t, rec, "default", "r-a")
	seedRoute(t, st, "default", "r-b", "gq.test", "/x", "fb")
	reconcile(t, rec, "default", "r-b")
	_, reason := readyCond(t, st, "default", "r-b")
	require.Equal(t, "RouteConflict", reason)
	deleteObject(t, st, v1.KindRoute.GVK(), "default", "r-a")
	reconcile(t, rec, "default", "r-a")
	fn, ok := resolves("/x")
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("fb"), fn)
	status, reason := readyCond(t, st, "default", "r-b")
	require.Equal(t, v1.ConditionTrue, status, "r-b serves /x, so it is Ready (reason %q)", reason)

	// (b) a lexically earlier Route that takes the slot leaves the displaced Route NotReady.
	seedRoute(t, st, "default", "r-z", "gq.test", "/y", "fb")
	reconcile(t, rec, "default", "r-z")
	seedRoute(t, st, "default", "r-0", "gq.test", "/y", "fa")
	reconcile(t, rec, "default", "r-0")
	fn, _ = resolves("/y")
	require.Equal(t, v1.ObjectName("fa"), fn)
	status, reason = readyCond(t, st, "default", "r-z")
	require.Equal(t, v1.ConditionFalse, status, "r-z no longer serves /y")
	require.Equal(t, "RouteConflict", reason)

	// (c) a Route applied before its backend is requeued, so it converges once the Function exists.
	seedRoute(t, st, "default", "r-late", "gq.test", "/late", "flate")
	require.Positive(t, reconcileResult("r-late").RequeueAfter, "a Route waiting on its backend is requeued")
	_, reason = readyCond(t, st, "default", "r-late")
	require.Equal(t, "BackendNotFound", reason)
	seedFunction(t, st, "default", "flate")
	reconcile(t, rec, "default", "r-late")
	status, _ = readyCond(t, st, "default", "r-late")
	require.Equal(t, v1.ConditionTrue, status)
	_, ok = resolves("/late")
	require.True(t, ok)

	// (d) a Ready Route is requeued, so it goes BackendNotFound once its Function is deleted.
	require.Positive(t, reconcileResult("r-b").RequeueAfter, "a Ready Route is requeued")
	deleteObject(t, st, v1.KindFunction.GVK(), "default", "fb")
	reconcile(t, rec, "default", "r-b")
	_, reason = readyCond(t, st, "default", "r-b")
	require.Equal(t, "BackendNotFound", reason)
	_, ok = resolves("/x")
	require.False(t, ok, "a Route whose Function is gone is not programmed")

	// The static arm waits on its Bucket the same way.
	seedStaticRoute(t, st, "default", "r-static", "gq.test", "/static", "assets")
	require.Positive(t, reconcileResult("r-static").RequeueAfter, "a Route waiting on its Bucket is requeued")
}

// Issue #102: no Bucket event reaches the Route reconciler, so a static Route must requeue to re-check its
// Bucket; the requeued pass after the Bucket is deleted marks it NotReady and unprograms it.
func TestIssue102_StaticRouteNotReadyAfterBucketDeleted(t *testing.T) {
	ctx, st, rtr, rec := setup(t)
	seedBucket(t, st, "default", "reports")
	seedStaticRoute(t, st, "default", "bi", "", "/", "reports")
	req := controller.Request{GVK: v1.KindRoute.GVK(), Namespace: "default", Name: "bi"}

	res, err := rec.Reconcile(ctx, req)
	require.NoError(t, err)
	status, _ := readyCond(t, st, "default", "bi")
	require.Equal(t, v1.ConditionTrue, status)
	require.Positive(t, res.RequeueAfter, "a static Route requeues, else it stays Ready after its Bucket is deleted")

	b, err := st.Get(ctx, v1.KindBucket.GVK(), "default", "reports")
	require.NoError(t, err)
	require.NoError(t, st.Delete(ctx, v1.KindBucket.GVK(), "default", "reports", b.GetObjectMeta().ResourceVersion))
	_, err = rec.Reconcile(ctx, req)
	require.NoError(t, err)
	status, reason := readyCond(t, st, "default", "bi")
	require.Equal(t, v1.ConditionFalse, status)
	require.Equal(t, "BucketNotFound", reason)
	_, ok := rtr.Resolve("any", "/", "GET")
	require.False(t, ok, "a static Route whose Bucket is gone is not programmed")
}

// scenario: route-loses-to-earlier-catalog (ADR-0176) — CatalogService a/c and Route b/r on one claim: the
// catalog serves it and the Route is NotReady (RouteConflict) naming the catalog.
func TestScenario_route_loses_to_earlier_catalog(t *testing.T) {
	var notified []router.Owner
	ctx, st, rtr, agg, rec := setupAggregator(t, func(o router.Owner) { notified = append(notified, o) })
	cat := router.Entry{
		Namespace: "a",
		Host:      "h",
		Rules:     []router.CompiledRule{{Path: "/q", Upstream: "http://catalog-proxy"}},
		Owner:     router.Owner{Kind: v1.KindCatalogService, Namespace: "a", Name: "c"},
	}
	_, err := agg.Set(ctx, "catalog/a/c", []router.Entry{cat})
	require.NoError(t, err)

	seedFunction(t, st, "b", "fn")
	seedRoute(t, st, "b", "r", "h", "/q", "fn")
	reconcile(t, rec, "b", "r")

	obj, err := st.Get(ctx, v1.KindRoute.GVK(), "b", "r")
	require.NoError(t, err)
	cond, ok := obj.(*v1.Route).Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "RouteConflict", cond.Reason)
	require.Contains(t, cond.Message, "CatalogService a/c")
	m, ok := rtr.Resolve("h", "/q", "GET")
	require.True(t, ok)
	require.Equal(t, "http://catalog-proxy", m.Upstream, "the catalog serves the claim")
	require.Equal(t, []router.Owner{cat.Owner}, notified, "the catalog, held until the Route table loaded, is re-run")
}
