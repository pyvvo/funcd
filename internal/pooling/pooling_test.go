package pooling_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/pooling"
)

// fn builds a Function with the given namespace, runtime, worker id (pooling.worker), and name.
func fn(ns, runtime, worker, name string) *v1.Function {
	obj, _ := v1.NewObject(v1.KindFunction)
	f := obj.(*v1.Function)
	f.Name = v1.ObjectName(name)
	f.Namespace = v1.NamespaceName(ns)
	f.Spec.Runtime = v1.RuntimeName(runtime)
	f.Spec.Pooling.Worker = worker
	return f
}

// scenario: solo-by-default — a function with no pooling.worker is placed solo (own worker).
func TestScenarioSoloByDefault(t *testing.T) {
	t.Parallel()
	a := pooling.NewAssigner()
	f := fn("default", "nodejs22", "", "echo")
	got, err := a.Assign(f, keyOf(f), []*v1.Function{f}, 16)
	require.NoError(t, err)
	require.False(t, got.Pooled, "no worker id ⇒ solo")
	require.False(t, got.Rejected)
	require.Equal(t, pooling.PoolKey{}, got.Key, "solo carries the zero key")
}

// scenario: distinct-workers-distinct-pools — two functions in one (ns, runtime) naming
// DIFFERENT worker ids land in different pools (different keys).
func TestScenarioDistinctWorkersDistinctPools(t *testing.T) {
	t.Parallel()
	a := pooling.NewAssigner()
	fa := fn("default", "nodejs22", "agents", "fa")
	fb := fn("default", "nodejs22", "tools", "fb")
	ga, err := a.Assign(fa, keyOf(fa), []*v1.Function{fa, fb}, 16)
	require.NoError(t, err)
	gb, err := a.Assign(fb, keyOf(fb), []*v1.Function{fa, fb}, 16)
	require.NoError(t, err)
	require.True(t, ga.Pooled)
	require.True(t, gb.Pooled)
	require.NotEqual(t, ga.Key, gb.Key, "different worker ids ⇒ different pools")
	require.Equal(t, "agents", ga.Key.Worker)
	require.Equal(t, "tools", gb.Key.Worker)
}

// scenario: runtime-separates-pools — same worker id, different runtime ⇒ different pools;
// a different-runtime member never counts toward the cap (it is not in the same key).
func TestScenarioRuntimeSeparatesPools(t *testing.T) {
	t.Parallel()
	a := pooling.NewAssigner()
	node := fn("default", "nodejs22", "shared", "n")
	py := fn("default", "python312", "shared", "p")
	gn, err := a.Assign(node, keyOf(node), []*v1.Function{node, py}, 16)
	require.NoError(t, err)
	gp, err := a.Assign(py, keyOf(py), []*v1.Function{node, py}, 16)
	require.NoError(t, err)
	require.NotEqual(t, gn.Key, gp.Key, "a pool hosts one runtime")
	require.Equal(t, "nodejs22", gn.Key.Runtime)
	require.Equal(t, "python312", gp.Key.Runtime)
	// the python member does not consume a node-pool slot: node is admitted at a limit of 1.
	gn1, err := a.Assign(node, keyOf(node), []*v1.Function{node, py}, 1)
	require.NoError(t, err)
	require.False(t, gn1.Rejected, "a cross-runtime peer never fills the node pool")
}

// scenario: cross-namespace-never-pools — same worker id + runtime in two namespaces ⇒
// different pools; the namespace trust boundary holds, and a cross-namespace peer never
// fills the cap.
func TestScenarioCrossNamespaceNeverPools(t *testing.T) {
	t.Parallel()
	a := pooling.NewAssigner()
	fa := fn("ns-a", "nodejs22", "shared", "f")
	fb := fn("ns-b", "nodejs22", "shared", "f")
	ga, err := a.Assign(fa, keyOf(fa), []*v1.Function{fa, fb}, 16)
	require.NoError(t, err)
	gb, err := a.Assign(fb, keyOf(fb), []*v1.Function{fa, fb}, 16)
	require.NoError(t, err)
	require.NotEqual(t, ga.Key, gb.Key, "the namespace is part of the key")
	require.Equal(t, v1.NamespaceName("ns-a"), ga.Key.Namespace)
	require.Equal(t, v1.NamespaceName("ns-b"), gb.Key.Namespace)
	// at a limit of 1, fa is still admitted — fb (other namespace) does not consume its slot.
	ga1, err := a.Assign(fa, keyOf(fa), []*v1.Function{fa, fb}, 1)
	require.NoError(t, err)
	require.False(t, ga1.Rejected, "a cross-namespace peer never fills the pool")
}

// scenario: pool-cap-guard — more than `limit` functions naming one worker id ⇒ the over-cap
// members (by name order) are Rejected with a PoolFull reason; the first `limit` are admitted.
func TestScenarioPoolCapGuard(t *testing.T) {
	t.Parallel()
	a := pooling.NewAssigner()
	const limit = 2
	a1 := fn("default", "nodejs22", "w", "a1")
	b2 := fn("default", "nodejs22", "w", "b2")
	c3 := fn("default", "nodejs22", "w", "c3")
	all := []*v1.Function{c3, a1, b2} // deliberately unsorted input

	ga1, err := a.Assign(a1, keyOf(a1), all, limit)
	require.NoError(t, err)
	gb2, err := a.Assign(b2, keyOf(b2), all, limit)
	require.NoError(t, err)
	gc3, err := a.Assign(c3, keyOf(c3), all, limit)
	require.NoError(t, err)

	require.True(t, ga1.Pooled && !ga1.Rejected, "a1 (rank 0) admitted")
	require.True(t, gb2.Pooled && !gb2.Rejected, "b2 (rank 1) admitted")
	require.True(t, gc3.Pooled && gc3.Rejected, "c3 (rank 2) over the cap ⇒ Rejected")
	require.Contains(t, gc3.Reason, "full", "PoolFull reason names the cap")
	require.Contains(t, gc3.Reason, "w", "PoolFull reason names the worker id")
}

