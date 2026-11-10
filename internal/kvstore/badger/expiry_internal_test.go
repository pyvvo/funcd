package badger

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// scenario: restore-takes-a-new-timeline
// TestRestoreTakesANewTimeline: the DR chain carries each key's expiry (Badger's backup format) but not the
// instance's timeline, so a restored lease keeps its value and expiry under a new version (ADR-0202's rule): its
// holder's compare-and-set with the version from before the restore loses, and one with the version it reads again
// wins; a write after the restore takes no version a write after the last ship took. A key that expired after it was shipped stays absent, and an expiry writes no delete record.
// oracle: docs/design/storage/kv-claims.fizz#StaleVersionLoses
// oracle: docs/design/storage/kv-claims.fizz#VersionNamesItsWrite
func TestRestoreTakesANewTimeline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bucket := newFakeBucket()
	kv, seams := openBackupKV(t, t.TempDir(), bucket)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	c := kv.(kvstore.Conditional)
	short, err := c.PutIf(ctx, "ns/s/short", []byte("x"), kvstore.Write{IfAbsent: true, TTL: time.Second})
	require.NoError(t, err)
	require.True(t, short.Done)
	lease, err := c.PutIf(ctx, "ns/s/lease", []byte("holder"), kvstore.Write{IfAbsent: true, TTL: time.Hour})
	require.NoError(t, err)
	before, _, err := c.GetEntry(ctx, "ns/s/lease")
	require.NoError(t, err)
	putKeys(t, kv, "ns/s/plain")
	ship(t, seams)
	require.Eventually(t, func() bool {
		_, found, err := kv.Get(ctx, "ns/s/short")
		return err == nil && !found
	}, 4*time.Second, 20*time.Millisecond)
	putKeys(t, kv, "ns/s/later")
	ship(t, seams)
	require.Zero(t, countPrefix(t, kvDB(kv), delRecordPrefix), "an expiry writes no delete record")
	lost, err := c.PutIf(ctx, "ns/s/lost", []byte("after the ship"), kvstore.Write{})
	require.NoError(t, err)

	dir := t.TempDir()
	restored, rseams := openBackupKV(t, dir, bucket)
	require.NoError(t, rseams.Backup.Restore(ctx))
	require.NoError(t, restored.(io.Closer).Close())
	dst, err := Open(dir, WithSyncWrites(false), WithValueLogGCInterval(0)) // the restored node starts on its store
	require.NoError(t, err)
	t.Cleanup(func() { _ = dst.(io.Closer).Close() })
	dc := dst.(kvstore.Conditional)
	require.NotEqual(t, kv.(*driver).timeline, dst.(*driver).timeline, "a restore mints another timeline")
	fresh, err := dc.PutIf(ctx, "ns/s/lost", []byte("after the restore"), kvstore.Write{})
	require.NoError(t, err)
	require.NotEqual(t, lost.Version, fresh.Version, "no write after the restore takes a version the lost part handed out")
	got, found, err := dc.GetEntry(ctx, "ns/s/lease")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, before.Value, got.Value)
	require.Equal(t, before.ExpiresAt, got.ExpiresAt, "the lease keeps its expiry")
	require.NotEqual(t, before.Version, got.Version, "a restore takes a new timeline")
	requireAbsent(t, dst, "ns/s/short")
	stale, err := dc.PutIf(ctx, "ns/s/lease", []byte("holder"), kvstore.Write{IfVersion: lease.Version, TTL: time.Hour})
	require.NoError(t, err)
	require.Equal(t, kvstore.Result{Version: got.Version}, stale, "a version from before the restore loses")
	renewed, err := dc.PutIf(ctx, "ns/s/lease", []byte("holder"), kvstore.Write{IfVersion: got.Version, TTL: time.Hour})
	require.NoError(t, err)
	require.True(t, renewed.Done, "the holder renews with the version it reads again")
}

