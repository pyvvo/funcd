//go:build linux

package procreg

import (
	"bytes"
	"os"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// startTime reads field 22 of /proc/<pid>/stat: the start time in clock ticks since boot.
func startTime(pid int) (uint64, error) {
	const op = "procreg.startTime"
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, fault.Wrapf(err, fault.NotFound, op, "read stat of pid %d", pid)
	}
	// The command name (field 2) may hold spaces and parentheses, so the fields count from its last ')'.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, fault.Internalf(op, "malformed stat of pid %d", pid)
	}
	fields := strings.Fields(string(b[i+1:]))
	const field22 = 22 - 3 // fields[0] is field 3 (state)
	if len(fields) <= field22 {
		return 0, fault.Internalf(op, "short stat of pid %d", pid)
	}
	st, err := strconv.ParseUint(fields[field22], 10, 64)
	if err != nil {
		return 0, fault.Wrapf(err, fault.Internal, op, "parse start time of pid %d", pid)
	}
	return st, nil
}

// argvContains reports whether some element of /proc/<pid>/cmdline contains token.
func argvContains(pid int, token string) (bool, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false, fault.Wrapf(err, fault.NotFound, "procreg.argvContains", "read cmdline of pid %d", pid)
	}
	for arg := range bytes.SplitSeq(b, []byte{0}) {
		if bytes.Contains(arg, []byte(token)) {
			return true, nil
		}
	}
	return false, nil
}
