// Package observ is the edge observability middleware (ADR-0114, F76): RED metrics (request rate /
// errors / duration by function+status) off the OTel meter, an edge span that mints or adopts a W3C
// traceparent so the downstream invocation span parents under it (the ingress hop joins the
// one-run-one-trace waterfall, ADR-0101/0102), and a structured access log correlated by
// X-Request-Id. It is additive (never rejects) and no-op-friendly (the default no-op Telemetry ⇒ no
// metric/trace dials; the access log uses slog). It sits outer than the F75 limiter (so it times the
// whole hop incl. rejects) and inner than RequestID (so it can read the id). Its ResponseWriter
// wrapper forwards http.Flusher + http.Hijacker so SSE/WS are never broken.
package observ

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/platform/observability"
)

// Config toggles the three observability signals. A zero Config is a pass-through.
type Config struct {
	Metrics   bool
	AccessLog bool
	Trace     bool
}

// Target is the request-scoped holder observ seeds and dataplane fills with the resolved function, so
// the metric/log label is correct even for a Route hit (observ runs outside dataplane.Handler).
type Target struct {
	Namespace string
	Function  string
}

type targetKey struct{}

// WithTarget seeds an empty Target holder into ctx.
func WithTarget(ctx context.Context) (context.Context, *Target) {
	t := &Target{}
	return context.WithValue(ctx, targetKey{}, t), t
}

// TargetFrom returns the request's Target holder, if seeded.
func TargetFrom(ctx context.Context) (*Target, bool) {
	t, ok := ctx.Value(targetKey{}).(*Target)
	return t, ok
}

// Chain returns the observability middleware. A zero Config is a pass-through.
func Chain(cfg Config, telemetry *observability.Telemetry, logger *slog.Logger) func(http.Handler) http.Handler {
	if cfg == (Config{}) {
		return func(next http.Handler) http.Handler { return next }
	}
	if logger == nil {
		logger = slog.Default()
	}
	var reqCounter metric.Int64Counter
	var durHist metric.Float64Histogram
	if cfg.Metrics && telemetry != nil {
		m := telemetry.MeterProvider().Meter("funcd.edge")
		reqCounter, _ = m.Int64Counter("funcd.edge.requests")
		durHist, _ = m.Float64Histogram("funcd.edge.duration_ms")
	}
	tp := trace.TracerProvider(tracenoop.NewTracerProvider())
	if cfg.Trace && telemetry != nil {
		tp = telemetry.TracerProvider()
	}
	tracer := tp.Tracer("funcd.edge")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			var span trace.Span
			if cfg.Trace {
				span = startEdgeSpan(r, tracer)
			}
			ctx, tgt := WithTarget(r.Context())
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			completed := false
			// Deferred so a panicking inner handler (e.g. ReverseProxy's http.ErrAbortHandler) is still
			// counted, logged and its span ended, as a 5xx; the panic itself keeps unwinding (#311).
			defer func() {
				status := rec.status
				if !completed {
					status = http.StatusInternalServerError
				}
				dur := time.Since(start)
				fn, ns := tgt.Function, tgt.Namespace
				if fn == "" {
					fn = "-"
				}
				statusClass := string(rune('0'+status/100)) + "xx"
				attrs := []attribute.KeyValue{
					attribute.String("function", fn),
					attribute.String("namespace", ns),
					attribute.String("status_class", statusClass),
				}
				if reqCounter != nil {
					opt := metric.WithAttributes(attrs...)
					reqCounter.Add(r.Context(), 1, opt)
					durHist.Record(r.Context(), float64(dur.Milliseconds()), opt)
				}
				if span != nil {
					span.SetAttributes(attrs...)
					span.End()
				}
				if cfg.AccessLog {
					logger.Info("edge request",
						"method", r.Method, "path", r.URL.Path, "function", fn, "namespace", ns,
						"status", status, "duration", dur,
						"request_id", gateway.RequestIDFromContext(r.Context()))
				}
			}()
			next.ServeHTTP(rec, r.WithContext(ctx))
			completed = true
		})
	}
}

// startEdgeSpan starts the edge SERVER span under the inbound W3C traceparent (adopted only when
// valid) and injects the edge span's own traceparent, so the downstream invocation span (the shim)
// parents under it (shared trace-id → one-run-one-trace). When the tracer records, the injected ids
// ARE the emitted span's ids. Otherwise (the default no-op telemetry) the missing ids are minted with
// crypto/rand, so the header is still valid (an all-zero span context would be rejected by the shim).
func startEdgeSpan(r *http.Request, tracer trace.Tracer) trace.Span {
	parent := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	_, span := tracer.Start(parent, "edge "+r.Method, trace.WithSpanKind(trace.SpanKindServer))
	sc := span.SpanContext()
	traceID, spanID := sc.TraceID().String(), sc.SpanID().String()
	if !sc.TraceID().IsValid() {
		traceID = randHex(16)
	}
	if !span.IsRecording() {
		spanID = randHex(8)
	}
	r.Header.Set("traceparent", "00-"+traceID+"-"+spanID+"-01")
	return span
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// recorder captures the final response status (a relayed 1xx is passed on, not recorded) and
// FORWARDS http.Flusher + http.Hijacker so SSE streams and WebSocket upgrades pass through unbroken
// (the invoke path streams with FlushInterval=-1 and hijacks for upgrades).
type recorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *recorder) WriteHeader(code int) {
	if !r.wrote && !gateway.Interim(code) {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.wrote = true
	}
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("observ: underlying ResponseWriter does not support hijacking")
}
