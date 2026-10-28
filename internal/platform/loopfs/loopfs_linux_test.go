package loopfs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/loopfs"
)

// TestLoopfsAsRoot makes a preallocated image, mounts it, and removes both (ADR-0294 Decision 6).
func TestLoopfsAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: run `just test-volumes-root`")
	}
	ctx := context.Background()
	dir := t.TempDir()
	image, mount := filepath.Join(dir, "x.img"), filepath.Join(dir, "x")
	require.NoError(t, loopfs.Create(ctx, image, 64<<20, ""))
	fi, err := os.Stat(image)
	require.NoError(t, err)
	require.Equal(t, int64(64<<20), fi.Size())
	require.Error(t, loopfs.Create(ctx, image, 64<<20, ""), "an image is never made over another")

	require.NoError(t, loopfs.Mount(image, mount))
	_, ok := loopfs.MountedAt(mount)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(filepath.Join(mount, "catalog.ducklake"), []byte("x"), 0o600))

	require.NoError(t, loopfs.Remove(image, mount))
	_, ok = loopfs.MountedAt(mount)
	require.False(t, ok)
	require.NoFileExists(t, image)
	require.NoDirExists(t, mount)
	require.NoError(t, loopfs.Remove(image, mount), "removing what is gone is a no-op")
}
