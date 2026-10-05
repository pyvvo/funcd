//go:build linux

package procreg

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
)

// bootID reads /proc/sys/kernel/random/boot_id once: it does not change while the process lives.
//
//nolint:gochecknoglobals // read once per process (ADR-0187 Contracts), immutable after
var bootID = sync.OnceValues(func() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, "procreg.bootID", "read boot id")
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", fault.Internalf("procreg.bootID", "empty boot id")
	}
	return id, nil
})

// statFields reads /proc/<pid>/stat from field 3 (state) on.
func statFields(pid int) ([]string, error) {
	const op = "procreg.statFields"
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nil, fault.Wrapf(err, fault.NotFound, op, "read stat of pid %d", pid)
	}
	// The command name (field 2) may hold spaces and parentheses, so the fields count from its last ')'.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return nil, fault.Internalf(op, "malformed stat of pid %d", pid)
	}
	return strings.Fields(string(b[i+1:])), nil
}

// startTime reads field 22 of /proc/<pid>/stat: the start time in clock ticks since boot.
func startTime(pid int) (uint64, error) {
	const op = "procreg.startTime"
	fields, err := statFields(pid)
	if err != nil {
		return 0, err
	}
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

// bootMatches scopes a start time, which counts ticks since boot, to the boot that saved it. An entry without a boot
// ID was written by a release before ADR-0187 and keeps that release's argv token rule until the legacy branch is
// removed (ADR-0187, Temporary workarounds).
func bootMatches(e Entry) (bool, error) {
	if e.BootID == "" {
		return argvContains(e.PID, e.Token)
	}
	id, err := bootID()
	if err != nil {
		return false, err
	}
	return id == e.BootID, nil
}

// zombie reports whether field 3 of /proc/<pid>/stat is Z (zombie) or X (dead).
func zombie(pid int) bool {
	fields, err := statFields(pid)
	if err != nil || len(fields) == 0 {
		return false
	}
	return fields[0] == "Z" || fields[0] == "X"
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
