package eventing

import (
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// heldGate is a platform hold a test lifts.
type heldGate struct{ held atomic.Bool }

func (g *heldGate) Held() bool            { return g.held.Load() }
func (g *heldGate) ReleasedAt() time.Time { return time.Time{} }

// heldWatcher is a watcher whose seen list holds a and b, over a bucket holding a to d, held.
func heldWatcher(t *testing.T) (*fakeLister, *capturePub, *BlobWatcher, *heldGate) {
	t.Helper()
	lister, pub := &fakeLister{}, &capturePub{}
	w, err := NewBlobWatcher(lister, pub, NewMemWatermark(), 20*time.Millisecond, nil)
	require.NoError(t, err)
	w.Register("lake", "drops", testUID, &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "arrived", Prefix: "drop/"}}})
	lister.set(objects(0, "a", "b")...)
	w.poll(context.Background())
	require.Equal(t, 2, pub.count())
	lister.set(objects(0, "a", "b", "c", "d")...)
	gate := &heldGate{}
	gate.held.Store(true)
	w.SetHold(gate)
	return lister, pub, w, gate
}

func objects(shift time.Duration, names ...string) []blob.Attributes {
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC).Add(shift)
	out := make([]blob.Attributes, 0, len(names))
	for _, n := range names {
		out = append(out, blob.Attributes{Key: "drop/" + n, Size: 1, ModTime: t0})
	}
	return out
}

func runWatcher(t *testing.T, w *BlobWatcher) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// scenario: blob-replay-or-advance — seen list {a, b}, bucket {a, b, c, d}: while held Run neither polls nor fires;
// after the release c and d fire once. With Advance first none fires and a to d are seen. After a blob restore (a
// new ModTime) Pending counts 4.
func TestScenarioBlobReplayOrAdvance(t *testing.T) {
	ctx := context.Background()
	_, pub, w, gate := heldWatcher(t)
	pending, err := w.Pending(ctx, "lake", "drops")
	require.NoError(t, err)
	require.Equal(t, map[string]int{"arrived": 2}, pending)
	runWatcher(t, w)
	time.Sleep(150 * time.Millisecond)
	require.Equal(t, 2, pub.count(), "a held watcher fired")
	gate.held.Store(false)
	require.Eventually(t, func() bool { return pub.count() == 4 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	var fired []string
	for _, ev := range pub.snapshot()[2:] {
		var d BlobEventData
		require.NoError(t, json.Unmarshal(ev.Data, &d))
		fired = append(fired, d.Key)
	}
	slices.Sort(fired)
	require.Equal(t, []string{"drop/c", "drop/d"}, fired, "c and d fire once")

	lister, pub, w, gate := heldWatcher(t)
	require.NoError(t, w.Advance(ctx, "lake", "drops"))
	pending, err = w.Pending(ctx, "lake", "drops")
	require.NoError(t, err)
	require.Equal(t, map[string]int{"arrived": 0}, pending, "a to d are seen")
	gate.held.Store(false)
	runWatcher(t, w)
	time.Sleep(150 * time.Millisecond)
	require.Equal(t, 2, pub.count(), "an advanced source fires none")

	lister.set(objects(time.Hour, "a", "b", "c", "d")...)
	pending, err = w.Pending(ctx, "lake", "drops")
	require.NoError(t, err)
	require.Equal(t, map[string]int{"arrived": 4}, pending, "a restored blob store rewrote every object")
	require.Equal(t, fault.Unavailable, fault.KindOf(w.Advance(ctx, "lake", "unknown")))
}

// The timer Run skips due timers while held; after the release each fires once for the periods it missed.
func TestTimerHeldFiresOnceAfterRelease(t *testing.T) {
	pub := &capturePub{}
	gate := &heldGate{}
	gate.held.Store(true)
	s, err := NewSource(Deps{Store: store.New(memory.New()), Publisher: pub, Hold: gate})
	require.NoError(t, err)
	require.NoError(t, s.registerTimer("lake", "tick", time.Time{}, &v1.TimerSource{Events: []v1.TimerEvent{{Name: "t", Interval: v1.Duration(100 * time.Millisecond)}}}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	time.Sleep(450 * time.Millisecond)
	require.Equal(t, 0, pub.count(), "a held timer fired")
	gate.held.Store(false)
	require.Eventually(t, func() bool { return pub.count() >= 1 }, 2*time.Second, 5*time.Millisecond)
	require.LessOrEqual(t, pub.count(), 2, "missed periods fire once, then the timer keeps its period")
}
