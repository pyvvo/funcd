package workload_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

func put(t *testing.T, b blob.Bucket, key string, n int) {
	t.Helper()
	require.NoError(t, b.Put(context.Background(), key, []byte(strings.Repeat("x", n)), blob.PutOptions{}))
}

// kinds are the five member kinds, as a listing parses them.
func kinds() []workload.MemberKind {
	return []workload.MemberKind{workload.KVStores(nil, nil, nil), workload.Buckets(nil, nil, nil, nil),
		workload.Lakes(nil, nil, nil, nil, nil), workload.Databases(nil, nil), workload.Volumes(nil, nil)}
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

	got, err := workload.SlotCopies(ctx, b, "orders", "a", after, kinds())
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

	c, err := workload.ListCopy(ctx, b, d, kinds())
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
	c, err = workload.ListCopy(ctx, b, d, kinds())
	require.NoError(t, err)
	require.True(t, c.Whole)
	require.False(t, c.Completed.IsZero())

	_, err = workload.ListCopy(ctx, b, "orders/b1/", kinds())
	require.Error(t, err)
}

// pagedKeys is a target whose listing answers total keys in pages of the limit it is asked for, all of them key.
type pagedKeys struct {
	blob.Bucket
	key  string
	left int
}

func (b *pagedKeys) ListAfter(_ context.Context, _, _ string, limit int) ([]blob.Attributes, bool, error) {
	n := min(limit, b.left)
	b.left -= n
	return slices.Repeat([]blob.Attributes{{Key: b.key}}, n), b.left > 0, nil
}

// A listing of MaxListedKeys keys is read; one key more fails it, naming what it listed, whatever the target goes on answering.
func TestSlotCopies_RefusesAListingOverItsLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const key = "orders/a-not-a-copy/k"
	_, err := workload.SlotCopies(ctx, &pagedKeys{key: key, left: workload.MaxListedKeys}, "orders", "a", time.Time{}, kinds())
	require.NoError(t, err)

	target := &pagedKeys{key: key, left: workload.MaxListedKeys * 2}
	_, err = workload.SlotCopies(ctx, target, "orders", "a", time.Time{}, kinds())
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, `"orders/a-"`)
	require.Greater(t, target.left, workload.MaxListedKeys-2*1000, "it stopped at the limit")
}

// A listing of MaxListedKeyBytes bytes of keys is read; one key more fails it.
func TestSlotCopies_RefusesAListingOverItsKeyBytes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const size = 16 << 10
	key := "orders/a-not-a-copy/"
	key += strings.Repeat("k", size-len(key))
	_, err := workload.SlotCopies(ctx, &pagedKeys{key: key, left: workload.MaxListedKeyBytes / size}, "orders", "a", time.Time{}, kinds())
	require.NoError(t, err)

	_, err = workload.SlotCopies(ctx, &pagedKeys{key: key, left: workload.MaxListedKeyBytes/size + 1}, "orders", "a", time.Time{}, kinds())
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, `"orders/a-"`)
}

// A copy of MaxCopyMembers members is listed; one member more fails its listing, whichever way it is listed.
func TestListings_RefuseACopyOverItsMembers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	after := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	d := "orders/" + string(v1.BackupSlotName("a", after.Add(time.Minute))) + "-" + uid1 + "/"
	seeded := func(members int) blob.Bucket {
		b, err := gocloud.Open(ctx, "mem://")
		require.NoError(t, err)
		for i := range members {
			put(t, b, fmt.Sprintf("%skv/m-%d/part-00000", d, i), 1)
		}
		return b
	}
	at := seeded(workload.MaxCopyMembers)
	c, err := workload.ListCopy(ctx, at, d, kinds())
	require.NoError(t, err)
	require.Len(t, c.Members, workload.MaxCopyMembers)
	copies, err := workload.SlotCopies(ctx, at, "orders", "a", after, kinds())
	require.NoError(t, err)
	require.Len(t, copies, 1)

	over := seeded(workload.MaxCopyMembers + 1)
	_, err = workload.ListCopy(ctx, over, d, kinds())
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, d)
	_, err = workload.SlotCopies(ctx, over, "orders", "a", after, kinds())
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, d)
}

// A key that names no store is no member: a store's and a volume's names are DNS labels.
func TestListCopy_SkipsAKeyThatNamesNoStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	d := "orders/b1-" + uid1 + "/"
	long := strings.Repeat("a", 64)
	for _, key := range []string{"kv/" + long + "/part-00000", "kv/Not_A_Name/part-00000", "volume/cart/" + long + "/part-00000", "kv/orders-state/part-00000"} {
		put(t, b, d+key, 1)
	}
	c, err := workload.ListCopy(ctx, b, d, kinds())
	require.NoError(t, err)
	require.Len(t, c.Members, 1)
	require.Equal(t, v1.ObjectName("orders-state"), c.Members[0].Name)
}

func TestListCopy_RefusesAListingOverItsLimit(t *testing.T) {
	t.Parallel()
	d := "orders/b1-" + strings.Repeat("a", 32) + "/"
	_, err := workload.ListCopy(context.Background(), &pagedKeys{key: d + "kv/x/part-00000", left: workload.MaxListedKeys + 1}, d, kinds())
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, d)
}

// ADR-0311 Decision 6: a listing skips a key that no kind parses, as it skips a key of no kind directory, and lists
// the members of the kinds it is given.
func TestListCopy_SkipsAKeyNoKindParses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	d := "orders/b1-" + uid1 + "/"
	put(t, b, d+"kv/orders-state/part-00000", 7)
	put(t, b, d+"volume/fn/data/part-00000", 17)
	put(t, b, d+"vector/notes/part-00000", 3)
	put(t, b, d+"manifest.yaml", 1)

	c, err := workload.ListCopy(ctx, b, d, []workload.MemberKind{workload.KVStores(nil, nil, nil)})
	require.NoError(t, err)
	require.True(t, c.Whole)
	require.Equal(t, []v1.BackupMember{{Kind: v1.KindKVStore, Name: "orders-state", Bytes: 7}}, c.Members)
}
