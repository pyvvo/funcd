package function_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime"
)

// engineID is the instance ID of CatalogService lake's engine (ADR-0152 Contracts).
const engineID runtime.InstanceID = "default/lake/r0"

// kindGuard records every Start, Stop and Remove the Function reconciler sends to the CatalogService engine.
type kindGuard struct {
	*fakeRuntime
	mu      sync.Mutex
	touched []string
}

func (g *kindGuard) note(call string, id runtime.InstanceID) {
	if id != engineID {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.touched = append(g.touched, call)
}

func (g *kindGuard) Start(ctx context.Context, id runtime.InstanceID) error {
	g.note("start", id)
	return g.fakeRuntime.Start(ctx, id)
}

func (g *kindGuard) Stop(ctx context.Context, id runtime.InstanceID) error {
	g.note("stop", id)
	return g.fakeRuntime.Stop(ctx, id)
}

func (g *kindGuard) Remove(ctx context.Context, id runtime.InstanceID) error {
	g.note("remove", id)
	return g.fakeRuntime.Remove(ctx, id)
}

func (g *kindGuard) engineCalls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.touched...)
}

// ownerKindPair is a Function reconciler and a CatalogService provider runtime over one fake runtime, as funcd wires
// them (pkg/funcd), with the engine answering its readiness probe.
type ownerKindPair struct {
	*shimHarness
	guard  *kindGuard
	pr     provider.Runtime
	engine provider.ProviderSpec
}

func newOwnerKindPair(t *testing.T) *ownerKindPair {
	t.Helper()
	g := &kindGuard{}
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		g.fakeRuntime = d.Runtime.(*fakeRuntime)
		d.Runtime = g
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	pr, err := provider.NewRuntime(provider.Deps{Runtime: h.rt, OwnerKind: v1.KindCatalogService})
	require.NoError(t, err)
	return &ownerKindPair{shimHarness: h, guard: g, pr: pr, engine: provider.ProviderSpec{
		Ref:       provider.ProviderRef{Namespace: "default", Name: "lake"},
		Image:     "funcd/runtime-duckdb",
		Port:      port,
		Readiness: provider.ReadinessProbe{Path: "/", ExpectStatus: http.StatusOK},
		Replicas:  1,
	}}
}

// convergeReady converges the CatalogService until its engine is Ready: a fresh engine is Created on the pass that
// starts it, so it is first counted on the next one.
func (p *ownerKindPair) convergeReady(t *testing.T) provider.ProviderStatus {
	t.Helper()
	for range 3 {
		st, err := p.pr.Converge(context.Background(), p.engine)
		require.NoError(t, err)
		if st.Ready {
			return st
		}
	}
	t.Fatal("the CatalogService engine did not become Ready")
	return provider.ProviderStatus{}
}

// reconcileUntil runs Function passes until done holds for the stored Function.
func (p *ownerKindPair) reconcileUntil(t *testing.T, name string, done func(*v1.Function) bool) *v1.Function {
	t.Helper()
	for range 20 {
		p.reconcile(t, name)
		if fn := p.getFn(t, name); done(fn) {
			return fn
		}
		settle()
	}
	t.Fatalf("function %s did not converge", name)
	return nil
}

// workers returns the listed workers of name by owner kind.
func (p *ownerKindPair) workers(t *testing.T, name v1.ObjectName) map[v1.Kind][]runtime.Instance {
	t.Helper()
	insts, err := p.rt.List(context.Background(), "default")
	require.NoError(t, err)
	out := map[v1.Kind][]runtime.Instance{}
	for _, in := range insts {
		if in.Name == name {
			out[in.OwnerKind] = append(out[in.OwnerKind], in)
		}
	}
	return out
}

// engineURL is the upstream the Function would route to if it took the engine for one of its replicas.
func (p *ownerKindPair) engineURL() string {
	return fmt.Sprintf("http://%s:%d", p.rt.ip, p.rt.port)
}

