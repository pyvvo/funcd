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
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

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
