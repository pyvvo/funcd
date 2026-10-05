package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

func (l *nsLocks) entries() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

// Namespaces never wait on each other: holding one namespace's lock leaves another's free.
func TestNSLocksNamespacesIndependent(t *testing.T) {
	l := newNSLocks()
	unlockA, err := l.lock(context.Background(), "a")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlockB, err := l.lock(ctx, "b")
	require.NoError(t, err, "b is not blocked by a's holder")
	unlockB()
	unlockA()
	assert.Zero(t, l.entries())
}

// A wait ended by its context is fault.Unavailable and takes nothing: the holder keeps the slot, the next
// waiter gets it once the holder unlocks, and the map empties.
func TestNSLocksCancelledWaitUnavailable(t *testing.T) {
	l := newNSLocks()
	unlock, err := l.lock(context.Background(), "a")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = l.lock(ctx, "a")
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	assert.Contains(t, err.Error(), `wait for the admission lock of namespace "a"`)

	short, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	_, err = l.lock(short, "a")
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "the holder still has the slot")

	got := make(chan func(), 1)
	go func() {
		u, lerr := l.lock(context.Background(), "a")
		if lerr == nil {
			got <- u
		}
	}()
	unlock()
	select {
	case u := <-got:
		u()
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter never got the slot")
	}
	assert.Zero(t, l.entries(), "the map empties once nothing holds or waits")
}
