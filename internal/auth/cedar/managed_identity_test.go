package cedar

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
)

// scenario: identity-default-deny — an Identity is a legal Cedar principal (ADR-0135) but with NO role
// assignment it grants nothing: neither s3::read nor s3::write is permitted (default-deny preserved).
func TestScenarioIdentityDefaultDeny(t *testing.T) {
	t.Parallel()
	reg, err := NewRegistry(
		[]Capability{S3Capability()},
		[]PrincipalSource{FunctionPrincipalSource(), CatalogServicePrincipalSource()},
	)
	require.NoError(t, err)
	m := s3Meta{
		buckets: map[string]*v1.Bucket{
			"default/lake": {
				ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"},
				Spec:       v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "gold", Owner: "producer"}}},
			},
		},
	}
	principal := auth.EntityRef{Type: v1.KindIdentity, Namespace: "default", Name: "dropper"}
	resource := auth.EntityRef{Type: v1.KindBucket, Namespace: "default", Name: "lake", Path: "gold"}
	rUID := blobPrefixUID("default", "lake", "gold")

	require.False(t, evaluate(t, reg, m, principal, resource, auth.ActionS3Read, rUID),
		"an unassigned Identity is default-denied read")
	require.False(t, evaluate(t, reg, m, principal, resource, auth.ActionS3Write, rUID),
		"an unassigned Identity is default-denied write (issuing a credential is authN, not authZ)")
}
