// Package quotafs is the volume Limiter of the containerd runtime (ADR-0262 Decision 3): one preallocated ext4 image
// with project quotas, loop-mounted at the volume root, and a hard block limit per leaf.
package quotafs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/containerd/containerd/v2/core/mount"
	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
)

// Config names the image file, where it is mounted, and its size.
type Config struct {
	Image         string
	Mount         string
	CapacityBytes int64
}

// FS is the mounted image.
type FS struct {
	cfg Config
	dev string // the loop device, quotactl's special file
}

// The kernel ABI quotafs drives, from linux/fs.h and linux/quota.h; golang.org/x/sys/unix has none of them.
const (
	fsIOCGetXattr      = 0x801c581f // _IOR('X', 31, struct fsxattr)
	fsIOCSetXattr      = 0x401c5820 // _IOW('X', 32, struct fsxattr)
	fsXflagProjInherit = 0x200
	qSetQuota          = 0x800008
	prjQuota           = 2
	qifBLimits         = 1
	qifBlockSize       = 1024
)

type fsxattr struct {
	xflags     uint32
	extsize    uint32
	nextents   uint32
	projid     uint32
	cowextsize uint32
	pad        [8]byte
}

type ifDqblk struct {
	bhardlimit uint64
	bsoftlimit uint64
	curspace   uint64
	ihardlimit uint64
	isoftlimit uint64
	curinodes  uint64
	btime      uint64
	itime      uint64
	valid      uint32
}

// Open creates the image once (preallocated, ext4 with project quotas and no reserved blocks), grows it when
// CapacityBytes is larger, refuses a smaller one, and mounts it at Mount with prjquota, nosuid and nodev.
func Open(ctx context.Context, cfg Config) (*FS, error) {
	const op = "quotafs.Open"
	if cfg.CapacityBytes <= 0 {
		return nil, fault.Invalidf(op, "volumes.capacityBytes %d must be above 0", cfg.CapacityBytes)
	}
	if os.Geteuid() != 0 {
		return nil, fault.Invalidf(op, "the volume image needs root")
	}
	if err := kernelSupported(); err != nil {
		return nil, err
	}
	if dev, ok := mountedAt(cfg.Mount); ok {
		if err := mount.UnmountAll(cfg.Mount, 0); err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "unmount a stale volume image at %s (%s)", cfg.Mount, dev)
		}
	}
	fi, err := os.Stat(cfg.Image)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := create(ctx, cfg); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, fault.Wrapf(err, fault.Internal, op, "inspect the volume image")
	case fi.Size() > cfg.CapacityBytes:
		return nil, fault.Invalidf(op, "volumes.capacityBytes %d is below the volume image's %d bytes; a volume image does not shrink", cfg.CapacityBytes, fi.Size())
	case fi.Size() < cfg.CapacityBytes:
		if err := grow(ctx, cfg); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(cfg.Mount, 0o700); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "create the volume mount point")
	}
	m := mount.Mount{Type: "ext4", Source: cfg.Image, Options: []string{"loop", "nosuid", "nodev", "prjquota"}}
	if err := m.Mount(cfg.Mount); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "mount the volume image")
	}
	dev, ok := mountedAt(cfg.Mount)
	if !ok {
		return nil, fault.Internalf(op, "the volume image is not mounted at %s after mount", cfg.Mount)
	}
	if err := os.Chmod(cfg.Mount, 0o700); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "set the volume root's mode")
	}
	return &FS{cfg: cfg, dev: dev}, nil
}

func create(ctx context.Context, cfg Config) error {
	const op = "quotafs.create"
	if err := os.MkdirAll(filepath.Dir(cfg.Image), 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create the volume image's directory")
	}
	f, err := os.OpenFile(cfg.Image, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create the volume image")
	}
	ferr := unix.Fallocate(int(f.Fd()), 0, 0, cfg.CapacityBytes)
	if cerr := f.Close(); ferr == nil {
		ferr = cerr
	}
	if ferr == nil {
		ferr = run(ctx, "mkfs.ext4", "-q", "-F", "-m", "0", "-O", "quota,project", cfg.Image)
	}
	if ferr != nil {
		_ = os.Remove(cfg.Image)
		return fault.Wrapf(ferr, fault.Internal, op, "preallocate and format the volume image")
	}
	return nil
}