// scenario: deterministic-ordering — admission is stable under reconcile/input order: the
// same member gets the same verdict regardless of how sameKey is shuffled.
func TestScenarioDeterministicOrdering(t *testing.T) {
	t.Parallel()
	a := pooling.NewAssigner()
	const limit = 1
	a1 := fn("default", "nodejs22", "w", "a1")
	b2 := fn("default", "nodejs22", "w", "b2")

	for _, order := range [][]*v1.Function{{a1, b2}, {b2, a1}} {
		ga, err := a.Assign(a1, keyOf(a1), order, limit)
		require.NoError(t, err)
		gb, err := a.Assign(b2, keyOf(b2), order, limit)
		require.NoError(t, err)
		require.False(t, ga.Rejected, "a1 (first by name) is always admitted")
		require.True(t, gb.Rejected, "b2 (second by name) is always rejected — order-independent")
	}
}

// a bad limit on a pooled function is a typed fault (defensive contract).
func TestAssignRejectsBadLimit(t *testing.T) {
	t.Parallel()
	a := pooling.NewAssigner()
	f := fn("default", "nodejs22", "w", "x")
	_, err := a.Assign(f, keyOf(f), []*v1.Function{f}, 0)
	require.Error(t, err, "a limit < 1 is invalid for a pooled function")
}

// keyOf is f's key with no owned data and no grants.
func keyOf(f *v1.Function) pooling.PoolKey {
	k, _ := pooling.KeyOf(f, nil, nil)
	return k
}

func TestAccessHashOfIgnoresBindingOrderAndTreatsNilAsEmpty(t *testing.T) {
	a := fn("ns", "nodejs22", "w", "a").Spec
	a.KV = []v1.FunctionKV{{Alias: "x", Store: "s", Table: "t"}, {Alias: "y", Store: "s", Table: "u"}}
	a.Links = []v1.FunctionLink{{Alias: "p", Target: "b", Timeout: 5}, {Alias: "q", Target: "c"}}
	b := fn("ns", "nodejs22", "w", "b").Spec
	b.KV = []v1.FunctionKV{a.KV[1], a.KV[0]}
	b.Links = []v1.FunctionLink{{Alias: "q", Target: "c"}, {Alias: "p", Target: "b"}}
	require.Equal(t, pooling.AccessHashOf(a, []string{"kv/s/t", "blob/k/raw"}, nil),
		pooling.AccessHashOf(b, []string{"blob/k/raw", "kv/s/t"}, []string{}), "alias order, owned order, a link timeout and nil vs empty never change the hash")
	require.Len(t, pooling.AccessHashOf(a, nil, nil), 16)

	empty := fn("ns", "nodejs22", "w", "c").Spec
	require.Equal(t, pooling.AccessHashOf(empty, nil, nil), pooling.AccessHashOf(v1.FunctionSpec{}, []string{}, []string{}))
	require.NotEqual(t, pooling.AccessHashOf(empty, nil, nil), pooling.AccessHashOf(a, nil, nil))

	s1 := v1.FunctionSpec{Secrets: []v1.ObjectName{"x", "y"}}
	s2 := v1.FunctionSpec{Secrets: []v1.ObjectName{"y", "x"}}
	require.NotEqual(t, pooling.AccessHashOf(s1, nil, nil), pooling.AccessHashOf(s2, nil, nil), "secrets keep their declared order")
	require.NotEqual(t, pooling.AccessHashOf(empty, []string{"kv/s/t"}, nil), pooling.AccessHashOf(empty, nil, nil), "owned data counts")
	require.NotEqual(t, pooling.AccessHashOf(empty, nil, []string{"group/g"}), pooling.AccessHashOf(empty, nil, nil), "grants count")

	scaled := empty
	scaled.Handler = "other"
	scaled.Scaling.MinReplicas = 3
	require.Equal(t, pooling.AccessHashOf(empty, nil, nil), pooling.AccessHashOf(scaled, nil, nil), "handler and scaling are not access")
}

func TestParsePoolRoundTripsString(t *testing.T) {
	k, ok := pooling.KeyOf(fn("ns", "nodejs22", "agents", "a"), nil, nil)
	require.True(t, ok)
	require.Equal(t, "nodejs22/agents/"+k.AccessHash, k.String())
	got, ok := pooling.ParsePool("ns", k.String())
	require.True(t, ok)
	require.Equal(t, k, got)
	for _, bad := range []string{"", "nodejs22/agents", "nodejs22//abc", "a/b/c/d", "/agents/abc"} {
		_, ok := pooling.ParsePool("ns", bad)
		require.False(t, ok, bad)
	}
}

func TestPoolFullNamesThePool(t *testing.T) {
	a1, b2 := fn("ns", "nodejs22", "w", "a1"), fn("ns", "nodejs22", "w", "b2")
	got, err := pooling.NewAssigner().Assign(b2, keyOf(b2), []*v1.Function{a1, b2}, 1)
	require.NoError(t, err)
	require.True(t, got.Rejected)
	require.Equal(t, "pool "+keyOf(b2).String()+" is full (1); use another pooling.worker", got.Reason)
}
