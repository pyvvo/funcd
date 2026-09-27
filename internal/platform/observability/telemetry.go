package observability

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otellog "go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/pyvvo/funcd/api/fault"
)

const defaultServiceName = "funcd"

// TelemetryConfig configures the OTel pipeline. An empty Endpoint disables export
// (no-op providers) — dev/tests need no collector. ServiceName defaults to "funcd".
type TelemetryConfig struct {
	// Endpoint is the OTLP gRPC target, e.g. "localhost:4317"; "" disables export.
	Endpoint string
	// ServiceName sets the resource service.name; "" defaults to "funcd".
	ServiceName string
	// Insecure runs gRPC without TLS (dev/local collector).
	Insecure bool
}

// Telemetry holds the platform's OTel providers and the OTLP log handler. It is
// injected (no globals); Shutdown flushes and closes every provider.
type Telemetry struct {
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	loggerProvider otellog.LoggerProvider
	logHandler     slog.Handler
	shutdownFuncs  []func(context.Context) error
}

// NewTelemetry builds the pipeline from cfg. With cfg.Endpoint set it constructs
// OTLP gRPC exporters and SDK providers; otherwise it returns no-op providers and
// a no-op log handler. Construction never dials the network (export is lazy).
func NewTelemetry(ctx context.Context, cfg TelemetryConfig) (*Telemetry, error) {
	if cfg.Endpoint == "" {
		return noopTelemetry(), nil
	}

	res := newResource(cfg.ServiceName)

	traceExp, err := otlptracegrpc.New(ctx, grpcTraceOpts(cfg)...)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, "observability.NewTelemetry", "build OTLP trace exporter")
	}
	metricExp, err := otlpmetricgrpc.New(ctx, grpcMetricOpts(cfg)...)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, "observability.NewTelemetry", "build OTLP metric exporter")
	}
	logExp, err := otlploggrpc.New(ctx, grpcLogOpts(cfg)...)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, "observability.NewTelemetry", "build OTLP log exporter")
	}

	tp := newTracerProvider(res, traceExp)
	mp := newMeterProvider(res, metricExp)
	lp := newLoggerProvider(res, logExp)

	return &Telemetry{
		tracerProvider: tp,
		meterProvider:  mp,
		loggerProvider: lp,
		logHandler:     logHandlerFor(lp),
		shutdownFuncs:  []func(context.Context) error{tp.Shutdown, mp.Shutdown, lp.Shutdown},
	}, nil
}

// logHandlerFor builds the otelslog bridge handler over the logger provider.
func logHandlerFor(lp otellog.LoggerProvider) slog.Handler {
	return otelslog.NewHandler(defaultServiceName, otelslog.WithLoggerProvider(lp))
}

// NewFromProviders builds a Telemetry from explicit OTel providers (a nil provider falls back to the
// no-op). It is the seam for injecting SDK providers directly — e.g. an in-memory reader/recorder in
// tests, or a custom-wired pipeline — without going through the OTLP-exporter constructor.
func NewFromProviders(mp metric.MeterProvider, tp trace.TracerProvider) *Telemetry {
	t := noopTelemetry()
	if mp != nil {
		t.meterProvider = mp
	}
	if tp != nil {
		t.tracerProvider = tp
	}
	return t
}

// TracerProvider returns the tracer provider (SDK-backed or no-op).
func (t *Telemetry) TracerProvider() trace.TracerProvider { return t.tracerProvider }

// MeterProvider returns the meter provider (SDK-backed or no-op).
func (t *Telemetry) MeterProvider() metric.MeterProvider { return t.meterProvider }

// LoggerProvider returns the OTel logger provider (SDK-backed or no-op).
func (t *Telemetry) LoggerProvider() otellog.LoggerProvider { return t.loggerProvider }

// LogHandler returns the otelslog bridge handler (records ship over OTLP), or a
// no-op handler whose Enabled is false when telemetry is disabled.
func (t *Telemetry) LogHandler() slog.Handler { return t.logHandler }

// Shutdown flushes and closes every provider. It is deadline-bounded (honors ctx)
// and idempotent — a second call is a no-op.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	funcs := t.shutdownFuncs
	t.shutdownFuncs = nil
	var errs []error
	for _, fn := range funcs {
		errs = append(errs, fn(ctx))
	}
	return errors.Join(errs...)
}

func noopTelemetry() *Telemetry {
	return &Telemetry{
		tracerProvider: tracenoop.NewTracerProvider(),
		meterProvider:  metricnoop.NewMeterProvider(),
		loggerProvider: lognoop.NewLoggerProvider(),
		logHandler:     discardHandler{},
	}
}

// newResource builds the platform resource: service.name (default "funcd") and
// source=platform (distinguishing platform telemetry from function telemetry).
func newResource(serviceName string) *resource.Resource {
	if serviceName == "" {
		serviceName = defaultServiceName
	}
	return resource.NewSchemaless(
		attribute.String("service.name", serviceName),
		attribute.String("source", "platform"),
	)
}

func newTracerProvider(res *resource.Resource, exp sdktrace.SpanExporter) *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exp))
}

func newMeterProvider(res *resource.Resource, exp sdkmetric.Exporter) *sdkmetric.MeterProvider {
	return sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)))
}

func newLoggerProvider(res *resource.Resource, exp sdklog.Exporter) *sdklog.LoggerProvider {
	return sdklog.NewLoggerProvider(sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)))
}

func grpcTraceOpts(cfg TelemetryConfig) []otlptracegrpc.Option {
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	return opts
}

func grpcMetricOpts(cfg TelemetryConfig) []otlpmetricgrpc.Option {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	return opts
}

func grpcLogOpts(cfg TelemetryConfig) []otlploggrpc.Option {
	opts := []otlploggrpc.Option{otlploggrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlploggrpc.WithInsecure())
	}
	return opts
}

// discardHandler is the no-op slog.Handler used when telemetry is disabled. Its
// Enabled returns false so a fanout short-circuits and pays nothing for it.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (discardHandler) WithAttrs([]slog.Attr) slog.Handler        { return discardHandler{} }
func (discardHandler) WithGroup(string) slog.Handler             { return discardHandler{} }

// Fanout is a slog.Handler that dispatches every record to all inner handlers —
// the seam the composition root (P-I) uses to send a record to stdout and OTLP.
// Concrete return type (ADR-0002 §1: constructors return concrete structs).
type Fanout struct{ handlers []slog.Handler }

// NewFanout returns a Fanout over the given handlers.
func NewFanout(handlers ...slog.Handler) *Fanout { return &Fanout{handlers: handlers} }

// Enabled reports whether any inner handler is enabled at the level.
func (f *Fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle dispatches a clone of the record to every inner handler that is enabled.
func (f *Fanout) Handle(ctx context.Context, record slog.Record) error {
	var errs []error
	for _, h := range f.handlers {
		if h.Enabled(ctx, record.Level) {
			errs = append(errs, h.Handle(ctx, record.Clone()))
		}
	}
	return errors.Join(errs...)
}

// WithAttrs returns a Fanout whose inner handlers all carry the attrs.
func (f *Fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &Fanout{handlers: next}
}

// WithGroup returns a Fanout whose inner handlers all open the group.
func (f *Fanout) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return &Fanout{handlers: next}
}
