package function_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0344: an Addon ships runtimes, and a Function or a Job names one as it names one of funcd's.

const (
	denoImageA = "registry.example.com/deno@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	denoImageB = "registry.example.com/deno@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	// deno2Curated is the image ADR-0149 resolves deno2 to with no Addon: under the default prefix it is not embedded.
	deno2Curated = "funcd/runtime-deno2:latest"
)

// putAddon stores the Addon deno, which ships deno2 from image for workloads, or replaces the stored one.
func (h *shimHarness) putAddon(t *testing.T, image string, workloads ...v1.Workload) {
	t.Helper()
	ctx := context.Background()
	obj, _ := v1.NewObject(v1.KindAddon)
	a := obj.(*v1.Addon)
	a.Name = "deno"
	a.Spec.Runtimes = []v1.ShippedRuntime{{Name: "deno2", Image: image, Workloads: workloads}}
	if a.Spec.Runtimes[0].RunsKind() {
		a.Spec.Runtimes[0].Attributes = &v1.AddonAttributes{Kind: "Deno", Port: 8080, Readiness: v1.AddonReadiness{Path: "/"}}
	}
	cur, err := h.st.Get(ctx, v1.KindAddon.GVK(), "", "deno")
	if err == nil {
		a.ResourceVersion = cur.GetObjectMeta().ResourceVersion
		_, err = h.st.Update(ctx, a)
	} else {
		_, err = h.st.Create(ctx, a)
	}
	require.NoError(t, err)
}

func (h *shimHarness) deleteAddon(t *testing.T) {
	t.Helper()
	require.NoError(t, h.st.Delete(context.Background(), v1.KindAddon.GVK(), "", "deno", ""))
}

// onDeno2 makes a Function run on deno2.
func onDeno2(fn *v1.Function) { fn.Spec.Runtime = "deno2" }

// workerImage is the image of replica 0 of revision rev of name.
func (h *shimHarness) workerImage(name, rev v1.ObjectName) string {
	return h.rt.specOf(runtime.NewInstanceID("default", name, rev, 0)).Image
}

// requireReady asserts name is Ready with one replica.
func (h *shimHarness) requireReady(t *testing.T, name string) {
	t.Helper()
	fn := h.getFn(t, name)
	ready := h.condition(t, name, "Ready")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "%s: %s", ready.Reason, ready.Message)
	require.Equal(t, 1, fn.Status.Replicas)
}

// scenario: addon-runtime-function — a Function on deno2, which Addon deno ships for Functions, runs deno2's image on
// containerd and turns Ready; its Revision records that image.
func TestScenarioAddonRuntimeFunction(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK)
	h.putAddon(t, denoImageA, v1.WorkloadFunction)
	h.create(t, "greeter", onDeno2)
	h.reconcile(t, "greeter")

	h.requireReady(t, "greeter")
	require.Equal(t, denoImageA, h.workerImage("greeter", "greeter-1"), "the worker runs the Addon's image")
	rev, err := h.revision(t, "greeter-1")
	require.NoError(t, err)
	require.Equal(t, denoImageA, rev.Spec.RuntimeImage, "the Revision records it at its creation")
	spec := h.rt.specOf(runtime.NewInstanceID("default", "greeter", "greeter-1", 0))
	require.Empty(t, spec.Command, "the image's shim is its entrypoint")
	require.NotContains(t, spec.Env, "PYTHONPATH", "a runtime outside the Python family vendors no dependency path")
}

// scenario: addon-runtime-wrong-workload — a runtime whose entry serves only Jobs, or an engine, fails a Function with
// RuntimeUnavailable naming the Addon and the workloads, before any worker starts.
func TestScenarioAddonRuntimeWrongWorkload(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		workload v1.Workload
		want     string
	}{
		{v1.WorkloadJob, `runtime "deno2" is not available on this node: Addon "deno" ships it for Job, not for Functions`},
		{v1.WorkloadEngine, `runtime "deno2" is not available on this node: Addon "deno" ships it for Engine, not for Functions`},
	} {
		h := newContainerHarness(t, http.StatusOK)
		h.putAddon(t, denoImageA, c.workload)
		h.create(t, "greeter", onDeno2)
		h.reconcile(t, "greeter")

		h.requireUnavailable(t, "greeter", c.want)
		require.Zero(t, h.rt.createAttempts(), "%s: no worker is created", c.workload)
	}
}

