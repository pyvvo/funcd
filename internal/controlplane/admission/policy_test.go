package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

func policyObj(cedar string) *v1.Policy {
	p := &v1.Policy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy}}
	p.Name, p.Namespace, p.ResourceGroup = "p", "default", "rg1"
	p.Spec.Cedar = cedar
	return p
}

// scenario: policy-validity — a Policy whose Cedar parses + references only the curated schema is
// admitted; an un-parseable or off-schema Policy is rejected (Invalid).
func TestScenarioPolicyValidityAdmission(t *testing.T) {
	t.Parallel()
	a := admission.NewPolicyValidityAdmission()
	require.True(t, a.Handles(v1.KindPolicy.GVK(), admission.Create))
	require.True(t, a.Handles(v1.KindPolicy.GVK(), admission.Update))
	require.False(t, a.Handles(v1.KindFunction.GVK(), admission.Create))

	ctx := context.Background()
	good := policyObj(`permit(principal == Function::"default/reporting", action == Action::"kv::read", resource in KVStore::"default/orders");`)
	_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindPolicy.GVK(), Object: good})
	require.NoError(t, err, "well-formed curated-schema Policy is admitted")

	for name, text := range map[string]string{
		"unparseable":    `permit(principal, action ==`,
		"unknown action": `permit(principal, action == Action::"kv::frobnicate", resource);`,
		"unknown type":   `permit(principal == Mystery::"x", action == Action::"kv::read", resource);`,
	} {
		text := text
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := a.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: v1.KindPolicy.GVK(), Object: policyObj(text)})
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%s ⇒ Invalid", name)
		})
	}
}

func admitInTeamA(t *testing.T, text string) error {
	t.Helper()
	p := policyObj(text)
	p.Namespace = "team-a"
	_, err := admission.NewPolicyValidityAdmission().Admit(context.Background(),
		admission.Request{Operation: admission.Create, GVK: v1.KindPolicy.GVK(), Object: p})
	return err
}

func requireRefused(t *testing.T, err error, entity string) {
	t.Helper()
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Equal(t, 400, fault.ToProblem(err).Status)
	require.Contains(t, err.Error(), entity)
}

// scenario: policy-foreign-principal-refused — a team-a Policy naming a team-b principal is refused.
func TestScenario_policy_foreign_principal_refused(t *testing.T) {
	t.Parallel()
	err := admitInTeamA(t, `permit(principal == Function::"team-b/g", action == Action::"kv::read", resource in KVStore::"team-a/orders");`)
	requireRefused(t, err, `Function::"team-b/g"`)

	require.NoError(t, admitInTeamA(t, `permit(principal, action == Action::"kv::read", resource);`),
		"an unconstrained scope is admitted; the per-namespace PolicySet contains it")
}

// scenario: policy-foreign-resource-refused — a team-a Policy naming a team-b resource is refused.
func TestScenario_policy_foreign_resource_refused(t *testing.T) {
	t.Parallel()
	err := admitInTeamA(t, `permit(principal == Function::"team-a/f", action == Action::"kv::read", resource in KVStore::"team-b/orders");`)
	requireRefused(t, err, `KVStore::"team-b/orders"`)

	err = admitInTeamA(t, `permit(principal, action == Action::"kv::read", resource is KVTable in KVStore::"team-b/orders");`)
	requireRefused(t, err, `KVStore::"team-b/orders"`)

	require.NoError(t, admitInTeamA(t, `permit(principal == Function::"team-a/f", action == Action::"kv::read", resource in KVStore::"team-a/orders");`))
}
