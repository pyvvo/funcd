//go:build !linux

package bench

import "context"

// RunContainerd is a skip stub off Linux (ADR-0052): the containerd/crun production path needs a
// Linux host (containerd socket, runc/crun, netns, cgroup v2), so on macOS/dev it returns a
// Skipped report with a nil error — the lane never fails the run, and this file imports neither
// the containerd driver nor the /proc+/sys cgroup reader (both Linux-only). The real lane lives
// in containerd_linux.go.
func RunContainerd(_ context.Context, _ ContainerdConfig) (FootprintReport, error) {
	return FootprintReport{
		Skipped:    true,
		SkipReason: "containerd footprint lane requires linux (got a non-linux build); the process lane still ran",
	}, nil
}
