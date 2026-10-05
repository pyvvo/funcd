// Package httpx gives each HTTP caller a transport of its own. A CloseIdleConnections on the process-wide
// http.DefaultTransport, which every httptest.Server.Close also does, breaks a caller's connections parked there
// (#287, #531, #564), so production code never uses http.DefaultClient or http.DefaultTransport (a forbidigo rule
// in .golangci.yml enforces it).
package httpx

import (
	"io"
	"net"
	"net/http"
	"time"
)

// Transport returns a new transport with http.DefaultTransport's settings, the environment proxy included, and a
// connection pool of its own, for destinations off this node. A pool whose every destination runs on this node uses
// NodeTransport (ADR-0155).
func Transport() *http.Transport {
	if t, ok := http.DefaultTransport.(*http.Transport); ok { //nolint:forbidigo // copies the settings, never the pool
		return t.Clone()
	}
	// Something replaced http.DefaultTransport with another RoundTripper: use the settings net/http gives it.
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// Client returns a client over a new Transport; a zero timeout means none, as for http.DefaultClient.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: Transport()}
}

// NodeTransport returns a new transport, with a connection pool of its own, for calls that never leave this node:
// function and pool workers, provider engines and the daemon's own node-private servers (ADR-0155). Concurrent calls
// to one host reuse keep-alive connections (ADR-0041), and no proxy is used.
func NodeTransport() *http.Transport {
	tr := Transport()
	tr.Proxy = nil
	tr.MaxIdleConns = 512
	tr.MaxIdleConnsPerHost = 256
	tr.IdleConnTimeout = 90 * time.Second
	tr.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return tr
}

// NodeClient returns a client over a new NodeTransport; a zero timeout means none.
func NodeClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: NodeTransport()}
}

// DrainLimit is the most of an unread response body that CloseBody discards.
const DrainLimit = 4 << 10

// CloseBody discards at most DrainLimit bytes of body and closes it. A body read to EOF returns its keep-alive
// connection to the pool; a longer one closes the connection, so an unbounded answer is never read in full.
func CloseBody(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, DrainLimit))
	_ = body.Close()
}
