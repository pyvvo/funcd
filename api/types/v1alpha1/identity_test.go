package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// scenario: identity-validate — envelope + type validation (ADR-0135).
func TestIdentityValidate(t *testing.T) {
	t.Parallel()
	obj, ok := NewObject(KindIdentity)
	require.True(t, ok)
	id := obj.(*Identity)
	id.Namespace, id.Name, id.ResourceGroup = "data", "dropper", "rg1"
	id.Spec.Type = IdentityTypeExternal
	require.NoError(t, id.Validate())

	require.Equal(t, KindIdentity.GVK(), id.GroupVersionKind())
	require.NotNil(t, id.GetStatus(), "Identity is a StatusObject")

	id.Spec.Type = "bogus"
	require.Error(t, id.Validate(), "spec.type must be external")
}
