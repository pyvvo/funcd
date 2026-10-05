// Package stallregistry serves an OCI registry that accepts every request and never answers it (#697).
package stallregistry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Start serves the registry over TLS until the test ends and returns its host and the count of requests for a
// repository; a request waits until its client abandons it or the test ends. The registry client copies
// http.DefaultTransport's TLS config (httpx.Transport), so Start trusts the registry there, and gives the test an empty
// credential store: the test must not run in parallel with another one.
func Start(t *testing.T) (host string, requests func(repo string) int) {
	t.Helper()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	var mu sync.Mutex
	counts := map[string]int{}
	release := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		repo, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v2/"), "/")
		mu.Lock()
		counts[repo]++
		mu.Unlock()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	srv.StartTLS()
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	tr := http.DefaultTransport.(*http.Transport) //nolint:forbidigo // the registry client copies its TLS config
	trust := tr.TLSClientConfig
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	t.Cleanup(func() { tr.TLSClientConfig = trust })
	return strings.TrimPrefix(srv.URL, "https://"), func(repo string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[repo]
	}
}
