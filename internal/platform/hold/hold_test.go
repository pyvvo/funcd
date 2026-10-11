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

// TestReleaseNamespace (ADR-0222 Decision 8): restore run's offline data marker holds its namespace alone across an
// Open; another reason's file is not read; after the release HeldIn is false and ReleasedAtIn is the release time,
// also after a restart; a second release is a Conflict.
func TestReleaseNamespace(t *testing.T) {
	dir := t.TempDir()
	m := hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}
	require.NoError(t, hold.MarkNamespaces(dir, []v1.NamespaceName{"orders"}, m))
	require.NoError(t, os.WriteFile(filepath.Join(dir, hold.NamespaceDir, "web.other"), []byte("{}"), 0o600))
	require.Equal(t, fault.Invalid, fault.KindOf(hold.MarkNamespaces(dir, []v1.NamespaceName{"web"}, hold.Marker{Reason: "other"})))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	require.False(t, h.Held())
	require.True(t, h.HeldIn("orders"))
	require.False(t, h.HeldIn("web"), "only the restore and data reasons are read")
	require.True(t, h.ReleasedAtIn("orders").IsZero())

	before := time.Now().Truncate(time.Millisecond)
	require.NoError(t, h.ReleaseNamespace("orders", hold.ReasonData))
	require.False(t, h.HeldIn("orders"))
	at := h.ReleasedAtIn("orders")
	require.False(t, at.Before(before))
	require.True(t, h.ReleasedAtIn("web").IsZero())
	require.Equal(t, fault.Conflict, fault.KindOf(h.ReleaseNamespace("orders", hold.ReasonData)))

	h, err = hold.Open(dir)
	require.NoError(t, err)
	require.False(t, h.HeldIn("orders"))
	require.Equal(t, at, h.ReleasedAtIn("orders"), "a restart reads the release time back")

	require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	h, err = hold.Open(dir)
	require.NoError(t, err)
	require.True(t, h.HeldIn("web"), "a held platform holds every namespace")
	require.NoError(t, h.HoldNamespace("web", hold.Marker{Reason: hold.ReasonRestore}))
	require.NoError(t, h.Release(time.Now().Add(time.Hour)))
	require.True(t, h.HeldIn("web"))
	require.False(t, h.HeldIn("orders"))
	require.Equal(t, h.ReleasedAt(), h.ReleasedAtIn("orders"), "the later of the two releases")
	require.False(t, hold.NeverNamespace.HeldIn("web"))
}

// TestReleaseNamespaceMarker (ADR-0222 Decision 8): a restore marker names its Restore across an Open; a release of a
// marker another Restore rewrote since is a Conflict that keeps the namespace held.
func TestReleaseNamespaceMarker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h, err := hold.Open(dir)
	require.NoError(t, err)
	require.NoError(t, h.HoldNamespace("web", hold.Marker{Reason: hold.ReasonRestore, Since: v1.NewTimestamp(time.Now()), Restore: "first"}))
	first := h.NamespaceMarkers()["web"][hold.ReasonRestore]
	require.NoError(t, h.HoldNamespace("web", hold.Marker{Reason: hold.ReasonRestore, Since: v1.NewTimestamp(time.Now()), Restore: "second"}))
	require.Equal(t, fault.Conflict, fault.KindOf(h.ReleaseNamespaceMarker("web", first)))
	require.True(t, h.HeldIn("web"))

	h, err = hold.Open(dir)
	require.NoError(t, err)
	second := h.NamespaceMarkers()["web"][hold.ReasonRestore]
	require.Equal(t, v1.ObjectName("second"), second.Restore)
	require.NoError(t, h.ReleaseNamespaceMarker("web", second))
	require.False(t, h.HeldIn("web"))
	require.Empty(t, h.NamespaceMarkers())
}
