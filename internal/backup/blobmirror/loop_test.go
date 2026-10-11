package blobmirror_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup/blobmirror"
)

// gate is a hold the test flips.
type gate struct {
	mu   sync.Mutex
	held bool
}

func (g *gate) Held() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held
}

func (g *gate) set(held bool) {
	g.mu.Lock()
	g.held = held
	g.mu.Unlock()
}

// waits records each wait of a Loop and lets the test end it.
type waits struct {
	got     chan time.Duration
	release chan time.Time
}

func newWaits() *waits {
	return &waits{got: make(chan time.Duration, 16), release: make(chan time.Time)}
}

func (w *waits) after(d time.Duration) <-chan time.Time {
	w.got <- d
	return w.release
}

// records collects a Loop's Record calls.
type records struct {
	mu   sync.Mutex
	errs []error
}

func (r *records) record(_ time.Time, err error) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
}

func (r *records) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

func runLoop(t *testing.T, m blobmirror.Mirror) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Loop(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel, done
}

// While held, Loop neither runs nor records and checks again after Interval; released, it runs and records.
func TestLoopSkipsWhileHeld(t *testing.T) {
	t.Parallel()
	g, rec, w := &gate{held: true}, &records{}, newWaits()
	f := newFixture(t, func(c *blobmirror.Config) { c.Hold, c.Record = g, rec.record })
	f.put("a", "A")
	blobmirror.Set(f.mirror, nil, w.after, nil, nil)
	runLoop(t, f.mirror)
	require.Equal(t, time.Hour, <-w.got, "held: the next check is an interval later")
	require.Empty(t, f.stub.PutKeys(""))
	require.Zero(t, rec.len())

	g.set(false)
	f.clock.Advance(time.Hour)
	w.release <- f.clock.Now()
	require.Equal(t, time.Hour, <-w.got, "after a run: due an interval after its start")
	require.Equal(t, 1, rec.len())
	require.NoError(t, rec.errs[0])
	require.NotEmpty(t, f.stub.Keys("blob/0000000001/gen/"))
}

// A failed run (a Ready refusal included) is recorded and retried RetryInterval after its end (ADR-0205 Decision 2).
func TestLoopRetriesAfterFailure(t *testing.T) {
	t.Parallel()
	rec, w := &records{}, newWaits()
	refused := fault.Unavailablef("test", "the target is down")
	ready := make(chan error, 2)
	ready <- refused
	f := newFixture(t, func(c *blobmirror.Config) {
		c.Record = rec.record
		c.Ready = func(context.Context) (bool, error) { return true, <-ready }
	})
	blobmirror.Set(f.mirror, nil, w.after, nil, nil)
	runLoop(t, f.mirror)
	require.Equal(t, 5*time.Minute, <-w.got)
	require.Equal(t, 1, rec.len())
	require.ErrorIs(t, rec.errs[0], refused)

	ready <- nil
	f.clock.Advance(5 * time.Minute)
	w.release <- f.clock.Now()
	require.Equal(t, time.Hour, <-w.got)
	require.Equal(t, 2, rec.len())
	require.NoError(t, rec.errs[1])
}

// A Loop starts an interval after the newest complete generation, which survives a restart.
func TestLoopDueAfterNewestGeneration(t *testing.T) {
	t.Parallel()
	w := newWaits()
	f := newFixture(t, nil)
	f.run()
	f.clock.Advance(20 * time.Minute)
	blobmirror.Set(f.mirror, nil, w.after, nil, nil)
	runLoop(t, f.mirror)
	require.Equal(t, 40*time.Minute, <-w.got)
}

// A run cancelled with the Loop's context is never recorded, and its image goes.
func TestLoopCancelRecordsNothing(t *testing.T) {
	t.Parallel()
	rec := &records{}
	f := newFixture(t, func(c *blobmirror.Config) { c.Record = rec.record })
	f.put("a", "A")
	f.put("b", "B")
	ctx, cancel := context.WithCancel(context.Background())
	blobmirror.Set(f.mirror, nil, nil, nil, &blobmirror.Hooks{AfterLink: func(string) { cancel() }})
	done := make(chan struct{})
	go func() { defer close(done); f.mirror.Loop(ctx) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Loop did not return after its context ended")
	}
	require.Zero(t, rec.len())
	require.NoDirExists(t, f.dir+"-frozen")
	require.Empty(t, f.stub.Keys("blob/0000000001/gen/"))
}
