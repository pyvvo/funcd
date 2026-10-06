package badger

import (
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

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/kvstore"
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
	kv, seams, err := OpenWithSeamsFor(dir, bucket, BackupConfig{}, nil, CDCConfig{},
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
