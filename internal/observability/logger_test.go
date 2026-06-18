package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/observability"
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
