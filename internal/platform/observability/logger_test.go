package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/observability"
)

// logLine is the decoded shape of one slog JSON record. All fields the tests
// assert on are strings, so this avoids any/map[string]any (forbidigo §4).
type logLine struct {
	Level     string `json:"level"`
	Msg       string `json:"msg"`
	Component string `json:"component"`
	TraceID   string `json:"trace_id"`
	SpanID    string `json:"span_id"`
}

// decodeLines parses every non-empty JSON record written to buf.
func decodeLines(t *testing.T, buf *bytes.Buffer) []logLine {
	t.Helper()
	var out []logLine
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line logLine
		require.NoError(t, json.Unmarshal([]byte(raw), &line))
		out = append(out, line)
	}
	return out
}

// scenario: level-and-format — a configured logger renders JSON at the chosen
// level and suppresses below-level records.
func TestScenarioLevelAndFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg, err := observability.NewLogger(observability.Config{Level: slog.LevelInfo, Format: observability.FormatJSON}, &buf)
	require.NoError(t, err)

	ctx := context.Background()
	lg.Component("controller").InfoContext(ctx, "hello")
	lg.Component("controller").DebugContext(ctx, "suppressed")

	lines := decodeLines(t, &buf)
	require.Len(t, lines, 1, "info emitted, debug suppressed below level")
	require.Equal(t, "INFO", lines[0].Level)
	require.Equal(t, "hello", lines[0].Msg)
	require.Equal(t, "controller", lines[0].Component)
}

// scenario: runtime-level-switch — SetLevel changes the level of an
// already-constructed child with no rebuild, both lowering and raising.
func TestScenarioRuntimeLevelSwitch(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg, err := observability.NewLogger(observability.Config{Level: slog.LevelInfo, Format: observability.FormatJSON}, &buf)
	require.NoError(t, err)

	ctx := context.Background()
	child := lg.Component("gateway") // constructed before any level change

	child.DebugContext(ctx, "before")
	require.Empty(t, decodeLines(t, &buf), "debug suppressed at Info")

	lg.SetLevel(slog.LevelDebug)
	require.Equal(t, slog.LevelDebug, lg.Level())
	child.DebugContext(ctx, "after-lower")
	lines := decodeLines(t, &buf)
	require.Len(t, lines, 1)
	require.Equal(t, "after-lower", lines[0].Msg)

	buf.Reset()
	lg.SetLevel(slog.LevelInfo)
	child.DebugContext(ctx, "after-raise")
	require.Empty(t, decodeLines(t, &buf), "debug suppressed again after raising the level")
}

// scenario: named-child-component — every child record carries component=name
// and children are independent.
func TestScenarioNamedChildComponent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg, err := observability.NewLogger(observability.Config{Format: observability.FormatJSON}, &buf)
	require.NoError(t, err)

	ctx := context.Background()
	lg.Component("gateway").InfoContext(ctx, "a")
	lg.Component("scheduler").InfoContext(ctx, "b")

	lines := decodeLines(t, &buf)
	require.Len(t, lines, 2)
	require.Equal(t, "gateway", lines[0].Component)
	require.Equal(t, "scheduler", lines[1].Component, "second child does not inherit the first's component")
}

// scenario: trace-correlation — a valid OTel span context in ctx yields
// trace_id/span_id; an empty context yields neither and no error.
func TestScenarioTraceCorrelation(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg, err := observability.NewLogger(observability.Config{Format: observability.FormatJSON}, &buf)
	require.NoError(t, err)

	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	lg.Root().InfoContext(ctx, "with-span")
	lg.Root().InfoContext(context.Background(), "no-span")

	lines := decodeLines(t, &buf)
	require.Len(t, lines, 2)
	require.Equal(t, traceID.String(), lines[0].TraceID)
	require.Equal(t, spanID.String(), lines[0].SpanID)
	require.Empty(t, lines[1].TraceID, "no span in ctx → no trace_id")
	require.Empty(t, lines[1].SpanID, "no span in ctx → no span_id")
}

