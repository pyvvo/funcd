package cedar_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/cedar"
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
			"default/reporting":     {ObjectMeta: v1.ObjectMeta{Name: "reporting", Namespace: "default", ResourceGroup: "rg1"}},
			"default/customers-svc": {ObjectMeta: v1.ObjectMeta{Name: "customers-svc", Namespace: "default", ResourceGroup: "rg1"}},
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
		Spec: v1.PolicySpec{Cedar: `permit(principal == Function::"default/reporting", action == Action::"kv::read", resource in KVStore::"default/orders");`},
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
