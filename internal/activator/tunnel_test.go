package activator

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/testkit/wsupstream"
)

const testIdle = 300 * time.Millisecond

// tunnelProxy fronts up with an httputil.ReverseProxy set up as forward sets it, over idleTunnel at testIdle.
func tunnelProxy(t *testing.T, up *wsupstream.Server) *httptest.Server {
	t.Helper()
	target, err := url.Parse(up.URL)
	require.NoError(t, err)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1
	rp.Transport = idleTunnel(httpx.NodeTransport(), testIdle)
	srv := httptest.NewServer(rp)
	t.Cleanup(srv.Close)
	return srv
}

func dialTunnel(t *testing.T, srv *httptest.Server, query string) *websocket.Conn {
	t.Helper()
	conn, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/?"+query, "", srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// awaitEcho sends frame and reads until its echo, skipping push: frames.
func awaitEcho(t *testing.T, conn *websocket.Conn, frame string) {
	t.Helper()
	require.NoError(t, websocket.Message.Send(conn, frame))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	for {
		var reply string
		require.NoError(t, websocket.Message.Receive(conn, &reply))
		if strings.HasPrefix(reply, "push:") {
			continue
		}
		require.Equal(t, "echo:"+frame, reply)
		return
	}
}

// scenario: idle-tunnel-closed — with no byte either way, funcd closes the tunnel 300 ms to 1.3 s after the last
// byte, and both ends' pending reads fail.
func TestScenarioIdleTunnelClosed(t *testing.T) {
	t.Parallel()
	up := wsupstream.New(t)
	srv := tunnelProxy(t, up)
	start := time.Now()
	conn := dialTunnel(t, srv, "mode=sink")
	require.NoError(t, conn.SetReadDeadline(start.Add(5*time.Second)))

	var frame string
	err := websocket.Message.Receive(conn, &frame)
	closedAfter := time.Since(start)
	require.ErrorIs(t, err, io.EOF, "the client's pending read fails when funcd closes the tunnel")
	require.GreaterOrEqual(t, closedAfter, testIdle)
	require.LessOrEqual(t, closedAfter, testIdle+time.Second)
	require.Eventually(t, func() bool { return up.Ended() == 1 }, time.Second, 10*time.Millisecond,
		"the upstream's pending read fails too")
}

// scenario: active-tunnel-stays-open — a frame every 100 ms keeps the tunnel open past 1.5 s, every echo matched.
func TestScenarioActiveTunnelStaysOpen(t *testing.T) {
	t.Parallel()
	srv := tunnelProxy(t, wsupstream.New(t))
	conn := dialTunnel(t, srv, "mode=echo")
	until := time.Now().Add(1500 * time.Millisecond)
	for i := 0; time.Now().Before(until); i++ {
		awaitEcho(t, conn, "f"+strconv.Itoa(i))
		time.Sleep(100 * time.Millisecond)
	}
	awaitEcho(t, conn, "open")
}

// scenario: one-direction-is-traffic — bytes in one direction only keep a tunnel open: the client writing to a sink
// that replies to none of it, and the upstream pushing to a client that never writes, are both open at 1.5 s.
func TestScenarioOneDirectionIsTraffic(t *testing.T) {
	t.Parallel()
	srv := tunnelProxy(t, wsupstream.New(t))

	t.Run("client-writes", func(t *testing.T) {
		t.Parallel()
		conn := dialTunnel(t, srv, "mode=sink")
		until := time.Now().Add(1500 * time.Millisecond)
		for i := 0; time.Now().Before(until); i++ {
			require.NoError(t, websocket.Message.Send(conn, "f"+strconv.Itoa(i)))
			time.Sleep(100 * time.Millisecond)
		}
		awaitEcho(t, conn, "probe:client")
	})

	t.Run("upstream-writes", func(t *testing.T) {
		t.Parallel()
		conn := dialTunnel(t, srv, "mode=push&every=100ms")
		until := time.Now().Add(1500 * time.Millisecond)
		require.NoError(t, conn.SetReadDeadline(until.Add(5*time.Second)))
		pushes := 0
		for time.Now().Before(until) {
			var frame string
			require.NoError(t, websocket.Message.Receive(conn, &frame))
			require.True(t, strings.HasPrefix(frame, "push:"), frame)
			pushes++
		}
		require.Greater(t, pushes, 5, "the upstream pushed through the 1.5 s")
		awaitEcho(t, conn, "probe:upstream")
	})
}

// ADR-0181: a response other than a 101 is returned as is, its body the same value.
func TestIdleTunnelPassesNon101(t *testing.T) {
	t.Parallel()
	body := io.NopCloser(strings.NewReader("ok"))
	rt := idleTunnel(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	}), testIdle)
	resp, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	require.True(t, resp.Body == body, "a 200 body is not wrapped")
}
