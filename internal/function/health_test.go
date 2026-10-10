package function_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// ADR-0215 tests: liveness restarts (Decisions 1, 2) and dependency readiness (Decision 5).

const (
	healthPeriod   = 10 * time.Second
	healthLiveness = 30 * time.Second
)

// healthShim is a fake shim whose /health/liveness and /health/readiness answers a test sets; a readiness report is
// sent as a new shim passes funcd's 503 through (Decision 4). It counts the probes it serves.
type healthShim struct {
	mu             sync.Mutex
	live, ready    int
	report         *local.DependencyReport
	lives, readies int
}

func (s *healthShim) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case "/health/liveness":
		s.lives++
		w.WriteHeader(s.live)
	case "/health/readiness":
		s.readies++
		if s.report != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(s.report)
			return
		}
		w.WriteHeader(s.ready)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (s *healthShim) setLive(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live = code
}

func (s *healthShim) setReady(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = code
}

func (s *healthShim) setReport(rep *local.DependencyReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.report = rep
}

func (s *healthShim) probes() (lives, readies int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lives, s.readies
}

// listenShim serves a healthy healthShim and returns it with its port.
func listenShim(t *testing.T) (*healthShim, int) {
	t.Helper()
	s := &healthShim{live: http.StatusOK, ready: http.StatusOK}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return s, port
}

// serveShim gives every worker of revision rev a healthShim.
func (f *fakeRuntime) serveShim(t *testing.T, rev v1.ObjectName) *healthShim {
	t.Helper()
	s, port := listenShim(t)
	f.mu.Lock()
	f.revPort[rev] = port
	f.mu.Unlock()
	return s
}

// serveShimOn gives worker id, and each one created again under id, its own healthShim.
func (f *fakeRuntime) serveShimOn(t *testing.T, id runtime.InstanceID) *healthShim {
	t.Helper()
	s, port := listenShim(t)
	f.mu.Lock()
	f.idPort[id] = port
	f.mu.Unlock()
	return s
}

// specCreated is when worker id was last created.
func (f *fakeRuntime) specCreated(id runtime.InstanceID) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created[id]
}

// setMemberDependency sets pool member name's /health/members dependency report; nil clears it.
func (f *fakeRuntime) setMemberDependency(name string, rep *local.DependencyReport) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memberDeps[name] = rep
}

// syncBuffer is a log sink safe for the reconciler's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// healthHarness runs the shim-mode reconciler on a manual clock with a 10 s supervision period, a 30 s liveness timeout
// and the 1 m default boot timeout, logging to the returned buffer.
func healthHarness(t *testing.T, opts ...func(*function.Deps)) (*shimHarness, *clock.Manual, *syncBuffer) {
	t.Helper()
	clk := clock.NewManual(time.Now())
	logs := &syncBuffer{}
	opts = append([]func(*function.Deps){func(d *function.Deps) {
		d.Clock, d.SupervisionPeriod, d.LivenessTimeout, d.HandOutSettle = clk, healthPeriod, healthLiveness, time.Millisecond
		d.BootBackoffInitial, d.BootBackoffMax = 10*time.Second, 5*time.Minute
		d.Logger = slog.New(slog.NewTextHandler(logs, nil))
	}}, opts...)
	return newShimHarness(t, http.StatusOK, false, opts...), clk, logs
}

// deployServing creates name with replicas and reconciles it to Ready, then runs one steady pass, in which each replica
// answers its first liveness probe.
func (h *shimHarness) deployServing(t *testing.T, name string, replicas int) {
	t.Helper()
	h.create(t, name, func(fn *v1.Function) { fn.Spec.Replicas = replicas })
	h.reconcile(t, name)
	require.Equal(t, v1.PhaseReady, h.getFn(t, name).Status.Phase)
	require.Equal(t, healthPeriod, h.reconcile(t, name).RequeueAfter)
}

func (h *shimHarness) creates() int {
	c, _ := h.rt.counts()
	return c
}

// audit is the report of a kv::read a Policy forbids on table audit.
func audit() *local.DependencyReport {
	return &local.DependencyReport{Kind: "kv", Binding: "audit", Reason: "Forbidden", Message: "kv::read is not permitted on todo-store/audit"}
}

