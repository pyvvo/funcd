package funcd_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/artifact"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// writeStep writes a tiny ES-module step handler to dir/<name>.mjs. A workflow step handler reads
// the raw `event` (the dispatcher POSTs the step input verbatim — a parent's output, or the run
// input for a root step) and returns the raw output that becomes the next step's input. No build
// step: the shim loads a plain module, defaults the handler to `handle`, and treats baked
// validators as optional (shim.mjs resolveValidators/resolveHandler).
func writeStep(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".mjs"), []byte(body), 0o600))
}

// pushStepImage pushes a step handler as an OCI artifact carrying the dev.funcd.runtime.v1
// annotation (ADR-0094), so the workflow materializer resolves its runtime from the manifest alone.
// Returns the ref (a tag; the Function reconciler pins the digest at Revision, ADR-0035).
func pushStepImage(t *testing.T, layoutDir, srcDir, name string) string {
	t.Helper()
	ref := "oci-layout://" + layoutDir + ":" + name
	_, err := artifact.Push(context.Background(), ref, filepath.Join(srcDir, name+".mjs"), nil, "nodejs22")
	require.NoError(t, err)
	return ref
}

// waitMaterializedReady waits for each owned step Function to be CREATED (materialization is
// async — the Workflow reconciler makes them on its own turn) and then reconcile to Ready. Unlike
// the shared phaseOf helper, it tolerates a transient NotFound while materialization catches up.
func waitMaterializedReady(t *testing.T, c *sdk.Client, names ...string) {
	t.Helper()
	for _, n := range names {
		name := v1.ObjectName(n)
		require.Eventually(t, func() bool {
			obj, err := c.Get(context.Background(), v1.KindFunction, "default", name)
			if err != nil {
				return false // not materialized yet
			}
			return obj.(*v1.Function).Status.Phase == v1.PhaseReady
		}, 30*time.Second, 100*time.Millisecond, "%s materializes and reconciles to Ready", n)
	}
}

