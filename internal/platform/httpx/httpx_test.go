package httpx_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/httpx"
)

// proxyAddr is the environment proxy every test of this package runs under (TestMain). The tests that call 127.0.0.1
// are unaffected: the environment proxy exempts loopback.
const proxyAddr = "192.0.2.1:3128"

// TestMain sets the proxy environment before any request: net/http reads it once per process, so a t.Setenv in a
// test would come too late.
func TestMain(m *testing.M) {
	if err := os.Setenv("HTTP_PROXY", "http://"+proxyAddr); err != nil {
		os.Exit(2)
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		if err := os.Unsetenv(name); err != nil {
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

// A client from httpx keeps its keep-alive connection when the process-wide http.DefaultTransport's idle connections
// are closed, as every httptest.Server.Close does. Not parallel: it closes the default transport's idle connections,
// which would break the other tests' parked connections.
func TestIssue564_ClientSurvivesDefaultTransportCloseIdle(t *testing.T) {
	conns := new(atomic.Int32)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	client := httpx.Client(5 * time.Second)
	get := func() {
		resp, err := client.Get(srv.URL)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	get()
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	get()
	require.EqualValues(t, 1, conns.Load(), "closing the default transport's idle connections must not touch the client's")
}

// Transport keeps http.DefaultTransport's settings, also after something replaced http.DefaultTransport with a
// RoundTripper that is not an *http.Transport. Not parallel: it swaps http.DefaultTransport.
func TestIssue564_TransportKeepsDefaultSettings(t *testing.T) {
	def, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok)
	check := func(t *testing.T, tr *http.Transport) {
		t.Helper()
		require.NotSame(t, def, tr)
		require.NotNil(t, tr.Proxy)
		require.NotNil(t, tr.DialContext)
		require.Equal(t, def.TLSHandshakeTimeout, tr.TLSHandshakeTimeout)
		require.Equal(t, def.IdleConnTimeout, tr.IdleConnTimeout)
		require.Equal(t, def.MaxIdleConns, tr.MaxIdleConns)
		require.Equal(t, def.ExpectContinueTimeout, tr.ExpectContinueTimeout)
		require.Equal(t, def.ForceAttemptHTTP2, tr.ForceAttemptHTTP2)
	}

	t.Run("clone", func(t *testing.T) { check(t, httpx.Transport()) })
	t.Run("replaced default", func(t *testing.T) {
		http.DefaultTransport = wrappedTransport{def}
		t.Cleanup(func() { http.DefaultTransport = def })
		check(t, httpx.Transport())
	})
}

type wrappedTransport struct{ http.RoundTripper }

// TestNodeTransportSettings: a node-local pool keeps an idle connection for each concurrent call to a host, never
// takes a proxy, and keeps everything else from Transport (ADR-0155 Decision 2, ADR-0041).
func TestNodeTransportSettings(t *testing.T) {
	base := httpx.Transport()
	tr := httpx.NodeTransport()
	require.NotSame(t, tr, httpx.NodeTransport(), "each call builds a pool of its own")
	require.Nil(t, tr.Proxy)
	require.Equal(t, 256, tr.MaxIdleConnsPerHost)
	require.Equal(t, 512, tr.MaxIdleConns)
	require.Equal(t, 90*time.Second, tr.IdleConnTimeout)
	require.NotNil(t, tr.DialContext)
	require.Zero(t, tr.MaxConnsPerHost)
	require.Zero(t, tr.ResponseHeaderTimeout)
	require.Equal(t, base.TLSHandshakeTimeout, tr.TLSHandshakeTimeout)
	require.Equal(t, base.ExpectContinueTimeout, tr.ExpectContinueTimeout)
	require.Equal(t, base.ForceAttemptHTTP2, tr.ForceAttemptHTTP2)
	require.Equal(t, base.DisableKeepAlives, tr.DisableKeepAlives)

	client := httpx.NodeClient(3 * time.Second)
	require.Equal(t, 3*time.Second, client.Timeout)
	ctr, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, ctr.Proxy)
	require.Equal(t, 256, ctr.MaxIdleConnsPerHost)
}

type countedBody struct {
	left   int64
	read   int64
	closed bool
}

func (b *countedBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), b.left)
	b.left -= n
	b.read += n
	return int(n), nil
}

func (b *countedBody) Close() error {
	b.closed = true
	return nil
}

func TestCloseBodyDrainsAtMostDrainLimit(t *testing.T) {
	long := &countedBody{left: 8 * httpx.DrainLimit}
	httpx.CloseBody(long)
	require.EqualValues(t, httpx.DrainLimit, long.read, "an unbounded answer is never read in full")
	require.True(t, long.closed)

	short := &countedBody{left: 100}
	httpx.CloseBody(short)
	require.EqualValues(t, 100, short.read, "a short answer is read to EOF, so its connection returns to the pool")
	require.Zero(t, short.left)
	require.True(t, short.closed)
}

// scenario: proxy-env-ignored — with HTTP_PROXY set and no NO_PROXY (TestMain), a call to a worker at a netns address
// goes to that address, never to the proxy. Transport is the control: it reads the same environment.
func TestScenarioProxyEnvIgnored(t *testing.T) {
	const worker = "10.63.0.5:8080"
	errNoDial := errors.New("dial refused by the test")
	dialed := func(t *testing.T, tr *http.Transport) string {
		t.Helper()
		addrs := make(chan string, 1)
		tr.DialContext = func(_ context.Context, _, addr string) (net.Conn, error) {
			addrs <- addr
			return nil, errNoDial
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+worker+"/", strings.NewReader("{}"))
		require.NoError(t, err)
		resp, err := tr.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		require.ErrorIs(t, err, errNoDial)
		return <-addrs
	}

	require.Equal(t, proxyAddr, dialed(t, httpx.Transport()), "the environment proxy was read")
	require.Equal(t, worker, dialed(t, httpx.NodeTransport()), "a node-local call never takes the proxy")
}
