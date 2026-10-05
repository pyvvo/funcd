//go:build !linux

// Package containerd implements the runtime.Runtime port over containerd + runc on
// Linux (ADR-0011). On non-Linux platforms it is a stub: New returns
// fault.Unavailable, so the process driver is unambiguously the cross-platform path
// and `go build ./...` / the e2e harness work on macOS/CI.
package containerd

import (
	"log/slog"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/runtime"
)

// Config configures the containerd driver (the composition root supplies it).
type Config struct {
	Socket      string       // /run/containerd/containerd.sock
	Snapshotter string       // overlayfs
	CNIBinDir   string       // /opt/cni/bin
	CNIConfDir  string       // funcd-written conflist dir
	StateDir    string       // funcd-owned dir for runtime-generated worker files (e.g. resolv.conf); dataDir-relative
	SubnetCIDR  string       // lateral bridge subnet
	Logger      *slog.Logger // nil ⇒ slog.Default()

	// Pullable reports whether an image ref that is not embedded may be pulled (ctrmanager.Config.Pullable);
	// nil ⇒ none may.
	Pullable func(ref string) bool

	// Private marks Socket as funcd's private managed containerd (ADR-0054): no other owner runs workers in it, so
	// Close may sweep every funcd namespace in it. An external containerd may hold another daemon's or a bench's
	// workers (ADR-0055), so there Close stops only the workers this driver runs.
	Private bool
}

// New returns fault.Unavailable on non-Linux platforms — the containerd runtime
// requires Linux (containerd socket + runc + netns). Use the process driver for
// development and the e2e harness.
func New(_ Config) (runtime.Runtime, error) {
	return nil, fault.Unavailablef("runtime.containerd.New",
		"containerd runtime requires linux (got a non-linux build); use the process driver for dev/e2e")
}
