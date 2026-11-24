//go:build linux

package containerd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0352 Decision 2: a host-only worker with no egress fence is refused while egress is off, before its netns is read;
// while egress is on it goes on to its netns, whose fence holds the input chain alone. The packets are the egress
// lane's.
func TestHostOnlyWorkerWithNoFenceNeedsEgressOn(t *testing.T) {
	spec := runtime.WorkerSpec{HostOnly: true}
	err := (&driver{}).writeNetns("/nonexistent", spec)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.Contains(t, err.Error(), "needs an egress fence")

	err = (&driver{cfg: Config{Egress: true}}).writeNetns("/nonexistent", spec)
	require.Error(t, err, "there is no netns at the path")
	require.NotContains(t, err.Error(), "needs an egress fence", "egress on, it is not refused")
}
