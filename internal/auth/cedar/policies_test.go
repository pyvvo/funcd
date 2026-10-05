package cedar

import (
	"context"
	"maps"
	"net/netip"
	"testing"

	cedartypes "github.com/cedar-policy/cedar-go/types"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// mutableSource is a PolicySource whose revision (and policy slice) the test controls, counting how many
// times the cache polls it — so the test can prove the compile only re-runs on a revision change.
type mutableSource struct {
	rev   string
	pols  []v1.Policy
	calls int
}

func (m *mutableSource) Policies(context.Context) ([]v1.Policy, string, error) {
	m.calls++
	return m.pols, m.rev, nil
}

// scenario: policy-cache-atomic-revision-swap — the compiled PolicySet is held behind an atomic pointer
// (ADR-0117 §4a): an unchanged source revision returns the SAME cached PolicySet (no rebuild — a
// lock-free load), while a revision change swaps in a freshly compiled set. The source is polled on every
// For (the revision-detection contract), but compile() runs only when the revision actually moves.
func TestScenarioPolicyCacheAtomicSwap(t *testing.T) {
	t.Parallel()
	src := &mutableSource{rev: "1"}
	c := &policyCache{src: src}

	ps1, err := c.For(context.Background(), "", "")
	require.NoError(t, err)
	require.NotNil(t, ps1)

	ps2, err := c.For(context.Background(), "", "")
	require.NoError(t, err)
	require.Same(t, ps1, ps2, "same revision ⇒ the cached PolicySet is returned (no rebuild)")

	src.rev = "2" // a Policy/EgressPolicy/Function write bumped the store revision
	ps3, err := c.For(context.Background(), "", "")
	require.NoError(t, err)
	require.NotSame(t, ps1, ps3, "a revision change ⇒ a freshly compiled PolicySet (atomic swap)")

	require.Equal(t, 3, src.calls, "the source is polled on every For (cheap revision detection)")
}

const (
	reasonNoPermit     = "cedar default-deny: no permitting policy"
	reasonCrossNSDeny  = reasonNoPermit + " (cross-namespace request: built-in policies only)"
	readAllCedar       = `permit(principal, action == Action::"kv::read", resource);`
	teamBForeignCedar  = `permit(principal == Function::"team-b/g", action == Action::"kv::read", resource in KVStore::"team-b/orders");`
	teamBUnrelatedText = `permit(principal == Function::"team-b/g", action == Action::"link::invoke", resource == Function::"team-b/h");`
)

// twoNamespaces holds team-a/f and team-b/g, each owning the table items of its namespace's store orders.
func twoNamespaces() regMeta {
	store := func(ns v1.NamespaceName, owner v1.ObjectName) *v1.KVStore {
		return &v1.KVStore{
			ObjectMeta: v1.ObjectMeta{Name: "orders", Namespace: ns, ResourceGroup: "rg1"},
			Spec:       v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "items", Owner: owner}}},
		}
	}
	return regMeta{
		fns: map[string]*v1.Function{
			"team-a/f": {ObjectMeta: v1.ObjectMeta{Name: "f", Namespace: "team-a", ResourceGroup: "rg1"}},
			"team-b/g": {ObjectMeta: v1.ObjectMeta{Name: "g", Namespace: "team-b", ResourceGroup: "rg1"}},
		},
		stores: map[string]*v1.KVStore{"team-a/orders": store("team-a", "f"), "team-b/orders": store("team-b", "g")},
	}
}

func nsPolicy(ns v1.NamespaceName, name v1.ObjectName, text string) v1.Policy {
	return v1.Policy{ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns}, Spec: v1.PolicySpec{Cedar: text}}
}

func nsDriver(t *testing.T, m MetaReader, pols ...v1.Policy) (auth.Authorizer, *policyCache) {
	t.Helper()
	ep, err := NewEntityProvider(m)
	require.NoError(t, err)
	a, err := New(Deps{Entities: ep, Policies: &mutableSource{rev: "1", pols: pols}})
	require.NoError(t, err)
	d, ok := a.(driver)
	require.True(t, ok)
	return a, d.policies
}

