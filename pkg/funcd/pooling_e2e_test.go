//go:build e2e

package funcd_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/bus/nats"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// poolHarness is an embedded platform running real functions on the process-driver shim with
// worker pooling enabled (ADR-0046). It holds the runtime so a test can introspect how many
// pool worker processes exist. Node-gated (the pool host is pool.mjs).
type poolHarness struct {
	client    *sdk.Client
	dpURL     string
	rt        runtime.Runtime
	dir       string            // a stable artifact dir for this harness
	artifacts map[string]string // name → file:// URI, stable across re-applies (no-op reconcile)
}

func newPoolHarness(t *testing.T) *poolHarness {
	t.Helper()
	shim := langmod.NodeShim(t)
	poolShim := langmod.PoolShim(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the pooling node lane")
	}

	ctx := context.Background()
	bucket, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	rt := process.New()

	// Wire the in-memory drivers explicitly (not InMemory(), which builds its own runtime) so
	// the test holds the runtime and can List pool worker instances; plus the pool shim + a
	// low cap to exercise the cap guard.
	p, err := funcd.New(
		funcd.WithBlob(bucket),
		funcd.WithBus(messaging),
		funcd.WithStore(store.New(memory.New())),
		funcd.WithRuntime(rt),
		funcd.WithGateway(embedded.New()),
		funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"),
		funcd.WithDevAuth(funcd.DevToken, "default"),
		funcd.WithRuntimeShim(node, shim),
		funcd.WithPoolShim(node, poolShim),
		funcd.WithPoolLimit(2),
	)
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	return &poolHarness{
		client: c, dpURL: "http://" + p.DataPlaneAddr(), rt: rt,
		dir: t.TempDir(), artifacts: map[string]string{},
	}
}

// applyPooled applies a function joining worker id `worker` (empty ⇒ solo).
func (h *poolHarness) applyPooled(t *testing.T, name, worker string, scaling v1.Scaling, replicas int) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Image = h.artifactFor(t, name)
	fn.Spec.Replicas = replicas
	fn.Spec.Scaling = scaling
	fn.Spec.Pooling.Worker = worker
	_, err := h.client.Apply(context.Background(), fn)
	require.NoError(t, err)
}

func (h *poolHarness) phase(t *testing.T, name string) v1.Phase {
	t.Helper()
	got, err := h.client.Get(context.Background(), v1.KindFunction, "default", v1.ObjectName(name))
	require.NoError(t, err)
	return got.(*v1.Function).Status.Phase
}

// poolWorkers counts the running pool worker processes (instances whose synthetic name is
// prefixed "__pool__"); one per (namespace, runtime, worker-id).
func (h *poolHarness) poolWorkers(t *testing.T) int {
	t.Helper()
	insts, err := h.rt.List(context.Background(), "default")
	require.NoError(t, err)
	n := 0
	for _, in := range insts {
		if strings.HasPrefix(string(in.Name), "__pool__") && in.State == runtime.StateRunning {
			n++
		}
	}
	return n
}

