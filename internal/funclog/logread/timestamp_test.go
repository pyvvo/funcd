package logread_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/funclog/compact"
	"github.com/pyvvo/funcd/internal/funclog/logread"
)

// scenario: every-timestamp-utc-millisecond — a log row stamped at …35.965999999 reads …35.965Z.
func TestEveryTimestampUTCMillisecond(t *testing.T) {
	b := memBucket(t)
	at := time.Date(2026, 10, 7, 20, 3, 35, 965999999, time.UTC).UnixNano()
	seedRaw(t, b, "default", "fn", "0", at, []compact.Row{row(at, "INFO", 9, "hello")})
	lines, err := logread.NewBlobReader(b).Read(context.Background(), logread.Query{Namespace: "default", Function: "fn"})
	require.NoError(t, err)
	require.Len(t, lines, 1)
	out, err := json.Marshal(lines[0])
	require.NoError(t, err)
	require.Contains(t, string(out), `"time":"2026-10-07T20:03:35.965Z"`)
}

// Two objects whose rows fall in one millisecond, listed in the reverse of their rows' order, read back in time order:
// the read sorts on the nanosecond time before Line.Time truncates it (ADR-0196 Decision 7).
func TestLinesWithinOneMillisecondKeepTimeOrder(t *testing.T) {
	b := memBucket(t)
	base := baseTime().UnixNano()
	seedRaw(t, b, "default", "fn", "0", base+1000, []compact.Row{row(base+900_000, "INFO", 9, "second")})
	seedRaw(t, b, "default", "fn", "0", base+2000, []compact.Row{row(base+100_000, "INFO", 9, "first")})
	lines, err := logread.NewBlobReader(b).Read(context.Background(), logread.Query{Namespace: "default", Function: "fn"})
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, bodies(lines))
	require.Equal(t, lines[0].Time, lines[1].Time, "both lines read the same millisecond")
}
