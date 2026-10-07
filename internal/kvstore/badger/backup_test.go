package badger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/kvstore/kvstorecontract"
)

// fakeBucket is an in-memory blob.Bucket recording puts (count + bytes) and able to inject upload failures
// — enough to prove default-off, incremental-delta, cursor-after-durable-upload, and restore parity.
type fakeBucket struct {
	// The port is embedded only to satisfy ListAfter, which the backup never calls.
	blob.Bucket
	mu       sync.Mutex
	objs     map[string][]byte
	puts     int
	failPut  func(key string) error
	onDelete func(key string)
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
	if f.onDelete != nil {
		f.onDelete(key)
	}
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

// partCountingBucket counts the bytes of every uploaded part and drops them, so a heap measurement of an
// export does not count the fake's own copies. Everything else (the manifest) goes to the embedded fake.
type partCountingBucket struct {
	*fakeBucket
	partBytes int
}

func (p *partCountingBucket) Put(ctx context.Context, key string, data []byte, opts blob.PutOptions) error {
	if strings.Contains(key, "/part-") {
		p.partBytes += len(data)
		return nil
	}
	return p.fakeBucket.Put(ctx, key, data, opts)
}

// peakHeap runs f and returns how far the Go heap rose above its post-GC level while f ran. A low GC target
// keeps the sampled heap close to the live heap, so the result does not depend on when the pacer collects.
func peakHeap(f func()) uint64 {
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base, top := ms.HeapAlloc, ms.HeapAlloc
	stop := make(chan struct{})
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				top = max(top, m.HeapAlloc)
			}
		}
	}()
	f()
	close(stop)
	done.Wait()
	return top - base
}

// A re-baseline exports with one producer (ADR-0067 Decision 2): its peak heap stays near that of a
// single-producer Stream export of the same store through the same chunk writer, instead of also holding
// the batch buffers of Badger's default eight producers.
func TestIssue805_RebaselineExportsWithLowConcurrency(t *testing.T) {
	const keys, valLen = 50_000, 256
	dir := t.TempDir()
	db, err := openDB(dir, false)
	require.NoError(t, err)
	wb := db.NewWriteBatch()
	val := make([]byte, valLen)
	for i := 0; i < keys; i++ {
		require.NoError(t, wb.Set([]byte(fmt.Sprintf("k/%07d", i)), val))
	}
	require.NoError(t, wb.Flush())
	require.NoError(t, db.Close()) // the keys move into tables, as in a long-running instance
	db = openRawDB(t, dir)

	single := func(w io.Writer) {
		s := db.NewStream()
		s.NumGo = 1
		_, err := s.Backup(w, 0)
		require.NoError(t, err)
	}
	single(io.Discard) // the first export fills Badger's block and index caches; neither measurement counts them

	ctx := context.Background()
	bucket := &partCountingBucket{fakeBucket: newFakeBucket()}
	b, err := NewBackup(db, bucket, BackupConfig{ChunkBytes: 1 << 20})
	require.NoError(t, err)
	bk := b.(*backup)
	rebaseline := peakHeap(func() { require.NoError(t, bk.Rebaseline(ctx)) })
	require.Greater(t, bucket.partBytes, keys*valLen, "the re-baseline exported the whole store")
	low := peakHeap(func() {
		w := bk.newChunkWriter(ctx, "single")
		single(w)
		require.NoError(t, w.Close())
	})
	t.Logf("peak heap: Rebaseline %d MiB, single-producer export %d MiB", rebaseline>>20, low>>20)
	require.Less(t, rebaseline, 2*low,
		"the re-baseline export holds more than one producer's buffers: its Stream.NumGo is not low (ADR-0067 Decision 2)")
}

// A process that restarts more often than kvstore.backup.rebaseline still re-baselines once a period has passed
// since the recorded base: three runs of 1.5 s with a 2 s period, a write before each. A later run's base holds
// that run's write.
func TestIssue807_RebaselineRunsAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	db := openRawDB(t, t.TempDir())
	bucket := newFakeBucket()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := BackupConfig{Interval: 200 * time.Millisecond, Rebaseline: 2 * time.Second}
	for run := range 3 {
		writeKeys(t, db, fmt.Sprintf("run%d/", run), 10, 16)
		b, err := NewBackup(db, bucket, cfg)
		require.NoError(t, err)
		runCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		RunBackup(runCtx, b, logger)
		cancel()
	}

	man, err := (&backup{bucket: bucket}).loadManifest(ctx)
	require.NoError(t, err)
	require.NotNil(t, man.Base, "no full re-baseline in more than two periods of restarting runs (incs=%d)", len(man.Incs))
	dst := openRawDB(t, t.TempDir())
	require.NoError(t, (&backup{db: dst, bucket: bucket}).loadSegment(ctx, *man.Base))
	require.Equal(t, 10, countKeys(dst, "run1/"), "the base predates the second run: no later run re-baselined")
}

