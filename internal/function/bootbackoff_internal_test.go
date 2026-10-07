package function

import (
	"bytes"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func testBootBackoff(initial, limit time.Duration) *bootBackoff {
	return newBootBackoff(initial, limit, defaultBootTimeout, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// crashed is replica 0 of a worker created at, ended as ex.
func crashed(at time.Time, ex runtime.Exit) runtime.Instance {
	return runtime.Instance{ID: "default/fn/fn-1/r0", Namespace: "default", Name: "fn", State: runtime.StateFailed, CreatedAt: at, Exit: ex}
}

func TestClassifyExit(t *testing.T) {
	t.Parallel()
	code := func(c int) runtime.Exit { return runtime.Exit{Cause: runtime.ExitByCode, Code: c} }
	signal := runtime.Exit{Cause: runtime.ExitBySignal, Signal: 9}
	stop := runtime.Exit{Cause: runtime.ExitByStop}
	cases := []struct {
		name            string
		exit            runtime.Exit
		listened        bool
		serving, legacy bool
		want            exitClass
	}{
		{"stopped", stop, false, false, false, exitStopped},
		{"stopped after listening", stop, true, true, false, exitStopped},
		{"exit 3 before listening, not serving", code(3), false, false, false, exitShapeError},
		{"exit 3 before listening, serving", code(3), false, true, false, exitBootCrash},
		{"exit 3 after listening", code(3), true, false, false, exitAfterServing},
		{"exit 0 before listening", code(0), false, false, false, exitBootCrash},
		{"exit 1 before listening", code(1), false, false, false, exitBootCrash},
		{"exit 2 before listening", code(2), false, false, false, exitBootCrash},
		{"signal before listening", signal, false, false, false, exitBootCrash},
		{"unknown before listening", runtime.Exit{}, false, false, false, exitBootCrash},
		{"signal after listening", signal, true, true, false, exitAfterServing},
		{"legacy end on its own", code(3), false, false, true, exitAfterServing},
	}
	for _, c := range cases {
		in := crashed(time.Now(), c.exit)
		in.Listened = c.listened
		require.Equal(t, c.want, classifyExit(in, c.serving, c.legacy), c.name)
	}
}

func TestBootBackoffObserve(t *testing.T) {
	t.Parallel()
	b := testBootBackoff(10*time.Second, 5*time.Minute)
	at := time.Now()
	killed := crashed(at, runtime.Exit{Cause: runtime.ExitBySignal, Signal: 9})

	c, due := b.observe(killed, exitBootCrash)
	require.Equal(t, 1, c.count)
	require.Equal(t, at.Add(10*time.Second), due)
	require.Equal(t, "replica 0 was killed by signal 9 before it listened; boot crash 1 in a row, retried 10s after its last start", c.message)

	c, due = b.observe(killed, exitBootCrash)
	require.Equal(t, 1, c.count, "one exit is counted once")
	require.Equal(t, at.Add(10*time.Second), due)

	stopped := killed
	stopped.Exit = runtime.Exit{Cause: runtime.ExitByStop}
	c, due = b.observe(stopped, exitStopped)
	require.Equal(t, 1, c.count, "a counted crash Stop ran after keeps its count")
	require.Equal(t, at.Add(10*time.Second), due, "and its wait")

	again := crashed(at.Add(time.Minute), runtime.Exit{Cause: runtime.ExitByCode, Code: 0})
	c, due = b.observe(again, exitBootCrash)
	require.Equal(t, 2, c.count)
	require.Equal(t, again.CreatedAt.Add(20*time.Second), due)
	require.Equal(t, "replica 0 exited with code 0 before it listened; boot crash 2 in a row, retried 20s after its last start", c.message)

	unknown := crashed(at.Add(2*time.Minute), runtime.Exit{})
	c, _ = b.observe(unknown, exitBootCrash)
	require.Contains(t, c.message, "replica 0 ended with an unknown exit status before it listened; boot crash 3 in a row")

	fresh := crashed(at.Add(3*time.Minute), runtime.Exit{})
	c, due = b.observe(fresh, exitStopped)
	require.Zero(t, c.count, "a stop of an instance that never crashed is not counted")
	require.True(t, due.IsZero())

	c, due = b.observe(crashed(at, runtime.Exit{Cause: runtime.ExitByCode, Code: 3}), exitShapeError)
	require.Zero(t, c.count, "a shape error is not a boot crash")
	require.True(t, due.IsZero())

	served := crashed(at.Add(4*time.Minute), runtime.Exit{})
	served.Listened = true
	b.observe(served, exitAfterServing)
	_, ok := b.crash(served.ID)
	require.False(t, ok, "an end after listening clears the count")
}

// scenario: boot-crash-wait-grows-to-max (ADR-0160) — a worker crashing at every boot waits initial, 2×, 4×, …, never
// above the max.
func TestScenarioBootCrashWaitGrowsToMax(t *testing.T) {
	t.Parallel()
	b := testBootBackoff(10*time.Second, 5*time.Minute)
	start := time.Now()
	var waits []time.Duration
	for i := range 8 {
		in := crashed(start.Add(time.Duration(i)*time.Hour), runtime.Exit{Cause: runtime.ExitBySignal, Signal: 9})
		_, due := b.observe(in, exitBootCrash)
		waits = append(waits, due.Sub(in.CreatedAt))
	}
	require.Equal(t, []time.Duration{
		10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second,
		5 * time.Minute, 5 * time.Minute, 5 * time.Minute,
	}, waits)

	huge := testBootBackoff(time.Second, time.Duration(math.MaxInt64))
	require.Equal(t, time.Duration(math.MaxInt64), huge.wait(1000), "doubling stops at the max, never overflows")
	require.Equal(t, 8*time.Second, huge.wait(4))
}

func reconcilerWithBackoff(t *testing.T, initial, limit time.Duration) (*Reconciler, error) {
	t.Helper()
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	rt := process.New(nil)
	t.Cleanup(func() { _ = rt.Close() })
	return NewReconciler(Deps{
		Store: store.New(memory.New()), Runtime: rt, Scheduler: sch, Gateway: embedded.New(), Validator: NewBasicValidator(),
		BootBackoffInitial: initial, BootBackoffMax: limit,
	})
}

// scenario: configured-backoff-honored (ADR-0160) — initial 2 s, max 8 s: waits 2, 4, 8, 8 s; a negative key, or a
// set max below the initial wait, is refused.
func TestScenarioConfiguredBackoffHonored(t *testing.T) {
	t.Parallel()
	r, err := reconcilerWithBackoff(t, 2*time.Second, 8*time.Second)
	require.NoError(t, err)
	require.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second},
		[]time.Duration{r.boot.wait(1), r.boot.wait(2), r.boot.wait(3), r.boot.wait(4)})

	r, err = reconcilerWithBackoff(t, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 10*time.Second, r.boot.wait(1), "the default initial wait")
	require.Equal(t, 5*time.Minute, r.boot.wait(100), "the default max")

	r, err = reconcilerWithBackoff(t, 10*time.Minute, 0)
	require.NoError(t, err)
	require.Equal(t, 10*time.Minute, r.boot.wait(5), "an unset max is the initial wait when that is larger")

	for _, bad := range []struct{ initial, limit time.Duration }{
		{-time.Second, 0}, {0, -time.Second}, {8 * time.Second, 2 * time.Second}, {0, 5 * time.Second},
	} {
		_, err = reconcilerWithBackoff(t, bad.initial, bad.limit)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "initial %s, max %s", bad.initial, bad.limit)
	}
}

