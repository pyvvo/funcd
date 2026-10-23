//go:build linux

// projid-probe runs inside a volumes-lane worker (ADR-0268): in the volume directory it is given, it asks the kernel to
// set a file's project id, the same with the command's upper bits set, the directory's flags, and makes an ioctl the
// filter allows, then prints one line per call: its name and the errno, or ok.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	fsIOCGetXattr = 0x801c581f
	fsIOCSetXattr = 0x401c5820
	fsIOCGetFlags = 0x80086601
	fsIOCSetFlags = 0x40086602
)

type fsxattr struct {
	xflags, extsize, nextents, projid, cowextsize uint32
	pad                                           [8]byte
}

func ioctl(fd uintptr, req uint64, arg unsafe.Pointer) error {
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(req), uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

func result(name string, err error) {
	var errno unix.Errno
	switch {
	case err == nil:
		_, _ = fmt.Fprintln(os.Stdout, name, "ok")
	case errors.As(err, &errno):
		_, _ = fmt.Fprintln(os.Stdout, name, unix.ErrnoName(errno))
	default:
		_, _ = fmt.Fprintln(os.Stdout, name, err)
	}
}

func main() {
	dir := os.Args[1]
	f, err := os.Create(filepath.Join(dir, "probe-file"))
	if err != nil {
		_, _ = fmt.Fprintln(os.Stdout, "create", err)
		os.Exit(1)
	}
	var x fsxattr
	result("getxattr", ioctl(f.Fd(), fsIOCGetXattr, unsafe.Pointer(&x)))
	x.projid = 4242
	result("setxattr", ioctl(f.Fd(), fsIOCSetXattr, unsafe.Pointer(&x)))
	result("setxattr-high", ioctl(f.Fd(), 0xFFFFFFFF00000000|fsIOCSetXattr, unsafe.Pointer(&x)))
	d, err := os.Open(dir)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stdout, "open", err)
		os.Exit(1)
	}
	var flags uint64
	result("getflags", ioctl(d.Fd(), fsIOCGetFlags, unsafe.Pointer(&flags)))
	cleared := flags &^ 0x20000000 // FS_PROJINHERIT_FL
	result("setflags", ioctl(d.Fd(), fsIOCSetFlags, unsafe.Pointer(&cleared)))
}
