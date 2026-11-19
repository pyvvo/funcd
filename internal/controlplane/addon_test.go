package controlplane_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
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

// ADR-0334 Decision 5: under funcdctl dev an Addon's image may come from any registry, still pinned by digest; without
// AnyRegistry an unlisted registry is refused.
func TestAddonRulesAnyRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	write := func(image string) admission.Request {
		a := &v1.Addon{Spec: v1.AddonSpec{Kind: "IdentityProvider", Image: image, Port: 8080}}
		return admission.Request{Operation: admission.Create, GVK: v1.KindAddon.GVK(), Object: a}
	}
	digested := "quay.io/pyvvo/funcd-rauthy@sha256:" + strings.Repeat("a", 64)
	rules := func(r controlplane.AddonRules) admission.Admission { return controlplane.NewAddonAdmissions(r)[1] }
	_, err := rules(controlplane.AddonRules{AnyRegistry: true}).Admit(ctx, write(digested))
	require.NoError(t, err, "any registry")
	_, err = rules(controlplane.AddonRules{AnyRegistry: true}).Admit(ctx, write("quay.io/pyvvo/funcd-rauthy:0.1.1"))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a tag is still refused: %v", err)
	_, err = rules(controlplane.AddonRules{Registries: []string{"ghcr.io"}}).Admit(ctx, write(digested))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an unlisted registry: %v", err)
}
