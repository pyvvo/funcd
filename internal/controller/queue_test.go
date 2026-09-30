package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

func key(name string) Request {
	return Request{GVK: v1.KindConfigMap.GVK(), Namespace: "default", Name: v1.ObjectName(name)}
}

// scenario: workqueue-dedup — many adds of the same key collapse to one Get, and a
// key re-added while processing is re-queued exactly once on Done.
func TestScenarioWorkqueueDedup(t *testing.T) {
	t.Parallel()
	q := newQueue(time.Millisecond, time.Second)
	k := key("a")

	for range 5 {
		q.Add(k)
	}
	require.Equal(t, 1, q.Len(), "5 adds deduplicate to 1")

	got, shutdown := q.Get()
	require.False(t, shutdown)
	require.Equal(t, k, got)
	require.Equal(t, 0, q.Len())

	// Re-add while processing: marked dirty but not queued until Done.
	q.Add(k)
	require.Equal(t, 0, q.Len(), "no concurrent re-queue while processing")
	q.Done(k)
	require.Equal(t, 1, q.Len(), "re-queued once on Done")
}

// backoff grows exponentially and caps at max (deterministic — no timing).
func TestBackoffGrowth(t *testing.T) {
	t.Parallel()
	q := newQueue(10*time.Millisecond, 100*time.Millisecond)
	require.Equal(t, 10*time.Millisecond, q.backoff(1))
	require.Equal(t, 20*time.Millisecond, q.backoff(2))
	require.Equal(t, 40*time.Millisecond, q.backoff(3))
	require.Equal(t, 80*time.Millisecond, q.backoff(4))
	require.Equal(t, 100*time.Millisecond, q.backoff(5), "capped at max")
	require.Equal(t, 100*time.Millisecond, q.backoff(20), "stays capped")
}

// ShutDown unblocks a waiting Get.
func TestQueueShutdownUnblocksGet(t *testing.T) {
	t.Parallel()
	q := newQueue(time.Millisecond, time.Second)
	done := make(chan bool, 1)
	go func() {
		_, shutdown := q.Get()
		done <- shutdown
	}()
	time.Sleep(20 * time.Millisecond)
	q.ShutDown()
	select {
	case shutdown := <-done:
		require.True(t, shutdown)
	case <-time.After(time.Second):
		t.Fatal("ShutDown did not unblock Get")
	}
}

// An earlier AddAfter replaces the pending one, and the replaced timer never adds the key (ADR-0142).
func TestAddAfterEarliestWins(t *testing.T) {
	t.Parallel()
	q := newQueue(time.Millisecond, time.Second)
	k := key("a")

	q.AddAfter(k, 300*time.Millisecond)
	q.AddAfter(k, 20*time.Millisecond)
	require.Eventually(t, func() bool { return q.Len() == 1 }, 150*time.Millisecond, 5*time.Millisecond)
	got, _ := q.Get()
	q.Done(got)

	time.Sleep(400 * time.Millisecond)
	require.Equal(t, 0, q.Len(), "the replaced 300ms timer does not add the key")
}

// A later AddAfter is dropped while an earlier one is pending (ADR-0142).
func TestAddAfterLaterIsDropped(t *testing.T) {
	t.Parallel()
	q := newQueue(time.Millisecond, time.Second)
	k := key("a")

	q.AddAfter(k, 20*time.Millisecond)
	q.AddAfter(k, 300*time.Millisecond)
	require.Eventually(t, func() bool { return q.Len() == 1 }, 150*time.Millisecond, 5*time.Millisecond)
	got, _ := q.Get()
	q.Done(got)

	time.Sleep(400 * time.Millisecond)
	require.Equal(t, 0, q.Len(), "the later delay was dropped, so nothing fires at 300ms")
}