// scenario: sweep-deletes-what-expired (change feed)
// TestCDCRecordCarriesExpiry: a write with a TTL publishes its key's expiry; the expiry publishes nothing until the
// sweep deletes the key, which publishes its delete.
func TestCDCRecordCarriesExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fb := &fakeBus{}
	kv, seams, err := OpenWithSeamsFor(t.TempDir(), nil, BackupConfig{}, fb, CDCConfig{Subject: "kv.changes"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	c := kv.(kvstore.Conditional)
	_, err = c.PutIf(ctx, "ns/s/claim", []byte("x"), kvstore.Write{IfAbsent: true, TTL: time.Second})
	require.NoError(t, err)
	require.NoError(t, kv.Put(ctx, "ns/s/plain", []byte("p")))
	e, _, err := c.GetEntry(ctx, "ns/s/claim")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, found, err := kv.Get(ctx, "ns/s/claim")
		return err == nil && !found
	}, 4*time.Second, 20*time.Millisecond)
	_, err = seams.CDC.(*cdc).drain(ctx)
	require.NoError(t, err)
	require.Len(t, fb.pubs, 2, "the expiry published nothing")
	require.Equal(t, changeRecord{Key: "ns/s/claim", Op: OpPut, Seq: 1, ExpiresAt: e.ExpiresAt}, fb.pubs[0])
	require.Zero(t, fb.pubs[1].ExpiresAt)
	swept, _, err := kv.(kvstore.Sweeper).Sweep(ctx, time.Now(), 10, func(string) bool { return false })
	require.NoError(t, err)
	require.Equal(t, []string{"ns/s/claim"}, swept)
	_, err = seams.CDC.(*cdc).drain(ctx)
	require.NoError(t, err)
	require.Len(t, fb.pubs, 3)
	require.Equal(t, changeRecord{Key: "ns/s/claim", Op: OpDelete, Seq: 3}, fb.pubs[2], "the sweep publishes the delete")
}

// scenario: restore-takes-a-new-timeline (restart)
// TestVersionAndExpirySurviveRestart: a key's version and expiry are Badger's own, so a reopened store keeps both and
// a compare-and-set against the version its holder kept still wins.
func TestVersionAndExpirySurviveRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	kv, err := Open(dir, WithSyncWrites(false), WithValueLogGCInterval(0))
	require.NoError(t, err)
	held, err := kv.(kvstore.Conditional).PutIf(ctx, "ns/s/lease", []byte("me"), kvstore.Write{IfAbsent: true, TTL: time.Hour})
	require.NoError(t, err)
	before, _, err := kv.(kvstore.Conditional).GetEntry(ctx, "ns/s/lease")
	require.NoError(t, err)
	require.NoError(t, kv.(io.Closer).Close())

	kv, err = Open(dir, WithSyncWrites(false), WithValueLogGCInterval(0))
	require.NoError(t, err)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	after, found, err := kv.(kvstore.Conditional).GetEntry(ctx, "ns/s/lease")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, before, after)
	renewed, err := kv.(kvstore.Conditional).PutIf(ctx, "ns/s/lease", []byte("me"), kvstore.Write{IfVersion: held.Version, TTL: time.Hour})
	require.NoError(t, err)
	require.True(t, renewed.Done)
	require.NotEqual(t, held.Version, renewed.Version, "a reopened store keeps its timeline and gives a new version")
}

// scenario: sweep-deletes-what-expired (index)
// TestRenewalKeepsOneIndexEntry: a versioned write drops the index key of the entry it replaces, so 50 renewals of one
// lease leave one index key, and a conditional delete leaves none.
func TestRenewalKeepsOneIndexEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv, err := Open(t.TempDir(), WithSyncWrites(false), WithValueLogGCInterval(0))
	require.NoError(t, err)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	c := kv.(kvstore.Conditional)
	res, err := c.PutIf(ctx, "ns/s/lease", []byte("me"), kvstore.Write{IfAbsent: true, TTL: time.Hour})
	require.NoError(t, err)
	for i := range 50 {
		res, err = c.PutIf(ctx, "ns/s/lease", []byte("me"), kvstore.Write{IfVersion: res.Version, TTL: time.Hour + time.Duration(i)*time.Second})
		require.NoError(t, err)
		require.True(t, res.Done)
	}
	require.Equal(t, 1, countPrefix(t, kvDB(kv), expiryPrefix))
	done, err := c.DeleteIf(ctx, "ns/s/lease", res.Version)
	require.NoError(t, err)
	require.True(t, done.Done)
	require.Zero(t, countPrefix(t, kvDB(kv), expiryPrefix))
}

// scenario: sweep-deletes-what-expired (index) — the index holds one key per live key with an expiry: a plain put, a
// delete and a store's DropPrefix each drop the index key of the entry they replace, as a versioned write does.
func TestEveryWriteDropsTheIndexKeyItReplaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv, err := Open(t.TempDir(), WithSyncWrites(false), WithValueLogGCInterval(0))
	require.NoError(t, err)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	for _, k := range []string{"ns/s/put", "ns/s/del", "ns/d/a", "ns/d/b"} {
		_, err := kv.(kvstore.Conditional).PutIf(ctx, k, []byte("x"), kvstore.Write{TTL: time.Hour})
		require.NoError(t, err)
	}
	require.Equal(t, 4, countPrefix(t, kvDB(kv), expiryPrefix))
	require.NoError(t, kv.Put(ctx, "ns/s/put", []byte("plain")))
	require.NoError(t, kv.Delete(ctx, "ns/s/del"))
	require.NoError(t, kv.(*driver).DropPrefix("ns/d/"))
	require.Zero(t, countPrefix(t, kvDB(kv), expiryPrefix), "no index key outlives the entry it names")
}