func fnRef(ns v1.NamespaceName, name v1.ObjectName) auth.EntityRef {
	return auth.EntityRef{Type: v1.KindFunction, Namespace: ns, Name: name}
}

func itemsRef(ns v1.NamespaceName) auth.EntityRef {
	return auth.EntityRef{Type: v1.KindKVStore, Namespace: ns, Name: "orders", Path: "items"}
}

func decide(t *testing.T, a auth.Authorizer, principal auth.EntityRef, action auth.Action, resource auth.EntityRef) auth.Decision {
	t.Helper()
	dec, err := a.Authorize(context.Background(), auth.Request{
		Identity: auth.Identity{Principal: &principal},
		Action:   action,
		Resource: &resource,
	})
	require.NoError(t, err)
	return dec
}

// scenario: unscoped-policy-stays-in-namespace — an unscoped permit in team-a grants team-a only.
func TestScenario_unscoped_policy_stays_in_namespace(t *testing.T) {
	t.Parallel()
	a, _ := nsDriver(t, twoNamespaces(), nsPolicy("team-a", "read-all", readAllCedar))

	dec := decide(t, a, fnRef("team-a", "f"), auth.ActionKVRead, itemsRef("team-a"))
	require.True(t, dec.Allowed, dec.Reason)

	dec = decide(t, a, fnRef("team-b", "g"), auth.ActionKVRead, itemsRef("team-b"))
	require.False(t, dec.Allowed)
	require.Equal(t, reasonNoPermit, dec.Reason)

	dec = decide(t, a, fnRef("team-a", "f"), auth.ActionKVRead, itemsRef("team-b"))
	require.False(t, dec.Allowed)
	require.Equal(t, reasonCrossNSDeny, dec.Reason)
}

// scenario: stored-foreign-policy-inert — a stored team-a Policy naming team-b entities grants nothing in
// team-b, and re-applying it is refused.
func TestScenario_stored_foreign_policy_inert(t *testing.T) {
	t.Parallel()
	a, _ := nsDriver(t, twoNamespaces(), nsPolicy("team-a", "foreign", teamBForeignCedar))

	dec := decide(t, a, fnRef("team-b", "g"), auth.ActionKVRead, itemsRef("team-b"))
	require.False(t, dec.Allowed, "a team-a Policy never decides a team-b request")
	require.Equal(t, reasonNoPermit, dec.Reason)

	require.Equal(t, fault.Invalid, fault.KindOf(ValidateCedarInNamespace("team-a", teamBForeignCedar)),
		"re-applying the foreign Policy is refused")
}

