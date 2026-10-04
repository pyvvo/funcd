package ctrmanager

import (
	"context"
	"testing"

	"github.com/pyvvo/funcd/internal/platform/config"
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

// An ImageOverride (--image runtime=ref, ADR-0054) is the image of its runtime, so the driver pulls that ref
// instead of importing the embedded tar; a runtime without one keeps the curated prefix+runtime+":latest".
func TestImageFor_OverrideReplacesItsRuntimeImage(t *testing.T) {
	const ref = "ghcr.io/example/custom-node:2"
	imageFor := Config{ImageOverride: map[string]string{"nodejs22": ref}}.ImageFor("funcd/runtime-")
	if got := imageFor("nodejs22"); got != ref {
		t.Fatalf("image for nodejs22 = %q, want the override %q", got, ref)
	}
	if got, want := imageFor("python314"), "funcd/runtime-python314:latest"; got != want {
		t.Fatalf("image for python314 = %q, want the curated %q", got, want)
	}
}

// A runtime image is pulled only from a registry the operator chose: an imageOverride entry or a custom imagePrefix.
// With the default prefix only the embedded images are used, so its refs are not pullable.
func TestPullable_OnlyOperatorChosenRegistry(t *testing.T) {
	const override = "ghcr.io/example/custom-node:2"
	cfg := Config{ImageOverride: map[string]string{"nodejs22": override}}
	imageFor, pullable := cfg.ImageFor(config.DefaultImagePrefix), cfg.Pullable(config.DefaultImagePrefix)
	if ref := imageFor("deno"); pullable(ref) {
		t.Fatalf("%q comes from the default prefix, so it must not be pullable", ref)
	}
	if !pullable(imageFor("nodejs22")) {
		t.Fatalf("the imageOverride ref %q must be pullable", override)
	}

	const custom = "registry.example/team/runtime-"
	imageFor, pullable = cfg.ImageFor(custom), cfg.Pullable(custom)
	if ref := imageFor("deno"); !pullable(ref) {
		t.Fatalf("%q comes from a custom imagePrefix, so it must be pullable", ref)
	}
	if ref := "funcd/runtime-deno:latest"; pullable(ref) {
		t.Fatalf("%q is under neither the custom prefix nor an override, so it must not be pullable", ref)
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
