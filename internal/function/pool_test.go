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
	"github.com/pyvvo/funcd/internal/runtime"
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
		case r.URL.Path == "/health/readiness":
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

// invokeDataPlane POSTs a call to name through the data plane, the activator and the reconciler's Endpoints.
func (h *shimHarness) invokeDataPlane(t *testing.T, name string) (int, string) {
	t.Helper()
	act, err := activator.New(activator.Deps{Store: h.st, Endpoints: h.r.Endpoints(), Scaler: storescaler.New(h.st)})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	dataplane.Handler(h.st, act, nil, nil, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/function/"+name, strings.NewReader(`{"x":1}`)))
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

	h.rt.exit("__pool__nodejs22__w", runtime.StateFailed, time.Hour)
	h.reconcile(t, "a")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("__pool__nodejs22__w")[""][0], "a's reconcile restarts the dead pool")
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

	h.rt.exit("__pool__nodejs22__w", runtime.StateFailed, time.Hour)
	h.reconcile(t, "b-here")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("__pool__nodejs22__w")[""][0], "b-here's reconcile restarts the dead pool")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b-here").Status.Phase)
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
	pool, ok := h.rt.specFor("__pool__nodejs22__agents")
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
		require.NotEmpty(t, h.rt.revisionStates("__pool__nodejs22__pair"), "m2 still needs its pool")
		require.NoError(t, h.st.Delete(ctx, v1.KindFunction.GVK(), "default", "m2", ""))
		h.reconcile(t, "m2")
		require.Empty(t, h.rt.revisionStates("__pool__nodejs22__pair"), "the pool is removed with its last member")
	})
	t.Run("left the key", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
		h.create(t, "lonely", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "lonely" })
		h.reconcile(t, "lonely")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "lonely").Status.Phase)

		h.apply(t, "lonely", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "" })
		h.reconcile(t, "lonely")
		require.Empty(t, h.rt.revisionStates("__pool__nodejs22__lonely"), "the pool is removed when its last member runs solo")
		require.Equal(t, runtime.StateRunning, h.rt.revisionStates("lonely")["lonely-2"][0], "the former member runs solo")
	})
}

// A pooled member that declares a Secret or a ConfigMap fails closed (ADR-0057, ADR-0093 Decision 3), so no pool worker
// loads its code: a sibling's reconcile leaves it out of the pool manifest.
func TestIssue69_GatedMemberNotLoadedIntoPool(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	h.create(t, "a-ok", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "gated" })
	h.create(t, "b-secret", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "gated"; fn.Spec.Secrets = []v1.ObjectName{"db"} })
	h.create(t, "c-config", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "gated"; fn.Spec.Config = []v1.ObjectName{"settings"} })
	for _, name := range []string{"b-secret", "c-config", "a-ok"} {
		h.reconcile(t, name)
	}
	require.Equal(t, "SecretResolveFailed", h.condition(t, "b-secret", "Ready").Reason)
	require.Equal(t, "SecretResolveFailed", h.condition(t, "c-config", "Ready").Reason)
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a-ok").Status.Phase)

	pool, ok := h.rt.specFor("__pool__nodejs22__gated")
	require.True(t, ok, "the pool worker runs")
	manifest, err := os.ReadFile(pool.Env["FUNCD_POOL_MANIFEST"])
	require.NoError(t, err)
	require.Contains(t, string(manifest), `"a-ok"`)
	require.NotContains(t, string(manifest), "b-secret", "the member gated on its Secret gets no worker")
	require.NotContains(t, string(manifest), "c-config", "the member gated on its ConfigMap gets no worker")
}
