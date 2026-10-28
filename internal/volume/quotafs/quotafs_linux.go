// Package quotafs is the volume Limiter of the containerd runtime (ADR-0262 Decision 3): one preallocated ext4 image
// with project quotas, loop-mounted at the volume root, and a hard block limit per leaf.
package quotafs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/loopfs"
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
	// imageBytes and imageInodes are the mounted image's size and inode count, which share out inodes (ADR-0295).
	imageBytes, imageInodes int64
}

// The kernel ABI quotafs drives, from linux/fs.h and linux/quota.h; golang.org/x/sys/unix has none of them.
const (
	fsIOCGetXattr      = 0x801c581f // _IOR('X', 31, struct fsxattr)
	fsIOCSetXattr      = 0x401c5820 // _IOW('X', 32, struct fsxattr)
	fsXflagProjInherit = 0x200
	qGetQuota          = 0x800007
	qSetQuota          = 0x800008
	prjQuota           = 2
	qifBLimits         = 1
	qifILimits         = 4
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
	if dev, ok := loopfs.MountedAt(cfg.Mount); ok {
		if err := loopfs.Unmount(cfg.Mount); err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "unmount a stale volume image at %s (%s)", cfg.Mount, dev)
		}
	}
	fi, err := os.Stat(cfg.Image)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := loopfs.Create(ctx, cfg.Image, cfg.CapacityBytes, "quota,project"); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, fault.Wrapf(err, fault.Internal, op, "inspect the volume image")
	case fi.Size() > cfg.CapacityBytes:
		return nil, fault.Invalidf(op, "volumes.capacityBytes %d is below the volume image's %d bytes; a volume image does not shrink", cfg.CapacityBytes, fi.Size())
	case fi.Size() < cfg.CapacityBytes:
		if err := loopfs.Grow(ctx, cfg.Image, cfg.CapacityBytes); err != nil {
			return nil, err
		}
	}
	if err := loopfs.Mount(cfg.Image, cfg.Mount, "prjquota"); err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "mount the volume image")
	}
	dev, ok := loopfs.MountedAt(cfg.Mount)
	if !ok {
		return nil, fault.Internalf(op, "the volume image is not mounted at %s after mount", cfg.Mount)
	}
	if err := os.Chmod(cfg.Mount, 0o700); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "set the volume root's mode")
	}
	var st unix.Statfs_t
	if err := unix.Statfs(cfg.Mount, &st); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "read the volume image's size and inodes")
	}
	return &FS{cfg: cfg, dev: dev, imageBytes: int64(st.Blocks) * st.Bsize, imageInodes: int64(st.Files)}, nil
}

// Limit gives dir the project id, inherited by everything created under it, a hard block limit of sizeBytes and a hard
// inode limit of its share of the image's inodes (ADR-0295).
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
	inodes := inodesFor(sizeBytes, f.imageBytes, f.imageInodes)
	q := ifDqblk{bhardlimit: blocks, bsoftlimit: blocks, ihardlimit: inodes, isoftlimit: inodes, valid: qifBLimits | qifILimits}
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

// Free is the bytes the image's filesystem can still allocate: less than its size, since ext4 keeps part for its
// metadata (#1201).
func (f *FS) Free() (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(f.cfg.Mount, &st); err != nil {
		return 0, fault.Wrapf(err, fault.Internal, "quotafs.Free", "read the free space of %s", f.cfg.Mount)
	}
	return int64(st.Bavail) * st.Bsize, nil
}

// Used is the bytes charged to project, its files and directories.
func (f *FS) Used(project uint32) (int64, error) {
	const op = "quotafs.Used"
	dev, err := unix.BytePtrFromString(f.dev)
	if err != nil {
		return 0, fault.Wrapf(err, fault.Internal, op, "the volume device")
	}
	var q ifDqblk
	cmd := uintptr(qGetQuota<<8 | prjQuota)
	if _, _, e := unix.Syscall6(unix.SYS_QUOTACTL, cmd, uintptr(unsafe.Pointer(dev)), uintptr(project), uintptr(unsafe.Pointer(&q)), 0, 0); e != 0 {
		return 0, fault.Wrapf(e, fault.Internal, op, "read the use of project %d", project)
	}
	return int64(q.curspace), nil
}

// Root is where the volumes live.
func (f *FS) Root() string { return f.cfg.Mount }

// Close unmounts the image.
func (f *FS) Close() error {
	if err := loopfs.Unmount(f.cfg.Mount); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "quotafs.Close", "unmount the volume image")
	}
	return nil
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
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
