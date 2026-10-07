package funcd

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// poolHost is a recording runtime whose workers all listen on one fake pool host: its /health/members lists every
// member of the last pool manifest as ready, and every other path answers 200.
type poolHost struct {
	*recordingRuntime
	port int
}

func newPoolHost(t *testing.T) *poolHost {
	t.Helper()
	h := &poolHost{recordingRuntime: &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}}
	srv := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(srv.Close)
	h.port = srv.Listener.Addr().(*net.TCPAddr).Port
	return h
}

func (h *poolHost) Create(ctx context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	in, _ := h.recordingRuntime.Create(ctx, spec)
	h.mu.Lock()
	defer h.mu.Unlock()
	in.IP, in.Port, in.Listened = "127.0.0.1", h.port, true
	h.insts[in.ID] = in
	return in, nil
}

func (h *poolHost) serve(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/health/members" {
		return
	}
	h.mu.Lock()
	manifest := ""
	for _, s := range h.specs {
		if m := s.Env["FUNCD_POOL_MANIFEST"]; m != "" {
			manifest = m
		}
	}
	h.mu.Unlock()
	var entries []struct {
		Name string `json:"name"`
	}
	if data, err := os.ReadFile(manifest); err == nil {
		_ = json.Unmarshal(data, &entries)
	}
	members := make([]map[string]string, 0, len(entries))
	for _, e := range entries {
		members = append(members, map[string]string{"name": e.Name, "state": "ready"})
	}
	_ = json.NewEncoder(w).Encode(members)
}

func isPool(name v1.ObjectName) bool { return strings.HasPrefix(string(name), "__pool__") }

func (h *poolHost) poolsRunning() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, in := range h.insts {
		if isPool(in.Name) && in.State == runtime.StateRunning {
			n++
		}
	}
	return n
}

func (h *poolHost) poolCreates() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, s := range h.specs {
		if isPool(s.Name) {
			n++
		}
	}
	return n
}

// pooledRig is the asleep rig with pooling on: every Function it applies declares spec.pooling.worker "shared", so
// reader and writer share one pool key when their bindings match.
type pooledRig struct {
	*asleepRig
	host *poolHost
}

// newPooledRig builds a pooled rig; activation 0 keeps the default activation hold.
func newPooledRig(t *testing.T, referentPoll, activation time.Duration, opts ...Option) *pooledRig {
	t.Helper()
	host := newPoolHost(t)
	pacing := asleepPacing(referentPoll)
	pacing.ActivationTimeout = activation
	r := newAsleepRig(t, referentPoll, append([]Option{WithRuntime(host), WithRuntimeShim("node", "shim.mjs"),
		WithPoolShim("node", "pool.mjs"), WithPacing(pacing)}, opts...)...)
	r.rt = host.recordingRuntime
	return &pooledRig{asleepRig: r, host: host}
}

func pooledReader(bind func(*v1.Function)) func(*v1.Function) {
	return func(fn *v1.Function) {
		bind(fn)
		fn.Spec.Pooling.Worker = "shared"
	}
}

// member applies a pooled Function name with the given minReplicas (replicas 1 when minReplicas is 1).
func (r *pooledRig) member(t *testing.T, name v1.ObjectName, minReplicas int, bind func(*v1.Function)) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = name, "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file://"+r.art
	fn.Spec.Replicas, fn.Spec.Scaling = minReplicas, v1.Scaling{MinReplicas: minReplicas, IdleTimeout: v1.Duration(asleepIdle)}
	fn.Spec.Pooling.Worker = "shared"
	bind(fn)
	r.apply(t, fn)
}

// writer applies the always-on sibling writer and waits for it to be Ready.
func (r *pooledRig) writer(t *testing.T, bind func(*v1.Function)) {
	t.Helper()
	r.member(t, "writer", 1, bind)
	require.Eventually(t, func() bool { return r.get(t, "writer").Status.Phase == v1.PhaseReady }, 5*time.Second,
		10*time.Millisecond, "writer is Ready")
}

