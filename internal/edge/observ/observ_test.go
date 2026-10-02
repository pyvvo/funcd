package observ_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/pyvvo/funcd/internal/edge/observ"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/platform/observability"
)

func serve(t *testing.T, mw func(http.Handler) http.Handler, next http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, r)
	return rec
}

// scenario: disabled-passthrough
func TestScenarioDisabledPassthrough(t *testing.T) {
	called := false
	h := observ.Chain(observ.Config{}, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/y", nil))
	require.True(t, called, "zero Config is a pass-through")
}

// scenario: metrics-recorded + function-label-from-holder
func TestScenarioMetricsRecorded(t *testing.T) {
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	tel := observability.NewFromProviders(mp, nil)

	// next fills the Target holder as dataplane would.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tgt, ok := observ.TargetFrom(r.Context()); ok {
			tgt.Namespace, tgt.Function = "team", "orders"
		}
		w.WriteHeader(http.StatusOK)
	})
	mw := observ.Chain(observ.Config{Metrics: true}, tel, nil)
	serve(t, mw, next, httptest.NewRequest("GET", "http://x/orders", nil))

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	names := map[string]bool{}
	sawFunction := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			names[m.Name] = true
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, dp := range sum.DataPoints {
					if v, present := dp.Attributes.Value("function"); present && v.AsString() == "orders" {
						sawFunction = true
					}
				}
			}
		}
	}
	require.True(t, names["funcd.edge.requests"], "request counter emitted")
	require.True(t, names["funcd.edge.duration_ms"], "duration histogram emitted")
	require.True(t, sawFunction, "the function label came from the dataplane-filled Target holder (correct for a Route hit)")
}

// scenario: edge-span-injects-traceparent — valid non-zero traceparent even under NO-OP telemetry.
func TestScenarioEdgeSpanInjectsTraceparent(t *testing.T) {
	var got string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Get("traceparent") })
	// no-op telemetry (nil) — the header must still be a valid minted traceparent.
	mw := observ.Chain(observ.Config{Trace: true}, nil, nil)
	serve(t, mw, next, httptest.NewRequest("GET", "http://x/y", nil))
	parts := strings.Split(got, "-")
	require.Len(t, parts, 4, "well-formed traceparent")
	require.Equal(t, "00", parts[0])
	require.Len(t, parts[1], 32)
	require.NotEqual(t, strings.Repeat("0", 32), parts[1], "trace-id is non-zero (the shim rejects all-zeros)")
	require.Len(t, parts[2], 16)
	require.NotEqual(t, strings.Repeat("0", 16), parts[2], "edge span-id is non-zero")
}

// scenario: edge-span-adopts-inbound
func TestScenarioEdgeSpanAdoptsInbound(t *testing.T) {
	var got string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Get("traceparent") })
	inboundTrace := strings.Repeat("ab", 16) // 32 hex
	req := httptest.NewRequest("GET", "http://x/y", nil)
	req.Header.Set("traceparent", "00-"+inboundTrace+"-"+strings.Repeat("cd", 8)+"-01")
	serve(t, observ.Chain(observ.Config{Trace: true}, nil, nil), next, req)
	require.Equal(t, inboundTrace, strings.Split(got, "-")[1], "the inbound trace-id is adopted (trace continues)")
}

// scenario: access-log-correlated
func TestScenarioAccessLogCorrelated(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tgt, ok := observ.TargetFrom(r.Context()); ok {
			tgt.Function = "orders"
		}
		w.WriteHeader(http.StatusTeapot)
	})
	req := httptest.NewRequest("GET", "http://x/orders", nil)
	req.Header.Set("X-Request-Id", "req-123")
	// observ reads the id from context (seeded by the RequestID middleware upstream in the real
	// chain), NOT from the header — so wrap observ under gateway.RequestID exactly as funcd wires it.
	mw := func(next http.Handler) http.Handler {
		return gateway.RequestID(observ.Chain(observ.Config{AccessLog: true}, nil, logger)(next))
	}
	serve(t, mw, next, req)
	line := buf.String()
	require.Contains(t, line, `"status":418`)
	require.Contains(t, line, `"function":"orders"`)
	require.Contains(t, line, `"request_id":"req-123"`)
	require.Contains(t, line, `"duration_ms"`)
}

// The status recorder forwards Flusher + Hijacker (SSE/WS must not break).
func TestRecorderForwardsFlusherAndHijacker(t *testing.T) {
	var flushed, hijacked bool
	done := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(done) // f.Flush() unblocks the client before the handler finishes, so publish the flags
		// via `done` (happens-before) rather than reading them racily after http.Get returns.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
			flushed = true
		}
		if _, ok := w.(http.Hijacker); ok {
			hijacked = true
		}
	})
	mw := observ.Chain(observ.Config{AccessLog: true}, nil, nil)
	// A real server response writer implements both; use httptest.NewServer.
	srv := httptest.NewServer(mw(next))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	<-done // wait for the handler to publish the flags before reading them
	require.True(t, flushed, "the observ wrapper forwards http.Flusher")
	require.True(t, hijacked, "the observ wrapper forwards http.Hijacker")
}

