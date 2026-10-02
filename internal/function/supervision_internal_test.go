package function

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/runtime"
)

// readyReplicas judges only the replicas below its bound (ADR-0142): a Failed replica that is being scaled away is
// not a shape failure.
func TestReadyReplicasIgnoresReplicasAtOrAboveBound(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	ctx := context.Background()
	inst, err := r.runtime.Create(ctx, runtime.WorkerSpec{
		Namespace: "default", Name: "gone", Revision: "gone-1", Replica: 1,
		Command: []string{"sh", "-c", "exit 3"}, LogPath: filepath.Join(t.TempDir(), "w.log"),
	})
	require.NoError(t, err)
	require.NoError(t, r.runtime.Start(ctx, inst.ID))
	require.Eventually(t, func() bool {
		in, serr := r.runtime.Status(ctx, inst.ID)
		return serr == nil && in.State == runtime.StateFailed
	}, 5*time.Second, 10*time.Millisecond)

	_, failed := r.readyReplicas(ctx, "default", "gone", "gone-1", 0, 1, readinessPath)
	require.False(t, failed, "replica 1 is at the bound, so it is not judged")
	_, failed = r.readyReplicas(ctx, "default", "gone", "gone-1", 0, 2, readinessPath)
	require.True(t, failed, "inside the bound, a Failed replica is a shape failure")
}

// The readiness probe keeps its connections out of http.DefaultTransport. Anything in the process may close that
// transport's idle connections (every httptest.Server.Close does), and a close that lands while a probe's
// connection is parked fails the probe, so a ready revision misses its switch (CI flake on PR #275).
func TestProbeConnectionsSurviveDefaultTransportCloseIdle(t *testing.T) {
	t.Parallel()
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	r := newShimReconciler(t, fakeResolver{})
	ctx := context.Background()
	require.True(t, r.probeReady(ctx, host, port, readinessPath))
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	require.True(t, r.probeReady(ctx, host, port, readinessPath))
	require.Equal(t, int32(1), conns.Load(), "the second probe reuses the first one's connection")
}
