package function

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

	_, failed := r.readyReplicas(ctx, "default", "gone", "gone-1", 0, 1, readinessPath, bootTimeout)
	require.False(t, failed, "replica 1 is at the bound, so it is not judged")
	_, failed = r.readyReplicas(ctx, "default", "gone", "gone-1", 0, 2, readinessPath, bootTimeout)
	require.True(t, failed, "inside the bound, a Failed replica is a shape failure")
}

// Repeated readiness probes to one worker reuse a keep-alive connection, so a busy probe loop does not churn
// ephemeral ports into TIME_WAIT (ADR-0041).
func TestIssue236_ReadinessProbeReusesConnection(t *testing.T) {
	t.Parallel()
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	addr, ok := srv.Listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	r := newShimReconciler(t, fakeResolver{})
	for range 50 {
		require.True(t, r.probeReady(context.Background(), addr.IP.String(), addr.Port, readinessPath))
	}
	require.EqualValues(t, 1, conns.Load(), "50 probes must share one keep-alive connection")
}

// The readiness probe keeps its connections out of http.DefaultTransport: every httptest.Server.Close in the process
// closes that transport's idle connections, and one landing while a probe's connection is parked fails the probe.
func TestIssue287_ProbeSurvivesDefaultTransportCloseIdle(t *testing.T) {
	t.Parallel()
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	addr, ok := srv.Listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	r := newShimReconciler(t, fakeResolver{})
	ctx := context.Background()
	require.True(t, r.probeReady(ctx, addr.IP.String(), addr.Port, readinessPath))
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	require.True(t, r.probeReady(ctx, addr.IP.String(), addr.Port, readinessPath))
	require.EqualValues(t, 1, conns.Load(), "closing the default transport's idle connections must not touch the probe's")
}