// At start, RunBackup re-baselines at once when the manifest's base records no time (a manifest written before
// the field existed), and not when the recorded base is younger than the period.
func TestIssue807_StartRebaselinesOnlyWhenDue(t *testing.T) {
	ctx := context.Background()
	db := openRawDB(t, t.TempDir())
	bucket := newFakeBucket()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := BackupConfig{Interval: 50 * time.Millisecond, Rebaseline: time.Hour}
	bk := &backup{db: db, bucket: bucket}
	require.NoError(t, bk.saveManifest(ctx, manifest{Base: &segment{Prefix: "base/old"}}))
	current := func() manifest {
		man, err := bk.loadManifest(ctx)
		if err != nil {
			return manifest{}
		}
		return man
	}
	runUntil := func(done func(manifest) bool, msg string) {
		b, err := NewBackup(db, bucket, cfg)
		require.NoError(t, err)
		runCtx, cancel := context.WithCancel(ctx)
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			RunBackup(runCtx, b, logger)
		}()
		defer func() {
			cancel()
			<-stopped
		}()
		require.Eventually(t, func() bool { return done(current()) }, 10*time.Second, 20*time.Millisecond, msg)
	}

	writeKeys(t, db, "a/", 10, 16)
	runUntil(func(m manifest) bool { return m.Base != nil && m.Base.Prefix != "base/old" },
		"a base with no recorded time was not re-baselined at start")
	rebased := current().Base.Prefix

	writeKeys(t, db, "b/", 10, 16)
	runUntil(func(m manifest) bool { return len(m.Incs) > 0 }, "the write after a fresh base was not shipped as an incremental")
	require.Equal(t, rebased, current().Base.Prefix, "a restart re-baselined although the recorded base is younger than the period")
}

// RunBackup now re-baselines at start, so a stop soon after a boot finds an export running: the driver's Close
// waits for it instead of closing the db under it (Badger panics on a closed db).
func TestIssue807_CloseWaitsForRunningRebaseline(t *testing.T) {
	ctx := context.Background()
	bucket := newFakeBucket()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	bucket.failPut = func(key string) error {
		if strings.Contains(key, "/part-") {
			once.Do(func() {
				close(entered)
				<-release
			})
		}
		return nil
	}
	kv, seams, err := OpenWithSeams(t.TempDir(), func(db *badger.DB) (Backup, error) {
		return NewBackup(db, bucket, BackupConfig{})
	}, nil)
	require.NoError(t, err)
	require.NoError(t, kv.Put(ctx, "a/b/c", []byte("v")))

	rebased := make(chan error, 1)
	go func() { rebased <- seams.Backup.(*backup).Rebaseline(ctx) }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- kv.(io.Closer).Close() }()
	select {
	case err := <-closed:
		close(release)
		t.Fatalf("Close returned (%v) while a re-baseline was running", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-rebased, "the re-baseline finished on an open db")
	require.NoError(t, <-closed)
}

// openBackupKV opens the driver on dir through the daemon-facing opener; a nil bucket means DR backup is off.
func openBackupKV(t *testing.T, dir string, bucket blob.Bucket) (kvstore.KV, Seams) {
	t.Helper()
	return openBackupKVWith(t, dir, bucket, BackupConfig{})
}

// openBackupKVWith is openBackupKV with a backup config.
func openBackupKVWith(t *testing.T, dir string, bucket blob.Bucket, cfg BackupConfig) (kvstore.KV, Seams) {
	t.Helper()
	kv, seams, err := OpenWithSeamsFor(dir, bucket, cfg, nil, CDCConfig{},
		WithSyncWrites(false), WithValueLogGCInterval(0))
	require.NoError(t, err)
	return kv, seams
}

// A key deleted while DR backup is off stays deleted in a restore taken after backup is on again, although
// the store may hold no version of the deletion (a native DropPrefix, or a delete marker compaction dropped).
func TestIssue808_DeleteWhileBackupOffStaysDeletedAfterRestore(t *testing.T) {
	cases := map[string]func(t *testing.T, kv kvstore.KV){
		"Delete then churn": func(t *testing.T, kv kvstore.KV) {
			ctx := context.Background()
			require.NoError(t, kv.Delete(ctx, "s/victim"))
			val := make([]byte, 4<<10)
			for i := 0; i < 12000; i++ {
				require.NoError(t, kv.Put(ctx, fmt.Sprintf("churn/%06d", i), val))
			}
		},
		"native DropPrefix": func(t *testing.T, kv kvstore.KV) {
			require.NoError(t, kv.(*driver).DropPrefix("s/"))
		},
	}
	for name, deleteVictim := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			bucket := newFakeBucket()

			kv, seams := openBackupKV(t, dir, bucket)
			require.NoError(t, kv.Put(ctx, "keep", []byte("k")))
			require.NoError(t, kv.Put(ctx, "s/victim", []byte("v")))
			_, err := seams.Backup.Ship(ctx)
			require.NoError(t, err)
			require.NoError(t, kv.(io.Closer).Close())

			kv, seams = openBackupKV(t, dir, nil)
			require.Nil(t, seams.Backup)
			deleteVictim(t, kv)
			require.NoError(t, kv.(io.Closer).Close())
			// A second open without backup lets compaction run over the deletion.
			kv, _ = openBackupKV(t, dir, nil)
			require.NoError(t, kv.(io.Closer).Close())

			kv, seams = openBackupKV(t, dir, bucket)
			_, err = seams.Backup.Ship(ctx)
			require.NoError(t, err)
			_, found, err := kv.Get(ctx, "s/victim")
			require.NoError(t, err)
			require.False(t, found, "deleted in the live store")
			require.NoError(t, kv.(io.Closer).Close())

			dst, dseams := openBackupKV(t, t.TempDir(), bucket)
			t.Cleanup(func() { _ = dst.(io.Closer).Close() })
			require.NoError(t, dseams.Backup.Restore(ctx))
			_, found, err = dst.Get(ctx, "keep")
			require.NoError(t, err)
			require.True(t, found, "keep restored")
			_, found, err = dst.Get(ctx, "s/victim")
			require.NoError(t, err)
			require.False(t, found, "a key deleted while backup was off came back after the restore")
		})
	}
}

