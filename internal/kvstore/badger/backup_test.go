package badger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
)

// fakeBucket is an in-memory blob.Bucket recording puts (count + bytes) and able to inject upload failures
// — enough to prove default-off, incremental-delta, cursor-after-durable-upload, and restore parity.
type fakeBucket struct {
	// The port is embedded only to satisfy ListAfter, which the backup never calls.
	blob.Bucket
	mu      sync.Mutex
	objs    map[string][]byte
	puts    int
	failPut func(key string) error
}

func newFakeBucket() *fakeBucket { return &fakeBucket{objs: map[string][]byte{}} }

func (f *fakeBucket) Put(_ context.Context, key string, data []byte, _ blob.PutOptions) error {
	if f.failPut != nil {
		if err := f.failPut(key); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs[key] = append([]byte(nil), data...)
	f.puts++
	return nil
}

func (f *fakeBucket) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.objs[key]
	if !ok {
		return nil, fault.NotFoundf("fakeBucket.Get", "%q not found", key)
	}
	return append([]byte(nil), v...), nil
}

func (f *fakeBucket) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objs, key)
	return nil
}

func (f *fakeBucket) Exists(_ context.Context, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objs[key]
	return ok, nil
}

func (f *fakeBucket) Attributes(_ context.Context, key string) (blob.Attributes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.objs[key]
	if !ok {
		return blob.Attributes{}, fault.NotFoundf("fakeBucket.Attributes", "%q not found", key)
	}
	return blob.Attributes{Key: key, Size: int64(len(v))}, nil
}

func (f *fakeBucket) List(_ context.Context, prefix string) ([]blob.Attributes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []blob.Attributes
	for k, v := range f.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, blob.Attributes{Key: k, Size: int64(len(v))})
		}
	}
	return out, nil
}

func (f *fakeBucket) SignedURL(context.Context, string, blob.SignOptions) (string, error) {
	return "", fault.Invalidf("fakeBucket.SignedURL", "unsupported")
}
func (f *fakeBucket) Close() error { return nil }

// bytesUnder sums the size of every uploaded object whose key starts with prefix.
func (f *fakeBucket) bytesUnder(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for k, v := range f.objs {
		if strings.HasPrefix(k, prefix) {
			total += len(v)
		}
	}
	return total
}

func openRawDB(t *testing.T, dir string) *badger.DB {
	t.Helper()
	db, err := openDB(dir, false) // sync off — faster tests; correctness is unaffected
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func writeKeys(t *testing.T, db *badger.DB, prefix string, n int, valLen int) {
	t.Helper()
	val := make([]byte, valLen)
	for i := range val {
		val[i] = byte('a' + i%26)
	}
	require.NoError(t, db.Update(func(txn *badger.Txn) error {
		for i := 0; i < n; i++ {
			if err := txn.Set([]byte(fmt.Sprintf("%s%06d", prefix, i)), val); err != nil {
				return err
			}
		}
		return nil
	}))
}

func countKeys(db *badger.DB, prefix string) int {
	n := 0
	_ = db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		p := []byte(prefix)
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			n++
		}
		return nil
	})
	return n
}

