package function

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/pooling"
)

// writePoolManifest writes into a 0700 dir it creates, and refuses a dir open to others or a symlink.
func TestWritePoolManifestRefusesUnsafeDir(t *testing.T) {
	t.Parallel()
	key := pooling.PoolKey{Namespace: "default", Runtime: "nodejs22", Worker: "w", AccessHash: "0123456789abcdef"}
	rows := []poolManifestEntry{{Name: "a", Env: map[string]string{"K": "v"}}}

	dir := filepath.Join(t.TempDir(), "pool")
	path, err := writePoolManifest(dir, key, rows)
	require.NoError(t, err)
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	_, err = writePoolManifest(dir, key, rows)
	require.NoError(t, err, "a rewrite replaces the file")

	open := filepath.Join(t.TempDir(), "open")
	require.NoError(t, os.Mkdir(open, 0o700))
	require.NoError(t, os.Chmod(open, 0o755))
	_, err = writePoolManifest(open, key, rows)
	require.Error(t, err, "a dir open to others is refused")

	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))
	_, err = writePoolManifest(link, key, rows)
	require.Error(t, err, "a symlinked dir is refused")
}

// NewReconciler empties a configured manifest dir, and Close removes only a dir it created.
func TestPoolManifestDirIsEmptiedAndOwnedTempRemoved(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "pool")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stale.json"), []byte("[]"), 0o600))
	got, own, err := preparePoolManifestDir(dir)
	require.NoError(t, err)
	require.False(t, own)
	entries, err := os.ReadDir(got)
	require.NoError(t, err)
	require.Empty(t, entries, "a stale manifest from an earlier run is removed")

	tmp, own, err := preparePoolManifestDir("")
	require.NoError(t, err)
	require.True(t, own)
	r := &Reconciler{poolManifestDir: tmp, ownManifestDir: own}
	require.NoError(t, r.Close())
	_, err = os.Stat(tmp)
	require.True(t, os.IsNotExist(err))
}
