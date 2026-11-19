package provider_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/provider"
)

// snapshotQuiet is a Backup's quiet (ADR-0333 Decision 2): 2 s.
const snapshotQuiet = 2 * time.Second

// engineFiles is the file access of the add-on object default/login under a new storage root, and its directory.
func engineFiles(t *testing.T) (provider.EngineFiles, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "addonobject", "default", "login")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return provider.NewEngineFiles(root, v1.KindAddonObject), dir
}

// file writes name under dir with data, last changed age ago.
func file(t *testing.T, dir, name, data string, age time.Duration) {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte(data), 0o600))
	at := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(p, at, at))
}

func read(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(b)
}

// The engine runtime and the file access are ADR-0333's AddonFiles.
func TestEngineFilesAreAddonFiles(t *testing.T) {
	var _ workload.AddonFiles = provider.EngineFiles{}
	rt, err := provider.NewRuntime(provider.Deps{Runtime: newFakeRuntime("10.0.0.1", 8080), OwnerKind: v1.KindAddonObject})
	require.NoError(t, err)
	_, ok := rt.(workload.AddonFiles)
	require.True(t, ok)
}

// The quiet rule (ADR-0333 Decision 2): the newest regular file whose name does not end in ~ and that has not changed
// for quiet; a fresher file, a link, a folder and a name that is no file name are no candidates.
func TestEngineFilesNewestQuietFile(t *testing.T) {
	t.Parallel()
	f, dir := engineFiles(t)
	ctx := context.Background()
	snaps := filepath.Join(dir, "state_machine", "backups")

	_, _, err := f.Newest(ctx, "default", "login", "state_machine/backups", snapshotQuiet)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "no folder yet")
	require.NoError(t, os.MkdirAll(snaps, 0o700))
	_, _, err = f.Newest(ctx, "default", "login", "state_machine/backups", snapshotQuiet)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "an empty folder")

	file(t, snaps, "backup_1.sqlite", "one", time.Minute)
	file(t, snaps, "backup_2.sqlite", "two", 30*time.Second)
	file(t, snaps, "backup_3.sqlite", "three", 0)
	file(t, snaps, "backup_4.sqlite~", "four", time.Second)
	file(t, snaps, ".backup_5.sqlite", "five", time.Second)
	outside := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(snaps, "backup_6.sqlite")))
	require.NoError(t, os.Mkdir(filepath.Join(snaps, "backup_7.sqlite"), 0o700))

	name, rc, err := f.Newest(ctx, "default", "login", "state_machine/backups", snapshotQuiet)
	require.NoError(t, err)
	require.Equal(t, "backup_2.sqlite", name, "the newest quiet file; backup_3 changed within the quiet")
	require.Equal(t, "two", read(t, rc))

	name, rc, err = f.Newest(ctx, "default", "login", "state_machine/backups", 0)
	require.NoError(t, err)
	require.Equal(t, "backup_3.sqlite", name, "with no quiet, the newest regular file")
	require.Equal(t, "three", read(t, rc))
}

// Put writes a new copy owned by the engine's user, creating its folder; Clear empties the folder and keeps it.
func TestEngineFilesPutAndClear(t *testing.T) {
	t.Parallel()
	f, dir := engineFiles(t)
	ctx := context.Background()
	uid, gid := os.Geteuid(), os.Getegid()

	require.NoError(t, f.Put(ctx, "default", "login", "restore", "backup_2.sqlite", strings.NewReader("two"), uid, gid))
	p := filepath.Join(dir, "restore", "backup_2.sqlite")
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "two", string(b))
	fi, err := os.Stat(p)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	fi, err = os.Stat(filepath.Join(dir, "restore"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())

	err = f.Put(ctx, "default", "login", "restore", "backup_2.sqlite", strings.NewReader("again"), uid, gid)
	require.Error(t, err, "a copy is a new file")

	require.NoError(t, f.Clear(ctx, "default", "login", "restore"))
	ents, err := os.ReadDir(filepath.Join(dir, "restore"))
	require.NoError(t, err)
	require.Empty(t, ents, "the folder is empty and kept")
	require.NoError(t, f.Clear(ctx, "default", "login", "restore"), "an empty folder")
	require.NoError(t, f.Clear(ctx, "default", "login", "nothing-here"), "no folder")
	require.NoError(t, f.Clear(ctx, "default", "gone", "restore"), "no storage")
}

// Every call stays inside the engine's storage: a file is one segment, a folder lies under the storage, and a link
// out of it is refused by the os.Root (ADR-0333 Decision 6).
func TestEngineFilesStayInsideTheStorage(t *testing.T) {
	t.Parallel()
	f, dir := engineFiles(t)
	ctx := context.Background()
	uid, gid := os.Geteuid(), os.Getegid()
	for _, name := range []string{"../escape", "a/b", ".hidden", ""} {
		err := f.Put(ctx, "default", "login", "restore", name, strings.NewReader("x"), uid, gid)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%q", name)
	}
	for _, folder := range []string{"../other", "/abs", ".", "", "a/../.."} {
		err := f.Put(ctx, "default", "login", folder, "copy", strings.NewReader("x"), uid, gid)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%q", folder)
		_, _, err = f.Newest(ctx, "default", "login", folder, 0)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%q", folder)
	}
	_, _, err := f.Newest(ctx, "default", "Bad_Name", "restore", 0)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a name that is no DNS label")

	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "linked")))
	require.Error(t, f.Put(ctx, "default", "login", "linked", "copy", strings.NewReader("x"), uid, gid))
	ents, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, ents, "nothing is written through a link out of the storage")
	require.Error(t, f.Clear(ctx, "default", "login", "linked"))

	none := provider.NewEngineFiles("", v1.KindAddonObject)
	_, _, err = none.Newest(ctx, "default", "login", "restore", 0)
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "no storage root")
}
