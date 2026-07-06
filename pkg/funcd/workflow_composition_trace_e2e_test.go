package funcd_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"

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

// scenario (e2e): one-composition-one-trace (ADR-0104) — a REAL parent workflow with a sub-workflow step over
// the shim platform. The child run inherits the parent's trace (ADR-0104) and the engine emits its run-root
// span nested under the parent run span; the parent run-root span comes from the reconciler (ADR-0103). So the
// whole composition — parent run, parent steps, child run, child steps — is ONE nested OTel trace. Process
// driver → darwin-runnable; the containerd variant runs on the Lima workflow Venom lane.
func TestScenarioE2ECompositionOneTrace(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the composition-trace lane")
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
	ctx := context.Background()

	src, layout := t.TempDir(), t.TempDir()
	writeStep(t, src, "ct-double", `export const handle = (ctx, e) => ({ doubled: e.data.n * 2 });`)
	writeStep(t, src, "ct-seed", `export const handle = (ctx, e) => ({ n: e.data.n });`)
	writeStep(t, src, "ct-sink", `export const handle = (ctx, e) => ({ got: e.data.doubled });`)
	img := map[string]string{}
	for _, s := range []string{"ct-double", "ct-seed", "ct-sink"} {
		img[s] = pushStepImage(t, layout, src, s)
	}

	child := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "ctchild", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated, MinReplicas: 1},
			Steps:   []v1.WorkflowStep{{Name: "double", Function: &v1.FunctionStep{Image: img["ct-double"]}}},
		},
	}
	_, err = c.Apply(ctx, child)
	require.NoError(t, err)

	parent := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "ctparent", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated, MinReplicas: 1},
			Steps: []v1.WorkflowStep{
				{Name: "seed", Function: &v1.FunctionStep{Image: img["ct-seed"]}},
				{Name: "sub", Workflow: &v1.WorkflowRef{Ref: "ctchild"}, DependsOn: []v1.ObjectName{"seed"}},
				{Name: "sink", Function: &v1.FunctionStep{Image: img["ct-sink"]}, DependsOn: []v1.ObjectName{"sub"}},
			},
		},
	}
	_, err = c.Apply(ctx, parent)
	require.NoError(t, err)
	waitMaterializedReady(t, c, "ctchild-double", "ctparent-seed", "ctparent-sink")

	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "ctrun-01", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "ctparent", Input: json.RawMessage(`{"n":4}`)},
	}
	_, err = c.Apply(ctx, run)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return getRun(t, c, "ctrun-01").Status.Phase == "Succeeded"
	}, 40*time.Second, 100*time.Millisecond, "the parent run (with a sub-workflow) reaches Succeeded")

	// One composition = one trace: all spans share one trace-id, a parent run-root span (INTERNAL, no parent),
	// and a child run-root span (INTERNAL) nested under it (ParentID == the parent run-root's SpanID).
	require.Eventually(t, func() bool {
		traceIDs := map[string]struct{}{}
		var parentRootID string
		haveParentRoot, haveChildNested := false, false
		internal := 0
		for _, sp := range readSpans(t, bucket) {
			traceIDs[sp.TraceID().String()] = struct{}{}
			if sp.Kind() == ptrace.SpanKindInternal {
				internal++
				if sp.ParentSpanID().String() == "" {
					parentRootID, haveParentRoot = sp.SpanID().String(), true
				}
			}
		}
		for _, sp := range readSpans(t, bucket) {
			if sp.Kind() == ptrace.SpanKindInternal && sp.ParentSpanID().String() == parentRootID && parentRootID != "" {
				haveChildNested = true
			}
		}
		// one trace · two run-root spans (parent + child) · the child run-root nests under the parent run-root.
		return len(traceIDs) == 1 && internal >= 2 && haveParentRoot && haveChildNested
	}, 15*time.Second, 300*time.Millisecond, "the parent + child runs are one nested trace (composition)")
}