// scenario: zero-start-time-keeps-created-at (ADR-0183) — a Runtime that leaves StartedAt zero keeps the boot clock on
// CreatedAt, so a boot crash is re-created a wait after it; a reported StartedAt moves the clock.
func TestScenarioZeroStartTimeKeepsCreatedAt(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	in := crashed(created, runtime.Exit{Cause: runtime.ExitBySignal, Signal: 9})
	require.Equal(t, created, lastStart(in))
	_, due := testBootBackoff(10*time.Second, 5*time.Minute).observe(in, exitBootCrash)
	require.Equal(t, created.Add(10*time.Second), due)

	in.StartedAt = created.Add(2*time.Minute + 30*time.Second)
	require.Equal(t, in.StartedAt, lastStart(in))
}

// scenario: configured-timeout-whole-ms (ADR-0197) — the boot-timeout warning names the 60 s timeout timeout_ms=60000.
func TestScenarioConfiguredTimeoutWholeMs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg, err := observability.NewLogger(observability.Config{Format: observability.FormatJSON}, &buf)
	require.NoError(t, err)
	b := newBootBackoff(10*time.Second, 5*time.Minute, 60*time.Second, lg.Root())
	b.timedOut(runtime.Instance{ID: "default/fn/fn-1/r0", Namespace: "default", Name: "fn", CreatedAt: time.Now()})
	require.Contains(t, buf.String(), `"timeout_ms":60000`)
	require.NotContains(t, buf.String(), `"timeout":`)
}
