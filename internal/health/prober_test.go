package health_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	iblob "github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/health"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// switchProbe is a probe whose answer a test sets; calls counts its runs.
type switchProbe struct {
	err   atomic.Pointer[error]
	calls atomic.Int32
}

func (s *switchProbe) fail(err error) { s.err.Store(&err) }
func (s *switchProbe) pass()          { s.err.Store(nil) }

func (s *switchProbe) probe(context.Context) error {
	s.calls.Add(1)
	if e := s.err.Load(); e != nil {
		return *e
	}
	return nil
}

// flips records each OnChange call.
type flips struct {
	mu  sync.Mutex
	got []string
}

func (f *flips) record(t health.Target, r health.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := "unhealthy"
	if r.Healthy {
		state = "healthy"
	}
	f.got = append(f.got, string(t)+" "+state)
}

func (f *flips) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.got
	f.got = nil
	return out
}

func newProber(t *testing.T, clk clock.Clock, interval, timeout time.Duration, probes map[health.Target]health.Probe) *health.Prober {
	t.Helper()
	p, err := health.NewProber(clk, interval, timeout, probes, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return p
}

func TestNewProberRefusesBadPacing(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string][2]time.Duration{
		"zero interval":          {0, time.Second},
		"zero timeout":           {time.Second, 0},
		"timeout equal interval": {time.Second, time.Second},
		"timeout above interval": {time.Second, 2 * time.Second},
		"negative interval":      {-time.Second, time.Millisecond},
		"negative timeout":       {time.Second, -time.Millisecond},
	} {
		_, err := health.NewProber(nil, tc[0], tc[1], nil, nil)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
	}
}

// The first probe of every target runs before Start returns, so a reconciler started after it reads a real result; a
// target with no probe, and a nil prober, read healthy.
func TestProberProbesBeforeStartReturns(t *testing.T) {
	t.Parallel()
	kv, blob := &switchProbe{}, &switchProbe{}
	kv.fail(errors.New("engine closed"))
	p := newProber(t, clock.NewManual(time.Now()), time.Hour, time.Second, map[health.Target]health.Probe{
		health.TargetKV: kv.probe, health.TargetBlob: blob.probe,
	})
	require.True(t, p.Result(health.TargetKV).Healthy, "not probed yet")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	require.EqualValues(t, 1, kv.calls.Load())
	require.EqualValues(t, 1, blob.calls.Load())
	require.Equal(t, health.Result{Healthy: false, Message: "engine closed", Since: p.Result(health.TargetKV).Since}, p.Result(health.TargetKV))
	require.True(t, p.Result(health.TargetBlob).Healthy)

	only := newProber(t, nil, time.Hour, time.Second, map[health.Target]health.Probe{health.TargetKV: kv.probe})
	only.Start(ctx)
	require.True(t, only.Result(health.TargetBlob).Healthy, "a target with no probe is healthy")
	var none *health.Prober
	require.True(t, none.Result(health.TargetKV).Healthy, "no prober: always healthy")
}

// After the first probe, Start probes once per interval until its context ends.
func TestProberProbesEveryInterval(t *testing.T) {
	t.Parallel()
	kv := &switchProbe{}
	p := newProber(t, nil, 10*time.Millisecond, 5*time.Millisecond, map[health.Target]health.Probe{health.TargetKV: kv.probe})
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	require.Eventually(t, func() bool { return kv.calls.Load() >= 4 }, 2*time.Second, time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)
	stopped := kv.calls.Load()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, stopped, kv.calls.Load(), "no probe after the context ends")
}

// A probe that does not answer within the timeout, even one that ignores its context, is a failure.
func TestProberTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	hang := func(context.Context) error {
		<-release
		return nil
	}
	p := newProber(t, nil, time.Hour, 20*time.Millisecond, map[health.Target]health.Probe{health.TargetBlob: hang})
	start := time.Now()
	p.Start(t.Context())
	require.Less(t, time.Since(start), time.Second, "Start does not wait for a hung probe")
	r := p.Result(health.TargetBlob)
	require.False(t, r.Healthy)
	require.Contains(t, r.Message, "did not answer within 20ms")
}

