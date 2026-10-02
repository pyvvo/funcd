package activator

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestCallTrackerIdleWithNoCall(t *testing.T) {
	tr := NewCallTracker(nil)
	require.True(t, tr.Idle("http://127.0.0.1:1234", 2*time.Second))
}

func TestCallTrackerCountsStreamUntilBodyClosed(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "first chunk")
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)

	tr := NewCallTracker(nil)
	client := &http.Client{Transport: tr.Wrap(nil)}
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	buf := make([]byte, len("first chunk"))
	_, err = io.ReadFull(resp.Body, buf)
	require.NoError(t, err)
	require.False(t, tr.Idle(srv.URL, 0), "a streamed answer counts while its body is open")

	require.NoError(t, resp.Body.Close())
	require.True(t, tr.Idle(srv.URL, 0), "closing the body ends the call")
}

func TestCallTrackerFailedRoundTripEndsTheCall(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	url := "http://" + ln.Addr().String()
	require.NoError(t, ln.Close())

	tr := NewCallTracker(nil)
	client := &http.Client{Transport: tr.Wrap(nil)}
	_, err = client.Get(url) //nolint:bodyclose // the round trip fails, so there is no body
	require.Error(t, err)
	require.True(t, tr.Idle(url, 0))
}

func TestCallTrackerRecentHandOutIsNotIdle(t *testing.T) {
	clk := &manualClock{now: time.Unix(1000, 0)}
	tr := NewCallTracker(clk)
	tr.HandedOut("http://10.0.0.5:8080")

	clk.advance(time.Second)
	require.False(t, tr.Idle("http://10.0.0.5:8080", 2*time.Second), "handed out 1 s ago")
	clk.advance(time.Second)
	require.True(t, tr.Idle("http://10.0.0.5:8080", 2*time.Second), "handed out 2 s ago")
}

func TestCallTrackerForgetsIdleUpstreams(t *testing.T) {
	clk := &manualClock{now: time.Unix(1000, 0)}
	tr := NewCallTracker(clk)
	tr.HandedOut("http://10.0.0.5:8080")
	tr.HandedOut("http://10.0.0.6:8080")

	clk.advance(3 * time.Second)
	require.True(t, tr.Idle("http://10.0.0.5:8080", 2*time.Second))
	require.NotContains(t, tr.hosts, "10.0.0.5:8080", "Idle forgets an idle upstream")

	clk.advance(2 * idleRetention)
	tr.HandedOut("http://10.0.0.7:8080")
	require.NotContains(t, tr.hosts, "10.0.0.6:8080", "a hand-out purges upstreams idle past the retention")
	require.Contains(t, tr.hosts, "10.0.0.7:8080")
}

func TestCallTrackerCountsUpgradedConnUntilClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(conn, rw)
	}))
	defer srv.Close()

	tr := NewCallTracker(nil)
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "echo")
	resp, err := (&http.Client{Transport: tr.Wrap(nil)}).Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	conn, ok := resp.Body.(io.ReadWriteCloser)
	require.True(t, ok, "an upgraded body stays writable, so a reverse proxy can tunnel it")
	require.False(t, tr.Idle(srv.URL, 0), "the tunnel counts while open")

	_, err = conn.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, len("ping"))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	require.Equal(t, "ping", string(buf))

	require.NoError(t, conn.Close())
	require.True(t, tr.Idle(srv.URL, 0), "closing the tunnel ends the call")
}
