//go:build e2e && soak

package funcd_test

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// soakBeat is the active stream's gap between events, the gap the guide advises for an SSE comment line.
const soakBeat = 30 * time.Second

// TestSoakHTTPFunctionStream runs one stage of FUNCD_SOAK_DURATION through the platform with its real limits, the
// Function's spec.timeout unset: an http Function's SSE stream with an event every soakBeat, and one silent from its
// first event to its last at the stage's end, each stay open and end where the app ends them. Never run by just ci,
// just ci-full or CI.
func TestSoakHTTPFunctionStream(t *testing.T) {
	raw := os.Getenv("FUNCD_SOAK_DURATION")
	if raw == "" {
		t.Skip("FUNCD_SOAK_DURATION is unset")
	}
	stage, err := time.ParseDuration(raw)
	require.NoError(t, err)

	e := startGC(t)
	ref, digest := e.pushStream(t, "v1")
	e.apply(t, streamFn(ref, digest, 0))
	waitReady(t, e.c, ticker)
	t.Logf("baseline: goroutines=%d open files=%d", runtime.NumGoroutine(), openFiles(t))

	beats := int(stage/soakBeat) + 1
	opened := time.Now()
	activeEvents, activeEnd := e.openStream(t, fmt.Sprintf("/function/%s/events?gap=%d&count=%d", ticker, soakBeat.Milliseconds(), beats))
	silentEvents, silentEnd := e.openStream(t, fmt.Sprintf("/function/%s/events?gap=%d&count=2", ticker, stage.Milliseconds()))

	active, err := readStream(t, activeEvents, activeEnd, stage+time.Minute)
	require.ErrorIs(t, err, io.EOF, "the active stream ends where the app ends it")
	require.Len(t, active, beats)
	t.Logf("active stream: %d events, the last %s after the open, longest gap %s", len(active),
		active[len(active)-1].at.Sub(opened).Round(time.Millisecond), longestGap(active).Round(time.Millisecond))

	silent, err := readStream(t, silentEvents, silentEnd, time.Minute)
	require.ErrorIs(t, err, io.EOF, "the silent stream ends where the app ends it")
	require.Len(t, silent, 2)
	quiet := silent[1].at.Sub(silent[0].at)
	t.Logf("silent stream: no byte for %s", quiet.Round(time.Millisecond))
	require.GreaterOrEqual(t, quiet, stage-time.Second)

	http.DefaultClient.CloseIdleConnections()
	time.Sleep(2 * time.Second)
	t.Logf("end: goroutines=%d open files=%d", runtime.NumGoroutine(), openFiles(t))
}
