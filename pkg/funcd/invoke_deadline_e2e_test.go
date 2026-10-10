//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The ADR-0151 legs on the real shims. A 32 s handler outlives the pool's fixed 30 s timer, which the pool now
// replaces with funcd's X-Funcd-Timeout-Ms plus 1 s. Each parallel test has its own pool worker name: the pool
// manifest file is per host, keyed by namespace and worker.
const (
	neverSettles = "export function handle() { return new Promise(() => {}); }\n"
	after32s     = "export async function handle() { await new Promise((r) => setTimeout(r, 32000)); return { done: true }; }\n"
)

// requireDeadline504 checks funcd's 504 came at the limit, before a pooled worker's own timer (limit + 1 s).
func requireDeadline504(t *testing.T, r reply, elapsed, limit time.Duration) {
	t.Helper()
	require.Equal(t, http.StatusGatewayTimeout, r.status, r.body)
	var p struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.body), &p), r.body)
	require.Equal(t, "urn:funcd:problem:deadline-exceeded", p.Type)
	require.GreaterOrEqual(t, elapsed, limit, "not before the limit")
	require.Less(t, elapsed, limit+time.Second, "before the pool's 503")
}

func (h *shimRig) timedPost(name string) (reply, time.Duration) {
	start := time.Now()
	r := h.postWithin(name, `{}`, 10*time.Second)
	return r, time.Since(start)
}

// scenario: hung-handler-cut-at-default — a never-settling handler without spec.timeout ⇒ 504 at the default.
func TestScenarioHungHandlerCutAtDefault(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "", funcd.WithDefaultInvokeTimeout(2*time.Second))
	h.deploy(t, "hung", nodeFn(neverSettles))
	waitReady(t, h.c, "hung")
	r, took := h.timedPost("hung")
	requireDeadline504(t, r, took, 2*time.Second)
}

// scenario: pooled-node-follows-limit — a pooled call follows spec.timeout: 32 s at 40 s ⇒ 200; a never-settling
// one at 2 s ⇒ funcd's 504.
func TestScenarioPooledNodeFollowsLimit(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deploy(t, "pslow", nodeFn(after32s).pooled("w151p").withTimeout(40*time.Second))
	h.deploy(t, "phung", nodeFn(neverSettles).pooled("w151p").withTimeout(2*time.Second))
	waitReady(t, h.c, "pslow", "phung")

	slow := make(chan reply, 1)
	go func() { slow <- h.postWithin("pslow", `{}`, time.Minute) }()
	r, took := h.timedPost("phung")
	requireDeadline504(t, r, took, 2*time.Second)
	got := <-slow
	require.Equal(t, http.StatusOK, got.status, got.body)
	require.JSONEq(t, `{"done":true}`, got.body)
}

// scenario: link-keeps-own-limit — a link of 40 s to a pooled callee answering after 32 s ⇒ the callee's output.
func TestScenarioLinkKeepsOwnLimit(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deploy(t, "lcallee", nodeFn(after32s).pooled("w151l"))
	caller := nodeFn("export async function handle(ctx) { return await ctx.invoke('callee', {}); }\n")
	caller.links = []v1.FunctionLink{{Alias: "callee", Target: "lcallee", Timeout: v1.Duration(40 * time.Second)}}
	h.deploy(t, "lcaller", caller)
	waitReady(t, h.c, "lcallee", "lcaller")

	got := h.postWithin("lcaller", `{}`, time.Minute)
	require.Equal(t, http.StatusOK, got.status, got.body)
	require.JSONEq(t, `{"done":true}`, got.body)
}

// scenario: step-keeps-own-limit — at D = 1 s, a 5 s step answering after 2 s and a shared-pool 40 s step
// answering after 32 s both succeed.
func TestScenarioStepKeepsOwnLimit(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "", funcd.WithDefaultInvokeTimeout(time.Second))
	src, layout := t.TempDir(), t.TempDir()
	writeStep(t, src, "brief", "export async function handle() { await new Promise((r) => setTimeout(r, 2000)); return {}; }\n")
	writeStep(t, src, "long", after32s)
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "limits", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingShared, MinReplicas: 1},
			Steps: []v1.WorkflowStep{
				{Name: "start", Builtin: &v1.BuiltinStep{Pass: `${{ input }}`}},
				{Name: "brief", Function: &v1.FunctionStep{Image: pushStepImage(t, layout, src, "brief"), Timeout: v1.Duration(5 * time.Second)}, DependsOn: []v1.ObjectName{"start"}},
				{Name: "long", Function: &v1.FunctionStep{Image: pushStepImage(t, layout, src, "long"), Timeout: v1.Duration(40 * time.Second)}, DependsOn: []v1.ObjectName{"start"}},
			},
		},
	}
	_, err := h.c.Apply(context.Background(), wf)
	require.NoError(t, err)
	waitMaterializedReady(t, h.c, "limits-brief", "limits-long")

	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "limits-1", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "limits", Input: json.RawMessage(`{}`)},
	}
	_, err = h.c.Apply(context.Background(), run)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		p := getRun(t, h.c, "limits-1").Status.Phase
		return p == "Succeeded" || p == "Failed"
	}, time.Minute, 200*time.Millisecond, "the run reaches a terminal phase")
	got := getRun(t, h.c, "limits-1")
	require.Equal(t, "Succeeded", string(got.Status.Phase), "steps: %+v", got.Status.Steps)
}
