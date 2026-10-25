package workload_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

func put(t *testing.T, b blob.Bucket, key string, n int) {
	t.Helper()
	require.NoError(t, b.Put(context.Background(), key, []byte(strings.Repeat("x", n)), blob.PutOptions{}))
}

const (
	uid1 = "0123456789abcdef0123456789abcdef"
	uid2 = "fedcba9876543210fedcba9876543210"
)

// TestFindSlotsSkipsOtherSchedules (ADR-0287 Decision 3): SlotCopies takes a's slot copies after `after`, oldest first,
// whole or not, and skips a-1's slots, a Backup by hand, an export, a's older slots and keys of no directory.
func TestFindSlotsSkipsOtherSchedules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	after := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	slot := func(m int) time.Time { return after.Add(time.Duration(m) * time.Minute) }
	dir := func(name v1.ObjectName, uid string) string { return "orders/" + string(name) + "-" + uid + "/" }
	for _, d := range []string{
		dir(v1.BackupSlotName("a", slot(-1)), uid1), dir(v1.BackupSlotName("a", slot(0)), uid1),
		dir(v1.BackupSlotName("a", slot(2)), uid1), dir(v1.BackupSlotName("a-1", slot(3)), uid1),
		dir("a-by-hand", uid1), dir("a-restore-before", uid1), dir(v1.BackupSlotName("a", slot(1)), uid2),
	} {
		put(t, b, d+"kv/orders-state/part-00000", 3)
		put(t, b, d+"manifest.yaml", 1)
	}
	put(t, b, dir(v1.BackupSlotName("a", slot(3)), uid1)+"kv/orders-state/part-00000", 3)
	put(t, b, "orders/"+string(v1.BackupSlotName("a", slot(4))), 1)
	put(t, b, "orders/"+string(v1.BackupSlotName("a", slot(5)))+"-short/manifest.yaml", 1)

	got, err := workload.SlotCopies(ctx, b, "orders", "a", after)
	require.NoError(t, err)
	var names []v1.ObjectName
	var whole []bool
	for _, c := range got {
		names, whole = append(names, c.Name), append(whole, c.Whole)
	}
	require.Equal(t, []v1.ObjectName{v1.BackupSlotName("a", slot(1)), v1.BackupSlotName("a", slot(2)),
		v1.BackupSlotName("a", slot(3))}, names)
	require.Equal(t, []bool{true, true, false}, whole)
	require.Equal(t, v1.UID(uid2), got[0].UID)
	require.Equal(t, dir(v1.BackupSlotName("a", slot(1)), uid2), got[0].Path)
	require.False(t, got[0].Completed.IsZero())
}

// TestListCopyMembers (ADR-0287 Decision 4): a directory's members and bytes from its kind directories, a Lake's last
// attempt only; no manifest, not whole; a path that names no Backup directory is refused.
func TestListCopyMembers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	d := "orders/b1-" + uid1 + "/"
	put(t, b, d+"blob/orders-files/part-00000", 5)
	put(t, b, d+"kv/orders-state/part-00000", 7)
	put(t, b, d+"kv/orders-state/part-00001", 2)
	put(t, b, d+"lake/lake/1/files/part-00000", 100)
	put(t, b, d+"lake/lake/2/files/part-00000", 11)
	put(t, b, d+"lake/lake/2/catalog.db/part-00000", 4)
	put(t, b, d+"sql/notes-db/part-00000", 13)
	put(t, b, d+"volume/fn/data/part-00000", 17)

	c, err := workload.ListCopy(ctx, b, d)
	require.NoError(t, err)
	require.False(t, c.Whole, "no manifest yet")
	require.Equal(t, []v1.BackupMember{
		{Kind: v1.KindBucket, Name: "orders-files", Bytes: 5},
		{Kind: v1.KindKVStore, Name: "orders-state", Bytes: 9},
		{Kind: v1.KindLake, Name: "lake", Bytes: 15},
		{Kind: v1.KindSQLDatabase, Name: "notes-db", Bytes: 13},
		{Kind: v1.KindFunction, Name: "fn", Volume: "data", Bytes: 17},
	}, c.Members)
	require.Equal(t, v1.ObjectName("b1"), c.Name)
	require.Equal(t, v1.UID(uid1), c.UID)

	put(t, b, d+"manifest.yaml", 1)
	c, err = workload.ListCopy(ctx, b, d)
	require.NoError(t, err)
	require.True(t, c.Whole)
	require.False(t, c.Completed.IsZero())

	_, err = workload.ListCopy(ctx, b, "orders/b1/")
	require.Error(t, err)
}
