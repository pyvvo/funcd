package funcd_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvbadger "github.com/pyvvo/funcd/internal/kvstore/badger"
	"github.com/pyvvo/funcd/internal/store"
	bstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// A KVStore whose delete committed before its NotFound reconcile ran (a crash, or a stop that drops that
// reconcile) keeps its <ns>/<store>/ data in the KV engine; the next start reclaims it (ADR-0170 "also
// across a crash"), so a store re-created under the same name starts empty.
func TestIssue708_DeletedKVStoreDataReclaimedOnRestart(t *testing.T) {
	t.Run("crash after the collector deletes the store", func(t *testing.T) {
		dir := kvRestartDir(t)
		ctx := context.Background()
		st, kv := openKVRestart(t, dir)
		ks := kvStoreS()
		ref := v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: v1.KindWorkflow, Name: "w"}, UID: "u-dead"}
		marker := ref
		ref.Controller = true
		ks.OwnerReferences = []v1.OwnerReference{ref, marker}
		_, err := st.Create(ctx, ks)
		require.NoError(t, err)
		putOld(t, kv)
		col, err := gc.New(gc.Deps{Store: st, Purger: noPurge{}})
		require.NoError(t, err)
		require.NoError(t, col.CollectNamespace(ctx, "default"))
		_, err = st.Get(ctx, v1.KindKVStore.GVK(), "default", "s")
		require.Equal(t, fault.NotFound, fault.KindOf(err), "the collector deleted the store")
		require.NoError(t, st.Close())
		require.NoError(t, kv.(io.Closer).Close())

		requireReclaimed(t, dir)
	})

	t.Run("stop while the delete's reconcile runs", func(t *testing.T) {
		dir := kvRestartDir(t)
		st, kv := openKVRestart(t, dir)
		stalled := &stalledDrop{KV: kv, entered: make(chan struct{}), release: make(chan struct{})}
		p, err := funcd.New(funcd.InMemory(), funcd.WithStore(st), funcd.WithKVStore(stalled))
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- p.Run(ctx) }()

		_, err = st.Create(ctx, kvStoreS())
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			obj, gerr := st.Get(ctx, v1.KindKVStore.GVK(), "default", "s")
			return gerr == nil && obj.(*v1.KVStore).Status.Phase == v1.PhaseReady
		}, 5*time.Second, 10*time.Millisecond)
		putOld(t, kv)
		require.NoError(t, st.Delete(ctx, v1.KindKVStore.GVK(), "default", "s", ""))
		select {
		case <-stalled.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the delete's reconcile never reached DropPrefix")
		}
		cancel()
		close(stalled.release)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("platform Run did not return after cancel")
		}
		_ = st.Close()
		require.NoError(t, kv.(io.Closer).Close())

		requireReclaimed(t, dir)
	})
}

// stalledDrop holds the delete's DropPrefix until the platform is stopping, then fails it, as a reconcile
// that runs with the cancelled context does.
// noPurge is a gc.BucketPurger that purges nothing: these tests collect no Bucket.
type noPurge struct{}

func (noPurge) Purge(context.Context, v1.NamespaceName, v1.ObjectName) error { return nil }

type stalledDrop struct {
	kvstore.KV
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *stalledDrop) DropPrefix(string) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return errors.New("platform stopping")
}

func kvRestartDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func openKVRestart(t *testing.T, dir string) (store.Store, kvstore.KV) {
	t.Helper()
	eng, err := bstore.Open(filepath.Join(dir, "store"), bstore.WithValueLogGCInterval(0))
	require.NoError(t, err)
	kv, err := kvbadger.Open(filepath.Join(dir, "kv"), kvbadger.WithValueLogGCInterval(0))
	require.NoError(t, err)
	return store.New(eng), kv
}

func kvStoreS() *v1.KVStore {
	ks := &v1.KVStore{}
	ks.TypeMeta = v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore}
	ks.Name, ks.Namespace, ks.ResourceGroup = "s", "default", "rg1"
	ks.Spec.Tables = []v1.KVTable{{Name: "t"}}
	return ks
}

func putOld(t *testing.T, kv kvstore.KV) {
	t.Helper()
	require.NoError(t, kv.Put(context.Background(), "default/s/t/k", []byte("old-incarnation")))
}

// requireReclaimed boots the platform on dir, re-creates the store through the control plane and checks the
// deleted store's key is gone.
func requireReclaimed(t *testing.T, dir string) {
	t.Helper()
	st, kv := openKVRestart(t, dir)
	p, err := funcd.New(funcd.InMemory(), funcd.WithStore(st), funcd.WithKVStore(kv))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	_, err = c.Apply(ctx, kvStoreS())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		obj, gerr := c.Get(ctx, v1.KindKVStore, "default", "s")
		return gerr == nil && obj.(*v1.KVStore).Status.Phase == v1.PhaseReady
	}, 5*time.Second, 10*time.Millisecond)
	v, found, err := kv.Get(ctx, "default/s/t/k")
	require.NoError(t, err)
	require.False(t, found, "the re-created store reads %q, the data of the deleted store", v)
}
