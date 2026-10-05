package sensor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

func unitFor(sensor, action, fn string) delivery {
	return delivery{ns: "team-a", sensor: v1.ObjectName(sensor), action: v1.Action{Name: v1.ObjectName(action), Function: v1.ObjectName(fn)}}
}

func mustGet(t *testing.T, q *retryQueue) pending {
	t.Helper()
	p, shutdown := q.get()
	require.False(t, shutdown)
	return p
}

func (q *retryQueue) stateOf(id string) (unitState, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	u, ok := q.units[id]
	if !ok {
		return 0, false
	}
	return u.state, true
}

func (q *retryQueue) queuedFor(k sensorKey) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.sensors[k])
}

func (q *retryQueue) slotsInUse(t actionTarget) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if l := q.lanes[t]; l != nil {
		return l.inFlight
	}
	return 0
}

func (q *retryQueue) ringLen() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.ring)
}

// A unit whose backoff elapsed while every slot was busy is only queued, not in flight: once the queue shuts
// down it is not started, so shutdown waits for the attempts in flight alone (ADR-0118 §6, issue #347).
func TestIssue347_ShutdownStartsNoDueUnit(t *testing.T) {
	t.Parallel()
	q := newRetryQueue(time.Hour, time.Hour, 4, 4096)
	require.NoError(t, q.enqueue("due", unitFor("s", "a", "f")))
	mustGet(t, q)
	ok, _ := q.reschedule("due", 1)
	require.True(t, ok)
	q.ready("due")
	q.shutDown(time.Now())
	p, shutdown := q.get()
	require.True(t, shutdown, "get handed out queued unit %q after the queue shut down", p.id)
}

// Each unit is in exactly one state, and every call moves it and its slot as ADR-0156 §3 tabulates.
func TestRetryQueueOneStatePerUnit(t *testing.T) {
	t.Parallel()
	k := sensorKey{"team-a", "s"}
	f := targetOf(unitFor("s", "a", "f"))

	t.Run("withdrawn then readied is handed out once, as withdrawn", func(t *testing.T) {
		t.Parallel()
		q := newRetryQueue(time.Hour, time.Hour, 4, 4096)
		require.NoError(t, q.enqueue("u", unitFor("s", "a", "f")))
		mustGet(t, q)
		ok, _ := q.reschedule("u", 1)
		require.True(t, ok)
		q.withdraw(k, nil)
		q.ready("u")
		p := mustGet(t, q)
		require.Equal(t, "u", p.id)
		require.True(t, p.withdrawn)
		require.Equal(t, 1, p.attempts)
		require.Zero(t, q.ringLen(), "nothing else to hand out")
	})

	t.Run("ready on an in-flight unit is a no-op", func(t *testing.T) {
		t.Parallel()
		q := newRetryQueue(time.Hour, time.Hour, 4, 4096)
		require.NoError(t, q.enqueue("u", unitFor("s", "a", "f")))
		mustGet(t, q)
		q.ready("u")
		st, _ := q.stateOf("u")
		require.Equal(t, unitInFlight, st)
		require.Zero(t, q.ringLen())
	})

	t.Run("reschedule frees the slot in the call", func(t *testing.T) {
		t.Parallel()
		q := newRetryQueue(time.Hour, time.Hour, 1, 4096)
		require.NoError(t, q.enqueue("a", unitFor("s", "a", "f")))
		require.NoError(t, q.enqueue("b", unitFor("s", "a", "f")))
		require.Equal(t, "a", mustGet(t, q).id)
		require.Zero(t, q.ringLen(), "the target is at its cap")
		ok, stale := q.reschedule("a", 1)
		require.True(t, ok)
		require.False(t, stale)
		require.Equal(t, 1, q.ringLen(), "the freed slot re-offers the target")
		require.Equal(t, "b", mustGet(t, q).id)
	})

	t.Run("stale reschedule is refused", func(t *testing.T) {
		t.Parallel()
		q := newRetryQueue(time.Hour, time.Hour, 4, 4096)
		require.NoError(t, q.enqueue("u", unitFor("s", "a", "f")))
		mustGet(t, q)
		q.withdraw(k, nil)
		ok, stale := q.reschedule("u", 1)
		require.False(t, ok)
		require.True(t, stale)
		st, _ := q.stateOf("u")
		require.Equal(t, unitInFlight, st, "left in flight for the caller to park and forget")
	})

	t.Run("forget of a withdrawn unit leaves the Sensor count unchanged", func(t *testing.T) {
		t.Parallel()
		q := newRetryQueue(time.Hour, time.Hour, 4, 4096)
		require.NoError(t, q.enqueue("old", unitFor("s", "a", "f")))
		require.NoError(t, q.enqueue("kept", unitFor("s", "b", "h")))
		q.withdraw(k, func(a v1.Action) bool { return a.Name == "b" })
		require.Equal(t, 1, q.queuedFor(k))
		var withdrawn pending
		for withdrawn.id == "" {
			if p := mustGet(t, q); p.withdrawn {
				withdrawn = p
			}
		}
		require.Equal(t, "old", withdrawn.id)
		q.forget(withdrawn.id)
		require.Equal(t, 1, q.queuedFor(k))
	})

	t.Run("parking frees the withdrawn entry's slot or the target's", func(t *testing.T) {
		t.Parallel()
		q := newRetryQueue(time.Hour, time.Hour, 1, 4096)
		require.NoError(t, q.enqueue("w", unitFor("s", "a", "f")))
		q.withdraw(k, nil)
		require.True(t, mustGet(t, q).withdrawn)
		require.Equal(t, 1, q.slotsInUse(actionTarget{}))
		q.forget("w")
		require.Zero(t, q.slotsInUse(actionTarget{}))

		require.NoError(t, q.enqueue("s", unitFor("s", "a", "f")))
		require.False(t, mustGet(t, q).withdrawn)
		q.withdraw(k, nil)
		_, stale := q.reschedule("s", 1)
		require.True(t, stale)
		require.Equal(t, 1, q.slotsInUse(f))
		q.forget("s")
		require.Zero(t, q.slotsInUse(f))
	})

	t.Run("shutDown returns a withdrawn unit as withdrawn", func(t *testing.T) {
		t.Parallel()
		q := newRetryQueue(time.Hour, time.Hour, 4, 4096)
		require.NoError(t, q.enqueue("u", unitFor("s", "a", "f")))
		q.withdraw(k, nil)
		got := q.shutDown(time.Now())
		require.Len(t, got, 1)
		require.True(t, got[0].withdrawn)
	})
}
