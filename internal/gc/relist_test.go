package gc_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/store/storecontract"
)

// identityDrops records every Identity Watch; the first stream delivers its first event, then closes once
// release is closed, as the store closes a watcher that fell behind.
type identityDrops struct {
	store.Store
	delivered, release chan struct{}
	mu                 sync.Mutex
	since              []string
	kinds              []fault.Kind
}

func (s *identityDrops) Watch(ctx context.Context, gvk v1.GroupVersionKind, opts store.WatchOptions) (store.Watch, error) {
	w, err := s.Store.Watch(ctx, gvk, opts)
	if gvk != v1.KindIdentity.GVK() {
		return w, err
	}
	s.mu.Lock()
	first := len(s.since) == 0
	s.since, s.kinds = append(s.since, opts.SinceResourceVersion), append(s.kinds, fault.KindOf(err))
	s.mu.Unlock()
	if err != nil || !first {
		return w, err
	}
	d := &droppedWatch{Watch: w, out: make(chan store.Event), done: make(chan struct{})}
	go d.forward(s.delivered, s.release)
	return d, nil
}

func (s *identityDrops) watches() ([]string, []fault.Kind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.since...), append([]fault.Kind(nil), s.kinds...)
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

func (d *droppedWatch) forward(delivered, release chan struct{}) {
	defer close(d.out)
	select {
	case ev, ok := <-d.Watch.ResultChan():
		if !ok {
			return
		}
		select {
		case d.out <- ev:
			close(delivered)
		case <-d.done:
			return
		}
	case <-d.done:
		return
	}
	select {
	case <-release:
	case <-d.done:
	}
}

// scenario: stale-watch-relists (collector) — on B, restored from A, the collector's owner watch saw a T1
// version when the store dropped it: the re-watch from that version gets fault.Unavailable, so the collector
// re-watches from now and sweeps, collecting the child of an owner deleted while the stream was down.
func TestScenarioStaleWatchRelists(t *testing.T) {
	a := store.New(memory.New())
	id := create(t, a, v1.KindIdentity, "id", nil)
	create(t, a, v1.KindSecret, "id", id)
	b := &identityDrops{Store: storecontract.Restore(t, a, memory.New()), delivered: make(chan struct{}), release: make(chan struct{})}
	c := newCollector(t, b, time.Hour)
	swept := make(chan struct{})
	gc.OnStartSwept(c, func() { close(swept) })
	runCollector(t, c)
	for _, ch := range []chan struct{}{b.delivered, swept} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("the collector did not start")
		}
	}

	del(t, b, v1.KindIdentity, "id")
	close(b.release)
	require.Eventually(t, func() bool { return !exists(t, b, v1.KindSecret, "id") }, 5*time.Second, 10*time.Millisecond,
		"the sweep after the re-list collects the child")
	since, kinds := b.watches()
	require.Equal(t, []string{"", id.GetObjectMeta().ResourceVersion, ""}, since)
	require.Equal(t, []fault.Kind{"", fault.Unavailable, ""}, kinds)
}