func (r *pooledRig) get(t *testing.T, name v1.ObjectName) *v1.Function {
	t.Helper()
	obj, err := r.p.cfg.store.Get(context.Background(), v1.KindFunction.GVK(), "default", name)
	require.NoError(t, err)
	return obj.(*v1.Function)
}

// wakeReady wakes reader with a call and waits for it to be Ready in a running pool worker.
func (r *pooledRig) wakeReady(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, r.wake(ctx), "the call is answered")
	require.Equal(t, v1.PhaseReady, r.fn(t).Status.Phase)
	require.Equal(t, 1, r.host.poolsRunning(), "the pool worker runs")
}

// gateCase is a dependency of reader and writer that goes away while reader sleeps.
type gateCase struct {
	setup   func(*testing.T, *asleepRig)
	bind    func(*v1.Function)
	trigger func(*testing.T, *asleepRig)
	phase   v1.Phase
	reason  string
}

func catalogDeleted() gateCase {
	return gateCase{
		setup:   func(t *testing.T, r *asleepRig) { r.catalog(t) },
		bind:    bindLake,
		trigger: func(t *testing.T, r *asleepRig) { r.del(t, v1.KindCatalogService, "lake") },
		phase:   v1.PhasePending, reason: "CatalogNotReady",
	}
}

func engineDown() gateCase {
	g := catalogDeleted()
	g.trigger = func(_ *testing.T, r *asleepRig) { r.engine.down.Store(true) }
	return g
}

func secretDeleted() gateCase {
	return gateCase{
		setup:   func(t *testing.T, r *asleepRig) { r.secret(t) },
		bind:    bindCreds,
		trigger: func(t *testing.T, r *asleepRig) { r.del(t, v1.KindSecret, "creds") },
		phase:   v1.PhaseFailed, reason: "SecretResolveFailed",
	}
}

// asleepAlone wakes reader alone in its pool, takes g's dependency away and requires reader asleep and quiet with
// its pool worker stopped.
func (r *pooledRig) asleepAlone(t *testing.T, g gateCase) {
	t.Helper()
	g.setup(t, r.asleepRig)
	r.reader(t, pooledReader(g.bind))
	r.wakeReady(t)
	writes := r.phases(t)
	start := time.Now()
	g.trigger(t, r.asleepRig)
	r.requireAsleep(t, start, writes, g.phase, g.reason)
	require.Zero(t, r.host.poolsRunning(), "the pool worker stops")
}

// asleepBesideWriter wakes reader beside writer, takes g's dependency away and requires reader asleep and quiet while
// writer stays Ready in the running pool worker.
func (r *pooledRig) asleepBesideWriter(t *testing.T, g gateCase) {
	t.Helper()
	g.setup(t, r.asleepRig)
	r.writer(t, g.bind)
	r.reader(t, pooledReader(g.bind))
	r.wakeReady(t)
	writes := r.phases(t)
	start := time.Now()
	g.trigger(t, r.asleepRig)
	r.requireAsleep(t, start, writes, g.phase, g.reason)
	w := r.get(t, "writer")
	require.Equal(t, v1.PhaseReady, w.Status.Phase, "writer stays Ready")
	require.Equal(t, v1.ConditionFalse, condition(w, "RevisionReady").Status)
	require.Equal(t, g.reason, condition(w, "RevisionReady").Reason)
	require.Equal(t, 1, r.host.poolsRunning(), "the pool worker keeps running")
}

// TestIssue796 holds scenarios pooled-gate-while-asleep-goes-quiet (alone/*) and pooled-sibling-keeps-pool
// (sibling/*): an asleep pooled member whose gate fails goes to the gate's phase once with Asleep=True, never
// Degraded, and stays quiet; its pool worker stops unless a sibling wants it.
func TestIssue796(t *testing.T) {
	t.Parallel()
	t.Run("alone", func(t *testing.T) {
		t.Parallel()
		for name, g := range map[string]gateCase{"catalog-deleted": catalogDeleted(), "engine-down": engineDown(), "secret-deleted": secretDeleted()} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				newPooledRig(t, 0, 0).asleepAlone(t, g)
			})
		}
	})
	t.Run("sibling", func(t *testing.T) {
		t.Parallel()
		for name, g := range map[string]gateCase{"catalog-deleted": catalogDeleted(), "secret-deleted": secretDeleted()} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				newPooledRig(t, 0, 0).asleepBesideWriter(t, g)
			})
		}
	})
}

