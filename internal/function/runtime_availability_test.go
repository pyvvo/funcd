package function_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0149 tests: a runtime this node cannot serve is Failed with RuntimeUnavailable.

const ruby3Image = "funcd/runtime-ruby3:latest"

// notEmbedded is the containerd driver's Create error for a default-prefix runtime with no embedded image.
func notEmbedded(ref string) error {
	return fault.Wrapf(runtime.ErrImageUnavailable, fault.NotFound, "runtime.containerd.Create",
		"runtime image %q is not embedded; set runtime.containerd.imageOverride for its runtime to pull it", ref)
}

const ruby3NotEmbedded = `runtime "ruby3" is not available on this node: runtime.containerd.Create: runtime image ` +
	`"funcd/runtime-ruby3:latest" is not embedded; set runtime.containerd.imageOverride for its runtime to pull it: ` +
	`image is not available`

func (f *fakeRuntime) failImage(image string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.imageErr, image)
		return
	}
	f.imageErr[image] = err
}

func (f *fakeRuntime) createAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func (f *fakeRuntime) allSpecs() []runtime.WorkerSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]runtime.WorkerSpec, 0, len(f.specs))
	for _, spec := range f.specs {
		out = append(out, spec)
	}
	return out
}

// requireUnavailable asserts name is Failed with RuntimeUnavailable and message msg, with no replica.
func (h *shimHarness) requireUnavailable(t *testing.T, name, msg string) {
	t.Helper()
	fn := h.getFn(t, name)
	require.Equal(t, v1.PhaseFailed, fn.Status.Phase)
	require.Zero(t, fn.Status.Replicas)
	ready := h.condition(t, name, "Ready")
	require.Equal(t, v1.ConditionFalse, ready.Status)
	require.Equal(t, "RuntimeUnavailable", ready.Reason)
	require.Equal(t, msg, ready.Message)
}

// scenario: unknown-runtime-process-solo — a process-mode daemon runs no shim for ruby3, so the Function fails at the
// gate and no worker is created under the node shim.
func TestADR0149_UnknownRuntimeProcessSolo(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false)
	h.create(t, "rb", func(fn *v1.Function) { fn.Spec.Runtime = "ruby3" })
	res := h.reconcile(t, "rb")

	h.requireUnavailable(t, "rb", `runtime "ruby3" is not available on this node: no shim is registered for it`)
	require.Zero(t, h.rt.createAttempts(), "no worker is created")
	require.Zero(t, res.RequeueAfter, "the process-mode shims are fixed at daemon start, so the gate sets no requeue")
}

// scenario: unknown-runtime-process-pool — with the node pool host configured, a pooled ruby3 Function gets no pool
// worker either.
func TestADR0149_UnknownRuntimeProcessPool(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withNodePool)
	h.create(t, "rb", func(fn *v1.Function) {
		fn.Spec.Runtime = "ruby3"
		fn.Spec.Pooling.Worker = "w1"
	})
	h.reconcile(t, "rb")

	h.requireUnavailable(t, "rb", `runtime "ruby3" is not available on this node: no shim is registered for it`)
	require.Zero(t, h.rt.createAttempts(), "no pool worker is created")
}

// scenario: engine-image-not-a-runtime — in containerd mode duckdb is the CatalogService engine image, so a Function
// naming it fails at the gate, before any Create.
func TestADR0149_EngineImageNotARuntime(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK, withPeriod)
	h.create(t, "db", func(fn *v1.Function) { fn.Spec.Runtime = "duckdb" })
	res := h.reconcile(t, "db")

	h.requireUnavailable(t, "db", `runtime "duckdb" is not available on this node: it is the CatalogService engine image, not a function runtime`)
	require.Equal(t, "RuntimeUnavailable", h.condition(t, "db", "RevisionReady").Reason)
	require.Zero(t, h.rt.createAttempts(), "no worker is created")
	require.Zero(t, res.RequeueAfter, "the duckdb rule is fixed at daemon start")
}

