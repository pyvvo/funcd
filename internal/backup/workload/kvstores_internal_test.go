package workload

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// scenario: store-backup-carries-the-expiry
// ADR-0320 on ADR-0209 Decision 7 and ADR-0222: a KVStore member's records carry each key's expiry, a load into a store
// keeps it, and a key whose expiry passed before the load stays out.
func TestKVStores_CarryEachKeysExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := memory.New()
	c := src.(kvstore.Conditional)
	_, err := c.PutIf(ctx, "orders/state/t/lease", []byte("me"), kvstore.Write{TTL: time.Hour})
	require.NoError(t, err)
	_, err = c.PutIf(ctx, "orders/state/t/short", []byte("x"), kvstore.Write{TTL: time.Second})
	require.NoError(t, err)
	require.NoError(t, src.Put(ctx, "orders/state/t/plain", []byte("p")))
	lease, _, err := c.GetEntry(ctx, "orders/state/t/lease")
	require.NoError(t, err)
	target, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	const path = "orders/b-uid/"
	k := KVStores(nil, src.(interface{ SnapshotPrefix(string) snapshot.Source }).SnapshotPrefix, nil)
	mf, err := k.Write(ctx, &writer{o: Options{Target: target, Path: path}, buf: make([]byte, 0, backup.PartBytes)}, "orders",
		Member{Kind: v1.KindKVStore, Name: "state"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, found, err := src.Get(ctx, "orders/state/t/short")
		return err == nil && !found
	}, 4*time.Second, 20*time.Millisecond, "the short key expires before the load")
	dst := memory.New()
	open := func(ctx context.Context, f backup.StoreFile) func() (snapshot.Record, error) {
		return Records(ctx, target, path, f, nil)
	}
	d := kvDestination{load: dst.(KVLoader), prefix: "orders/copy/"}
	require.NoError(t, d.Load(ctx, mf, open))
	keys, err := dst.List(ctx, "orders/copy/")
	require.NoError(t, err)
	require.Equal(t, []string{"orders/copy/t/lease", "orders/copy/t/plain"}, keys)
	got, _, err := dst.(kvstore.Conditional).GetEntry(ctx, "orders/copy/t/lease")
	require.NoError(t, err)
	require.Equal(t, lease.ExpiresAt, got.ExpiresAt, "the load keeps the key's expiry")
	require.Equal(t, "me", string(got.Value))
}
