//go:build e2e

package funcd_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// revisionEnv is a process-shim platform with a two-step Workflow flow = a then b (ADR-0190): a blocks until its
// gate file exists, each b image appends its version to a log file.
type revisionEnv struct {
	c      *sdk.Client
	src    string
	layout string
	gate   string
	log    string
}

func newRevisionEnv(t *testing.T, opts ...funcd.Option) *revisionEnv {
	t.Helper()
	c, _ := shimPlatformOCI(t, opts...)
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	e := &revisionEnv{c: c, src: t.TempDir(), layout: t.TempDir(), gate: filepath.Join(dir, "gate"), log: filepath.Join(dir, "b.log")}
	writeStep(t, e.src, "a", fmt.Sprintf(`import { existsSync } from 'node:fs';
export const handle = async () => { while (!existsSync(%q)) await new Promise((r) => setTimeout(r, 50)); return {}; };`, e.gate))
	return e
}

// bImage pushes a b step that appends version to the log.
func (e *revisionEnv) bImage(t *testing.T, version string) string {
	t.Helper()
	name := "b" + version
	writeStep(t, e.src, name, fmt.Sprintf(`import { appendFileSync } from 'node:fs';
export const handle = async () => { appendFileSync(%q, %q); return { v: %q }; };`, e.log, version+"\n", version))
	return pushStepImage(t, e.layout, e.src, name)
}

func (e *revisionEnv) applyFlow(t *testing.T, steps ...v1.WorkflowStep) {
	t.Helper()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "flow", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowSpec{Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated, MinReplicas: 1}, Steps: steps},
	}
	_, err := e.c.Apply(context.Background(), wf)
	require.NoError(t, err)
}

func (e *revisionEnv) flowSteps(t *testing.T, bImg string) []v1.WorkflowStep {
	t.Helper()
	return []v1.WorkflowStep{
		{Name: "a", Function: &v1.FunctionStep{Image: pushStepImage(t, e.layout, e.src, "a"), Timeout: time.Minute}},
		{Name: "b", Function: &v1.FunctionStep{Image: bImg}, DependsOn: []v1.ObjectName{"a"}},
	}
}

// waitWorkflowReady waits until flow is Ready for its current generation.
func (e *revisionEnv) waitWorkflowReady(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(context.Background(), v1.KindWorkflow, "default", "flow")
		if err != nil {
			return false
		}
		wf := obj.(*v1.Workflow)
		c, ok := wf.Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionTrue && c.ObservedGeneration == wf.Generation
	}, 30*time.Second, 50*time.Millisecond, "flow is Ready")
}

// waitServing waits until Function name serves a Revision of image img.
func (e *revisionEnv) waitServing(t *testing.T, name, img string) {
	t.Helper()
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(context.Background(), v1.KindFunction, "default", v1.ObjectName(name))
		if err != nil {
			return false
		}
		fn := obj.(*v1.Function)
		if fn.Spec.Image != img || fn.Status.Phase != v1.PhaseReady || fn.Status.ServingRevision != fn.Status.CurrentRevision {
			return false
		}
		rev, err := e.c.Get(context.Background(), v1.KindRevision, "default", v1.ObjectName(fn.Status.CurrentRevision))
		return err == nil && rev.(*v1.Revision).Spec.Image == img
	}, 30*time.Second, 50*time.Millisecond, "%s serves %s", name, img)
}

func (e *revisionEnv) waitRunning(t *testing.T, run, step string) {
	t.Helper()
	require.Eventually(t, func() bool { return runStepPhase(getRun(t, e.c, run), step) == v1.StepRunning },
		30*time.Second, 20*time.Millisecond, "%s runs %s", run, step)
}

func (e *revisionEnv) waitPhase(t *testing.T, run string, phase v1.RunPhase) {
	t.Helper()
	require.Eventually(t, func() bool { return getRun(t, e.c, run).Status.Phase == phase },
		60*time.Second, 50*time.Millisecond, "%s ends %s", run, phase)
}

// block makes a block again.
func (e *revisionEnv) block(t *testing.T) {
	t.Helper()
	require.NoError(t, os.Remove(e.gate))
}

func (e *revisionEnv) gone(t *testing.T, kind v1.Kind, name string) bool {
	t.Helper()
	_, err := e.c.Get(context.Background(), kind, "default", v1.ObjectName(name))
	return err != nil && strings.Contains(err.Error(), "not found")
}

