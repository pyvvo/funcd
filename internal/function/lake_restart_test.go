package function_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
)

// storeReadyLake stores a Ready Lake lake whose status.endpoint is the URL of a previous run's proxy.
func storeReadyLake(t *testing.T, st store.Store) *v1.Lake {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindLake)
	require.True(t, ok)
	cs := obj.(*v1.Lake)
	cs.Name, cs.Namespace, cs.ResourceGroup = "lake", "default", "rg1"
	cs.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "lakehouse", Bucket: "lakehouse", Prefix: "gold"}}
	cs.Spec.Location = v1.LakeLocation{Bucket: "lakehouse", Prefix: "gold"}
	cs.Status.Phase = v1.PhaseReady
	cs.Status.Endpoint = "127.0.0.1:1"
	_, err := st.Create(context.Background(), cs)
	require.NoError(t, err)
	return cs
}

func bindLake(fn *v1.Function) {
	fn.Spec.Bindings.Lake = []v1.FunctionLake{{Alias: "lake", Lake: "lake"}}
}

// TestIssue662_ConsumerWaitsForTheLiveLakeProxy: a restarted daemon binds no lake listener until the
// Lake reconciler's first pass, yet the stored Lake is still Ready with the old proxy's URL. A
// consumer pass that runs first must not start a worker with that URL; once a listener is bound, the worker gets its
// URL (ADR-0162 Decision 3).
func TestIssue662_ConsumerWaitsForTheLiveLakeProxy(t *testing.T) {
	t.Parallel()
	proxies := lakegw.NewManager("", lakegw.NewLakeKeys(nil, nil), nil, nil)
	t.Cleanup(proxies.Shutdown)
	h := newShimHarness(t, http.StatusOK, false, func(d *function.Deps) { d.LakeProxies = proxies })
	cs := storeReadyLake(t, h.st)
	h.create(t, "reader", bindLake)

	h.reconcile(t, "reader")
	require.Zero(t, h.rt.creates, "no worker starts with the URL of a proxy this daemon does not run")
	require.Equal(t, "LakeNotReady", h.condition(t, "reader", "Ready").Reason)

	url, err := proxies.Ensure(auth.EntityRef{Type: v1.KindLake, Namespace: "default", Name: "lake"}, "http://127.0.0.1:9", "engine-token", "")
	require.NoError(t, err)
	require.NotEqual(t, cs.Status.Endpoint, url)
	h.reconcile(t, "reader")
	require.Equal(t, 1, h.rt.creates)
	spec := h.rt.specOf(runtime.NewInstanceID("default", "reader", v1.ObjectName(h.getFn(t, "reader").Status.CurrentRevision), 0))
	require.Equal(t, url, spec.Env["FUNCD_LAKE_LAKE_URL"], "the worker gets the live listener's URL, not the stored endpoint")
}

// TestResolveLakeEnvInjectsBoundListener: with LakeProxies set a consumer is injected the listener bound in this
// run, even one that answers 503 until the engine is targeted; without it, the lake's status.endpoint.
func TestResolveLakeEnvInjectsBoundListener(t *testing.T) {
	t.Parallel()
	proxies := lakegw.NewManager("", lakegw.NewLakeKeys(nil, nil), nil, nil)
	t.Cleanup(proxies.Shutdown)
	_, _, err := proxies.Listen("default", "lake", 0)
	require.NoError(t, err)
	bound, ok := proxies.ProxyURL("default", "lake")
	require.True(t, ok)

	for name, tc := range map[string]struct {
		opts []func(*function.Deps)
		want string
	}{
		"bound-listener": {opts: []func(*function.Deps){func(d *function.Deps) { d.LakeProxies = proxies }}, want: bound},
		"no-manager":     {want: "127.0.0.1:1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, tc.opts...)
			storeReadyLake(t, h.st)
			h.create(t, "reader", bindLake)
			h.reconcile(t, "reader")
			spec := h.rt.specOf(runtime.NewInstanceID("default", "reader", v1.ObjectName(h.getFn(t, "reader").Status.CurrentRevision), 0))
			require.Equal(t, tc.want, spec.Env["FUNCD_LAKE_LAKE_URL"])
		})
	}
}

// lakeGets counts the Lake Gets made through a store.
type lakeGets struct {
	store.Store
	n atomic.Int32
}

func (s *lakeGets) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if gvk == v1.KindLake.GVK() {
		s.n.Add(1)
	}
	return s.Store.Get(ctx, gvk, ns, name)
}