// scenario: addon-runtime-process-driver — on a process server no worker driver runs an image, so a Function on an
// Addon's runtime fails with RuntimeUnavailable, with no worker.
func TestScenarioAddonRuntimeProcessDriver(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false)
	h.putAddon(t, denoImageA, v1.WorkloadFunction)
	h.create(t, "greeter", onDeno2)
	h.reconcile(t, "greeter")

	h.requireUnavailable(t, "greeter",
		`runtime "deno2" is not available on this node: it is an Addon's image, which no worker driver of this server runs`)
	require.Zero(t, h.rt.createAttempts(), "no worker is created")
}

// scenario: addon-runtime-appears — a Function on deno2 applied before deno, then while deno2 served only Jobs, turns
// Ready once deno ships deno2 for Functions, with no change of its own: the Addon's change requeues it.
func TestScenarioAddonRuntimeAppears(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK)
	h.rt.failImage(deno2Curated, notEmbedded(deno2Curated))
	h.create(t, "greeter", onDeno2)
	h.reconcile(t, "greeter")
	require.Equal(t, "RuntimeUnavailable", h.condition(t, "greeter", "Ready").Reason, "no Addon ships deno2 yet")
	generation := h.getFn(t, "greeter").Generation

	h.putAddon(t, denoImageA, v1.WorkloadJob)
	h.reconcileMapped(t, "greeter")
	require.Contains(t, h.condition(t, "greeter", "Ready").Message, `ships it for Job, not for Functions`)

	h.putAddon(t, denoImageA, v1.WorkloadFunction, v1.WorkloadJob)
	h.reconcileMapped(t, "greeter")
	h.requireReady(t, "greeter")
	require.Equal(t, generation, h.getFn(t, "greeter").Generation, "the Function did not change")
	require.Equal(t, denoImageA, h.workerImage("greeter", "greeter-1"))
	rev, err := h.revision(t, "greeter-1")
	require.NoError(t, err)
	require.Equal(t, denoImageA, rev.Spec.RuntimeImage, "the Revision created before the runtime served it records it once")
}

// reconcileMapped runs the pass the Addon watch asks for: MapAddon must name the Function.
func (h *shimHarness) reconcileMapped(t *testing.T, name string) {
	t.Helper()
	reqs := h.r.MapAddon(context.Background(), nil)
	require.Contains(t, reqs, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	h.reconcile(t, name)
}

// scenario: addon-runtime-pinned — an Addon update to image B moves no Revision: greeter's keeps A, a replaced worker
// runs A again, and only a change of the Function's spec makes a Revision that runs B.
func TestScenarioAddonRuntimePinned(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK, withPeriod)
	h.putAddon(t, denoImageA, v1.WorkloadFunction)
	h.create(t, "greeter", onDeno2)
	h.reconcile(t, "greeter")
	h.requireReady(t, "greeter")

	h.putAddon(t, denoImageB, v1.WorkloadFunction)
	h.reconcileMapped(t, "greeter")
	rev, err := h.revision(t, "greeter-1")
	require.NoError(t, err)
	require.Equal(t, denoImageA, rev.Spec.RuntimeImage, "the Revision keeps its image")
	h.rt.exit("greeter", runtime.StateFailed, time.Hour)
	require.Eventually(t, func() bool {
		h.reconcile(t, "greeter")
		return h.getFn(t, "greeter").Status.Phase == v1.PhaseReady && h.rt.stateOf(runtime.NewInstanceID("default", "greeter", "greeter-1", 0)) == runtime.StateRunning
	}, 5*time.Second, 10*time.Millisecond, "the worker is replaced")
	require.Equal(t, denoImageA, h.workerImage("greeter", "greeter-1"), "a replaced worker runs the pinned image")

	h.apply(t, "greeter", func(fn *v1.Function) { fn.Spec.Handler = "handle2" })
	require.Eventually(t, func() bool {
		h.reconcile(t, "greeter")
		fn := h.getFn(t, "greeter")
		return fn.Status.ServingRevision == "greeter-2" && fn.Status.Phase == v1.PhaseReady
	}, 5*time.Second, 10*time.Millisecond, "the new generation serves")
	rev, err = h.revision(t, "greeter-2")
	require.NoError(t, err)
	require.Equal(t, denoImageB, rev.Spec.RuntimeImage, "a new spec makes a Revision with the newer image")
	require.Equal(t, denoImageB, h.workerImage("greeter", "greeter-2"))
}

