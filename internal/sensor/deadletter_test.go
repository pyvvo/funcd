package sensor_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	dlbadger "github.com/pyvvo/funcd/internal/eventing/deadletter/badger"
	dlmemory "github.com/pyvvo/funcd/internal/eventing/deadletter/memory"
	"github.com/pyvvo/funcd/internal/sensor"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// scriptedInvoker fails its first `failFirst` calls then succeeds; failFirst < 0 ⇒ always fail. A real
// Invoker stub (no mock framework), safe under the concurrent retry workers.
type scriptedInvoker struct {
	mu        sync.Mutex
	failFirst int
	calls     int
}

func (s *scriptedInvoker) Invoke(_ context.Context, _ v1.NamespaceName, _ v1.ObjectName, _ eventing.CloudEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failFirst < 0 || s.calls <= s.failFirst {
		return fault.Unavailablef("test.invoke", "target returned 500 (attempt %d)", s.calls)
	}
	return nil
}
func (s *scriptedInvoker) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

// dlqHarness builds a Sensor reconciler with the DLQ wired (in-memory driver) + the retry workers running,
// and returns the pieces the DLQ scenarios assert on. The workers stop on test cleanup.
func dlqHarness(t *testing.T, inv sensor.Invoker, attempts int) (store.Store, *eventing.Fanout, deadletter.Store, *sensor.Reconciler) {
	t.Helper()
	st := store.New(memory.New())
	fan := eventing.NewFanout()
	dlq := dlmemory.New()
	r, err := sensor.NewReconciler(sensor.Deps{Store: st, Subscriber: fan, Invoker: inv, DeadLetters: dlq, DeliveryAttempts: attempts})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	go r.RunRetryWorkers(ctx)
	t.Cleanup(cancel)
	return st, fan, dlq, r
}

// invocationsByPhase counts recorded Invocations by phase (Ready vs Failed).
func invocationsByPhase(t *testing.T, st store.Store) (ready, failed int) {
	t.Helper()
	list, err := st.List(context.Background(), v1.KindInvocation.GVK(), store.ListOptions{})
	require.NoError(t, err)
	for _, o := range list.Items {
		switch o.(*v1.Invocation).Status.Phase {
		case v1.PhaseReady:
			ready++
		case v1.PhaseFailed:
			failed++
		}
	}
	return ready, failed
}

func dlqList(t *testing.T, dlq deadletter.Store, ns v1.NamespaceName) []deadletter.DeadLetter {
	t.Helper()
	items, err := dlq.List(context.Background(), ns)
	require.NoError(t, err)
	return items
}

// scenario: action-fails-then-dead-lettered — a function: action target that returns 500 on every attempt
// is retried up to DeliveryAttempts (3) then parked with the full CloudEvent + provenance + Attempts +
// Reason, AND one Failed Invocation is recorded (the ADR-0023 audit line stays).
func TestActionFailsThenDeadLettered(t *testing.T) {
	inv := &scriptedInvoker{failFirst: -1} // always fail
	st, fan, dlq, r := dlqHarness(t, inv, 3)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "notify", On: "d", Function: "mailer"}})
	_, err := r.Reconcile(context.Background(), reqOf("s"))
	require.NoError(t, err)
	fire(t, fan, "git", "push", `{"repository":"acme/x"}`)

	require.Eventually(t, func() bool { return len(dlqList(t, dlq, "team-a")) == 1 }, 3*time.Second, 20*time.Millisecond,
		"the exhausted delivery must be dead-lettered")
	items := dlqList(t, dlq, "team-a")
	dl := items[0]
	require.Equal(t, v1.ObjectName("s"), dl.Sensor)
	require.Equal(t, v1.ObjectName("git"), dl.Source)
	require.Equal(t, v1.ObjectName("push"), dl.Event)
	require.Equal(t, "notify", dl.Action)
	require.Equal(t, 3, dl.Attempts, "Attempts = DeliveryAttempts")
	require.NotEmpty(t, dl.Reason)
	require.Contains(t, string(dl.Payload), "specversion") // the full CloudEvent is parked
	require.Equal(t, 3, inv.count(), "delivered exactly DeliveryAttempts times")

	_, failed := invocationsByPhase(t, st)
	require.Equal(t, 1, failed, "one terminal Failed Invocation (never silently dropped)")
	ready, _ := invocationsByPhase(t, st)
	require.Equal(t, 0, ready)
}

