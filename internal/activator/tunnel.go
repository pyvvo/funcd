package activator

import (
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// tunnelIdleTimeout is how long a 101 tunnel may carry no byte either way before funcd closes it (ADR-0181).
const tunnelIdleTimeout = 5 * time.Minute

// idleTunnel returns rt with the io.ReadWriteCloser body of a 101 response wrapped to close once idle passes
// with no byte read or written; every other response is returned unchanged.
func idleTunnel(rt http.RoundTripper, idle time.Duration) http.RoundTripper {
	return idleTunnelTransport{rt: rt, idle: idle}
}

type idleTunnelTransport struct {
	rt   http.RoundTripper
	idle time.Duration
}

func (t idleTunnelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.rt.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		return resp, err
	}
	if rwc, ok := resp.Body.(io.ReadWriteCloser); ok {
		resp.Body = newIdleBody(rwc, t.idle)
	}
	return resp, nil
}

// idleBody holds the tunnel in a field, never embedded, so io.Copy cannot reach the inner io.WriterTo or
// io.ReaderFrom and move bytes past the clock. Read and Write run on httputil.ReverseProxy's two copy goroutines.
type idleBody struct {
	rwc   io.ReadWriteCloser
	idle  time.Duration
	start time.Time
	last  atomic.Int64 // monotonic nanoseconds since start at the last Read or Write that moved a byte

	mu       sync.Mutex // orders timer and stopped against the timer's own callback
	timer    *time.Timer
	stopped  bool
	once     sync.Once
	closeErr error
}

func newIdleBody(rwc io.ReadWriteCloser, idle time.Duration) *idleBody {
	b := &idleBody{rwc: rwc, idle: idle, start: time.Now()}
	b.mu.Lock()
	b.timer = time.AfterFunc(idle, b.expire)
	b.mu.Unlock()
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.rwc.Read(p)
	if n > 0 {
		b.last.Store(int64(time.Since(b.start)))
	}
	return n, err
}

func (b *idleBody) Write(p []byte) (int, error) {
	n, err := b.rwc.Write(p)
	if n > 0 {
		b.last.Store(int64(time.Since(b.start)))
	}
	return n, err
}

// Close is idempotent: handleUpgradeResponse closes the back connection again after the tunnel ends.
func (b *idleBody) Close() error {
	b.once.Do(func() {
		b.mu.Lock()
		b.stopped = true
		b.timer.Stop()
		b.mu.Unlock()
		b.closeErr = b.rwc.Close()
	})
	return b.closeErr
}

// expire closes the tunnel when idle has passed since the last byte, and otherwise re-arms for the remainder.
func (b *idleBody) expire() {
	left := time.Duration(b.last.Load()) + b.idle - time.Since(b.start)
	if left <= 0 {
		_ = b.Close()
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.stopped {
		b.timer.Reset(left)
	}
}
