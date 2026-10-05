package funcd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// toggleEngine is a CatalogService engine that reports Ready unless the test takes it down.
type toggleEngine struct {
	addr string
	down atomic.Bool
}

func (e *toggleEngine) Converge(context.Context, provider.ProviderSpec) (provider.ProviderStatus, error) {
	return provider.ProviderStatus{Running: 1, Ready: !e.down.Load(), Address: e.addr}, nil
}

func (*toggleEngine) Teardown(context.Context, provider.ProviderRef) error { return nil }

// asleepRig is an assembled in-memory platform on a recording runtime, reclaiming idle Functions every 50 ms and
// re-converging a Ready catalog engine every 100 ms, with objects applied and deleted through the sdk so admission runs
// (issue #769).
type asleepRig struct {
	p      *Platform
	rt     *recordingRuntime
	c      *sdk.Client
	engine *toggleEngine
	art    string
}

const asleepIdle = 400 * time.Millisecond

func asleepPacing(referentPoll time.Duration) Pacing {
	return Pacing{
		ReclaimInterval: 50 * time.Millisecond, HandOutSettle: 50 * time.Millisecond, ReferentPollInterval: referentPoll,
		EnginePollInterval: 50 * time.Millisecond, SupervisionPeriod: 100 * time.Millisecond,
	}
}

// newAsleepRig builds the rig; opts come after its own, so a later WithRuntime or WithPacing replaces them.
func newAsleepRig(t *testing.T, referentPoll time.Duration, opts ...Option) *asleepRig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	r := &asleepRig{
		rt:     &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}},
		engine: &toggleEngine{addr: strings.TrimPrefix(srv.URL, "http://")},
	}
	p, err := New(append([]Option{InMemory(), WithoutLogCompaction(), WithRuntime(r.rt), WithCatalogProviderRuntime(r.engine),
		WithPacing(asleepPacing(referentPoll))}, opts...)...)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	r.p = p
	r.c, err = sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	r.art = filepath.Join(dir, "reader.mjs")
	require.NoError(t, os.WriteFile(r.art, []byte("export function handle() { return {}; }\n"), 0o600))
	return r
}

func (r *asleepRig) apply(t *testing.T, obj v1.Object) {
	t.Helper()
	_, err := r.c.Apply(context.Background(), obj)
	require.NoError(t, err, "apply %s", obj.GroupVersionKind().Kind)
}

func (r *asleepRig) del(t *testing.T, kind v1.Kind, name v1.ObjectName) {
	t.Helper()
	require.NoError(t, r.c.Delete(context.Background(), kind, "default", name))
}

func (r *asleepRig) bucket(t *testing.T, name, prefix, owner v1.ObjectName) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindBucket)
	b := obj.(*v1.Bucket)
	b.Name, b.Namespace, b.ResourceGroup = name, "default", "rg1"
	b.Spec = v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: string(prefix), Owner: owner}}}
	r.apply(t, b)
}

// catalog applies Bucket lakehouse and CatalogService lake and waits for lake to be Ready.
func (r *asleepRig) catalog(t *testing.T) {
	t.Helper()
	r.bucket(t, "lakehouse", "raw", "lake")
	r.applyCatalog(t)
}

func (r *asleepRig) applyCatalog(t *testing.T) {
	t.Helper()
	r.apply(t, lakeCatalogService("default"))
	require.Eventually(t, func() bool {
		obj, err := r.p.cfg.store.Get(context.Background(), v1.KindCatalogService.GVK(), "default", "lake")
		return err == nil && obj.(*v1.CatalogService).Status.Phase == v1.PhaseReady
	}, 5*time.Second, 10*time.Millisecond, "catalog lake is Ready")
}

func (r *asleepRig) secret(t *testing.T) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindSecret)
	s := obj.(*v1.Secret)
	s.Name, s.Namespace, s.ResourceGroup = "creds", "default", "rg1"
	s.Spec.Data = map[string][]byte{"API_KEY": []byte("k")}
	r.apply(t, s)
}