const auditMessage = `kv binding "audit": kv::read is not permitted on todo-store/audit`

// app-hung-worker-restarted, the reconciler half (the scenario runs end to end in pkg/funcd): a replica that stops
// answering /health/liveness while its process runs is replaced livenessTimeout after its last answer, at the same
// index and outside the boot backoff, with one Warn line; with no other replica ready the Function is Degraded,
// Ready=False Restarting with the liveness message, until the new replica is ready.
func TestHungReplicaIsReplacedAtTheSameIndex(t *testing.T) {
	t.Parallel()
	h, clk, logs := healthHarness(t)
	shim := h.rt.serveShim(t, "todo-api-1")
	h.deployServing(t, "todo-api", 1)
	rv, creates := h.getFn(t, "todo-api").ResourceVersion, h.creates()

	shim.setLive(http.StatusServiceUnavailable)
	for range 2 {
		clk.Advance(healthPeriod)
		require.Equal(t, healthPeriod, h.reconcile(t, "todo-api").RequeueAfter)
	}
	clk.Advance(healthPeriod - time.Millisecond)
	h.reconcile(t, "todo-api")
	require.Equal(t, rv, h.getFn(t, "todo-api").ResourceVersion, "a replica within its liveness timeout is not restarted")
	require.Equal(t, creates, h.creates())
	require.NotContains(t, logs.String(), "silent on its liveness")

	shim.setReady(http.StatusServiceUnavailable)
	clk.Advance(time.Millisecond)
	res := h.reconcile(t, "todo-api")
	require.Equal(t, creates+1, h.creates(), "replaced in the pass that found it hung, with no boot backoff")
	require.Equal(t, map[int]runtime.State{0: runtime.StateRunning}, h.rt.revisionStates("todo-api")["todo-api-1"], "at the same index")
	require.Equal(t, clk.Now(), h.rt.specCreated(replicaID("todo-api", 1, 0)))
	fn := h.getFn(t, "todo-api")
	require.Equal(t, v1.PhaseDegraded, fn.Status.Phase)
	ready := h.requireCondition(t, "todo-api", "Ready", v1.ConditionFalse, "Restarting")
	require.Equal(t, "a replica stopped answering /health/liveness and is being replaced", ready.Message)
	require.Less(t, res.RequeueAfter, healthPeriod, "the replacement's boot is polled")
	clk.Advance(200 * time.Millisecond)
	h.reconcile(t, "todo-api")
	require.Equal(t, "a replica stopped answering /health/liveness and is being replaced", h.condition(t, "todo-api", "Ready").Message,
		"the message holds while the replacement boots")
	line := logs.String()
	require.Contains(t, line, `level=WARN msg="restarting a replica silent on its liveness" component=function namespace=default function=todo-api replica=0`)
	require.Equal(t, 1, strings.Count(line, "silent on its liveness"))

	shim.setReady(http.StatusOK)
	shim.setLive(http.StatusOK)
	h.reconcile(t, "todo-api")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "todo-api").Status.Phase)
	require.Empty(t, h.requireCondition(t, "todo-api", "Ready", v1.ConditionTrue, "").Message)
}

// A replica's silence counts from this process's first probe of it, not from its creation: a replica created an hour
// ago, as a restarted funcd finds it, gets a full livenessTimeout.
func TestLivenessSilenceCountsFromTheFirstProbe(t *testing.T) {
	t.Parallel()
	h, clk, _ := healthHarness(t)
	shim := h.rt.serveShim(t, "todo-api-1")
	shim.setLive(http.StatusServiceUnavailable)
	h.create(t, "todo-api", func(*v1.Function) {})
	h.reconcile(t, "todo-api")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "todo-api").Status.Phase)
	h.rt.exitRevision("todo-api", "todo-api-1", 0, runtime.StateRunning, time.Hour)
	creates := h.creates()

	h.reconcile(t, "todo-api")
	clk.Advance(healthLiveness - time.Millisecond)
	h.reconcile(t, "todo-api")
	require.Equal(t, creates, h.creates(), "silent since the first probe, not since its creation an hour ago")

	clk.Advance(time.Millisecond)
	h.reconcile(t, "todo-api")
	require.Equal(t, creates+1, h.creates())
}

