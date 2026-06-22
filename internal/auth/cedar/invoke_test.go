package cedar_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/cedar"
)

// fnWithLinks builds a Function in "default" declaring spec.links to the given same-namespace targets
// (the naming ADR-0064 + the link::invoke `links` Set attribute, ADR-0075).
func fnWithLinks(name v1.ObjectName, targets ...v1.ObjectName) *v1.Function {
	links := make([]v1.FunctionLink, 0, len(targets))
	for _, t := range targets {
		links = append(links, v1.FunctionLink{Alias: string(t), Target: t})
	}
	return &v1.Function{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Links: links},
	}
}

func fnResource(ns v1.NamespaceName, name v1.ObjectName) *auth.EntityRef {
	return &auth.EntityRef{Type: v1.KindFunction, Namespace: ns, Name: name}
}

// invokeMeta is a MetaReader with a caller "front" that declares a link to "pricing", plus a bare
// "pricing" target — the request-relevant entities for an invoke decision.
func invokeMeta() fakeMeta {
	return fakeMeta{
		fns: map[string]*v1.Function{
			"default/front":   fnWithLinks("front", "pricing"),
			"default/pricing": {ObjectMeta: v1.ObjectMeta{Name: "pricing", Namespace: "default", ResourceGroup: "rg1"}},
			"default/secret":  {ObjectMeta: v1.ObjectMeta{Name: "secret", Namespace: "default", ResourceGroup: "rg1"}},
		},
		stores: map[string]*v1.KVStore{},
	}
}

// scenario: declared-link-invokes (ADR-0075) — front declares spec.links→pricing and no Policy forbids
// it, so the built-in permit(link::invoke) when principal.links.contains(resource) ALLOWS the invoke.
func TestScenarioDeclaredLinkInvokes(t *testing.T) {
	t.Parallel()
	d := newDriver(t, invokeMeta(), fixedPolicies{rev: "0"}) // no user policies

	dec := authorize(t, d, fnPrincipal("default", "front"), auth.ActionLinkInvoke, fnResource("default", "pricing"))
	require.True(t, dec.Allowed, "a declared link invokes via the built-in permit (ADR-0064 preserved): %s", dec.Reason)
}

// scenario: undeclared-not-in-links (ADR-0075 defense-in-depth) — front declares NO link to "secret",
// so even though it is a Function resource, the built-in permit does NOT match (principal.links does not
// contain the target) ⇒ default-deny. The PDP self-enforces "declared" without trusting the Resolver.
func TestScenarioUndeclaredNotInLinks(t *testing.T) {
	t.Parallel()
	d := newDriver(t, invokeMeta(), fixedPolicies{rev: "0"})

	dec := authorize(t, d, fnPrincipal("default", "front"), auth.ActionLinkInvoke, fnResource("default", "secret"))
	require.False(t, dec.Allowed, "an undeclared target is not in caller.links ⇒ default-deny (no SSRF)")
}

// scenario: policy-revokes-invoke (ADR-0075) — front declares the pricing link (built-in would permit),
// but an operator Policy forbid(link::invoke) on Function::"default/pricing" REVOKES it ⇒ denied,
// without editing front.spec.links. Forbid wins over the built-in permit (Cedar semantics).
func TestScenarioPolicyRevokesInvoke(t *testing.T) {
	t.Parallel()
	pol := v1.Policy{
		ObjectMeta: v1.ObjectMeta{Name: "revoke-pricing", Namespace: "default"},
		Spec:       v1.PolicySpec{Cedar: `forbid(principal == Function::"default/front", action == Action::"link::invoke", resource == Function::"default/pricing");`},
	}
	d := newDriver(t, invokeMeta(), fixedPolicies{policies: []v1.Policy{pol}, rev: "1"})

	dec := authorize(t, d, fnPrincipal("default", "front"), auth.ActionLinkInvoke, fnResource("default", "pricing"))
	require.False(t, dec.Allowed, "a forbid Policy revokes a declared invoke (operator revoke, forbid wins)")
}

// scenario: rbac-and-kv-unaffected (ADR-0075) — adding link::invoke leaves KV decisions exactly as
// before: a kv::read is still default-deny with no permitting Policy, and the curated schema still
// accepts the KV actions. (RBAC for control-plane CRUD is a separate driver, not exercised here.)
func TestScenarioRBACAndKVUnaffected(t *testing.T) {
	t.Parallel()
	d := newDriver(t, newMeta(), fixedPolicies{rev: "0"})

	dec := authorize(t, d, fnPrincipal("default", "reporting"), auth.ActionKVRead, tableResource("default", "orders", "customers"))
	require.False(t, dec.Allowed, "kv::read stays default-deny — link::invoke only extends the schema")

	// the curated schema validates a link::invoke Policy naming Function as both principal and resource.
	require.NoError(t, cedar.ValidateCedar(`permit(principal == Function::"default/a", action == Action::"link::invoke", resource == Function::"default/b");`),
		"Function is a valid resource for link::invoke")
}
