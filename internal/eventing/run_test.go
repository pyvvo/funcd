package eventing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func TestIssue114_TimerFiresOncePerInterval(t *testing.T) {
	const span = 10 * time.Second
	for _, interval := range []time.Duration{100 * time.Millisecond, 300 * time.Millisecond, time.Second, 1100 * time.Millisecond} {
		t.Run(interval.String(), func(t *testing.T) {
			pub := &capturePub{}
			src, err := NewSource(Deps{Store: store.New(memory.New()), Publisher: pub})
			require.NoError(t, err)
			src.registerTimer("team-a", "clock", &v1.TimerSource{Events: []v1.TimerEvent{{Name: "tick", Interval: interval}}})
			start := src.timers[eventKey{ns: "team-a", source: "clock", event: "tick"}].lastFire

			// The Run loop's ticks: not aligned with the registration, with sub-millisecond ticker jitter.
			for k := 1; ; k++ {
				now := start.Add(7*time.Millisecond + time.Duration(k)*runTick + time.Duration(k%3)*100*time.Microsecond)
				if now.Sub(start) > span {
					break
				}
				for _, e := range src.dueTimers(now) {
					require.NoError(t, src.Fire(context.Background(), e.ns, e.source, e.event))
				}
			}
			require.InDelta(t, int(span/interval), pub.count(), 1, "a %s timer over %s", interval, span)
		})
	}
}

// failKV is a KV whose List fails, so a Watermark.Delete (and with it Purge) fails.
type failKV struct{ kvstore.KV }

func (failKV) List(context.Context, string) ([]string, error) {
	return nil, fault.Unavailablef("failKV.List", "kv unavailable")
}

// newFailPurgeSource builds a Source over st whose BlobWatcher's Purge fails.
func newFailPurgeSource(t *testing.T, st store.Store) *Source {
	t.Helper()
	wm, err := NewKVWatermark(failKV{KV: kvmemory.New()})
	require.NoError(t, err)
	w, err := NewBlobWatcher(&fakeLister{}, &capturePub{}, wm, time.Second, nil)
	require.NoError(t, err)
	src, err := NewSource(Deps{Store: st, Publisher: &capturePub{}, Blob: w})
	require.NoError(t, err)
	return src
}

func TestReconcileDeletedSourceReturnsPurgeError(t *testing.T) {
	src := newFailPurgeSource(t, store.New(memory.New()))
	src.registerTimer("team-a", "clock", &v1.TimerSource{Events: []v1.TimerEvent{{Name: "tick", Interval: time.Minute}}})

	_, err := src.Reconcile(context.Background(), controller.Request{GVK: v1.KindEventSource.GVK(), Namespace: "team-a", Name: "clock"})
	require.Error(t, err, "a Purge error is returned for a retry")
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.Zero(t, src.ActiveTimers(), "the deleted source's timers are deregistered")
}

func TestReconcileTimerPurgeErrorKeepsTimer(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	obj, ok := v1.NewObject(v1.KindEventSource)
	require.True(t, ok)
	es := obj.(*v1.EventSource)
	es.Name, es.Namespace, es.ResourceGroup = "clock", "team-a", "rg1"
	es.Spec.Timer = &v1.TimerSource{Events: []v1.TimerEvent{{Name: "tick", Interval: time.Minute}}}
	_, err := st.Create(ctx, es)
	require.NoError(t, err)
	src := newFailPurgeSource(t, st)
	req := controller.Request{GVK: v1.KindEventSource.GVK(), Namespace: "team-a", Name: "clock"}

	_, err = src.Reconcile(ctx, req)
	require.Error(t, err, "a Purge error is returned for a retry")
	require.Equal(t, 1, src.ActiveTimers(), "the timer still ticks")
	got, err := st.Get(ctx, v1.KindEventSource.GVK(), "team-a", "clock")
	require.NoError(t, err)
	require.Equal(t, v1.PhaseReady, got.(*v1.EventSource).Status.Phase)
	k := eventKey{ns: "team-a", source: "clock", event: "tick"}
	first := src.timers[k].lastFire

	_, err = src.Reconcile(ctx, req)
	require.Error(t, err)
	require.Equal(t, first, src.timers[k].lastFire, "the retry keeps lastFire")
}
