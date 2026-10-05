//go:build e2e

package funcd_test

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
)

func (e *revisionEnv) applyPooledFlow(t *testing.T, steps ...v1.WorkflowStep) {
	t.Helper()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "flow", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowSpec{Pooling: v1.WorkflowPooling{Mode: v1.PoolingShared, MinReplicas: 1}, Steps: steps},
	}
	_, err := e.c.Apply(context.Background(), wf)
	require.NoError(t, err)
}

// bPoolImage pushes a b step that appends version, and whether a pool host or a solo worker ran it, to the log.
func (e *revisionEnv) bPoolImage(t *testing.T, version string) string {
	t.Helper()
	name := "bpool" + version
	writeStep(t, e.src, name, fmt.Sprintf(`import { appendFileSync } from 'node:fs';
export const handle = async () => {
  appendFileSync(%q, %q + (process.env.FUNCD_POOL_MANIFEST ? ' pool' : ' solo') + '\n');
  return { v: %q };
};`, e.log, version, version))
	return pushStepImage(t, e.layout, e.src, name)
}

// scenario: pooled-edit-isolated (ADR-0190 Decision 8) — flow's steps share one pool worker and r1's call to a is in
// flight in it; when b is edited, the pool rebuild drains, so r1's call to a completes, and r1's b runs v1 in a solo
// worker beside the rebuilt pool.
func TestScenarioPooledEditIsolated(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the pooling lane")
	}
	e := newRevisionEnv(t, funcd.WithPoolShim(node, langmod.PoolShim(t)))
	v1Img, v2Img := e.bPoolImage(t, "v1"), e.bPoolImage(t, "v2")
	steps := e.idleFlowSteps(t, v1Img)
	e.applyPooledFlow(t, steps...)
	e.waitWorkflowReady(t)
	e.waitServing(t, "flow-b", v1Img)
	held := e.function(t, "flow-b").Status.CurrentRevision
	require.NotEmpty(t, e.function(t, "flow-a").Status.Pool, "the steps are pooled")

	applyRun(t, e.c, "r1", "flow")
	e.waitBlocked(t)
	steps[1].Function.Image = v2Img
	e.applyPooledFlow(t, steps...)
	e.waitWorkflowReady(t)
	e.waitServing(t, "flow-b", v2Img)

	e.release(t)
	e.waitRun(t, "r1", v1.RunSucceeded)
	require.Equal(t, "v1 solo\n", e.logged(t), "r1's b runs v1 in a solo worker")
	require.Contains(t, runStepRevision(getRun(t, e.c, "r1"), "b"), v1Img)
	obj, err := e.c.Get(context.Background(), v1.KindRevision, "default", v1.ObjectName(held))
	require.NoError(t, err)
	require.NotEmpty(t, obj.(*v1.Revision).Status.Phase, "r1's b woke v1 as a held revision, solo")

	applyRun(t, e.c, "r2", "flow")
	e.waitRun(t, "r2", v1.RunSucceeded)
	require.Equal(t, "v1 solo\nv2 pool\n", e.logged(t), "a run started after the edit runs v2 in the pool")
	require.Eventually(t, func() bool { return e.function(t, "flow-a").Status.Phase == v1.PhaseReady },
		10*time.Second, 50*time.Millisecond, "flow-a keeps serving from the rebuilt pool")
}
