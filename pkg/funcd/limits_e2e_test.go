//go:build e2e

package funcd_test

import (
	"bufio"
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

// TestIssue300_ControlPlaneClosesSilentConns — an anonymous client that stops sending its body, or that
// keeps a keep-alive connection idle after one response, must not hold a control-plane connection (a
// goroutine and an fd) for as long as it likes: the server closes both.
func TestIssue300_ControlPlaneClosesSilentConns(t *testing.T) {
	p := bringUp(t)
	cases := map[string]string{
		"silent body":     "POST /apis/funcd.io/v1alpha1/namespaces/default/configmaps HTTP/1.1\r\nHost: funcd\r\nContent-Length: 100\r\n\r\nhello",
		"idle keep-alive": "GET /apis/funcd.io/v1alpha1/namespaces/default/configmaps HTTP/1.1\r\nHost: funcd\r\n\r\n",
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			conn, err := net.Dial("tcp", p.Addr())
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(20*time.Second)))
			_, err = io.WriteString(conn, req)
			require.NoError(t, err)

			br := bufio.NewReader(conn)
			resp, err := http.ReadResponse(br, nil)
			require.NoError(t, err, "the server must answer instead of waiting on the connection")
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			_, err = br.ReadByte()
			require.ErrorIs(t, err, io.EOF, "the server must close the connection")
		})
	}
}

func postStatus(url string) int {
	resp, err := http.Post(url, "application/json", strings.NewReader(`{}`))
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
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
