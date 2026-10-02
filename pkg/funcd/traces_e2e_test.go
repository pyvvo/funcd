//go:build e2e

package funcd_test

import (
	"context"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/blob"
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

// scenario (e2e): funclog-captures-spans (ADR-0101) — a REAL Node function invocation produces one
// auto SERVER span, captured over the SAME Path B fd-3 side channel as logs, demuxed by "funcd.signal",
// and persisted as OTLP-trace-JSONL under traces/. Invoked WITH a W3C traceparent, the span adopts the
// caller trace (adopt-traceparent) and the invocation's console logs carry the SAME trace id
// (logs-correlated-to-span — the ADR-0081 gap closed). Process driver → darwin-runnable; the
// containerd/UDS variant runs on the Lima Venom lane.
func TestScenarioE2EFunclogCapturesSpans(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the traces capture lane")
	}
	shim := langmod.NodeShim(t)

	exDir := tsExample(t, "log-burst")

	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)

	layout := t.TempDir()
	ref := "oci-layout://" + layout + ":log-burst"
	digest, err := artifact.Push(context.Background(), ref, filepath.Join(exDir, "burst.mjs"), nil, "", "")
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
	dpURL := "http://" + p.DataPlaneAddr()

	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "log-burst", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Image, fn.Spec.ImageDigest = ref, digest
	fn.Spec.Replicas = 1
	fn.Spec.Scaling.MinReplicas = 1
	_, err = c.Apply(context.Background(), fn)
	require.NoError(t, err)
	waitReady(t, c, "log-burst")

	// Invoke WITH a W3C traceparent so the span must adopt the caller trace + parent.
	const traceID = "11111111111111111111111111111111"
	const callerSpan = "2222222222222222"
	req, err := http.NewRequest(http.MethodPost, dpURL+"/function/log-burst", strings.NewReader(`{"data":{}}`))
	require.NoError(t, err)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("traceparent", "00-"+traceID+"-"+callerSpan+"-01")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "invoke log-burst: %s", body)

	// The invocation span lands in blob as OTLP-trace-JSONL: it adopts the incoming trace + parent.
	require.Eventually(t, func() bool {
		for _, sp := range readSpans(t, bucket) {
			if sp.TraceID().String() == traceID && sp.ParentSpanID().String() == callerSpan &&
				sp.Kind() == ptrace.SpanKindServer {
				return true
			}
		}
		return false
	}, 15*time.Second, 300*time.Millisecond, "the invocation SERVER span adopts the incoming traceparent")

	// logs-correlated-to-span: the burst's console logs carry the SAME trace id as the span.
	require.Eventually(t, func() bool {
		for _, tid := range logTraceIDs(t, bucket) {
			if tid == traceID {
				return true
			}
		}
		return false
	}, 15*time.Second, 300*time.Millisecond, "captured logs carry the invocation's trace id (correlation)")
}

// readSpans collects every span across the OTLP-trace-JSONL objects under traces/.
func readSpans(t *testing.T, b blob.Bucket) []ptrace.Span {
	t.Helper()
	objs, err := b.List(context.Background(), "traces/")
	require.NoError(t, err)
	var u ptrace.JSONUnmarshaler
	var out []ptrace.Span
	for _, o := range objs {
		data, gerr := b.Get(context.Background(), o.Key)
		require.NoError(t, gerr)
		tr, perr := u.UnmarshalTraces([]byte(strings.TrimSpace(string(data))))
		require.NoError(t, perr)
		for i := 0; i < tr.ResourceSpans().Len(); i++ {
			ss := tr.ResourceSpans().At(i).ScopeSpans()
			for j := 0; j < ss.Len(); j++ {
				spans := ss.At(j).Spans()
				for k := 0; k < spans.Len(); k++ {
					out = append(out, spans.At(k))
				}
			}
		}
	}
	return out
}

// logTraceIDs collects the non-empty TraceIDs stamped on captured log records under logs/.
func logTraceIDs(t *testing.T, b blob.Bucket) []string {
	t.Helper()
	objs, err := b.List(context.Background(), "logs/")
	require.NoError(t, err)
	var u plog.JSONUnmarshaler
	var out []string
	for _, o := range objs {
		data, gerr := b.Get(context.Background(), o.Key)
		require.NoError(t, gerr)
		logs, perr := u.UnmarshalLogs([]byte(strings.TrimSpace(string(data))))
		require.NoError(t, perr)
		for i := 0; i < logs.ResourceLogs().Len(); i++ {
			sl := logs.ResourceLogs().At(i).ScopeLogs()
			for j := 0; j < sl.Len(); j++ {
				lr := sl.At(j).LogRecords()
				for k := 0; k < lr.Len(); k++ {
					if tid := lr.At(k).TraceID().String(); tid != "" {
						out = append(out, tid)
					}
				}
			}
		}
	}
	return out
}