// scenario: isolated-instances — two independently constructed loggers do not
// share level state (proving there is no package-level global).
func TestScenarioIsolatedInstances(t *testing.T) {
	t.Parallel()
	var bufA, bufB bytes.Buffer
	loggerA, err := observability.NewLogger(observability.Config{Level: slog.LevelInfo, Format: observability.FormatJSON}, &bufA)
	require.NoError(t, err)
	loggerB, err := observability.NewLogger(observability.Config{Level: slog.LevelInfo, Format: observability.FormatJSON}, &bufB)
	require.NoError(t, err)

	loggerA.SetLevel(slog.LevelDebug)

	require.Equal(t, slog.LevelDebug, loggerA.Level())
	require.Equal(t, slog.LevelInfo, loggerB.Level(), "SetLevel on A must not move B")

	ctx := context.Background()
	loggerB.Root().DebugContext(ctx, "should-be-suppressed")
	require.Empty(t, decodeLines(t, &bufB), "B stays at Info despite A switching to Debug")
}

// scenario: invalid-format-rejected — an unknown format is a typed fault.Invalid
// and yields no *Logger.
func TestScenarioInvalidFormatRejected(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg, err := observability.NewLogger(observability.Config{Format: observability.Format("xml")}, &buf)
	require.Error(t, err)
	require.Nil(t, lg)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

func newTestLogger(t *testing.T, format observability.Format, w io.Writer) *slog.Logger {
	t.Helper()
	lg, err := observability.NewLogger(observability.Config{Format: format}, w)
	require.NoError(t, err)
	return lg.Root()
}

// TestMillis checks the ADR-0197 Decision table, through the hook NewLogger sets.
func TestMillis(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{60 * time.Second, "60000"},
		{412 * time.Microsecond, "0.412"},
		{1234500 * time.Nanosecond, "1.235"},
		{-412 * time.Microsecond, "-0.412"},
		{400 * time.Nanosecond, "0"},
		{500 * time.Nanosecond, "0.001"},
		{-400 * time.Nanosecond, "0"},
		{1500 * time.Millisecond, "1500"},
		{1001 * time.Microsecond, "1.001"},
		{1100 * time.Microsecond, "1.1"},
		{math.MaxInt64, "9223372036854.775"},
		{math.MinInt64, "-9223372036854.775"},
	}
	for _, c := range cases {
		got := observability.ReplaceAttr(nil, slog.Duration("d", c.d))
		require.Equal(t, "d_ms", got.Key)
		require.Equal(t, json.Number(c.want), got.Value.Any(), "%d ns", int64(c.d))
	}
}

// scenario: measured-latency-keeps-fraction
func TestScenarioMeasuredLatencyKeepsFraction(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := newTestLogger(t, observability.FormatJSON, &buf)
	logger.Info("m", "latency", 412*time.Microsecond)
	logger.Info("m", "latency", 1234500*time.Nanosecond)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	require.Contains(t, lines[0], `"latency_ms":0.412`)
	require.Contains(t, lines[1], `"latency_ms":1.235`)
	for i, want := range []float64{0.412, 1.235} {
		var line struct {
			LatencyMs float64 `json:"latency_ms"`
		}
		require.NoError(t, json.Unmarshal([]byte(lines[i]), &line), "latency_ms is a JSON number")
		require.InDelta(t, want, line.LatencyMs, 1e-9)
		require.NotContains(t, lines[i], `"latency"`)
	}
}

// scenario: text-format-same-key
func TestScenarioTextFormatSameKey(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := newTestLogger(t, observability.FormatText, &buf)
	logger.Info("m", "timeout", 60*time.Second)
	logger.Info("m", "latency", 412*time.Microsecond)
	logger.Info("m", "latency", 1234500*time.Nanosecond)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 3)
	require.True(t, strings.HasSuffix(lines[0], " timeout_ms=60000"), lines[0])
	require.True(t, strings.HasSuffix(lines[1], " latency_ms=0.412"), lines[1])
	require.True(t, strings.HasSuffix(lines[2], " latency_ms=1.235"), lines[2])
}

