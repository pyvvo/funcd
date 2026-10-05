package controlplane

import (
	"context"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// nsLocks: per-namespace admission lock (ADR-0147); an entry lives while it has a holder or waiter.
// Namespaces never wait on each other.
type nsLocks struct {
	mu sync.Mutex
	m  map[v1.NamespaceName]*nsLock
}

type nsLock struct {
	slot chan struct{} // capacity 1; holding the slot is holding the lock
	refs int           // holder + waiters; guarded by nsLocks.mu
}

func newNSLocks() *nsLocks { return &nsLocks{m: map[v1.NamespaceName]*nsLock{}} }

// lock waits for ns's slot or ctx's end. On ctx's end it returns fault.Unavailable and holds nothing.
func (l *nsLocks) lock(ctx context.Context, ns v1.NamespaceName) (unlock func(), err error) {
	l.mu.Lock()
	e, ok := l.m[ns]
	if !ok {
		e = &nsLock{slot: make(chan struct{}, 1)}
		l.m[ns] = e
	}
	e.refs++
	l.mu.Unlock()
	select {
	case e.slot <- struct{}{}:
		return func() {
			<-e.slot
			l.release(ns, e)
		}, nil
	case <-ctx.Done():
		l.release(ns, e)
		return nil, fault.Wrapf(ctx.Err(), fault.Unavailable, "controlplane.admit", "wait for the admission lock of namespace %q", ns)
	}
}

// release drops one reference to e and removes ns's entry when none is left.
func (l *nsLocks) release(ns v1.NamespaceName, e *nsLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.refs--
	if e.refs == 0 {
		delete(l.m, ns)
	}
}
