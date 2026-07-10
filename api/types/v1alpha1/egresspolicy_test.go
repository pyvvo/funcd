package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
)

func egressPolicy(spec EgressPolicySpec) *EgressPolicy {
	return &EgressPolicy{
		TypeMeta:   TypeMeta{APIVersion: KindEgressPolicy.GVK().APIVersion(), Kind: KindEgressPolicy},
		ObjectMeta: ObjectMeta{Name: "egress", Namespace: "acme", ResourceGroup: "rg1"},
		Spec:       spec,
	}
}

// scenario: egresspolicy-validate — a well-formed allow-list validates; a bad domain / malformed CIDR /
// out-of-range port / a rule missing `to` / an empty rule set is rejected with fault.Invalid.
func TestScenarioEgressPolicyValidate(t *testing.T) {
	t.Parallel()

	good := egressPolicy(EgressPolicySpec{
		AppliesTo: []ObjectName{"etl"},
		Rules: []EgressRule{
			{To: EgressTo{Domains: []string{"api.x.com", "*.x.com"}, CIDRs: []string{"10.0.0.0/8"}}, Ports: []int{443, 5432}},
			{To: EgressTo{CIDRs: []string{"192.168.0.0/16"}}}, // no ports ⇒ any port
		},
	})
	require.NoError(t, good.Validate(), "a well-formed egress allow-list is valid")

	bad := map[string]EgressPolicySpec{
		"no rules":        {Rules: nil},
		"rule missing to": {Rules: []EgressRule{{Ports: []int{443}}}},
		"bad domain":      {Rules: []EgressRule{{To: EgressTo{Domains: []string{"not a domain!"}}}}},
		"bare wildcard":   {Rules: []EgressRule{{To: EgressTo{Domains: []string{"*"}}}}},
		"malformed cidr":  {Rules: []EgressRule{{To: EgressTo{CIDRs: []string{"10.0.0.0/999"}}}}},
		"port zero":       {Rules: []EgressRule{{To: EgressTo{Domains: []string{"x.com"}}, Ports: []int{0}}}},
		"port too high":   {Rules: []EgressRule{{To: EgressTo{Domains: []string{"x.com"}}, Ports: []int{70000}}}},
		"bad appliesTo":   {AppliesTo: []ObjectName{"Not A Name"}, Rules: []EgressRule{{To: EgressTo{Domains: []string{"x.com"}}}}},
	}
	for name, spec := range bad {
		spec := spec
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := egressPolicy(spec).Validate()
			require.Error(t, err, "%s must be rejected", name)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "rejection is fault.Invalid")
		})
	}
}

// scenario: egresspolicy-caps-reject-oversize — a policy exceeding an admission cap (too many rules /
// destinations-per-rule / ports-per-rule) is rejected with fault.Invalid, so the compiled-set memory
// ceiling is enforced at admission (ADR-0117 §4a).
func TestScenarioEgressPolicyCapsRejectOversize(t *testing.T) {
	t.Parallel()

	manyRules := make([]EgressRule, 65) // > maxEgressRules (64)
	for i := range manyRules {
		manyRules[i] = EgressRule{To: EgressTo{Domains: []string{"x.com"}}}
	}

	manyDests := make([]string, 65) // > maxEgressDests (64)
	for i := range manyDests {
		manyDests[i] = "x.com"
	}

	manyPorts := make([]int, 33) // > maxEgressPorts (32)
	for i := range manyPorts {
		manyPorts[i] = i + 1
	}

	oversize := map[string]EgressPolicySpec{
		"too many rules":        {Rules: manyRules},
		"too many destinations": {Rules: []EgressRule{{To: EgressTo{Domains: manyDests}}}},
		"too many ports":        {Rules: []EgressRule{{To: EgressTo{Domains: []string{"x.com"}}, Ports: manyPorts}}},
	}
	for name, spec := range oversize {
		spec := spec
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := egressPolicy(spec).Validate()
			require.Error(t, err, "%s must be rejected by the admission cap", name)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "an oversized policy is fault.Invalid")
		})
	}
}
