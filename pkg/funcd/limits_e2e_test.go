//go:build e2e

package funcd_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/internal/edge/limit"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
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
