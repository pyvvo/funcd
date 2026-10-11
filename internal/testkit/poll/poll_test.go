package poll_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/testkit/poll"
)

func TestNeverStopsAtTheFirstTrue(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	require.False(t, poll.Never(func() bool { return calls.Add(1) == 3 }, time.Minute, time.Millisecond))
	require.Equal(t, int32(3), calls.Load())
}

func TestNeverHoldsForItsWindow(t *testing.T) {
	t.Parallel()
	start := time.Now()
	require.True(t, poll.Never(func() bool { return false }, 50*time.Millisecond, 5*time.Millisecond))
	require.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond)
}

// Issue 950: a check slower than the window has finished when Never returns; require.Never would leave it running.
func TestIssue950_NoCheckRunsAfterNeverReturns(t *testing.T) {
	t.Parallel()
	var running atomic.Int32
	slow := func() bool {
		running.Add(1)
		defer running.Add(-1)
		time.Sleep(30 * time.Millisecond)
		return false
	}
	require.True(t, poll.Never(slow, 10*time.Millisecond, time.Millisecond))
	require.Zero(t, running.Load(), "a check still runs after Never returned")
}
