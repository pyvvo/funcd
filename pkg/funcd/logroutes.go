package funcd

import (
	"context"
	"io"
	"sync"
)

// logRoutes tracks the per-instance funclog Route goroutines (ADR-0081), so Shutdown can let each one read its
// channel to the end and seal its segments before the sinks and the blob close.
type logRoutes struct {
	mu   sync.Mutex
	live map[*logRoute]struct{}
}

type logRoute struct {
	r    io.ReadCloser
	done chan struct{}
}

// start runs route over r in its own goroutine and closes r once route returns.
func (l *logRoutes) start(r io.ReadCloser, route func()) {
	lr := &logRoute{r: r, done: make(chan struct{})}
	l.mu.Lock()
	if l.live == nil {
		l.live = map[*logRoute]struct{}{}
	}
	l.live[lr] = struct{}{}
	l.mu.Unlock()
	go func() {
		defer func() {
			_ = r.Close()
			l.mu.Lock()
			delete(l.live, lr)
			l.mu.Unlock()
			close(lr.done)
		}()
		route()
	}()
}

// drain waits, until ctx ends, for every Route to reach the end of its channel. A channel whose read side can be
// half-closed (a socket) is half-closed first: the runtime's Close may leave its writer running (a containerd
// worker outlives the daemon), and on Linux a half-closed socket still yields what was written, then EOF.
func (l *logRoutes) drain(ctx context.Context) {
	l.mu.Lock()
	pending := make([]*logRoute, 0, len(l.live))
	for lr := range l.live {
		if hc, ok := lr.r.(interface{ CloseRead() error }); ok {
			_ = hc.CloseRead()
		}
		pending = append(pending, lr)
	}
	l.mu.Unlock()
	for _, lr := range pending {
		select {
		case <-lr.done:
		case <-ctx.Done():
			return
		}
	}
}
