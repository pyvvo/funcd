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