// scaleToZero sets lake to scale to zero and reclaims it, as the activator does once it is idle (ADR-0142).
func (p *ownerKindPair) scaleToZero(t *testing.T) {
	t.Helper()
	p.apply(t, "lake", func(fn *v1.Function) { fn.Spec.Replicas = 0 })
	p.reconcile(t, "lake")
	p.setPhase(t, "lake", v1.PhaseIdle)
	p.reconcileUntil(t, "lake", func(fn *v1.Function) bool { return fn.Status.Replicas == 0 })
}

func servesLatest(fn *v1.Function) bool {
	return fn.Status.Phase == v1.PhaseReady && fn.Status.ServingRevision == fn.Status.CurrentRevision &&
		fn.Status.DrainingRevision == ""
}

// scenario: same-name-function-and-catalog-stay-ready (ADR-0152) — a Function and a CatalogService both named lake: the
// Function's redeploy and scaling never move the CatalogService off its engine, and the Function serves its latest
// revision after each redeploy.
func TestScenarioSameNameFunctionAndCatalogStayReady(t *testing.T) {
	t.Parallel()
	p := newOwnerKindPair(t)
	ctx := context.Background()
	p.convergeReady(t)
	engine, err := p.rt.Status(ctx, engineID)
	require.NoError(t, err)
	stayed := func(step string) {
		t.Helper()
		st, cerr := p.pr.Converge(ctx, p.engine)
		require.NoError(t, cerr)
		require.True(t, st.Ready, "%s: the CatalogService stays Ready", step)
		require.Equal(t, 1, st.Running, step)
		got, serr := p.rt.Status(ctx, engineID)
		require.NoError(t, serr)
		require.Equal(t, runtime.StateRunning, got.State, step)
		require.Equal(t, engine.CreatedAt, got.CreatedAt, "%s: the CatalogService runs on the same engine worker", step)
		require.Equal(t, v1.KindCatalogService, got.OwnerKind, step)
	}
	urls := map[string]string{}
	for _, rev := range []v1.ObjectName{"lake-1", "lake-2", "lake-3", "lake-4"} {
		urls[string(rev)], _ = p.rt.serveRevision(t, rev, http.StatusOK)
	}
	serving := func(step string) {
		t.Helper()
		fn := p.reconcileUntil(t, "lake", servesLatest)
		up, ready := p.upstream(t, "lake")
		require.True(t, ready, step)
		require.Equal(t, urls[fn.Status.ServingRevision], up, "%s: the Function serves its latest revision", step)
	}

	p.deployReady(t, "lake")
	serving("deploy")
	stayed("deploy")

	p.apply(t, "lake", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	serving("redeploy")
	require.NotEqual(t, "lake-1", p.getFn(t, "lake").Status.ServingRevision, "the redeploy switched revisions")
	stayed("redeploy")

	p.apply(t, "lake", func(fn *v1.Function) { fn.Spec.Replicas = 2 })
	fn := p.reconcileUntil(t, "lake", func(fn *v1.Function) bool { return servesLatest(fn) && fn.Status.Replicas == 2 })
	require.Equal(t, 2, fn.Status.Replicas, "the engine is not counted as a Function replica")
	stayed("scale 1 → 2")

	p.scaleToZero(t)
	for _, in := range p.workers(t, "lake")[v1.KindFunction] {
		require.True(t, in.State.Terminal(), "scaled to zero: no Function worker of lake runs (%s)", in.ID)
	}
	stayed("scale to zero")
	require.Empty(t, p.guard.engineCalls(), "the Function reconciler never touched the engine")
}

// scenario: function-pass-never-touches-engine (ADR-0152) — Function passes beside a running or exited engine of the
// same name never start, stop or remove it, never count it as a replica and never route to it.
func TestScenarioFunctionPassNeverTouchesEngine(t *testing.T) {
	t.Parallel()
	for _, engineState := range []runtime.State{runtime.StateRunning, runtime.StateFailed} {
		t.Run(string(engineState), func(t *testing.T) {
			t.Parallel()
			p := newOwnerKindPair(t)
			ctx := context.Background()
			p.convergeReady(t)
			p.rt.mu.Lock()
			p.rt.state[engineID] = engineState
			p.rt.mu.Unlock()
			url1, _ := p.rt.serveRevision(t, "lake-1", http.StatusOK)
			url2, _ := p.rt.serveRevision(t, "lake-2", http.StatusOK)
			routesTo := func(step, want string) {
				t.Helper()
				up, ready := p.upstream(t, "lake")
				require.True(t, ready, step)
				require.Equal(t, want, up, step)
				require.NotEqual(t, p.engineURL(), up, "%s: the engine is never the Function's upstream", step)
			}

			p.deployReady(t, "lake")
			fn := p.reconcileUntil(t, "lake", servesLatest)
			require.Equal(t, 1, fn.Status.Replicas, "deploy: the engine is not counted as a Function replica")
			routesTo("deploy", url1)

			p.apply(t, "lake", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
			fn = p.reconcileUntil(t, "lake", servesLatest)
			require.Equal(t, "lake-2", fn.Status.ServingRevision, "redeploy and drain")
			require.Equal(t, 1, fn.Status.Replicas)
			routesTo("redeploy", url2)

			p.scaleToZero(t)

			require.NoError(t, p.st.Delete(ctx, v1.KindFunction.GVK(), "default", "lake", ""))
			p.reconcile(t, "lake")
			require.Empty(t, p.workers(t, "lake")[v1.KindFunction], "delete: every Function worker of lake is gone")

			require.Empty(t, p.guard.engineCalls(), "no Function pass started, stopped or removed the engine")
			got, err := p.rt.Status(ctx, engineID)
			require.NoError(t, err)
			require.Equal(t, engineState, got.State, "the engine is left as it was")
			require.False(t, p.rt.wasRemoved(engineID))
		})
	}
}

// scenario: owner-kind-across-daemon-restart (ADR-0152) — after a restart the runtime lists no worker; both return to
// Ready, each creating only its own workers, and every listed worker reports the kind that created it.
func TestScenarioOwnerKindAcrossDaemonRestart(t *testing.T) {
	t.Parallel()
	p := newOwnerKindPair(t)
	p.convergeReady(t)
	p.rt.serveRevision(t, "lake-1", http.StatusOK)
	p.deployReady(t, "lake")

	p.rt.forget()
	p.reconcileUntil(t, "lake", func(fn *v1.Function) bool {
		running := 0
		for _, in := range p.workers(t, "lake")[v1.KindFunction] {
			if in.State == runtime.StateRunning {
				running++
			}
		}
		return servesLatest(fn) && running == 1
	})
	st := p.convergeReady(t)
	require.Equal(t, p.rt.ip+":"+strconv.Itoa(p.engine.Port), st.Address, "the CatalogService publishes its own engine")
	_, ready := p.upstream(t, "lake")
	require.True(t, ready, "the Function serves again")

	byKind := p.workers(t, "lake")
	require.Len(t, byKind, 2, "only Function and CatalogService workers are listed")
	require.Len(t, byKind[v1.KindCatalogService], 1, "the provider created only its engine")
	require.Equal(t, engineID, byKind[v1.KindCatalogService][0].ID)
	for _, in := range byKind[v1.KindFunction] {
		require.NotEmpty(t, in.Revision, "a Function worker (%s) carries its revision", in.ID)
	}
	p.rt.mu.Lock()
	defer p.rt.mu.Unlock()
	for id, spec := range p.rt.specs {
		want := v1.KindFunction
		if id == engineID {
			want = v1.KindCatalogService
		}
		require.Equal(t, want, spec.OwnerKind, "worker %s was created by its own kind", id)
	}
}
