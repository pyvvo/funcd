//go:build linux

package containerd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/moby/sys/mountinfo"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestBootDirTmpfsAsRoot is the boot half of scenario boot-and-log-dirs-bounded: a worker's boot dir is a 1 MiB tmpfs,
// a 2 MiB write there fails, and removing it, or the boot sweep's unmountUnder, leaves no mount behind (ADR-0294).
func TestBootDirTmpfsAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: run `just test-volumes-root`")
	}
	d := &driver{bootRoot: t.TempDir(), bootTmpfs: true}
	dir, err := d.makeBootDir("default", "w-1.r0")
	require.NoError(t, err)
	var st unix.Statfs_t
	require.NoError(t, unix.Statfs(dir, &st))
	require.Equal(t, int64(unix.TMPFS_MAGIC), int64(st.Type))
	require.Equal(t, int64(bootDirBytes), int64(st.Blocks)*st.Bsize)
	require.ErrorIs(t, os.WriteFile(filepath.Join(dir, "big"), make([]byte, 2<<20), 0o600), syscall.ENOSPC)
	require.NoError(t, removeBootDir(dir))
	require.NoDirExists(t, dir)

	left, err := d.makeBootDir("default", "w-2.r0")
	require.NoError(t, err)
	require.NoError(t, unmountUnder(d.bootRoot), "the boot sweep detaches a mount that no container names")
	mounted, err := mountinfo.Mounted(left)
	require.NoError(t, err)
	require.False(t, mounted)
}
