package eventing

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// holdPub stands in for a hung subscriber on one source: it holds every publish from that source until
// the test ends, counts them, and records every other publish.
type holdPub struct {
	capturePub
	slow    string
	held    chan struct{}
	release chan struct{}
	once    sync.Once
	entered atomic.Int32
}

func newHoldPub(slow string) *holdPub {
	return &holdPub{slow: slow, held: make(chan struct{}), release: make(chan struct{})}
}

func (p *holdPub) Publish(ctx context.Context, ev CloudEvent) error {
	if ev.Source != p.slow {
		return p.capturePub.Publish(ctx, ev)
	}
	p.entered.Add(1)
	p.once.Do(func() { close(p.held) })
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	return nil
}

// pollClock counts the List calls of one prefix, so a test can wait out whole polls.
type pollClock struct {
	BucketLister
	prefix string
	n      atomic.Int32
}

func (c *pollClock) List(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName, prefix string) ([]blob.Attributes, error) {
	if prefix == c.prefix {
		c.n.Add(1)
	}
	return c.BucketLister.List(ctx, ns, bucket, prefix)
}

// startHeld runs a side loop until the test ends and waits until the slow source's publish is held.
func startHeld(t *testing.T, p *holdPub, run func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	t.Cleanup(func() {
		close(p.release)
		cancel()
		<-done
	})
	select {
	case <-p.held:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow source never published")
	}
}

func TestIssue35_SlowSubscriberDoesNotStallOtherTimers(t *testing.T) {
	pub := newHoldPub(SourceURI("team-a", "slow"))
	src, err := NewSource(Deps{Store: store.New(memory.New()), Publisher: pub})
	require.NoError(t, err)
	src.registerTimer("team-a", "slow", &v1.TimerSource{Events: []v1.TimerEvent{{Name: "tick", Interval: 100 * time.Millisecond}}})
	src.registerTimer("team-a", "fast", &v1.TimerSource{Events: []v1.TimerEvent{{Name: "tick", Interval: 100 * time.Millisecond}}})
	startHeld(t, pub, src.Run)

	before := pub.count()
	require.Eventually(t, func() bool { return pub.count() >= before+3 }, 5*time.Second, 10*time.Millisecond,
		"the fast timer stopped firing while the slow timer's publish was held")
	require.Equal(t, int32(1), pub.entered.Load(), "the held timer was published again while its first publish was in flight")
}

func TestIssue35_SlowSubscriberDoesNotStallOtherBlobSources(t *testing.T) {
	lister := &fakeLister{}
	clock := &pollClock{BucketLister: lister, prefix: "fast/"}
	pub := newHoldPub(SourceURI("lake", "slow"))
	w, err := NewBlobWatcher(clock, pub, NewMemWatermark(), 20*time.Millisecond, nil)
	require.NoError(t, err)
	w.Register("lake", "slow", &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "arrived", Prefix: "slow/"}}})
	w.Register("lake", "fast", &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "arrived", Prefix: "fast/"}}})
	mt := time.Now().UTC()
	lister.set(blob.Attributes{Key: "slow/a", Size: 1, ModTime: mt})
	startHeld(t, pub, w.Run)

	lister.set(blob.Attributes{Key: "slow/a", Size: 1, ModTime: mt}, blob.Attributes{Key: "fast/b", Size: 1, ModTime: mt})
	require.Eventually(t, func() bool { return pub.count() == 1 }, 5*time.Second, 10*time.Millisecond,
		"an object under another source was not published while the slow source's publish was held")
	require.Equal(t, "fast/b", mustData(t, pub.snapshot()[0]).Key)

	polls := clock.n.Load()
	require.Eventually(t, func() bool { return clock.n.Load() >= polls+3 }, 5*time.Second, 10*time.Millisecond, "the watcher stopped polling")
	require.Equal(t, int32(1), pub.entered.Load(), "the held prefix was polled and its object published again while its first poll was in flight")
}

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
					src.fireDue(context.Background(), e)
				}
			}
			require.InDelta(t, int(span/interval), pub.count(), 1, "a %s timer over %s", interval, span)
		})
	}
}
