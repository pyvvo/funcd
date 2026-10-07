// Package observability builds the funcd platform's root logger (ADR-0009).
//
// It constructs a root *slog.Logger from configuration, hands out named child
// loggers, allows runtime log-level switching, and lifts trace_id/span_id from
// the OpenTelemetry span context into every record (the trace API only — the
// SDK pipeline, OTLP export, and audit channel belong to the follow-up P-F2).
// There is no package-level logger: the root is constructed here and injected
// from the composition root, so tests can assert on output with an in-memory
// writer.
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Format selects the slog handler rendering. The empty value is valid and
// defaults to FormatJSON in NewLogger (blueprint: stdout JSON by default).
type Format string

const (
	// FormatText renders human-readable lines — the dev default.
	FormatText Format = "text"
	// FormatJSON renders structured JSON — the production default.
	FormatJSON Format = "json"
)

// Validate reports whether f is a known format. The empty value is accepted
// (NewLogger applies the JSON default); any other unknown value maps to
// fault.Invalid so it surfaces as HTTP 400 at an edge.
func (f Format) Validate() error {
	switch f {
	case "", FormatText, FormatJSON:
		return nil
	default:
		return fault.Invalidf("observability.Format.Validate",
			"unknown log format %q (want %q or %q)", string(f), FormatText, FormatJSON)
	}
}

// Config configures the root logger. It is value config (ADR-0002 §1): the
// composition root supplies a populated Config. Level is a slog.Level, which
// implements encoding.TextUnmarshaler, so "info"/"debug"/"warn+2"/… bind
// directly from TOML or the environment without a custom parser here.
type Config struct {
	// Level is the initial log level; it is switchable at runtime via SetLevel.
	Level slog.Level
	// Format selects text or json rendering; the empty value defaults to json.
	Format Format
}

// Logger is the platform's root logging facade: a root *slog.Logger whose level
// is switchable at runtime, plus named child loggers. It holds no package-level
// state — two NewLogger calls are fully independent.
type Logger struct {
	root  *slog.Logger
	level *slog.LevelVar
}

// NewLogger builds the root Logger from cfg, writing to w (the composition root
// passes os.Stdout; tests pass a buffer). It returns a fault.Invalid error if
// cfg.Format is an unknown value. The name matches the blueprint "Platform
// logging" spec; the package will also expose the OTel provider and audit
// constructors in P-F2, so it is NewLogger, not a bare New. slog.LevelVar and
// the stdlib handlers are goroutine-safe, so SetLevel is race-free during live
// logging.
func NewLogger(cfg Config, w io.Writer) (*Logger, error) {
	if err := cfg.Format.Validate(); err != nil {
		return nil, err
	}
	format := cfg.Format
	if format == "" {
		format = FormatJSON
	}

	level := new(slog.LevelVar)
	level.Set(cfg.Level)
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: ReplaceAttr}

	var base slog.Handler
	if format == FormatText {
		base = slog.NewTextHandler(w, opts)
	} else {
		base = slog.NewJSONHandler(w, opts)
	}

	return &Logger{
		root:  slog.New(traceHandler{inner: base}),
		level: level,
	}, nil
}

// ReplaceAttr is the slog hook every funcd handler sets: it writes a record's own top-level time in the
// v1alpha1.Timestamp form (ADR-0196); a time-valued attribute, or a time inside a group, is left as it is. A duration
// at any group depth becomes a <key>_ms number of milliseconds (ADR-0197).
func ReplaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		return slog.String(slog.TimeKey, v1.NewTimestamp(a.Value.Time()).String())
	}
	return durationField(a)
}

// durationField rewrites a time.Duration or v1.Duration attribute by ADR-0197; any other attribute is unchanged.
func durationField(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindDuration:
		return durationAttr(a.Key, a.Value.Duration())
	case slog.KindAny:
		if d, ok := a.Value.Any().(v1.Duration); ok {
			return durationAttr(a.Key, time.Duration(d))
		}
	}
	return a
}

// durationAttr is the attribute <key>_ms (a key already ending in _ms is kept) valued millis(d).
func durationAttr(key string, d time.Duration) slog.Attr {
	if !strings.HasSuffix(key, "_ms") {
		key += "_ms"
	}
	return slog.Any(key, json.Number(millis(d)))
}

// millis writes d in milliseconds, rounded to the nearest microsecond, with at most three decimals and no trailing
// zeros; a whole number of milliseconds has no fraction.
func millis(d time.Duration) string {
	us := int64(d.Round(time.Microsecond) / time.Microsecond)
	sign := ""
	if us < 0 {
		sign, us = "-", -us
	}
	s := sign + strconv.FormatInt(us/1000, 10)
	if frac := us % 1000; frac != 0 {
		s += "." + strings.TrimRight(fmt.Sprintf("%03d", frac), "0")
	}
	return s
}

// Root returns the root logger. Prefer Component for per-component children.
func (l *Logger) Root() *slog.Logger { return l.root }

// Component returns a child logger tagged with component=name. Children are
// independent and do not mutate the root or each other.
func (l *Logger) Component(name string) *slog.Logger {
	return l.root.With("component", name)
}

// SetLevel switches the level for the root and every already-constructed child
// (they share one LevelVar), with no logger rebuild. It is the seam the admin
// log-level endpoint (P-L) drives.
func (l *Logger) SetLevel(level slog.Level) { l.level.Set(level) }

// Level reports the current log level.
func (l *Logger) Level() slog.Level { return l.level.Level() }

// traceHandler decorates a slog.Handler, lifting trace_id/span_id from the OTel
// span context in ctx into every record. It starts no spans and exports nothing
// (that is P-F2). When ctx carries no valid span it is a transparent
// pass-through. WithGroup is not used on the root logger, so the correlation
// attrs always render top-level; the record is added-to then forwarded (never
// retained), so no Record.Clone is needed.
type traceHandler struct{ inner slog.Handler }

func (h traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h traceHandler) Handle(ctx context.Context, record slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, record)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{inner: h.inner.WithGroup(name)}
}