// scenario: addon-runtime-gone — once deno is deleted, greeter's next pass fails RuntimeUnavailable, while its serving
// Revision keeps serving with RevisionReady False; once its worker stops, greeter fails.
func TestScenarioAddonRuntimeGone(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK, withPeriod)
	h.putAddon(t, denoImageA, v1.WorkloadFunction)
	h.create(t, "greeter", onDeno2)
	h.reconcile(t, "greeter")
	h.requireReady(t, "greeter")

	h.deleteAddon(t)
	h.reconcileMapped(t, "greeter")
	gone := `runtime "deno2" is not available on this node: the Addon that shipped it is gone`
	revReady := h.condition(t, "greeter", "RevisionReady")
	require.Equal(t, v1.ConditionFalse, revReady.Status)
	require.Equal(t, "RuntimeUnavailable", revReady.Reason)
	require.Equal(t, gone, revReady.Message)
	fn := h.getFn(t, "greeter")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "the serving Revision keeps serving")
	require.Equal(t, 1, fn.Status.Replicas)

	h.rt.exit("greeter", runtime.StateStopped, time.Hour)
	h.reconcile(t, "greeter")
	h.requireUnavailable(t, "greeter", gone)
}

// scenario: operator-runtime-unchanged — an operator's imageOverride for ruby3 runs a Function on ruby3 as ADR-0149
// decides, with no Addon. An Addon that ships ruby3 after the key was added shadows it only once no worker of the
// Revision runs: the running worker keeps the operator's image (ADR-0344 Decisions 6 and 8).
func TestScenarioOperatorRuntimeUnchanged(t *testing.T) {
	t.Parallel()
	const override = "registry.example.com/ruby@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	h := newContainerHarness(t, http.StatusOK, withPeriod, func(d *function.Deps) {
		d.ImageFor = func(rt string) string {
			if rt == "ruby3" {
				return override
			}
			return "funcd/runtime-" + rt + ":latest"
		}
	})
	h.create(t, "rb", func(fn *v1.Function) { fn.Spec.Runtime = "ruby3" })
	h.reconcile(t, "rb")
	h.requireReady(t, "rb")
	require.Equal(t, override, h.workerImage("rb", "rb-1"))
	rev, err := h.revision(t, "rb-1")
	require.NoError(t, err)
	require.Empty(t, rev.Spec.RuntimeImage, "an operator's runtime records no image")

	ctx := context.Background()
	obj, _ := v1.NewObject(v1.KindAddon)
	a := obj.(*v1.Addon)
	a.Name, a.Spec.Runtimes = "ruby", []v1.ShippedRuntime{{Name: "ruby3", Image: denoImageA, Workloads: []v1.Workload{v1.WorkloadFunction}}}
	_, err = h.st.Create(ctx, a)
	require.NoError(t, err)
	h.reconcileMapped(t, "rb")
	rev, err = h.revision(t, "rb-1")
	require.NoError(t, err)
	require.Empty(t, rev.Spec.RuntimeImage, "a running worker of the Revision keeps it unrecorded")
	require.Equal(t, override, h.workerImage("rb", "rb-1"))

	h.rt.exit("rb", runtime.StateStopped, time.Hour)
	require.Eventually(t, func() bool {
		h.reconcile(t, "rb")
		return h.getFn(t, "rb").Status.Phase == v1.PhaseReady && h.rt.stateOf(runtime.NewInstanceID("default", "rb", "rb-1", 0)) == runtime.StateRunning
	}, 5*time.Second, 10*time.Millisecond, "the worker is replaced")
	rev, err = h.revision(t, "rb-1")
	require.NoError(t, err)
	require.Equal(t, denoImageA, rev.Spec.RuntimeImage, "with no worker running, the Revision records the Addon's image")
	require.Equal(t, denoImageA, h.workerImage("rb", "rb-1"))
}