// scenario: transient-then-succeeds — an action that fails once then succeeds records ONE Ready Invocation
// and NO DeadLetter (bounded retry absorbs the transient blip).
func TestTransientThenSucceeds(t *testing.T) {
	inv := &scriptedInvoker{failFirst: 1} // fail once, then succeed
	st, fan, dlq, r := dlqHarness(t, inv, 3)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "notify", On: "d", Function: "mailer"}})
	_, _ = r.Reconcile(context.Background(), reqOf("s"))
	fire(t, fan, "git", "push", "")

	require.Eventually(t, func() bool {
		ready, _ := invocationsByPhase(t, st)
		return ready == 1
	}, 3*time.Second, 20*time.Millisecond, "the retried delivery eventually succeeds")
	// give any stray extra work a beat; assert the terminal shape is exactly one Ready + no DLQ.
	require.Empty(t, dlqList(t, dlq, "team-a"), "a transient blip must NOT be dead-lettered")
	ready, failed := invocationsByPhase(t, st)
	require.Equal(t, 1, ready, "exactly one Ready Invocation at the terminal outcome")
	require.Equal(t, 0, failed, "no Failed Invocation for a delivery that recovered")
	require.Equal(t, 2, inv.count(), "one failure + one success")
}

// heldRetryInvoker fails the inline attempt, then holds the first retry attempt until release (or until its context
// ends, as an HTTP POST does), recording the context that attempt was given.
type heldRetryInvoker struct {
	mu      sync.Mutex
	calls   int
	retry   context.Context
	entered chan struct{}
	release chan struct{}
}

func (h *heldRetryInvoker) Invoke(ctx context.Context, _ v1.NamespaceName, _ v1.ObjectName, _ eventing.CloudEvent) error {
	h.mu.Lock()
	h.calls++
	call := h.calls
	if call == 2 {
		h.retry = ctx
	}
	h.mu.Unlock()
	switch call {
	case 1:
		return fault.Unavailablef("test.invoke", "target returned 500")
	case 2:
		close(h.entered)
		select {
		case <-ctx.Done():
			return fault.Unavailablef("test.invoke", "POST: %v", ctx.Err())
		case <-h.release:
			return nil
		}
	}
	return nil
}

func (h *heldRetryInvoker) retryCtx() context.Context {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.retry
}

// Shutdown drains the retry workers (ADR-0118 §6): a retry attempt already in flight runs to its end with a live
// context, so the target's answer is recorded. Handing it the cancelled platform context aborted the POST the target
// was already serving: the delivery was then dropped with no record, or, on its last attempt, dead-lettered although
// the target had run it (issue #145).
func TestIssue145_ShutdownLetsInflightRetryFinish(t *testing.T) {
	for _, attempts := range []int{2, 3} {
		t.Run(fmt.Sprintf("attempts=%d", attempts), func(t *testing.T) {
			inv := &heldRetryInvoker{entered: make(chan struct{}), release: make(chan struct{})}
			st := store.New(memory.New())
			fan := eventing.NewFanout()
			dlq := dlmemory.New()
			r, err := sensor.NewReconciler(sensor.Deps{Store: st, Subscriber: fan, Invoker: inv, DeadLetters: dlq, DeliveryAttempts: attempts})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			drained := make(chan struct{})
			go func() {
				r.RunRetryWorkers(ctx)
				close(drained)
			}()
			createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
				[]v1.Action{{Name: "notify", On: "d", Function: "mailer"}})
			_, err = r.Reconcile(context.Background(), reqOf("s"))
			require.NoError(t, err)
			fire(t, fan, "git", "push", "")

			select {
			case <-inv.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the retry attempt never started")
			}
			cancel()
			require.NoError(t, inv.retryCtx().Err(), "shutdown cancelled the retry attempt in flight instead of letting it finish")
			close(inv.release)
			select {
			case <-drained:
			case <-time.After(5 * time.Second):
				t.Fatal("RunRetryWorkers did not return after the in-flight attempt finished")
			}

			ready, failed := invocationsByPhase(t, st)
			require.Equal(t, 1, ready, "the delivery the target served is recorded Ready")
			require.Equal(t, 0, failed)
			require.Empty(t, dlqList(t, dlq, "team-a"), "a delivery the target served is not dead-lettered")
		})
	}
}

