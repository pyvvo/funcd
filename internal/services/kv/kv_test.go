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

// fakeBinder is a static Binder: a map of (function, binding) → Binding, default-deny on a miss.
type fakeBinder struct {
	m map[string]kv.Binding
}

func bkey(fn v1.ObjectName, binding string) string { return string(fn) + "/" + binding }

func (b fakeBinder) Resolve(_ context.Context, _ v1.NamespaceName, fn v1.ObjectName, binding string) (kv.Binding, error) {
	if bd, ok := b.m[bkey(fn, binding)]; ok {
		return bd, nil
	}
	return kv.Binding{}, fault.Forbiddenf("fakeBinder", "no grant for %s/%s", fn, binding)
}

func newFacade(t *testing.T, b fakeBinder) *kv.Facade {
	t.Helper()
	f, err := kv.NewFacade(kv.FacadeDeps{KV: memory.New(), Binder: b})
	require.NoError(t, err)
	return f
}

// scenario: grant-binds-function-to-store — an rw Grant for binding "counters" → store S; a put then
// reads back (prefixed by <ns>/<S>/), proving the binding resolves to the granted store.
func TestScenarioGrantBindsFunctionToStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeBinder{m: map[string]kv.Binding{
		bkey("counter", "counters"): {Store: "s", Mode: v1.KVModeRW, MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	f := newFacade(t, b)

	require.NoError(t, f.Put(ctx, "default", "counter", "counters", "alice", []byte("1")))
	v, found, err := f.Get(ctx, "default", "counter", "counters", "alice")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("1"), v)

	keys, err := f.List(ctx, "default", "counter", "counters", "")
	require.NoError(t, err)
	require.Equal(t, []string{"alice"}, keys, "keys are stripped of <ns>/<store>/")
}

// scenario: ungranted-access-denied — a function with no Grant for a binding is Forbidden on every verb.
func TestScenarioUngrantedAccessDenied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFacade(t, fakeBinder{m: map[string]kv.Binding{}}) // no grants

	require.Equal(t, fault.Forbidden, fault.KindOf(f.Put(ctx, "default", "fn", "b", "k", []byte("v"))))
	_, _, gerr := f.Get(ctx, "default", "fn", "b", "k")
	require.Equal(t, fault.Forbidden, fault.KindOf(gerr))
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Delete(ctx, "default", "fn", "b", "k")))
	_, lerr := f.List(ctx, "default", "fn", "b", "")
	require.Equal(t, fault.Forbidden, fault.KindOf(lerr))
}

// scenario: reader-grant-allows-get-not-put — an ro Grant permits get/list but Forbids put/del.
func TestScenarioReaderGrantAllowsGetNotPut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeBinder{m: map[string]kv.Binding{
		bkey("reader", "shared"): {Store: "s", Mode: v1.KVModeRO, MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	f := newFacade(t, b)

	// a get/list succeeds (no value yet, but not Forbidden)
	_, found, err := f.Get(ctx, "default", "reader", "shared", "k")
	require.NoError(t, err)
	require.False(t, found)
	_, err = f.List(ctx, "default", "reader", "shared", "")
	require.NoError(t, err)

	// a put/del is Forbidden (read sharing without write)
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Put(ctx, "default", "reader", "shared", "k", []byte("v"))))
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Delete(ctx, "default", "reader", "shared", "k")))
}

// scenario: value-over-cap-rejected — a put exceeding the store's maxValueBytes (or over-long key) is
// Invalid, before the write reaches the driver.
func TestScenarioValueOverCapRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeBinder{m: map[string]kv.Binding{
		bkey("fn", "small"): {Store: "s", Mode: v1.KVModeRW, MaxValueBytes: 4, MaxKeyBytes: 3},
	}}
	f := newFacade(t, b)

	require.NoError(t, f.Put(ctx, "default", "fn", "small", "ok", []byte("abcd")), "at the cap is allowed")
	require.Equal(t, fault.Invalid, fault.KindOf(f.Put(ctx, "default", "fn", "small", "ok", []byte("abcde"))), "over value cap ⇒ Invalid")
	require.Equal(t, fault.Invalid, fault.KindOf(f.Put(ctx, "default", "fn", "small", "abcd", []byte("x"))), "over key cap ⇒ Invalid")
}

// scenario: store-scoped-prefix-isolation — two functions granted DIFFERENT stores under the SAME
// binding alias do not collide (the prefix is the store, not the alias).
func TestScenarioStoreScopedPrefixIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeBinder{m: map[string]kv.Binding{
		bkey("fa", "kv"): {Store: "store-a", Mode: v1.KVModeRW, MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
		bkey("fb", "kv"): {Store: "store-b", Mode: v1.KVModeRW, MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	f := newFacade(t, b)

	require.NoError(t, f.Put(ctx, "default", "fa", "kv", "k", []byte("a-val")))
	require.NoError(t, f.Put(ctx, "default", "fb", "kv", "k", []byte("b-val")))

	va, _, err := f.Get(ctx, "default", "fa", "kv", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("a-val"), va)
	vb, _, err := f.Get(ctx, "default", "fb", "kv", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("b-val"), vb, "each store's key space is independent")
}
