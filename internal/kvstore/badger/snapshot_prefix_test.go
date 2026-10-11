package badger

import (
	"context"
	"io"
	"testing"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/kvstore/kvstorecontract"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// The per-store export contract (ADR-0209 Decision 7) on the Badger driver.
func TestSnapshotPrefixContract(t *testing.T) {
	t.Parallel()
	kv, _ := openBackupKV(t, t.TempDir(), nil)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	kvstorecontract.RunSnapshotPrefix(t, kv)
}

// scenario: store-export — with the instance backup off, SnapshotPrefix("a/s1/") emits a/s1/'s live keys without the
// prefix, in key order, from one read under writers on a/s1/ and a/s10/ (the contract), and no reserved key or delete
// record, though both exist.
func TestScenarioStoreExport(t *testing.T) {
	t.Parallel()
	kv, seams := openBackupKV(t, t.TempDir(), nil)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	require.Nil(t, seams.Backup)
	require.NoError(t, kvDB(kv).Update(func(txn *badger.Txn) error {
		if err := txn.Set(delRecordKey("a/s1/gone"), nil); err != nil {
			return err
		}
		return txn.Set([]byte(Reserved+"a/s1/x"), []byte("reserved"))
	}))
	kvstorecontract.RunSnapshotPrefix(t, kv)
	var keys []string
	_, err := kv.(*driver).SnapshotPrefix("a/s1/").Snapshot(context.Background(), func(r snapshot.Record) error {
		keys = append(keys, string(r.Key))
		return nil
	})
	require.NoError(t, err)
	for _, k := range keys {
		require.NotContains(t, k, Reserved, "a reserved key or a delete record was exported")
	}
}

// TestLoadPrefixRefusesNonEmpty: the per-store load contract (ADR-0222 Decision 7) on the Badger driver, its writes
// through the gateway.
func TestLoadPrefixRefusesNonEmpty(t *testing.T) {
	t.Parallel()
	kv, _ := openBackupKV(t, t.TempDir(), nil)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	kvstorecontract.RunLoadPrefix(t, kv)
}
