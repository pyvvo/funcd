package eventing

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// holdPub stands in for a hung subscriber on one source: it holds every publish from that source until
// the test ends, and records every other publish.
type holdPub struct {
	capturePub
	slow    string
	held    chan struct{}
	release chan struct{}
	once    sync.Once
}

func newHoldPub(slow string) *holdPub {
	return &holdPub{slow: slow, held: make(chan struct{}), release: make(chan struct{})}
}

func (p *holdPub) Publish(ctx context.Context, ev CloudEvent) error {
	if ev.Source != p.slow {
		return p.capturePub.Publish(ctx, ev)
	}
	p.once.Do(func() { close(p.held) })
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	return nil
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
}

func TestIssue35_SlowSubscriberDoesNotStallOtherBlobSources(t *testing.T) {
	lister := &fakeLister{}
	pub := newHoldPub(SourceURI("lake", "slow"))
	w, err := NewBlobWatcher(lister, pub, NewMemWatermark(), 20*time.Millisecond, nil)
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
}
