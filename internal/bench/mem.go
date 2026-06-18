package bench

import (
	"os"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

const bytesPerMB = 1024 * 1024

// rssMB returns the resident set size of a single process in MB, or 0 if unavailable. Uses gopsutil
// (ADR-0051) — cross-platform, in-process — instead of shelling out to `ps`.
func rssMB(pid int) float64 {
	p, err := process.NewProcess(int32(pid)) //nolint:gosec // pid is platform-internal, fits int32
	if err != nil {
		return 0
	}
	mem, err := p.MemoryInfo()
	if err != nil || mem == nil {
		return 0
	}
	return float64(mem.RSS) / bytesPerMB
}

// shimRSSMB sums the RSS (MB) of every running shim process — those whose command line contains the
// shim path — i.e. the total memory of all function workers on the process driver. The harness's own
// PID is excluded so funcd bench (which carries the shim path as an argument) is never counted.
//
// Technique + caveat: RSS counts shared library/runtime pages in EVERY process, so summing RSS across
// workers OVER-counts the shared runtime (each worker re-counts the node binary + libs). It is a
// per-process proxy, not the marginal truth — see https://www.phusionpassenger.com/library/indepth/accurately_measuring_memory_usage.html
// (RSS vs PSS). The containerd lane (cgroup_linux.go) measures the honest, shared-page-correct number.
func shimRSSMB(shimPath string) float64 {
	procs, err := process.Processes()
	if err != nil {
		return 0
	}
	self := int32(os.Getpid()) //nolint:gosec // own PID fits int32
	var total float64
	for _, p := range procs {
		if p.Pid == self {
			continue
		}
		cmd, err := p.Cmdline()
		if err != nil || !strings.Contains(cmd, shimPath) {
			continue
		}
		if mem, merr := p.MemoryInfo(); merr == nil && mem != nil {
			total += float64(mem.RSS) / bytesPerMB
		}
	}
	return total
}