// scenario: missing-image-containerd — the Create of an absent image fails the Function with RuntimeUnavailable, a
// message naming the image and the cause, and a requeue after the supervision period.
func TestADR0149_MissingImageContainerd(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK, withPeriod)
	h.rt.failImage(ruby3Image, notEmbedded(ruby3Image))
	h.create(t, "rb", func(fn *v1.Function) { fn.Spec.Runtime = "ruby3" })
	res := h.reconcile(t, "rb")

	h.requireUnavailable(t, "rb", ruby3NotEmbedded)
	require.Equal(t, ruby3NotEmbedded, h.condition(t, "rb", "RevisionReady").Message)
	require.Equal(t, 1, h.rt.createAttempts())
	require.Empty(t, h.rt.revisionStates("rb"), "no worker exists")
	require.Equal(t, testPeriod, res.RequeueAfter, "the image is checked again after the supervision period")
}

// scenario: missing-image-scale-to-zero — a scale-to-zero ruby3 Function goes Idle and creates nothing; a wake learns
// that its image is absent, and each later periodic pass tries one Create and keeps RuntimeUnavailable.
func TestADR0149_MissingImageScaleToZero(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK, withPeriod)
	h.rt.failImage(ruby3Image, notEmbedded(ruby3Image))
	h.create(t, "rb", func(fn *v1.Function) {
		fn.Spec.Runtime = "ruby3"
		fn.Spec.Replicas = 0
	})
	h.reconcile(t, "rb")
	require.Equal(t, v1.PhaseIdle, h.getFn(t, "rb").Status.Phase)
	require.Zero(t, h.rt.createAttempts(), "an idle Function creates nothing")

	h.setPhase(t, "rb", v1.PhaseDeploying) // the activator's wake
	h.reconcile(t, "rb")
	h.requireUnavailable(t, "rb", ruby3NotEmbedded)
	require.Equal(t, 1, h.rt.createAttempts())

	for pass := 2; pass <= 3; pass++ {
		res := h.reconcile(t, "rb")
		h.requireUnavailable(t, "rb", ruby3NotEmbedded)
		require.Equal(t, pass, h.rt.createAttempts(), "each periodic pass tries one Create")
		require.Equal(t, testPeriod, res.RequeueAfter)
	}
}

// scenario: published-image-recovers — once the image is available, the next periodic pass deploys the Function,
// with no re-apply.
func TestADR0149_PublishedImageRecovers(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK, withPeriod)
	h.rt.failImage(ruby3Image, notEmbedded(ruby3Image))
	h.create(t, "rb", func(fn *v1.Function) {
		fn.Spec.Runtime = "ruby3"
		fn.Spec.Replicas = 0
	})
	h.reconcile(t, "rb")
	h.setPhase(t, "rb", v1.PhaseDeploying)
	h.reconcile(t, "rb")
	h.requireUnavailable(t, "rb", ruby3NotEmbedded)
	generation := h.getFn(t, "rb").Generation

	h.rt.failImage(ruby3Image, nil)
	res := h.reconcile(t, "rb")

	fn := h.getFn(t, "rb")
	require.Equal(t, generation, fn.Generation, "no re-apply")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, 1, fn.Status.Replicas)
	require.Equal(t, v1.ConditionTrue, h.condition(t, "rb", "RevisionReady").Status)
	spec, ok := h.rt.specFor("rb")
	require.True(t, ok)
	require.Equal(t, ruby3Image, spec.Image)
	require.Equal(t, testPeriod, res.RequeueAfter)
}

// scenario: registry-outage-stays-retryable — a pull that fails for any reason but absence is returned from Reconcile
// for the controller's backoff, and the stored status is unchanged.
func TestADR0149_RegistryOutageStaysRetryable(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK, withPeriod)
	h.rt.failImage(ruby3Image, fault.Unavailablef("runtime.containerd.Create", "pull image %q: connection refused", ruby3Image))
	h.create(t, "rb", func(fn *v1.Function) { fn.Spec.Runtime = "ruby3" })
	pass := func() error {
		_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "rb"})
		return err
	}

	err := pass()
	require.Error(t, err)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.False(t, errors.Is(err, runtime.ErrImageUnavailable))
	before := h.getFn(t, "rb").Status
	require.NotEqual(t, v1.PhaseFailed, before.Phase)
	_, hasReady := before.Conditions.Get("Ready")
	require.False(t, hasReady, "no outcome is written for an outage")

	require.Error(t, pass())
	require.Equal(t, before, h.getFn(t, "rb").Status, "the stored status is unchanged")
	require.Equal(t, 2, h.rt.createAttempts(), "the pass is retried")
}

