//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/pdata/ptrace"

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

// scenario (e2e): one-run-one-trace (ADR-0102) — a REAL 2-step workflow run over the shim platform.
// The engine mints one W3C trace context per run and stamps `traceparent` on every step dispatch; each
// step-function invocation (F51/ADR-0101) adopts it. So every step's captured SERVER span in blob shares
// the SAME trace-id, parented on the run root — the run is one trace. Process driver → darwin-runnable;
// the containerd/UDS variant runs on the Lima workflow Venom lane.
func TestScenarioE2EWorkflowOneRunOneTrace(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the workflow-trace lane")
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
		funcd.WithRuntime(process.New()),
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

	// Two tiny step handlers: a root ingest → a dependent enrich (its input is ingest's output verbatim).
	src, layout := t.TempDir(), t.TempDir()
	writeStep(t, src, "wt-ingest", `export const handle = (ctx, e) => ({ n: e.data.n, ingested: true });`)
	writeStep(t, src, "wt-enrich", `export const handle = (ctx, e) => ({ n: e.data.n, enriched: e.data.n * 2 });`)
	ingestImg := pushStepImage(t, layout, src, "wt-ingest")
	enrichImg := pushStepImage(t, layout, src, "wt-enrich")

	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "traced", Namespace: "default", ResourceGroup: "rg1"},
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
	waitMaterializedReady(t, c, "traced-ingest", "traced-enrich")

	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "traced-01", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "traced", Input: json.RawMessage(`{"n":4}`)},
	}
	_, err = c.Apply(context.Background(), run)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return getRun(t, c, "traced-01").Status.Phase == "Succeeded"
	}, 30*time.Second, 100*time.Millisecond, "the 2-step run reaches Succeeded")

	// One run = one trace with a REAL root (ADR-0102 propagation + ADR-0103 run-root span): the engine
	// emits an INTERNAL run-root span (no parent) whose SpanID is what every step-function SERVER span
	// (F51) parents on — so the run is one trace, rooted by a run span carrying its total duration.
	// (The funclog age-flusher seals within ~500ms, so this retries.)
	require.Eventually(t, func() bool {
		traceIDs := map[string]struct{}{}
		var rootSpanID string
		haveRoot := false
		stepParents := map[string]struct{}{}
		steps := 0
		for _, sp := range readSpans(t, bucket) {
			traceIDs[sp.TraceID().String()] = struct{}{}
			switch {
			case sp.Kind() == ptrace.SpanKindInternal && sp.ParentSpanID().String() == "":
				rootSpanID, haveRoot = sp.SpanID().String(), true // the run-root span (ADR-0103)
			case sp.Kind() == ptrace.SpanKindServer:
				stepParents[sp.ParentSpanID().String()] = struct{}{} // a step invocation span (F51)
				steps++
			}
		}
		_, rootStepPresent := stepParents[rootSpanID] // the root step parents on the run root
		nested := false                               // ADR-0105: a downstream step nests under another step (not flat)
		for p := range stepParents {
			if p != rootSpanID && p != "" {
				nested = true
			}
		}
		// one trace · a run-root span · ≥2 step spans · the root step parents on the run root · and a
		// downstream step nests under a predecessor step (ADR-0105 DAG parenting, not the flat run-root model).
		return len(traceIDs) == 1 && haveRoot && steps >= 2 && rootStepPresent && nested
	}, 15*time.Second, 300*time.Millisecond, "the run-root roots one trace and the step spans nest along their DAG edges")
}