// applyShared applies the standalone Function shared with image img.
func (e *revisionEnv) applyShared(t *testing.T, img string) {
	t.Helper()
	fn := &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "shared", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: img, Scaling: v1.Scaling{MinReplicas: 1}},
	}
	_, err := e.c.Apply(context.Background(), fn)
	require.NoError(t, err)
	e.waitServing(t, "shared", img)
}

func runStepError(run *v1.WorkflowRun, step string) string {
	for _, s := range run.Status.Steps {
		if string(s.Name) == step {
			return s.Error
		}
	}
	return ""
}

func (e *revisionEnv) release(t *testing.T) {
	t.Helper()
	require.NoError(t, os.WriteFile(e.gate, nil, 0o600))
}

func (e *revisionEnv) logged(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.log)
	require.NoError(t, err)
	return string(b)
}

func runStepRevision(run *v1.WorkflowRun, step string) string {
	for _, s := range run.Status.Steps {
		if string(s.Name) == step {
			return s.Revision
		}
	}
	return ""
}

// scenario: edit-mid-run-keeps-old-code (ADR-0190, #776) — a run started on b v1 runs v1's b after b is edited to
// v2 and flow is Ready again; the next run runs v2.
func TestIssue776_EditMidRunKeepsOldCode(t *testing.T) {
	e := newRevisionEnv(t)
	v1Img, v2Img := e.bImage(t, "v1"), e.bImage(t, "v2")
	steps := e.flowSteps(t, v1Img)
	e.applyFlow(t, steps...)
	waitMaterializedReady(t, e.c, "flow-a", "flow-b")
	e.waitWorkflowReady(t)

	applyRun(t, e.c, "r1", "flow")
	e.waitRunning(t, "r1", "a")
	steps[1].Function.Image = v2Img
	e.applyFlow(t, steps...)
	e.waitWorkflowReady(t)
	e.waitServing(t, "flow-b", v2Img)

	e.release(t)
	e.waitPhase(t, "r1", v1.RunSucceeded)
	require.Equal(t, "v1\n", e.logged(t), "r1's b runs the code it started with")
	require.Contains(t, runStepRevision(getRun(t, e.c, "r1"), "b"), v1Img, "r1 records v1's b")

	applyRun(t, e.c, "r2", "flow")
	e.waitPhase(t, "r2", v1.RunSucceeded)
	require.Equal(t, "v1\nv2\n", e.logged(t), "a run started after the edit runs v2")
	require.Contains(t, runStepRevision(getRun(t, e.c, "r2"), "b"), v2Img)
}

// scenario: removed-step-still-runs (ADR-0190) — b removed from flow while r1 runs a: r1's b runs v1, and flow-b is
// deleted only after r1 ends.
func TestScenarioRemovedStepStillRuns(t *testing.T) {
	e := newRevisionEnv(t)
	steps := e.flowSteps(t, e.bImage(t, "v1"))
	e.applyFlow(t, steps...)
	waitMaterializedReady(t, e.c, "flow-a", "flow-b")
	e.waitWorkflowReady(t)

	applyRun(t, e.c, "r1", "flow")
	e.waitRunning(t, "r1", "a")
	e.applyFlow(t, steps[0])
	e.waitWorkflowReady(t)
	time.Sleep(time.Second)
	require.False(t, e.gone(t, v1.KindFunction, "flow-b"), "the removed step's Function waits for r1")

	e.release(t)
	e.waitPhase(t, "r1", v1.RunSucceeded)
	require.Equal(t, "v1\n", e.logged(t), "r1's removed b still runs")
	require.Eventually(t, func() bool { return e.gone(t, v1.KindFunction, "flow-b") }, 60*time.Second, 100*time.Millisecond,
		"flow-b is pruned once r1 ended")
}

