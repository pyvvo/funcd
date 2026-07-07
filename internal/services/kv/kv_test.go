package kv_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/kvstore/memory"
	"github.com/green-0-rabbit/funcd/internal/services/kv"
)

// fakeResolver is a static BindingResolver: a map of (function, alias) → Binding, default-deny on a miss
// (ADR-0073 — the binding IS the capability / naming).
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

// pdpStub is a test PDP (ADR-0074) emulating the cedar contract without the engine: kv::read is
// allowed only when (principal,store) is in readGrants (DEFAULT-DENY otherwise); kv::write is
// allowed only when the principal IS the table's owner (the built-in single-writer forbid). It also
// records the last request so a test can assert the connection-scoped per-function principal.
type pdpStub struct {
	readGrants map[string]bool // key: "<ns>/<fn>::<store>"
	owners     map[string]v1.ObjectName
	last       *auth.Request
}

func rgKey(ns v1.NamespaceName, fn v1.ObjectName, store v1.ObjectName) string {
	return string(ns) + "/" + string(fn) + "::" + string(store)
}

func (p *pdpStub) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	p.last = &req
	res := req.Resource
	pr := req.Identity.Principal
	if res == nil || pr == nil {
		return auth.Decision{Allowed: false, Reason: "no principal/resource"}, nil
	}
	switch req.Action {
	case auth.ActionKVRead:
		if p.readGrants[rgKey(pr.Namespace, pr.Name, res.Name)] {
			return auth.Decision{Allowed: true}, nil
		}
		return auth.Decision{Allowed: false, Reason: "default-deny: no read policy"}, nil
	case auth.ActionKVWrite:
		if owner, ok := p.owners[string(res.Name)+"/"+res.Path]; ok && owner == pr.Name {
			return auth.Decision{Allowed: true}, nil
		}
		return auth.Decision{Allowed: false, Reason: "not the table owner"}, nil
	default:
		return auth.Decision{Allowed: false, Reason: "unknown action"}, nil
	}
}

func newFacade(t *testing.T, b fakeResolver, pdp auth.Authorizer) *kv.Facade {
	t.Helper()
	f, err := kv.NewFacade(kv.FacadeDeps{KV: memory.New(), Resolver: b, Authorizer: pdp})
	require.NoError(t, err)
	return f
}

// allowAll lets the binding/caps scenarios exercise naming without re-testing authorization.
type allowAll struct{}

func (allowAll) Authorize(_ context.Context, _ auth.Request) (auth.Decision, error) {
	return auth.Decision{Allowed: true}, nil
}

// scenario: kv-binding-resolves — a Function with spec.kv {alias: customers, store: orders, table:
// customers}, when authorized, reads <ns>/orders/customers/k (the table sub-domain prefix).
func TestScenarioKVBindingResolves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeResolver{m: map[string]kv.Binding{
		bkey("customers-svc", "customers"): {Store: "orders", Table: "customers", Owner: "customers-svc", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	f := newFacade(t, b, allowAll{})

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
// verb (default-deny — the binding is the capability / naming, before the PDP is even consulted).
func TestScenarioUnboundAccessDenied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFacade(t, fakeResolver{m: map[string]kv.Binding{}}, allowAll{}) // no bindings

	require.Equal(t, fault.Forbidden, fault.KindOf(f.Put(ctx, "default", "fn", "b", "k", []byte("v"))))
	_, _, gerr := f.Get(ctx, "default", "fn", "b", "k")
	require.Equal(t, fault.Forbidden, fault.KindOf(gerr))
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Delete(ctx, "default", "fn", "b", "k")))
	_, lerr := f.List(ctx, "default", "fn", "b", "")
	require.Equal(t, fault.Forbidden, fault.KindOf(lerr))
}

