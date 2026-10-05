//go:build unix

package main

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

// A hangup stops funcd gracefully: it does not reach the private containerd in its own process group, so funcd must
// stop the workers and then containerd, or both outlive it.
func TestHangupStopsGracefully(t *testing.T) {
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGHUP) // the test binary survives the hangup whatever stopSignals returns
	defer signal.Stop(guard)
	ctx, stop := signal.NotifyContext(context.Background(), stopSignals()...)
	defer stop()

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("a hangup did not start the graceful stop")
	}
}

// Under nohup a hangup stays ignored: handling it would let a closed terminal stop a funcd started with nohup.
func TestNohupKeepsHangupIgnored(t *testing.T) {
	if os.Getenv("FUNCD_TEST_NOHUP") == "" {
		cmd := exec.Command("nohup", os.Args[0], "-test.run=^TestNohupKeepsHangupIgnored$")
		cmd.Env = append(os.Environ(), "FUNCD_TEST_NOHUP=1")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return
	}
	require.True(t, signal.Ignored(syscall.SIGHUP), "nohup did not ignore SIGHUP")
	_, stop := signal.NotifyContext(context.Background(), stopSignals()...)
	defer stop()
	require.True(t, signal.Ignored(syscall.SIGHUP), "funcd took over the hangup nohup ignores")
}
