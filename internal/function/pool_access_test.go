package function_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/pooling"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
)

// listFailStore fails every List of kind fail while on is set.
type listFailStore struct {
	store.Store
	fail v1.Kind
	on   atomic.Bool
}

func (s *listFailStore) List(ctx context.Context, gvk v1.GroupVersionKind, opts store.ListOptions) (store.List, error) {
	if s.on.Load() && gvk == s.fail.GVK() {
		return store.List{}, errors.New("list " + string(s.fail) + " failed")
	}
	return s.Store.List(ctx, gvk, opts)
}

// The access counts a KV table only when the Function both owns and binds it, and a Bucket prefix the Function owns
// whether it binds it or not.
func TestPoolKeyCountsOwnedBoundTablesAndOwnedPrefixes(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	h.createObj(t, v1.KindKVStore, "s", func(o v1.Object) {
		o.(*v1.KVStore).Spec.Tables = []v1.KVTable{{Name: "bound", Owner: "owner"}, {Name: "unbound", Owner: "idle-owner"}}
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) {
		o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw", Owner: "writer"}}
	})
	bound := v1.FunctionSpec{KV: []v1.FunctionKV{{Alias: "t", Store: "s", Table: "bound"}}}
	for _, n := range []string{"owner", "reader"} {
		h.create(t, n, func(fn *v1.Function) {
			fn.Spec.Pooling.Worker = "agents"
			fn.Spec.KV = bound.KV
		})
	}
	for _, n := range []string{"idle-owner", "writer", "plain"} {
		h.create(t, n, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "agents" })
	}
	got := h.pools(t, "owner", "reader", "idle-owner", "writer", "plain")
	access := func(n string) string {
		key, ok := pooling.ParsePool("default", got[n])
		require.True(t, ok, n)
		return key.AccessHash
	}
	group := []string{"group/rg1"}
	require.Equal(t, pooling.AccessHashOf(bound, []string{"kv/s/bound"}, group), access("owner"), "an owned and bound table counts")
	require.Equal(t, pooling.AccessHashOf(bound, nil, group), access("reader"), "a table another Function owns counts as a binding only")
	require.Equal(t, pooling.AccessHashOf(v1.FunctionSpec{}, nil, group), access("idle-owner"), "an owned table not bound does not count")
	require.Equal(t, access("plain"), access("idle-owner"))
	require.Equal(t, pooling.AccessHashOf(v1.FunctionSpec{}, []string{"blob/k/raw"}, group), access("writer"), "an owned prefix counts unbound")
}

// A pass whose access index cannot be read fails and reclaims nothing; once the List answers, the pool a spec change
// left is reclaimed. One subtest per List the index makes.
func TestPoolAccessListErrorFailsThePassAndReclaimsNothing(t *testing.T) {
	t.Parallel()
	for _, k := range []v1.Kind{v1.KindKVStore, v1.KindBucket, v1.KindRolesAssignment, v1.KindEgressPolicy, v1.KindPolicy} {
		t.Run(string(k), func(t *testing.T) {
			t.Parallel()
			fs := &listFailStore{fail: k}
			h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) {
				fs.Store = d.Store
				d.Store = fs
			})
			h.create(t, "a", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "agents" })
			h.reconcile(t, "a")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "a").Status.Phase)
			pool := runtime.NewInstanceID("default", poolOf("agents"), "", 0)
			before := h.getFn(t, "a").Status.Pool

			h.apply(t, "a", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "other" })
			fs.on.Store(true)
			_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "a"})
			require.ErrorContains(t, err, string(k), "a List error fails the pass")
			require.False(t, h.rt.wasRemoved(pool), "a failed pass reclaims nothing")
			require.Equal(t, before, h.getFn(t, "a").Status.Pool, "a failed pass writes no status")

			fs.on.Store(false)
			h.reconcile(t, "a")
			require.True(t, h.rt.wasRemoved(pool), "the pool a left is reclaimed once the List answers")
		})
	}
}

// A member whose first load failed is Ready once a later pool start loads it, with no spec change.
func TestPooledFailedMemberIsReadyAfterALaterPoolStart(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod, withNodePool)
	h.create(t, "m", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w" })
	h.rt.setMember("m", "failed", "SyntaxError: unexpected token")
	h.reconcile(t, "m")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "m").Status.Phase)
	gen := h.getFn(t, "m").Generation
	creates, _ := h.rt.counts()

	h.rt.exitRevision(poolOf("w"), "", 0, runtime.StateFailed, time.Hour)
	h.rt.setMember("m", "ready", "")
	h.reconcile(t, "m")
	after, _ := h.rt.counts()
	require.Equal(t, creates+1, after, "the pool worker started again")
	fn := h.getFn(t, "m")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, gen, fn.Generation, "no spec change")
	require.Equal(t, v1.ConditionTrue, h.shapeValid(t, "m"))
}