func getRun(t *testing.T, c *sdk.Client, name string) *v1.WorkflowRun {
	t.Helper()
	obj, err := c.Get(context.Background(), v1.KindWorkflowRun, "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.WorkflowRun)
}

func runStepPhase(run *v1.WorkflowRun, step string) v1.StepPhase {
	for _, s := range run.Status.Steps {
		if string(s.Name) == step {
			return s.Phase
		}
	}
	return ""
}

// scenario: end-to-end workflow execution (ADR-0094) — the REAL orchestration path over the shim
// platform: image steps are materialized into owned Functions that PULL + serve; a WorkflowRun is
// driven through the engine, dispatching each step over the real activator+HTTP path. It exercises
// sequential chaining (verbatim parent output), fan-out, native-JS when-conditions (one branch
// Skipped), a join:any merge, and the status mirror + status.runs link — all with real functions,
// no fakes.
func TestScenarioWorkflowEndToEnd(t *testing.T) {
	c, _ := shimPlatformOCI(t)
	src := t.TempDir()
	layout := t.TempDir()

	// Five tiny step handlers. Each is an ordinary funcd function: it reads the CloudEvent's `data`
	// (the flowing input the dispatcher enveloped) and returns the raw output that becomes the next
	// step's input.
	writeStep(t, src, "ingest", `export const handle = (ctx, e) => ({ n: e.data.n, ingested: true });`)
	writeStep(t, src, "enrich", `export const handle = (ctx, e) => ({ n: e.data.n, enriched: e.data.n * 2 });`)
	writeStep(t, src, "hi", `export const handle = (ctx, e) => ({ picked: "hi", enriched: e.data.enriched });`)
	writeStep(t, src, "lo", `export const handle = (ctx, e) => ({ picked: "lo", enriched: e.data.enriched });`)
	writeStep(t, src, "report", `export const handle = (ctx, e) => ({ done: true, branches: Object.keys(e.data) });`)

	img := map[string]string{}
	for _, s := range []string{"ingest", "enrich", "hi", "lo", "report"} {
		img[s] = pushStepImage(t, layout, src, s)
	}

	// The DAG: ingest → enrich → {hi | lo (exclusive via when)} → report (join: any).
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "flow", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			// isolated pooling + one warm replica each → solo workers on the process shim, no cold-start race.
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated, MinReplicas: 1},
			Steps: []v1.WorkflowStep{
				{Name: "ingest", Function: &v1.FunctionStep{Image: img["ingest"]}},
				{Name: "enrich", Function: &v1.FunctionStep{Image: img["enrich"]}, DependsOn: []v1.ObjectName{"ingest"}},
				{Name: "hi", Function: &v1.FunctionStep{Image: img["hi"]}, DependsOn: []v1.ObjectName{"enrich"}, When: &v1.StepWhen{Condition: `${{ step.enrich.output.enriched > 5 }}`}},
				{Name: "lo", Function: &v1.FunctionStep{Image: img["lo"]}, DependsOn: []v1.ObjectName{"enrich"}, When: &v1.StepWhen{Condition: `${{ step.enrich.output.enriched <= 5 }}`}},
				{Name: "report", Function: &v1.FunctionStep{Image: img["report"]}, DependsOn: []v1.ObjectName{"hi", "lo"}, Join: v1.JoinAny},
				// engine-native built-ins (ADR-0096): a wait parks the run on a durable timer, then a pass
				// reshapes report's (flowed-through) output in-process — neither is materialized/dispatched.
				{Name: "settle", Builtin: &v1.BuiltinStep{Wait: "1s"}, DependsOn: []v1.ObjectName{"report"}},
				{Name: "summary", Builtin: &v1.BuiltinStep{Pass: `${{ {done: step.settle.output.done, settled: true} }}`}, DependsOn: []v1.ObjectName{"settle"}},
			},
		},
	}
	_, err := c.Apply(context.Background(), wf)
	require.NoError(t, err)

	// The Workflow reconciler materialized owned Functions <workflow>-<step>; wait for them to pull + serve.
	waitMaterializedReady(t, c, "flow-ingest", "flow-enrich", "flow-hi", "flow-lo", "flow-report")

	// Start a run with n=4 → enriched=8 (>5) → hi runs, lo Skipped, report merges hi.
	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "flow-01", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "flow", Input: json.RawMessage(`{"n":4}`)},
	}
	_, err = c.Apply(context.Background(), run)
	require.NoError(t, err)

	// The RunReconciler drives it to terminal; poll the mirrored status.
	require.Eventually(t, func() bool {
		return getRun(t, c, "flow-01").Status.Phase == "Succeeded"
	}, 30*time.Second, 100*time.Millisecond, "the run reaches Succeeded via real step execution")

	got := getRun(t, c, "flow-01")
	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(got, "ingest"), "ingest ran")
	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(got, "enrich"), "enrich ran with ingest's output")
	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(got, "hi"), "hi ran (enriched 8 > 5 — proves enrich's output flowed into the when)")
	require.Equal(t, v1.StepPhase("Skipped"), runStepPhase(got, "lo"), "lo skipped (the exclusive branch)")
	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(got, "report"), "report merged the surviving branch (join: any)")
	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(got, "settle"), "settle (builtin wait) blocked then resolved in-engine")
	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(got, "summary"), "summary (builtin pass) transformed in-engine, no dispatch")

	// A second run with n=2 → enriched=4 (≤5) → the exclusive branch flips: lo runs, hi Skipped.
	run2 := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "flow-02", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "flow", Input: json.RawMessage(`{"n":2}`)},
	}
	_, err = c.Apply(context.Background(), run2)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return getRun(t, c, "flow-02").Status.Phase == "Succeeded"
	}, 30*time.Second, 100*time.Millisecond, "the second run reaches Succeeded")
	got2 := getRun(t, c, "flow-02")
	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(got2, "lo"), "lo ran (enriched 4 ≤ 5 — the other when branch)")
	require.Equal(t, v1.StepPhase("Skipped"), runStepPhase(got2, "hi"), "hi skipped for n=2")

	// The parent Workflow links BOTH terminal runs (status.runs, the CronJob status.active pattern).
	require.Eventually(t, func() bool {
		obj, err := c.Get(context.Background(), v1.KindWorkflow, "default", "flow")
		if err != nil {
			return false
		}
		links := obj.(*v1.Workflow).Status.Runs
		return links != nil && links.Succeeded == 2 && len(links.Active) == 0
	}, 10*time.Second, 100*time.Millisecond, "status.runs shows succeeded=2, no active")

	// funcdctl-equivalent read verb over the real control plane: list the runs.
	list, err := c.List(context.Background(), v1.KindWorkflowRun, "default")
	require.NoError(t, err)
	require.Len(t, list, 2, "workflow runs lists both runs")
}

