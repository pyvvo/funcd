package cedar_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/cedar"
)

// fakeMeta is an in-memory MetaReader (the request-relevant entities the provider resolves):
// Functions + KVStores keyed by (ns, name). No mock framework — a plain map.
type fakeMeta struct {
	fns    map[string]*v1.Function
	stores map[string]*v1.KVStore
}

func key(ns v1.NamespaceName, name v1.ObjectName) string { return string(ns) + "/" + string(name) }

func (m fakeMeta) Get(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	switch gvk.Kind {
	case v1.KindFunction:
		if f, ok := m.fns[key(ns, name)]; ok {
			return f, nil
		}
	case v1.KindKVStore:
		if s, ok := m.stores[key(ns, name)]; ok {
			return s, nil
		}
	}
	return nil, fault.NotFoundf("fakeMeta.Get", "%s %s/%s not found", gvk.Kind, ns, name)
}

// newOrders builds a store "orders" in "default" with table "customers" owned by "customers-svc"
// and table "public" with NO owner (read-only).
func newOrders() *v1.KVStore {
	return &v1.KVStore{
		ObjectMeta: v1.ObjectMeta{Name: "orders", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.KVStoreSpec{Tables: []v1.KVTable{
			{Name: "customers", Owner: "customers-svc"},
			{Name: "public"},
		}},
	}
}

func newMeta() fakeMeta {
	return fakeMeta{
		fns: map[string]*v1.Function{
			// reporting has NO spec.kv binding — it exercises the cross-binding Policy path + default-deny.
			"default/reporting": {ObjectMeta: v1.ObjectMeta{Name: "reporting", Namespace: "default", ResourceGroup: "rg1"}},
			// customers-svc owns orders/customers (newOrders) but declares no binding — keeps the owner-attribute
			// test isolated from binding-as-read-grant.
			"default/customers-svc": {ObjectMeta: v1.ObjectMeta{Name: "customers-svc", Namespace: "default", ResourceGroup: "rg1"}},
			// analytics is a CONSUMER: it BINDS orders/customers (spec.kv) but does NOT own it — exercises
			// binding-as-read-grant (read via binding) and write-unaffected (a binding never grants write).
			"default/analytics": {
				ObjectMeta: v1.ObjectMeta{Name: "analytics", Namespace: "default", ResourceGroup: "rg1"},
				Spec:       v1.FunctionSpec{KV: []v1.FunctionKV{{Alias: "cust", Store: "orders", Table: "customers"}}},
			},
		},
		stores: map[string]*v1.KVStore{"default/orders": newOrders()},
	}
}

// fixedPolicies is a PolicySource over an in-line slice with a fixed revision.
type fixedPolicies struct {
	policies []v1.Policy
	rev      string
}

func (f fixedPolicies) Policies(_ context.Context) ([]v1.Policy, string, error) {
	return f.policies, f.rev, nil
}

func newDriver(t *testing.T, m fakeMeta, src cedar.PolicySource) auth.Authorizer {
	t.Helper()
	ep, err := cedar.NewEntityProvider(m)
	require.NoError(t, err)
	d, err := cedar.New(cedar.Deps{Entities: ep, Policies: src})
	require.NoError(t, err)
	return d
}

func fnPrincipal(ns v1.NamespaceName, name v1.ObjectName) *auth.EntityRef {
	return &auth.EntityRef{Type: v1.KindFunction, Namespace: ns, Name: name}
}

func tableResource(ns v1.NamespaceName, store v1.ObjectName, table string) *auth.EntityRef {
	return &auth.EntityRef{Type: v1.KindKVStore, Namespace: ns, Name: store, Path: table}
}

func authorize(t *testing.T, a auth.Authorizer, principal *auth.EntityRef, action auth.Action, resource *auth.EntityRef) auth.Decision {
	t.Helper()
	dec, err := a.Authorize(context.Background(), auth.Request{
		Identity: auth.Identity{Principal: principal},
		Action:   action,
		Resource: resource,
	})
	require.NoError(t, err)
	return dec
}

// scenario: cedar-permits-read — a Policy permitting reporting to kv::read KVStore orders allows
// reporting to read ANY table in orders (the KVTable in KVStore hierarchy covers all tables).
func TestScenarioCedarPermitsRead(t *testing.T) {
	t.Parallel()
	pol := v1.Policy{
		ObjectMeta: v1.ObjectMeta{Name: "reporting-read", Namespace: "default"},
		Spec:       v1.PolicySpec{Cedar: `permit(principal == Function::"default/reporting", action == Action::"kv::read", resource in KVStore::"default/orders");`},
	}
	d := newDriver(t, newMeta(), fixedPolicies{policies: []v1.Policy{pol}, rev: "1"})

	dec := authorize(t, d, fnPrincipal("default", "reporting"), auth.ActionKVRead, tableResource("default", "orders", "customers"))
	require.True(t, dec.Allowed, "reporting reads orders/customers via the store-level permit: %s", dec.Reason)

	dec2 := authorize(t, d, fnPrincipal("default", "reporting"), auth.ActionKVRead, tableResource("default", "orders", "public"))
	require.True(t, dec2.Allowed, "the KVTable in KVStore hierarchy covers every table in orders: %s", dec2.Reason)
}

// scenario: cedar-default-deny — with NO permitting Policy a kv::read is denied (Forbidden),
// even though the binding (naming) resolved. Authorization is the PDP, default-deny.
func TestScenarioCedarDefaultDeny(t *testing.T) {
	t.Parallel()
	d := newDriver(t, newMeta(), fixedPolicies{rev: "0"}) // no policies

	dec := authorize(t, d, fnPrincipal("default", "reporting"), auth.ActionKVRead, tableResource("default", "orders", "customers"))
	require.False(t, dec.Allowed, "no Policy ⇒ kv::read denied (default-deny)")
}

// scenario: owner-write-via-policy — the owner may write its table; another principal is denied even
// with a permissive read Policy. Owner-write is the built-in forbid(kv::write) unless principal ==
// resource.owner (NOT a user Policy).
func TestScenarioOwnerWriteViaPolicy(t *testing.T) {
	t.Parallel()
	// a permissive read Policy for BOTH functions — proving it does not grant writes.
	pol := v1.Policy{
		ObjectMeta: v1.ObjectMeta{Name: "all-read", Namespace: "default"},
		Spec:       v1.PolicySpec{Cedar: `permit(principal, action == Action::"kv::read", resource);`},
	}
	d := newDriver(t, newMeta(), fixedPolicies{policies: []v1.Policy{pol}, rev: "1"})

	owner := authorize(t, d, fnPrincipal("default", "customers-svc"), auth.ActionKVWrite, tableResource("default", "orders", "customers"))
	require.True(t, owner.Allowed, "the owner customers-svc writes orders/customers (built-in permit + owner-forbid passes): %s", owner.Reason)

	nonOwner := authorize(t, d, fnPrincipal("default", "reporting"), auth.ActionKVWrite, tableResource("default", "orders", "customers"))
	require.False(t, nonOwner.Allowed, "a non-owner write is Forbidden by the built-in forbid even with a permissive read Policy")

	// an owner-LESS table (public) has no writer at all — the forbid fires (resource has owner is false).
	ownerless := authorize(t, d, fnPrincipal("default", "customers-svc"), auth.ActionKVWrite, tableResource("default", "orders", "public"))
	require.False(t, ownerless.Allowed, "an owner-less table is read-only (the built-in forbid denies every write)")
}

// scenario: entities-from-resources — the PDP builds the entity store from the existing
// Function/KVStore resources: owner as a Function entity-ref (so principal == resource.owner compares
// entities) and KVTable in KVStore parents. Proven by the owner-equality decision above holding only
// when the metastore supplies the owner; here we assert a permit keyed on resource.owner works.
func TestScenarioEntitiesFromResources(t *testing.T) {
	t.Parallel()
	// a Policy that permits a read only when the principal IS the table owner — exercises the owner
	// attribute being a comparable Function entity-reference materialized from the KVStore resource.
	pol := v1.Policy{
		ObjectMeta: v1.ObjectMeta{Name: "owner-read", Namespace: "default"},
		Spec:       v1.PolicySpec{Cedar: `permit(principal, action == Action::"kv::read", resource) when { resource has owner && principal == resource.owner };`},
	}
	d := newDriver(t, newMeta(), fixedPolicies{policies: []v1.Policy{pol}, rev: "1"})

	ownerRead := authorize(t, d, fnPrincipal("default", "customers-svc"), auth.ActionKVRead, tableResource("default", "orders", "customers"))
	require.True(t, ownerRead.Allowed, "owner read permitted via resource.owner entity comparison: %s", ownerRead.Reason)

	strangerRead := authorize(t, d, fnPrincipal("default", "reporting"), auth.ActionKVRead, tableResource("default", "orders", "customers"))
	require.False(t, strangerRead.Allowed, "non-owner read denied (principal != resource.owner)")
}

// scenario: scoped-policies — a Policy can grant at a whole namespace or a whole resourceGroup
// without naming each store, because the provider materializes namespace/resourceGroup as entity
// attributes (ADR-0074). The grant must NOT leak to a resource in a different resourceGroup.
// (Store-level scoping via `resource in KVStore::"…"` is covered by TestScenarioCedarPermitsRead —
// the KVTable-in-KVStore parent; namespace/resourceGroup need no extra entity types, just the attrs.)
func TestScenarioScopedPolicies(t *testing.T) {
	t.Parallel()
	// orders is in rg1 (newMeta); add billing in rg2 — both in "default".
	m := newMeta()
	m.stores["default/billing"] = &v1.KVStore{
		ObjectMeta: v1.ObjectMeta{Name: "billing", Namespace: "default", ResourceGroup: "rg2"},
		Spec:       v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "invoices"}}},
	}

	// resourceGroup-scoped: grant read on everything in rg1 — orders qualifies, billing (rg2) does not.
	rgPol := v1.Policy{
		ObjectMeta: v1.ObjectMeta{Name: "rg1-read", Namespace: "default"},
		Spec:       v1.PolicySpec{Cedar: `permit(principal, action == Action::"kv::read", resource) when { resource.resourceGroup == "rg1" };`},
	}
	drg := newDriver(t, m, fixedPolicies{policies: []v1.Policy{rgPol}, rev: "1"})

	inRG := authorize(t, drg, fnPrincipal("default", "reporting"), auth.ActionKVRead, tableResource("default", "orders", "public"))
	require.True(t, inRG.Allowed, "resourceGroup-scoped read permits a table in rg1: %s", inRG.Reason)
	outRG := authorize(t, drg, fnPrincipal("default", "reporting"), auth.ActionKVRead, tableResource("default", "billing", "invoices"))
	require.False(t, outRG.Allowed, "the rg1-scoped grant does NOT reach billing (rg2) — no leak across resourceGroups")

	// namespace-scoped: grant read on everything in "default" — both stores qualify.
	nsPol := v1.Policy{
		ObjectMeta: v1.ObjectMeta{Name: "default-ns-read", Namespace: "default"},
		Spec:       v1.PolicySpec{Cedar: `permit(principal, action == Action::"kv::read", resource) when { resource.namespace == "default" };`},
	}
	dns := newDriver(t, m, fixedPolicies{policies: []v1.Policy{nsPol}, rev: "1"})
	for _, r := range []*auth.EntityRef{
		tableResource("default", "orders", "public"),
		tableResource("default", "billing", "invoices"),
	} {
		dec := authorize(t, dns, fnPrincipal("default", "reporting"), auth.ActionKVRead, r)
		require.True(t, dec.Allowed, "namespace-scoped read permits %s/%s: %s", r.Name, r.Path, dec.Reason)
	}
}

