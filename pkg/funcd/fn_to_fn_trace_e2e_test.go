//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge/observ"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// The ADR-0165 fn-to-fn trace: front calls greeter through context.invoke; greeter logs a "greeting" line.
const (
	callerTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpan  = "00f067aa0ba902b7"
)

type traceSources struct{ front, frontNoLink, greeter string }

// traceSrc returns the sources for runtime (nodejs22 or python314).
func traceSrc(runtime string) traceSources {
	if runtime == "python314" {
		return traceSources{
			front:       "def handle(ctx, event):\n    return ctx.invoke(\"greeter\", {\"name\": \"x\"})\n",
			frontNoLink: "def handle(ctx, event):\n    return ctx.invoke(\"nolink\", {\"name\": \"x\"})\n",
			greeter:     "def handle(ctx, event):\n    print(\"greeting \" + event[\"data\"][\"name\"], flush=True)\n    return {\"greeting\": \"hello \" + event[\"data\"][\"name\"]}\n",
		}
	}
	return traceSources{
		front:       "export async function handle(ctx) { return await ctx.invoke('greeter', { name: 'x' }); }\n",
		frontNoLink: "export async function handle(ctx) { return await ctx.invoke('nolink', { name: 'x' }); }\n",
		greeter:     "export function handle(ctx, e) { console.log('greeting ' + e.data.name); return { greeting: 'hello ' + e.data.name }; }\n",
	}
}

func traceFn(runtime, src string) shimFn {
	if runtime == "python314" {
		return pythonFn(src)
	}
	return nodeFn(src)
}

func linkGreeter(f shimFn) shimFn {
	f.links = []v1.FunctionLink{{Alias: "greeter", Target: "greeter"}}
	return f
}

// postTraced invokes a function over the data plane with a W3C traceparent.
func (h *shimRig) postTraced(t *testing.T, name, body, tp string) reply {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.dp+"/function/"+name, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("traceparent", tp)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, ctype: resp.Header.Get("Content-Type"), body: string(b)}
}

// traceSpans waits until trace holds want spans, then returns them.
func traceSpans(t *testing.T, h *shimRig, trace string, want int) []ptrace.Span {
	t.Helper()
	var got []ptrace.Span
	require.Eventually(t, func() bool {
		got = got[:0]
		for _, sp := range readSpans(t, h.bucket) {
			if sp.TraceID().String() == trace {
				got = append(got, sp)
			}
		}
		return len(got) >= want
	}, 15*time.Second, 300*time.Millisecond, "trace %s holds %d spans", trace, want)
	return got
}

func spanNamed(t *testing.T, spans []ptrace.Span, kind ptrace.SpanKind, name string) ptrace.Span {
	t.Helper()
	for _, sp := range spans {
		if sp.Kind() == kind && sp.Name() == name {
			return sp
		}
	}
	require.FailNow(t, "no span", "kind %s name %q", kind, name)
	return ptrace.Span{}
}

