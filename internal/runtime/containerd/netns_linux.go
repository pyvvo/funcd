//go:build linux

package containerd

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// netnsPins pins each worker's netns at <dir>/<CNI ID> with a bind mount, the way CNI runtimes pin a netns, so the
// netns outlives the task: a task that exits on its own takes its /proc netns with it, and the bridge plugin frees a
// worker's masquerade rules only in the worker's netns (issue #706). The zero value pins nothing.
type netnsPins struct {
	dir     string
	mount   func(src, dst string) error
	unmount func(path string) error
	isNetns func(path string) bool
}

func newNetnsPins(dir string) netnsPins {
	return netnsPins{
		dir:     dir,
		mount:   func(src, dst string) error { return unix.Mount(src, dst, "", unix.MS_BIND, "") },
		unmount: func(path string) error { return unix.Unmount(path, unix.MNT_DETACH) },
		isNetns: func(path string) bool {
			var st unix.Statfs_t
			return unix.Statfs(path, &st) == nil && st.Type == unix.NSFS_MAGIC
		},
	}
}

// pin bind-mounts process pid's netns onto the attachment's pin, replacing one an earlier run left, and returns the pin;
// without a pin dir it returns the /proc netns.
func (p netnsPins) pin(cniID string, pid uint32) (string, error) {
	src := fmt.Sprintf("/proc/%d/ns/net", pid)
	if p.dir == "" {
		return src, nil
	}
	p.unpin(p.pinned(cniID))
	path := filepath.Join(p.dir, cniID)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDONLY, 0o400)
	if err != nil {
		return "", err
	}
	_ = f.Close()
	if err := p.mount(src, path); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// pinned returns the attachment's pin, or "" when it has none. A reboot drops the mounts but keeps the pin files under
// the persistent state dir, and the bridge plugin fails a DEL in a path that is not a netns before it frees the IP, so
// pinned removes a pin with no netns mounted on it and returns "".
func (p netnsPins) pinned(cniID string) string {
	if p.dir == "" {
		return ""
	}
	path := filepath.Join(p.dir, cniID)
	if _, err := os.Lstat(path); err != nil {
		return ""
	}
	if !p.isNetns(path) {
		_ = os.Remove(path)
		return ""
	}
	return path
}

// unpin unmounts and removes a pin; any other path, a /proc netns or "", is left alone.
func (p netnsPins) unpin(path string) {
	if p.dir == "" || filepath.Dir(path) != p.dir {
		return
	}
	_ = p.unmount(path)
	_ = os.Remove(path)
}

// unheld returns the CNI IDs of the pins that are not held.
func (p netnsPins) unheld(held map[string]bool) []string {
	entries, _ := os.ReadDir(p.dir)
	var ids []string
	for _, e := range entries {
		if !held[filepath.Join(p.dir, e.Name())] {
			ids = append(ids, e.Name())
		}
	}
	return ids
}
