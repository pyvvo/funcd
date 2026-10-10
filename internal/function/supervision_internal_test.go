package function

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// readyReplicas judges only the replicas below its bound (ADR-0142): a Failed replica that is being scaled away is
// not a shape failure.
func TestReadyReplicasIgnoresReplicasAtOrAboveBound(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	ctx := context.Background()
	inst, err := r.runtime.Create(ctx, runtime.WorkerSpec{
		Namespace: "default", OwnerKind: v1.KindFunction, Name: "gone", Revision: "gone-1", Replica: 1,
		Command: []string{"sh", "-c", "exit 3"},
	})
	require.NoError(t, err)
	require.NoError(t, r.runtime.Start(ctx, inst.ID))
	require.Eventually(t, func() bool {
		in, serr := r.runtime.Status(ctx, inst.ID)
		return serr == nil && in.State == runtime.StateFailed
	}, 5*time.Second, 10*time.Millisecond)

	rd, err := r.readyReplicas(ctx, "default", "gone", "gone-1", 0, 1, r.bootTimeout, false, true, r.boot)
	require.NoError(t, err)
	require.Empty(t, rd.failed, "replica 1 is at the bound, so it is not judged")
	rd, err = r.readyReplicas(ctx, "default", "gone", "gone-1", 0, 2, r.bootTimeout, false, true, r.boot)
	require.NoError(t, err)
	require.Equal(t, inst.ID, rd.failed, "inside the bound, a Failed replica is a shape failure")
}

// Repeated readiness probes to one worker reuse a keep-alive connection, so a busy probe loop does not churn
// ephemeral ports into TIME_WAIT (ADR-0041).
func TestIssue236_ReadinessProbeReusesConnection(t *testing.T) {
	t.Parallel()
	addr, conns := countingServer(t, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"status":"ready"}`)) })

	r := newShimReconciler(t, fakeResolver{})
	for range 50 {
		ok, _ := r.probeReadiness(context.Background(), addr.IP.String(), addr.Port)
		require.True(t, ok)
	}
	require.EqualValues(t, 1, conns.Load(), "50 probes must share one keep-alive connection")
}

// The readiness probe keeps its connections out of http.DefaultTransport: every httptest.Server.Close in the process
// closes that transport's idle connections, and one landing while a probe's connection is parked fails the probe.
// Not parallel: it closes the process-wide transport's idle connections, which other tests may have parked there.
func TestIssue287_ProbeSurvivesDefaultTransportCloseIdle(t *testing.T) {
	addr, conns := countingServer(t, func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) })

	r := newShimReconciler(t, fakeResolver{})
	ctx := context.Background()
	require.True(t, r.probeOK(ctx, addr.IP.String(), addr.Port, readinessPath))
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	require.True(t, r.probeOK(ctx, addr.IP.String(), addr.Port, readinessPath))
	require.EqualValues(t, 1, conns.Load(), "closing the default transport's idle connections must not touch the probe's")
}

// countingServer serves respond on loopback and counts the TCP connections it accepts.
func countingServer(t *testing.T, respond func(http.ResponseWriter)) (*net.TCPAddr, *atomic.Int32) {
	t.Helper()
	conns := new(atomic.Int32)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { respond(w) }))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	addr, ok := srv.Listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return addr, conns
}

// holdsFailed holds only for a pass that started Failed and found nothing running, booting, ready or newly failed
// (ADR-0169 Decision 2); each verdict field releases it.
func TestHoldsFailed(t *testing.T) {
	t.Parallel()
	require.True(t, holdsFailed(v1.PhaseFailed, verdict{}))
	require.True(t, holdsFailed(v1.PhaseFailed, verdict{retryAt: time.Now(), pooled: true}), "a pool worker waits out its backoff")
	for _, p := range []v1.Phase{"", v1.PhasePending, v1.PhaseIdle, v1.PhaseDeploying, v1.PhaseReady, v1.PhaseDegraded} {
		require.False(t, holdsFailed(p, verdict{}), "a pass that started %q", p)
	}
	for name, v := range map[string]verdict{
		"running":          {running: 1},
		"ready":            {ready: 1},
		"booting":          {booting: true},
		"shapeFailed":      {shapeFailed: true},
		"currentFailed":    {currentFailed: true},
		"startErr":         {startErr: errors.New("exec: node: not found")},
		"crashLoop":        {crashLoop: "replica 0 exited with code 1 before it listened"},
		"currentCrashLoop": {currentCrashLoop: "replica 0 exited with code 1 before it listened"},
	} {
		require.False(t, holdsFailed(v1.PhaseFailed, v), name)
	}
}

// failStart records a failed worker spec for replica 0 of revision rev of Function name, as convergeRevision does: an
// entry with no instance behind it.
func failStart(r *Reconciler, name, rev v1.ObjectName) runtime.InstanceID {
	id := runtime.NewInstanceID("default", name, rev, 0)
	r.boot.startResult(id, time.Now(), errors.New("create socket dir: permission denied"))
	return id
}

// requireEntries asserts which IDs keep a bootBackoff entry and which have none.
func requireEntries(t *testing.T, r *Reconciler, kept, forgotten []runtime.InstanceID) {
	t.Helper()
	for _, id := range kept {
		_, ok := r.boot.crash(id)
		require.True(t, ok, "%s is kept", id)
	}
	for _, id := range forgotten {
		_, ok := r.boot.crash(id)
		require.False(t, ok, "%s is forgotten", id)
	}
}

// Deleting a Function forgets its replicas that never got an instance, and only its own (ADR-0169 Decision 4).
func TestTeardownForgetsReplicaWithNoInstance(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	gone, other := failStart(r, "fn", "fn-1"), failStart(r, "other", "other-1")

	require.NoError(t, r.teardown(context.Background(), "default", "fn"))
	requireEntries(t, r, []runtime.InstanceID{other}, []runtime.InstanceID{gone})
}

// A replaced revision's replica that failed at its worker spec has no worker to retire; the switch's drain and the
// scale-to-zero stopAll forget its entry all the same and keep those of the revisions still live (ADR-0169 Decision 4).
func TestStaleRevisionRetireForgetsReplicaWithNoInstance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, tc := range map[string]struct {
		retire func(*Reconciler, *v1.Function) error
		kept   []v1.ObjectName
	}{
		"drain": {
			retire: func(r *Reconciler, fn *v1.Function) error { _, err := r.drain(ctx, fn); return err },
			kept:   []v1.ObjectName{"fn-2", "fn-3"},
		},
		"stopAll": {
			retire: func(r *Reconciler, fn *v1.Function) error { return r.stopAll(ctx, fn, "fn-3") },
			kept:   []v1.ObjectName{"fn-3"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newShimReconciler(t, fakeResolver{})
			fn := sampleFn()
			fn.Name = "fn"
			fn.Status.DrainingRevision, fn.Status.ServingRevision, fn.Status.CurrentRevision = "fn-1", "fn-2", "fn-3"
			var kept, forgotten []runtime.InstanceID
			for _, rev := range []v1.ObjectName{"fn-1", "fn-2", "fn-3"} {
				if id := failStart(r, "fn", rev); slices.Contains(tc.kept, rev) {
					kept = append(kept, id)
				} else {
					forgotten = append(forgotten, id)
				}
			}
			other := failStart(r, "other", "fn-1")

			require.NoError(t, tc.retire(r, fn))
			requireEntries(t, r, append(kept, other), forgotten)
		})
	}
}
