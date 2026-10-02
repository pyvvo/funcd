//go:build e2e

package funcd_test

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario: over-rate-429-no-wake (e2e, F75/ADR-0112) — the ingress limiter runs on the real
// data-plane listener BEFORE dataplane.Handler, so an over-rate flood is 429'd; the requests that
// pass the limiter reach the data-plane (404 for an unknown function). A rejected request never
// reaches the activator (structurally: the limiter is the innermost middleware, before the handler
// that calls the activator) — the unit test asserts the zero-wake counter directly.
func TestScenarioE2ELimitsRateLimit(t *testing.T) {
	p := bringUp(t, funcd.WithLimits(limit.Config{RatePerMin: 60, Burst: 2, Key: limit.KeyClientIP}))
	base := "http://" + p.DataPlaneAddr()

	codes := map[int]int{}
	for i := 0; i < 8; i++ {
		resp, err := http.Get(base + "/function/nope")
		require.NoError(t, err)
		_ = resp.Body.Close()
		codes[resp.StatusCode]++
	}
	require.Equal(t, 2, codes[http.StatusNotFound], "the burst (2) passes the limiter and reaches the data plane (unknown fn → 404)")
	require.Equal(t, 6, codes[http.StatusTooManyRequests], "the rest are rejected by the limiter (429) before the handler")

	// A retry-after header is present on the 429s.
	resp, err := http.Get(base + "/function/nope")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.NotEmpty(t, resp.Header.Get("Retry-After"))
}

// scenario: over-size-413 (e2e) — a Content-Length over the cap is 413'd on the real listener.
func TestScenarioE2ELimitsBodySize(t *testing.T) {
	p := bringUp(t, funcd.WithLimits(limit.Config{MaxBodyBytes: 16}))
	base := "http://" + p.DataPlaneAddr()

	resp, err := http.Post(base+"/function/nope", "application/json", strings.NewReader(`{"data":"way too large for the sixteen byte cap"}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}

// TestIssue90_SilentBodyReleasesInFlightSlot — a client that sends its headers and then stops sending
// the body must not hold an ADR-0112 in-flight slot for as long as it keeps the connection open: the
// data-plane server's read deadline cuts the request, so the slot frees for other callers.
func TestIssue90_SilentBodyReleasesInFlightSlot(t *testing.T) {
	p := bringUp(t, funcd.WithLimits(limit.Config{MaxInFlight: 1}))
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	applyFn(t, c, "silent", v1.Scaling{}, 0, writeArtifact(t))

	conn, err := net.Dial("tcp", p.DataPlaneAddr())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = io.WriteString(conn, "POST /function/silent HTTP/1.1\r\nHost: funcd\r\nContent-Length: 100\r\n\r\nhello")
	require.NoError(t, err)

	other := "http://" + p.DataPlaneAddr() + "/function/nope"
	require.Eventually(t, func() bool { return postStatus(other) == http.StatusServiceUnavailable },
		5*time.Second, 20*time.Millisecond, "the silent request holds the only in-flight slot")
	require.Eventually(t, func() bool { return postStatus(other) == http.StatusNotFound },
		20*time.Second, 100*time.Millisecond, "the read deadline must cut the silent request and free its slot")
}

func postStatus(url string) int {
	resp, err := http.Post(url, "application/json", strings.NewReader(`{}`))
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
