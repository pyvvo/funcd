package v1alpha1_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

func mkPolicy(cedar string) *v1.Policy {
	p := &v1.Policy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy}}
	p.Name, p.Namespace, p.ResourceGroup = "reporting-read", "default", "rg1"
	p.Spec.Cedar = cedar
	return p
}

// scenario (types): Policy roundtrips through JSON and Validate enforces the envelope + non-empty cedar.
func TestPolicyRoundtripAndValidate(t *testing.T) {
	t.Parallel()
	p := mkPolicy(`permit(principal == Function::"default/reporting", action == Action::"kv::read", resource in KVStore::"default/orders");`)
	require.NoError(t, p.Validate())

	b, err := json.Marshal(p)
	require.NoError(t, err)
	var got v1.Policy
	require.NoError(t, json.Unmarshal(b, &got))
	require.Equal(t, p.Spec.Cedar, got.Spec.Cedar)
	require.Equal(t, v1.KindPolicy, got.Kind)

	// empty cedar is structurally Invalid (the parse/schema check is the admission, not Validate).
	require.Equal(t, fault.Invalid, fault.KindOf(mkPolicy("   ").Validate()))
}

// KindPolicy is registered (NewObject + GVK + Validate + Namespaced).
func TestKindPolicyRegistered(t *testing.T) {
	t.Parallel()
	require.NoError(t, v1.KindPolicy.Validate())
	require.True(t, v1.KindPolicy.Namespaced())
	obj, ok := v1.NewObject(v1.KindPolicy)
	require.True(t, ok)
	_, isPolicy := obj.(*v1.Policy)
	require.True(t, isPolicy, "NewObject(KindPolicy) yields a *Policy")
	require.Equal(t, v1.KindPolicy.GVK(), obj.GroupVersionKind())
}
