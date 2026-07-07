//go:build linux

package bench

import (
	"os"
	"testing"
)

// TestCgroupMemMBSelf checks the cgroup-v2 reader (ADR-0052) returns a positive size for this
// process's own cgroup on a cgroup-v2 host. Linux-gated; if the host is cgroup v1 (no "0::" line)
// cgroupPath is "" and the read yields 0 — skipped rather than failed, since v1 hosts are out of
// scope (the production target is cgroup v2).
func TestCgroupMemMBSelf(t *testing.T) {
	pid := os.Getpid()
	if cgroupPath(pid) == "" {
		t.Skip("host is not cgroup v2 (no unified 0:: hierarchy) — cgroup footprint is v2-only")
	}
	if mb := cgroupMemMB(pid); mb <= 0 {
		t.Errorf("cgroupMemMB(self) = %.2f, want > 0 on a cgroup-v2 host", mb)
	}
	if mb := cgroupMemMB(-1); mb != 0 {
		t.Errorf("cgroupMemMB(invalid pid) = %.2f, want 0", mb)
	}
}