// One missed probe is no restart, and a replica that answers again starts its timeout afresh; a replacement is not
// restarted again after its own first missed probe.
func TestLivenessOneMissedProbeIsNoRestart(t *testing.T) {
	t.Parallel()
	h, clk, _ := healthHarness(t)
	shim := h.rt.serveShim(t, "todo-api-1")
	h.deployServing(t, "todo-api", 1)
	creates := h.creates()

	shim.setLive(http.StatusServiceUnavailable)
	clk.Advance(healthPeriod)
	h.reconcile(t, "todo-api")
	shim.setLive(http.StatusOK)
	clk.Advance(healthPeriod)
	h.reconcile(t, "todo-api")
	shim.setLive(http.StatusServiceUnavailable)
	clk.Advance(healthLiveness - time.Second)
	h.reconcile(t, "todo-api")
	require.Equal(t, creates, h.creates(), "answered 29 s ago")

	clk.Advance(time.Second)
	h.reconcile(t, "todo-api")
	require.Equal(t, creates+1, h.creates(), "30 s since its last answer")

	clk.Advance(healthPeriod)
	h.reconcile(t, "todo-api")
	require.Equal(t, creates+1, h.creates(), "the replacement missed one probe: not restarted")
}

// With another replica ready, a hung one is replaced while the Function stays Ready; only the hung replica is replaced.
func TestLivenessRestartKeepsAnotherReplicaReady(t *testing.T) {
	t.Parallel()
	h, clk, _ := healthHarness(t)
	hung := h.rt.serveShimOn(t, replicaID("todo-api", 1, 1))
	h.deployServing(t, "todo-api", 2)
	creates := h.creates()

	hung.setLive(http.StatusServiceUnavailable)
	hung.setReady(http.StatusServiceUnavailable)
	clk.Advance(healthLiveness)
	h.reconcile(t, "todo-api")
	require.Equal(t, creates+1, h.creates())
	require.Equal(t, clk.Now(), h.rt.specCreated(replicaID("todo-api", 1, 1)), "replica 1 is the one created again")
	require.NotEqual(t, clk.Now(), h.rt.specCreated(replicaID("todo-api", 1, 0)))
	fn := h.getFn(t, "todo-api")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, 1, fn.Status.Replicas)
	h.requireCondition(t, "todo-api", "Ready", v1.ConditionTrue, "")
}

// Steady-state passes send each replica its liveness and readiness probes and write nothing (ADR-0142 Decision 3).
func TestSteadyStateProbesWriteNothing(t *testing.T) {
	t.Parallel()
	h, clk, _ := healthHarness(t)
	shim := h.rt.serveShim(t, "todo-api-1")
	h.deployServing(t, "todo-api", 1)
	rv, creates := h.getFn(t, "todo-api").ResourceVersion, h.creates()
	_, lists := h.rt.counts()
	lives, readies := shim.probes()

	for range 5 {
		clk.Advance(healthPeriod)
		require.Equal(t, healthPeriod, h.reconcile(t, "todo-api").RequeueAfter)
	}
	require.Equal(t, rv, h.getFn(t, "todo-api").ResourceVersion, "no store write in steady state")
	c, l := h.rt.counts()
	require.Equal(t, creates, c)
	require.Equal(t, lists, l, "steady state reads replicas with Status only")
	gotLives, gotReadies := shim.probes()
	require.Equal(t, lives+5, gotLives, "one liveness probe per pass")
	require.Equal(t, readies+5, gotReadies, "one readiness probe per pass")
}

// rollOutBlocked deploys todo-api on revision 1, then applies a revision 2 whose readiness reports audit; revision 1's
// workers report it too, since the check reads the current spec. It returns both revisions' shims.
func rollOutBlocked(t *testing.T) (h *shimHarness, clk *clock.Manual, old, next *healthShim) {
	t.Helper()
	h, clk, _ = healthHarness(t)
	old, next = h.rt.serveShim(t, "todo-api-1"), h.rt.serveShim(t, "todo-api-2")
	h.deployServing(t, "todo-api", 1)
	next.setReport(audit())
	old.setReport(audit())
	h.apply(t, "todo-api", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "todo-api")
	return h, clk, old, next
}