// A member that reads failed in a pass that already serves is not ready: Degraded, never a shape failure, and the pool
// worker is not restarted for it.
func TestPooledFailedServingMemberIsDegraded(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod, withNodePool)
	h.create(t, "m", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w" })
	h.reconcile(t, "m")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "m").Status.Phase)
	creates, _ := h.rt.counts()

	h.rt.setMember("m", "failed", "SyntaxError: unexpected token")
	h.reconcile(t, "m")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "m").Status.Phase)
	require.NotEqual(t, "ShapeInvalid", h.condition(t, "m", "Ready").Reason)
	require.NotEqual(t, v1.ConditionFalse, h.shapeValid(t, "m"))
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "a member's state never restarts the pool worker")
}

// A Policy grants only in its own namespace: one in another namespace that names a pooled Function, or a same-named
// Function of its own, does not split that Function's pool, and a change to it queues only its namespace's pooled
// Functions.
func TestPoolAccessReadsOnlyItsNamespacePolicies(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	for _, n := range []string{"a", "b"} {
		h.create(t, n, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "agents" })
	}
	before := h.pools(t, "a", "b")
	require.Equal(t, before["a"], before["b"], "one access, one pool")

	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	other := obj.(*v1.Function)
	other.Name, other.Namespace, other.ResourceGroup = "a", "other", "rg1"
	other.Spec = h.getFn(t, "a").Spec
	_, err := h.st.Create(context.Background(), other)
	require.NoError(t, err)
	obj, ok = v1.NewObject(v1.KindPolicy)
	require.True(t, ok)
	pol := obj.(*v1.Policy)
	pol.Name, pol.Namespace, pol.ResourceGroup = "pol", "other", "rg1"
	pol.Spec.Cedar = `permit (principal == Function::"other/a", action, resource);
permit (principal == Function::"default/a", action, resource);`
	_, err = h.st.Create(context.Background(), pol)
	require.NoError(t, err)

	after := h.pools(t, "a", "b", "a")
	require.Equal(t, before, after, "a Policy of another namespace grants nothing here")
	require.Equal(t, []controller.Request{{GVK: v1.KindFunction.GVK(), Namespace: "other", Name: "a"}}, h.r.MapAccess(context.Background(), pol),
		"a Policy change queues only its namespace's pooled Functions")
}

// A pooled Function whose spec no pass has seen maps to each asleep member of its key past the first PoolLimit names
// that does not show PoolFull yet, and to nothing else (ADR-0193 Decision 4). Members rank by name: "a" (awake), the
// newcomer "n", the member "s".
func TestMapPoolDisplaced(t *testing.T) {
	t.Parallel()
	asleep := func(fn *v1.Function) {
		fn.Spec.Scaling.MinReplicas = 0
		fn.Status.Phase = v1.PhaseIdle
	}
	cases := []struct {
		name     string
		limit    int
		newcomer func(*v1.Function)
		member   func(*v1.Function)
		want     []v1.ObjectName
	}{
		{name: "asleep-member-past-the-limit", limit: 1, newcomer: func(*v1.Function) {}, member: asleep, want: []v1.ObjectName{"s"}},
		{name: "newcomer-already-observed", limit: 1, newcomer: func(fn *v1.Function) { fn.Status.ObservedGeneration = fn.Generation }, member: asleep},
		{name: "member-awake", limit: 1, newcomer: func(*v1.Function) {}, member: func(fn *v1.Function) { fn.Status.Phase = v1.PhaseReady }},
		{name: "member-already-pool-full", limit: 1, newcomer: func(*v1.Function) {}, member: func(fn *v1.Function) {
			asleep(fn)
			fn.Status.Conditions.Set(v1.Condition{Type: "PoolFull", Status: v1.ConditionTrue, Reason: "PoolFull"})
		}},
		{name: "member-admitted", limit: 3, newcomer: func(*v1.Function) {}, member: asleep},
		{name: "newcomer-not-pooled", limit: 1, newcomer: func(fn *v1.Function) { fn.Spec.Pooling.Worker = "" }, member: asleep},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) { d.PoolLimit = tc.limit })
			for _, n := range []string{"a", "n", "s"} {
				h.create(t, n, func(fn *v1.Function) {
					fn.Spec.Pooling.Worker = "agents"
					fn.Spec.Scaling.MinReplicas = 1
				})
			}
			h.apply(t, "a", func(fn *v1.Function) { fn.Status.Phase = v1.PhaseReady })
			h.apply(t, "s", tc.member)
			newcomer := h.getFn(t, "n")
			tc.newcomer(newcomer)

			var got []v1.ObjectName
			for _, req := range h.r.MapPoolDisplaced(context.Background(), newcomer) {
				got = append(got, req.Name)
			}
			require.Equal(t, tc.want, got)
		})
	}
}
