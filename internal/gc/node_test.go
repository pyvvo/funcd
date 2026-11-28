package gc_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// A namespace that a worker node runs is collected by that node: a collection of it on the control plane, as a forced
// ResourceGroup delete asks, waits until the node did, for a bounded time (FEAT-0002, ADR-0170).
func TestACollectionOfANodesNamespaceWaitsForTheNode(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	c, err := gc.New(gc.Deps{Store: st, Purger: &purges{}, Skip: func(n v1.NamespaceName) bool { return n == ns }})
	require.NoError(t, err)
	child := object(t, v1.KindRevision, "fn-1")
	child.GetObjectMeta().OwnerReferences = []v1.OwnerReference{{UID: "gone", Controller: true,
		ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Namespace: ns, Name: "fn"}}}
	_, err = st.Create(ctx, child)
	require.NoError(t, err)

	gc.WaitForNode(c, 300*time.Millisecond)
	start := time.Now()
	require.NoError(t, c.CollectNamespace(ctx, ns))
	require.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond, "the node is away")
	_, err = st.Get(ctx, v1.KindRevision.GVK(), ns, "fn-1")
	require.NoError(t, err, "the control plane collects nothing of a node's namespace")

	gc.WaitForNode(c, 10*time.Second)
	time.AfterFunc(200*time.Millisecond, func() { _ = st.Delete(ctx, v1.KindRevision.GVK(), ns, "fn-1", "") }) // the node's report
	start = time.Now()
	require.NoError(t, c.CollectNamespace(ctx, ns))
	require.Less(t, time.Since(start), 5*time.Second)
	_, err = st.Get(ctx, v1.KindRevision.GVK(), ns, "fn-1")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "it waited until the node collected the child")
}
