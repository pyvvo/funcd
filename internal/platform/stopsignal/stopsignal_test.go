//go:build unix

package stopsignal

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A hangup stops a funcd process gracefully: it does not reach the workers or the private containerd in their own
// process groups, so the process must stop them, or they outlive it.
func TestHangupStopsGracefully(t *testing.T) {
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGHUP) // the test binary survives the hangup whatever Signals returns
	defer signal.Stop(guard)
	ctx, stop := signal.NotifyContext(context.Background(), Signals()...)
	defer stop()

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("a hangup did not start the graceful stop")
	}
}

// Under nohup a hangup stays ignored: handling it would let a closed terminal stop a process started with nohup.
func TestNohupKeepsHangupIgnored(t *testing.T) {
	if os.Getenv("FUNCD_TEST_NOHUP") == "" {
		cmd := exec.Command("nohup", os.Args[0], "-test.run=^TestNohupKeepsHangupIgnored$")
		cmd.Env = append(os.Environ(), "FUNCD_TEST_NOHUP=1")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return
	}
	require.True(t, signal.Ignored(syscall.SIGHUP), "nohup did not ignore SIGHUP")
	_, stop := signal.NotifyContext(context.Background(), Signals()...)
	defer stop()
	require.True(t, signal.Ignored(syscall.SIGHUP), "the process took over the hangup nohup ignores")
}