func grow(ctx context.Context, cfg Config) error {
	const op = "quotafs.grow"
	f, err := os.OpenFile(cfg.Image, os.O_RDWR, 0)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open the volume image")
	}
	ferr := unix.Fallocate(int(f.Fd()), 0, 0, cfg.CapacityBytes)
	if cerr := f.Close(); ferr == nil {
		ferr = cerr
	}
	if ferr == nil {
		ferr = run(ctx, "e2fsck", "-f", "-p", cfg.Image)
	}
	if ferr == nil {
		ferr = run(ctx, "resize2fs", cfg.Image)
	}
	if ferr != nil {
		return fault.Wrapf(ferr, fault.Internal, op, "grow the volume image to %d bytes", cfg.CapacityBytes)
	}
	return nil
}

// Limit gives dir the project id, inherited by everything created under it, and a hard block limit of sizeBytes.
func (f *FS) Limit(dir string, project uint32, sizeBytes int64) error {
	const op = "quotafs.Limit"
	d, err := os.Open(dir)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open %s", dir)
	}
	defer func() { _ = d.Close() }()
	var x fsxattr
	if err := ioctl(d.Fd(), fsIOCGetXattr, unsafe.Pointer(&x)); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "read the project of %s", dir)
	}
	x.projid, x.xflags = project, x.xflags|fsXflagProjInherit
	if err := ioctl(d.Fd(), fsIOCSetXattr, unsafe.Pointer(&x)); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "set project %d on %s", project, dir)
	}
	blocks := uint64((sizeBytes + qifBlockSize - 1) / qifBlockSize)
	q := ifDqblk{bhardlimit: blocks, bsoftlimit: blocks, valid: qifBLimits}
	dev, err := unix.BytePtrFromString(f.dev)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "the volume device")
	}
	cmd := uintptr(qSetQuota<<8 | prjQuota)
	if _, _, e := unix.Syscall6(unix.SYS_QUOTACTL, cmd, uintptr(unsafe.Pointer(dev)), uintptr(project), uintptr(unsafe.Pointer(&q)), 0, 0); e != 0 {
		return fault.Wrapf(e, fault.Internal, op, "set the quota of project %d", project)
	}
	return nil
}

// Enforced: the kernel refuses a write past the limit.
func (*FS) Enforced() bool { return true }

// Root is where the volumes live.
func (f *FS) Root() string { return f.cfg.Mount }

// Close unmounts the image.
func (f *FS) Close() error {
	if err := mount.UnmountAll(f.cfg.Mount, 0); err != nil {
		return fault.Wrapf(err, fault.Internal, "quotafs.Close", "unmount the volume image")
	}
	return nil
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// mountedAt returns the device mounted exactly at dir.
func mountedAt(dir string) (string, bool) {
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
		return fault.Wrapf(err, fault.Unavailable, "quotafs.run", "%s is not installed on the host", name)
	}
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "quotafs.run", "%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// kernelSupported refuses a kernel from 6.17 (ADR-0268 Temporary workarounds): its file_setattr call sets a project id
// too, and the workers' seccomp rule for it is not yet verified on such a kernel.
func kernelSupported() error {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return fault.Wrapf(err, fault.Internal, "quotafs.Open", "read the kernel release")
	}
	release := unix.ByteSliceToString(u.Release[:])
	var major, minor int
	if _, err := fmt.Sscanf(release, "%d.%d", &major, &minor); err != nil {
		return fault.Invalidf("quotafs.Open", "kernel release %q is not <major>.<minor>", release)
	}
	if major > 6 || major == 6 && minor >= 17 {
		return fault.Invalidf("quotafs.Open", "volumes refuse kernel %s: from 6.17 a worker's file_setattr rule is not yet verified (ADR-0268)", release)
	}
	return nil
}
