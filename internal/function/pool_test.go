package function_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/activator/storescaler"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/pooling"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/sensor"
	"github.com/pyvvo/funcd/internal/workflow"
)

func withNodePool(d *function.Deps) { d.PoolShimCommand = []string{"node", "/opt/funcd/pool.mjs"} }

// serveCalls gives revision rev's workers an endpoint that answers a call only where the real host serves it: at
// POST /function/<name> on a pool host (pooled, the pool worker has no revision), at POST / on a solo shim. Anything
// else is a 404, as on the shims.
func (f *fakeRuntime) serveCalls(t *testing.T, rev v1.ObjectName, pooled bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, routed := strings.CutPrefix(r.URL.Path, "/function/")
		switch {
		case r.URL.Path == "/health/members":
			f.serveMembers(w)
		case r.URL.Path == "/health/readiness" || r.URL.Path == "/health/liveness":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && pooled && routed && name != "" && !strings.Contains(name, "/"):
			_, _ = io.WriteString(w, `{"served":"`+name+`"}`)
		case r.Method == http.MethodPost && !pooled && r.URL.Path == "/":
			_, _ = io.WriteString(w, `{"served":"solo"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	f.mu.Lock()
	f.revPort[rev] = port
	f.mu.Unlock()
}

// poolOf is the pool worker name of worker id w on nodejs22 for the harness's members with no bindings, all in
// resource group rg1.
func poolOf(w string) v1.ObjectName {
	return v1.ObjectName("__pool__nodejs22__" + w + "__" + pooling.AccessHashOf(v1.FunctionSpec{}, nil, []string{"group/rg1"}))
}

// poolManifest reads the manifest file of the pool worker named pool.
func (h *shimHarness) poolManifest(t *testing.T, pool v1.ObjectName) string {
	t.Helper()
	spec, ok := h.rt.specFor(pool)
	require.True(t, ok, "the pool worker runs")
	data, err := os.ReadFile(spec.Env["FUNCD_POOL_MANIFEST"])
	require.NoError(t, err)
	return string(data)
}

// invokeDataPlane POSTs a call to name through the data plane, the activator and the reconciler's Endpoints.
func (h *shimHarness) invokeDataPlane(t *testing.T, name string) (int, string) {
	t.Helper()
	act, err := activator.New(activator.Deps{Store: h.st, Endpoints: h.r.Endpoints(), Scaler: storescaler.New(h.st)})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	dataplane.Handler(h.st, act, nil, nil, nil, nil, 0, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/function/"+name, strings.NewReader(`{"x":1}`)))
	return rec.Code, rec.Body.String()
}

// A Function that sets spec.pooling.worker on a platform with no pool host for its runtime runs solo, so its calls
// must reach its shim at /, not at the pool's /function/<name>.
func TestIssue36_SoloRunPoolingOptInIsServed(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false)
	h.create(t, "agent", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "agents" })
	h.rt.serveCalls(t, "agent-1", false)
	h.reconcile(t, "agent")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "agent").Status.Phase)

	code, body := h.invokeDataPlane(t, "agent")
	require.Equal(t, http.StatusOK, code, "the data plane reaches the solo shim: %s", body)
	require.JSONEq(t, `{"served":"solo"}`, body)
}

// A pooled Function's calls reach its pool worker at /function/<name> through the data plane (ADR-0046 Decision 5).
func TestPooledFunctionIsServedThroughDataPlane(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withNodePool)
	h.create(t, "agent", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "agents" })
	h.rt.serveCalls(t, "", true)
	h.reconcile(t, "agent")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "agent").Status.Phase)

	code, body := h.invokeDataPlane(t, "agent")
	require.Equal(t, http.StatusOK, code, "the data plane reaches the pool worker: %s", body)
	require.JSONEq(t, `{"served":"agent"}`, body)
}

// The pool worker's spec names how many members it hosts, so the runtime can size the log connections it accepts from
// the worker (one per member) instead of cutting members off past a fixed count.
func TestPoolWorkerSpecCountsItsMembers(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withNodePool)
	h.create(t, "agent", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "agents" })
	h.rt.serveCalls(t, "", true)
	h.reconcile(t, "agent")

	h.rt.mu.Lock()
	defer h.rt.mu.Unlock()
	var pools int
	for _, spec := range h.rt.specs {
		if strings.HasPrefix(string(spec.Name), "__pool__") {
			pools++
			require.Equal(t, 1, spec.Members, "pool worker %s", spec.Name)
		}
	}
	require.Equal(t, 1, pools)
}

// A pooled Function's upstream is its pool worker, which serves a member only at /function/<name>, so a Workflow step
// and a Sensor action reach it there, like the data plane.
func TestIssue37_PooledFunctionReachableFromEveryInvoker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newShimHarness(t, http.StatusOK, false, withNodePool)
	h.create(t, "svc", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "services" })
	h.rt.serveCalls(t, "", true)
	h.reconcile(t, "svc")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "svc").Status.Phase)

	d, err := workflow.NewHTTPDispatcher(workflow.DispatchDeps{Endpoints: h.r.Endpoints()})
	require.NoError(t, err)
	out, err := d.Dispatch(ctx, workflow.DispatchRequest{Namespace: "default", Run: "run-1", Step: "call", Target: "svc", Attempt: 1})
	if assert.NoError(t, err, "the workflow step reaches the pooled function") {
		assert.JSONEq(t, `{"served":"svc"}`, string(out))
	}

	inv := &sensor.HTTPInvoker{Endpoints: h.r.Endpoints()}
	assert.NoError(t, inv.Invoke(ctx, "default", "svc", eventing.CloudEvent{SpecVersion: "1.0", ID: "e1", Source: "funcd://default/eventsource/tick", Type: "tick"}),
		"the Sensor action reaches the pooled function")
}

// A pool member whose artifact cannot be materialized fails alone: its own reconcile returns the error, and its
// siblings' reconciles still supervise the pool, so a dead pool is restarted for them.
func TestIssue38_UnmaterializableMemberFailsAlone(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	h.create(t, "a", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w" })
	h.reconcile(t, "a")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a").Status.Phase)

	h.create(t, "c", func(fn *v1.Function) {
		fn.Spec.Pooling.Worker = "w"
		fn.Spec.Image = "file://" + filepath.Join(t.TempDir(), "missing.mjs")
	})
	_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "c"})
	require.Error(t, err, "the broken member's own reconcile fails")

	h.rt.exit(poolOf("w"), runtime.StateFailed, time.Hour)
	h.reconcile(t, "a")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates(poolOf("w"))[""][0], "a's reconcile restarts the dead pool")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a").Status.Phase)
}

// A pool member whose artifact's platforms cannot be listed (a registry outage) fails alone: its own reconcile
// returns the error, and a sibling's reconcile still converges the pool.
func TestIssue38_PlatformOutageMemberFailsAlone(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, withPlatforms(&fakePlatforms{}))
	h.create(t, "b-here", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w"; fn.Spec.ImageDigest = digestHere })
	h.reconcile(t, "b-here")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b-here").Status.Phase)

	h.create(t, "a-outage", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w"; fn.Spec.ImageDigest = digestOutage })
	_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "a-outage"})
	require.Error(t, err, "the member with the outage retries")

	h.rt.exit(poolOf("w"), runtime.StateFailed, time.Hour)
	h.reconcile(t, "b-here")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates(poolOf("w"))[""][0], "b-here's reconcile restarts the dead pool")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b-here").Status.Phase)
}

// A serving pool member whose update cannot be materialized, or whose new digest's platforms cannot be listed, keeps
// the revision it serves in its pool, as a solo Function's serving revision keeps its calls (ADR-0143).
func TestIssue38_ServingMemberKeepsItsRevisionOnBrokenUpdate(t *testing.T) {
	t.Parallel()
	missing := "file://" + filepath.Join(t.TempDir(), "missing.mjs")
	for _, tc := range []struct {
		worker string
		update func(*v1.Function)
	}{
		{"unmaterializable", func(fn *v1.Function) { fn.Spec.Image = missing }},
		{"outage", func(fn *v1.Function) { fn.Spec.ImageDigest = digestOutage }},
	} {
		t.Run(tc.worker, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, withPlatforms(&fakePlatforms{}))
			pool := poolOf(tc.worker)
			for _, name := range []string{"a", "c"} {
				h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = tc.worker; fn.Spec.ImageDigest = digestHere })
			}
			h.reconcile(t, "a")
			h.reconcile(t, "c")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "c").Status.Phase)
			before := h.poolManifest(t, pool)
			require.Contains(t, before, `"c"`)

			h.apply(t, "c", tc.update)
			_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "c"})
			require.Error(t, err, "c's own reconcile retries its update")
			h.rt.exit(pool, runtime.StateFailed, time.Hour)
			h.reconcile(t, "a")
			require.Equal(t, runtime.StateRunning, h.rt.revisionStates(pool)[""][0], "a's reconcile restarts the dead pool")
			require.JSONEq(t, before, h.poolManifest(t, pool), "c keeps the revision it serves in the pool")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "c").Status.Phase)
		})
	}
}

// Only a serving member keeps its revision in the pool: an idle one whose update cannot be materialized cannot be woken
// (its own reconcile fails), so a sibling's reconcile leaves it out.
func TestIssue38_IdleMemberWithBrokenUpdateLeftOut(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	for _, name := range []string{"a", "c"} {
		h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "idle" })
	}
	h.reconcile(t, "a")
	h.reconcile(t, "c")
	require.Contains(t, h.poolManifest(t, poolOf("idle")), `"c"`)

	h.setPhase(t, "c", v1.PhaseIdle)
	h.apply(t, "c", func(fn *v1.Function) { fn.Spec.Image = "file://" + filepath.Join(t.TempDir(), "missing.mjs") })
	h.reconcile(t, "a")
	require.NotContains(t, h.poolManifest(t, poolOf("idle")), `"c"`)
}

// A member left out of its pool keeps no pool replica up: once its siblings are idle, the pool is reclaimed (ADR-0046
// Decision 6).
func TestIssue38_LeftOutMemberKeepsNoPoolUp(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	h.create(t, "a", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "reclaim" })
	h.reconcile(t, "a")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a").Status.Phase)
	h.create(t, "c", func(fn *v1.Function) {
		fn.Spec.Pooling.Worker = "reclaim"
		fn.Spec.Image = "file://" + filepath.Join(t.TempDir(), "missing.mjs")
	})

	h.setPhase(t, "a", v1.PhaseIdle)
	h.reconcile(t, "a")
	require.Equal(t, runtime.StateStopped, h.rt.revisionStates(poolOf("reclaim"))[""][0], "only the left-out c wants a replica")
}

// refResolver resolves each artifact ref to its own digest (ADR-0035).
type refResolver map[string]string

func (r refResolver) Resolve(_ context.Context, uri string) (string, error) { return r[uri], nil }

// pinnedMaterializer materializes only at a digest, as the OCI materializer does (the digest is the authority).
type pinnedMaterializer struct{ path string }

func (m pinnedMaterializer) Materialize(_ context.Context, fn *v1.Function) (string, error) {
	if fn.Spec.ImageDigest == "" {
		return "", fault.Invalidf("test.Materialize", "function %s/%s has no spec.imageDigest", fn.Namespace, fn.Name)
	}
	return m.path, nil
}

// A pooled Function deployed from a tag alone runs like a solo one: each pool member is gated and materialized at its
// Revision's pinned digest, so the healthy member serves and the one built for no node platform is left out.
func TestIssue43_TagOnlyPooledFunctionDeploys(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, withPlatforms(&fakePlatforms{}), func(d *function.Deps) {
		d.Resolver = refResolver{"oci-layout://hello:v1": digestHere, "oci-layout://other:v1": digestElsewhere}
		d.Materializer = pinnedMaterializer{path: filepath.Join(t.TempDir(), "handler.mjs")}
	})
	h.create(t, "a-elsewhere", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "agents"; fn.Spec.Image = "oci-layout://other:v1" })
	h.create(t, "hello-pooled", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "agents"; fn.Spec.Image = "oci-layout://hello:v1" })
	h.reconcile(t, "a-elsewhere")
	h.reconcile(t, "hello-pooled")

	require.Equal(t, "NoMatchingPlatform", h.condition(t, "a-elsewhere", "Ready").Reason)
	require.Equal(t, v1.PhaseReady, h.getFn(t, "hello-pooled").Status.Phase)
	require.Empty(t, h.getFn(t, "hello-pooled").Spec.ImageDigest, "the spec keeps the user's tag-only input")
	pool, ok := h.rt.specFor(poolOf("agents"))
	require.True(t, ok, "the pool worker runs")
	manifest, err := os.ReadFile(pool.Env["FUNCD_POOL_MANIFEST"])
	require.NoError(t, err)
	require.NotContains(t, string(manifest), "a-elsewhere", "the member built for no node platform is left out of the pool")
}

// A pool worker is reclaimed once no Function declares its key: after its last member is deleted, and after its last
// member leaves the key by clearing spec.pooling.worker (ADR-0046 Decision 6).
func TestIssue68_PoolReclaimedWithItsLastMember(t *testing.T) {
	t.Parallel()
	t.Run("deleted", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
		h.create(t, "m1", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "pair" })
		h.create(t, "m2", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "pair" })
		h.reconcile(t, "m1")
		h.reconcile(t, "m2")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "m2").Status.Phase)

		require.NoError(t, h.st.Delete(ctx, v1.KindFunction.GVK(), "default", "m1", ""))
		h.reconcile(t, "m1")
		require.NotEmpty(t, h.rt.revisionStates(poolOf("pair")), "m2 still needs its pool")
		require.NoError(t, h.st.Delete(ctx, v1.KindFunction.GVK(), "default", "m2", ""))
		h.reconcile(t, "m2")
		require.Empty(t, h.rt.revisionStates(poolOf("pair")), "the pool is removed with its last member")
	})
	t.Run("left the key", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
		h.create(t, "lonely", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "lonely" })
		h.reconcile(t, "lonely")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "lonely").Status.Phase)

		h.apply(t, "lonely", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "" })
		h.reconcile(t, "lonely")
		require.Empty(t, h.rt.revisionStates(poolOf("lonely")), "the pool is removed when its last member runs solo")
		require.Equal(t, runtime.StateRunning, h.rt.revisionStates("lonely")["lonely-2"][0], "the former member runs solo")
	})
}

// A pooled member that declares a Secret or a ConfigMap resolves it like a solo one and fails its own gate when it
// cannot; its other access puts it in another pool, so the pool of a member with neither never loads its code.
func TestIssue69_GatedMemberNotLoadedIntoPool(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	h.create(t, "a-ok", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "gated" })
	h.create(t, "b-secret", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "gated"; fn.Spec.Secrets = []v1.ObjectName{"db"} })
	h.create(t, "c-config", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "gated"; fn.Spec.Config = []v1.ObjectName{"settings"} })
	for _, name := range []string{"b-secret", "c-config", "a-ok"} {
		h.reconcile(t, name)
	}
	require.Equal(t, "SecretResolveFailed", h.condition(t, "b-secret", "Ready").Reason, "no secret resolver is wired, as for a solo Function")
	require.Equal(t, "ConfigResolveFailed", h.condition(t, "c-config", "Ready").Reason, "the ConfigMap is missing, as for a solo Function")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a-ok").Status.Phase)
	a, b, c := h.getFn(t, "a-ok").Status.Pool, h.getFn(t, "b-secret").Status.Pool, h.getFn(t, "c-config").Status.Pool
	require.NotEqual(t, a, b, "secrets are access: another pool")
	require.NotEqual(t, a, c, "config is access: another pool")

	manifest := h.poolManifest(t, poolOf("gated"))
	require.Contains(t, manifest, `"a-ok"`)
	require.NotContains(t, manifest, "b-secret")
	require.NotContains(t, manifest, "c-config")
}

// A member held PoolFull comes back on the supervision period, so it is admitted, Ready and routed once a slot frees
// (ADR-0046 Decision 3): no write to its own object is needed.
func TestIssue71_PoolFullMemberReadmittedWhenSlotFrees(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) { d.PoolLimit = 2 })
	for _, name := range []string{"p1", "p2", "p3"} {
		h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "full" })
	}
	h.reconcile(t, "p1")
	h.reconcile(t, "p2")
	res := h.reconcile(t, "p3")
	require.Equal(t, "PoolFull", h.condition(t, "p3", "Ready").Reason)
	require.Equal(t, testPeriod, res.RequeueAfter, "the rejected member comes back on the supervision period")
	rv := h.getFn(t, "p3").ResourceVersion
	h.reconcile(t, "p3")
	require.Equal(t, rv, h.getFn(t, "p3").ResourceVersion, "a pass that finds the pool still full writes nothing")

	require.NoError(t, h.st.Delete(ctx, v1.KindFunction.GVK(), "default", "p1", ""))
	h.reconcile(t, "p1")
	h.reconcile(t, "p2")
	h.reconcile(t, "p3")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "p3").Status.Phase, "the freed slot admits p3")
	require.Equal(t, "Admitted", h.condition(t, "p3", "PoolFull").Reason)
	_, ready := h.upstream(t, "p3")
	require.True(t, ready, "p3 is reachable")
}

// A sibling restarting after a fault reads restarting on /health/members, so it is not ready while the pool worker and
// the healthy members keep serving (ADR-0044 Decision 4).
func TestIssue72_SiblingThreadFaultKeepsMembersReady(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	h.create(t, "crasher", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "faults" })
	h.create(t, "good", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "faults" })
	h.reconcile(t, "crasher")
	h.reconcile(t, "good")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "good").Status.Phase)
	creates, _ := h.rt.counts()

	h.rt.setMember("crasher", "restarting", "")
	h.reconcile(t, "good")
	h.reconcile(t, "crasher")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "good").Status.Phase, "a sibling's thread fault is not good's")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "crasher").Status.Phase, "the restarting member is not ready")
	_, ready := h.upstream(t, "good")
	require.True(t, ready, "good stays reachable")
	require.NotEmpty(t, h.routes(t), "good keeps its route")
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "a member's state never restarts the pool worker")
}

// A member whose first load fails in its pool host is a shape failure, retried after the supervision period, and is
// Ready once a later pool start loads it; one whose load timed out is a boot crash loop, never Failed.
func TestIssue355_HungPoolWorkerFailsAfterBootTimeout(t *testing.T) {
	t.Parallel()
	t.Run("load error", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withPeriod, withNodePool)
		h.create(t, "m", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "hang" })
		h.rt.setMember("m", "failed", "SyntaxError: unexpected token")
		res := h.reconcile(t, "m")
		require.Equal(t, v1.PhaseFailed, h.getFn(t, "m").Status.Phase)
		require.Equal(t, "ShapeInvalid", h.condition(t, "m", "Ready").Reason)
		sv := h.condition(t, "m", "ShapeValid")
		require.Equal(t, v1.ConditionFalse, sv.Status)
		require.Contains(t, sv.Message, "SyntaxError")
		require.Equal(t, "ShapeInvalid", h.condition(t, "m", "RevisionReady").Reason)
		require.Equal(t, testPeriod, res.RequeueAfter, "a pooled shape failure comes back after the supervision period")

		h.rt.setMember("m", "ready", "")
		h.reconcile(t, "m")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "m").Status.Phase, "a later load makes it Ready with no spec change")
	})
	t.Run("load timed out", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withPeriod, withNodePool)
		h.create(t, "m", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "hang" })
		h.rt.setMember("m", "failed", "load timed out")
		h.reconcile(t, "m")
		ready := h.condition(t, "m", "Ready")
		require.Equal(t, "CrashLoopBackOff", ready.Reason)
		require.Contains(t, ready.Message, "did not load within")
		require.NotEqual(t, v1.PhaseFailed, h.getFn(t, "m").Status.Phase)
	})
}

// A member whose load timed out is read again at its backoff deadline, a wait after the pool start that was counted,
// not every readiness poll; once that deadline has passed, a wait after the pass. A pass over the same pool start
// counts no further crash, while serving included.
func TestPooledLoadTimeoutRereadsAtBackoffDeadline(t *testing.T) {
	t.Parallel()
	const initial = time.Minute
	h := newShimHarness(t, http.StatusOK, false, withNodePool, func(d *function.Deps) {
		d.BootBackoffInitial, d.BootBackoffMax = initial, 10*initial
	})
	h.create(t, "m", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "hang" })
	h.rt.setMember("m", "failed", "load timed out")

	res := h.reconcile(t, "m")
	require.Equal(t, "CrashLoopBackOff", h.condition(t, "m", "Ready").Reason)
	require.Greater(t, res.RequeueAfter, initial-10*time.Second, "the deadline of a fresh pool start")
	require.LessOrEqual(t, res.RequeueAfter, initial)

	h.rt.exitRevision(poolOf("hang"), "", 0, runtime.StateRunning, time.Hour)
	for range 2 {
		res = h.reconcile(t, "m")
		ready := h.condition(t, "m", "Ready")
		require.Contains(t, ready.Message, "boot crash 2 in a row", "one crash per pool start")
		require.Greater(t, res.RequeueAfter, 2*initial-10*time.Second, "a passed deadline: a wait after the pass")
		require.LessOrEqual(t, res.RequeueAfter, 2*initial)
	}

	h.setPhase(t, "m", v1.PhaseReady)
	res = h.reconcile(t, "m")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "m").Status.Phase)
	require.Equal(t, "CrashLoopBackOff", h.condition(t, "m", "Ready").Reason)
	require.Greater(t, res.RequeueAfter, 2*initial-10*time.Second, "serving, the member is read at the deadline too")
}

// Issue #422: a running pool worker silent on /health/liveness for the boot timeout since its last answer, else since
// it was created, is created again; one that answered within that time is kept.
func TestIssue422_NeverReadyPoolWorkerIsReplaced(t *testing.T) {
	t.Parallel()
	t.Run("never answered", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withPeriod, withNodePool)
		pool := poolOf("stall")
		h.rt.hold(runtime.NewInstanceID("default", pool, "", 0), true)
		h.create(t, "stall", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "stall" })
		h.reconcile(t, "stall")
		require.Equal(t, v1.PhaseDeploying, h.getFn(t, "stall").Status.Phase)

		h.rt.exitRevision(pool, "", 0, runtime.StateRunning, time.Hour)
		before := h.rt.poolWorkers("default", pool)
		require.Len(t, before, 1)
		creates, _ := h.rt.counts()
		h.reconcile(t, "stall")
		after, _ := h.rt.counts()
		require.Equal(t, creates+1, after, "the silent pool worker is created again")
		require.Equal(t, before, h.rt.poolWorkers("default", pool), "in place: no second pool worker beside it")
	})
	t.Run("answered recently", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withPeriod, withNodePool)
		pool := poolOf("stall")
		h.create(t, "stall", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "stall" })
		h.reconcile(t, "stall")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "stall").Status.Phase)

		h.rt.hold(runtime.NewInstanceID("default", pool, "", 0), true)
		h.rt.exitRevision(pool, "", 0, runtime.StateRunning, time.Hour)
		creates, _ := h.rt.counts()
		h.reconcile(t, "stall")
		after, _ := h.rt.counts()
		require.Equal(t, creates, after, "a pool worker that answered within the boot timeout is kept")
		require.Equal(t, v1.PhaseDegraded, h.getFn(t, "stall").Status.Phase)
	})
}

// Issue #70: a pool host that exits at boot (a member's handler cannot load) is created again on ADR-0142's backoff, as
// a solo replica is — once it is one supervision period old — not on every 200 ms readiness poll. The member keeps the
// phase it had while the pool host was restarted on every pass.
func TestIssue70_FailedPoolHostRespawnsOncePerPeriod(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		worker string
		held   bool               // the pool host never serves, so the member has not served yet
		phase  v1.Phase           // the member's phase while its pool host waits out the backoff
		shape  v1.ConditionStatus // ShapeValid: Unknown until the generation has served (ADR-0174)
	}{
		{"issue70-booting", true, v1.PhaseDeploying, v1.ConditionUnknown},
		{"issue70-serving", false, v1.PhaseDegraded, v1.ConditionTrue},
	} {
		t.Run(tc.worker, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withNodePool)
			pool := poolOf(tc.worker)
			h.rt.hold(runtime.NewInstanceID("default", pool, "", 0), tc.held)
			h.create(t, "m", func(fn *v1.Function) { fn.Spec.Pooling.Worker = tc.worker })
			h.reconcile(t, "m")

			h.rt.exitRevision(pool, "", 0, runtime.StateFailed, 0)
			creates, _ := h.rt.counts()
			res := h.reconcile(t, "m")
			after, _ := h.rt.counts()
			require.Equal(t, creates, after, "a pool host younger than one period is not created again")
			require.Equal(t, tc.phase, h.getFn(t, "m").Status.Phase)
			require.Equal(t, tc.shape, h.shapeValid(t, "m"))
			require.Greater(t, res.RequeueAfter, time.Second, "the pass comes back when the backoff ends, not at the readiness poll")
			require.LessOrEqual(t, res.RequeueAfter, controller.SupervisionPeriod)

			h.rt.exitRevision(pool, "", 0, runtime.StateFailed, controller.SupervisionPeriod)
			h.reconcile(t, "m")
			after, _ = h.rt.counts()
			require.Equal(t, creates+1, after, "created again once the backoff has passed")
		})
	}
}

// Issue #70: a serving member redeployed to a handler that cannot load is a shape failure of its new revision, as on a
// first deploy (ADR-0143 Decision 5, ADR-0158 Decision 4): its pool host holds only that revision, so it is Failed with
// ShapeValid and RevisionReady False/ShapeInvalid, retried after the period, and its sibling keeps serving.
func TestIssue70_RedeployToUnloadableHandlerIsShapeInvalid(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod, withNodePool)
	for _, name := range []string{"a", "b"} {
		h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "redeploy" })
	}
	h.reconcile(t, "a")
	h.reconcile(t, "b")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)

	update := filepath.Join(t.TempDir(), "update.mjs")
	require.NoError(t, os.WriteFile(update, []byte("export function handle() {}\n"), 0o600))
	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = "file://" + update })
	h.rt.setMember("b", "failed", "TypeError: handle is not exported")
	res := h.reconcile(t, "b")

	b := h.getFn(t, "b")
	require.Equal(t, "b-2", b.Status.CurrentRevision)
	require.Equal(t, v1.PhaseFailed, b.Status.Phase, "the pool holds only b's new revision")
	require.Equal(t, "ShapeInvalid", h.condition(t, "b", "Ready").Reason)
	sv := h.condition(t, "b", "ShapeValid")
	require.Equal(t, v1.ConditionFalse, sv.Status)
	require.Equal(t, "ShapeInvalid", sv.Reason)
	require.Contains(t, sv.Message, "is not exported")
	rr := h.condition(t, "b", "RevisionReady")
	require.Equal(t, v1.ConditionFalse, rr.Status)
	require.Equal(t, "ShapeInvalid", rr.Reason)
	require.Equal(t, b.Generation, rr.ObservedGeneration)
	require.Equal(t, testPeriod, res.RequeueAfter, "a pooled shape failure comes back after the supervision period")

	h.reconcile(t, "a")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a").Status.Phase, "the sibling keeps serving")
}

// Issue #70: only a member's load failure is judged by its revision; a serving member redeployed to a handler that loads
// is Degraded while the new revision loads, as before (the blueprint's Ready → Degraded → Ready, ADR-0158 Decision 4).
func TestIssue70_PooledRedeployIsDegradedWhileLoading(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod, withNodePool)
	h.create(t, "b", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "redeploy" })
	h.reconcile(t, "b")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)

	update := filepath.Join(t.TempDir(), "update.mjs")
	require.NoError(t, os.WriteFile(update, []byte("export function handle() { return {} }\n"), 0o600))
	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = "file://" + update })
	h.rt.setMember("b", "loading", "")
	h.reconcile(t, "b")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "b").Status.Phase)
	h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "Restarting")

	h.rt.setMember("b", "ready", "")
	h.reconcile(t, "b")
	b := h.getFn(t, "b")
	require.Equal(t, v1.PhaseReady, b.Status.Phase)
	require.Equal(t, "b-2", b.Status.ServingRevision)
}

// scenario: pooled-failed-member-never-idle (ADR-0169) — a woken scale-to-zero member whose handler cannot load in its
// pool host is never Idle over two periods of passes and an idle reclaim past idleTimeout, and a call while it is
// Failed is refused at once. A woken member whose pool host exits at once waits out the pool worker's backoff, never
// Idle either.
func TestScenarioPooledFailedMemberNeverIdle(t *testing.T) {
	t.Parallel()
	const idle = time.Minute
	woken := func(t *testing.T, runtimeFailed bool) *shimHarness {
		h := newShimHarness(t, http.StatusOK, runtimeFailed, withNodePool, withPeriod)
		h.create(t, "m", func(fn *v1.Function) {
			fn.Spec.Pooling.Worker = "adr0169"
			fn.Spec.Replicas = 0
			fn.Spec.Scaling.IdleTimeout = idle
		})
		h.reconcile(t, "m")
		require.Equal(t, v1.PhaseIdle, h.getFn(t, "m").Status.Phase)
		h.setPhase(t, "m", v1.PhaseDeploying)
		return h
	}
	t.Run("handler cannot load", func(t *testing.T) {
		t.Parallel()
		h := woken(t, false)
		h.rt.setMember("m", "failed", "SyntaxError: unexpected token")
		h.reconcile(t, "m")
		require.Equal(t, v1.PhaseFailed, h.getFn(t, "m").Status.Phase, "the handler cannot load")

		for i := range 2 {
			res := h.reconcile(t, "m")
			require.Equal(t, v1.PhaseFailed, h.getFn(t, "m").Status.Phase, "period %d", i)
			require.Positive(t, res.RequeueAfter, "the member is read again (period %d)", i)
			require.LessOrEqual(t, res.RequeueAfter, testPeriod)
			time.Sleep(testPeriod)
		}
		clk := clock.NewManual(time.Now())
		a := h.activator(t, clk)
		reclaimPastIdle(t, a, clk, idle)
		h.reconcile(t, "m")
		require.Equal(t, v1.PhaseFailed, h.getFn(t, "m").Status.Phase, "never Idle")
		require.Equal(t, v1.ConditionFalse, h.shapeValid(t, "m"))
		refusedAtOnce(t, a, "m", "ShapeInvalid")
	})
	t.Run("pool host exits at once", func(t *testing.T) {
		t.Parallel()
		h := woken(t, true)
		for i := range 2 {
			res := h.reconcile(t, "m")
			require.Equal(t, v1.PhaseDeploying, h.getFn(t, "m").Status.Phase, "pass %d", i)
			require.Positive(t, res.RequeueAfter, "the pool worker is tried again (pass %d)", i)
			require.LessOrEqual(t, res.RequeueAfter, testPeriod)
		}
		creates, _ := h.rt.counts()
		time.Sleep(testPeriod)
		h.reconcile(t, "m")
		after, _ := h.rt.counts()
		require.Equal(t, creates+1, after, "the pool worker is started again once a period has passed")
		require.Equal(t, v1.PhaseDeploying, h.getFn(t, "m").Status.Phase)

		clk := clock.NewManual(time.Now())
		reclaimPastIdle(t, h.activator(t, clk), clk, idle)
		h.reconcile(t, "m")
		require.NotEqual(t, v1.PhaseIdle, h.getFn(t, "m").Status.Phase, "never Idle")
	})
}

// Issue #359: a pool worker that cannot start (its host interpreter is missing) ends each member Failed with a reason
// naming the start error, as a solo worker does (#73), and a later pass creates the pool worker again, writing
// nothing while it still fails.
func TestIssue359_PoolStartFailureWritesFailedStatus(t *testing.T) {
	t.Parallel()
	rt := &createCounter{Runtime: process.New()}
	t.Cleanup(func() { _ = rt.Close() })
	h := newShimHarness(t, http.StatusOK, false, withPeriod, func(d *function.Deps) {
		d.Runtime = rt
		d.PoolShimCommand = []string{"/nonexistent/bin/node", "/opt/funcd/pool.mjs"}
	})
	members := []string{"m1", "m2"}
	for _, name := range members {
		h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "issue359" })
	}
	for _, name := range members {
		res := h.reconcile(t, name)
		fn := h.getFn(t, name)
		require.Equal(t, v1.PhaseFailed, fn.Status.Phase, name)
		require.Equal(t, fn.Generation, fn.Status.ObservedGeneration, name)
		ready := h.condition(t, name, "Ready")
		require.Equal(t, v1.ConditionFalse, ready.Status, name)
		require.Equal(t, "StartFailed", ready.Reason, name)
		require.Contains(t, ready.Message, "/nonexistent/bin/node", name)
		require.Equal(t, v1.ConditionUnknown, h.shapeValid(t, name), "a pool worker that cannot start is not a shape failure, and nothing loaded the generation (ADR-0174)")
		require.Equal(t, testPeriod, res.RequeueAfter, "a start failure is retried once per period")
	}

	rv := h.getFn(t, "m1").ResourceVersion
	res := h.reconcile(t, "m1")
	require.EqualValues(t, 3, rt.creates.Load(), "a pool worker that is not running is created again on each pass (issue #355)")
	require.Equal(t, rv, h.getFn(t, "m1").ResourceVersion, "a repeated start failure writes nothing")
	require.Equal(t, testPeriod, res.RequeueAfter)
}

// Two namespaces that pool one runtime under the same worker id each get their own pool worker, and each pool host
// loads only its own namespace's members (ADR-0046 Decision 7, ADR-0044 Decision 6): after both come up, and after a
// dead pool worker is created again by the supervision pass (ADR-0142).
func TestPoolManifestIsScopedToItsNamespace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newShimHarness(t, http.StatusOK, false, withNodePool)
	const worker = "tenancy-shared"
	pool := poolOf(worker)
	members := map[v1.NamespaceName]string{"team-a": "a1", "team-b": "b1"}
	reconcile := func(ns v1.NamespaceName) {
		_, err := h.r.Reconcile(ctx, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: ns, Name: v1.ObjectName(members[ns])})
		require.NoError(t, err)
	}
	manifestOf := func(ns v1.NamespaceName) (path, data string) {
		ids := h.rt.poolWorkers(ns, pool)
		require.NotEmpty(t, ids, "%s's pool worker exists", ns)
		spec := h.rt.specOf(ids[len(ids)-1])
		path = spec.Env["FUNCD_POOL_MANIFEST"]
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		return path, string(raw)
	}
	requireOwnMembers := func(stage string) {
		t.Helper()
		pathA, a := manifestOf("team-a")
		pathB, b := manifestOf("team-b")
		require.NotContains(t, a, `"b1"`, "%s: team-a's pool loads team-b's code", stage)
		require.NotContains(t, b, `"a1"`, "%s: team-b's pool loads team-a's code", stage)
		require.Contains(t, a, `"a1"`, "%s: team-a's pool loads its member", stage)
		require.Contains(t, b, `"b1"`, "%s: team-b's pool loads its member", stage)
		require.NotEqual(t, pathA, pathB, "%s: the namespaces share one pool manifest file", stage)
	}

	for ns, name := range members {
		h.create(t, name, func(fn *v1.Function) { fn.Namespace = ns; fn.Spec.Pooling.Worker = worker })
	}
	reconcile("team-a")
	reconcile("team-b")
	requireOwnMembers("after both pools came up")

	ids := h.rt.poolWorkers("team-a", pool)
	require.Len(t, ids, 1)
	crashed := ids[0]
	h.rt.mu.Lock()
	h.rt.state[crashed] = runtime.StateFailed
	h.rt.created[crashed] = time.Now().Add(-controller.SupervisionPeriod)
	h.rt.started[crashed] = h.rt.created[crashed]
	h.rt.mu.Unlock()
	creates, _ := h.rt.counts()
	reconcile("team-a")
	after, _ := h.rt.counts()
	require.Equal(t, creates+1, after, "the supervision pass creates team-a's dead pool worker again")
	requireOwnMembers("after team-a's pool worker was restarted")
}

// scenario: pooled-member-never-booted-is-unknown (ADR-0174) — a pooled member whose pool wants 0, or that the pool
// gate holds without loading it, reports both conditions Unknown/NotStarted until the pool worker serves it.
func TestScenarioPooledMemberNeverBootedIsUnknown(t *testing.T) {
	t.Parallel()
	t.Run("pool-wants-zero", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withNodePool)
		h.create(t, "m", func(fn *v1.Function) { fn.Spec.Replicas = 0; fn.Spec.Pooling.Worker = "zero" })
		h.reconcile(t, "m")
		require.Equal(t, v1.PhaseIdle, h.getFn(t, "m").Status.Phase)
		creates, _ := h.rt.counts()
		require.Zero(t, creates, "no pool worker is created")
		h.requireRevisionStatus(t, "m", v1.ConditionUnknown, "NotStarted")

		h.setPhase(t, "m", v1.PhaseDeploying)
		h.reconcile(t, "m")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "m").Status.Phase)
		h.requireRevisionStatus(t, "m", v1.ConditionTrue, "")
	})
	t.Run("pool-gate", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) { d.PoolLimit = 1 })
		for _, name := range []string{"p1", "p2"} {
			h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "gate" })
		}
		h.reconcile(t, "p1")
		h.reconcile(t, "p2")
		require.Equal(t, "PoolFull", h.condition(t, "p2", "Ready").Reason)
		sv := h.condition(t, "p2", "ShapeValid")
		require.Equal(t, v1.ConditionUnknown, sv.Status)
		require.Equal(t, "NotStarted", sv.Reason)
		require.Equal(t, h.getFn(t, "p2").Generation, sv.ObservedGeneration)
		require.Equal(t, "PoolFull", h.condition(t, "p2", "RevisionReady").Reason, "the gate's failure write is unchanged")

		require.NoError(t, h.st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "p1", ""))
		h.reconcile(t, "p1")
		h.reconcile(t, "p2")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "p2").Status.Phase, "the freed slot admits p2")
		h.requireRevisionStatus(t, "p2", v1.ConditionTrue, "")
	})
}

// scenario: pooled-missing-revision-left-out (ADR-0172).
func TestScenarioPooledMissingRevisionLeftOut(t *testing.T) {
	t.Parallel()
	res, rec := &fakeResolver{digest: "sha256:A"}, &pinRecorder{}
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, pinning(res, rec))
	for _, name := range []string{"a", "b"} {
		h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w" })
	}
	h.reconcile(t, "a")
	h.reconcile(t, "b")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a").Status.Phase)
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)
	require.Contains(t, h.poolManifest(t, poolOf("w")), `"a"`)

	require.NoError(t, h.st.Delete(context.Background(), v1.KindRevision.GVK(), "default", "a-1", ""))
	res.moveTo("sha256:B")
	h.rt.exit(poolOf("w"), runtime.StateFailed, time.Hour)
	h.reconcile(t, "b")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates(poolOf("w"))[""][0], "b's reconcile restarts the pool")
	require.NotContains(t, h.poolManifest(t, poolOf("w")), `"a"`, "a is left out of the pool")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)

	h.reconcile(t, "a")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "a").Status.Phase)
	require.Equal(t, "RevisionMissing", h.condition(t, "a", "Ready").Reason)
	require.NotContains(t, rec.digests(), "sha256:B", "nothing runs at B")
	require.Equal(t, 2, res.count(), "nothing is resolved after the deploy")
}

// A reclaimed pool worker's liveness goes with its other state, so a pool key that stops existing (its last member
// deleted, or moved to another worker id) leaves no entry behind.
func TestIssue724_ReclaimedPoolForgetsLiveness(t *testing.T) {
	t.Parallel()
	t.Run("member deleted", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
		for i := range 3 {
			name, worker := "m"+strconv.Itoa(i), "w"+strconv.Itoa(i)
			h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = worker })
			h.reconcile(t, name)
			h.reconcile(t, name)
			require.Equal(t, v1.PhaseReady, h.getFn(t, name).Status.Phase)
			require.Equal(t, 1, function.PoolLiveLen(h.r), "the pool answered its probe")

			require.NoError(t, h.st.Delete(context.Background(), v1.KindFunction.GVK(), "default", v1.ObjectName(name), ""))
			h.reconcile(t, name)
			require.Empty(t, h.rt.revisionStates(poolOf(worker)), "the pool is reclaimed")
			require.Zero(t, function.PoolLiveLen(h.r), "the reclaimed pool's liveness is forgotten")
		}
	})
	t.Run("member moved", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
		h.create(t, "m", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w0" })
		h.reconcile(t, "m")
		h.reconcile(t, "m")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "m").Status.Phase)

		h.apply(t, "m", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w1" })
		h.reconcile(t, "m")
		h.reconcile(t, "m")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "m").Status.Phase)
		require.Empty(t, h.rt.revisionStates(poolOf("w0")), "the old pool is reclaimed")
		require.Equal(t, 1, function.PoolLiveLen(h.r), "only the new pool's liveness is kept")
	})
}
