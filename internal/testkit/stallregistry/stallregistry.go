// Package stallregistry serves an OCI registry that accepts every request and stops answering it (#697).
package stallregistry

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// BodySize is the Content-Length of each response StartBody stalls.
const BodySize = 500

// Start serves the registry over TLS until the test ends and returns its host and the count of requests for a
// repository; a request waits until its client abandons it or the test ends. The registry client copies
// http.DefaultTransport's TLS config (httpx.Transport), so Start trusts the registry there, and gives the test an empty
// credential store: the test must not run in parallel with another one.
func Start(t *testing.T) (host string, requests func(repo string) int) {
	t.Helper()
	return serve(t, func(http.ResponseWriter, *http.Request) bool { return false })
}

// StartBody is Start for a registry that sends each response's headers and the first byte of its BodySize-byte body,
// then nothing more. A GET of manifest's digest gets manifest whole, so a pull reaches the blobs it names.
func StartBody(t *testing.T, manifest []byte) (host string) {
	t.Helper()
	whole := "/manifests/" + digest.FromBytes(manifest).String()
	host, _ = serve(t, func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
		if strings.HasSuffix(r.URL.Path, whole) {
			_, _ = w.Write(manifest)
			return true
		}
		w.Header().Set("Content-Length", strconv.Itoa(BodySize))
		_, _ = w.Write([]byte("{"))
		w.(http.Flusher).Flush()
		return false
	})
	return host
}

// serve counts each request and stalls it unless answer reports that it answered it whole.
func serve(t *testing.T, answer func(http.ResponseWriter, *http.Request) bool) (host string, requests func(repo string) int) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	var mu sync.Mutex
	counts := map[string]int{}
	release := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repo, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v2/"), "/")
		mu.Lock()
		counts[repo]++
		mu.Unlock()
		if answer(w, r) {
			return
		}
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