// scenario: fn-to-fn-joins-one-trace — front's SERVER span (parent P), the CLIENT span "call greeter" under it,
// and greeter's SERVER span under the CLIENT span share trace T; the CLIENT span contains greeter's to within
// 1 ms. Solo and pooled front, Node and Python.
func TestScenarioFnToFnJoinsOneTrace(t *testing.T) {
	for _, tc := range []struct {
		name, runtime string
		pooled        bool
	}{
		{"nodejs22", "nodejs22", false},
		{"python314", "python314", false},
		{"nodejs22-pooled", "nodejs22", true},
		{"python314-pooled", "python314", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			python := ""
			if tc.runtime == "python314" {
				python = requirePython(t, tc.pooled)
			}
			h := newShimRig(t, python)
			src := traceSrc(tc.runtime)
			front := linkGreeter(traceFn(tc.runtime, src.front))
			if tc.pooled {
				front = front.pooled("traced")
			}
			h.deploy(t, "greeter", traceFn(tc.runtime, src.greeter))
			h.deploy(t, "front", front)
			waitReady(t, h.c, "greeter", "front")

			got := h.postTraced(t, "front", `{"data":{}}`, "00-"+callerTrace+"-"+callerSpan+"-01")
			require.Equal(t, http.StatusOK, got.status, got.body)
			require.Contains(t, got.body, "hello x")

			spans := traceSpans(t, h, callerTrace, 3)
			require.Len(t, spans, 3)
			frontSpan := spanNamed(t, spans, ptrace.SpanKindServer, "front")
			client := spanNamed(t, spans, ptrace.SpanKindClient, "call greeter")
			greeter := spanNamed(t, spans, ptrace.SpanKindServer, "greeter")
			assert.Equal(t, callerSpan, frontSpan.ParentSpanID().String())
			assert.Equal(t, frontSpan.SpanID(), client.ParentSpanID())
			assert.Equal(t, client.SpanID(), greeter.ParentSpanID())
			assert.Equal(t, ptrace.StatusCodeOk, client.Status().Code())
			code, ok := client.Attributes().Get("http.status_code")
			require.True(t, ok)
			assert.Equal(t, "200", code.AsString())
			// Decision 6: Node's ms epoch base lets the CLIENT span end up to 1 ms off its child.
			assert.LessOrEqual(t, int64(client.StartTimestamp()), int64(greeter.StartTimestamp())+int64(time.Millisecond))
			assert.GreaterOrEqual(t, int64(client.EndTimestamp())+int64(time.Millisecond), int64(greeter.EndTimestamp()))
		})
	}
}

// scenario: failed-call-error-client-span — a call that fails before greeter's handler runs (its input contract
// rejects it, 422; or an undeclared alias, 403) leaves an ERROR CLIENT span with a message and the status code,
// and no greeter span.
func TestScenarioFailedCallErrorClientSpan(t *testing.T) {
	src := traceSrc("nodejs22")
	for _, tc := range []struct {
		name, alias, status, trace string
		front                      shimFn
	}{
		{"422 input contract", "greeter", "422", "5bf92f3577b34da6a3ce929d0e0e4736", linkGreeter(nodeFn(
			"export async function handle(ctx) { return await ctx.invoke('greeter', { wrong: 1 }); }\n"))},
		{"403 undeclared alias", "nolink", "403", "6bf92f3577b34da6a3ce929d0e0e4736", linkGreeter(nodeFn(src.frontNoLink))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newShimRig(t, "")
			h.deploy(t, "greeter", nodeFn(src.greeter).withContract(
				`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`, ""))
			h.deploy(t, "front", tc.front)
			waitReady(t, h.c, "greeter", "front")

			got := h.postTraced(t, "front", `{"data":{}}`, "00-"+tc.trace+"-"+callerSpan+"-01")
			require.Equal(t, http.StatusInternalServerError, got.status, got.body)

			spans := traceSpans(t, h, tc.trace, 2)
			client := spanNamed(t, spans, ptrace.SpanKindClient, "call "+tc.alias)
			assert.Equal(t, ptrace.StatusCodeError, client.Status().Code())
			assert.Contains(t, client.Status().Message(), "failed: "+tc.status)
			code, ok := client.Attributes().Get("http.status_code")
			require.True(t, ok)
			assert.Equal(t, tc.status, code.AsString())
			for _, sp := range spans {
				assert.NotEqual(t, "greeter", sp.Name(), "greeter's handler never ran")
			}
		})
	}
}

// scenario: workflow-logs-include-callee — a workflow step runs front, which calls greeter; the run's logs
// (`funcdctl workflow logs <run>`) include greeter's "greeting" line, since greeter now joins the run's trace.
func TestScenarioWorkflowLogsIncludeCallee(t *testing.T) {
	h := newShimRig(t, "")
	src := traceSrc("nodejs22")
	h.deploy(t, "greeter", nodeFn(src.greeter))
	h.deploy(t, "front", linkGreeter(nodeFn(src.front)))
	waitReady(t, h.c, "greeter", "front")

	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "greet", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Steps: []v1.WorkflowStep{{Name: "call", Function: &v1.FunctionStep{Ref: "front"}}},
		},
	}
	_, err := h.c.Apply(context.Background(), wf)
	require.NoError(t, err)
	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "greet-01", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "greet", Input: json.RawMessage(`{}`)},
	}
	_, err = h.c.Apply(context.Background(), run)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return getRun(t, h.c, "greet-01").Status.Phase == "Succeeded"
	}, 30*time.Second, 100*time.Millisecond, "the run reaches Succeeded")

	require.Eventually(t, func() bool {
		lines, lerr := h.c.RunLogs(context.Background(), "default", "greet-01", sdk.LogsOptions{})
		if lerr != nil {
			return false
		}
		for _, l := range lines {
			if l.Function == "greeter" && strings.Contains(l.Body, "greeting x") {
				return true
			}
		}
		return false
	}, 15*time.Second, 300*time.Millisecond, "workflow logs include greeter's greeting line")
}