// scenario: pooled-call-while-gated-goes-pending — a call that wakes an asleep pooled member while its catalog is
// still missing writes Deploying, then Pending with Asleep=False, never Degraded; the call is held, then refused.
func TestScenarioPooledCallWhileGatedGoesPending(t *testing.T) {
	t.Parallel()
	const hold = 2 * time.Second
	r := newPooledRig(t, 0, hold)
	r.asleepBesideWriter(t, catalogDeleted())
	writes := r.phases(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	called := time.Now()
	err := r.wake(ctx)
	require.Error(t, err, "the call is refused")
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.GreaterOrEqual(t, time.Since(called), hold, "the call is held first")
	fn := r.fn(t)
	require.Equal(t, v1.PhasePending, fn.Status.Phase)
	require.Equal(t, "CatalogNotReady", condition(fn, "Ready").Reason)
	require.Equal(t, v1.ConditionFalse, condition(fn, "Asleep").Status)
	got := writes()
	require.NotContains(t, got, v1.PhaseDegraded, "reader is never Degraded")
	i := indexOf(got, v1.PhaseDeploying)
	require.GreaterOrEqual(t, i, 0, "reader writes Deploying; writes %v", got)
	require.Contains(t, got[i:], v1.PhasePending, "then Pending; writes %v", got)
}

func indexOf(phases []v1.Phase, p v1.Phase) int {
	for i, x := range phases {
		if x == p {
			return i
		}
	}
	return -1
}

// requireIdleAsleepCleared waits for reader to be Idle/NoReplicas with Asleep=False and no replica.
func (r *pooledRig) requireIdleAsleepCleared(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		fn := r.fn(t)
		ready, asleep := condition(fn, "Ready"), condition(fn, "Asleep")
		return fn.Status.Phase == v1.PhaseIdle && ready.Reason == "NoReplicas" && asleep.Status == v1.ConditionFalse
	}, 5*time.Second, 10*time.Millisecond, "reader goes Idle/NoReplicas with Asleep=False")
	require.Zero(t, r.fn(t).Status.Replicas)
}

// scenario: pooled-dependency-returns-stays-asleep — once the catalog of an asleep pooled member alone in its pool
// returns, it goes Idle/NoReplicas with Asleep=False and no pool worker is created until a call wakes it to Ready.
func TestScenarioPooledDependencyReturnsStaysAsleep(t *testing.T) {
	t.Parallel()
	r := newPooledRig(t, 100*time.Millisecond, 0)
	r.asleepAlone(t, catalogDeleted())
	creates := r.host.poolCreates()
	r.applyCatalog(t)
	r.requireIdleAsleepCleared(t)
	require.Never(t, func() bool { return r.host.poolCreates() != creates || r.host.poolsRunning() != 0 }, time.Second,
		20*time.Millisecond, "no pool worker is created before a call")
	r.wakeReady(t)
}

// scenario: pooled-dependency-returns-beside-sibling — once the catalog returns, an asleep pooled member beside an
// always-on sibling goes Idle/NoReplicas with Asleep=False, is never Ready without a call and stays quiet; the sibling
// stays Ready in the running pool worker, and a call wakes the member to Ready.
func TestScenarioPooledDependencyReturnsBesideSibling(t *testing.T) {
	t.Parallel()
	r := newPooledRig(t, 100*time.Millisecond, 0)
	r.asleepBesideWriter(t, catalogDeleted())
	writes := r.phases(t)
	r.applyCatalog(t)
	r.requireIdleAsleepCleared(t)
	rv := r.fn(t).ResourceVersion
	held := assert.Never(t, func() bool { return r.fn(t).ResourceVersion != rv }, 3*time.Second, 20*time.Millisecond)
	require.True(t, held, "reader's resourceVersion holds; writes %v", writes())
	require.NotContains(t, writes(), v1.PhaseReady, "reader is never Ready without a call")
	require.Equal(t, v1.PhaseReady, r.get(t, "writer").Status.Phase, "writer stays Ready")
	require.Equal(t, 1, r.host.poolsRunning(), "the pool worker keeps running")
	r.wakeReady(t)
}

