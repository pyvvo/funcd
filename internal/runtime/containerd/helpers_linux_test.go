//go:build linux

package containerd

import (
	"testing"

	containerd "github.com/containerd/containerd/v2/client"

	"github.com/pyvvo/funcd/internal/runtime"
)

// TestMapStateExitStatus checks that a task that exited non-zero is Failed and a zero exit is Stopped (ADR-0142).
// Non-integration (no containerd needed).
func TestMapStateExitStatus(t *testing.T) {
	cases := []struct {
		st   containerd.Status
		want runtime.State
	}{
		{containerd.Status{Status: containerd.Created}, runtime.StateCreated},
		{containerd.Status{Status: containerd.Running}, runtime.StateRunning},
		{containerd.Status{Status: containerd.Stopped, ExitStatus: 0}, runtime.StateStopped},
		{containerd.Status{Status: containerd.Stopped, ExitStatus: 137}, runtime.StateFailed},
		{containerd.Status{Status: containerd.Unknown}, runtime.StateFailed},
	}
	for _, c := range cases {
		if got := mapState(c.st); got != c.want {
			t.Errorf("mapState(%v, exit %d) = %q, want %q", c.st.Status, c.st.ExitStatus, got, c.want)
		}
	}
}