// Only an open without the backup seam ends the chain: a restart with backup on ships an incremental that
// continues it, while a plain Open in between makes the next Ship re-baseline.
func TestIssue808_ReopenContinuesChainOnlyWithBackupOn(t *testing.T) {
	for name, plainOpen := range map[string]bool{"backup stays on": false, "plain Open in between": true} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			bucket := newFakeBucket()
			kv, seams := openBackupKV(t, dir, bucket)
			require.NoError(t, kv.Put(ctx, "a", []byte("1")))
			to, err := seams.Backup.Ship(ctx)
			require.NoError(t, err)
			require.NoError(t, kv.(io.Closer).Close())
			if plainOpen {
				kv, err = Open(dir, WithSyncWrites(false), WithValueLogGCInterval(0))
				require.NoError(t, err)
				require.NoError(t, kv.(io.Closer).Close())
			}

			kv, seams = openBackupKV(t, dir, bucket)
			t.Cleanup(func() { _ = kv.(io.Closer).Close() })
			require.NoError(t, kv.Put(ctx, "b", []byte("2")))
			_, err = seams.Backup.Ship(ctx)
			require.NoError(t, err)
			man, err := seams.Backup.(*backup).loadManifest(ctx)
			require.NoError(t, err)
			if plainOpen {
				require.NotNil(t, man.Base, "the next Ship re-baselined")
				require.Empty(t, man.Incs)
				return
			}
			require.Nil(t, man.Base, "no re-baseline")
			require.Len(t, man.Incs, 2)
			require.Equal(t, to, man.Incs[1].Since, "the incremental continues from the cursor")
		})
	}
}

// The first backup ever (no cursor, no chain) ships an incremental since 0, as before.
func TestIssue808_FirstBackupShipsIncremental(t *testing.T) {
	ctx := context.Background()
	kv, seams := openBackupKV(t, t.TempDir(), newFakeBucket())
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	require.NoError(t, kv.Put(ctx, "a", []byte("1")))
	_, err := seams.Backup.Ship(ctx)
	require.NoError(t, err)
	man, err := seams.Backup.(*backup).loadManifest(ctx)
	require.NoError(t, err)
	require.Nil(t, man.Base)
	require.Len(t, man.Incs, 1)
	require.Zero(t, man.Incs[0].Since)
}

// A re-baseline never writes into the live base: after backup was off twice, both re-baselines start with no
// cursor, and a base named after the cursor alone reused the live base's name, so a failure before the
// manifest moved left the live base holding parts of the unfinished export (#808).
func TestIssue808_FailedRebaselineLeavesLiveBaseIntact(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bucket := newFakeBucket()
	offThenOn := func(key string) (kvstore.KV, Seams) {
		kv, err := Open(dir, WithSyncWrites(false), WithValueLogGCInterval(0))
		require.NoError(t, err)
		require.NoError(t, kv.Put(ctx, key, []byte(key)))
		require.NoError(t, kv.(io.Closer).Close())
		return openBackupKV(t, dir, bucket)
	}

	kv, seams := openBackupKV(t, dir, bucket)
	require.NoError(t, kv.Put(ctx, "a", []byte("a")))
	_, err := seams.Backup.Ship(ctx)
	require.NoError(t, err)
	require.NoError(t, kv.(io.Closer).Close())

	kv, seams = offThenOn("b")
	_, err = seams.Backup.Ship(ctx)
	require.NoError(t, err)
	require.NoError(t, kv.(io.Closer).Close())
	live, err := seams.Backup.(*backup).loadManifest(ctx)
	require.NoError(t, err)
	require.NotNil(t, live.Base)
	before := map[string][]byte{}
	bucket.mu.Lock()
	for i := 0; i < live.Base.Parts; i++ {
		k := fmt.Sprintf("%s/part-%05d", live.Base.Prefix, i)
		before[k] = append([]byte(nil), bucket.objs[k]...)
	}
	bucket.mu.Unlock()

	kv, seams = offThenOn("c")
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	bucket.failPut = func(key string) error {
		if key == manifestKey {
			return errors.New("target down")
		}
		return nil
	}
	_, err = seams.Backup.Ship(ctx)
	require.Error(t, err, "the re-baseline fails before the manifest moves")

	man, err := seams.Backup.(*backup).loadManifest(ctx)
	require.NoError(t, err)
	require.Equal(t, live, man, "the manifest still names the live base")
	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	for k, b := range before {
		require.Equal(t, b, bucket.objs[k], "%s was overwritten by the unfinished re-baseline", k)
	}
}

