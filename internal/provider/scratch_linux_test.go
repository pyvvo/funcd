package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/loopfs"
	"github.com/pyvvo/funcd/internal/provider"
)

// TestRemoveLeftoverScratchAsRoot: a scratch an earlier run left mounted is detached and removed before the start
// waits for space (ADR-0297 Decision 2).
func TestRemoveLeftoverScratchAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: run `just test-volumes-root`")
	}
	root, mountRoot := t.TempDir(), t.TempDir()
	image, dir := filepath.Join(root, "default", "lake.img"), filepath.Join(mountRoot, "default", "lake")
	require.NoError(t, loopfs.Create(context.Background(), image, 64<<20, ""))
	require.NoError(t, loopfs.Mount(image, dir))

	require.NoError(t, provider.RemoveLeftoverScratch(root, mountRoot))
	_, mounted := loopfs.MountedAt(dir)
	require.False(t, mounted)
	require.NoFileExists(t, image)
	require.NoDirExists(t, dir)
}
