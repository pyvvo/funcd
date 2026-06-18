//go:build linux

package bench

import (
	"os"
	"strconv"
	"strings"
)

// cgroupPath returns the cgroup-v2 path of pid — the suffix of the unified-hierarchy line in
// /proc/<pid>/cgroup (cgroup v2 writes a single "0::<path>" line). "" on any error or if the
// host is cgroup v1 (no "0::" line). Used as the dedupe key so each container counts once.
func cgroupPath(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		// cgroup v2: "0::/path"; the hierarchy id is 0 and the controllers field is empty.
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return rest
		}
	}
	return ""
}

// cgroupMemMB reads memory.current of pid's cgroup-v2 (the live bytes charged to the cgroup —
// the shim's anonymous memory plus page cache and kernel slab the container is accountable for)
// and returns it in MB. 0 on any error (no v2, file absent, parse failure) — a 0 row means
// "could not read", not "free".
//
// Technique: memory.current is the SAME file production tooling uses for container memory —
// cAdvisor/kubelet/runc read it (working set = memory.current − inactive(file)). See
// https://github.com/google/cadvisor/issues/3081. This is why it is the honest footprint, not RSS.
func cgroupMemMB(pid int) float64 {
	path := cgroupPath(pid)
	if path == "" {
		return 0
	}
	data, err := os.ReadFile("/sys/fs/cgroup" + path + "/memory.current")
	if err != nil {
		return 0
	}
	bytes, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return float64(bytes) / bytesPerMB
}