// kvDB is the Badger instance under a driver opened by openBackupKV.
func kvDB(kv kvstore.KV) *badger.DB { return kv.(*driver).db }

// churn overwrites n keys under churn/ in one write batch.
func churn(db *badger.DB, n, valLen int) error {
	wb := db.NewWriteBatch()
	val := make([]byte, valLen)
	for i := 0; i < n; i++ {
		if err := wb.Set([]byte(fmt.Sprintf("churn/%06d", i)), val); err != nil {
			wb.Cancel()
			return err
		}
	}
	return wb.Flush()
}

// versionsLeft counts the keys of which db holds any version, delete markers included.
func versionsLeft(db *badger.DB, keys []string) int {
	left := 0
	_ = db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.AllVersions = true
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for _, k := range keys {
			it.Seek([]byte(k))
			if it.Valid() && string(it.Item().Key()) == k {
				left++
			}
		}
		return nil
	})
	return left
}

// churnUntilCompacted adds churn to db until compaction has dropped every version of keys, delete markers
// included. It polls an all-versions iterator with a bound: a fixed wait fails about 2 runs in 10 (#798), and
// db.Flatten alone left the marker.
func churnUntilCompacted(t *testing.T, db *badger.DB, keys ...string) {
	t.Helper()
	require.Eventually(t, func() bool {
		if versionsLeft(db, keys) == 0 {
			return true
		}
		if err := churn(db, 20_000, 1024); err != nil {
			t.Errorf("churn: %v", err)
			return true
		}
		return versionsLeft(db, keys) == 0
	}, 2*time.Minute, 10*time.Millisecond, "compaction kept a version of %d keys", len(keys))
}

func ship(t *testing.T, seams Seams) {
	t.Helper()
	_, err := seams.Backup.Ship(context.Background())
	require.NoError(t, err)
}

// restoreInto restores the chain in bucket into a fresh instance with the backup seam wired.
func restoreInto(t *testing.T, bucket blob.Bucket) (kvstore.KV, Seams) {
	t.Helper()
	kv, seams := openBackupKV(t, t.TempDir(), bucket)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	require.NoError(t, seams.Backup.Restore(context.Background()))
	return kv, seams
}

func putKeys(t *testing.T, kv kvstore.KV, keys ...string) {
	t.Helper()
	for _, k := range keys {
		require.NoError(t, kv.Put(context.Background(), k, []byte("v-"+k)))
	}
}

func seq(prefix string, n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("%s%04d", prefix, i)
	}
	return keys
}

func requireAbsent(t *testing.T, kv kvstore.KV, keys ...string) {
	t.Helper()
	for _, k := range keys {
		_, found, err := kv.Get(context.Background(), k)
		require.NoError(t, err)
		require.False(t, found, "%.40s came back", k)
	}
}

// requireSameKeys asserts that dst lists exactly the keys src lists.
func requireSameKeys(t *testing.T, src, dst kvstore.KV) {
	t.Helper()
	want, err := src.List(context.Background(), "")
	require.NoError(t, err)
	got, err := dst.List(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func countRecords(db *badger.DB) int { return countKeys(db, delRecordPrefix) }

// recordVersion is the version of key's delete record, 0 when there is none.
func recordVersion(t *testing.T, db *badger.DB, key string) uint64 {
	t.Helper()
	var v uint64
	require.NoError(t, db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(delRecordKey(key))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		v = item.Version()
		return nil
	}))
	return v
}

func currentManifest(t *testing.T, bucket blob.Bucket) manifest {
	t.Helper()
	man, err := (&backup{bucket: bucket}).loadManifest(context.Background())
	require.NoError(t, err)
	return man
}

// scenario: delete-survives-compaction — the #798 reproduction: a deleted key whose delete marker compaction
// dropped before the next Ship stays deleted in a restore.
func TestScenarioDeleteSurvivesCompaction(t *testing.T) {
	ctx := context.Background()
	bucket := newFakeBucket()
	kv, seams := openBackupKV(t, t.TempDir(), bucket)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	putKeys(t, kv, append(seq("k/", 50), "victim")...)
	ship(t, seams)

	require.NoError(t, kv.Delete(ctx, "victim"))
	require.NoError(t, churn(kvDB(kv), 100_000, 64))
	churnUntilCompacted(t, kvDB(kv), "victim")
	ship(t, seams)

	dst, _ := restoreInto(t, bucket)
	requireAbsent(t, dst, "victim")
	requireSameKeys(t, kv, dst)
}

