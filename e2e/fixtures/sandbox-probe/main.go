//go:build linux

// sandbox-probe runs inside a volumes-lane worker and prints one line per check of where the worker may write
// (ADR-0294): its root, /tmp and HOME, its boot directory and its log directory; then, in the volume directory it is
// given, it creates empty files until one fails and prints how many it made and the errno (ADR-0295). It removes what
// it wrote.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

func errnoOf(err error) string {
	var errno unix.Errno
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &errno):
		return unix.ErrnoName(errno)
	default:
		return err.Error()
	}
}

func write(name, path string, size int) {
	err := os.WriteFile(path, make([]byte, size), 0o600)
	_ = os.Remove(path)
	_, _ = fmt.Fprintln(os.Stdout, name, errnoOf(err))
}

func main() {
	write("etc", "/etc/sandbox-probe", 1)
	var st unix.Statfs_t
	if err := unix.Statfs("/tmp", &st); err != nil {
		_, _ = fmt.Fprintln(os.Stdout, "tmp", errnoOf(err))
	} else {
		fs := "other"
		if st.Type == unix.TMPFS_MAGIC {
			fs = "tmpfs"
		}
		_, _ = fmt.Fprintln(os.Stdout, "tmp", fs, int64(st.Blocks)*st.Bsize)
	}
	write("tmp-write", "/tmp/sandbox-probe", 1<<20)
	_, _ = fmt.Fprintln(os.Stdout, "home", os.Getenv("HOME"))
	write("boot", "/run/funcd-boot/sandbox-probe", 2<<20)
	write("log", "/run/funcd-log/sandbox-probe", 1)

	dir := os.Args[1]
	made := 0
	var err error
	for ; err == nil; made++ {
		var f *os.File
		if f, err = os.Create(filepath.Join(dir, "inode-"+strconv.Itoa(made))); err == nil {
			err = f.Close()
		}
	}
	made--
	_, _ = fmt.Fprintln(os.Stdout, "files", made, errnoOf(err))
	for i := range made {
		_ = os.Remove(filepath.Join(dir, "inode-"+strconv.Itoa(i)))
	}
}