// scenario: app-dependency-check (the Function half) — a new revision whose readiness reports a binding failure keeps
// running, not ready: RevisionReady=False DependencyNotReady naming the binding, never ShapeInvalid or Failed past the
// boot timeout; the old revision keeps serving, its own report on the new spec not judged; past the boot timeout the
// pass comes back every supervision period.
func TestScenarioAppDependencyCheck(t *testing.T) {
	t.Parallel()
	h, clk, _, _ := rollOutBlocked(t)
	requireBlocked := func(when string) {
		t.Helper()
		fn := h.getFn(t, "todo-api")
		require.Equal(t, v1.PhaseReady, fn.Status.Phase, when)
		require.Equal(t, "todo-api-1", fn.Status.ServingRevision, when)
		h.requireCondition(t, "todo-api", "Ready", v1.ConditionTrue, "")
		rr := h.requireCondition(t, "todo-api", "RevisionReady", v1.ConditionFalse, "DependencyNotReady")
		require.Equal(t, auditMessage, rr.Message, when)
		require.EqualValues(t, 2, rr.ObservedGeneration)
		require.NotEqual(t, v1.ConditionFalse, h.shapeValid(t, "todo-api"), "a dependency failure is never a shape failure")
		states := h.rt.revisionStates("todo-api")
		require.Equal(t, runtime.StateRunning, states["todo-api-1"][0], when)
		require.Equal(t, runtime.StateRunning, states["todo-api-2"][0], "the new replica keeps running: "+when)
	}
	requireBlocked("at boot")
	creates := h.creates()

	clk.Advance(time.Minute)
	res := h.reconcile(t, "todo-api")
	requireBlocked("past the boot timeout")
	require.Equal(t, healthPeriod, res.RequeueAfter)
	clk.Advance(time.Hour)
	h.reconcile(t, "todo-api")
	requireBlocked("an hour later")
	require.Equal(t, creates, h.creates(), "the reporting replica is never stopped or replaced")
	_, ready := h.upstream(t, "todo-api")
	require.True(t, ready, "revision 1 keeps serving")
}

// scenario: health-dependency-recovers (the Function half) — once the binding passes, the next pass switches todo-api
// to its new revision.
func TestScenarioHealthDependencyRecovers(t *testing.T) {
	t.Parallel()
	h, clk, old, next := rollOutBlocked(t)
	clk.Advance(time.Minute)
	require.Equal(t, healthPeriod, h.reconcile(t, "todo-api").RequeueAfter, "the next check within a supervision period")

	next.setReport(nil)
	old.setReport(nil)
	clk.Advance(healthPeriod)
	h.reconcile(t, "todo-api")
	fn := h.getFn(t, "todo-api")
	require.Equal(t, "todo-api-2", fn.Status.ServingRevision)
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	h.requireCondition(t, "todo-api", "RevisionReady", v1.ConditionTrue, "")
	require.Equal(t, v1.ConditionTrue, h.shapeValid(t, "todo-api"))
}

// scenario: health-serving-dependency-lost (the Function half) — a serving replica whose check starts failing makes
// todo-api Degraded, Ready=False DependencyNotReady, within one supervision period; it is not restarted or stopped,
// nor ever said not to have become ready, however long it lasts; it is Ready once the check passes.
func TestScenarioHealthServingDependencyLost(t *testing.T) {
	t.Parallel()
	h, clk, _ := healthHarness(t)
	shim := h.rt.serveShim(t, "todo-api-1")
	h.deployServing(t, "todo-api", 1)
	h.rt.exitRevision("todo-api", "todo-api-1", 0, runtime.StateRunning, time.Hour)
	h.reconcile(t, "todo-api")
	creates := h.creates()

	shim.setReport(audit())
	clk.Advance(healthPeriod)
	res := h.reconcile(t, "todo-api")
	requireLost := func(when string) {
		t.Helper()
		fn := h.getFn(t, "todo-api")
		require.Equal(t, v1.PhaseDegraded, fn.Status.Phase, when)
		ready := h.requireCondition(t, "todo-api", "Ready", v1.ConditionFalse, "DependencyNotReady")
		require.Equal(t, auditMessage, ready.Message, when)
		require.Equal(t, v1.ConditionTrue, h.shapeValid(t, "todo-api"), when)
		require.Equal(t, creates, h.creates(), "not restarted: "+when)
		require.Equal(t, runtime.StateRunning, h.rt.revisionStates("todo-api")["todo-api-1"][0], "not stopped: "+when)
	}
	requireLost("one period later")
	require.Equal(t, healthPeriod, res.RequeueAfter)

	h.degradedSinceAnHour(t, "todo-api")
	clk.Advance(2 * time.Minute)
	res = h.reconcile(t, "todo-api")
	requireLost("past the boot timeout, Degraded an hour")
	require.NotContains(t, h.condition(t, "todo-api", "Ready").Message, "did not become ready")
	require.Equal(t, healthPeriod, res.RequeueAfter)

	shim.setReport(nil)
	clk.Advance(healthPeriod)
	h.reconcile(t, "todo-api")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "todo-api").Status.Phase)
	h.requireCondition(t, "todo-api", "Ready", v1.ConditionTrue, "")
}

