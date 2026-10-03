// Package httpx gives each HTTP caller a transport of its own. A CloseIdleConnections on the process-wide
// http.DefaultTransport, which every httptest.Server.Close also does, breaks a caller's connections parked there
// (#287, #531, #564), so production code never uses http.DefaultClient or http.DefaultTransport (a forbidigo rule
// in .golangci.yml enforces it).
package httpx

import (
	"net/http"
	"time"
)

// Transport returns a new transport with http.DefaultTransport's settings and a connection pool of its own.
func Transport() *http.Transport {
	if t, ok := http.DefaultTransport.(*http.Transport); ok { //nolint:forbidigo // copies the settings, never the pool
		return t.Clone()
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}

// Client returns a client over a new Transport; a zero timeout means none, as for http.DefaultClient.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: Transport()}
}
