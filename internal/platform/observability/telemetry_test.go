package observability

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// capturingLogExporter is an in-memory sdklog.Exporter for hermetic tests (not a
// mock framework — a real exporter that records what it is given).
type capturingLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *capturingLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.records = append(e.records, records...)
	return nil
}
func (e *capturingLogExporter) Shutdown(context.Context) error   { return nil }
func (e *capturingLogExporter) ForceFlush(context.Context) error { return nil }

func (e *capturingLogExporter) bodies() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.records))
	for i := range e.records {
		out = append(out, e.records[i].Body().AsString())
	}
	return out
}

// scenario: telemetry-disabled-noop — no endpoint → usable no-op providers and a
// no-op (Enabled=false) log handler, and a nil Shutdown.
func TestScenarioTelemetryDisabledNoop(t *testing.T) {
	t.Parallel()
	tel, err := NewTelemetry(context.Background(), TelemetryConfig{})
	require.NoError(t, err)
	require.NotNil(t, tel.TracerProvider())
	require.NotNil(t, tel.MeterProvider())
	require.NotNil(t, tel.LoggerProvider())
	require.False(t, tel.LogHandler().Enabled(context.Background(), slog.LevelError),
		"disabled telemetry → no-op handler short-circuits")
	require.NoError(t, tel.Shutdown(context.Background()))
}

// scenario: telemetry-otlp-constructs — endpoint set → SDK-backed providers, no
// error, and Shutdown returns within a deadline (no live collector needed).
func TestScenarioTelemetryOTLPConstructs(t *testing.T) {
	t.Parallel()
	tel, err := NewTelemetry(context.Background(), TelemetryConfig{Endpoint: "localhost:4317", Insecure: true})
	require.NoError(t, err)

	_, ok := tel.TracerProvider().(*sdktrace.TracerProvider)
	require.True(t, ok, "endpoint set → SDK tracer provider, not no-op")
	require.NotNil(t, tel.LogHandler())

	// Shutdown must RETURN within the deadline (no hang) even though no collector
	// is listening — the scenario is "returns without hanging", not a clean flush.
	// A deadline error is acceptable; hanging past the bound is not.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tel.Shutdown(ctx) }()
	select {
	case <-done:
		// returned (with or without a deadline error) — the assertion
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown hung past the deadline")
	}
}

// scenario: telemetry-resource-attrs — a recorded span carries the platform
// resource attributes service.name=funcd and source=platform.
func TestScenarioTelemetryResourceAttrs(t *testing.T) {
	t.Parallel()
	exp := tracetest.NewInMemoryExporter()
	tp := newTracerProvider(newResource(""), exp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(context.Background(), "op")
	span.End()
	require.NoError(t, tp.ForceFlush(context.Background()))

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	attrs := map[string]string{}
	for _, kv := range spans[0].Resource.Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsString()
	}
	require.Equal(t, "funcd", attrs["service.name"])
	require.Equal(t, "platform", attrs["source"])
}

// scenario: log-bridge-emits — a record written through the otelslog bridge
// reaches the (in-memory) log exporter.
func TestScenarioLogBridgeEmits(t *testing.T) {
	t.Parallel()
	exp := &capturingLogExporter{}
	lp := newLoggerProvider(newResource("funcd"), exp)

	tel := &Telemetry{
		loggerProvider: lp,
		logHandler:     logHandlerFor(lp),
		shutdownFuncs:  []func(context.Context) error{lp.Shutdown},
	}
	slog.New(tel.LogHandler()).InfoContext(context.Background(), "platform-event")

	require.NoError(t, lp.ForceFlush(context.Background()))
	require.Contains(t, exp.bodies(), "platform-event")
	require.NoError(t, tel.Shutdown(context.Background()))
}

// scenario: fanout-dispatch — one record reaches every inner handler.
func TestScenarioFanoutDispatch(t *testing.T) {
	t.Parallel()
	var bufA, bufB bytes.Buffer
	hA := slog.NewJSONHandler(&bufA, &slog.HandlerOptions{Level: slog.LevelInfo})
	hB := slog.NewJSONHandler(&bufB, &slog.HandlerOptions{Level: slog.LevelInfo})

	fan := NewFanout(hA, hB)
	require.True(t, fan.Enabled(context.Background(), slog.LevelInfo))
	slog.New(fan).InfoContext(context.Background(), "fanned")

	require.Contains(t, bufA.String(), "fanned")
	require.Contains(t, bufB.String(), "fanned", "both sinks receive the record")
}

// spanExporterSpy and metricExporterSpy record Shutdown; nothing else is called on them.
type spanExporterSpy struct {
	sdktrace.SpanExporter
	shutdown bool
}

func (s *spanExporterSpy) Shutdown(context.Context) error { s.shutdown = true; return nil }

type metricExporterSpy struct {
	sdkmetric.Exporter
	shutdown bool
}

func (s *metricExporterSpy) Shutdown(context.Context) error { s.shutdown = true; return nil }

// When the metric or the log exporter fails to build, the exporters built before it (each holds a gRPC
// connection) are shut down and the error is returned.
func TestIssue508_FailedExporterShutsDownEarlierOnes(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	build := func(failMetric bool) (exporters, *spanExporterSpy, *metricExporterSpy) {
		traceExp, metricExp := &spanExporterSpy{}, &metricExporterSpy{}
		return exporters{
			trace: func(context.Context) (sdktrace.SpanExporter, error) { return traceExp, nil },
			metric: func(context.Context) (sdkmetric.Exporter, error) {
				if failMetric {
					return nil, boom
				}
				return metricExp, nil
			},
			log: func(context.Context) (sdklog.Exporter, error) { return nil, boom },
		}, traceExp, metricExp
	}

	t.Run("metric exporter fails", func(t *testing.T) {
		t.Parallel()
		exp, traceExp, _ := build(true)
		_, err := newTelemetry(context.Background(), "", exp)
		require.ErrorIs(t, err, boom)
		require.True(t, traceExp.shutdown, "the trace exporter is left open")
	})
	t.Run("log exporter fails", func(t *testing.T) {
		t.Parallel()
		exp, traceExp, metricExp := build(false)
		_, err := newTelemetry(context.Background(), "", exp)
		require.ErrorIs(t, err, boom)
		require.True(t, traceExp.shutdown, "the trace exporter is left open")
		require.True(t, metricExp.shutdown, "the metric exporter is left open")
	})
}
