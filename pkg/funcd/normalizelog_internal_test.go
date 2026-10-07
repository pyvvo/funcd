package funcd

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// scenario: embedder-opts-in (ADR-0197) — WithNormalizedLogFields brings a WithLogger logger to the duration rule and
// a UTC millisecond time; without it the embedder's handler writes nanoseconds and the record's own zone. The record
// carries a UTC+2 time, as on a host in that zone.
func TestScenarioEmbedderOptsIn(t *testing.T) {
	at := time.Date(2026, 10, 7, 22, 3, 35, 965999999, time.FixedZone("UTC+2", 2*60*60))
	logLine := func(opts ...Option) string {
		var logs lockedWriter
		p, err := New(append([]Option{InMemory(), WithLogger(slog.New(slog.NewJSONHandler(&logs, nil)))}, opts...)...)
		require.NoError(t, err)
		t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
		before := logs.String()
		r := slog.NewRecord(at, slog.LevelWarn, "probe", 0)
		r.AddAttrs(slog.Duration("timeout", 60*time.Second))
		require.NoError(t, p.logger.Handler().Handle(context.Background(), r))
		line, ok := strings.CutPrefix(logs.String(), before)
		require.True(t, ok)
		return line
	}

	normalized := logLine(WithNormalizedLogFields())
	require.Contains(t, normalized, `"time":"2026-10-07T20:03:35.965Z"`)
	require.Contains(t, normalized, `"timeout_ms":60000`)
	require.NotContains(t, normalized, `"timeout":`)

	plain := logLine()
	require.Contains(t, plain, `"time":"2026-10-07T22:03:35.965999999+02:00"`)
	require.Contains(t, plain, `"timeout":60000000000`)
}
