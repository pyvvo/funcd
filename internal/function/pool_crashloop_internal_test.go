package function

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/runtime"
)

// A boot-crash message names a pool worker as "the pool worker" and a solo replica by its index (ADR-0225 Decision 4).
func TestWorkerSubject(t *testing.T) {
	t.Parallel()
	pool := runtime.Instance{Name: poolInstancePrefix + "nodejs22__w__x", Replica: 0}
	solo := runtime.Instance{Name: "fn", Replica: 2}
	require.Equal(t, "the pool worker", workerSubject(pool))
	require.Equal(t, "replica 2", workerSubject(solo))

	b := testBootBackoff(10*time.Second, 5*time.Minute)
	pool.ID, pool.CreatedAt = "default/"+runtime.InstanceID(pool.Name)+"/sig/r0", time.Now()
	b.timedOut(pool)
	c, _ := b.crash(pool.ID)
	require.Equal(t, "the pool worker did not listen within 1m0s; boot crash 1 in a row, retried 1m0s after its last start", c.message)
	solo.ID, solo.CreatedAt, solo.Exit = "default/fn/fn-1/r2", time.Now(), runtime.Exit{Cause: runtime.ExitByCode, Code: 1}
	c, _ = b.observe(solo, exitBootCrash)
	require.Equal(t, "replica 2 exited with code 1 before it listened; boot crash 1 in a row, retried 10s after its last start", c.message)
}

// stopRecorder records each Stop instead of running it.
type stopRecorder struct {
	runtime.Runtime
	stopped []runtime.InstanceID
}

func (s *stopRecorder) Stop(_ context.Context, id runtime.InstanceID) error {
	s.stopped = append(s.stopped, id)
	return nil
}

// scenario: pool-boot-clock-from-last-start (ADR-0225, a guard of ADR-0183) — a pool worker whose StartedAt is after its
// CreatedAt is not stopped before bootTimeout after StartedAt, and is at that time.
func TestScenarioPoolBootClockFromLastStart(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	created := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	started := created.Add(10 * time.Minute)
	clk := clock.NewManual(started.Add(r.bootTimeout - time.Millisecond))
	rec := &stopRecorder{Runtime: r.runtime}
	r.clock, r.runtime = clk, rec
	in := runtime.Instance{
		ID: "default/" + poolInstancePrefix + "nodejs22__w__x/sig/r0", Namespace: "default", Name: poolInstancePrefix + "nodejs22__w__x",
		Revision: "sig", State: runtime.StateRunning, CreatedAt: created, StartedAt: started,
	}

	u, err := r.stopUnlistenedIn(context.Background(), []runtime.Instance{in})
	require.NoError(t, err)
	require.Empty(t, u.stopped, "bootTimeout runs from its last start, not its creation")
	require.Empty(t, rec.stopped)

	clk.Advance(time.Millisecond)
	u, err = r.stopUnlistenedIn(context.Background(), []runtime.Instance{in})
	require.NoError(t, err)
	require.Equal(t, []runtime.InstanceID{in.ID}, u.stopped)
	require.Equal(t, []runtime.InstanceID{in.ID}, rec.stopped)
	require.Equal(t, started.Add(10*time.Second), u.retryAt, "its wait runs from its last start")
	require.Contains(t, u.crashLoop, "the pool worker did not listen within 1m0s; boot crash 1 in a row")
}