// scenario: delete-survives-restart — a delete before a restart with backup on stays deleted in a restore,
// although compaction dropped its marker before the next Ship.
func TestScenarioDeleteSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bucket := newFakeBucket()
	kv, seams := openBackupKV(t, dir, bucket)
	putKeys(t, kv, append(seq("k/", 50), "victim")...)
	ship(t, seams)
	require.NoError(t, kv.Delete(ctx, "victim"))
	require.NoError(t, kv.(io.Closer).Close())

	kv, seams = openBackupKV(t, dir, bucket)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	churnUntilCompacted(t, kvDB(kv), "victim")
	ship(t, seams)

	dst, _ := restoreInto(t, bucket)
	requireAbsent(t, dst, "victim")
	requireSameKeys(t, kv, dst)
}

// scenario: delete-survives-target-outage — a delete made while the target is down stays deleted in a restore
// after the next successful Ship, although compaction dropped its marker during the outage.
func TestScenarioDeleteSurvivesTargetOutage(t *testing.T) {
	ctx := context.Background()
	bucket := newFakeBucket()
	kv, seams := openBackupKV(t, t.TempDir(), bucket)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	putKeys(t, kv, append(seq("k/", 50), "victim")...)
	ship(t, seams)

	require.NoError(t, kv.Delete(ctx, "victim"))
	bucket.failPut = func(string) error { return fault.Unavailablef("fakeBucket", "object storage down") }
	for range 3 {
		require.NoError(t, churn(kvDB(kv), 50_000, 64))
		_, err := seams.Backup.Ship(ctx)
		require.Error(t, err)
	}
	churnUntilCompacted(t, kvDB(kv), "victim")
	bucket.failPut = nil
	ship(t, seams)

	dst, _ := restoreInto(t, bucket)
	requireAbsent(t, dst, "victim")
	requireSameKeys(t, kv, dst)
}

// scenario: delete-then-set-keeps-value — a key deleted and set again, in two requests or in one gateway batch,
// holds its new value after a restore, although its delete record exists.
func TestScenarioDeleteThenSetKeepsValue(t *testing.T) {
	cases := map[string]func(t *testing.T, kv kvstore.KV){
		"two requests": func(t *testing.T, kv kvstore.KV) {
			require.NoError(t, kv.Delete(context.Background(), "k"))
			require.NoError(t, kv.Put(context.Background(), "k", []byte("new")))
		},
		"one gateway batch": func(t *testing.T, kv kvstore.KV) {
			batch := []*writeReq{
				{key: "k", del: true, done: make(chan error, 1)},
				{key: "k", val: []byte("new"), done: make(chan error, 1)},
			}
			kv.(*driver).commit(batch)
			for _, r := range batch {
				require.NoError(t, <-r.done)
			}
			var keyVersion uint64
			require.NoError(t, kvDB(kv).View(func(txn *badger.Txn) error {
				item, err := txn.Get([]byte("k"))
				if err == nil {
					keyVersion = item.Version()
				}
				return err
			}))
			require.Equal(t, keyVersion, recordVersion(t, kvDB(kv), "k"), "the delete and the set share a commit version")
		},
	}
	for name, deleteThenSet := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			bucket := newFakeBucket()
			kv, seams := openBackupKV(t, t.TempDir(), bucket)
			t.Cleanup(func() { _ = kv.(io.Closer).Close() })
			require.NoError(t, kv.Put(ctx, "k", []byte("old")))
			ship(t, seams)

			deleteThenSet(t, kv)
			require.Equal(t, 1, countRecords(kvDB(kv)), "the delete wrote its record")
			ship(t, seams)

			dst, _ := restoreInto(t, bucket)
			v, found, err := dst.Get(ctx, "k")
			require.NoError(t, err)
			require.True(t, found, "the restore deleted a key set again after its delete")
			require.Equal(t, []byte("new"), v)
		})
	}
}

// scenario: drop-prefix-stays-deleted — a DropPrefix with backup on stays complete in a restore after churn.
func TestScenarioDropPrefixStaysDeleted(t *testing.T) {
	ctx := context.Background()
	bucket := newFakeBucket()
	kv, seams := openBackupKV(t, t.TempDir(), bucket)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	dropped := seq("s/", 20)
	putKeys(t, kv, append(seq("k/", 20), dropped...)...)
	ship(t, seams)

	require.NoError(t, kv.(*driver).DropPrefix("s/"))
	require.Equal(t, len(dropped), countRecords(kvDB(kv)), "one record per dropped key")
	churnUntilCompacted(t, kvDB(kv), dropped...)
	ship(t, seams)

	dst, _ := restoreInto(t, bucket)
	left, err := dst.List(ctx, "s/")
	require.NoError(t, err)
	require.Empty(t, left, "a key under the dropped prefix came back")
	requireSameKeys(t, kv, dst)
}

