//go:build darwin

package procreg

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
)

// startTime reads p_starttime from kern.proc.pid.<pid>, in µs since the epoch.
func startTime(pid int) (uint64, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, fault.Wrapf(err, fault.NotFound, "procreg.startTime", "read kinfo of pid %d", pid)
	}
	if k.Proc.P_pid != int32(pid) { //nolint:gosec // a pid fits an int32
		return 0, fault.NotFoundf("procreg.startTime", "pid %d not found", pid)
	}
	tv := k.Proc.P_starttime
	return uint64(tv.Sec)*1_000_000 + uint64(tv.Usec), nil //nolint:gosec // a start time is after the epoch
}

// argvContains reports whether some argv element in kern.procargs2.<pid> contains token. The buffer holds argc, the
// exec path, NUL padding, then argc NUL-terminated arguments.
func argvContains(pid int, token string) (bool, error) {
	const op = "procreg.argvContains"
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return false, fault.Wrapf(err, fault.NotFound, op, "read argv of pid %d", pid)
	}
	if len(b) < 4 {
		return false, fault.Internalf(op, "short argv of pid %d", pid)
	}
	argc := int(binary.LittleEndian.Uint32(b[:4]))
	rest := b[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return false, fault.Internalf(op, "malformed argv of pid %d", pid)
	}
	rest = bytes.TrimLeft(rest[end:], "\x00")
	for range argc {
		arg, tail, _ := bytes.Cut(rest, []byte{0})
		if bytes.Contains(arg, []byte(token)) {
			return true, nil
		}
		rest = tail
	}
	return false, nil
}