// scenario: cedar-default-deny (facade PEP) — a bound caller whose binding RESOLVED is still
// Forbidden to read without a permitting Policy: authorization is the PDP, not the binding.
func TestScenarioFacadeReadDefaultDeny(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeResolver{m: map[string]kv.Binding{
		bkey("reporting", "customers"): {Store: "orders", Table: "customers", Owner: "customers-svc", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	pdp := &pdpStub{readGrants: map[string]bool{}, owners: map[string]v1.ObjectName{"orders/customers": "customers-svc"}}
	f := newFacade(t, b, pdp)

	_, _, gerr := f.Get(ctx, "default", "reporting", "customers", "alice")
	require.Equal(t, fault.Forbidden, fault.KindOf(gerr), "read denied without a permitting Policy (default-deny)")
	_, lerr := f.List(ctx, "default", "reporting", "customers", "")
	require.Equal(t, fault.Forbidden, fault.KindOf(lerr))
}

// scenario: cedar-permits-read (facade PEP) — once a Policy permits the caller kv::read on the store,
// the facade allows the get/list.
func TestScenarioFacadeReadAllowedWithPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeResolver{m: map[string]kv.Binding{
		bkey("customers-svc", "customers"): {Store: "orders", Table: "customers", Owner: "customers-svc", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
		bkey("reporting", "customers"):     {Store: "orders", Table: "customers", Owner: "customers-svc", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	pdp := &pdpStub{
		readGrants: map[string]bool{rgKey("default", "reporting", "orders"): true},
		owners:     map[string]v1.ObjectName{"orders/customers": "customers-svc"},
	}
	f := newFacade(t, b, pdp)

	require.NoError(t, f.Put(ctx, "default", "customers-svc", "customers", "alice", []byte("1")), "owner writes")
	v, found, err := f.Get(ctx, "default", "reporting", "customers", "alice")
	require.NoError(t, err, "reporting reads with a permitting Policy")
	require.True(t, found)
	require.Equal(t, []byte("1"), v)
}

// scenario: owner-write-via-policy (facade PEP) — the owner's put/del succeeds; a non-owner write is
// Forbidden (the built-in single-writer forbid) even when it CAN read.
func TestScenarioFacadeOwnerWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeResolver{m: map[string]kv.Binding{
		bkey("customers-svc", "customers"): {Store: "orders", Table: "customers", Owner: "customers-svc", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
		bkey("reporting", "customers"):     {Store: "orders", Table: "customers", Owner: "customers-svc", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	pdp := &pdpStub{
		readGrants: map[string]bool{rgKey("default", "reporting", "orders"): true},
		owners:     map[string]v1.ObjectName{"orders/customers": "customers-svc"},
	}
	f := newFacade(t, b, pdp)

	require.NoError(t, f.Put(ctx, "default", "customers-svc", "customers", "alice", []byte("1")), "owner writes")
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Put(ctx, "default", "reporting", "customers", "k", []byte("v"))), "non-owner write Forbidden")
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Delete(ctx, "default", "reporting", "customers", "alice")), "non-owner delete Forbidden")
}

// scenario: per-function-principal — the facade builds the PDP principal from the connection-scoped
// (ns, fn) it is handed (never from the request body): Function::"<ns>/<fn>".
func TestScenarioPerFunctionPrincipal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeResolver{m: map[string]kv.Binding{
		bkey("customers-svc", "customers"): {Store: "orders", Table: "customers", Owner: "customers-svc", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	pdp := &pdpStub{readGrants: map[string]bool{}, owners: map[string]v1.ObjectName{"orders/customers": "customers-svc"}}
	f := newFacade(t, b, pdp)

	_ = f.Put(ctx, "team-a", "customers-svc", "customers", "k", []byte("v"))
	require.NotNil(t, pdp.last, "the facade consulted the PDP")
	require.NotNil(t, pdp.last.Identity.Principal)
	require.Equal(t, v1.KindFunction, pdp.last.Identity.Principal.Type)
	require.Equal(t, v1.NamespaceName("team-a"), pdp.last.Identity.Principal.Namespace)
	require.Equal(t, v1.ObjectName("customers-svc"), pdp.last.Identity.Principal.Name)
	require.Equal(t, auth.ActionKVWrite, pdp.last.Action)
	require.NotNil(t, pdp.last.Resource)
	require.Equal(t, "customers", pdp.last.Resource.Path, "resource is the KVTable (store + table path)")
}

// scenario: value-over-cap-rejected — a put exceeding the store's maxValueBytes (or over-long key) is
// Invalid, before the write reaches the driver (after authorization).
func TestScenarioValueOverCapRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeResolver{m: map[string]kv.Binding{
		bkey("fn", "small"): {Store: "s", Table: "t", Owner: "fn", MaxValueBytes: 4, MaxKeyBytes: 3},
	}}
	pdp := &pdpStub{owners: map[string]v1.ObjectName{"s/t": "fn"}}
	f := newFacade(t, b, pdp)

	require.NoError(t, f.Put(ctx, "default", "fn", "small", "ok", []byte("abcd")), "at the cap is allowed")
	require.Equal(t, fault.Invalid, fault.KindOf(f.Put(ctx, "default", "fn", "small", "ok", []byte("abcde"))), "over value cap ⇒ Invalid")
	require.Equal(t, fault.Invalid, fault.KindOf(f.Put(ctx, "default", "fn", "small", "abcd", []byte("x"))), "over key cap ⇒ Invalid")
}