// scenario: binding-grants-read — a declared spec.kv binding grants kv::read on that table with NO
// Policy (ADR-0076 binding-as-read-grant, the read-side mirror of link-as-grant). analytics binds
// orders/customers (it does not own it) and reads it; a function with no binding to the table is
// default-deny (unbound-read-denied). The built-in permit(kv::read) when principal.kvBindings.contains.
func TestScenarioBindingGrantsRead(t *testing.T) {
	t.Parallel()
	d := newDriver(t, newMeta(), fixedPolicies{rev: "0"}) // NO user policies — the binding alone grants read

	bound := authorize(t, d, fnPrincipal("default", "analytics"), auth.ActionKVRead, tableResource("default", "orders", "customers"))
	require.True(t, bound.Allowed, "analytics reads orders/customers via its spec.kv binding, no Policy needed: %s", bound.Reason)

	// unbound-read-denied: reporting binds nothing → its kvBindings does not contain the table → default-deny.
	unbound := authorize(t, d, fnPrincipal("default", "reporting"), auth.ActionKVRead, tableResource("default", "orders", "customers"))
	require.False(t, unbound.Allowed, "reporting has no binding to orders/customers ⇒ kv::read default-deny (not default-allow)")

	// a bound function reading a DIFFERENT, unbound table is still denied (the grant is per-table, not per-store).
	otherTable := authorize(t, d, fnPrincipal("default", "analytics"), auth.ActionKVRead, tableResource("default", "orders", "public"))
	require.False(t, otherTable.Allowed, "the binding grants read on orders/customers only — orders/public (unbound) stays default-deny")
}