type durationValuer time.Duration

func (d durationValuer) LogValue() slog.Value { return slog.DurationValue(time.Duration(d)) }

type groupValuer time.Duration

func (d groupValuer) LogValue() slog.Value {
	return slog.GroupValue(slog.Duration("wait", time.Duration(d)))
}

// scenario: no-nanoseconds-in-logs
func TestScenarioNoNanosecondsInLogs(t *testing.T) {
	t.Parallel()
	const d = 1234567891 * time.Nanosecond
	paths := map[string]func(*slog.Logger){
		"key-value":         func(l *slog.Logger) { l.Info("m", "wait", d) },
		"slog.Duration":     func(l *slog.Logger) { l.Info("m", slog.Duration("wait", d)) },
		"slog.Any":          func(l *slog.Logger) { l.Info("m", slog.Any("wait", d)) },
		"Logger.With":       func(l *slog.Logger) { l.With("wait", d).Info("m") },
		"slog.Group":        func(l *slog.Logger) { l.Info("m", slog.Group("g", "wait", d)) },
		"Logger.WithGroup":  func(l *slog.Logger) { l.WithGroup("g").Info("m", "wait", d) },
		"LogValuer":         func(l *slog.Logger) { l.Info("m", "wait", durationValuer(d)) },
		"LogValuer-group":   func(l *slog.Logger) { l.Info("m", "g", groupValuer(d)) },
		"v1alpha1.Duration": func(l *slog.Logger) { l.Info("m", "wait", v1.Duration(d)) },
	}
	handlers := map[string]func(io.Writer) *slog.Logger{
		"json": func(w io.Writer) *slog.Logger { return newTestLogger(t, observability.FormatJSON, w) },
		"text": func(w io.Writer) *slog.Logger { return newTestLogger(t, observability.FormatText, w) },
		"normalize-json": func(w io.Writer) *slog.Logger {
			return slog.New(observability.NewNormalizeHandler(slog.NewJSONHandler(w, nil)))
		},
		"normalize-text": func(w io.Writer) *slog.Logger {
			return slog.New(observability.NewNormalizeHandler(slog.NewTextHandler(w, nil)))
		},
	}
	want := regexp.MustCompile(`wait_ms"?[:=]1234\.568[ ,}\n]`)
	for hname, newLogger := range handlers {
		for pname, logIt := range paths {
			var buf bytes.Buffer
			logIt(newLogger(&buf))
			line := buf.String()
			require.Regexp(t, want, line, "%s via %s", hname, pname)
			require.NotContains(t, line, "1234567891", "%s via %s", hname, pname)
			require.NotContains(t, line, "1.234567891s", "%s via %s", hname, pname)
		}
	}
}

func TestDurationKeyAlreadyMs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	newTestLogger(t, observability.FormatJSON, &buf).Info("m", "wait_ms", 1500*time.Millisecond,
		slog.Group("retry", "wait", 1500*time.Millisecond))
	require.Contains(t, buf.String(), `"wait_ms":1500,"retry":{"wait_ms":1500}`)
	require.NotContains(t, buf.String(), "_ms_ms")
	require.Equal(t, "_ms", observability.ReplaceAttr(nil, slog.Duration("", time.Second)).Key)
}

func TestNormalizeHandlerTime(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 7, 22, 3, 35, 965999999, time.FixedZone("UTC+2", 2*60*60))
	var buf bytes.Buffer
	h := observability.NewNormalizeHandler(slog.NewJSONHandler(&buf, nil))
	require.NoError(t, h.Handle(context.Background(), slog.NewRecord(at, slog.LevelInfo, "m", 0)))
	require.Contains(t, buf.String(), `"time":"2026-10-07T20:03:35.965Z"`)
}
