package httpx_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/httpx"
)

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
