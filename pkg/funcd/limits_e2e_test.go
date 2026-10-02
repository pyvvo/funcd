//go:build e2e

package funcd_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/pkg/funcd"
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

// Issue #87: the ingress limiter guards the data-plane listener (ADR-0112), not internal fn-to-fn
// invokes (ADR-0064). front's nested invoke of greeter must neither take the external call's
// in-flight slot nor draw on greeter's rate bucket.
func TestIssue87_FnToFnInvokeBypassesIngressLimits(t *testing.T) {
	deploy := func(t *testing.T, cfg limit.Config) string {
		c, dpURL := shimPlatformOCI(t, funcd.WithLimits(cfg))
		exDir := tsExample(t, "fn-to-fn")
		layout := t.TempDir()
		gRef, gDig := pushExampleFn(t, layout, exDir, "greeter")
		fRef, fDig := pushExampleFn(t, layout, exDir, "front")
		applyFnObj(t, c, loadFn(t, "greeter.yaml", gRef, gDig))
		applyFnObj(t, c, loadFn(t, "front.yaml", fRef, fDig))
		waitReady(t, c, "greeter", "front")
		return dpURL
	}
	post := func(t *testing.T, url string) (int, string) {
		resp, err := http.Post(url, "application/json", strings.NewReader(`{"data":{"name":"funcd"}}`))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	t.Run("maxInFlight", func(t *testing.T) {
		dpURL := deploy(t, limit.Config{MaxInFlight: 1})
		for i := 0; i < 3; i++ {
			code, body := post(t, dpURL+"/function/front")
			require.Equal(t, http.StatusOK, code, "call %d: the nested invoke does not count against maxInFlight: %s", i, body)
			require.Contains(t, body, "Hello, funcd!")
		}
	})

	t.Run("rate", func(t *testing.T) {
		dpURL := deploy(t, limit.Config{RatePerMin: 1, Burst: 1, Key: limit.KeyFunction})
		code, body := post(t, dpURL+"/function/greeter")
		require.Equal(t, http.StatusOK, code, "the external call takes greeter's only token: %s", body)
		code, body = post(t, dpURL+"/function/front")
		require.Equal(t, http.StatusOK, code, "the nested invoke of greeter is not rate-limited: %s", body)
		require.Contains(t, body, "Hello, funcd!")
	})
}
