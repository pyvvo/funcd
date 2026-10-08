package function_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0190 Decision 8 tests: a manifest rebuild starts a second pool worker and drains the old one.

// recordingSockets records the member set of each pool's local API socket.
type recordingSockets struct {
	mu      sync.Mutex
	members map[v1.ObjectName][]v1.ObjectName
}

func (s *recordingSockets) SocketFor(_ v1.NamespaceName, name v1.ObjectName) (string, error) {
	return "sock-" + string(name), nil
}

func (s *recordingSockets) PoolSocketFor(_ v1.NamespaceName, pool v1.ObjectName, members []v1.ObjectName) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members == nil {
		s.members = map[v1.ObjectName][]v1.ObjectName{}
	}
	s.members[pool] = slices.Sorted(slices.Values(members))
	return "sock-" + string(pool), nil
}

func (s *recordingSockets) Remove(v1.NamespaceName, v1.ObjectName) {}

func (s *recordingSockets) of(pool v1.ObjectName) []v1.ObjectName {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.members[pool]
}

// serveWorker gives instance id its own endpoint: its health paths answer at once, any other call stays in flight until
// release and then answers answer.
func (f *fakeRuntime) serveWorker(t *testing.T, id runtime.InstanceID, answer string) (url string, release func()) {
	t.Helper()
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/health/members":
			f.serveMembers(w)
			return
		case strings.HasPrefix(r.URL.Path, "/health/"):
			w.WriteHeader(http.StatusOK)
			return
		}
		<-gate
		_, _ = io.WriteString(w, answer)
	}))
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(srv.Close)
	t.Cleanup(release)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	f.mu.Lock()
	f.idPort[id] = port
	f.mu.Unlock()
	return srv.URL, release
}

// setHoldNew makes every instance created from now on held (true), or not (false).
func (f *fakeRuntime) setHoldNew(held bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holdNew = held
}

// otherArtifact writes a second handler file, so a member that switches to it changes the pool's manifest.
func otherArtifact(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "other.mjs")
	require.NoError(t, os.WriteFile(p, []byte("export function handle() {}\n"), 0o600))
	return "file://" + p
}

// pooledPair deploys a and b in pool worker w and returns the one pool worker running them.
func pooledPair(t *testing.T, h *shimHarness) runtime.InstanceID {
	t.Helper()
	for _, name := range []string{"a", "b"} {
		h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w" })
	}
	h.reconcile(t, "a")
	h.reconcile(t, "b")
	h.reconcile(t, "a")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a").Status.Phase)
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)
	workers := h.rt.poolWorkers("default", poolOf("w"))
	require.Len(t, workers, 1)
	return workers[0]
}

// A manifest rebuild starts a second pool worker of the key, named by the manifest signature in its revision slot; the
// old one keeps serving a call in flight until it ends, the resolver hands out the new one once it listens, and the
// old one is retired once idle (ADR-0190 Decision 8).
func TestPoolRebuildServesInFlightCall(t *testing.T) {
	t.Parallel()
	calls := activator.NewCallTracker(nil)
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) { d.Calls = calls })
	old := pooledPair(t, h)
	oldURL, release := h.rt.serveWorker(t, old, "old pool")
	up, ready := h.upstream(t, "a")
	require.True(t, ready)
	require.Equal(t, oldURL+"/function/a", up)
	answer := callInFlight(t, calls, oldURL)

	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = otherArtifact(t) })
	h.reconcile(t, "b")
	workers := h.rt.poolWorkers("default", poolOf("w"))
	require.Len(t, workers, 2, "the rebuild starts a second pool worker")
	require.Equal(t, old, workers[0])
	next := h.rt.specOf(workers[1])
	require.NotEmpty(t, next.Revision, "the new pool worker holds the manifest signature")
	require.NotEqual(t, h.rt.specOf(old).Revision, next.Revision)
	require.False(t, h.rt.wasRemoved(old), "the old pool worker serves its call in flight")
	up, ready = h.upstream(t, "a")
	require.True(t, ready)
	require.Equal(t, h.shimURL()+"/function/a", up, "the resolver hands out the new pool worker once it listens")

	h.reconcile(t, "a")
	require.False(t, h.rt.wasRemoved(old), "a sibling's pass waits for the call too")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a").Status.Phase)

	release()
	require.Equal(t, "old pool", answer(), "the call in flight completes on the old pool worker")
	settle()
	h.reconcile(t, "a")
	require.True(t, h.rt.wasRemoved(old), "the old pool worker is retired once idle")
	require.Equal(t, []runtime.InstanceID{workers[1]}, h.rt.poolWorkers("default", poolOf("w")))
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)
	require.Equal(t, h.getFn(t, "b").Status.CurrentRevision, h.getFn(t, "b").Status.ServingRevision)
}

