package cedar

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
)

// s3Meta is a MetaReader with Functions + Buckets (the s3Resource materializer reads the Bucket for a
// prefix's owner).
type s3Meta struct {
	fns     map[string]*v1.Function
	buckets map[string]*v1.Bucket
}

func (m s3Meta) Get(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	switch gvk.Kind {
	case v1.KindFunction:
		if f, ok := m.fns[mkey(ns, name)]; ok {
			return f, nil
		}
	case v1.KindBucket:
		if b, ok := m.buckets[mkey(ns, name)]; ok {
			return b, nil
		}
	}
	return nil, fault.NotFoundf("s3Meta.Get", "%s %s/%s not found", gvk.Kind, ns, name)
}

// scenario: dev-relaxed-writes-drop-owner-gate — the funcdctl-dev S3 variant authorizes s3::write for a
// principal that is NOT the prefix owner (so seeding a no-owner `landing` AND a producer whose dev owner
// was mis-inferred both work), while the PROD variant denies exactly that. Reads stay binding-gated in both.
func TestScenarioDevRelaxedWritesDropOwnerGate(t *testing.T) {
	t.Parallel()
	srcs := []PrincipalSource{FunctionPrincipalSource(), CatalogServicePrincipalSource()}
	prod, err := NewRegistry([]Capability{S3Capability()}, srcs)
	require.NoError(t, err)
	dev, err := NewRegistry([]Capability{S3CapabilityDevRelaxedWrites()}, srcs)
	require.NoError(t, err)

	// "consumer" BINDS gold (read) but does NOT own it; "producer" owns gold.
	m := s3Meta{
		fns: map[string]*v1.Function{
			"default/consumer": {
				ObjectMeta: v1.ObjectMeta{Name: "consumer", Namespace: "default", ResourceGroup: "rg1"},
				Spec:       v1.FunctionSpec{Blob: []v1.FunctionBlob{{Alias: "g", Bucket: "lake", Prefix: "gold"}}},
			},
		},
		buckets: map[string]*v1.Bucket{
			"default/lake": {
				ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"},
				Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
					{Name: "gold", Owner: "producer"}, // OWNED by another function
					{Name: "drop"},                    // NO owner — the `landing`-style external drop zone
				}},
			},
		},
	}
	principal := auth.EntityRef{Type: v1.KindFunction, Namespace: "default", Name: "consumer"}
	resource := auth.EntityRef{Type: v1.KindBucket, Namespace: "default", Name: "lake", Path: "gold"}
	rUID := blobPrefixUID("default", "lake", "gold")

	// write to an OWNED prefix by a non-owner: prod denies; dev allows (dev-producer-writes-inferred-owner).
	require.False(t, evaluate(t, prod, m, principal, resource, auth.ActionS3Write, rUID),
		"prod: a non-owner s3::write is denied by the single-writer forbid")
	require.True(t, evaluate(t, dev, m, principal, resource, auth.ActionS3Write, rUID),
		"dev: s3::write is not owner-gated (dev-inferred owners)")

	// write to a NO-OWNER prefix (the `landing` seed case, dev-seed-no-owner-prefix): prod denies EVERYONE
	// (the forbid fires — no owner to match); dev allows it, so a developer can `aws s3 cp` the run input.
	dropRes := auth.EntityRef{Type: v1.KindFunction, Namespace: "default", Name: "consumer"}
	drop := auth.EntityRef{Type: v1.KindBucket, Namespace: "default", Name: "lake", Path: "drop"}
	dropUID := blobPrefixUID("default", "lake", "drop")
	require.False(t, evaluate(t, prod, m, dropRes, drop, auth.ActionS3Write, dropUID),
		"prod: an owner-less prefix is unwritable by anyone (the forbid fires with no owner to match)")
	require.True(t, evaluate(t, dev, m, dropRes, drop, auth.ActionS3Write, dropUID),
		"dev: an owner-less prefix is writable (seed a workflow's input)")

	// read: binding-gated in BOTH (consumer bound gold), so the relaxation does not widen reads.
	require.True(t, evaluate(t, prod, m, principal, resource, auth.ActionS3Read, rUID),
		"a bound prefix grants s3::read (prod)")
	require.True(t, evaluate(t, dev, m, principal, resource, auth.ActionS3Read, rUID),
		"a bound prefix grants s3::read (dev — unchanged)")

	// read still default-deny for an UNBOUND prefix under the dev variant (reads not widened).
	unbound := auth.EntityRef{Type: v1.KindBucket, Namespace: "default", Name: "lake", Path: "silver"}
	uUID := blobPrefixUID("default", "lake", "silver")
	require.False(t, evaluate(t, dev, m, principal, unbound, auth.ActionS3Read, uUID),
		"dev: an unbound prefix stays default-deny for reads")
}
