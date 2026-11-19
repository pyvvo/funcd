package controlplane_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/controlplane"
)

// ADR-0332 Decision 6: an Addon's image is pinned by digest, with no tag, and comes from a listed registry, its host
// after docker.io normalization.
func TestAddonImageAllowed(t *testing.T) {
	t.Parallel()
	digest := "@sha256:" + strings.Repeat("a", 64)
	registries := []string{"ghcr.io", "docker.io", "localhost:5000"}
	for image, want := range map[string]bool{
		"ghcr.io/pyvvo/funcd-rauthy" + digest:              true,
		"GHCR.io/pyvvo/funcd-rauthy" + digest:              true,  // a host in another case is the same registry
		"ghcr.io/PyVVO/funcd-rauthy" + digest:              false, // a repository name is lower case
		"traefik/whoami" + digest:                          true,  // docker.io
		"busybox" + digest:                                 true,  // docker.io/library
		"localhost:5000/rauthy" + digest:                   true,
		"quay.io/pyvvo/funcd-rauthy" + digest:              false,
		"ghcr.io/pyvvo/funcd-rauthy:0.1.1":                 false,
		"ghcr.io/pyvvo/funcd-rauthy:0.1.1" + digest:        false,
		"ghcr.io/pyvvo/funcd-rauthy@sha256:abc":            false,
		"ghcr.io.evil.example/pyvvo/funcd-rauthy" + digest: false,
	} {
		require.Equal(t, want, controlplane.AddonImageAllowed(image, registries), image)
	}
	require.False(t, controlplane.AddonImageAllowed("ghcr.io/pyvvo/funcd-rauthy"+digest, nil), "no registry refuses every image")
}

// ADR-0332 Decision 1: the reserved plurals are every path segment funcd serves, its kinds' plurals and its other
// routes', and never the add-on routes' own template.
func TestReservedPlurals(t *testing.T) {
	t.Parallel()
	got := controlplane.ReservedPlurals()
	for _, want := range []string{"functions", "lakes", "sqldatabases", "addons", "namespaces", "runtimeclasses", "deadletters", "volumes", "hold"} {
		require.Contains(t, got, want)
	}
	for _, seg := range got {
		require.NotContains(t, seg, "{", seg)
	}
}