// scenario: records-pruned-at-rebaseline — a re-baseline leaves the records of earlier deletes out of the new
// base and prunes them from the store, and a restore from the new base still lacks the deleted keys.
func TestScenarioRecordsPrunedAtRebaseline(t *testing.T) {
	ctx := context.Background()
	bucket := newFakeBucket()
	kv, seams := openBackupKV(t, t.TempDir(), bucket)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	deleted := seq("d/", 10)
	putKeys(t, kv, append(seq("k/", 20), deleted...)...)
	ship(t, seams)
	for _, k := range deleted {
		require.NoError(t, kv.Delete(ctx, k))
	}
	require.Equal(t, len(deleted), countRecords(kvDB(kv)))

	require.NoError(t, seams.Backup.(*backup).Rebaseline(ctx))
	require.Zero(t, countRecords(kvDB(kv)), "the store still holds records the base reflects")
	man := currentManifest(t, bucket)
	require.Equal(t, manifestFormat, man.Format)
	require.NotNil(t, man.Base)
	base := openRawDB(t, t.TempDir())
	require.NoError(t, (&backup{db: base, bucket: bucket}).loadSegment(ctx, *man.Base))
	records := make([]string, len(deleted))
	for i, k := range deleted {
		records[i] = string(delRecordKey(k))
	}
	require.Zero(t, versionsLeft(base, records), "the base carries a version of a record it reflects")

	dst, _ := restoreInto(t, bucket)
	requireAbsent(t, dst, deleted...)
	requireSameKeys(t, kv, dst)
}

// The record prune deletes only records at or below the base's read timestamp: a later delete keeps its record
// for the next incremental.
func TestPruneKeepsRecordsAboveReadTs(t *testing.T) {
	ctx := context.Background()
	kv, seams := openBackupKV(t, t.TempDir(), newFakeBucket())
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	putKeys(t, kv, "a", "b")
	require.NoError(t, kv.Delete(ctx, "a"))
	readTs := recordVersion(t, kvDB(kv), "a")
	require.NoError(t, kv.Delete(ctx, "b"))
	require.Greater(t, recordVersion(t, kvDB(kv), "b"), readTs)

	require.NoError(t, seams.Backup.(*backup).pruneDelRecords(ctx, readTs))
	require.Zero(t, recordVersion(t, kvDB(kv), "a"), "a record at the read timestamp stays")
	require.NotZero(t, recordVersion(t, kvDB(kv), "b"), "a record above the read timestamp was pruned")
}

// A prune transaction re-reads its records: a key deleted again after the chunk was scanned keeps its newer
// record.
func TestPruneChunkKeepsRecordOfLaterDelete(t *testing.T) {
	ctx := context.Background()
	kv, seams := openBackupKV(t, t.TempDir(), newFakeBucket())
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	putKeys(t, kv, "a", "b")
	require.NoError(t, kv.Delete(ctx, "a"))
	require.NoError(t, kv.Delete(ctx, "b"))
	readTs := recordVersion(t, kvDB(kv), "b")
	scanned := [][]byte{delRecordKey("a"), delRecordKey("b")}

	putKeys(t, kv, "a")
	require.NoError(t, kv.Delete(ctx, "a"))
	require.NoError(t, seams.Backup.(*backup).pruneChunk(scanned, readTs))
	require.Greater(t, recordVersion(t, kvDB(kv), "a"), readTs, "the prune deleted the record of a later delete")
	require.Zero(t, recordVersion(t, kvDB(kv), "b"))
}