// scenario: cross-namespace-built-ins-only — a cross-namespace request is decided by the built-ins only:
// a team-a permit-all does not reach team-b, and cross-namespace kv/s3 writes stay denied.
func TestScenario_cross_namespace_built_ins_only(t *testing.T) {
	t.Parallel()
	a, cache := nsDriver(t, twoNamespaces(), nsPolicy("team-a", "read-all", readAllCedar))
	ctx := context.Background()

	builtinOnly, err := cache.For(ctx, "", "")
	require.NoError(t, err)
	cross, err := cache.For(ctx, "team-a", "team-b")
	require.NoError(t, err)
	teamA, err := cache.For(ctx, "team-a", "team-a")
	require.NoError(t, err)
	require.Same(t, builtinOnly, cross, "a cross-namespace request evaluates against the built-ins-only set")
	require.NotSame(t, builtinOnly, teamA)

	dec := decide(t, a, fnRef("team-a", "f"), auth.ActionKVRead, itemsRef("team-b"))
	require.False(t, dec.Allowed)
	require.Equal(t, reasonCrossNSDeny, dec.Reason)

	require.True(t, decide(t, a, fnRef("team-b", "g"), auth.ActionKVWrite, itemsRef("team-b")).Allowed,
		"the owner writes its own table")
	require.False(t, decide(t, a, fnRef("team-a", "f"), auth.ActionKVWrite, itemsRef("team-b")).Allowed,
		"a cross-namespace kv::write is denied")

	s3 := s3Meta{
		fns: map[string]*v1.Function{
			"team-a/f": {ObjectMeta: v1.ObjectMeta{Name: "f", Namespace: "team-a", ResourceGroup: "rg1"}},
			"team-b/g": {ObjectMeta: v1.ObjectMeta{Name: "g", Namespace: "team-b", ResourceGroup: "rg1"}},
		},
		buckets: map[string]*v1.Bucket{
			"team-b/lake": {
				ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "team-b", ResourceGroup: "rg1"},
				Spec:       v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "gold", Owner: "g"}}},
			},
		},
	}
	sa, _ := nsDriver(t, s3, nsPolicy("team-a", "write-all", `permit(principal, action == Action::"s3::write", resource);`))
	gold := auth.EntityRef{Type: v1.KindBucket, Namespace: "team-b", Name: "lake", Path: "gold"}
	require.True(t, decide(t, sa, fnRef("team-b", "g"), auth.ActionS3Write, gold).Allowed, "the owner writes its own prefix")
	require.False(t, decide(t, sa, fnRef("team-a", "f"), auth.ActionS3Write, gold).Allowed,
		"a cross-namespace s3::write is denied")
}

// scenario: synthetic-stays-in-namespace — RolesAssignment and EgressPolicy synthetics land in their
// source's namespace set only, so they never grant another namespace.
func TestScenario_synthetic_stays_in_namespace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := twoNamespaces()
	ra := &v1.RolesAssignment{
		ObjectMeta: v1.ObjectMeta{Name: "readers", Namespace: "team-a", ResourceGroup: "rg1"},
		Spec: v1.RolesAssignmentSpec{Assignments: []v1.AssignmentEntry{{
			Principal: &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: "f"},
			RoleRef:   v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "KV Data Reader"},
			Scope:     &v1.ScopeRef{Kind: v1.ScopeKindKVStore, Name: "orders"},
		}}},
	}
	roleSyn, err := CompileRolesAssignment(ctx, ra, NewRoleResolver(m))
	require.NoError(t, err)
	ep := &v1.EgressPolicy{
		ObjectMeta: v1.ObjectMeta{Name: "out", Namespace: "team-a", ResourceGroup: "rg1"},
		Spec:       v1.EgressPolicySpec{Rules: []v1.EgressRule{{To: v1.EgressTo{CIDRs: []string{"10.0.0.0/8"}}, Ports: []int{5432}}}},
	}
	egressSyn, err := CompileEgressPolicy("team-a", ep, []v1.ObjectName{"f"})
	require.NoError(t, err)
	pols := append(append(roleSyn, egressSyn...), nsPolicy("team-b", "local", teamBUnrelatedText))

	a, _ := nsDriver(t, m, pols...)
	require.True(t, decide(t, a, fnRef("team-a", "f"), auth.ActionKVRead, itemsRef("team-a")).Allowed)
	require.False(t, decide(t, a, fnRef("team-b", "g"), auth.ActionKVRead, itemsRef("team-b")).Allowed,
		"the team-a role grant does not reach team-b")
	dst := auth.NetDestination{IP: netip.MustParseAddr("10.1.2.3"), Port: 5432}
	require.True(t, decide(t, a, fnRef("team-a", "f"), auth.ActionEgressConnect, dst.Ref("team-a")).Allowed)
	require.False(t, decide(t, a, fnRef("team-b", "g"), auth.ActionEgressConnect, dst.Ref("team-b")).Allowed,
		"the team-a EgressPolicy does not reach team-b")

	builtin, byNS, err := compile(defaultRegistry.Builtins(), pols)
	require.NoError(t, err)
	require.Len(t, byNS, 2)
	roleID := cedartypes.PolicyID("team-a/rolesassignment-readers#0")
	egressID := cedartypes.PolicyID("team-a/egress-out#0")
	for _, id := range []cedartypes.PolicyID{roleID, egressID} {
		require.NotNil(t, byNS["team-a"].Get(id), "%s is in its source's namespace set", id)
		require.Nil(t, byNS["team-b"].Get(id), "%s is not in another namespace's set", id)
		require.Nil(t, builtin.Get(id), "%s is not in the built-ins-only set", id)
	}
	base := maps.Collect(builtin.All())
	teamA, teamB := maps.Collect(byNS["team-a"].All()), maps.Collect(byNS["team-b"].All())
	for id := range base {
		require.Contains(t, teamA, id)
		require.Contains(t, teamB, id)
	}
	require.Len(t, teamA, len(base)+2, "team-a holds the built-ins + its two synthetics")
	require.Len(t, teamB, len(base)+1, "team-b holds the built-ins + its one Policy")
}

