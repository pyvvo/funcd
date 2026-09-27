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
	got, err := a.Assign(f, []*v1.Function{f}, 16)
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
	ga, err := a.Assign(fa, []*v1.Function{fa, fb}, 16)
	require.NoError(t, err)
	gb, err := a.Assign(fb, []*v1.Function{fa, fb}, 16)
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
	gn, err := a.Assign(node, []*v1.Function{node, py}, 16)
	require.NoError(t, err)
	gp, err := a.Assign(py, []*v1.Function{node, py}, 16)
	require.NoError(t, err)
	require.NotEqual(t, gn.Key, gp.Key, "a pool hosts one runtime")
	require.Equal(t, "nodejs22", gn.Key.Runtime)
	require.Equal(t, "python312", gp.Key.Runtime)
	// the python member does not consume a node-pool slot: node is admitted at a limit of 1.
	gn1, err := a.Assign(node, []*v1.Function{node, py}, 1)
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
	ga, err := a.Assign(fa, []*v1.Function{fa, fb}, 16)
	require.NoError(t, err)
	gb, err := a.Assign(fb, []*v1.Function{fa, fb}, 16)
	require.NoError(t, err)
	require.NotEqual(t, ga.Key, gb.Key, "the namespace is part of the key")
	require.Equal(t, v1.NamespaceName("ns-a"), ga.Key.Namespace)
	require.Equal(t, v1.NamespaceName("ns-b"), gb.Key.Namespace)
	// at a limit of 1, fa is still admitted — fb (other namespace) does not consume its slot.
	ga1, err := a.Assign(fa, []*v1.Function{fa, fb}, 1)
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

	ga1, err := a.Assign(a1, all, limit)
	require.NoError(t, err)
	gb2, err := a.Assign(b2, all, limit)
	require.NoError(t, err)
	gc3, err := a.Assign(c3, all, limit)
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
		ga, err := a.Assign(a1, order, limit)
		require.NoError(t, err)
		gb, err := a.Assign(b2, order, limit)
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
	_, err := a.Assign(f, []*v1.Function{f}, 0)
	require.Error(t, err, "a limit < 1 is invalid for a pooled function")
}