// scenario: prune-failure-keeps-base — a record prune that fails after the manifest is saved leaves the new base
// live and Rebaseline returning nil; the next re-baseline prunes the records.
func TestScenarioPruneFailureKeepsBase(t *testing.T) {
	bucket := newFakeBucket()
	var logs bytes.Buffer
	cfg := BackupConfig{Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	kv, seams := openBackupKVWith(t, t.TempDir(), bucket, cfg)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	deleted := seq("d/", 10)
	putKeys(t, kv, append(seq("k/", 20), deleted...)...)
	ship(t, seams)
	for _, k := range deleted {
		require.NoError(t, kv.Delete(context.Background(), k))
	}
	bk := seams.Backup.(*backup)

	ctx, cancel := context.WithCancel(context.Background())
	bucket.onDelete = func(string) { cancel() } // the segment prune runs after the manifest and the cursor
	require.NoError(t, bk.Rebaseline(ctx))
	bucket.onDelete = nil
	man := currentManifest(t, bucket)
	require.NotNil(t, man.Base)
	require.Empty(t, man.Incs, "the new base is live")
	require.Equal(t, manifestFormat, man.Format)
	require.Equal(t, len(deleted), countRecords(kvDB(kv)), "the cancelled prune deleted records")
	require.Empty(t, logs.String(), "a cancelled prune is not logged")

	require.NoError(t, bk.Rebaseline(context.Background()))
	require.Zero(t, countRecords(kvDB(kv)), "the next re-baseline did not prune the records")
	dst, _ := restoreInto(t, bucket)
	requireAbsent(t, dst, deleted...)
	requireSameKeys(t, kv, dst)
}

// scenario: large-keys-prune — the records of 2,000 deleted keys of about 3 KB, more than one transaction holds,
// are applied by a restore and pruned by a re-baseline, in chunks.
func TestScenarioLargeKeysPrune(t *testing.T) {
	ctx := context.Background()
	bucket := newFakeBucket()
	var logs bytes.Buffer
	cfg := BackupConfig{Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	kv, seams := openBackupKVWith(t, t.TempDir(), bucket, cfg)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	pad := strings.Repeat("x", 3000)
	big := seq("big/", 2000)
	for i := range big {
		big[i] += pad
	}
	putKeys(t, kv, seq("k/", 20)...)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(dropConcurrency)
	for _, k := range big {
		g.Go(func() error { return kv.Put(gctx, k, []byte("v")) })
	}
	require.NoError(t, g.Wait())
	ship(t, seams)

	require.NoError(t, kv.(*driver).DropPrefix("big/"))
	require.Equal(t, len(big), countRecords(kvDB(kv)))
	churnUntilCompacted(t, kvDB(kv), big...)
	ship(t, seams)

	dst, _ := restoreInto(t, bucket)
	require.Equal(t, len(big), versionsLeft(kvDB(dst), big), "the restore's record pass deleted the loaded keys")
	requireAbsent(t, dst, big...)
	requireSameKeys(t, kv, dst)

	chunks, total := 0, 0
	whole := func(_ *badger.Txn, rec *badger.Item) ([]byte, error) { return rec.KeyCopy(nil), nil }
	require.NoError(t, seams.Backup.(*backup).forDelRecordChunks(ctx, whole, func(keys [][]byte) error {
		size := 0
		for _, k := range keys {
			size += len(k) + delEntryBytes
		}
		require.LessOrEqual(t, size, delChunkBytes)
		chunks, total = chunks+1, total+len(keys)
		return nil
	}))
	require.Greater(t, chunks, 1, "the records fit one chunk")
	require.Equal(t, len(big), total)

	require.NoError(t, seams.Backup.(*backup).Rebaseline(ctx))
	require.Zero(t, countRecords(kvDB(kv)), "the prune left records")
	require.Empty(t, logs.String(), "the prune failed")
	again, _ := restoreInto(t, bucket)
	requireAbsent(t, again, big...)
	requireSameKeys(t, kv, again)
}

// scenario: missing-key-delete-writes-nothing — deleting a key that does not exist writes no record.
func TestScenarioMissingKeyDeleteWritesNothing(t *testing.T) {
	kv, _ := openBackupKV(t, t.TempDir(), newFakeBucket())
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	require.NoError(t, kv.Delete(context.Background(), "absent"))
	require.Zero(t, countRecords(kvDB(kv)))
}

// Without the backup seam a delete writes no record (backup off costs nothing, ADR-0066).
func TestNoBackupSeamWritesNoRecord(t *testing.T) {
	kv, _ := openBackupKV(t, t.TempDir(), nil)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	putKeys(t, kv, "k")
	require.NoError(t, kv.Delete(context.Background(), "k"))
	require.Zero(t, countRecords(kvDB(kv)))
}

// scenario: first-start-after-upgrade-rebaselines — a v0.7.3 chain (a base with at, no format) misses the
// delete of a key whose marker compaction dropped; the first start re-baselines at once and records format 1,
// and the next start waits for the period.
func TestScenarioFirstStartAfterUpgradeRebaselines(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bucket := newFakeBucket()
	db, err := openDB(dir, false)
	require.NoError(t, err)
	old := startDriver(db, newConfig([]Option{WithValueLogGCInterval(0)})) // a v0.7.3 gateway: deletes write no record
	b, err := NewBackup(db, bucket, BackupConfig{})
	require.NoError(t, err)
	putKeys(t, old, append(seq("k/", 20), "victim")...)
	require.NoError(t, b.(*backup).Rebaseline(ctx))
	man := currentManifest(t, bucket)
	man.Format = 0
	require.NoError(t, b.(*backup).saveManifest(ctx, man))
	require.NotContains(t, string(bucket.objs[manifestKey]), "format")
	require.NoError(t, old.Delete(ctx, "victim"))
	churnUntilCompacted(t, db, "victim")
	_, err = b.Ship(ctx)
	require.NoError(t, err)
	require.NoError(t, old.Close())

	cfg := BackupConfig{Interval: 50 * time.Millisecond, Rebaseline: time.Hour}
	kv, seams := openBackupKVWith(t, dir, bucket, cfg)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runUntil := func(done func(manifest) bool, msg string) {
		runCtx, cancel := context.WithCancel(ctx)
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			RunBackup(runCtx, seams.Backup, logger)
		}()
		defer func() {
			cancel()
			<-stopped
		}()
		require.Eventually(t, func() bool { return done(currentManifest(t, bucket)) }, 10*time.Second, 20*time.Millisecond, msg)
	}
	runUntil(func(m manifest) bool { return m.Format == manifestFormat }, "the first start did not re-baseline")
	rebased := currentManifest(t, bucket).Base.Prefix
	dst, _ := restoreInto(t, bucket)
	requireAbsent(t, dst, "victim")
	requireSameKeys(t, kv, dst)

	putKeys(t, kv, "after")
	runUntil(func(m manifest) bool { return len(m.Incs) > 0 }, "the next start did not ship an incremental")
	require.Equal(t, rebased, currentManifest(t, bucket).Base.Prefix, "the next start re-baselined before the period")
}

// scenario: restore-then-ship-then-restore — a chain with records restored into B, then a delete in B and a
// Ship, restores into C without either deleted key, after compaction dropped B's delete markers.
func TestScenarioRestoreThenShipThenRestore(t *testing.T) {
	ctx := context.Background()
	bucket := newFakeBucket()
	a, aseams := openBackupKV(t, t.TempDir(), bucket)
	t.Cleanup(func() { _ = a.(io.Closer).Close() })
	putKeys(t, a, append(seq("k/", 20), "v1", "v2")...)
	ship(t, aseams)
	require.NoError(t, a.Delete(ctx, "v1"))
	churnUntilCompacted(t, kvDB(a), "v1")
	ship(t, aseams)
	require.Equal(t, 1, countRecords(kvDB(a)))

	b, bseams := restoreInto(t, bucket)
	requireAbsent(t, b, "v1")
	require.Equal(t, 1, countRecords(kvDB(b)), "the restore dropped the record it applied")
	require.NoError(t, b.Delete(ctx, "v2"))
	churnUntilCompacted(t, kvDB(b), "v1", "v2")
	ship(t, bseams)

	c, _ := restoreInto(t, bucket)
	requireAbsent(t, c, "v1", "v2")
	requireSameKeys(t, b, c)
}

// A failed re-baseline is retried after rebaselineRetry, far below the period; after a success the next one
// waits a full period (ADR-0195 Decision 6).
func TestFailedRebaselineRetriesAfterRebaselineRetry(t *testing.T) {
	db := openRawDB(t, t.TempDir())
	bucket := newFakeBucket()
	var attempts atomic.Int32
	bucket.failPut = func(key string) error {
		if key == manifestKey && attempts.Add(1) == 1 {
			return fault.Unavailablef("fakeBucket", "object storage down")
		}
		return nil
	}
	writeKeys(t, db, "k/", 10, 16)
	b, err := NewBackup(db, bucket, BackupConfig{Interval: time.Hour, Rebaseline: time.Hour, RebaselineRetry: 100 * time.Millisecond})
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		RunBackup(runCtx, b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	require.Eventually(t, func() bool { return attempts.Load() >= 2 }, 5*time.Second, 10*time.Millisecond,
		"a failed re-baseline was not retried within rebaselineRetry")
	require.NotNil(t, currentManifest(t, bucket).Base, "the retry did not write a base")
	require.Never(t, func() bool { return attempts.Load() > 2 }, 500*time.Millisecond, 10*time.Millisecond,
		"a successful re-baseline was followed by another before the period")
}

// The contract suite holds with the backup seam wired, and List never returns a delete record.
func TestBackupSeamKVContract(t *testing.T) {
	kv, _ := openBackupKV(t, t.TempDir(), newFakeBucket())
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	kvstorecontract.Run(t, kv)
	require.Equal(t, 1, countRecords(kvDB(kv)), "the contract's delete wrote its record")
	keys, err := kv.List(context.Background(), "")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a/k2", "b/k1"}, keys)
}

// The record of the longest key a store may hold (ADR-0148: maxKeyBytes plus a 192-byte prefix) fits Badger's
// key limit, and a key whose record would not fails its delete alone.
func TestDelRecordKeyFitsBadgerLimit(t *testing.T) {
	const badgerMaxKey = 65000 // txn.go maxKeySize, Badger v4.9.2
	longest := v1alpha1.MaxKeyBytesLimit + 192
	require.LessOrEqual(t, longest+len(delRecordPrefix), badgerMaxKey)

	ctx := context.Background()
	kv, _ := openBackupKV(t, t.TempDir(), newFakeBucket())
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	key := strings.Repeat("k", longest)
	putKeys(t, kv, key)
	require.NoError(t, kv.Delete(ctx, key))
	require.NotZero(t, recordVersion(t, kvDB(kv), key))

	over := strings.Repeat("o", badgerMaxKey-1)
	putKeys(t, kv, over, "other")
	batch := []*writeReq{
		{key: over, del: true, done: make(chan error, 1)},
		{key: "other", del: true, done: make(chan error, 1)},
	}
	kv.(*driver).commit(batch)
	require.Error(t, <-batch[0].done, "a delete whose record passes the key limit succeeded")
	require.NoError(t, <-batch[1].done, "a co-batched delete failed")
	err := kv.Delete(ctx, over)
	require.Equal(t, fault.Internal, fault.KindOf(err))
	_, found, err := kv.Get(ctx, over)
	require.NoError(t, err)
	require.True(t, found, "the failed delete removed the key without its record")
}
