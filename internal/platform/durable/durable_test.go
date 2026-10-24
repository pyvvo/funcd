package durable_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/durable"
)

// step is one traced step of a durable write.
type step struct {
	op   durable.Op
	path string
}

func traced() (durable.Disk, *[]step) {
	var steps []step
	return durable.Disk{Trace: func(op durable.Op, path string) { steps = append(steps, step{op, path}) }}, &steps
}

// WriteFile syncs the temporary file, renames it into place and syncs the directory, in that order, and leaves the
// file at its mode with no temporary file beside it.
func TestWriteFileSyncsBeforeAndAfterTheRename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, steps := traced()
	require.NoError(t, d.WriteFile(dir, "master.key", []byte("secret"), 0o640))

	require.Len(t, *steps, 3)
	require.Equal(t, durable.SyncFile, (*steps)[0].op)
	require.True(t, strings.HasPrefix((*steps)[0].path, filepath.Join(dir, "master.key.tmp-")), (*steps)[0].path)
	require.Equal(t, step{durable.Rename, filepath.Join(dir, "master.key")}, (*steps)[1])
	require.Equal(t, step{durable.SyncDir, dir}, (*steps)[2])
	got, err := os.ReadFile(filepath.Join(dir, "master.key")) //nolint:gosec // the test's own temp dir
	require.NoError(t, err)
	require.Equal(t, "secret", string(got))
	info, err := os.Stat(filepath.Join(dir, "master.key"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary file is left")
}

// SyncTree syncs every regular file and directory under its root, the root included.
func TestSyncTreeSyncsEveryFileAndDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "lib", "ext"), 0o750))
	for _, f := range []string{"main.py", "lib/a.py", "lib/ext/b.so"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, f), []byte(f), 0o600))
	}
	d, steps := traced()
	require.NoError(t, d.SyncTree(root))

	synced := map[step]bool{}
	for _, s := range *steps {
		synced[s] = true
	}
	require.Len(t, synced, 6)
	for _, dir := range []string{"", "lib", "lib/ext"} {
		require.True(t, synced[step{durable.SyncDir, filepath.Join(root, dir)}], "dir %q", dir)
	}
	for _, f := range []string{"main.py", "lib/a.py", "lib/ext/b.so"} {
		require.True(t, synced[step{durable.SyncFile, filepath.Join(root, f)}], "file %q", f)
	}
}

// MkdirAll syncs the parent of each directory it makes, outermost first, and nothing for a directory that exists.
func TestMkdirAllSyncsTheParentOfEachDirectoryItMakes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d, steps := traced()
	require.NoError(t, d.MkdirAll(filepath.Join(root, "a", "b"), 0o750))
	require.Equal(t, []step{
		{durable.SyncDir, root},
		{durable.SyncDir, filepath.Join(root, "a")},
	}, *steps)
	require.DirExists(t, filepath.Join(root, "a", "b"))

	*steps = nil
	require.NoError(t, d.MkdirAll(filepath.Join(root, "a"), 0o750))
	require.Empty(t, *steps, "an existing directory needs no sync")
}