// A new pool worker that never listens leaves the old one serving, past DrainGrace: the drain clock starts only when the
// new one listens, and then DrainGrace bounds a call that never ends (ADR-0190 Decision 8).
func TestPoolRebuildKeepsOldUntilNewListens(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Unix(1_700_000_000, 0))
	calls := activator.NewCallTracker(clk)
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) {
		d.Calls = calls
		d.Clock = clk
		d.DrainGrace = time.Second
	})
	old := pooledPair(t, h)
	oldURL, _ := h.rt.serveWorker(t, old, "old pool")
	callInFlight(t, calls, oldURL)

	h.rt.setHoldNew(true)
	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = otherArtifact(t) })
	h.reconcile(t, "b")
	clk.Advance(time.Minute)
	h.reconcile(t, "a")
	h.reconcile(t, "b")
	workers := h.rt.poolWorkers("default", poolOf("w"))
	require.Len(t, workers, 2)
	require.False(t, h.rt.wasRemoved(old), "the old pool worker serves while the new one does not listen")
	up, ready := h.upstream(t, "a")
	require.True(t, ready, "a sibling keeps serving from the old pool worker")
	require.Equal(t, oldURL+"/function/a", up)
	b := h.getFn(t, "b")
	require.NotEqual(t, b.Status.CurrentRevision, b.Status.ServingRevision, "the edited member is not judged on the old pool worker")

	h.rt.hold(workers[1], false)
	h.reconcile(t, "a")
	require.False(t, h.rt.wasRemoved(old), "the drain clock starts when the new one listens")
	clk.Advance(time.Second - time.Millisecond)
	h.reconcile(t, "a")
	require.False(t, h.rt.wasRemoved(old), "within DrainGrace the call holds the old pool worker")
	clk.Advance(time.Millisecond)
	h.reconcile(t, "a")
	require.True(t, h.rt.wasRemoved(old), "DrainGrace bounds a call that never ends")
	h.reconcile(t, "b")
	b = h.getFn(t, "b")
	require.Equal(t, b.Status.CurrentRevision, b.Status.ServingRevision)
}

// A call pinned to a pooled member's current revision is handed out only by a pool worker of the current manifest: while
// the rebuilt one does not listen, the old one, which holds the member's previous code, is not (ADR-0190 Decisions 4
// and 8).
func TestPoolPinnedCurrentWaitsForCurrentManifest(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	old := pooledPair(t, h)
	oldURL, _ := h.rt.serveWorker(t, old, "old pool")

	h.rt.setHoldNew(true)
	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = otherArtifact(t) })
	h.reconcile(t, "b")
	workers := h.rt.poolWorkers("default", poolOf("w"))
	require.Len(t, workers, 2)
	b := h.getFn(t, "b")
	require.NotEqual(t, b.Status.CurrentRevision, b.Status.ServingRevision)
	pin := activator.FunctionRef{Namespace: "default", Name: "b", Revision: v1.ObjectName(b.Status.CurrentRevision), UID: b.UID}
	up, ready, err := h.r.Endpoints().Upstream(context.Background(), pin)
	require.NoError(t, err)
	require.NotEqual(t, oldURL+"/function/b", up, "the old pool worker holds b's previous code")
	require.False(t, ready, "no pool worker of the current manifest listens")
	up, ready = h.upstream(t, "a")
	require.True(t, ready, "an unpinned call to a sibling is still handed the old pool worker")
	require.Equal(t, oldURL+"/function/a", up)

	h.rt.hold(workers[1], false)
	up, ready, err = h.r.Endpoints().Upstream(context.Background(), pin)
	require.NoError(t, err)
	require.True(t, ready, "the pool worker of the current manifest listens")
	require.Equal(t, h.shimURL()+"/function/b", up)
}

// After a restart of funcd the in-memory signature is gone: the pool workers the runtime lists say whether a rebuild is
// due, and the resolver hands out the key's newest listening pool worker (ADR-0190 Decision 8).
func TestPoolResolverAfterRestart(t *testing.T) {
	t.Parallel()
	calls := activator.NewCallTracker(nil)
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) { d.Calls = calls })
	old := pooledPair(t, h)
	oldURL, release := h.rt.serveWorker(t, old, "old pool")
	answer := callInFlight(t, calls, oldURL)
	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = otherArtifact(t) })
	h.reconcile(t, "b")
	require.Len(t, h.rt.poolWorkers("default", poolOf("w")), 2)

	r2, err := function.NewReconciler(h.deps)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r2.Close() })
	restarted := &shimHarness{r: r2, st: h.st, rt: h.rt, gw: h.gw, artifact: h.artifact, deps: h.deps}
	up, ready := restarted.upstream(t, "a")
	require.True(t, ready)
	require.Equal(t, h.shimURL()+"/function/a", up, "the newest listening pool worker, not the old one")

	creates, _ := h.rt.counts()
	restarted.reconcile(t, "a")
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "the runtime holds the current manifest: no rebuild")
	require.False(t, h.rt.wasRemoved(old), "the call in flight still holds the old pool worker")

	release()
	require.Equal(t, "old pool", answer())
	restarted.reconcile(t, "a")
	require.True(t, h.rt.wasRemoved(old))
	require.Len(t, h.rt.poolWorkers("default", poolOf("w")), 1)
}

