//go:build !linux

package egress

import "log/slog"

// newGateway on a non-Linux host: SO_ORIGINAL_DST + the redirect substrate are Linux-only, so a
// transparent egress PEP cannot be enforced. Log once and fall back to the no-op — egress stays open, as
// on any disabled host (the process runtime is dev/CI only; enforcement is a containerd/Linux capability).
func newGateway(_ Deps) Gateway {
	slog.Warn("egress gateway (F81) is enabled but unavailable on this platform; egress stays open",
		"reason", "SO_ORIGINAL_DST requires Linux")
	return noopGateway{}
}
