package badger_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"

	kvbadger "github.com/green-0-rabbit/funcd/internal/kvstore/badger"
	"github.com/green-0-rabbit/funcd/internal/kvstore/kvstorecontract"
)

type closer interface{ Close() error }
type dropper interface{ DropPrefix(string) error }

func openKV(t *testing.T, dir string) kvstoreKV {
	t.Helper()
	kv, err := kvbadger.Open(dir, kvbadger.WithSyncWrites(false), kvbadger.WithValueLogGCInterval(0))
	require.NoError(t, err)
	return kv
}

// kvstoreKV aliases the driver's surface used by the tests (kvstore.KV + the driver extras).
type kvstoreKV interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// scenario: kv-durable-driver-roundtrips — the Badger driver satisfies the shared kvstore contract AND
// persists a value across a Close+reopen (durable, unlike the memory driver).
func TestScenarioKVDurableDriverRoundtrips(t *testing.T) {
	t.Run("contract", func(t *testing.T) {
		kv := openKV(t, t.TempDir())
		t.Cleanup(func() { _ = kv.(closer).Close() })
		kvstorecontract.Run(t, kv)
	})

	t.Run("survives-restart", func(t *testing.T) {
		dir := t.TempDir()
		ctx := context.Background()
		kv1, err := kvbadger.Open(dir, kvbadger.WithValueLogGCInterval(0)) // default sync=true
		require.NoError(t, err)
		require.NoError(t, kv1.Put(ctx, "ns/b/key", []byte("durable")))
		require.NoError(t, kv1.(closer).Close())

		kv2, err := kvbadger.Open(dir, kvbadger.WithValueLogGCInterval(0))
		require.NoError(t, err)
		t.Cleanup(func() { _ = kv2.(closer).Close() })
		v, found, err := kv2.Get(ctx, "ns/b/key")
		require.NoError(t, err)
		require.True(t, found, "value persisted across restart")
		require.Equal(t, []byte("durable"), v)
	})
}

// scenario: per-store-writes-serialized — N concurrent writers all commit through the single-writer
// gateway with zero conflicts (and every value lands), proving the group-commit serialization.
func TestScenarioPerStoreWritesSerialized(t *testing.T) {
	kv := openKV(t, t.TempDir())
	t.Cleanup(func() { _ = kv.(closer).Close() })
	ctx := context.Background()

	const writers, perWriter = 16, 100
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := kv.Put(ctx, fmt.Sprintf("store/w%02d/k%03d", w, i), []byte("v")); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "gateway serializes writers → no conflicts")
	}
	keys, err := kv.List(ctx, "store/")
	require.NoError(t, err)
	require.Len(t, keys, writers*perWriter, "every concurrent write landed")
}

// scenario: store-teardown-drops-prefix — DropPrefix removes exactly one store's keys, others intact.
func TestScenarioStoreTeardownDropsPrefix(t *testing.T) {
	kv := openKV(t, t.TempDir())
	t.Cleanup(func() { _ = kv.(closer).Close() })
	ctx := context.Background()

	require.NoError(t, kv.Put(ctx, "store1/a", []byte("1")))
	require.NoError(t, kv.Put(ctx, "store1/b", []byte("2")))
	require.NoError(t, kv.Put(ctx, "store2/a", []byte("3")))

	require.NoError(t, kv.(dropper).DropPrefix("store1/"))

	g1, err := kv.List(ctx, "store1/")
	require.NoError(t, err)
	require.Empty(t, g1, "store1 wiped by DropPrefix")
	g2, err := kv.List(ctx, "store2/")
	require.NoError(t, err)
	require.Len(t, g2, 1, "store2 untouched")
}

// scenario: base-driver-has-no-durability-side-effects — with no CDC/Backup seam wired, the driver writes
// NO reserved (CDC outbox) keys and runs no background loop: a raw scan finds only the user keys.
func TestScenarioBaseDriverHasNoDurabilitySideEffects(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	kv, err := kvbadger.Open(dir, kvbadger.WithValueLogGCInterval(0)) // no WithCDC / WithBackup
	require.NoError(t, err)
	require.NoError(t, kv.Put(ctx, "ns/b/k1", []byte("v1")))
	require.NoError(t, kv.Put(ctx, "ns/b/k2", []byte("v2")))
	require.NoError(t, kv.(closer).Close())

	// raw scan: assert there are no reserved (NUL-prefixed) internal keys — i.e. no CDC outbox.
	raw, err := badger.Open(badger.DefaultOptions(dir).WithLoggingLevel(badger.ERROR))
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()
	var total, reserved int
	require.NoError(t, raw.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			total++
			if string(it.Item().Key()[:1]) == kvbadger.Reserved {
				reserved++
			}
		}
		return nil
	}))
	require.Equal(t, 2, total, "only the two user keys exist")
	require.Zero(t, reserved, "base driver writes no reserved/CDC keys")
}