// scenario: sweep-deletes-what-compaction-removed — a value with a TTL replaced a plain one the chain holds, and expired;
// Badger's compaction removed both before the sweep. The sweep still deletes the key and the change feed publishes it,
// and once compaction also removed the sweep's delete marker, its delete record keeps a restore from bringing the plain
// value back.
// oracle: docs/design/storage/kv-claims.fizz#FeedAgrees
func TestSweepDeletesWhatCompactionRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bucket := newFakeBucket()
	fb := &fakeBus{}
	kv, seams, err := OpenWithSeamsFor(t.TempDir(), bucket, BackupConfig{}, fb, CDCConfig{Subject: "kv.changes"},
		WithSyncWrites(false), WithValueLogGCInterval(0))
	require.NoError(t, err)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	putKeys(t, kv, "ns/s/k")
	ship(t, seams)
	_, err = kv.(kvstore.Conditional).PutIf(ctx, "ns/s/k", []byte("lease"), kvstore.Write{TTL: time.Second})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, found, err := kv.Get(ctx, "ns/s/k")
		return err == nil && !found
	}, 4*time.Second, 20*time.Millisecond)
	churnUntilCompacted(t, kvDB(kv), "ns/s/k")
	swept, _, err := kv.(kvstore.Sweeper).Sweep(ctx, time.Now(), 10, func(string) bool { return false })
	require.NoError(t, err)
	require.Equal(t, []string{"ns/s/k"}, swept, "the sweep deletes what compaction removed")
	_, err = seams.CDC.(*cdc).drain(ctx)
	require.NoError(t, err)
	require.Equal(t, changeRecord{Key: "ns/s/k", Op: OpDelete, Seq: 3}, fb.pubs[len(fb.pubs)-1], "the change feed hears of it")
	churnUntilCompacted(t, kvDB(kv), "ns/s/k")
	ship(t, seams)
	dst, _ := restoreInto(t, bucket)
	requireAbsent(t, dst, "ns/s/k")
}

// scenario: sweep-deletes-what-compaction-removed (a delete first) — a plain delete of a key whose value with a TTL
// expired before the sweep writes its delete record too, so once compaction removed every version, a restore does not
// bring the plain value back.
func TestDeleteOfAnExpiredKeyWritesItsRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bucket := newFakeBucket()
	kv, seams := openBackupKV(t, t.TempDir(), bucket)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	putKeys(t, kv, "ns/s/k")
	ship(t, seams)
	_, err := kv.(kvstore.Conditional).PutIf(ctx, "ns/s/k", []byte("lease"), kvstore.Write{TTL: time.Second})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, found, err := kv.Get(ctx, "ns/s/k")
		return err == nil && !found
	}, 4*time.Second, 20*time.Millisecond)
	require.NoError(t, kv.Delete(ctx, "ns/s/k"))
	churnUntilCompacted(t, kvDB(kv), "ns/s/k")
	ship(t, seams)
	dst, _ := restoreInto(t, bucket)
	requireAbsent(t, dst, "ns/s/k")
}

// scenario: store-backup-carries-the-expiry (the load) — a load leaves out a record whose expiry passed, rather than
// writing an entry already expired that the sweep would delete later.
func TestLoadLeavesOutAnExpiredRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv, err := Open(t.TempDir(), WithSyncWrites(false), WithValueLogGCInterval(0))
	require.NoError(t, err)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	recs := []snapshot.Record{{Key: []byte("gone"), Value: []byte("z"), ExpiresAt: 1}, {Key: []byte("kept"), Value: []byte("y")}}
	i := 0
	require.NoError(t, kv.(*driver).LoadPrefix(ctx, "ns/l/", func() (snapshot.Record, error) {
		if i == len(recs) {
			return snapshot.Record{}, io.EOF
		}
		i++
		return recs[i-1], nil
	}))
	require.Zero(t, versionsLeft(kvDB(kv), []string{"ns/l/gone"}), "the expired record is not written")
	require.Zero(t, countPrefix(t, kvDB(kv), expiryPrefix), "and adds no index key")
	_, found, err := kv.Get(ctx, "ns/l/kept")
	require.NoError(t, err)
	require.True(t, found)
}
