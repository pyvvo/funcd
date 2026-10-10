package hold_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
)

// The marker makes the platform held across an Open; the release persists its time, deletes the marker and lifts the
// gate live; a second release is a Conflict. restore.inprogress refuses Open, and only its own command reruns.
func TestHoldMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	h, err := hold.Open(dir)
	require.NoError(t, err)
	require.False(t, h.Held())
	require.True(t, h.ReleasedAt().IsZero())
	require.False(t, hold.Never.Held())
	require.True(t, hold.Never.ReleasedAt().IsZero())

	m := hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))}
	require.NoError(t, hold.Write(dir, m))
	info, err := os.Stat(filepath.Join(dir, hold.MarkerFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	h, err = hold.Open(dir)
	require.NoError(t, err)
	require.True(t, h.Held())
	got, ok := h.Marker()
	require.True(t, ok)
	require.Equal(t, m, got)

	now := time.Date(2026, 10, 9, 8, 30, 0, 0, time.UTC)
	require.NoError(t, h.Release(now))
	require.False(t, h.Held())
	require.Equal(t, now, h.ReleasedAt())
	require.NoFileExists(t, filepath.Join(dir, hold.MarkerFile))
	h, err = hold.Open(dir)
	require.NoError(t, err)
	require.False(t, h.Held())
	require.Equal(t, now, h.ReleasedAt(), "a restart reads the release time back")
	require.Equal(t, fault.Conflict, fault.KindOf(h.Release(now)))

	require.NoError(t, hold.Begin(dir, "run"))
	_, err = hold.Open(dir)
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.ErrorContains(t, err, "restore run")
	require.Equal(t, fault.Conflict, fault.KindOf(hold.Begin(dir, "kv")))
	require.NoError(t, hold.Begin(dir, "run"), "a rerun of the same command")
	require.NoError(t, hold.End(dir))
	_, err = hold.Open(dir)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, hold.MarkerFile), []byte("{"), 0o600))
	_, err = hold.Open(dir)
	require.Equal(t, fault.Internal, fault.KindOf(err))
}

// Own gives every entry under a root the reference's group where it differs, skips a missing root, and leaves what
// already matches. Without root a process can move a file only between its own groups, which is the case tested.
func TestOwn(t *testing.T) {
	ref := t.TempDir()
	root := filepath.Join(ref, "store")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o700))
	file := filepath.Join(root, "sub", "000001.vlog")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	require.NoError(t, hold.Own(ref, root, filepath.Join(ref, "absent")))

	want := stat(t, ref)
	other := -1
	groups, err := os.Getgroups()
	require.NoError(t, err)
	for _, g := range groups {
		if uint32(g) != want.Gid { //nolint:gosec // a group id fits
			other = g
			break
		}
	}
	if other < 0 {
		t.Log("this user has one group: only the no-op path ran")
		return
	}
	require.NoError(t, os.Lchown(file, -1, other))
	require.NotEqual(t, want.Gid, stat(t, file).Gid)
	require.NoError(t, hold.Own(ref, root))
	require.Equal(t, want.Gid, stat(t, file).Gid)
	require.Equal(t, want.Gid, stat(t, filepath.Join(root, "sub")).Gid)
}

func stat(t *testing.T, p string) *syscall.Stat_t {
	t.Helper()
	info, err := os.Lstat(p)
	require.NoError(t, err)
	st, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	return st
}