// A new Function whose only replica reports a dependency is Deploying, Ready and RevisionReady False
// DependencyNotReady: polled until the boot timeout after its start, then every supervision period, never Failed.
func TestDependencyReportAtBootIsNeverAShapeFailure(t *testing.T) {
	t.Parallel()
	h, clk, _ := healthHarness(t)
	h.rt.serveShim(t, "todo-api-1").setReport(audit())
	h.create(t, "todo-api", func(*v1.Function) {})
	res := h.reconcile(t, "todo-api")
	requireDeploying := func(when string) {
		t.Helper()
		require.Equal(t, v1.PhaseDeploying, h.getFn(t, "todo-api").Status.Phase, when)
		require.Equal(t, auditMessage, h.requireCondition(t, "todo-api", "Ready", v1.ConditionFalse, "DependencyNotReady").Message)
		require.Equal(t, auditMessage, h.requireCondition(t, "todo-api", "RevisionReady", v1.ConditionFalse, "DependencyNotReady").Message)
		require.NotEqual(t, v1.ConditionFalse, h.shapeValid(t, "todo-api"), when)
	}
	requireDeploying("at boot")
	require.Equal(t, 200*time.Millisecond, res.RequeueAfter, "polled while it boots")
	creates := h.creates()

	clk.Advance(time.Minute)
	res = h.reconcile(t, "todo-api")
	requireDeploying("at the boot timeout")
	require.Equal(t, healthPeriod, res.RequeueAfter)
	require.Equal(t, creates, h.creates())
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("todo-api")["todo-api-1"][0])
}

// Of two replicas, one report keeps the Function Ready with Ready's reason DependencyNotReady; two make it Degraded.
// Neither is restarted.
func TestDependencyReportOfOneOfTwoReplicas(t *testing.T) {
	t.Parallel()
	h, clk, _ := healthHarness(t)
	r0, r1 := h.rt.serveShimOn(t, replicaID("todo-api", 1, 0)), h.rt.serveShimOn(t, replicaID("todo-api", 1, 1))
	h.deployServing(t, "todo-api", 2)
	creates := h.creates()

	r1.setReport(&local.DependencyReport{Kind: "socket", Reason: "Timeout", Message: "the dependency check did not answer within 50ms"})
	clk.Advance(healthPeriod)
	require.Equal(t, healthPeriod, h.reconcile(t, "todo-api").RequeueAfter)
	require.Equal(t, v1.PhaseReady, h.getFn(t, "todo-api").Status.Phase)
	ready := h.requireCondition(t, "todo-api", "Ready", v1.ConditionTrue, "DependencyNotReady")
	require.Equal(t, `replicas 1 ready of 2: socket: the dependency check did not answer within 50ms`, ready.Message)

	r0.setReport(audit())
	clk.Advance(healthPeriod)
	h.reconcile(t, "todo-api")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "todo-api").Status.Phase)
	require.Equal(t, auditMessage, h.requireCondition(t, "todo-api", "Ready", v1.ConditionFalse, "DependencyNotReady").Message,
		"the lowest reporting replica's report")
	require.Equal(t, creates, h.creates())
}