// scenario: deny-reason-names-forbid — a deny decided by a user forbid names the forbidding PolicyID.
func TestScenario_deny_reason_names_forbid(t *testing.T) {
	t.Parallel()
	m := twoNamespaces()
	m.fns["team-a/f"].Spec.KV = []v1.FunctionKV{{Alias: "o", Store: "orders", Table: "items"}}

	a, _ := nsDriver(t, m)
	require.True(t, decide(t, a, fnRef("team-a", "f"), auth.ActionKVRead, itemsRef("team-a")).Allowed,
		"the binding grants the read")

	revoke := nsPolicy("team-a", "revoke",
		`forbid(principal == Function::"team-a/f", action == Action::"kv::read", resource in KVStore::"team-a/orders");`+"\n"+
			`forbid(principal, action == Action::"kv::read", resource in KVStore::"team-a/orders");`)
	a, _ = nsDriver(t, m, revoke)
	dec := decide(t, a, fnRef("team-a", "f"), auth.ActionKVRead, itemsRef("team-a"))
	require.False(t, dec.Allowed)
	require.Equal(t, "cedar: forbidden by policy team-a/revoke#0", dec.Reason)
}

// A deny by a built-in forbid names the built-in by its @id, whatever its position among the built-ins (#675).
func TestIssue675_BuiltinDenyNamesStableID(t *testing.T) {
	t.Parallel()
	m := twoNamespaces()
	m.fns["team-a/h"] = &v1.Function{ObjectMeta: v1.ObjectMeta{Name: "h", Namespace: "team-a", ResourceGroup: "rg1"}}
	ep, err := NewEntityProvider(m)
	require.NoError(t, err)
	const want = "cedar: forbidden by policy builtin/kv-single-writer"

	for name, builtins := range map[string]string{
		"default built-ins": defaultRegistry.Builtins(),
		"a built-in added first": `@id("added-first")` + "\n" +
			`permit(principal, action == Action::"link::invoke", resource) when { false };` + "\n" + defaultRegistry.Builtins(),
	} {
		a, err := New(Deps{Entities: ep, Policies: &mutableSource{rev: "1"}, Builtins: builtins})
		require.NoError(t, err)
		dec := decide(t, a, fnRef("team-a", "h"), auth.ActionKVWrite, itemsRef("team-a"))
		require.False(t, dec.Allowed, name)
		require.Equal(t, want, dec.Reason, name)
	}

	_, _, err = compile(`@id("x") permit(principal, action, resource);`+"\n"+`@id("x") forbid(principal, action, resource);`, nil)
	require.Error(t, err, "a duplicate built-in @id must not silently replace a policy")
	_, _, err = compile(`permit(principal, action, resource);`, nil)
	require.Error(t, err, "a built-in without an @id has no stable name")
}