func noBinding(*v1.Function) {}

// scenario: pool-full — a member applied to a full pool writes Pending/PoolFull once with no Asleep condition and stays
// quiet, the admitted member untouched; an asleep admitted member displaced by a newcomer whose name sorts earlier
// writes Pending/PoolFull once with Asleep=True.
func TestScenarioPoolFull(t *testing.T) {
	t.Parallel()
	t.Run("newcomer-not-asleep", func(t *testing.T) {
		t.Parallel()
		r := newPooledRig(t, 0, 0, WithPoolLimit(1))
		r.writer(t, noBinding)
		wrv := r.get(t, "writer").ResourceVersion
		r.member(t, "zulu", 0, noBinding)
		require.Eventually(t, func() bool {
			fn := r.get(t, "zulu")
			return fn.Status.Phase == v1.PhasePending && condition(fn, "PoolFull").Status == v1.ConditionTrue
		}, 5*time.Second, 10*time.Millisecond, "zulu is Pending/PoolFull")
		fn := r.get(t, "zulu")
		_, hasAsleep := fn.Status.Conditions.Get("Asleep")
		require.False(t, hasAsleep, "zulu carries no Asleep condition")
		held := assert.Never(t, func() bool { return r.get(t, "zulu").ResourceVersion != fn.ResourceVersion }, time.Second, 20*time.Millisecond)
		require.True(t, held, "zulu stays quiet")
		require.Equal(t, wrv, r.get(t, "writer").ResourceVersion, "writer is untouched")
		require.Equal(t, 1, r.host.poolsRunning(), "the pool worker keeps running")
	})
	t.Run("asleep-member-displaced", func(t *testing.T) {
		t.Parallel()
		r := newPooledRig(t, 0, 0, WithPoolLimit(1))
		r.reader(t, pooledReader(noBinding))
		r.wakeReady(t)
		require.Eventually(t, func() bool { return r.fn(t).Status.Phase == v1.PhaseIdle && r.host.poolsRunning() == 0 },
			5*time.Second, 10*time.Millisecond, "reader is reclaimed")
		idle := ""
		require.Eventually(t, func() bool {
			rv := r.fn(t).ResourceVersion
			held := rv == idle
			idle = rv
			return held
		}, 5*time.Second, 300*time.Millisecond, "reader's passes settle before the newcomer, so only its apply can reach reader")
		writes := r.phases(t)
		r.member(t, "alpha", 0, noBinding)
		require.Eventually(t, func() bool {
			fn := r.fn(t)
			return fn.Status.Phase == v1.PhasePending && condition(fn, "PoolFull").Status == v1.ConditionTrue &&
				condition(fn, "Asleep").Status == v1.ConditionTrue
		}, 5*time.Second, 10*time.Millisecond, "reader is Pending/PoolFull with Asleep=True; writes %v", writes())
		rv := r.fn(t).ResourceVersion
		held := assert.Never(t, func() bool { return r.fn(t).ResourceVersion != rv }, time.Second, 20*time.Millisecond)
		require.True(t, held, "reader stays quiet; writes %v", writes())
		got := writes()
		i := indexOf(got, v1.PhasePending)
		require.GreaterOrEqual(t, i, 0, "writes %v", got)
		require.Equal(t, []v1.Phase{v1.PhasePending}, got[i:], "one Pending/PoolFull write; writes %v", got)
	})
}
