//go:build darwin

package procreg

import (
	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
)

// sZomb is SZOMB in sys/proc.h, which golang.org/x/sys/unix does not export.
const sZomb = 5

// kinfo reads kern.proc.pid.<pid>.
func kinfo(pid int) (*unix.KinfoProc, error) {
	const op = "procreg.kinfo"
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return nil, fault.Wrapf(err, fault.NotFound, op, "read kinfo of pid %d", pid)
	}
	if k.Proc.P_pid != int32(pid) { //nolint:gosec // a pid fits an int32
		return nil, fault.NotFoundf(op, "pid %d not found", pid)
	}
	return k, nil
}

// startTime reads p_starttime from kern.proc.pid.<pid>, in µs since the epoch.
func startTime(pid int) (uint64, error) {
	k, err := kinfo(pid)
	if err != nil {
		return 0, err
	}
	tv := k.Proc.P_starttime
	return uint64(tv.Sec)*1_000_000 + uint64(tv.Usec), nil //nolint:gosec // a start time is after the epoch
}

// bootID is empty on macOS: p_starttime counts from the epoch, so it already differs across boots.
func bootID() (string, error) {
	return "", nil
}

// bootMatches is true for every entry on macOS, for the reason bootID is empty.
func bootMatches(Entry) (bool, error) {
	return true, nil
}

// zombie reports whether p_stat of pid is SZOMB.
func zombie(pid int) bool {
	k, err := kinfo(pid)
	return err == nil && k.Proc.P_stat == sZomb
}