// scenario: replay-restarts-action — a stored DeadLetter with a now-healthy target replays against the LIVE
// Sensor spec, succeeds, and the entry is removed.
func TestReplayRestartsAction(t *testing.T) {
	inv := &scriptedInvoker{failFirst: -1} // fail until we flip it
	st, fan, dlq, r := dlqHarness(t, inv, 3)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "notify", On: "d", Function: "mailer"}})
	_, _ = r.Reconcile(context.Background(), reqOf("s"))
	fire(t, fan, "git", "push", "")
	require.Eventually(t, func() bool { return len(dlqList(t, dlq, "team-a")) == 1 }, 3*time.Second, 20*time.Millisecond)
	id := dlqList(t, dlq, "team-a")[0].ID

	inv.mu.Lock()
	inv.failFirst = 0 // target now healthy
	inv.mu.Unlock()

	require.NoError(t, r.Replay(context.Background(), "team-a", id), "replay against the fixed target succeeds")
	require.Empty(t, dlqList(t, dlq, "team-a"), "a replayed-and-delivered entry is removed")
}

// scenario: replay-refails-redead-letters — replaying a still-broken target fails again and the entry
// REMAINS (Attempts reset) — replay is idempotent and never loses the event.
func TestReplayRefailsRedeadLetters(t *testing.T) {
	inv := &scriptedInvoker{failFirst: -1} // stays broken
	st, fan, dlq, r := dlqHarness(t, inv, 3)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "notify", On: "d", Function: "mailer"}})
	_, _ = r.Reconcile(context.Background(), reqOf("s"))
	fire(t, fan, "git", "push", "")
	require.Eventually(t, func() bool { return len(dlqList(t, dlq, "team-a")) == 1 }, 3*time.Second, 20*time.Millisecond)
	id := dlqList(t, dlq, "team-a")[0].ID

	err := r.Replay(context.Background(), "team-a", id)
	require.Error(t, err, "replay of a still-broken target returns the delivery error")
	items := dlqList(t, dlq, "team-a")
	require.Len(t, items, 1, "the entry remains dead-lettered")
	require.Equal(t, id, items[0].ID, "same entry, re-parked")
	require.Equal(t, 0, items[0].Attempts, "Attempts reset for the re-parked entry")
}

// scenario: replay-not-found — replaying a Sensor/action that no longer exists ⇒ NotFound (the operator
// discards); an absent id ⇒ NotFound.
func TestReplayNotFound(t *testing.T) {
	inv := &scriptedInvoker{failFirst: -1}
	st, fan, dlq, r := dlqHarness(t, inv, 3)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "notify", On: "d", Function: "mailer"}})
	_, _ = r.Reconcile(context.Background(), reqOf("s"))
	fire(t, fan, "git", "push", "")
	require.Eventually(t, func() bool { return len(dlqList(t, dlq, "team-a")) == 1 }, 3*time.Second, 20*time.Millisecond)
	id := dlqList(t, dlq, "team-a")[0].ID

	// Absent id ⇒ NotFound.
	require.Equal(t, fault.NotFound, fault.KindOf(r.Replay(context.Background(), "team-a", "does-not-exist")))
	// Delete the Sensor; the stored DeadLetter can no longer resolve its live action ⇒ NotFound.
	cur, _ := st.Get(context.Background(), v1.KindSensor.GVK(), "team-a", "s")
	require.NoError(t, st.Delete(context.Background(), v1.KindSensor.GVK(), "team-a", "s", cur.GetObjectMeta().ResourceVersion))
	require.Equal(t, fault.NotFound, fault.KindOf(r.Replay(context.Background(), "team-a", id)))
}

// scenario: discard-removes — a stored DeadLetter is deleted (the operator's `dlq discard`) and no longer
// listed.
func TestDiscardRemoves(t *testing.T) {
	inv := &scriptedInvoker{failFirst: -1}
	st, fan, dlq, r := dlqHarness(t, inv, 3)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "notify", On: "d", Function: "mailer"}})
	_, _ = r.Reconcile(context.Background(), reqOf("s"))
	fire(t, fan, "git", "push", "")
	require.Eventually(t, func() bool { return len(dlqList(t, dlq, "team-a")) == 1 }, 3*time.Second, 20*time.Millisecond)
	id := dlqList(t, dlq, "team-a")[0].ID

	require.NoError(t, dlq.Delete(context.Background(), "team-a", id))
	require.Empty(t, dlqList(t, dlq, "team-a"), "a discarded entry is gone")
}

