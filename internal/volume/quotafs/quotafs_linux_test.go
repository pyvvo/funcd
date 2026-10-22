package quotafs

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/volume"
)

// The image, its mount and its quotas need root; `just test-volumes-root` runs these tests as root in funcd-linux-tests.
func needRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root: run `just test-volumes-root`")
	}
}

const nobody = 65534

// asNobody runs a shell command as uid and gid 65534, without capabilities, in dir. Its 0700 root-owned parents
// stop nobody, as a bind of the leaf alone does in a worker, so dir reaches the shell as an open descriptor.
func asNobody(script, dir string) ([]byte, error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	cmd := exec.Command("/bin/sh", "-c", "cd /proc/self/fd/3 && "+script)
	cmd.ExtraFiles = []*os.File{d}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: nobody, Gid: nobody}}
	return cmd.CombinedOutput()
}

func openFS(t *testing.T, capacity int64) (*FS, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "quotafs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := Config{Image: filepath.Join(dir, "volumes.img"), Mount: filepath.Join(dir, "volumes"), CapacityBytes: capacity}
	f, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f, dir
}

// TestScenarioWritePastSizeFails: a 1 MiB volume refuses a 2 MiB write, and what was written before stays readable.
// scenario: write-past-size-fails
func TestScenarioWritePastSizeFails(t *testing.T) {
	needRoot(t)
	f, _ := openFS(t, 64<<20)
	// A writer with CAP_SYS_RESOURCE passes any quota, so the volume belongs to nobody (65534), who writes; a worker's
	// default capabilities lack CAP_SYS_RESOURCE, root in it included.
	m, err := volume.New(volume.Options{Root: f.Root(), Limiter: f, CapacityBytes: 64 << 20, UID: nobody, GID: nobody})
	if err != nil {
		t.Fatal(err)
	}
	id := volume.ID{Namespace: "shop", Function: "cart", Name: "state"}
	dir, err := m.Reserve(context.Background(), id, "uid-1", 1<<20, v1.DeletionRetain)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if out, err := asNobody("printf kept > before", dir); err != nil {
		t.Fatalf("a small write: %v: %s", err, out)
	}
	out, err := asNobody("dd if=/dev/zero of=big bs=1M count=2 conv=fsync", dir)
	if err == nil || !bytes.Contains(out, []byte("quota")) && !bytes.Contains(out, []byte("No space")) {
		t.Fatalf("a 2 MiB write into a 1 MiB volume = %v: %s, want a quota or no-space failure", err, out)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "before")); err != nil || !bytes.Equal(got, []byte("kept")) {
		t.Fatalf("the earlier file = %q, %v", got, err)
	}
}

// TestImageGrowsAndRefusesShrink (Decision 3): a larger capacity grows the image at the next Open, keeping the
// volumes; a smaller one fails Invalid.
func TestImageGrowsAndRefusesShrink(t *testing.T) {
	needRoot(t)
	f, dir := openFS(t, 32<<20)
	if err := os.WriteFile(filepath.Join(f.Root(), "mark"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Image: filepath.Join(dir, "volumes.img"), Mount: filepath.Join(dir, "volumes"), CapacityBytes: 48 << 20}
	g, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open larger: %v", err)
	}
	if _, err := os.Stat(filepath.Join(g.Root(), "mark")); err != nil {
		t.Fatalf("the file after growing: %v", err)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(g.Root(), &st); err != nil || int64(st.Blocks)*st.Bsize < 38<<20 {
		t.Fatalf("grown filesystem has %d bytes (%v), want about 48 MiB", int64(st.Blocks)*st.Bsize, err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.CapacityBytes = 16 << 20
	if _, err := Open(context.Background(), cfg); err == nil {
		t.Fatal("Open with a smaller capacity succeeded, want Invalid")
	}
}