// ADR-0344 Decision 7: an Addon's change requeues every Function whose runtime is none of funcd's own.
func TestAddonWatchMapsFunctionsOffFuncdsRuntimes(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK)
	h.create(t, "node", func(*v1.Function) {})
	h.create(t, "py", func(fn *v1.Function) { fn.Spec.Runtime = "python314" })
	h.create(t, "rb", func(fn *v1.Function) { fn.Spec.Runtime = "ruby3" })
	h.create(t, "greeter", onDeno2)
	reqs := h.r.MapAddon(context.Background(), nil)
	require.ElementsMatch(t, []controller.Request{
		{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "rb"},
		{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "greeter"},
	}, reqs)
}

// scenario: addon-runtime-job — a Job on deno2, which deno ships for Jobs, runs its runs on deno2's image; a Job on a
// runtime its entry ships only for Functions is not Ready, RuntimeUnavailable.
func TestScenarioAddonRuntimeJob(t *testing.T) {
	t.Parallel()
	h := newExecRig(t, 1<<20, func(d *function.Deps) {
		d.EndpointMode = function.EndpointNetnsFixedPort
		d.ImageFor = func(rt string) string { return "funcd/runtime-" + rt + ":latest" }
	})
	h.putAddon(t, denoImageA, v1.WorkloadJob)
	h.readyJob(t, func(s *v1.JobSpec) { s.Runtime = "deno2" })
	rev, err := h.revision(t, "hello-v1")
	require.NoError(t, err)
	require.Equal(t, denoImageA, rev.Spec.RuntimeImage, "the Job's Revision records the image")
	reqs := h.r.Jobs().MapAddon(context.Background(), nil)
	require.Contains(t, reqs, controller.Request{GVK: v1.KindJob.GVK(), Namespace: "default", Name: "hello"}, "the Job controller watches Addons")

	h.serveRun(t, "hello-1", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	h.run(t, "hello-1", `{}`, nil)
	h.pass(t, "hello-1")
	run := h.end(t, "hello-1")
	requireEnd(t, run, v1.JobRunSucceeded, "")
	specs := h.createdFor("hello-1", "hello-v1")
	require.Len(t, specs, 1)
	require.Equal(t, denoImageA, specs[0].Image, "the run's worker runs deno2's image")

	h.putAddon(t, denoImageA, v1.WorkloadFunction)
	obj, _ := v1.NewObject(v1.KindJob)
	job := obj.(*v1.Job)
	job.Name, job.Namespace, job.ResourceGroup = "other", "default", "rg1"
	job.Spec = v1.JobSpec{Runtime: "deno2", Handler: "handle", Image: "file://" + h.artifact}
	_, err = h.st.Create(context.Background(), job)
	require.NoError(t, err)
	_, err = h.r.Jobs().Reconcile(context.Background(), controller.Request{GVK: v1.KindJob.GVK(), Namespace: "default", Name: "other"})
	require.NoError(t, err)
	obj, err = h.st.Get(context.Background(), v1.KindJob.GVK(), "default", "other")
	require.NoError(t, err)
	ready := readyOf(t, obj.(*v1.Job))
	require.Equal(t, "RuntimeUnavailable", ready.Reason)
	require.Contains(t, ready.Message, "ships it for Function, not for Jobs")
}
