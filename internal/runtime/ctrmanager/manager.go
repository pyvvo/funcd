// Package ctrmanager brings up the container runtime funcd will drive (ADR-0054). It is
// the seam between funcd's self-contained model and the containerd driver: Ensure returns
// the containerd socket the driver should dial.
//
// Two modes, one Config:
//
//   - ExternalSocket set (--containerd <socket>): Ensure returns that socket as-is and
//     starts NO child — funcd defers to the operator's existing system/Docker-style
//     containerd. This is the cross-platform, unit-testable path.
//   - ExternalSocket "" (default): Ensure starts + supervises a PRIVATE containerd child on
//     a funcd-private socket + data-root, lays down crun for the runc-v2 shim, and imports
//     the embedded curated images (client.Import). This is REAL Go but only runs on
//     Linux+root (containerd + crun + cgroups); off Linux it returns a clear error telling
//     the operator to pass --containerd. It is exercised only in the deferred integration
//     lane (FUNCD_IT=1, root — the ADR-0011/0032 precedent).
//
// Close stops the supervised child (no-op for the external path).
package ctrmanager

import (
	"context"
	"path/filepath"

	"github.com/pyvvo/funcd/api/fault"
)

// FuncdRoot is funcd's runtime root — the value the install command + the Manager already use.
// The laid-down binaries live in BinDir (a sibling of the Manager's <root>/containerd data-root).
const FuncdRoot = "/var/lib/funcd"

// BinDir is the SINGLE source of truth for funcd's laid-down runtime binaries (crun, containerd,
// its shim, ctr): `funcd install`/provision.LayDown writes here and the Manager's resolveBin
// reads here, so install can never lay binaries where the Manager won't look (ADR-0056).
func BinDir() string { return filepath.Join(FuncdRoot, "bin") }

// Manager brings up the container runtime and yields the socket the driver dials.
type Manager interface {
	// Ensure returns the containerd socket the driver should dial: it starts + supervises a
	// private containerd (default) and imports the embedded images, OR returns ExternalSocket
	// as-is when --containerd is set. crun is laid down so the runc-v2 shim finds it.
	Ensure(ctx context.Context) (socket string, err error)
	// Close stops a supervised private containerd; a no-op for the external-socket path.
	Close() error
}

// Config selects + configures the runtime bring-up (the composition root supplies it).
type Config struct {
	// ExternalSocket is the --containerd <socket> / FUNCD_CONTAINERD_SOCKET value. "" ⇒
	// funcd starts + supervises its own private containerd.
	ExternalSocket string
	// DataRoot is the private containerd state dir (e.g. /var/lib/funcd/containerd) — used
	// only on the private-managed path.
	DataRoot string
	// ImageOverride maps a runtime to a registry ref (--image runtime=ref) that ImageFor yields in
	// place of the curated image; the containerd driver pulls a non-curated ref from its registry.
	ImageOverride map[string]string
}

// ImageFor is the runtime→image mapping the containerd execution runs (funcd.WithContainerExecution):
// a runtime's ImageOverride ref when set, else the curated prefix+runtime+":latest".
func (c Config) ImageFor(prefix string) func(runtime string) string {
	return func(rt string) string {
		if ref := c.ImageOverride[rt]; ref != "" {
			return ref
		}
		return prefix + rt + ":latest"
	}
}

// New builds a Manager for cfg.
//
//   - ExternalSocket set ⇒ an external-socket Manager (cross-platform: Ensure returns the
//     socket, starts no child, Close is a no-op).
//   - ExternalSocket "" ⇒ the private-managed Manager (Linux+root; non-Linux returns the
//     "private containerd is Linux-only; pass --containerd <socket>" error from Ensure).
func New(cfg Config) (Manager, error) {
	if cfg.ExternalSocket != "" {
		return &externalManager{socket: cfg.ExternalSocket}, nil
	}
	return newPrivate(cfg)
}

// externalManager is the --containerd <socket> path: it owns nothing and supervises
// nothing — it hands the operator's socket straight to the driver. Fully cross-platform.
type externalManager struct {
	socket string
}

// Ensure returns the external socket as-is (no child started). scenario: system-containerd-override.
func (m *externalManager) Ensure(_ context.Context) (string, error) {
	const op = "ctrmanager.externalManager.Ensure"
	if m.socket == "" { // defensive: New only builds this with a non-empty socket
		return "", fault.Invalidf(op, "external containerd socket must not be empty")
	}
	return m.socket, nil
}

// Close is a no-op: funcd does not own the external containerd.
func (m *externalManager) Close() error { return nil }