// scenario: backup-default-off — no DR config ⇒ no seam constructed, no object-storage writes.
func TestScenarioBackupDefaultOff(t *testing.T) {
	kv, seams, err := OpenWithSeams(t.TempDir(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	require.Nil(t, seams.Backup, "no backup seam when DR is unconfigured")
	require.Nil(t, seams.CDC, "no CDC seam when unconfigured")

	bucket := newFakeBucket()
	ctx := context.Background()
	require.NoError(t, kv.Put(ctx, "a/b/c", []byte("v")))
	require.NoError(t, kv.Delete(ctx, "a/b/c"))
	require.Equal(t, 0, bucket.puts, "the base driver never writes to object storage")
}

// scenario: backup-enabled-requires-target — enabled with an empty target ⇒ fault.Invalid.
func TestScenarioBackupEnabledRequiresTarget(t *testing.T) {
	db := openRawDB(t, t.TempDir())
	_, err := NewBackup(db, nil, BackupConfig{})
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// scenario: incremental-ships-only-delta — after a backup, a small delta exports far fewer bytes.
func TestScenarioIncrementalShipsOnlyDelta(t *testing.T) {
	db := openRawDB(t, t.TempDir())
	bucket := newFakeBucket()
	b, err := NewBackup(db, bucket, BackupConfig{ChunkBytes: 1 << 16})
	require.NoError(t, err)
	bk := b.(*backup)
	ctx := context.Background()

	writeKeys(t, db, "k/", 500, 256)
	to1, err := bk.Ship(ctx)
	require.NoError(t, err)
	require.Greater(t, to1, uint64(0))
	first := bucket.bytesUnder("inc/")
	require.Greater(t, first, 0)

	writeKeys(t, db, "delta/", 3, 256)
	firstPrefix := fmt.Sprintf("inc/%020d", uint64(0))
	to2, err := bk.Ship(ctx)
	require.NoError(t, err)
	require.Greater(t, to2, to1, "cursor advanced")

	second := bucket.bytesUnder("inc/") - bucket.bytesUnder(firstPrefix)
	require.Greater(t, second, 0, "the delta shipped something")
	require.Less(t, second, first/10, "the incremental ships far fewer bytes than the full first export")
}

// scenario: cursor-advances-only-after-durable-upload — an upload failure leaves the cursor unchanged; the
// next (successful) run re-ships the interval and then advances.
func TestScenarioCursorAdvancesOnlyAfterDurableUpload(t *testing.T) {
	db := openRawDB(t, t.TempDir())
	bucket := newFakeBucket()
	bucket.failPut = func(string) error { return fault.Unavailablef("fakeBucket", "object storage down") }
	b, err := NewBackup(db, bucket, BackupConfig{ChunkBytes: 1 << 12})
	require.NoError(t, err)
	bk := b.(*backup)
	ctx := context.Background()
	writeKeys(t, db, "k/", 200, 256)

	_, err = bk.Ship(ctx)
	require.Error(t, err, "upload failed")
	cur, err := bk.cursor(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(0), cur, "cursor NOT advanced on a failed upload")

	bucket.failPut = nil
	to, err := bk.Ship(ctx)
	require.NoError(t, err)
	require.Greater(t, to, uint64(0))
	cur, err = bk.cursor(ctx)
	require.NoError(t, err)
	require.Equal(t, to, cur, "cursor advances after a durable upload")
}

// scenario: restore-reconstructs-store — a base + an incremental restore into a fresh instance with full
// key/value parity.
func TestScenarioRestoreReconstructsStore(t *testing.T) {
	ctx := context.Background()
	src := openRawDB(t, t.TempDir())
	bucket := newFakeBucket()
	b, err := NewBackup(src, bucket, BackupConfig{ChunkBytes: 1 << 16})
	require.NoError(t, err)
	bk := b.(*backup)

	writeKeys(t, src, "base/", 100, 128)
	require.NoError(t, bk.Rebaseline(ctx))
	writeKeys(t, src, "inc/", 20, 128)
	_, err = bk.Ship(ctx)
	require.NoError(t, err)

	dst := openRawDB(t, t.TempDir())
	rb, err := NewBackup(dst, bucket, BackupConfig{})
	require.NoError(t, err)
	require.NoError(t, rb.Restore(ctx))

	require.Equal(t, 100, countKeys(dst, "base/"), "base keys restored")
	require.Equal(t, 20, countKeys(dst, "inc/"), "incremental keys restored")

	// value parity on a sampled key
	require.NoError(t, dst.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("base/000042"))
		require.NoError(t, err)
		return item.Value(func(v []byte) error {
			require.Equal(t, 128, len(v))
			return nil
		})
	}))
}

