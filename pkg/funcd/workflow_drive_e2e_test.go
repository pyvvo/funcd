//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// applyRun applies the WorkflowRun name of workflow wf with an empty input.
func applyRun(t *testing.T, c *sdk.Client, name, wf string) {
	t.Helper()
	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: v1.ObjectName(wf), Input: json.RawMessage(`{}`)},
	}
	_, err := c.Apply(context.Background(), run)
	require.NoError(t, err)
}

// scenario: running-step-does-not-block-other-kinds (ADR-0146) — the run executes on its own goroutine, so
// the controller worker reconciles a Function applied while a 6 s step runs.
func TestIssue17_RunningStepDoesNotBlockOtherKinds(t *testing.T) {
	c, _ := shimPlatformOCI(t)
	src, layout := t.TempDir(), t.TempDir()
	writeStep(t, src, "slow", `export const handle = async (ctx, e) => { await new Promise((r) => setTimeout(r, 6000)); return { done: true }; };`)
	writeStep(t, src, "echo", `export const handle = async (ctx, e) => e;`)
	slow := pushStepImage(t, layout, src, "slow")
	echo := pushStepImage(t, layout, src, "echo")
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "busy", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated, MinReplicas: 1},
			Steps:   []v1.WorkflowStep{{Name: "slow", Function: &v1.FunctionStep{Image: slow, Timeout: 60 * time.Second}}},
		},
	}
	_, err := c.Apply(context.Background(), wf)
	require.NoError(t, err)
	waitMaterializedReady(t, c, "busy-slow")
	applyRun(t, c, "busy-1", "busy")
	require.Eventually(t, func() bool { return runStepPhase(getRun(t, c, "busy-1"), "slow") == v1.StepRunning },
		10*time.Second, 20*time.Millisecond, "the 6 s step runs")

	fn := &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "echo", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: echo, Scaling: v1.Scaling{MinReplicas: 1}},
	}
	_, err = c.Apply(context.Background(), fn)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return phaseOf(t, c, "echo") == v1.PhaseReady },
		time.Second, 20*time.Millisecond, "a Function applied while a step runs is Ready within 1 s")
	require.Equal(t, v1.StepRunning, runStepPhase(getRun(t, c, "busy-1"), "slow"), "the step still runs")
	require.Eventually(t, func() bool { return getRun(t, c, "busy-1").Status.Phase == "Succeeded" },
		30*time.Second, 100*time.Millisecond, "the run Succeeds")
}

// scenario: cold-step-wakes-and-succeeds (ADR-0146) — a scaled-to-zero step's Wake no longer waits behind the
// run on the controller worker.
func TestIssue26_ColdStepWakesAndSucceeds(t *testing.T) {
	c, _ := shimPlatformOCI(t)
	src, layout := t.TempDir(), t.TempDir()
	writeStep(t, src, "echo", `export const handle = async (ctx, e) => e;`)
	img := pushStepImage(t, layout, src, "echo")
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "cold", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated, MinReplicas: 0},
			Steps:   []v1.WorkflowStep{{Name: "echo", Function: &v1.FunctionStep{Image: img}}},
		},
	}
	_, err := c.Apply(context.Background(), wf)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		obj, err := c.Get(context.Background(), v1.KindFunction, "default", "cold-echo")
		return err == nil && obj.(*v1.Function).Status.Phase == v1.PhaseIdle
	}, 30*time.Second, 100*time.Millisecond, "the step Function settles Idle")

	applyRun(t, c, "cold-1", "cold")
	require.Eventually(t, func() bool { return getRun(t, c, "cold-1").Status.Phase == "Succeeded" },
		10*time.Second, 50*time.Millisecond, "the cold step wakes and the run Succeeds within 10 s")
}
