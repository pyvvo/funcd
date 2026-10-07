//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/bus/nats"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario (e2e): run-scoped log read (ADR-0106) — a REAL 2-step workflow run whose step functions each
// console-log. Because the engine dispatches every step with the run's traceparent (ADR-0102) and the shim
// stamps that trace-id on captured console logs (F51/ADR-0101), `funcdctl workflow logs <run>` (via the
// SDK RunLogs) returns the run's lines ACROSS BOTH step functions in one namespace-wide, trace-filtered
// read — no per-function fan-out. `--severity error` filters, `--step` narrows. Process driver →
// darwin-runnable; the containerd/UDS variant runs on the workflow Venom lane.
func TestScenarioE2EWorkflowRunLogs(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the run-scoped log lane")
	}
	shim := langmod.NodeShim(t)

	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)

	p, err := funcd.New(
		funcd.WithBlob(bucket),
		funcd.WithBus(messaging),
		funcd.WithStore(store.New(memory.New())),
		funcd.WithRuntime(process.New(nil)),
		funcd.WithGateway(embedded.New()),
		funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"),
		funcd.WithDevAuth(funcd.DevToken, "default"),
		funcd.WithRuntimeShim(node, shim),
		funcd.WithArtifactStore(t.TempDir()),
		funcd.WithFunclog(500*time.Millisecond, 0),
	)
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-doneCh:
		case <-time.After(10 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)

	// Two step handlers that log: ingest emits an info line; enrich emits an info + an ERROR line.
	src, layout := t.TempDir(), t.TempDir()
	writeStep(t, src, "rl-ingest", `export const handle = (ctx, e) => { console.log("INGEST_MARK n=" + e.data.n); return { n: e.data.n, ingested: true }; };`)
	writeStep(t, src, "rl-enrich", `export const handle = (ctx, e) => { console.log("ENRICH_INFO"); console.error("ENRICH_ERR"); return { n: e.data.n, enriched: e.data.n * 2 }; };`)
	ingestImg := pushStepImage(t, layout, src, "rl-ingest")
	enrichImg := pushStepImage(t, layout, src, "rl-enrich")

	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "wlog", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated, MinReplicas: 1},
			Steps: []v1.WorkflowStep{
				{Name: "ingest", Function: &v1.FunctionStep{Image: ingestImg}},
				{Name: "enrich", Function: &v1.FunctionStep{Image: enrichImg}, DependsOn: []v1.ObjectName{"ingest"}},
			},
		},
	}
	_, err = c.Apply(context.Background(), wf)
	require.NoError(t, err)
	waitMaterializedReady(t, c, "wlog-ingest", "wlog-enrich")

	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "wlog-01", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "wlog", Input: json.RawMessage(`{"n":4}`)},
	}
	_, err = c.Apply(context.Background(), run)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return getRun(t, c, "wlog-01").Status.Phase == "Succeeded"
	}, 30*time.Second, 100*time.Millisecond, "the 2-step run reaches Succeeded")

	// ADR-0100: the run's trace-id is mirrored to status — the key the run-scoped read resolves.
	traceID := getRun(t, c, "wlog-01").Status.TraceID
	require.NotEmpty(t, traceID, "the run status must carry the mirrored trace-id (ADR-0100)")

	// The whole run's logs come back in one call, across BOTH step functions, all on the run's trace-id
	// (the funclog age-flusher seals within ~500ms, so this retries).
	require.Eventually(t, func() bool {
		lines, lerr := c.RunLogs(context.Background(), "default", "wlog-01", sdk.LogsOptions{})
		if lerr != nil {
			return false
		}
		fns := map[string]bool{}
		for _, l := range lines {
			if l.TraceID != traceID {
				return false // a foreign trace must never leak into a run-scoped read
			}
			fns[l.Function] = true
		}
		return fns["wlog-ingest"] && fns["wlog-enrich"] // one read spans both step functions
	}, 15*time.Second, 300*time.Millisecond, "workflow logs returns the run's lines across both step functions")

	// --severity error narrows to the error line(s) only — all from enrich.
	errLines, err := c.RunLogs(context.Background(), "default", "wlog-01", sdk.LogsOptions{Severity: "error"})
	require.NoError(t, err)
	require.NotEmpty(t, errLines, "the ERROR line must be returned under --severity error")
	for _, l := range errLines {
		require.Equal(t, traceID, l.TraceID)
		require.GreaterOrEqual(t, l.SeverityNumber, int32(17), "only error+ lines under --severity error")
	}

	// --step ingest narrows to that step's materialized function only.
	stepLines, err := c.RunLogs(context.Background(), "default", "wlog-01", sdk.LogsOptions{Step: "ingest"})
	require.NoError(t, err)
	require.NotEmpty(t, stepLines, "--step ingest must return ingest's lines")
	for _, l := range stepLines {
		require.Equal(t, "wlog-ingest", l.Function, "--step must narrow to the one step's function")
		require.Equal(t, traceID, l.TraceID)
	}
}
