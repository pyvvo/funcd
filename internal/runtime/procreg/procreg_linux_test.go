//go:build linux

package procreg_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/runtime/procreg"
)

// scenario: other-boot-entry-never-killed — an entry saved in another boot is never signalled at open, though a live
// process now has its pid, its start time and the token in argv, and the entry is cleared.
func TestScenarioOtherBootEntryNeverKilled(t *testing.T) {
	require.NotEqual(t, foreignBootID, mustBootID(t))
	dir := t.TempDir()
	live, e := startChild(t, "funcd-test", "--funcd-instance=default/f/r1")
	e.ID, e.Token, e.BootID = "default/f/r1", "--funcd-instance=default/f/r1", foreignBootID
	seed(t, dir, "workers", e)

	r, err := procreg.Open(dir, "workers")
	require.NoError(t, err)
	killed, err := r.Reap(context.Background(), 200*time.Millisecond)
	require.NoError(t, err)
	require.Zero(t, killed)
	require.True(t, alive(t, live.Process.Pid), "a process of another boot's entry is not signalled")
	require.Empty(t, savedEntries(t, dir, "workers"), "the entry is cleared")
	require.NoError(t, r.Close())
}
