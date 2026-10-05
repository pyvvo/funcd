package funcd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
)

// timeoutHeaderServer reports the X-Funcd-Timeout-Ms of each call.
func timeoutHeaderServer(t *testing.T) (*httptest.Server, <-chan string) {
	t.Helper()
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get(activator.TimeoutHeader)
		_, _ = io.WriteString(w, "output")
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func postWith(t *testing.T, c *http.Client, ctx context.Context, url string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	require.NoError(t, err)
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func requireHeaderWithin(t *testing.T, header string, bound time.Duration) {
	t.Helper()
	ms, err := strconv.ParseInt(header, 10, 64)
	require.NoError(t, err)
	require.LessOrEqual(t, ms, bound.Milliseconds())
	require.Greater(t, ms, (bound - time.Second).Milliseconds(), "within 1 s of the bound")
}

// The workflow dispatcher's client (no timeout) sends its step's context deadline; the Sensor invoker's (30 s)
// sends the client's own bound (ADR-0151).
func TestWorkerClientSendsDeadline(t *testing.T) {
	t.Parallel()
	calls := activator.NewCallTracker(nil)
	srv, got := timeoutHeaderServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	postWith(t, workerClient(calls, 0), ctx, srv.URL)
	requireHeaderWithin(t, <-got, 5*time.Second)

	postWith(t, workerClient(calls, 30*time.Second), context.Background(), srv.URL)
	requireHeaderWithin(t, <-got, 30*time.Second)
	require.True(t, calls.Idle(srv.URL, 0), "the calls are counted and ended")
}

func TestWithDefaultInvokeTimeout(t *testing.T) {
	t.Parallel()
	c := &config{}
	require.NoError(t, WithDefaultInvokeTimeout(0)(c))
	require.Equal(t, v1.DefaultInvokeTimeout, c.invokeDefaultTimeout, "0 ⇒ 60s")
	require.NoError(t, WithDefaultInvokeTimeout(v1.MaxInvokeTimeout)(c))
	require.Equal(t, time.Hour, c.invokeDefaultTimeout)

	for _, bad := range []time.Duration{-time.Second, v1.MaxInvokeTimeout + time.Nanosecond} {
		_, err := New(WithDefaultInvokeTimeout(bad))
		require.Equal(t, fault.Invalid, fault.KindOf(err), "New refuses %s", bad)
	}
}
