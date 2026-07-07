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
	"go.opentelemetry.io/collector/pdata/ptrace"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/bus/nats"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// scenario (e2e): dag-shaped-waterfall (ADR-0105) — a REAL fan-in workflow (a → {b, c} → merge) over the shim
// platform. The engine owns each step's span-id and the shim uses it, so the captured spans nest along the DAG
// edges: b and c parent on a; merge parents on b (the primary/first edge) and carries a span link to c. Process
// driver → darwin-runnable; the containerd variant runs on the Lima workflow Venom lane.
func TestScenarioE2EDagShapedWaterfall(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the DAG-trace lane")
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
	writeStep(t, src, "dg-a", `export const handle = (ctx, e) => ({ v: 1 });`)
	writeStep(t, src, "dg-b", `export const handle = (ctx, e) => ({ b: e.data.v });`)
	writeStep(t, src, "dg-c", `export const handle = (ctx, e) => ({ c: e.data.v });`)
	writeStep(t, src, "dg-merge", `export const handle = (ctx, e) => ({ done: true });`)
	img := map[string]string{}
	for _, s := range []string{"dg-a", "dg-b", "dg-c", "dg-merge"} {
		img[s] = pushStepImage(t, layout, src, s)
	}

	// a (root) → {b, c} (both depend on a) → merge (depends on b THEN c: b is the primary edge, c the link).
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "dagflow", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated, MinReplicas: 1},
			Steps: []v1.WorkflowStep{
				{Name: "a", Function: &v1.FunctionStep{Image: img["dg-a"]}},
				{Name: "b", Function: &v1.FunctionStep{Image: img["dg-b"]}, DependsOn: []v1.ObjectName{"a"}},
				{Name: "c", Function: &v1.FunctionStep{Image: img["dg-c"]}, DependsOn: []v1.ObjectName{"a"}},
				{Name: "merge", Function: &v1.FunctionStep{Image: img["dg-merge"]}, DependsOn: []v1.ObjectName{"b", "c"}},
			},
		},
	}
	_, err = c.Apply(ctx, wf)
	require.NoError(t, err)
	waitMaterializedReady(t, c, "dagflow-a", "dagflow-b", "dagflow-c", "dagflow-merge")

	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "dagrun-01", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "dagflow", Input: json.RawMessage(`{}`)},
	}
	_, err = c.Apply(ctx, run)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return getRun(t, c, "dagrun-01").Status.Phase == "Succeeded"
	}, 40*time.Second, 100*time.Millisecond, "the fan-in run reaches Succeeded")

	// The step spans nest along the DAG: b & c parent on a; merge parents on b (primary) and links c.
	require.Eventually(t, func() bool {
		byFn := spansByFunction(t, bucket)
		a := firstSpan(byFn["dagflow-a"])
		b := firstSpan(byFn["dagflow-b"])
		cc := firstSpan(byFn["dagflow-c"])
		m := firstSpan(byFn["dagflow-merge"])
		if a == nil || b == nil || cc == nil || m == nil {
			return false
		}
		// b and c nest under a
		if b.ParentSpanID().String() != a.SpanID().String() || cc.ParentSpanID().String() != a.SpanID().String() {
			return false
		}
		// merge parents on b (the primary/first edge) and links c
		if m.ParentSpanID().String() != b.SpanID().String() {
			return false
		}
		hasLinkToC := false
		for i := 0; i < m.Links().Len(); i++ {
			if m.Links().At(i).SpanID().String() == cc.SpanID().String() {
				hasLinkToC = true
			}
		}
		return hasLinkToC
	}, 15*time.Second, 300*time.Millisecond, "step spans nest along the DAG edges; fan-in merge links the non-primary predecessor")
}

// spansByFunction groups every captured span by its OTLP Resource "function" attribute (the step function name).
func spansByFunction(t *testing.T, b blob.Bucket) map[string][]ptrace.Span {
	t.Helper()
	objs, err := b.List(context.Background(), "traces/")
	require.NoError(t, err)
	var u ptrace.JSONUnmarshaler
	out := map[string][]ptrace.Span{}
	for _, o := range objs {
		data, gerr := b.Get(context.Background(), o.Key)
		require.NoError(t, gerr)
		tr, perr := u.UnmarshalTraces([]byte(strings.TrimSpace(string(data))))
		require.NoError(t, perr)
		for i := 0; i < tr.ResourceSpans().Len(); i++ {
			rs := tr.ResourceSpans().At(i)
			fn := ""
			if v, ok := rs.Resource().Attributes().Get("function"); ok {
				fn = v.Str()
			}
			ss := rs.ScopeSpans()
			for j := 0; j < ss.Len(); j++ {
				spans := ss.At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					out[fn] = append(out[fn], spans.At(k))
				}
			}
		}
	}
	return out
}

func firstSpan(spans []ptrace.Span) *ptrace.Span {
	if len(spans) == 0 {
		return nil
	}
	return &spans[0]
}