// reader applies Function reader, minReplicas 0 with a 400 ms idle timeout unless bind changes it.
func (r *asleepRig) reader(t *testing.T, bind func(*v1.Function)) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "reader", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file://"+r.art
	fn.Spec.Scaling = v1.Scaling{MinReplicas: 0, IdleTimeout: asleepIdle}
	bind(fn)
	r.apply(t, fn)
}

func (r *asleepRig) fn(t *testing.T) *v1.Function {
	t.Helper()
	obj, err := r.p.cfg.store.Get(context.Background(), v1.KindFunction.GVK(), "default", "reader")
	require.NoError(t, err)
	return obj.(*v1.Function)
}

func (r *asleepRig) wake(ctx context.Context) error {
	_, err := r.p.activator.Wake(ctx, activator.FunctionRef{Namespace: "default", Name: "reader"})
	return err
}

// wakeReady wakes reader with a call and waits for it to be Ready with one listening worker.
func (r *asleepRig) wakeReady(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, r.wake(ctx), "the call is answered")
	require.Equal(t, v1.PhaseReady, r.fn(t).Status.Phase)
	require.Equal(t, 1, r.running(), "one worker listens")
}

func (r *asleepRig) running() int {
	r.rt.mu.Lock()
	defer r.rt.mu.Unlock()
	n := 0
	for _, in := range r.rt.insts {
		if in.Name == "reader" && in.State == runtime.StateRunning {
			n++
		}
	}
	return n
}

func (r *asleepRig) creates() int { return len(r.rt.created()["reader"]) }

// phases records the phase of every write to reader from now on.
func (r *asleepRig) phases(t *testing.T) func() []v1.Phase {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w, err := r.p.cfg.store.Watch(ctx, v1.KindFunction.GVK(), store.WatchOptions{Namespace: "default"})
	require.NoError(t, err)
	var (
		mu  sync.Mutex
		got []v1.Phase
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range w.ResultChan() {
			if fn, ok := ev.Object.(*v1.Function); ok && fn.Name == "reader" {
				mu.Lock()
				got = append(got, fn.Status.Phase)
				mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() { w.Stop(); cancel(); <-done })
	return func() []v1.Phase {
		mu.Lock()
		defer mu.Unlock()
		return append([]v1.Phase(nil), got...)
	}
}

func condition(fn *v1.Function, typ v1.ConditionType) v1.Condition {
	c, _ := fn.Status.Conditions.Get(typ)
	return c
}

// requireAsleep waits for reader to reach phase with Ready=False/reason and Asleep=True, with no worker running and
// no Degraded write, then requires its resourceVersion to hold until 3 s after start.
func (r *asleepRig) requireAsleep(t *testing.T, start time.Time, writes func() []v1.Phase, phase v1.Phase, reason string) {
	t.Helper()
	reached := assert.Eventually(t, func() bool {
		fn := r.fn(t)
		ready, asleep := condition(fn, "Ready"), condition(fn, "Asleep")
		return fn.Status.Phase == phase && ready.Status == v1.ConditionFalse && ready.Reason == reason && asleep.Status == v1.ConditionTrue
	}, 3*time.Second, 10*time.Millisecond)
	require.True(t, reached, "reader goes %s/%s with Asleep=True; running %d, writes %v", phase, reason, r.running(), writes())
	rv := r.fn(t).ResourceVersion
	held := assert.Never(t, func() bool { return r.fn(t).ResourceVersion != rv }, time.Until(start.Add(3*time.Second)), 20*time.Millisecond)
	require.True(t, held, "reader's resourceVersion holds; writes %v", writes())
	require.Zero(t, r.running(), "no worker runs")
	require.NotContains(t, writes(), v1.PhaseDegraded, "reader is never Degraded")
	require.Contains(t, writes(), v1.PhaseIdle, "reader went Idle first")
	require.Zero(t, r.fn(t).Status.Replicas)
}

func bindLake(fn *v1.Function) {
	fn.Spec.Catalogs = []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}}
}

