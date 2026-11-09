package kv_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/quota"
	kv "github.com/pyvvo/funcd/internal/services/kv"
)

// scenario: sweep-deletes-what-expired (quota)
// The quota of a store whose keys expire (ADR-0292): a lost put-if-absent is charged nothing, and the sweep reports an
// expired key's delete, so the store's next write that does not fit measures again and fits (Decision 4).
func TestSweepFreesQuotaAtOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeResolver{m: map[string]kv.Binding{
		bkey("fn", "cache"): {Store: "s", Table: "t", Owner: "fn", MaxValueBytes: 1 << 20, MaxKeyBytes: 1024},
	}}
	db := memory.New()
	book := quota.NewBook(clock.System(), nil)
	book.Register(v1.KindKVStore, quota.Source{
		Limit: func(context.Context, quota.Subject) (int64, bool, error) { return 64, true, nil },
		Measure: func(ctx context.Context, s quota.Subject) (int64, error) {
			return db.(kvstore.Usager).Usage(ctx, string(s.Namespace)+"/"+string(s.Name)+"/")
		},
	})
	f, err := kv.NewFacade(kv.FacadeDeps{KV: db, Resolver: b, Authorizer: &pdpStub{
		readGrants: map[string]bool{rgKey("default", "fn", "s"): true}, owners: map[string]v1.ObjectName{"s/t": "fn"}}, Quota: book})
	require.NoError(t, err)

	value := []byte(strings.Repeat("v", 30))
	won, err := f.PutIf(ctx, "default", "fn", "cache", "lease", value, kvstore.Write{IfAbsent: true, TTL: time.Second})
	require.NoError(t, err)
	require.True(t, won.Done, "default/s/t/lease and 30 bytes: 47 of 64")
	lost, err := f.PutIf(ctx, "default", "fn", "cache", "lease", value, kvstore.Write{IfAbsent: true})
	require.NoError(t, err)
	require.False(t, lost.Done, "a lost claim is its result, never 507")
	require.Equal(t, fault.InsufficientStorage, fault.KindOf(f.Put(ctx, "default", "fn", "cache", "b", value)))
	require.Eventually(t, func() bool {
		_, found, err := f.Get(ctx, "default", "fn", "cache", "lease")
		return err == nil && !found
	}, 4*time.Second, 20*time.Millisecond)
	swept, err := kv.Sweep(ctx, db.(kvstore.Sweeper), book, hold.NeverNamespace, time.Now())
	require.NoError(t, err)
	require.Equal(t, []string{"default/s/t/lease"}, swept)
	require.NoError(t, f.Put(ctx, "default", "fn", "cache", "b", value), "the sweep's delete makes room at once")
}

// heldIn holds the namespaces it names, as a Restore's hold does.
type heldIn map[v1.NamespaceName]bool

func (h heldIn) HeldIn(ns v1.NamespaceName) bool       { return h[ns] }
func (heldIn) ReleasedAtIn(v1.NamespaceName) time.Time { return time.Time{} }

// scenario: sweep-deletes-what-expired (held namespace)
// A sweep keeps the expired keys of a held namespace, as its timers wait (ADR-0290 Decision 4), and deletes them once
// the hold is released.
func TestSweepWaitsForAHeldNamespace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := memory.New()
	c := db.(kvstore.Conditional)
	for _, k := range []string{"team-a/s/t/lease", "team-b/s/t/lease"} {
		_, err := c.PutIf(ctx, k, []byte("x"), kvstore.Write{TTL: time.Second})
		require.NoError(t, err)
	}
	later := time.Now().Add(3 * time.Second)
	gate := heldIn{"team-a": true}
	swept, err := kv.Sweep(ctx, db.(kvstore.Sweeper), quota.None, gate, later)
	require.NoError(t, err)
	require.Equal(t, []string{"team-b/s/t/lease"}, swept)
	delete(gate, "team-a")
	swept, err = kv.Sweep(ctx, db.(kvstore.Sweeper), quota.None, gate, later)
	require.NoError(t, err)
	require.Equal(t, []string{"team-a/s/t/lease"}, swept, "the release lets the sweep delete it")
}