// Issue #85: with real telemetry the edge emits an OTel SERVER span whose span-id is the one injected
// into the forwarded traceparent (so the invocation span has an emitted parent), parented under the
// inbound span; a malformed inbound trace-id is not adopted.
func TestIssue85_EdgeServerSpanEmittedWithInjectedSpanID(t *testing.T) {
	const inboundTrace, inboundSpan = "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331"
	cases := []struct {
		name        string
		inbound     string
		adopt       bool
		wantsParent bool
	}{
		{name: "adopts inbound", inbound: "00-" + inboundTrace + "-" + inboundSpan + "-01", adopt: true, wantsParent: true},
		{name: "root", inbound: ""},
		{name: "uppercase trace-id not adopted", inbound: "00-" + strings.ToUpper(inboundTrace) + "-" + inboundSpan + "-01"},
		{name: "non-hex trace-id not adopted", inbound: "00-" + strings.Repeat("zz", 16) + "-" + inboundSpan + "-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sr := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
			tel := observability.NewFromProviders(nil, tp)
			var injected string
			next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { injected = r.Header.Get("traceparent") })
			req := httptest.NewRequest("GET", "http://x/y", nil)
			if tc.inbound != "" {
				req.Header.Set("traceparent", tc.inbound)
			}
			serve(t, observ.Chain(observ.Config{Trace: true}, tel, nil), next, req)

			parts := strings.Split(injected, "-")
			require.Len(t, parts, 4, "well-formed traceparent")
			spans := sr.Ended()
			require.Len(t, spans, 1, "one edge span is emitted under real telemetry")
			sp := spans[0]
			require.Equal(t, trace.SpanKindServer, sp.SpanKind())
			require.Equal(t, sp.SpanContext().TraceID().String(), parts[1], "the injected trace-id is the edge span's")
			require.Equal(t, sp.SpanContext().SpanID().String(), parts[2], "the injected span-id is the emitted edge span's")
			if tc.adopt {
				require.Equal(t, inboundTrace, parts[1], "the inbound trace-id is adopted")
			} else {
				require.NotEqual(t, inboundTrace, strings.ToLower(parts[1]), "a malformed or absent inbound trace-id is not adopted")
			}
			if tc.wantsParent {
				require.Equal(t, inboundSpan, sp.Parent().SpanID().String(), "the edge span parents under the inbound span")
			} else {
				require.False(t, sp.Parent().IsValid(), "the edge span is a root")
			}
		})
	}
}

// Issue #311: a panicking inner handler is still counted (as 5xx), logged once, and its edge span
// ends — under gateway.Recover exactly as funcd wires it — and the panic still propagates.
func TestIssue311_PanicStillCountedLoggedAndSpanEnded(t *testing.T) {
	cases := []struct {
		name      string
		next      http.HandlerFunc
		wantPanic error
	}{
		{
			name: "plain panic answered 500 by Recover",
			next: func(http.ResponseWriter, *http.Request) { panic("boom") },
		},
		{
			name: "abort after a 200 header re-panics",
			next: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				panic(http.ErrAbortHandler)
			},
			wantPanic: http.ErrAbortHandler,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := metric.NewManualReader()
			sr := tracetest.NewSpanRecorder()
			tel := observability.NewFromProviders(
				metric.NewMeterProvider(metric.WithReader(reader)),
				sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)),
			)
			var buf bytes.Buffer
			mw := observ.Chain(observ.Config{Metrics: true, AccessLog: true, Trace: true}, tel, slog.New(slog.NewJSONHandler(&buf, nil)))
			h := gateway.Chain(tc.next, gateway.Recover, gateway.RequestID, mw)

			var got error
			func() {
				defer func() { got, _ = recover().(error) }()
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://x/y", nil))
			}()
			require.Equal(t, tc.wantPanic, got, "the panic continues past observ")

			var rm metricdata.ResourceMetrics
			require.NoError(t, reader.Collect(context.Background(), &rm))
			var counted int64
			var classes []string
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if sum, ok := m.Data.(metricdata.Sum[int64]); ok && m.Name == "funcd.edge.requests" {
						for _, dp := range sum.DataPoints {
							counted += dp.Value
							v, _ := dp.Attributes.Value("status_class")
							classes = append(classes, v.AsString())
						}
					}
				}
			}
			require.Equal(t, int64(1), counted, "the panicked request is counted")
			require.Equal(t, []string{"5xx"}, classes, "a panic counts as 5xx")

			spans := sr.Ended()
			require.Len(t, spans, 1, "the edge span ends")
			require.Contains(t, spans[0].Attributes(), attribute.String("status_class", "5xx"))

			require.Equal(t, 1, strings.Count(buf.String(), `"msg":"edge request"`), "one access-log line")
			require.Contains(t, buf.String(), `"status":500`)
		})
	}
}
