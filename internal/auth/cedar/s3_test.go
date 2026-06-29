package cedar_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/cedar"
)

// s3Meta is an in-memory MetaReader for the S3 (ADR-0080) tests: Functions + Buckets keyed by
// (ns, name) — the blob mirror of fakeMeta. No mock framework — a plain map.
type s3Meta struct {
	fns     map[string]*v1.Function
	buckets map[string]*v1.Bucket
}

func (m s3Meta) Get(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	switch gvk.Kind {
	case v1.KindFunction:
		if f, ok := m.fns[key(ns, name)]; ok {
			return f, nil
		}
	case v1.KindBucket:
		if b, ok := m.buckets[key(ns, name)]; ok {
			return b, nil
		}
	}
	return nil, fault.NotFoundf("s3Meta.Get", "%s %s/%s not found", gvk.Kind, ns, name)
}

// newS3Meta builds a bucket "lakehouse" in "default" with prefix "bronze" owned by "etl-svc" and
// prefix "gold" with NO owner (read-only). Functions:
//   - etl-svc owns lakehouse/bronze (no binding — isolates the owner-write path).
//   - analytics BINDS lakehouse/bronze + lakehouse/gold (spec.blob) but owns neither — exercises
//     binding-as-read-grant and a bound non-owner write being denied.
//   - reporting binds nothing — exercises unbound default-deny.
func newS3Meta() s3Meta {
	return s3Meta{
		fns: map[string]*v1.Function{
			"default/reporting": {ObjectMeta: v1.ObjectMeta{Name: "reporting", Namespace: "default", ResourceGroup: "rg1"}},
			"default/etl-svc":   {ObjectMeta: v1.ObjectMeta{Name: "etl-svc", Namespace: "default", ResourceGroup: "rg1"}},
			"default/analytics": {
				ObjectMeta: v1.ObjectMeta{Name: "analytics", Namespace: "default", ResourceGroup: "rg1"},
				Spec: v1.FunctionSpec{Blob: []v1.FunctionBlob{
					{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"},
					{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"},
				}},
			},
		},
		buckets: map[string]*v1.Bucket{
			"default/lakehouse": {
				ObjectMeta: v1.ObjectMeta{Name: "lakehouse", Namespace: "default", ResourceGroup: "rg1"},
				Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
					{Name: "bronze", Owner: "etl-svc"},
					{Name: "gold"},
				}},
			},
		},
	}
}

func s3Driver(t *testing.T, m s3Meta, src cedar.PolicySource) auth.Authorizer {
	t.Helper()
	ep, err := cedar.NewEntityProvider(m)
	require.NoError(t, err)
	d, err := cedar.New(cedar.Deps{Entities: ep, Policies: src})
	require.NoError(t, err)
	return d
}

func prefixResource(ns v1.NamespaceName, bucket v1.ObjectName, prefix string) *auth.EntityRef {
	return &auth.EntityRef{Type: v1.KindBucket, Namespace: ns, Name: bucket, Path: prefix}
}

// scenario: binding-grants-read + unbound-denied — a declared spec.blob binding grants s3::read on
// that (bucket, prefix) with NO Policy (ADR-0080 binding-as-read-grant); an unbound function is
// default-deny. The built-in permit(s3::read) when principal.blobBindings.contains.
func TestScenarioS3BindingGrantsRead(t *testing.T) {
	t.Parallel()
	d := s3Driver(t, newS3Meta(), fixedPolicies{rev: "0"}) // NO user policies — the binding alone grants read

	bound := authorize(t, d, fnPrincipal("default", "analytics"), auth.ActionS3Read, prefixResource("default", "lakehouse", "bronze"))
	require.True(t, bound.Allowed, "analytics reads lakehouse/bronze via its spec.blob binding, no Policy needed: %s", bound.Reason)

	unbound := authorize(t, d, fnPrincipal("default", "reporting"), auth.ActionS3Read, prefixResource("default", "lakehouse", "bronze"))
	require.False(t, unbound.Allowed, "reporting has no spec.blob binding ⇒ s3::read default-deny (not default-allow)")
}

// scenario: owner-writes + non-owner-denied — the owner writes its prefix (proves the base
// permit(s3::write) is present); a bound non-owner write is Forbidden by the owner-forbid.
func TestScenarioS3OwnerWrites(t *testing.T) {
	t.Parallel()
	d := s3Driver(t, newS3Meta(), fixedPolicies{rev: "0"})

	owner := authorize(t, d, fnPrincipal("default", "etl-svc"), auth.ActionS3Write, prefixResource("default", "lakehouse", "bronze"))
	require.True(t, owner.Allowed, "the owner etl-svc writes lakehouse/bronze (base permit + owner-forbid passes): %s", owner.Reason)

	nonOwner := authorize(t, d, fnPrincipal("default", "analytics"), auth.ActionS3Write, prefixResource("default", "lakehouse", "bronze"))
	require.False(t, nonOwner.Allowed, "a bound non-owner write is Forbidden by the built-in owner-forbid (binding grants read, never write)")
}

// scenario: owner-less prefix is read-only — the `resource has owner` guard fires when a prefix has
// no owner, so even the would-be writer is denied; the binding still grants read.
func TestScenarioS3OwnerLessPrefixReadOnly(t *testing.T) {
	t.Parallel()
	d := s3Driver(t, newS3Meta(), fixedPolicies{rev: "0"})

	read := authorize(t, d, fnPrincipal("default", "analytics"), auth.ActionS3Read, prefixResource("default", "lakehouse", "gold"))
	require.True(t, read.Allowed, "analytics reads owner-less lakehouse/gold via its binding: %s", read.Reason)

	write := authorize(t, d, fnPrincipal("default", "analytics"), auth.ActionS3Write, prefixResource("default", "lakehouse", "gold"))
	require.False(t, write.Allowed, "an owner-less prefix is read-only (the resource-has-owner guard denies every write)")
}

// scenario: external-sigv4 / admin Policy — an admin Policy naming Action::"s3::read" and an
// S3Identity entity type passes ValidateCedar (curated-vocabulary admission accepts the s3 schema).
func TestS3ValidateCedarCuratedSchema(t *testing.T) {
	t.Parallel()
	require.NoError(t, cedar.ValidateCedar(`permit(principal == S3Identity::"default/ext", action == Action::"s3::read", resource in Bucket::"default/lakehouse");`),
		"an admin Policy naming s3::read + an S3Identity principal + a Bucket resource is valid")
	require.NoError(t, cedar.ValidateCedar(`permit(principal, action == Action::"s3::write", resource);`),
		"an s3::write Policy is valid")

	require.True(t, cedar.KnownAction("s3::read"))
	require.True(t, cedar.KnownAction("s3::write"))
	require.True(t, cedar.KnownEntityType("Bucket"))
	require.True(t, cedar.KnownEntityType("BlobPrefix"))
	require.True(t, cedar.KnownEntityType("S3Identity"))
}