// setRunSpec applies a spec mutation to a WorkflowRun over the real control plane, retrying on the
// optimistic-concurrency conflict a concurrent reconcile can cause (the declarative pause/cancel path).
func setRunSpec(t *testing.T, c *sdk.Client, name string, mutate func(*v1.WorkflowRunSpec)) {
	t.Helper()
	require.Eventually(t, func() bool {
		run := getRun(t, c, name)
		mutate(&run.Spec)
		_, err := c.Apply(context.Background(), run)
		return err == nil
	}, 5*time.Second, 50*time.Millisecond, "apply spec change to %s", name)
}

// scenario: pause-and-resume-run + cancel-terminates-run (ADR-0094) over the REAL control plane — the
// declarative lifecycle: a run applied `paused` never dispatches (status Paused); clearing spec.paused
// resumes it to Succeeded; a second run is cancelled from Paused and ends Cancelled. This is the e2e
// proof that pause/resume/cancel work end-to-end through the reconciler + engine, not just in unit tests.
func TestScenarioWorkflowPauseResumeCancel(t *testing.T) {
	c, _ := shimPlatformOCI(t)
	src, layout := t.TempDir(), t.TempDir()
	writeStep(t, src, "work", `export const handle = (ctx, e) => ({ done: true, n: e.data.n });`)
	img := pushStepImage(t, layout, src, "work")

	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "life", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated, MinReplicas: 1},
			Steps:   []v1.WorkflowStep{{Name: "work", Function: &v1.FunctionStep{Image: img}}},
		},
	}
	_, err := c.Apply(context.Background(), wf)
	require.NoError(t, err)
	waitMaterializedReady(t, c, "life-work")

	// 1) Applied paused → the reconciler marks it Paused and dispatches nothing.
	pauseRun := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "life-pause", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "life", Input: json.RawMessage(`{"n":7}`), Paused: true},
	}
	_, err = c.Apply(context.Background(), pauseRun)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return getRun(t, c, "life-pause").Status.Phase == "Paused"
	}, 15*time.Second, 100*time.Millisecond, "the paused run reaches Paused")
	require.NotEqual(t, v1.StepPhase("Succeeded"), runStepPhase(getRun(t, c, "life-pause"), "work"),
		"the step must NOT have run while paused")

	// 2) Resume: clear spec.paused → the run drives to Succeeded.
	setRunSpec(t, c, "life-pause", func(s *v1.WorkflowRunSpec) { s.Paused = false })
	require.Eventually(t, func() bool {
		return getRun(t, c, "life-pause").Status.Phase == "Succeeded"
	}, 20*time.Second, 100*time.Millisecond, "the resumed run reaches Succeeded")
	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(getRun(t, c, "life-pause"), "work"),
		"the step ran after resume")

	// 3) Cancel from Paused: a second run applied paused, then cancelled, ends Cancelled.
	cancelRun := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "life-cancel", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "life", Input: json.RawMessage(`{"n":1}`), Paused: true},
	}
	_, err = c.Apply(context.Background(), cancelRun)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return getRun(t, c, "life-cancel").Status.Phase == "Paused"
	}, 15*time.Second, 100*time.Millisecond, "the second run reaches Paused")
	setRunSpec(t, c, "life-cancel", func(s *v1.WorkflowRunSpec) { s.Cancel = true })
	require.Eventually(t, func() bool {
		return getRun(t, c, "life-cancel").Status.Phase == "Cancelled"
	}, 20*time.Second, 100*time.Millisecond, "the cancelled run reaches Cancelled")
}