func (h *poolHarness) invoke(t *testing.T, name, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(h.dpURL+"/function/"+name, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// artifactFor returns a STABLE file:// URI for name's handler (returning its own function
// name, so an invocation proves which handler in a shared pool ran). Stable across re-applies
// so a no-op reconcile sees an unchanged artifact path (and thus an unchanged pool manifest).
func (h *poolHarness) artifactFor(t *testing.T, name string) string {
	t.Helper()
	if uri, ok := h.artifacts[name]; ok {
		return uri
	}
	p := filepath.Join(h.dir, name+".mjs")
	src := "export function handle(_, e) { return { fn: \"" + name + "\", echoed: e }; }\n"
	require.NoError(t, os.WriteFile(p, []byte(src), 0o600))
	uri := "file://" + p
	h.artifacts[name] = uri
	return uri
}

// scenario: same-worker-co-locates — two functions naming the same worker id run as handlers
// in ONE pool worker process, each invocable at /function/<name> over the data plane.
func TestScenarioPoolSameWorkerCoLocates(t *testing.T) {
	h := newPoolHarness(t)
	h.applyPooled(t, "alpha", "agents", v1.Scaling{MinReplicas: 1}, 1)
	h.applyPooled(t, "beta", "agents", v1.Scaling{MinReplicas: 1}, 1)

	require.Eventually(t, func() bool {
		return h.phase(t, "alpha") == v1.PhaseReady && h.phase(t, "beta") == v1.PhaseReady
	}, 20*time.Second, 100*time.Millisecond, "both pooled functions reconcile to Ready")

	require.Equal(t, 1, h.poolWorkers(t), "two functions, same worker id ⇒ exactly ONE pool worker process")

	code, body := h.invoke(t, "alpha", `{"x":1}`)
	require.Equal(t, http.StatusOK, code, "alpha invocable: %s", body)
	require.Contains(t, body, `"fn":"alpha"`, "the alpha handler ran in the pool")
	code, body = h.invoke(t, "beta", `{"y":2}`)
	require.Equal(t, http.StatusOK, code, "beta invocable: %s", body)
	require.Contains(t, body, `"fn":"beta"`, "the beta handler ran in the same pool")
}

// scenario: pool-scales-to-zero — a pool whose members are all idle is reclaimed (no pool
// worker), and the first data-plane request wakes the whole pool and is served.
func TestScenarioPoolScalesToZero(t *testing.T) {
	h := newPoolHarness(t)
	h.applyPooled(t, "cold1", "batch", v1.Scaling{MinReplicas: 0, IdleTimeout: time.Hour}, 0)
	h.applyPooled(t, "cold2", "batch", v1.Scaling{MinReplicas: 0, IdleTimeout: time.Hour}, 0)

	require.Eventually(t, func() bool {
		p1, p2 := h.phase(t, "cold1"), h.phase(t, "cold2")
		idle := func(p v1.Phase) bool { return p == v1.PhaseIdle || p == v1.PhasePending }
		return idle(p1) && idle(p2)
	}, 15*time.Second, 100*time.Millisecond, "an all-idle pool settles scaled to zero")
	require.Equal(t, 0, h.poolWorkers(t), "an idle pool is reclaimed — no pool worker resident")

	code, body := h.invoke(t, "cold1", `{"wake":true}`)
	require.Equal(t, http.StatusOK, code, "the first request wakes the whole pool and is served: %s", body)
	require.Contains(t, body, `"fn":"cold1"`)
	require.Equal(t, 1, h.poolWorkers(t), "the wake brought up exactly one pool worker")
}

// scenario: pool-wake-keeps-siblings — member A is warm (minReplicas≥1) while B is idle
// (scale-to-zero); the pool stays up for B and a request to B is served from the running pool,
// reclaim only after the last member is idle.
func TestScenarioPoolWakeKeepsSiblings(t *testing.T) {
	h := newPoolHarness(t)
	h.applyPooled(t, "warm", "mix", v1.Scaling{MinReplicas: 1}, 1)
	h.applyPooled(t, "sleepy", "mix", v1.Scaling{MinReplicas: 0, IdleTimeout: time.Hour}, 0)

	require.Eventually(t, func() bool { return h.phase(t, "warm") == v1.PhaseReady },
		20*time.Second, 100*time.Millisecond, "the warm member keeps the pool up")
	require.Equal(t, 1, h.poolWorkers(t), "one pool worker stays up because a sibling is warm")

	// sleepy is idle but its upstream is the (running) pool worker — a request to it is served.
	code, body := h.invoke(t, "sleepy", `{"z":3}`)
	require.Equal(t, http.StatusOK, code, "an idle sibling is served from the running pool: %s", body)
	require.Contains(t, body, `"fn":"sleepy"`, "the idle sibling's handler ran in the warm pool")
}

// scenario: membership-rebuild — adding a function to a worker id grows the manifest and
// restarts the pool worker exactly once (a new process); a no-op reconcile leaves it untouched.
func TestScenarioPoolMembershipRebuild(t *testing.T) {
	h := newPoolHarness(t)
	h.applyPooled(t, "m1", "grow", v1.Scaling{MinReplicas: 1}, 1)
	require.Eventually(t, func() bool { return h.phase(t, "m1") == v1.PhaseReady },
		20*time.Second, 100*time.Millisecond)
	require.Equal(t, 1, h.poolWorkers(t))

	// capture the running pool worker's PID; a rebuild restarts the worker (a new PID) while
	// reusing its stable instance id (so a member never gets a second worker).
	firstPID := h.poolWorkerPID(t, "grow")
	require.NotZero(t, firstPID)

	// Add a second member to the SAME worker id → the manifest grows → one restart (new PID).
	h.applyPooled(t, "m2", "grow", v1.Scaling{MinReplicas: 1}, 1)
	require.Eventually(t, func() bool {
		pid := h.poolWorkerPID(t, "grow")
		return h.phase(t, "m2") == v1.PhaseReady && pid != 0 && pid != firstPID
	}, 20*time.Second, 100*time.Millisecond, "the manifest grew and the pool worker was rebuilt once")
	require.Equal(t, 1, h.poolWorkers(t), "still exactly one pool worker after the rebuild")

	// Both members now serve from the rebuilt pool.
	code, body := h.invoke(t, "m1", `{}`)
	require.Equal(t, http.StatusOK, code, "%s", body)
	require.Contains(t, body, `"fn":"m1"`)
	code, body = h.invoke(t, "m2", `{}`)
	require.Equal(t, http.StatusOK, code, "%s", body)
	require.Contains(t, body, `"fn":"m2"`)

	// A no-op reconcile (re-apply m2 unchanged) must NOT restart the pool worker (same PID).
	stablePID := h.poolWorkerPID(t, "grow")
	require.NotZero(t, stablePID)
	h.applyPooled(t, "m2", "grow", v1.Scaling{MinReplicas: 1}, 1)
	require.Never(t, func() bool {
		pid := h.poolWorkerPID(t, "grow")
		return pid != 0 && pid != stablePID
	}, 3*time.Second, 250*time.Millisecond, "an unchanged manifest does not restart the pool worker")
}

// scenario: pool-cap-guard — with the cap at 2, a third function naming one worker id is held
// NotReady with a PoolFull condition (the over-cap member, by name order), never overloading
// the process.
func TestScenarioPoolCapGuard(t *testing.T) {
	h := newPoolHarness(t) // PoolLimit = 2
	h.applyPooled(t, "p1", "capped", v1.Scaling{MinReplicas: 1}, 1)
	h.applyPooled(t, "p2", "capped", v1.Scaling{MinReplicas: 1}, 1)
	h.applyPooled(t, "p3", "capped", v1.Scaling{MinReplicas: 1}, 1)

	require.Eventually(t, func() bool {
		got, err := h.client.Get(context.Background(), v1.KindFunction, "default", "p3")
		if err != nil {
			return false
		}
		fn := got.(*v1.Function)
		_, ok := fn.Status.Conditions.Get("PoolFull")
		return ok && fn.Status.Phase != v1.PhaseReady
	}, 20*time.Second, 100*time.Millisecond, "the over-cap member is held NotReady with a PoolFull condition")

	// p3 has no data-plane route (never Ready).
	code, _ := h.invoke(t, "p3", `{}`)
	require.NotEqual(t, http.StatusOK, code, "the rejected member is not invocable")
}

// poolWorkerPID returns the running pool worker's OS PID for worker id `worker`, or 0. A pool
// rebuild restarts the worker (a new PID) while reusing its stable instance id, so the PID is
// the observable signal of a restart.
func (h *poolHarness) poolWorkerPID(t *testing.T, worker string) int {
	t.Helper()
	insts, err := h.rt.List(context.Background(), "default")
	require.NoError(t, err)
	want := "__pool__nodejs22__" + worker
	for _, in := range insts {
		if string(in.Name) == want && in.State == runtime.StateRunning {
			return in.PID
		}
	}
	return 0
}
