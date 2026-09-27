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
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

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
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			if cfg.Trace {
				injectTraceparent(r)
			}
			ctx, tgt := WithTarget(r.Context())
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r.WithContext(ctx))

			dur := time.Since(start)
			fn, ns := tgt.Function, tgt.Namespace
			if fn == "" {
				fn = "-"
			}
			statusClass := string(rune('0'+rec.status/100)) + "xx"
			if reqCounter != nil {
				attrs := metric.WithAttributes(
					attribute.String("function", fn),
					attribute.String("namespace", ns),
					attribute.String("status_class", statusClass),
				)
				reqCounter.Add(r.Context(), 1, attrs)
				durHist.Record(r.Context(), float64(dur.Milliseconds()), attrs)
			}
			if cfg.AccessLog {
				logger.Info("edge request",
					"method", r.Method, "path", r.URL.Path, "function", fn, "namespace", ns,
					"status", rec.status, "duration_ms", dur.Milliseconds(),
					"request_id", gateway.RequestIDFromContext(r.Context()))
			}
		})
	}
}

// injectTraceparent mints or adopts a W3C trace context and injects it, so the downstream invocation
// span (the shim) parents under the edge span (shared trace-id → one-run-one-trace). The ids are
// minted with crypto/rand — NOT derived from the OTel tracer — so the header is valid even under the
// default no-op telemetry (an all-zero OTel span context would be rejected by the shim).
func injectTraceparent(r *http.Request) {
	traceID := ""
	if tp := r.Header.Get("traceparent"); tp != "" {
		if p := strings.Split(tp, "-"); len(p) >= 3 && len(p[1]) == 32 && p[1] != strings.Repeat("0", 32) {
			traceID = p[1] // adopt the inbound trace-id (continue the trace)
		}
	}
	if traceID == "" {
		traceID = randHex(16)
	}
	edgeSpan := randHex(8)
	r.Header.Set("traceparent", "00-"+traceID+"-"+edgeSpan+"-01")
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// recorder captures the response status and FORWARDS http.Flusher + http.Hijacker so SSE streams and
// WebSocket upgrades pass through unbroken (the invoke path streams with FlushInterval=-1 and hijacks
// for upgrades).
type recorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
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