// While an old pool worker drains, the pool's local API serves the members of both, so a call in flight on a departing
// member keeps its local API; once the old one is retired, only the current manifest's (ADR-0190 Decision 8).
func TestPoolRebuildSocketKeepsDepartingMember(t *testing.T) {
	t.Parallel()
	calls := activator.NewCallTracker(nil)
	sockets := &recordingSockets{}
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) {
		d.Calls = calls
		d.InvokeSockets = sockets
	})
	old := pooledPair(t, h)
	require.Equal(t, []v1.ObjectName{"a", "b"}, sockets.of(poolOf("w")))
	oldURL, release := h.rt.serveWorker(t, old, "old pool")
	answer := callInFlight(t, calls, oldURL)

	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w2" })
	h.reconcile(t, "a")
	require.Len(t, h.rt.poolWorkers("default", poolOf("w")), 2)
	require.Equal(t, []v1.ObjectName{"a", "b"}, sockets.of(poolOf("w")), "b's call in flight keeps its local API")
	members, _, _ := h.r.PoolMembers("default", poolOf("w"))
	require.ElementsMatch(t, []v1.ObjectName{"a", "b"}, members)

	release()
	require.Equal(t, "old pool", answer())
	h.reconcile(t, "a")
	require.True(t, h.rt.wasRemoved(old))
	require.Equal(t, []v1.ObjectName{"a"}, sockets.of(poolOf("w")), "the retirement narrows the socket to the current members")
}

// A stopped or exited pool worker of the current manifest is still restarted by stop and create, in place: no call can
// complete on it, so nothing drains (ADR-0190 Decision 8).
func TestPoolCrashedWorkerRestartsInPlace(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	old := pooledPair(t, h)
	h.rt.exitRevision(poolOf("w"), "", 0, runtime.StateFailed, controller.SupervisionPeriod)
	creates, _ := h.rt.counts()
	h.reconcile(t, "a")
	after, _ := h.rt.counts()
	require.Equal(t, creates+1, after, "the exited pool worker is created again")
	require.Equal(t, []runtime.InstanceID{old}, h.rt.poolWorkers("default", poolOf("w")), "under the same instance id")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates(poolOf("w"))[""][0])
}

// A call pinned to a pooled member's current revision is not handed the newest pool worker when that worker's manifest
// holds the member at other code: a stamped revision that cannot be materialized leaves the member's previous code in
// the manifest (ADR-0190 Decisions 4 and 8).
func TestPoolPinnedCurrentNeedsItsCodeInManifest(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	pooledPair(t, h)
	served := h.getFn(t, "b").Status.CurrentRevision

	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = "file://" + filepath.Join(t.TempDir(), "missing.mjs") })
	_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "b"})
	require.Error(t, err, "b's update cannot be materialized")
	h.reconcile(t, "a")
	require.Len(t, h.rt.poolWorkers("default", poolOf("w")), 1, "the manifest keeps b's previous code: no rebuild")
	b := h.getFn(t, "b")
	require.NotEqual(t, served, b.Status.CurrentRevision, "b's update is stamped as its current revision")

	pin := activator.FunctionRef{Namespace: "default", Name: "b", Revision: v1.ObjectName(b.Status.CurrentRevision), UID: b.UID}
	up, ready, err := h.r.Endpoints().Upstream(context.Background(), pin)
	require.NoError(t, err)
	require.Empty(t, up, "the pool worker serves b's previous code, not the pinned revision")
	require.False(t, ready)
	up, ready = h.upstream(t, "a")
	require.True(t, ready, "a sibling is still handed the pool worker")
	require.Equal(t, h.shimURL()+"/function/a", up)
}

// lateClose carries a call whose response body takes closeLag to close: the call ends, as the tracker counts it, that
// long after its answer was read.
type lateClose struct{}

const closeLag = 100 * time.Millisecond

func (lateClose) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = lateBody{resp.Body}
	return resp, nil
}

type lateBody struct{ io.ReadCloser }

func (b lateBody) Close() error {
	time.Sleep(closeLag)
	return b.ReadCloser.Close()
}

// A released call holds the old pool worker until it ends, which can be well after its answer arrives; the test's wait
// for the answer covers its end, so the next pass retires the old pool worker (#858).
func TestIssue858_ReleasedCallEndsBeforeRetireCheck(t *testing.T) {
	t.Parallel()
	calls := activator.NewCallTracker(nil)
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) { d.Calls = calls })
	old := pooledPair(t, h)
	oldURL, release := h.rt.serveWorker(t, old, "old pool")
	answer := callThrough(t, calls, lateClose{}, oldURL)

	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w2" })
	h.reconcile(t, "a")
	require.False(t, h.rt.wasRemoved(old), "the call in flight holds the old pool worker")

	release()
	require.Equal(t, "old pool", answer())
	h.reconcile(t, "a")
	require.True(t, h.rt.wasRemoved(old), "the old pool worker is retired once the released call has ended")
}
