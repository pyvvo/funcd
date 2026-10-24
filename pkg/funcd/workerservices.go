package funcd

import (
	"context"
	"sync"
)

// workerServices runs the services the workers use (the S3 gateway, the egress gateway, the DNS forwarder) on a
// context that Run's does not end, so they serve through the data-plane drain and while the runtime stops the
// workers; Shutdown stops them after that (#1232).
type workerServices struct {
	mu      sync.Mutex
	stopped bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// start runs serve in its own goroutine on the services' context, which keeps parent's values but not its end. After
// stop it starts nothing.
func (s *workerServices) start(parent context.Context, serve func(context.Context)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.WithoutCancel(parent))
	}
	ctx := s.ctx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		serve(ctx)
	}()
}

// stop ends the services' context and waits, until ctx ends, for every service to return.
func (s *workerServices) stop(ctx context.Context) {
	s.mu.Lock()
	s.stopped = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
