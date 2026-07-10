//go:build !linux

package network

import "log/slog"

// newEnabledManager on a non-Linux host: nftables/netns are unavailable, so worker network isolation
// cannot be enforced. Log once and fall back to the no-op — egress stays open, as on any disabled host.
// (The process runtime is dev/CI only; enforcement is a containerd/Linux capability, per ADR-0115.)
func newDriver() Manager {
	slog.Warn("network isolation (F80) is enabled but unavailable on this platform; egress stays open",
		"reason", "nftables requires Linux")
	return noop{}
}
