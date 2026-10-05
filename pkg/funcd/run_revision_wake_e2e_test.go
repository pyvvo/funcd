//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// idleFlowSteps is flow = a then b with scale-to-zero steps (ADR-0190 Decision 6): a blocks the first call until the
// gate exists and answers every later call at once, so a second run passes a while the first one waits in it.
func (e *revisionEnv) idleFlowSteps(t *testing.T, bImg string) []v1.WorkflowStep {
	t.Helper()
	first := e.gate + ".first"
	writeStep(t, e.src, "aonce", fmt.Sprintf(`import { existsSync, writeFileSync } from 'node:fs';
export const handle = async () => {
  if (existsSync(%q)) return {};
  writeFileSync(%q, '');
  while (!existsSync(%q)) await new Promise((r) => setTimeout(r, 50));
  return {};
};`, first, first, e.gate))
	return []v1.WorkflowStep{
		{Name: "a", Function: &v1.FunctionStep{Image: pushStepImage(t, e.layout, e.src, "aonce"), Timeout: time.Minute}},
		{Name: "b", Function: &v1.FunctionStep{Image: bImg}, DependsOn: []v1.ObjectName{"a"}},
	}
}

// waitBlocked waits until the first call to a is in its handler, so a later call is answered at once.
func (e *revisionEnv) waitBlocked(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := os.Stat(e.gate + ".first")
		return err == nil
	}, 30*time.Second, 20*time.Millisecond, "the first call to a waits in its handler")
}

func (e *revisionEnv) applyIdleFlow(t *testing.T, steps ...v1.WorkflowStep) {
	t.Helper()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "flow", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowSpec{Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated}, Steps: steps},
	}
	_, err := e.c.Apply(context.Background(), wf)
	require.NoError(t, err)
}

func (e *revisionEnv) function(t *testing.T, name string) *v1.Function {
	t.Helper()
	obj, err := e.c.Get(context.Background(), v1.KindFunction, "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Function)
}

// waitCurrent waits until Function name's current revision holds image img and returns that revision's name.
func (e *revisionEnv) waitCurrent(t *testing.T, name, img string) string {
	t.Helper()
	var current string
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(context.Background(), v1.KindFunction, "default", v1.ObjectName(name))
		if err != nil {
			return false
		}
		current = obj.(*v1.Function).Status.CurrentRevision
		rev, err := e.c.Get(context.Background(), v1.KindRevision, "default", v1.ObjectName(current))
		return err == nil && rev.(*v1.Revision).Spec.Image == img
	}, 30*time.Second, 50*time.Millisecond, "%s's current revision holds %s", name, img)
	return current
}

// waitRun waits until run ends in phase and, if it does not, reports the run's and flow-b's status.
func (e *revisionEnv) waitRun(t *testing.T, run string, phase v1.RunPhase) {
	t.Helper()
	ok := assertEventually(func() bool { return getRun(t, e.c, run).Status.Phase == phase }, 60*time.Second)
	if !ok {
		rs, _ := json.Marshal(getRun(t, e.c, run).Status)
		fs, _ := json.Marshal(e.function(t, "flow-b").Status)
		t.Fatalf("%s did not end %s: run status %s; flow-b status %s", run, phase, rs, fs)
	}
}

func assertEventually(cond func() bool, within time.Duration) bool {
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// waitReleased waits until the revision name a run woke reads Idle in its own status: the run ended, so its pin no
// longer holds the revision (ADR-0190 Decision 10).
func (e *revisionEnv) waitReleased(t *testing.T, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(context.Background(), v1.KindRevision, "default", v1.ObjectName(name))
		return err == nil && obj.(*v1.Revision).Status.Phase == v1.PhaseIdle
	}, 30*time.Second, 20*time.Millisecond, "the held revision %s is released once its run ends", name)
}

// scenario: broken-edit-spares-old-run (ADR-0190) — r1 waits in a; b is edited to an image whose handler cannot load
// and flow-b turns Failed on r2's call; r1's b still runs v1 and r1 succeeds, r2 fails at b.
func TestScenarioBrokenEditSparesOldRun(t *testing.T) {
	e := newRevisionEnv(t)
	v1Img := e.bImage(t, "v1")
	writeStep(t, e.src, "bbroken", `throw new Error('b cannot load');
export const handle = async () => ({});`)
	broken := pushStepImage(t, e.layout, e.src, "bbroken")
	steps := e.idleFlowSteps(t, v1Img)
	e.applyIdleFlow(t, steps...)
	e.waitWorkflowReady(t)
	held := e.waitCurrent(t, "flow-b", v1Img)

	applyRun(t, e.c, "r1", "flow")
	e.waitBlocked(t)
	steps[1].Function.Image = broken
	e.applyIdleFlow(t, steps...)
	e.waitWorkflowReady(t)
	e.waitCurrent(t, "flow-b", broken)

	applyRun(t, e.c, "r2", "flow")
	e.waitRun(t, "r2", v1.RunFailed)
	require.Equal(t, v1.PhaseFailed, e.function(t, "flow-b").Status.Phase, "the broken edit fails flow-b")

	e.release(t)
	e.waitRun(t, "r1", v1.RunSucceeded)
	require.Equal(t, "v1\n", e.logged(t), "r1's b runs v1 beside the Failed current revision")
	require.Equal(t, v1.PhaseFailed, e.function(t, "flow-b").Status.Phase, "the held wake writes no Function phase")
	e.waitReleased(t, held)
}

// A pinned call to a held revision scaled to zero wakes that revision solo (ADR-0190 Decision 6): r1 is pinned to b v1,
// which never ran; b is edited to v2 and r2 wakes the Function on v2; then r1's b boots v1 beside v2 and succeeds.
func TestScenarioHeldRevisionWakesSolo(t *testing.T) {
	e := newRevisionEnv(t)
	v1Img, v2Img := e.bImage(t, "v1"), e.bImage(t, "v2")
	steps := e.idleFlowSteps(t, v1Img)
	e.applyIdleFlow(t, steps...)
	e.waitWorkflowReady(t)
	held := e.waitCurrent(t, "flow-b", v1Img)
	require.Equal(t, v1.PhaseIdle, e.function(t, "flow-b").Status.Phase, "flow-b is scaled to zero")

	applyRun(t, e.c, "r1", "flow")
	e.waitBlocked(t)
	steps[1].Function.Image = v2Img
	e.applyIdleFlow(t, steps...)
	e.waitWorkflowReady(t)
	current := e.waitCurrent(t, "flow-b", v2Img)

	applyRun(t, e.c, "r2", "flow")
	e.waitRun(t, "r2", v1.RunSucceeded)
	require.Equal(t, "v2\n", e.logged(t))

	e.release(t)
	e.waitRun(t, "r1", v1.RunSucceeded)
	require.Equal(t, "v2\nv1\n", e.logged(t), "r1's b wakes and runs v1")
	fn := e.function(t, "flow-b")
	require.Equal(t, current, fn.Status.ServingRevision, "the current revision keeps serving")
	e.waitReleased(t, held)
}
