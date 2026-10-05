package cedar_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// providerS3Meta builds a bucket "lakehouse" with prefix "gold" OWNED by the add-on provider "lake"
// (a CatalogService, ADR-0088) which BINDS lakehouse/gold via its spec.blob. There is NO Function
// "lake" — the engine is a provider, not a Function (ADR-0087). The engine's key presents the
// CatalogService principal (ADR-0175); its blobBindings come from the CatalogService, and the prefix
// owner "lake" resolves to it because it binds the prefix and no Function holds the name.
func csPrincipal(ns v1.NamespaceName, name v1.ObjectName) *auth.EntityRef {
	return &auth.EntityRef{Type: v1.KindCatalogService, Namespace: ns, Name: name}
}

func providerS3Meta() s3Meta {
	return s3Meta{
		fns: map[string]*v1.Function{},
		css: map[string]*v1.CatalogService{
			"default/lake": {
				ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"},
				Spec: v1.CatalogServiceSpec{Blob: []v1.FunctionBlob{
					{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"},
				}},
			},
		},
		buckets: map[string]*v1.Bucket{
			"default/lakehouse": {
				ObjectMeta: v1.ObjectMeta{Name: "lakehouse", Namespace: "default", ResourceGroup: "rg1"},
				Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
					{Name: "gold", Owner: "lake"}, // owned by the provider "lake"
					{Name: "silver"},              // unbound, unowned
				}},
			},
		},
	}
}

// scenario: provider-reads-bound-prefix — a CatalogService principal resolves its blobBindings from
// spec.blob, so s3::read is granted on the bound prefix (ADR-0088). No Function "lake" exists.
func TestScenarioS3ProviderReadsBoundPrefix(t *testing.T) {
	t.Parallel()
	d := s3Driver(t, providerS3Meta(), fixedPolicies{rev: "0"})

	read := authorize(t, d, csPrincipal("default", "lake"), auth.ActionS3Read, prefixResource("default", "lakehouse", "gold"))
	require.True(t, read.Allowed, "provider lake reads lakehouse/gold via its CatalogService spec.blob (ADR-0088): %s", read.Reason)
}

// scenario: provider-writes-owned-prefix — the prefix owner names the provider, which binds the prefix and
// shares its name with no Function, so the CatalogService is a writer ⇒ s3::write is granted.
func TestScenarioS3ProviderWritesOwnedPrefix(t *testing.T) {
	t.Parallel()
	d := s3Driver(t, providerS3Meta(), fixedPolicies{rev: "0"})

	write := authorize(t, d, csPrincipal("default", "lake"), auth.ActionS3Write, prefixResource("default", "lakehouse", "gold"))
	require.True(t, write.Allowed, "provider lake writes its OWNED prefix lakehouse/gold: %s", write.Reason)
}

// scenario: provider-denied-unbound — default-deny holds: the provider reads/writes a prefix it
// neither binds nor owns ⇒ denied (no blanket allow from the fallback).
func TestScenarioS3ProviderDeniedUnbound(t *testing.T) {
	t.Parallel()
	d := s3Driver(t, providerS3Meta(), fixedPolicies{rev: "0"})

	read := authorize(t, d, csPrincipal("default", "lake"), auth.ActionS3Read, prefixResource("default", "lakehouse", "silver"))
	require.False(t, read.Allowed, "provider lake has no binding on lakehouse/silver ⇒ s3::read default-deny")
	write := authorize(t, d, csPrincipal("default", "lake"), auth.ActionS3Write, prefixResource("default", "lakehouse", "silver"))
	require.False(t, write.Allowed, "lakehouse/silver is unowned ⇒ s3::write denied")
}