// scenario: policy-revokes-read — a forbid Policy overrides the binding grant (operator revoke without
// editing the caller's spec.kv; forbid wins in Cedar).
func TestScenarioPolicyRevokesRead(t *testing.T) {
	t.Parallel()
	pol := v1.Policy{
		ObjectMeta: v1.ObjectMeta{Name: "revoke-analytics", Namespace: "default"},
		Spec:       v1.PolicySpec{Cedar: `forbid(principal == Function::"default/analytics", action == Action::"kv::read", resource);`},
	}
	d := newDriver(t, newMeta(), fixedPolicies{policies: []v1.Policy{pol}, rev: "1"})

	dec := authorize(t, d, fnPrincipal("default", "analytics"), auth.ActionKVRead, tableResource("default", "orders", "customers"))
	require.False(t, dec.Allowed, "a forbid Policy revokes the binding's read (forbid wins), without editing spec.kv")
}

// scenario: write-unaffected — a binding grants READ only; a bound non-owner write is still Forbidden by
// the owner-write built-in (ADR-0074). The kvBindings Set feeds the kv::read permit, never kv::write.
func TestScenarioBindingDoesNotGrantWrite(t *testing.T) {
	t.Parallel()
	d := newDriver(t, newMeta(), fixedPolicies{rev: "0"})

	// analytics binds orders/customers (so it can READ) but customers-svc owns it — analytics may NOT write.
	w := authorize(t, d, fnPrincipal("default", "analytics"), auth.ActionKVWrite, tableResource("default", "orders", "customers"))
	require.False(t, w.Allowed, "a spec.kv binding grants read, never write — the owner-write forbid still denies a non-owner write")
}