// scenario: unavailable-runtime-keeps-serving-revision — a switch to a runtime whose image is absent leaves the
// serving revision serving and RevisionReady carrying the reason; once the image appears, the calls switch.
func TestADR0149_UnavailableRuntimeKeepsServingRevision(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK, withSwitch)
	url1, _ := h.rt.serveRevision(t, "sw-1", http.StatusOK)
	url2, _ := h.rt.serveRevision(t, "sw-2", http.StatusOK)
	h.deployReady(t, "sw")
	h.rt.failImage(ruby3Image, notEmbedded(ruby3Image))

	h.apply(t, "sw", func(fn *v1.Function) { fn.Spec.Runtime = "ruby3" })
	res := h.reconcile(t, "sw")

	fn := h.getFn(t, "sw")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, v1.ConditionTrue, h.condition(t, "sw", "Ready").Status)
	require.Equal(t, "sw-1", fn.Status.ServingRevision)
	rr := h.condition(t, "sw", "RevisionReady")
	require.Equal(t, v1.ConditionFalse, rr.Status)
	require.Equal(t, "RuntimeUnavailable", rr.Reason)
	require.Equal(t, ruby3NotEmbedded, rr.Message)
	states := h.rt.revisionStates("sw")
	require.NotContains(t, states, v1.ObjectName("sw-2"), "no worker of the new revision remains")
	require.Equal(t, runtime.StateRunning, states["sw-1"][0])
	require.Equal(t, testPeriod, res.RequeueAfter)
	up, ready := h.upstream(t, "sw")
	require.True(t, ready)
	require.Equal(t, url1, up)

	h.rt.failImage(ruby3Image, nil)
	h.reconcile(t, "sw")
	fn = h.getFn(t, "sw")
	require.Equal(t, "sw-2", fn.Status.ServingRevision, "the calls switch to the new revision")
	require.Equal(t, v1.ConditionTrue, h.condition(t, "sw", "RevisionReady").Status)
	up, _ = h.upstream(t, "sw")
	require.Equal(t, url2, up)
	require.Equal(t, ruby3Image, h.rt.specOf(runtime.NewInstanceID("default", "sw", "sw-2", 0)).Image)
}

// scenario: node-runtime-unchanged — node-family runtimes keep the node shim and pool host in process mode, and
// containerd mode runs nodejs22 from imageFor with no gate.
func TestADR0149_NodeRuntimeUnchanged(t *testing.T) {
	t.Parallel()
	nodeShim := []string{"node", "/opt/funcd/shim.mjs"}
	nodePool := []string{"node", "/opt/funcd/pool.mjs"}
	h := newShimHarness(t, http.StatusOK, false, withNodePool)
	for _, tc := range []struct {
		name    string
		runtime v1.RuntimeName
		worker  string
	}{
		{"solo-nodejs22", "nodejs22", ""},
		{"solo-node", "node", ""},
		{"pool-nodejs22", "nodejs22", "w1"},
		{"pool-node", "node", "w2"},
	} {
		h.create(t, tc.name, func(fn *v1.Function) {
			fn.Spec.Runtime = tc.runtime
			fn.Spec.Pooling.Worker = tc.worker
		})
		h.reconcile(t, tc.name)
		require.NotEqual(t, v1.PhaseFailed, h.getFn(t, tc.name).Status.Phase, tc.name)
	}
	for _, name := range []v1.ObjectName{"solo-nodejs22", "solo-node"} {
		spec, ok := h.rt.specFor(name)
		require.True(t, ok, name)
		require.Equal(t, nodeShim, spec.Command, "%s runs on the node shim", name)
	}
	pools := 0
	for _, spec := range h.rt.allSpecs() {
		if slices.Equal(spec.Command, nodePool) {
			pools++
		}
	}
	require.Equal(t, 2, pools, "each pooled node-family Function runs on a node pool host")

	c := newContainerHarness(t, http.StatusOK)
	c.createFn(t, "echo")
	c.reconcile(t, "echo")
	require.Equal(t, v1.PhaseReady, c.getFn(t, "echo").Status.Phase)
	spec, ok := c.rt.specFor("echo")
	require.True(t, ok)
	require.Equal(t, "funcd/runtime-nodejs22:latest", spec.Image)
}
