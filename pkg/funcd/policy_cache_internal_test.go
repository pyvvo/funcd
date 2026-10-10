package funcd

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	cedarauth "github.com/pyvvo/funcd/internal/auth/cedar"
	rolessvc "github.com/pyvvo/funcd/internal/services/roles"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// scenario: policy-cache-follows-writes — with <timeline>-<n> versions, a RolesAssignment or a Policy written,
// then deleted, changes the next authorization decision: the policy cache recompiles on every change.
func TestScenarioPolicyCacheFollowsWrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	src := policySource{st}
	reg, err := cedarauth.NewRegistry(
		[]cedarauth.Capability{cedarauth.S3CapabilityWithWriters(rolessvc.NewLister(st))},
		[]cedarauth.PrincipalSource{cedarauth.FunctionPrincipalSource()},
	)
	require.NoError(t, err)
	ep, err := reg.EntityProvider(cedarMetaReader{st})
	require.NoError(t, err)
	pdp, err := cedarauth.New(cedarauth.Deps{Entities: ep, Policies: src, Builtins: reg.Builtins()})
	require.NoError(t, err)

	meta := func(name v1.ObjectName) v1.ObjectMeta {
		return v1.ObjectMeta{Name: name, Namespace: "data", ResourceGroup: "rg1"}
	}
	seedObjects(t, st, &v1.Bucket{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
		ObjectMeta: meta("releves"),
		Spec:       v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "silver"}}},
	})
	_, rev, err := src.Policies(ctx)
	require.NoError(t, err)
	for _, rv := range strings.Split(rev, ",") {
		v, perr := store.ParseVersion(rv)
		require.NoError(t, perr)
		require.Len(t, v.Timeline, 16, "the cache key joins <timeline>-<n> versions: %q", rev)
	}
	reads := func() bool {
		principal := auth.EntityRef{Type: v1.KindIdentity, Namespace: "data", Name: "dash"}
		res := auth.EntityRef{Type: v1.KindBucket, Namespace: "data", Name: "releves", Path: "silver"}
		d, aerr := pdp.Authorize(ctx, auth.Request{Action: auth.ActionS3Read, Resource: &res, Identity: auth.Identity{Principal: &principal}})
		require.NoError(t, aerr)
		return d.Allowed
	}
	require.False(t, reads(), "no grant yet")

	seedObjects(t, st, &v1.RolesAssignment{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindRolesAssignment.GVK().APIVersion(), Kind: v1.KindRolesAssignment},
		ObjectMeta: meta("dash-reads"),
		Spec: v1.RolesAssignmentSpec{
			Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindIdentity, Name: "dash"},
			Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "Blob Data Reader"}, Scope: &v1.ScopeRef{Kind: v1.ScopeKindBucketPrefix, Name: "releves/silver"}}},
		},
	})
	require.True(t, reads(), "a new RolesAssignment grants the read")

	seedObjects(t, st, &v1.Policy{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: meta("no-reads"),
		Spec:       v1.PolicySpec{Cedar: `forbid(principal, action == Action::"s3::read", resource);`},
	})
	require.False(t, reads(), "a new Policy forbids the read")

	require.NoError(t, st.Delete(ctx, v1.KindPolicy.GVK(), "data", "no-reads", ""))
	require.True(t, reads(), "the deleted Policy no longer forbids it")
	require.NoError(t, st.Delete(ctx, v1.KindRolesAssignment.GVK(), "data", "dash-reads", ""))
	require.False(t, reads(), "the deleted RolesAssignment no longer grants it")
}
