package funcd_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/bus/nats"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// scenario (e2e): run-replay (ADR-0107) — a REAL 2-step run a→b over the shim platform. A replay --from b
// creates a NEW run that re-runs b while reusing a's recorded output. Proof it reused a (did not re-invoke
// it): each step logs, and the run-scoped read (ADR-0106) over the REPLAY's trace contains b's log line but
// NOT a's — a ran only under the source's trace. Process driver → darwin-runnable; the containerd variant
// is the workflow Venom lane.
func TestScenarioE2EWorkflowReplay(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH; skipping the replay lane")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	shim := filepath.Join(root, "shim", "nodejs", "shim.mjs")
	if _, serr := os.Stat(shim); serr != nil {
		t.Skipf("node shim not built at %s (run: just build-shim)", shim)
	}

	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	p, err := funcd.New(
		funcd.WithBlob(bucket), funcd.WithBus(messaging),
		funcd.WithStore(store.New(memory.New())), funcd.WithRuntime(process.New()),
		funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"), funcd.WithDevAuth(funcd.DevToken, "default"),
		funcd.WithRuntimeShim("node", shim), funcd.WithArtifactStore(t.TempDir()),
		funcd.WithFunclog(500*time.Millisecond, 0),
	)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)

	src, layout := t.TempDir(), t.TempDir()
	writeStep(t, src, "rp-a", `export const handle = (ctx, e) => { console.log("A_RAN n=" + e.data.n); return { n: e.data.n }; };`)
	writeStep(t, src, "rp-b", `export const handle = (ctx, e) => { console.log("B_RAN n=" + e.data.n); return { doubled: e.data.n * 2 }; };`)
	aImg := pushStepImage(t, layout, src, "rp-a")
	bImg := pushStepImage(t, layout, src, "rp-b")

	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "rp", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated, MinReplicas: 1},
			Steps: []v1.WorkflowStep{
				{Name: "a", Function: &v1.FunctionStep{Image: aImg}},
				{Name: "b", Function: &v1.FunctionStep{Image: bImg}, DependsOn: []v1.ObjectName{"a"}},
			},
		},
	}
	_, err = c.Apply(context.Background(), wf)
	require.NoError(t, err)
	waitMaterializedReady(t, c, "rp-a", "rp-b")

	// Source run → Succeeded.
	srcRun := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "rp-src", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "rp", Input: json.RawMessage(`{"n":4}`)},
	}
	_, err = c.Apply(context.Background(), srcRun)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return getRun(t, c, "rp-src").Status.Phase == "Succeeded" }, 30*time.Second, 100*time.Millisecond, "source run succeeds")
	srcTrace := getRun(t, c, "rp-src").Status.TraceID
	require.NotEmpty(t, srcTrace)

	// Replay --from b: a new run that re-runs b and reuses a's output.
	replay := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "rp-rep", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "rp", Replay: &v1.ReplaySeed{Run: "rp-src", From: "b"}},
	}
	_, err = c.Apply(context.Background(), replay)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return getRun(t, c, "rp-rep").Status.Phase == "Succeeded" }, 30*time.Second, 100*time.Millisecond, "replay run succeeds")

	rep := getRun(t, c, "rp-rep")
	require.NotEmpty(t, rep.Status.TraceID)
	require.NotEqual(t, srcTrace, rep.Status.TraceID, "the replay must mint its own trace (one run = one trace)")

	// The run-scoped logs over the REPLAY's trace contain b (re-ran) but NOT a (copied, never re-invoked).
	require.Eventually(t, func() bool {
		lines, lerr := c.RunLogs(context.Background(), "default", "rp-rep", sdk.LogsOptions{})
		if lerr != nil || len(lines) == 0 {
			return false
		}
		var body strings.Builder
		for _, l := range lines {
			require.Equal(t, rep.Status.TraceID, l.TraceID, "a foreign trace leaked into the replay read")
			body.WriteString(l.Body)
			body.WriteString("\n")
		}
		return strings.Contains(body.String(), "B_RAN") && !strings.Contains(body.String(), "A_RAN")
	}, 15*time.Second, 300*time.Millisecond, "replay logs show b re-ran and a was reused (not re-invoked)")
}
