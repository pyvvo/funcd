//go:build !linux

package ctrmanager

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
)

// privateManager is the non-Linux stub: the private-managed containerd needs a Linux host
// (containerd socket, crun, cgroups, netns), so off Linux Ensure returns a clear error
// telling the operator to defer to a system containerd via --containerd. The real
// supervision lives in manager_linux.go (the deferred integration lane). This file imports
// neither the containerd client nor embedimg.
type privateManager struct{}

// newPrivate builds the non-Linux private-managed Manager (Ensure errors at use, so a
// build/dial on macOS/CI never hard-requires Linux until the private path is actually taken).
func newPrivate(_ Config) (Manager, error) {
	return &privateManager{}, nil
}

// Ensure reports that the private-managed containerd is Linux-only and points at the
// --containerd escape hatch. scenario: system-containerd-override is the cross-platform path.
func (m *privateManager) Ensure(_ context.Context) (string, error) {
	return "", fault.Unavailablef("ctrmanager.privateManager.Ensure",
		"private containerd is Linux-only; pass --containerd <socket> to use an existing daemon (got a non-linux build)")
}

// Close is a no-op: no child was ever started off Linux.
func (m *privateManager) Close() error { return nil }