func bindCreds(fn *v1.Function) { fn.Spec.Secrets = []v1.ObjectName{"creds"} }

// scenario: catalog-gate-while-asleep-goes-pending (ADR-0192, issue #769) — a woken scale-to-zero catalog consumer whose
// catalog goes away is reclaimed, then held Pending/CatalogNotReady with Asleep=True and no worker, and stays quiet.
func TestIssue769_CatalogGateWhileAsleepGoesPending(t *testing.T) {
	t.Parallel()
	for name, trigger := range map[string]func(*testing.T, *asleepRig){
		"deleted-via-api": func(t *testing.T, r *asleepRig) { r.del(t, v1.KindCatalogService, "lake") },
		"engine-down":     func(_ *testing.T, r *asleepRig) { r.engine.down.Store(true) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newAsleepRig(t, 0)
			r.catalog(t)
			r.reader(t, bindLake)
			r.wakeReady(t)
			writes := r.phases(t)
			start := time.Now()
			trigger(t, r)
			r.requireAsleep(t, start, writes, v1.PhasePending, "CatalogNotReady")
		})
	}
}

// scenario: secret-gate-while-asleep-goes-failed (ADR-0192, issue #769) — a woken scale-to-zero Function whose bound
// Secret is deleted is reclaimed, then held Failed/SecretResolveFailed with Asleep=True and no worker, stays quiet, and
// a call to it is refused at once.
func TestIssue769_SecretGateWhileAsleepGoesFailed(t *testing.T) {
	t.Parallel()
	r := newAsleepRig(t, 0)
	r.secret(t)
	r.reader(t, bindCreds)
	r.wakeReady(t)
	writes := r.phases(t)
	start := time.Now()
	r.del(t, v1.KindSecret, "creds")
	r.requireAsleep(t, start, writes, v1.PhaseFailed, "SecretResolveFailed")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	called := time.Now()
	err := r.wake(ctx)
	require.Error(t, err)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.Contains(t, err.Error(), "is Failed (SecretResolveFailed)")
	require.Less(t, time.Since(called), time.Second, "the call is refused at once")
	require.Zero(t, r.running(), "the refused call boots no worker")
}

