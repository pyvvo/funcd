//go:build !linux

package containerd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/runtime/containerd"
)

// scenario: containerd-requires-linux — on a non-Linux build, New returns a typed
// fault.Unavailable, so the process driver is the cross-platform path.
func TestScenarioContainerdRequiresLinux(t *testing.T) {
	t.Parallel()
	rt, err := containerd.New(containerd.Config{})
	require.Error(t, err)
	require.Nil(t, rt)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
}
