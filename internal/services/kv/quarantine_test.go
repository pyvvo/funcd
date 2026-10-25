package kv_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmem "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	kvsvc "github.com/pyvvo/funcd/internal/services/kv"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
)

// cutDrop is the memory substrate whose DropPrefix fails while cut is set: a crash between a move's copies and its
// drop, as the next start sees it.
type cutDrop struct {
	kvstore.KV
	cut bool
}

func (c *cutDrop) DropPrefix(prefix string) error {
	if c.cut {
		return errors.New("cut")
	}
	return c.KV.(kvsvc.PrefixManager).DropPrefix(prefix)
}

func ordersRef() quarantine.Ref {
	return quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindKVStore, Namespace: "default", Name: "orders"}}
}

func seedOrders(t *testing.T, kv kvstore.KV) {
	t.Helper()
	for k, v := range map[string]string{"default/orders/t/a": "1", "default/orders/t/b": "2"} {
		require.NoError(t, kv.Put(context.Background(), k, []byte(v)))
	}
}

func keysUnder(t *testing.T, kv kvstore.KV, prefix string) []string {
	t.Helper()
	keys, err := kv.List(context.Background(), prefix)
	require.NoError(t, err)
	return keys
}

// ADR-0288 Decisions 2 and 3: a start moves an orphan store's keys into a kept entry and empties its prefix; a
// crash before the drop leaves a moving entry, which the next start resumes under the same since.
func TestQuarantineMovesAndResumes(t *testing.T) {
	ctx := context.Background()
	kv := &cutDrop{KV: kvmem.New(), cut: true}
	seedOrders(t, kv)
	r, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: store.New(storemem.New()), KV: kv, QuarantinePeriod: time.Hour})
	require.NoError(t, err)
	t0 := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)

	require.Error(t, r.ReclaimDeleted(ctx, t0))
	entries, err := r.Quarantine().List(ctx)
	require.NoError(t, err)
	require.Equal(t, []quarantine.Entry{{Ref: ordersRef(), Since: t0, State: quarantine.Moving}}, entries)
	require.Len(t, keysUnder(t, kv, "default/orders/"), 2, "the source keeps its keys until the drop")

	kv.cut = false
	require.NoError(t, r.ReclaimDeleted(ctx, t0.Add(time.Minute)))
	entries, err = r.Quarantine().List(ctx)
	require.NoError(t, err)
	require.Equal(t, []quarantine.Entry{{Ref: ordersRef(), Since: t0, State: quarantine.Kept}}, entries, "resumed, not a new entry")
	require.Empty(t, keysUnder(t, kv, "default/orders/"))
	v, found, err := kv.Get(ctx, ".quarantine/default/orders/"+quarantine.Stamp(t0)+"/data/t/b")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "2", string(v))
}

// Decision 3: a moving entry whose store exists again by the next start is written kept as it stands, the keys not
// yet moved staying with the new store; one whose source is empty is kept too.
func TestQuarantineSettlesASplitEntry(t *testing.T) {
	ctx := context.Background()
	kv := &cutDrop{KV: kvmem.New(), cut: true}
	seedOrders(t, kv)
	st := store.New(storemem.New())
	r, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: st, KV: kv})
	require.NoError(t, err)
	t0 := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	require.Error(t, r.ReclaimDeleted(ctx, t0))

	_, err = st.Create(ctx, mkKVStore("orders"))
	require.NoError(t, err)
	kv.cut = false
	require.NoError(t, r.ReclaimDeleted(ctx, t0.Add(time.Minute)))
	entries, err := r.Quarantine().List(ctx)
	require.NoError(t, err)
	require.Equal(t, []quarantine.Entry{{Ref: ordersRef(), Since: t0, State: quarantine.Kept}}, entries)
	require.Len(t, keysUnder(t, kv, "default/orders/"), 2, "the new store keeps the keys not yet moved")
}

// Decision 6: Return copies a kept entry back into an empty store, then drops the entry; a store with keys is
// Conflict and nothing moves; no entry is NotFound; a cut return resumes, its emptiness check skipped.
func TestQuarantineReturn(t *testing.T) {
	ctx := context.Background()
	kv := kvmem.New()
	seedOrders(t, kv)
	r, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: store.New(storemem.New()), KV: kv.(kvsvc.PrefixManager)})
	require.NoError(t, err)
	t0 := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	require.NoError(t, r.ReclaimDeleted(ctx, t0))
	q := r.Quarantine()

	require.NoError(t, kv.Put(ctx, "default/orders/t/new", []byte("n")))
	require.Equal(t, fault.Conflict, fault.KindOf(q.Return(ctx, ordersRef(), time.Time{})))
	require.Len(t, keysUnder(t, kv, ".quarantine/"), 3, "the entry is untouched")
	require.NoError(t, kv.Delete(ctx, "default/orders/t/new"))

	entry := ".quarantine/default/orders/" + quarantine.Stamp(t0) + "/"
	require.NoError(t, kv.Put(ctx, entry+"state", []byte(quarantine.Returning)))
	require.NoError(t, kv.Put(ctx, "default/orders/t/a", []byte("1")))
	require.NoError(t, q.Return(ctx, ordersRef(), time.Time{}), "a returning entry resumes into a partial copy")
	require.ElementsMatch(t, []string{"default/orders/t/a", "default/orders/t/b"}, keysUnder(t, kv, "default/orders/"))
	require.Empty(t, keysUnder(t, kv, ".quarantine/"))
	require.Equal(t, fault.NotFound, fault.KindOf(q.Return(ctx, ordersRef(), time.Time{})))
}

// Decision 5: Expire drops the kept entries older than the cutoff and reports them; a moving one stays.
func TestQuarantineExpire(t *testing.T) {
	ctx := context.Background()
	kv := kvmem.New()
	seedOrders(t, kv)
	r, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: store.New(storemem.New()), KV: kv.(kvsvc.PrefixManager)})
	require.NoError(t, err)
	t0 := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	require.NoError(t, r.ReclaimDeleted(ctx, t0))
	moving := ".quarantine/default/gone/" + quarantine.Stamp(t0) + "/"
	require.NoError(t, kv.Put(ctx, moving+"state", []byte(quarantine.Moving)))
	q := r.Quarantine()

	gone, err := q.Expire(ctx, t0)
	require.NoError(t, err)
	require.Empty(t, gone, "an entry exactly one period old stays")
	gone, err = q.Expire(ctx, t0.Add(time.Millisecond))
	require.NoError(t, err)
	require.Equal(t, []quarantine.Entry{{Ref: ordersRef(), Since: t0, State: quarantine.Kept}}, gone)
	require.Equal(t, []string{moving + "state"}, keysUnder(t, kv, ".quarantine/"), "a moving entry never expires")
}
