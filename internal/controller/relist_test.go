package controller_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/store/storecontract"
)

// watchCall is one Watch a store served: the since-version asked and the error kind returned.
type watchCall struct {
	since string
	kind  fault.Kind
}

// droppingStore records every Watch and closes the first stream after its first event, as the store closes a
// watcher that fell behind.
type droppingStore struct {
	store.Store
	mu    sync.Mutex
	calls []watchCall
}

func (s *droppingStore) Watch(ctx context.Context, gvk v1.GroupVersionKind, opts store.WatchOptions) (store.Watch, error) {
	w, err := s.Store.Watch(ctx, gvk, opts)
	s.mu.Lock()
	first := len(s.calls) == 0
	s.calls = append(s.calls, watchCall{since: opts.SinceResourceVersion, kind: fault.KindOf(err)})
	s.mu.Unlock()
	if err != nil || !first {
		return w, err
	}
	d := &droppedWatch{Watch: w, out: make(chan store.Event), done: make(chan struct{})}
	go d.forward()
	return d, nil
}

func (s *droppingStore) watches() []watchCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]watchCall(nil), s.calls...)
}

type droppedWatch struct {
	store.Watch
	out  chan store.Event
	done chan struct{}
	once sync.Once
}

func (d *droppedWatch) ResultChan() <-chan store.Event { return d.out }

func (d *droppedWatch) Stop() {
	d.once.Do(func() { close(d.done) })
	d.Watch.Stop()
}

func (d *droppedWatch) forward() {
	defer close(d.out)
	select {
	case ev, ok := <-d.Watch.ResultChan():
		if ok {
			select {
			case d.out <- ev:
			case <-d.done:
			}
		}
	case <-d.done:
	}
}

// scenario: stale-watch-relists (controller) — on B, restored from A, the controller's watch saw a T1 version
// when the store dropped it: the re-watch from that version gets fault.Unavailable, and the controller re-lists
// and reconciles the object again.
func TestScenarioStaleWatchRelists(t *testing.T) {
	t.Parallel()
	a := store.New(memory.New())
	createObject(t, a, v1.KindConfigMap, "restored")
	t1, err := a.Get(context.Background(), v1.KindConfigMap.GVK(), "default", "restored")
	require.NoError(t, err)
	b := &droppingStore{Store: storecontract.Restore(t, a, memory.New())}
	fr := &fakeReconciler{}
	stop := run(t, b, v1.KindConfigMap.GVK(), fr)
	defer stop()

	require.Eventually(t, func() bool { return len(b.watches()) >= 3 && fr.count() >= 2 }, 3*time.Second, 10*time.Millisecond,
		"the controller re-watches, re-lists and reconciles again")
	require.Equal(t, []watchCall{
		{since: ""},
		{since: t1.GetObjectMeta().ResourceVersion, kind: fault.Unavailable},
		{since: ""},
	}, b.watches()[:3])
}