// The backup's own cursor write is not a change: an idle tick uploads nothing, and a restored instance
// resumes from the chain's head, so a later ship and restore still carry every key.
func TestIssue790_IdleShipUploadsNothing(t *testing.T) {
	ctx := context.Background()
	src := openRawDB(t, t.TempDir())
	bucket := newFakeBucket()
	b, err := NewBackup(src, bucket, BackupConfig{})
	require.NoError(t, err)
	bk := b.(*backup)

	writeKeys(t, src, "k/", 10, 16)
	to, err := bk.Ship(ctx)
	require.NoError(t, err)
	puts := bucket.puts
	for i := 0; i < 20; i++ {
		_, err = bk.Ship(ctx)
		require.NoError(t, err)
	}
	require.Equal(t, puts, bucket.puts, "an idle tick uploads nothing")
	cur, err := bk.cursor(ctx)
	require.NoError(t, err)
	require.Equal(t, to, cur, "an idle tick leaves the cursor where it was")

	dst := openRawDB(t, t.TempDir())
	rb, err := NewBackup(dst, bucket, BackupConfig{})
	require.NoError(t, err)
	rbk := rb.(*backup)
	require.NoError(t, rbk.Restore(ctx))
	require.Equal(t, 10, countKeys(dst, "k/"), "every data key restored")
	cur, err = rbk.cursor(ctx)
	require.NoError(t, err)
	require.Equal(t, to, cur, "the restored instance resumes from the chain's head")

	writeKeys(t, dst, "more/", 5, 16)
	_, err = rbk.Ship(ctx)
	require.NoError(t, err)
	again := openRawDB(t, t.TempDir())
	ab, err := NewBackup(again, bucket, BackupConfig{})
	require.NoError(t, err)
	require.NoError(t, ab.Restore(ctx))
	require.Equal(t, 10, countKeys(again, "k/"))
	require.Equal(t, 5, countKeys(again, "more/"), "a ship after a restore continues the chain")
}

// A write committed while an incremental export runs is in that segment or a later one. Each producer of the
// export's Stream opens its own snapshot, so the cursor must not pass a version that an earlier snapshot
// could not see.
func TestIssue806_WriteDuringShipIsBackedUp(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const groups, perGroup = 2, 10000
	// Closing the seed flushes it to a table, whose splits give every export many key ranges.
	seed, err := openDB(dir, false)
	require.NoError(t, err)
	for g := range groups {
		writeKeys(t, seed, fmt.Sprintf("k/%d/", g), perGroup, 4)
	}
	require.NoError(t, seed.Close())
	src := openRawDB(t, dir)
	bucket := newFakeBucket()
	b, err := NewBackup(src, bucket, BackupConfig{ChunkBytes: 1 << 20})
	require.NoError(t, err)
	bk := b.(*backup)
	_, err = bk.Ship(ctx)
	require.NoError(t, err)

	const writers = 4
	var stop atomic.Bool
	var wg sync.WaitGroup
	stopWriters := func() {
		stop.Store(true)
		wg.Wait()
	}
	t.Cleanup(stopWriters)
	written := make([][]string, writers)
	for w := range writers {
		wg.Go(func() {
			for seq := 0; !stop.Load(); seq++ {
				k := fmt.Sprintf("k/%d/%06d-w%d-%d", rand.IntN(groups), rand.IntN(perGroup), w, seq)
				if err := src.Update(func(txn *badger.Txn) error { return txn.Set([]byte(k), nil) }); err != nil {
					t.Errorf("write %s: %v", k, err)
					return
				}
				written[w] = append(written[w], k)
			}
		})
	}
	ships := 0
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); ships++ {
		_, err := bk.Ship(ctx)
		require.NoError(t, err)
	}
	stopWriters()
	_, err = bk.Ship(ctx)
	require.NoError(t, err)

	dst := openRawDB(t, t.TempDir())
	rb, err := NewBackup(dst, bucket, BackupConfig{})
	require.NoError(t, err)
	require.NoError(t, rb.Restore(ctx))
	missing, total := 0, 0
	require.NoError(t, dst.View(func(txn *badger.Txn) error {
		for _, keys := range written {
			total += len(keys)
			for _, k := range keys {
				_, err := txn.Get([]byte(k))
				if errors.Is(err, badger.ErrKeyNotFound) {
					missing++
				} else if err != nil {
					return err
				}
			}
		}
		return nil
	}))
	require.Zero(t, missing, "%d of %d keys written during %d ships are in no backup segment", missing, total, ships)
}