// OnChange fires only when Healthy flips, once per flip, with one Warn on a failure and one Info on recovery; Since
// moves only on a flip, read from the clock.
func TestProberOnChangeOnFlipOnly(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	kv := &switchProbe{}
	p := newProber(t, clk, time.Hour, time.Second, map[health.Target]health.Probe{health.TargetKV: kv.probe})
	f := &flips{}
	p.OnChange(f.record)
	ctx := context.Background()

	health.ProbeAll(ctx, p)
	health.ProbeAll(ctx, p)
	require.Empty(t, f.take(), "healthy from the start: no flip")

	kv.fail(fault.Unavailablef("kv", "disk gone"))
	failedAt := clk.Now()
	health.ProbeAll(ctx, p)
	require.Equal(t, []string{"kv unhealthy"}, f.take())
	require.Equal(t, health.Result{Healthy: false, Message: "kv: disk gone", Since: failedAt}, p.Result(health.TargetKV))

	clk.Advance(time.Minute)
	health.ProbeAll(ctx, p)
	require.Empty(t, f.take(), "still failing: no flip")
	require.Equal(t, failedAt, p.Result(health.TargetKV).Since, "Since moves only on a flip")

	kv.pass()
	health.ProbeAll(ctx, p)
	require.Equal(t, []string{"kv healthy"}, f.take())
	require.Equal(t, health.Result{Healthy: true, Since: clk.Now()}, p.Result(health.TargetKV))
}

// A probe cut short because the prober is stopping records nothing.
func TestProberIgnoresItsOwnCancellation(t *testing.T) {
	t.Parallel()
	p := newProber(t, nil, time.Hour, time.Second, map[health.Target]health.Probe{
		health.TargetKV: func(ctx context.Context) error { return context.Canceled },
	})
	health.ProbeAll(context.Background(), p)
	require.True(t, p.Result(health.TargetKV).Healthy)
}

// readKV records every key the probe reads and fails the test on any other call.
type readKV struct {
	kvstore.KV
	t    *testing.T
	keys []string
	err  error
}

func (k *readKV) Get(_ context.Context, key string) ([]byte, bool, error) {
	k.keys = append(k.keys, key)
	return nil, false, k.err
}

func (k *readKV) Put(context.Context, string, []byte) error {
	k.t.Error("the KV probe must not write")
	return nil
}

type existsBucket struct {
	iblob.Bucket
	keys []string
	err  error
}

func (b *existsBucket) Exists(_ context.Context, key string) (bool, error) {
	b.keys = append(b.keys, key)
	return false, b.err
}

// The storage probes only read the sentinel key: a missing key is healthy, a read error is not.
func TestStorageProbesReadTheSentinelKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	require.NoError(t, health.KVProbe(kvmemory.New())(ctx), "a missing sentinel is healthy")

	kv := &readKV{t: t}
	require.NoError(t, health.KVProbe(kv)(ctx))
	kv.err = fault.Unavailablef("kv", "closed")
	require.Error(t, health.KVProbe(kv)(ctx))
	require.Equal(t, []string{".funcd-health", ".funcd-health"}, kv.keys)

	b := &existsBucket{}
	require.NoError(t, health.BlobProbe(b)(ctx))
	b.err = fault.Unavailablef("blob", "unreachable")
	require.Error(t, health.BlobProbe(b)(ctx))
	require.Equal(t, []string{health.SentinelKey, health.SentinelKey}, b.keys)
}

// StoreStatus is Ready while the probe passes and Degraded StorageUnreachable while it fails, at the given generation.
func TestStoreStatus(t *testing.T) {
	t.Parallel()
	got := health.StoreStatus(v1.Status{}, health.Result{Healthy: true}, 3)
	require.Equal(t, v1.PhaseReady, got.Phase)
	require.EqualValues(t, 3, got.ObservedGeneration)
	c, ok := got.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.Condition{Type: "Ready", Status: v1.ConditionTrue, ObservedGeneration: 3, LastTransitionTime: c.LastTransitionTime}, c)

	down := health.StoreStatus(got, health.Result{Message: "disk gone"}, 4)
	require.Equal(t, v1.PhaseDegraded, down.Phase)
	c, _ = down.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "StorageUnreachable", c.Reason)
	require.Equal(t, "disk gone", c.Message)
	require.EqualValues(t, 4, c.ObservedGeneration)
	ready, _ := got.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionTrue, ready.Status, "the input status is not changed")
}
