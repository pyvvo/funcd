//go:build e2e && soak

package funcd_test

import (
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/pyvvo/funcd/internal/platform/observability"
)

// TestSoakUpgradeTunnel runs one ADR-0181 soak stage of FUNCD_SOAK_DURATION through the real edge chain,
// dataplane.Handler and activator.New, so the real 5-minute idle timeout applies: an active tunnel stays open for
// the whole stage with every echo matched, an idle tunnel is closed 5 min to 5 min 10 s after it opened with both
// ends seeing the close, and no goroutine or open file is left behind. Never run by just ci, just ci-full or CI.
func TestSoakUpgradeTunnel(t *testing.T) {
	raw := os.Getenv("FUNCD_SOAK_DURATION")
	if raw == "" {
		t.Skip("FUNCD_SOAK_DURATION is unset")
	}
	stage, err := time.ParseDuration(raw)
	require.NoError(t, err)

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv, up := edgeChainServer(t, observability.NewFromProviders(nil, nil), logger)
	baseGoroutines, baseFiles := runtime.NumGoroutine(), openFiles(t)
	t.Logf("baseline: goroutines=%d open files=%d", baseGoroutines, baseFiles)

	start := time.Now()
	idle := dialEdgeWS(t, srv, "mode=sink")
	idleClosed := make(chan time.Time, 1)
	go func() {
		var frame string
		for websocket.Message.Receive(idle, &frame) == nil {
		}
		idleClosed <- time.Now()
	}()

	active := dialEdgeWS(t, srv, "mode=push&every=60s")
	frames, activeErr := make(chan string, 16), make(chan error, 1)
	go func() {
		for {
			var frame string
			if err := websocket.Message.Receive(active, &frame); err != nil {
				activeErr <- err
				return
			}
			frames <- frame
		}
	}()

	send := time.NewTimer(30 * time.Second)
	defer send.Stop()
	end := time.NewTimer(stage)
	defer end.Stop()
	var (
		pending          []string
		sent, pushes     int
		stageOver, idled bool
	)
	for !stageOver || !idled {
		select {
		case <-send.C:
			sent++
			frame := "f" + strconv.Itoa(sent)
			pending = append(pending, frame)
			require.NoError(t, websocket.Message.Send(active, frame))
			send.Reset(60 * time.Second)
		case frame := <-frames:
			if strings.HasPrefix(frame, "push:") {
				pushes++
				continue
			}
			require.NotEmpty(t, pending, "an echo arrived for no frame: %q", frame)
			require.Equal(t, "echo:"+pending[0], frame)
			pending = pending[1:]
		case err := <-activeErr:
			t.Fatalf("the active tunnel closed after %s: %v", time.Since(start), err)
		case at := <-idleClosed:
			after := at.Sub(start)
			t.Logf("idle tunnel closed after %s", after)
			require.GreaterOrEqual(t, after, 5*time.Minute)
			require.LessOrEqual(t, after, 5*time.Minute+10*time.Second)
			require.Eventually(t, func() bool { return up.Ended() >= 1 }, 5*time.Second, 50*time.Millisecond,
				"the upstream end of the idle tunnel saw the close")
			idled = true
		case <-end.C:
			stageOver = true
		}
	}
	t.Logf("active tunnel: %d frames echoed, %d pushes, open after %s", sent, pushes, time.Since(start))

	require.NoError(t, active.Close())
	require.Eventually(t, func() bool { return up.Ended() >= 2 }, 5*time.Second, 50*time.Millisecond)
	time.Sleep(2 * time.Second)
	goroutines, files := runtime.NumGoroutine(), openFiles(t)
	t.Logf("end: goroutines=%d open files=%d", goroutines, files)
	require.LessOrEqual(t, goroutines, baseGoroutines+5)
	require.LessOrEqual(t, files, baseFiles+2)
}

func openFiles(t *testing.T) int {
	t.Helper()
	// Readdirnames, not ReadDir: on macOS a stat of each /dev/fd entry fails once the listing's own fd is closed.
	d, err := os.Open("/dev/fd")
	require.NoError(t, err)
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(-1)
	require.NoError(t, err)
	return len(names)
}