// scenario: driver-independent — the whole dead-letter → list → replay path works identically on the
// in-memory bus (the ADR-0108 Fanout — genuinely NO JetStream) regardless of the DLQ store driver: it runs
// over BOTH the in-memory store and the Badger-in-memory store, proving the guarantee never touches the bus.
func TestDriverIndependent(t *testing.T) {
	drivers := map[string]func(t *testing.T) deadletter.Store{
		"memory": func(*testing.T) deadletter.Store { return dlmemory.New() },
		"badger-in-mem": func(t *testing.T) deadletter.Store {
			s, err := dlbadger.New(dlbadger.Config{InMemory: true})
			require.NoError(t, err)
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	}
	for name, newStore := range drivers {
		t.Run(name, func(t *testing.T) {
			st := store.New(memory.New())
			fan := eventing.NewFanout() // the in-process bus — no NATS/JetStream anywhere in this path
			dlq := newStore(t)
			inv := &scriptedInvoker{failFirst: -1}
			r, err := sensor.NewReconciler(sensor.Deps{Store: st, Subscriber: fan, Invoker: inv, DeadLetters: dlq, DeliveryAttempts: 3})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			go r.RunRetryWorkers(ctx)
			t.Cleanup(cancel)

			createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
				[]v1.Action{{Name: "notify", On: "d", Function: "mailer"}})
			_, _ = r.Reconcile(context.Background(), reqOf("s"))
			fire(t, fan, "git", "push", "")

			// dead-letters …
			require.Eventually(t, func() bool { return len(dlqList(t, dlq, "team-a")) == 1 }, 3*time.Second, 20*time.Millisecond)
			id := dlqList(t, dlq, "team-a")[0].ID
			// … lists …
			require.Len(t, dlqList(t, dlq, "team-a"), 1)
			// … and replays (fixed target) — removed on success.
			inv.mu.Lock()
			inv.failFirst = 0
			inv.mu.Unlock()
			require.NoError(t, r.Replay(context.Background(), "team-a", id))
			require.Empty(t, dlqList(t, dlq, "team-a"))
		})
	}
}

// scenario: retention-evicts — the retention sweep evicts past-TTL and over-cap entries (both knobs), the
// rest remain (ADR-0118 §5, SweepExpired: global TTL + per-namespace cap).
func TestRetentionEvicts(t *testing.T) {
	dlq := dlmemory.New()
	ctx := context.Background()
	now := time.Now().UTC()
	// team-a: 3 entries — one past TTL, two within; cap 1 ⇒ the sweep keeps only the newest survivor.
	put := func(ns v1.NamespaceName, id string, at time.Time) {
		require.NoError(t, dlq.Put(ctx, deadletter.DeadLetter{ID: id, Namespace: ns, Sensor: "s", Action: "a", FailedAt: at}))
	}
	put("team-a", "01A", now.Add(-48*time.Hour)) // past TTL
	put("team-a", "01B", now.Add(-2*time.Minute))
	put("team-a", "01C", now.Add(-1*time.Minute))
	put("team-b", "01A", now.Add(-1*time.Minute)) // under cap, within TTL — untouched

	n, err := dlq.SweepExpired(ctx, 24*time.Hour, 1) // TTL 24h + per-ns cap 1
	require.NoError(t, err)
	require.Equal(t, 2, n, "evict the past-TTL entry AND the over-cap oldest survivor")
	teamA := dlqList(t, dlq, "team-a")
	require.Len(t, teamA, 1)
	require.Equal(t, "01C", teamA[0].ID, "the newest survivor remains")
	require.Len(t, dlqList(t, dlq, "team-b"), 1, "another namespace under cap/TTL is untouched")
}

func TestIssue174_ReplayRecordsInvocation(t *testing.T) {
	inv := &scriptedInvoker{failFirst: -1}
	st, fan, dlq, r := dlqHarness(t, inv, 1)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "notify", On: "d", Function: "mailer"}})
	_, err := r.Reconcile(context.Background(), reqOf("s"))
	require.NoError(t, err)
	fire(t, fan, "git", "push", "")
	require.Len(t, dlqList(t, dlq, "team-a"), 1)
	id := dlqList(t, dlq, "team-a")[0].ID

	require.Error(t, r.Replay(context.Background(), "team-a", id))
	ready, failed := invocationsByPhase(t, st)
	require.Equal(t, 0, ready)
	require.Equal(t, 2, failed, "a failed replay is an action delivery: it records a Failed Invocation")

	inv.mu.Lock()
	inv.failFirst = 0
	inv.mu.Unlock()
	require.NoError(t, r.Replay(context.Background(), "team-a", id))
	ready, failed = invocationsByPhase(t, st)
	require.Equal(t, 1, ready, "a successful replay records a Ready Invocation")
	require.Equal(t, 2, failed)
}