// scenario: health-pool-member-dependency (the funcd half) — a pool member whose /health/members entry carries a
// dependency report is not ready, DependencyNotReady; its sibling stays Ready and the pool host is not restarted.
func TestScenarioHealthPoolMemberDependency(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	for _, n := range []string{"a", "b"} {
		h.create(t, n, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "agents" })
	}
	pools := h.pools(t, "a", "b")
	require.Equal(t, pools["a"], pools["b"], "one pool host")
	for _, n := range []string{"a", "b"} {
		require.Equal(t, v1.PhaseReady, h.getFn(t, n).Status.Phase, n)
	}
	creates := h.creates()

	h.rt.setMemberDependency("b", &local.DependencyReport{Kind: "blob", Binding: "files", Reason: "StorageUnreachable", Message: "blob: unreachable"})
	h.reconcile(t, "b")
	h.reconcile(t, "a")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "b").Status.Phase, "a serving member with no ready worker")
	ready := h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "DependencyNotReady")
	require.Equal(t, `blob binding "files": blob: unreachable`, ready.Message)
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a").Status.Phase, "the sibling is unaffected")
	h.requireCondition(t, "a", "Ready", v1.ConditionTrue, "")
	require.Equal(t, creates, h.creates(), "the pool host is not restarted")

	h.rt.setMemberDependency("b", nil)
	h.reconcile(t, "b")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)
}

// scenario: health-shim-compat (the funcd half) — a funcd with no dependency checker answers 404 on the invoke socket,
// which a new shim reads as a pass; a shim that answers readiness 200, as an old one does, is ready; a 503 whose body is
// not a report is a shim not ready yet, as before, not a dependency failure.
func TestScenarioHealthShimCompat(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "fh")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	h, _, _ := healthHarness(t)
	m := local.NewManager(dir, h.st, nil, nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	t.Cleanup(m.Close)
	sock, err := m.SocketFor("default", "todo-api")
	require.NoError(t, err)
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	t.Cleanup(c.CloseIdleConnections)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://local/health/dependencies", http.NoBody)
	require.NoError(t, err)
	resp, err := c.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	h.rt.serveShim(t, "old-1")
	h.create(t, "old", func(*v1.Function) {})
	h.reconcile(t, "old")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "old").Status.Phase)

	notYet := h.rt.serveShim(t, "loading-1")
	notYet.setReady(http.StatusServiceUnavailable)
	h.create(t, "loading", func(*v1.Function) {})
	h.reconcile(t, "loading")
	h.requireCondition(t, "loading", "Ready", v1.ConditionFalse, "ShimNotReady")
}

// While a gate fails, a Degraded Function whose serving replica reports a dependency stays Degraded, as finish keeps it,
// and serves once the report clears (Decision 5 with ADR-0221's servingReady).
func TestDependencyReportNotPromotedUnderGate(t *testing.T) {
	t.Parallel()
	h, clk, _ := healthHarness(t)
	shim := h.rt.serveShim(t, "echo-1")
	h.configMap(t, "app")
	h.create(t, "echo", bindApp)
	h.reconcile(t, "echo")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "echo").Status.Phase)
	shim.setReport(audit())
	clk.Advance(healthPeriod)
	h.reconcile(t, "echo")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "echo").Status.Phase)

	h.deleteConfigMap(t, "app")
	h.reconcile(t, "echo")
	h.requireDegradedUnderGate(t, "echo", "ConfigResolveFailed")

	shim.setReport(nil)
	h.reconcile(t, "echo")
	h.requireServesUnderGate(t, "echo", "ConfigResolveFailed")
}

// The same holds for a pooled member judged by its /health/members entry: an entry reading ready with a dependency
// report is not promoted under a gate.
func TestPooledDependencyReportNotPromotedUnderGate(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	h.configMap(t, "app")
	h.create(t, "member", pooledApp)
	h.reconcile(t, "member")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "member").Status.Phase)
	h.rt.setMemberDependency("member", audit())
	h.reconcile(t, "member")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "member").Status.Phase)

	h.deleteConfigMap(t, "app")
	h.reconcile(t, "member")
	h.requireDegradedUnderGate(t, "member", "ConfigResolveFailed")

	h.rt.setMemberDependency("member", nil)
	h.reconcile(t, "member")
	h.requireServesUnderGate(t, "member", "ConfigResolveFailed")
}
