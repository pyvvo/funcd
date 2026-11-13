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

	// a Secret scope names one Secret (ADR-0327)
	ra.Spec.Assignments = []AssignmentEntry{{
		RoleRef: RoleRef{Kind: RoleRefKindBuiltin, Name: "Secret Data Writer"},
		Scope:   &ScopeRef{Kind: ScopeKindSecret, Name: "oauth"},
	}}
	require.NoError(t, ra.Validate())

	// no entries → error
	ra.Spec.Assignments = nil
	require.Error(t, ra.Validate())

	// an entry with no principal + no default → error
	ra.Spec.Principal = nil
	ra.Spec.Assignments = []AssignmentEntry{{RoleRef: RoleRef{Kind: RoleRefKindBuiltin, Name: "Reader"}, Scope: &ScopeRef{Kind: ScopeKindNamespace}}}
	require.Error(t, ra.Validate())
}

// A Secret's owner names a Function, as a SQLDatabase's does (ADR-0327 Decision 2): a DNS label, or none.
func TestSecretOwnerIsAFunctionName(t *testing.T) {
	t.Parallel()
	obj, ok := NewObject(KindSecret)
	require.True(t, ok)
	sec := obj.(*Secret)
	sec.Namespace, sec.Name, sec.ResourceGroup = "data", "oauth", "rg1"
	require.NoError(t, sec.Validate(), "no owner")
	sec.Spec.Owner = "refresher"
	require.NoError(t, sec.Validate())
	sec.Spec.Owner = "Not_A_Name"
	require.ErrorContains(t, sec.Validate(), "spec.owner")
}
