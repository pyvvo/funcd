package ctrmanager

import (
	"context"
	"testing"
)

// scenario: system-containerd-override — Given --containerd <socket> (ExternalSocket set),
// when funcd starts, then the Manager returns that socket as-is and starts NO private
// containerd child (the Docker-style escape hatch). Cross-platform / no root.
func TestSystemContainerdOverride(t *testing.T) {
	const sock = "/run/containerd/containerd.sock"
	m, err := New(Config{ExternalSocket: sock})
	if err != nil {
		t.Fatalf("New(external): %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	got, err := m.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure(external): %v", err)
	}
	if got != sock {
		t.Fatalf("Ensure returned %q, want the external socket %q (must pass through unchanged)", got, sock)
	}
	if _, ok := m.(*externalManager); !ok {
		t.Fatalf("ExternalSocket set: got %T, want *externalManager (no private child supervisor)", m)
	}

	// Close on the external path is a no-op and must not error (funcd owns nothing).
	if err := m.Close(); err != nil {
		t.Fatalf("Close(external): %v, want nil (no-op)", err)
	}
}

// With ExternalSocket "" the Manager is the private-managed one (it does not start a child
// at construction — supervision is on Ensure, which is root/Linux-gated). We assert it is
// NOT the external manager, so the default really is the self-contained private path.
func TestDefaultIsPrivateManaged(t *testing.T) {
	m, err := New(Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("New(private): %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if _, ok := m.(*externalManager); ok {
		t.Fatalf("ExternalSocket \"\": got *externalManager, want the private-managed Manager (default is self-contained)")
	}
}