// scenario: dependency-returns-stays-asleep (ADR-0192) — once the dependency of an asleep replicas: 1 Function returns,
// it goes Idle/NoReplicas with Asleep=False and no worker is created until a call wakes it; a call that wakes it while
// it is still Pending with Asleep=True brings it to Ready with Asleep=False.
func TestScenarioDependencyReturnsStaysAsleep(t *testing.T) {
	t.Parallel()
	replicas1 := func(bind func(*v1.Function)) func(*v1.Function) {
		return func(fn *v1.Function) { bind(fn); fn.Spec.Replicas = 1 }
	}
	// created is taken before the dependency returns, so a worker booted by the pass that sees it back is counted.
	stayAsleep := func(t *testing.T, r *asleepRig, created int) {
		t.Helper()
		require.Eventually(t, func() bool {
			fn := r.fn(t)
			ready, asleep := condition(fn, "Ready"), condition(fn, "Asleep")
			return fn.Status.Phase == v1.PhaseIdle && ready.Reason == "NoReplicas" && asleep.Status == v1.ConditionFalse
		}, 5*time.Second, 10*time.Millisecond, "reader goes Idle/NoReplicas with Asleep=False")
		require.Never(t, func() bool { return r.creates() != created || r.running() != 0 }, time.Second, 20*time.Millisecond,
			"no worker is created before a call")
		require.Zero(t, r.fn(t).Status.Replicas)
		r.wakeReady(t)
	}
	t.Run("pending", func(t *testing.T) {
		t.Parallel()
		r := newAsleepRig(t, 100*time.Millisecond)
		r.catalog(t)
		r.reader(t, replicas1(bindLake))
		r.wakeReady(t)
		writes := r.phases(t)
		start := time.Now()
		r.del(t, v1.KindCatalogService, "lake")
		r.requireAsleep(t, start, writes, v1.PhasePending, "CatalogNotReady")
		created := r.creates()
		r.applyCatalog(t)
		stayAsleep(t, r, created)
	})
	t.Run("failed", func(t *testing.T) {
		t.Parallel()
		r := newAsleepRig(t, 100*time.Millisecond)
		r.secret(t)
		r.reader(t, replicas1(bindCreds))
		r.wakeReady(t)
		writes := r.phases(t)
		start := time.Now()
		r.del(t, v1.KindSecret, "creds")
		r.requireAsleep(t, start, writes, v1.PhaseFailed, "SecretResolveFailed")
		created := r.creates()
		r.secret(t)
		stayAsleep(t, r, created)
	})
	t.Run("woken-while-asleep", func(t *testing.T) {
		t.Parallel()
		// No Bucket event reconciles a Function and the referent poll is a minute away, so the call's wake is the first
		// pass to see the Bucket back while Asleep=True.
		r := newAsleepRig(t, time.Minute)
		r.bucket(t, "data", "in", "reader")
		r.reader(t, replicas1(func(fn *v1.Function) {
			fn.Spec.Blob = []v1.FunctionBlob{{Alias: "in", Bucket: "data", Prefix: "in"}}
		}))
		r.wakeReady(t)
		writes := r.phases(t)
		start := time.Now()
		// a store delete: admission's deletion protection refuses a bound Bucket
		require.NoError(t, r.p.cfg.store.Delete(context.Background(), v1.KindBucket.GVK(), "default", "data", ""))
		r.requireAsleep(t, start, writes, v1.PhasePending, "BucketNotFound")
		r.bucket(t, "data", "in", "reader")
		r.wakeReady(t)
		require.Equal(t, v1.ConditionFalse, condition(r.fn(t), "Asleep").Status, "the woken pass clears Asleep")
	})
}

// scenario: min-replicas-one-unaffected (ADR-0192) — an always-on catalog consumer whose catalog is deleted stays Ready
// with its worker listening and RevisionReady=False/CatalogNotReady; reclaim never puts it to sleep.
func TestScenarioMinReplicasOneUnaffected(t *testing.T) {
	t.Parallel()
	r := newAsleepRig(t, 0)
	r.catalog(t)
	r.reader(t, func(fn *v1.Function) {
		bindLake(fn)
		fn.Spec.Scaling.MinReplicas = 1
	})
	require.Eventually(t, func() bool { return r.fn(t).Status.Phase == v1.PhaseReady }, 5*time.Second, 10*time.Millisecond, "reader is Ready")
	writes := r.phases(t)
	r.del(t, v1.KindCatalogService, "lake")
	require.Eventually(t, func() bool {
		rr := condition(r.fn(t), "RevisionReady")
		return rr.Status == v1.ConditionFalse && rr.Reason == "CatalogNotReady"
	}, 3*time.Second, 10*time.Millisecond, "reader shows CatalogNotReady")
	stayed := assert.Never(t, func() bool { return r.fn(t).Status.Phase != v1.PhaseReady || r.running() != 1 }, 3*time.Second, 20*time.Millisecond)
	require.True(t, stayed, "reader stays Ready with its worker listening; writes %v", writes())
	fn := r.fn(t)
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, 1, r.running(), "its worker still listens")
	require.Equal(t, v1.ConditionFalse, condition(fn, "RevisionReady").Status)
	require.Equal(t, "CatalogNotReady", condition(fn, "RevisionReady").Reason)
	for _, ph := range writes() {
		require.Equal(t, v1.PhaseReady, ph, "no Idle, Degraded or Pending write")
	}
}