// edgeRequests keeps the "function" attribute of every "edge request" access-log line.
type edgeRequests struct {
	mu  *sync.Mutex
	fns *[]string
}

func (e edgeRequests) Enabled(context.Context, slog.Level) bool { return true }
func (e edgeRequests) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "edge request" {
		return nil
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "function" {
			e.mu.Lock()
			*e.fns = append(*e.fns, a.Value.String())
			e.mu.Unlock()
		}
		return true
	})
	return nil
}
func (e edgeRequests) WithAttrs([]slog.Attr) slog.Handler { return e }
func (e edgeRequests) WithGroup(string) slog.Handler      { return e }

func (e edgeRequests) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), *e.fns...)
}

// scenario: internal-call-skips-edge-observ — with edge metrics, access log and trace on, one external
// POST /function/front that calls greeter records one edge request, for front: one funcd.edge.requests count,
// one "edge request" line, one edge span; none names greeter.
func TestScenarioInternalCallSkipsEdgeObserv(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	spans := tracetest.NewSpanRecorder()
	tel := observability.NewFromProviders(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	logs := edgeRequests{mu: &sync.Mutex{}, fns: &[]string{}}
	c, dpURL := shimPlatformOCI(t,
		funcd.WithEdgeObservability(observ.Config{Metrics: true, AccessLog: true, Trace: true}),
		funcd.WithTelemetry(tel), funcd.WithLogger(slog.New(logs)))
	exDir := tsExample(t, "fn-to-fn")
	layout := t.TempDir()
	gRef, gDig := pushExampleFn(t, layout, exDir, "greeter")
	fRef, fDig := pushExampleFn(t, layout, exDir, "front")
	applyFnObj(t, c, loadFn(t, "greeter.yaml", gRef, gDig))
	applyFnObj(t, c, loadFn(t, "front.yaml", fRef, fDig))
	waitReady(t, c, "greeter", "front")

	resp, err := http.Post(dpURL+"/function/front", "application/json", strings.NewReader(`{"data":{"name":"funcd"}}`))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "front invoked greeter: %s", body)

	edgeSpanFns := func() []string {
		var fns []string
		for _, s := range spans.Ended() {
			if s.InstrumentationScope().Name != "funcd.edge" {
				continue
			}
			for _, a := range s.Attributes() {
				if a.Key == "function" {
					fns = append(fns, a.Value.AsString())
				}
			}
		}
		return fns
	}
	edgeCounts := func() map[string]int64 {
		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &rm))
		out := map[string]int64{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if sum, ok := m.Data.(metricdata.Sum[int64]); ok && m.Name == "funcd.edge.requests" {
					for _, dp := range sum.DataPoints {
						fn, _ := dp.Attributes.Value("function")
						out[fn.AsString()] += dp.Value
					}
				}
			}
		}
		return out
	}
	require.Eventually(t, func() bool {
		return len(edgeSpanFns()) > 0 && len(logs.list()) > 0 && len(edgeCounts()) > 0
	}, 10*time.Second, 100*time.Millisecond, "the edge records the external request")
	assert.Equal(t, []string{"front"}, edgeSpanFns(), "one edge span, for front")
	assert.Equal(t, []string{"front"}, logs.list(), "one edge request line, for front")
	assert.Equal(t, map[string]int64{"front": 1}, edgeCounts(), "one funcd.edge.requests count, for front")
}
