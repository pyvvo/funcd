package chaos

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/stallregistry"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// A Function whose registry accepts a request and never answers fails its pull after a bounded wait, and a KVStore
// applied meanwhile reconciles: the pull does not hold the controller's only worker forever (#697).
func TestIssue697_StalledRegistryDoesNotStopOtherReconciles(t *testing.T) {
	const stallWait = 30 * time.Second
	host, requests := stallregistry.Start(t)
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	st := store.New(memory.New())
	p, err := funcd.New(funcd.InMemory(), funcd.WithStore(st), funcd.WithRuntimeShim("/bin/sh", "-c", "sleep 600"),
		funcd.WithArtifactStore(filepath.Join(dir, "artifacts")))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = p.Shutdown(context.Background())
	})
	create := func(kind v1.Kind, name v1.ObjectName, spec func(v1.Object)) func() v1.Phase {
		obj, _ := v1.NewObject(kind)
		meta := obj.GetObjectMeta()
		meta.Name, meta.Namespace, meta.ResourceGroup = name, "default", "rg1"
		spec(obj)
		_, cerr := st.Create(ctx, obj)
		require.NoError(t, cerr)
		return func() v1.Phase {
			got, gerr := st.Get(ctx, kind.GVK(), "default", name)
			require.NoError(t, gerr)
			return got.(v1.StatusObject).GetStatus().Phase
		}
	}
	kvStore := func(o v1.Object) { o.(*v1.KVStore).Spec.Tables = []v1.KVTable{{Name: "t"}} }

	before := create(v1.KindKVStore, "before", kvStore)
	require.Eventually(t, func() bool { return before() == v1.PhaseReady }, 5*time.Second, 10*time.Millisecond)
	fn := create(v1.KindFunction, "fn", func(o v1.Object) {
		f := o.(*v1.Function)
		f.Spec.Runtime, f.Spec.Handler, f.Spec.Image = "nodejs22", "handle", host+"/team/fn:latest"
	})
	require.Eventually(t, func() bool { return requests("team") == 1 }, 5*time.Second, 10*time.Millisecond,
		"the Function's reconcile waits on the registry")
	after := create(v1.KindKVStore, "after", kvStore)
	require.Eventually(t, func() bool { return after() == v1.PhaseReady }, stallWait, 50*time.Millisecond,
		"a KVStore applied while a Function's reconcile waits on a stalled registry never reconciles")
	require.Eventually(t, func() bool { return fn() == v1.PhaseFailed }, 5*time.Second, 10*time.Millisecond,
		"the Function reports its failed pull")
}
