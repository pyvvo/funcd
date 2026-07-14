package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// scenario: role-and-rolesassignment-validate (ADR-0136).
func TestRoleValidate(t *testing.T) {
	t.Parallel()
	obj, ok := NewObject(KindRole)
	require.True(t, ok)
	ro := obj.(*Role)
	ro.Namespace, ro.Name, ro.ResourceGroup = "data", "silver-analyst", "rg1"
	ro.Spec.Actions = []string{"s3::read", "kv::read"}
	require.NoError(t, ro.Validate())
	require.Equal(t, KindRole.GVK(), ro.GroupVersionKind())

	ro.Spec.Actions = nil
	require.Error(t, ro.Validate(), "at least one action required")
}

func TestRolesAssignmentValidate(t *testing.T) {
	t.Parallel()
	obj, ok := NewObject(KindRolesAssignment)
	require.True(t, ok)
	ra := obj.(*RolesAssignment)
	ra.Namespace, ra.Name, ra.ResourceGroup = "data", "grants", "rg1"
	ra.Spec = RolesAssignmentSpec{
		Principal: &PrincipalRef{Kind: PrincipalKindIdentity, Name: "dropper"},
		Assignments: []AssignmentEntry{{
			RoleRef: RoleRef{Kind: RoleRefKindBuiltin, Name: "Blob Data Writer"},
			Scope:   &ScopeRef{Kind: ScopeKindBucketPrefix, Name: "releves/landing"},
		}},
	}
	require.NoError(t, ra.Validate())
	require.Equal(t, KindRolesAssignment.GVK(), ra.GroupVersionKind())

	// no entries → error
	ra.Spec.Assignments = nil
	require.Error(t, ra.Validate())

	// an entry with no principal + no default → error
	ra.Spec.Principal = nil
	ra.Spec.Assignments = []AssignmentEntry{{RoleRef: RoleRef{Kind: RoleRefKindBuiltin, Name: "Reader"}, Scope: &ScopeRef{Kind: ScopeKindNamespace}}}
	require.Error(t, ra.Validate())
}
