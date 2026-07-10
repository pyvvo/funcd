//go:build !linux

package network

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// scenario: non-linux-noop — on a non-Linux host, New(true) cannot program netns/nftables, so it falls
// back to the no-op (logged once): Apply/Remove program nothing and never error.
func TestScenarioNonLinuxNoop(t *testing.T) {
	m := New(true)
	require.IsType(t, noop{}, m, "enabled on a non-Linux host ⇒ the no-op Manager")
	require.NoError(t, m.Apply(context.Background(), goodPolicy()))
	require.NoError(t, m.Remove(context.Background()))
}
