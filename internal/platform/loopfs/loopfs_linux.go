// Package loopfs makes, grows, mounts and removes an ext4 filesystem kept in a preallocated file, mounted through a
// loop device: the volume image (ADR-0262) and the Lake engine's scratch (ADR-0294).
package loopfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/v2/core/mount"
	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
)

// Create makes image: sizeBytes preallocated, then formatted as ext4 with no reserved blocks and the given features
// ("" for none). A failure removes the file.
func Create(ctx context.Context, image string, sizeBytes int64, features string) error {
	const op = "loopfs.Create"
	if err := os.MkdirAll(filepath.Dir(image), 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create the directory of %s", image)
	}
	f, err := os.OpenFile(image, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create %s", image)
	}
	ferr := unix.Fallocate(int(f.Fd()), 0, 0, sizeBytes)
	if cerr := f.Close(); ferr == nil {
		ferr = cerr
	}
	if ferr == nil {
		// nodiscard: mkfs discards a file's blocks by default, which would undo the preallocation
		args := []string{"-q", "-F", "-m", "0", "-E", "nodiscard"}
		if features != "" {
			args = append(args, "-O", features)
		}
		ferr = run(ctx, "mkfs.ext4", append(args, image)...)
	}
	if ferr != nil {
		_ = os.Remove(image)
		return fault.Wrapf(ferr, fault.KindOf(ferr), op, "preallocate and format %s", image)
	}
	return nil
}

// Grow preallocates image to sizeBytes and resizes its filesystem to fill it; image must not be mounted.
func Grow(ctx context.Context, image string, sizeBytes int64) error {
	const op = "loopfs.Grow"
	f, err := os.OpenFile(image, os.O_RDWR, 0)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open %s", image)
	}
	ferr := unix.Fallocate(int(f.Fd()), 0, 0, sizeBytes)
	if cerr := f.Close(); ferr == nil {
		ferr = cerr
	}
	if ferr == nil {
		ferr = run(ctx, "e2fsck", "-f", "-p", image)
	}
	if ferr == nil {
		ferr = run(ctx, "resize2fs", image)
	}
	if ferr != nil {
		return fault.Wrapf(ferr, fault.KindOf(ferr), op, "grow %s to %d bytes", image, sizeBytes)
	}
	return nil
}

// Mount mounts image at dir through a loop device, with nosuid, nodev and the extra options. The loop device reads and
// writes image with direct I/O: buffered, every write is cached a second time, for image, and a 1 GiB worker spilling
// 0.7 GiB through it took 126 s instead of 5 s.
func Mount(image, dir string, options ...string) error {
	const op = "loopfs.Mount"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create the mount point %s", dir)
	}
	m := mount.Mount{Type: "ext4", Source: image, Options: append([]string{"loop", "direct-io", "nosuid", "nodev"}, options...)}
	if err := m.Mount(dir); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "mount %s at %s", image, dir)
	}
	return nil
}

// MountedAt returns the device mounted exactly at dir.
func MountedAt(dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	info, err := mount.Lookup(abs)
	if err != nil || info.Mountpoint != abs {
		return "", false
	}
	return info.Source, true
}

// Unmount unmounts every mount at dir.
func Unmount(dir string) error {
	if err := mount.UnmountAll(dir, 0); err != nil {
		return fault.Wrapf(err, fault.Internal, "loopfs.Unmount", "unmount %s", dir)
	}
	return nil
}

// Remove detaches the filesystem mounted at dir, when one is, and removes dir and image. A process that still holds
// the filesystem, in another mount namespace, keeps its blocks until it ends.
func Remove(image, dir string) error {
	const op = "loopfs.Remove"
	if _, ok := MountedAt(dir); ok {
		if err := unix.Unmount(dir, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) {
			return fault.Wrapf(err, fault.Internal, op, "detach %s", dir)
		}
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fault.Wrapf(err, fault.Internal, op, "remove %s", dir)
	}
	if err := os.Remove(image); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fault.Wrapf(err, fault.Internal, op, "remove %s", image)
	}
	return nil
}

func run(ctx context.Context, name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin"} {
			if _, serr := os.Stat(filepath.Join(dir, name)); serr == nil {
				path, err = filepath.Join(dir, name), nil
				break
			}
		}
	}
	if err != nil {
		return fault.Wrapf(err, fault.Unavailable, "loopfs.run", "%s is not installed on the host", name)
	}
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "loopfs.run", "%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}