// scenario: two-revisions-side-by-side (ADR-0190) — r1 pinned to v1 and r2 pinned to v2 call b concurrently; each is
// answered by its own revision.
func TestScenarioTwoRevisionsSideBySide(t *testing.T) {
	e := newRevisionEnv(t)
	v1Img, v2Img := e.bImage(t, "v1"), e.bImage(t, "v2")
	steps := e.flowSteps(t, v1Img)
	e.applyFlow(t, steps...)
	waitMaterializedReady(t, e.c, "flow-a", "flow-b")
	e.waitWorkflowReady(t)

	applyRun(t, e.c, "r1", "flow")
	e.waitRunning(t, "r1", "a")
	steps[1].Function.Image = v2Img
	e.applyFlow(t, steps...)
	e.waitWorkflowReady(t)
	e.waitServing(t, "flow-b", v2Img)
	applyRun(t, e.c, "r2", "flow")
	e.waitRunning(t, "r2", "a")

	e.release(t)
	e.waitPhase(t, "r1", v1.RunSucceeded)
	e.waitPhase(t, "r2", v1.RunSucceeded)
	lines := strings.Fields(e.logged(t))
	sort.Strings(lines)
	require.Equal(t, []string{"v1", "v2"}, lines, "each run's b is answered once, by its own revision")
	require.Contains(t, runStepRevision(getRun(t, e.c, "r1"), "b"), v1Img)
	require.Contains(t, runStepRevision(getRun(t, e.c, "r2"), "b"), v2Img)
}

// scenario: ref-step-bound (ADR-0190) — a function.ref step runs the pinned revision of shared after shared is edited;
// once shared is deleted and re-created, the step fails naming the pin.
func TestScenarioRefStepBound(t *testing.T) {
	e := newRevisionEnv(t)
	s1, s2 := e.bImage(t, "v1"), e.bImage(t, "v2")
	e.applyShared(t, s1)
	e.applyFlow(t,
		v1.WorkflowStep{Name: "a", Function: &v1.FunctionStep{Image: pushStepImage(t, e.layout, e.src, "a"), Timeout: time.Minute}},
		v1.WorkflowStep{Name: "c", Function: &v1.FunctionStep{Ref: "shared"}, DependsOn: []v1.ObjectName{"a"}},
	)
	waitMaterializedReady(t, e.c, "flow-a")
	e.waitWorkflowReady(t)

	applyRun(t, e.c, "r1", "flow")
	e.waitRunning(t, "r1", "a")
	e.applyShared(t, s2)
	e.release(t)
	e.waitPhase(t, "r1", v1.RunSucceeded)
	require.Equal(t, "v1\n", e.logged(t), "the ref step runs the revision of shared the run started with")

	e.block(t)
	applyRun(t, e.c, "r3", "flow")
	e.waitRunning(t, "r3", "a")
	require.NoError(t, e.c.Delete(context.Background(), v1.KindFunction, "default", "shared"))
	require.Eventually(t, func() bool { return e.gone(t, v1.KindFunction, "shared") }, 30*time.Second, 50*time.Millisecond)
	e.applyShared(t, s2)
	e.release(t)
	e.waitPhase(t, "r3", v1.RunFailed)
	require.Contains(t, runStepError(getRun(t, e.c, "r3"), "c"), "pinned revision", "the step names the pin")
	require.Equal(t, "v1\n", e.logged(t), "nothing falls back to the re-created shared")
}

// scenario: workflow-delete-cancels-runs (ADR-0190) — flow deleted and re-created under the same name while r1 is
// open: r1 is cancelled, the old step Functions are collected and the new flow becomes Ready.
func TestScenarioWorkflowDeleteCancelsRuns(t *testing.T) {
	e := newRevisionEnv(t, funcd.WithGCSweepInterval(500*time.Millisecond))
	steps := e.flowSteps(t, e.bImage(t, "v1"))
	e.applyFlow(t, steps...)
	waitMaterializedReady(t, e.c, "flow-a", "flow-b")
	e.waitWorkflowReady(t)
	applyRun(t, e.c, "r1", "flow")
	e.waitRunning(t, "r1", "a")
	old, err := e.c.Get(context.Background(), v1.KindWorkflow, "default", "flow")
	require.NoError(t, err)

	require.NoError(t, e.c.Delete(context.Background(), v1.KindWorkflow, "default", "flow"))
	require.Eventually(t, func() bool { return e.gone(t, v1.KindWorkflow, "flow") }, 30*time.Second, 50*time.Millisecond)
	e.applyFlow(t, steps...)
	e.waitPhase(t, "r1", v1.RunCancelled)
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(context.Background(), v1.KindWorkflow, "default", "flow")
		if err != nil || obj.GetObjectMeta().UID == old.GetObjectMeta().UID {
			return false
		}
		wf := obj.(*v1.Workflow)
		c, ok := wf.Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionTrue && c.ObservedGeneration == wf.Generation
	}, 90*time.Second, 100*time.Millisecond, "the re-created flow becomes Ready once the old step Functions are collected")

	e.release(t)
	applyRun(t, e.c, "r2", "flow")
	e.waitPhase(t, "r2", v1.RunSucceeded)
}
