package kv_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/kvstore/memory"
	"github.com/green-0-rabbit/funcd/internal/services/kv"
)

// fakeResolver is a static BindingResolver: a map of (function, alias) → Binding, default-deny on a miss
// (ADR-0073 — the binding IS the capability).
type fakeResolver struct {
	m map[string]kv.Binding
}

func bkey(fn v1.ObjectName, alias string) string { return string(fn) + "/" + alias }

func (b fakeResolver) Resolve(_ context.Context, _ v1.NamespaceName, fn v1.ObjectName, alias string) (kv.Binding, error) {
	if bd, ok := b.m[bkey(fn, alias)]; ok {
		return bd, nil
	}
	return kv.Binding{}, fault.Forbiddenf("fakeResolver", "no kv binding for %s/%s", fn, alias)
}

func newFacade(t *testing.T, b fakeResolver) *kv.Facade {
	t.Helper()
	f, err := kv.NewFacade(kv.FacadeDeps{KV: memory.New(), Resolver: b})
	require.NoError(t, err)
	return f
}

// scenario: kv-binding-resolves — a Function with spec.kv {alias: customers, store: orders, table:
// customers}, when the owner puts/gets, reads <ns>/orders/customers/k (the table sub-domain prefix).
func TestScenarioKVBindingResolves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeResolver{m: map[string]kv.Binding{
		bkey("customers-svc", "customers"): {Store: "orders", Table: "customers", Owner: "customers-svc", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	f := newFacade(t, b)

	require.NoError(t, f.Put(ctx, "default", "customers-svc", "customers", "alice", []byte("1")))
	v, found, err := f.Get(ctx, "default", "customers-svc", "customers", "alice")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("1"), v)

	keys, err := f.List(ctx, "default", "customers-svc", "customers", "")
	require.NoError(t, err)
	require.Equal(t, []string{"alice"}, keys, "keys are stripped of <ns>/<store>/<table>/")
}

// scenario: unbound-access-denied — a function with no spec.kv entry for an alias is Forbidden on every
// verb (default-deny — the binding is the capability).
func TestScenarioUnboundAccessDenied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFacade(t, fakeResolver{m: map[string]kv.Binding{}}) // no bindings

	require.Equal(t, fault.Forbidden, fault.KindOf(f.Put(ctx, "default", "fn", "b", "k", []byte("v"))))
	_, _, gerr := f.Get(ctx, "default", "fn", "b", "k")
	require.Equal(t, fault.Forbidden, fault.KindOf(gerr))
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Delete(ctx, "default", "fn", "b", "k")))
	_, lerr := f.List(ctx, "default", "fn", "b", "")
	require.Equal(t, fault.Forbidden, fault.KindOf(lerr))
}

// scenario: owner-writes-others-read — table orders/customers has owner customers-svc. The owner's put
// succeeds; another function bound to the same table is Forbidden on put/del but allowed to get/list
// (read is coarse-allowed in-namespace; write is owner-only).
func TestScenarioOwnerWritesOthersRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Both functions bind the same table; only customers-svc is the owner.
	b := fakeResolver{m: map[string]kv.Binding{
		bkey("customers-svc", "customers"): {Store: "orders", Table: "customers", Owner: "customers-svc", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
		bkey("reporting", "customers"):     {Store: "orders", Table: "customers", Owner: "customers-svc", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	f := newFacade(t, b)

	// owner writes
	require.NoError(t, f.Put(ctx, "default", "customers-svc", "customers", "alice", []byte("1")))

	// a non-owner reads what the owner wrote (coarse same-namespace read)
	v, found, err := f.Get(ctx, "default", "reporting", "customers", "alice")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("1"), v)
	_, err = f.List(ctx, "default", "reporting", "customers", "")
	require.NoError(t, err)

	// a non-owner write/del is Forbidden (single-writer per table)
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Put(ctx, "default", "reporting", "customers", "k", []byte("v"))))
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Delete(ctx, "default", "reporting", "customers", "alice")))
}

// scenario: single-writer-per-table — tables orders/customers (owner A) and orders/fulfillment (owner B)
// in the SAME store; A and B each write their own table and neither may write the other's (disjoint
// prefixes, one gateway).
func TestScenarioSingleWriterPerTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeResolver{m: map[string]kv.Binding{
		// A owns customers; it also (incorrectly) binds fulfillment but is not its owner.
		bkey("a", "customers"):   {Store: "orders", Table: "customers", Owner: "a", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
		bkey("a", "fulfillment"): {Store: "orders", Table: "fulfillment", Owner: "b", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
		// B owns fulfillment; it also binds customers but is not its owner.
		bkey("b", "fulfillment"): {Store: "orders", Table: "fulfillment", Owner: "b", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
		bkey("b", "customers"):   {Store: "orders", Table: "customers", Owner: "a", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	f := newFacade(t, b)

	// each writes its own table (disjoint sub-domain prefixes)
	require.NoError(t, f.Put(ctx, "default", "a", "customers", "k", []byte("a-val")))
	require.NoError(t, f.Put(ctx, "default", "b", "fulfillment", "k", []byte("b-val")))

	// neither may write the other's table
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Put(ctx, "default", "a", "fulfillment", "k", []byte("x"))))
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Put(ctx, "default", "b", "customers", "k", []byte("x"))))

	// the disjoint prefixes did not collide
	va, _, err := f.Get(ctx, "default", "a", "customers", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("a-val"), va)
	vb, _, err := f.Get(ctx, "default", "b", "fulfillment", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("b-val"), vb)
}

// scenario: value-over-cap-rejected — a put exceeding the store's maxValueBytes (or over-long key) is
// Invalid, before the write reaches the driver.
func TestScenarioValueOverCapRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeResolver{m: map[string]kv.Binding{
		bkey("fn", "small"): {Store: "s", Table: "t", Owner: "fn", MaxValueBytes: 4, MaxKeyBytes: 3},
	}}
	f := newFacade(t, b)

	require.NoError(t, f.Put(ctx, "default", "fn", "small", "ok", []byte("abcd")), "at the cap is allowed")
	require.Equal(t, fault.Invalid, fault.KindOf(f.Put(ctx, "default", "fn", "small", "ok", []byte("abcde"))), "over value cap ⇒ Invalid")
	require.Equal(t, fault.Invalid, fault.KindOf(f.Put(ctx, "default", "fn", "small", "abcd", []byte("x"))), "over key cap ⇒ Invalid")
}
