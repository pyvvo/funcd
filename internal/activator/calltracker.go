package activator

import (
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/httpx"
)

// idleRetention bounds the tracker's map: an upstream with no call in flight and no hand-out for this long is
// forgotten, whether or not anything asked Idle about it.
const idleRetention = time.Minute

// CallTracker counts the calls made to each worker, keyed by the upstream's host:port, so the Function reconciler can
// tell when a worker of a demoted revision is drained (ADR-0143). Every caller that reaches workers — the activator's
// proxy, the workflow dispatcher, the Sensor invoker — sends through Wrap, and the resolver records each hand-out.
// Safe for concurrent use.
type CallTracker struct {
	clock     clock.Clock
	mu        sync.Mutex
	hosts     map[string]*hostCalls
	lastPurge time.Time
}

type hostCalls struct {
	inFlight  int
	handedOut time.Time
}

// NewCallTracker builds a tracker on c (the system clock when nil).
func NewCallTracker(c clock.Clock) *CallTracker {
	if c == nil {
		c = clock.System()
	}
	return &CallTracker{clock: c, hosts: map[string]*hostCalls{}}
}

// Wrap returns a RoundTripper that counts each request to its URL's host:port from RoundTrip until the response body
// is closed, or until RoundTrip fails. A nil rt wraps a new httpx.NodeTransport.
func (t *CallTracker) Wrap(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = httpx.NodeTransport()
	}
	return &countingTransport{t: t, rt: rt}
}

// HandedOut records that the resolver returned upstream to a caller.
func (t *CallTracker) HandedOut(upstream string) {
	host := hostOf(upstream)
	if host == "" {
		return
	}
	now := t.clock.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entry(host).handedOut = now
	if now.Sub(t.lastPurge) < idleRetention {
		return
	}
	for k, h := range t.hosts {
		if h.inFlight == 0 && now.Sub(h.handedOut) >= idleRetention {
			delete(t.hosts, k)
		}
	}
	t.lastPurge = now
}

// Idle reports whether no call to upstream is in flight and it was not handed out within settle. An idle upstream is
// forgotten.
func (t *CallTracker) Idle(upstream string, settle time.Duration) bool {
	host := hostOf(upstream)
	now := t.clock.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.hosts[host]
	if !ok {
		return true
	}
	if h.inFlight > 0 || now.Sub(h.handedOut) < settle {
		return false
	}
	delete(t.hosts, host)
	return true
}

func (t *CallTracker) begin(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entry(host).inFlight++
}

func (t *CallTracker) end(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if h, ok := t.hosts[host]; ok && h.inFlight > 0 {
		h.inFlight--
	}
}

// entry returns host's counters, creating them; the caller holds t.mu.
func (t *CallTracker) entry(host string) *hostCalls {
	h, ok := t.hosts[host]
	if !ok {
		h = &hostCalls{}
		t.hosts[host] = h
	}
	return h
}

func hostOf(upstream string) string {
	u, err := url.Parse(upstream)
	if err != nil {
		return ""
	}
	return u.Host
}

type countingTransport struct {
	t  *CallTracker
	rt http.RoundTripper
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	c.t.begin(host)
	resp, err := c.rt.RoundTrip(req)
	if err != nil {
		c.t.end(host)
		return nil, err
	}
	done := sync.OnceFunc(func() { c.t.end(host) })
	// An upgraded connection (101) keeps a writable body, which httputil.ReverseProxy needs to tunnel it.
	if rwc, ok := resp.Body.(io.ReadWriteCloser); ok {
		resp.Body = countedConn{ReadWriteCloser: rwc, done: done}
	} else {
		resp.Body = countedBody{ReadCloser: resp.Body, done: done}
	}
	return resp, nil
}

// countedBody ends its call's count when the body is closed.
type countedBody struct {
	io.ReadCloser
	done func()
}

func (b countedBody) Close() error {
	defer b.done()
	return b.ReadCloser.Close()
}

// countedConn is countedBody for an upgraded connection.
type countedConn struct {
	io.ReadWriteCloser
	done func()
}

func (c countedConn) Close() error {
	defer c.done()
	return c.ReadWriteCloser.Close()
}