// TestSteadyStateChecksBoundLakes: a steady pass reads no lake without spec.lakes and one per binding with
// them, writes nothing, and turns into the full pass, which shows LakeNotReady, once the bound lake is not Ready.
func TestSteadyStateChecksBoundLakes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newLakeWorld(t)
	gets := &lakeGets{Store: w.st}
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

	cs := w.lake(t)
	cs.Status.Phase = v1.PhasePending
	_, err := w.st.Update(ctx, cs)
	require.NoError(t, err)
	_, err = r.fn.Reconcile(ctx, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "reader"})
	require.NoError(t, err)
	rr, ok := w.function(t, "reader").Status.Conditions.Get("RevisionReady")
	require.True(t, ok)
	require.Equal(t, "LakeNotReady", rr.Reason, "a not-Ready bound lake takes the full pass")

	require.NoError(t, w.st.Delete(ctx, v1.KindLake.GVK(), "default", "lake", ""))
	_, err = r.fn.Reconcile(ctx, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "reader"})
	require.NoError(t, err)
	rr, ok = w.function(t, "reader").Status.Conditions.Get("RevisionReady")
	require.True(t, ok)
	require.Equal(t, "LakeNotReady", rr.Reason, "a missing bound lake shows LakeNotReady")
}

// TestMapLakeMapsConsumers: a Lake event maps to the Functions of its namespace that bind it.
func TestMapLakeMapsConsumers(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false)
	cs := storeReadyLake(t, h.st)
	h.create(t, "reader", bindLake)
	h.create(t, "other", func(fn *v1.Function) {
		fn.Spec.Bindings.Lake = []v1.FunctionLake{{Alias: "sea", Lake: "sea"}}
	})
	h.createFn(t, "plain")

	require.Equal(t, []controller.Request{{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "reader"}},
		h.r.MapLake(context.Background(), cs))
}

// Issue #690, ADR-0161 Decision 2 with ADR-0158: a served pooled member whose gate fails, any gate and not only the
// lake's, keeps serving through its pool worker while its /health/members entry reads ready, and turns Degraded once
// the entry does not.
func TestIssue690_PooledMemberServesThroughAFailedGate(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		reason string
		bind   func(*v1.Function)
		fail   func(*testing.T, *shimHarness)
	}{
		"lake-not-ready": {reason: "LakeNotReady", bind: bindLake, fail: func(t *testing.T, h *shimHarness) {
			obj, err := h.st.Get(context.Background(), v1.KindLake.GVK(), "default", "lake")
			require.NoError(t, err)
			cs := obj.(*v1.Lake)
			cs.Status.Phase = v1.PhasePending
			_, err = h.st.Update(context.Background(), cs)
			require.NoError(t, err)
		}},
		"config-missing": {
			reason: "ConfigResolveFailed",
			bind:   func(fn *v1.Function) { fn.Spec.Bindings.Config = []v1.ObjectName{"app"} },
			fail:   func(t *testing.T, h *shimHarness) { h.deleteConfigMap(t, "app") },
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			proxies := lakegw.NewManager("", lakegw.NewLakeKeys(nil, nil), nil, nil)
			t.Cleanup(proxies.Shutdown)
			_, _, err := proxies.Listen("default", "lake", 0)
			require.NoError(t, err)
			h := newShimHarness(t, http.StatusOK, false, withPeriod, withNodePool, func(d *function.Deps) { d.LakeProxies = proxies })
			storeReadyLake(t, h.st)
			h.configMap(t, "app")
			h.create(t, "member", func(fn *v1.Function) {
				tc.bind(fn)
				fn.Spec.Pooling.Worker = "w1"
			})
			h.reconcile(t, "member")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "member").Status.Phase)

			tc.fail(t, h)
			res := h.reconcile(t, "member")
			fn := h.getFn(t, "member")
			require.Equal(t, v1.PhaseReady, fn.Status.Phase, "the pool worker serves the member")
			require.Equal(t, 1, fn.Status.Replicas)
			require.Equal(t, testPeriod, res.RequeueAfter)
			h.requireCondition(t, "member", "Ready", v1.ConditionTrue, "")
			h.requireCondition(t, "member", "RevisionReady", v1.ConditionFalse, tc.reason)
			require.Len(t, h.routes(t), 1, "the member keeps its route")
			_, ready := h.upstream(t, "member")
			require.True(t, ready, "the member is handed out")

			h.rt.setMember("member", "loading", "")
			h.reconcile(t, "member")
			fn = h.getFn(t, "member")
			require.Equal(t, v1.PhaseDegraded, fn.Status.Phase, "the pool worker runs but the member's entry is not ready")
			require.Zero(t, fn.Status.Replicas)
			h.requireCondition(t, "member", "Ready", v1.ConditionFalse, "Restarting")
			h.requireCondition(t, "member", "RevisionReady", v1.ConditionFalse, tc.reason)
		})
	}
}
