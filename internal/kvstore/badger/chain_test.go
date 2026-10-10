package badger

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/age"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// restoreDir runs RestoreDir of point (n, m) of an unsealed chain into a fresh directory and opens it.
func restoreDir(t *testing.T, bucket blob.Bucket, n, m uint64) *badger.DB {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, RestoreDir(context.Background(), dir, bucket, Point{Chain: n, Segment: m}, unsealed))
	return openRawDB(t, dir)
}

func hasKey(t *testing.T, db *badger.DB, key string) bool {
	t.Helper()
	return countKeys(db, key) == 1
}

// rewriteManifest edits the manifest under dir in place, as another funcd's would read.
func rewriteManifest(t *testing.T, bucket *fakeBucket, dir string, edit func(m *ChainManifest)) {
	t.Helper()
	m := readManifest(t, bucket, dir)
	edit(&m)
	data, err := yaml.Marshal(m)
	require.NoError(t, err)
	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	bucket.objs[dir+manifestName] = fakeObj{data: data, mod: bucket.objs[dir+manifestName].mod}
}

// scenario: put-and-list-suffice — on ADR-0203's box policy (no read, no delete), a re-baseline, three Ships, a
// restart and a re-baseline all succeed; no key is put twice, and after the restart the re-baseline waits its period.
func TestScenarioPutAndListSuffice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := s3stub.New(t, nil)
	s.Set(func(s *s3stub.Stub) { s.Box = true })
	bucket, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{CredentialsFile: s3stub.CredentialsFile(t, "AKIDBOX")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = bucket.Close() })
	dir := t.TempDir()
	cfg := BackupConfig{Rebaseline: time.Hour, ChunkBytes: 1 << 10}
	kv, seams := openBackupKVWith(t, dir, bucket, cfg)
	putKeys(t, kv, seq("k/", 20)...)
	require.NoError(t, seams.Backup.(*exporter).Rebaseline(ctx))
	for i := range 3 {
		putKeys(t, kv, fmt.Sprintf("s/%d", i))
		ship(t, seams)
	}
	require.NoError(t, kv.(io.Closer).Close())

	kv, seams = openBackupKVWith(t, dir, bucket, cfg)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	bk := seams.Backup.(*exporter)
	require.Greater(t, bk.untilRebaseline(ctx, time.Hour), 50*time.Minute, "the restart re-baselines before its period")
	putKeys(t, kv, "after")
	ship(t, seams)
	require.NoError(t, bk.Rebaseline(ctx))

	require.Len(t, s.Keys(incPrefix+"0000000001/"), 4*2, "segments 1 to 4 of chain 1, each with one part")
	require.NotEmpty(t, s.Keys(KVPrefix+"base/"))
	requests, _ := s.Seen()
	for _, r := range requests {
		require.True(t, strings.HasPrefix(r, "PUT ") || r == "GET ", "the writer sent %q", r)
	}
	puts := s.PutKeys(KVPrefix)
	require.Len(t, puts, len(slices.Compact(slices.Sorted(slices.Values(puts)))), "a key was put twice")
}

// The writer never reads, overwrites or deletes on the target: no Get, Attributes, Exists or Delete over Ship,
// Rebaseline and RunBackup, and no key put twice.
func TestWriterReadsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bucket := newFakeBucket()
	kv, seams := openBackupKVWith(t, t.TempDir(), bucket, BackupConfig{Interval: 10 * time.Millisecond, Rebaseline: time.Hour})
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	putKeys(t, kv, "a")
	ship(t, seams)
	putKeys(t, kv, "b")
	ship(t, seams)
	require.NoError(t, seams.Backup.(*exporter).Rebaseline(ctx))
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunBackup(runCtx, seams.Backup, quiet())
	}()
	putKeys(t, kv, "c")
	require.Eventually(t, func() bool { return len(bucket.keys(incPrefix+"0000000002/")) > 0 }, 10*time.Second, 5*time.Millisecond)
	cancel()
	<-done
	bucket.requireBoxOnly(t)
}

// The cursor record (Decision 3): absent and 8 bytes long each make the next Ship write a base; 24 bytes naming a
// chain the listing holds ship its next segment; 24 bytes naming a chain the listing lacks write a base.
func TestCursorRecordForms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, tc := range map[string]struct {
		record []byte
		base   bool
	}{
		"absent":        {base: true},
		"8 bytes":       {record: binary.BigEndian.AppendUint64(nil, 5), base: true},
		"24 bytes held": {record: (chainState{cursor: 1, chain: 1, next: 1}).bytes()},
		"24 bytes gone": {record: (chainState{cursor: 1, chain: 9, next: 4}).bytes(), base: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := openRawDB(t, t.TempDir())
			bucket := newFakeBucket()
			writeKeys(t, db, "k/", 3, 8)
			require.NoError(t, newExporter(t, db, bucket, BackupConfig{}).Rebaseline(ctx))
			if tc.record != nil {
				require.NoError(t, setCursorRecord(db, tc.record))
			} else {
				require.NoError(t, dropBackupCursor(db))
			}
			writeKeys(t, db, "more/", 3, 8)
			_, err := newExporter(t, db, bucket, BackupConfig{}).Ship(ctx)
			require.NoError(t, err)
			if tc.base {
				require.Equal(t, []uint64{1, 2}, bucket.chains(t), "the Ship wrote no base above every chain held")
				require.Empty(t, bucket.keys(incPrefix))
				st, full := state(t, db)
				require.True(t, full)
				require.Equal(t, uint64(2), st.chain)
				return
			}
			require.Equal(t, []uint64{1}, bucket.chains(t))
			require.NotEmpty(t, bucket.keys(incDir(1, 1)))
		})
	}
}

// bytes is the 24-byte record of st.
func (st chainState) bytes() []byte {
	b := binary.BigEndian.AppendUint64(nil, st.cursor)
	b = binary.BigEndian.AppendUint64(b, st.chain)
	return binary.BigEndian.AppendUint64(b, st.next)
}

// scenario: upgrade-starts-new-chain — a v0.7.3 target (manifest.json, base/, inc/) and an 8-byte cursor: the start
// writes chain 1's base under kv/ at once, leaves every legacy object as it was, and the next Ship writes segment 1.
func TestScenarioUpgradeStartsNewChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bucket := newFakeBucket()
	legacy := map[string][]byte{
		"manifest.json":                       []byte(`{"format":1,"base":{"prefix":"base/x","since":0,"to":5,"parts":1}}`),
		"base/x/part-00000":                   []byte("old base"),
		"inc/00000000000000000005/part-00000": []byte("old inc"),
	}
	for k, v := range legacy {
		bucket.objs[k] = fakeObj{data: v}
	}
	dir := t.TempDir()
	db, err := openDB(dir, false)
	require.NoError(t, err)
	writeKeys(t, db, "k/", 5, 8)
	require.NoError(t, setCursorRecord(db, binary.BigEndian.AppendUint64(nil, 5)))
	require.NoError(t, db.Close())

	kv, seams := openBackupKVWith(t, dir, bucket, BackupConfig{Interval: 20 * time.Millisecond, Rebaseline: time.Hour})
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunBackup(runCtx, seams.Backup, quiet())
	}()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return len(bucket.chains(t)) == 1 }, 10*time.Second, 5*time.Millisecond,
		"the start wrote no base under kv/")
	require.Equal(t, []uint64{1}, bucket.chains(t))
	putKeys(t, kv, "after")
	require.Eventually(t, func() bool { return slices.Contains(bucket.keys(incDir(1, 1)), incDir(1, 1)+manifestName) },
		10*time.Second, 5*time.Millisecond, "the next Ship wrote no segment 1")
	for k, v := range legacy {
		require.Equal(t, v, bucket.objs[k].data, "%s changed", k)
	}
}

// scenario: failed-ship-skips-number — chain n at segment 4; segment 5's upload fails after one part: the cursor
// stays, the next Ship writes segment 6 with the same since, and point (n, 6) restores.
func TestScenarioFailedShipSkipsNumber(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openRawDB(t, t.TempDir())
	bucket := newFakeBucket()
	bk := newExporter(t, db, bucket, BackupConfig{ChunkBytes: 1 << 10})
	writeKeys(t, db, "base/", 5, 8)
	require.NoError(t, bk.Rebaseline(ctx))
	for i := range 4 {
		writeKeys(t, db, fmt.Sprintf("s%d/", i+1), 5, 8)
		_, err := bk.Ship(ctx)
		require.NoError(t, err)
	}
	cursor, err := bk.cursor(ctx)
	require.NoError(t, err)
	writeKeys(t, db, "s5/", 50, 64)
	bucket.mu.Lock()
	bucket.failPut = func(key string) error {
		if key == incDir(1, 5)+"part-00001" {
			return fault.Unavailablef("fakeBucket", "object storage down")
		}
		return nil
	}
	bucket.mu.Unlock()
	_, err = bk.Ship(ctx)
	require.Error(t, err)
	require.Equal(t, []string{incDir(1, 5) + "part-00000"}, bucket.keys(incDir(1, 5)), "the failed segment put one part")
	st, full := state(t, db)
	require.True(t, full)
	require.Equal(t, chainState{cursor: cursor, chain: 1, next: 6}, st, "the cursor stays; the number is spent")

	bucket.mu.Lock()
	bucket.failPut = nil
	bucket.mu.Unlock()
	_, err = bk.Ship(ctx)
	require.NoError(t, err)
	require.Equal(t, cursor, readManifest(t, bucket, incDir(1, 6)).Since, "segment 6 starts where 4 ended")
	restored := restoreDir(t, bucket, 1, 6)
	require.Equal(t, 50, countKeys(restored, "s5/"))
	require.Equal(t, 5, countKeys(restored, "s4/"))
	ps, err := ListPoints(ctx, bucket)
	require.NoError(t, err)
	for _, p := range ps {
		require.NotEqual(t, uint64(5), p.Segment, "the incomplete segment 5 is listed")
	}
}

// scenario: expired-base-numbers-above — weekly base 6, and chain 7 whose base expired while kv/inc/7/ is kept: the
// restart writes chain 8's base, above every number held, and puts nothing under kv/inc/7/.
func TestScenarioExpiredBaseNumbersAbove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bucket := newFakeBucket()
	for _, k := range []string{baseDir(backup.Weekly, 6) + "part-00000", baseDir(backup.Weekly, 6) + manifestName,
		incDir(7, 1) + "part-00000", incDir(7, 1) + manifestName} {
		bucket.objs[k] = fakeObj{data: []byte("x"), mod: time.Now()}
	}
	db := openRawDB(t, t.TempDir())
	writeKeys(t, db, "k/", 5, 8)
	require.NoError(t, setCursorRecord(db, (chainState{cursor: 3, chain: 7, next: 2}).bytes()))
	_, err := newExporter(t, db, bucket, BackupConfig{}).Ship(ctx)
	require.NoError(t, err)
	require.Equal(t, []uint64{6, 8}, bucket.chains(t))
	for _, k := range bucket.puts {
		require.False(t, strings.HasPrefix(k, incPrefix+"0000000007/"), "put %s", k)
	}
}

// scenario: newer-chain-refused — a walk with a manifest of format 2, or one by a newer funcd: ListPoints lists
// the point not restorable, or with that funcd as its stamp; RestoreDir names both formats and writes nothing.
func TestScenarioNewerChainRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openRawDB(t, t.TempDir())
	bucket := newFakeBucket()
	bk := newExporter(t, db, bucket, BackupConfig{})
	writeKeys(t, db, "k/", 5, 8)
	require.NoError(t, bk.Rebaseline(ctx))
	for i := range 2 {
		writeKeys(t, db, fmt.Sprintf("s%d/", i), 5, 8)
		_, err := bk.Ship(ctx)
		require.NoError(t, err)
	}
	rewriteManifest(t, bucket, incDir(1, 1), func(m *ChainManifest) { m.Funcd = "v0.99.0" })
	rewriteManifest(t, bucket, incDir(1, 2), func(m *ChainManifest) { m.Format = 2 })
	ps, err := ListPoints(ctx, bucket)
	require.NoError(t, err)
	require.Len(t, ps, 3)
	require.True(t, ps[1].Restorable)
	require.Equal(t, "v0.99.0", ps[1].Funcd, "the walk's highest stamp")
	require.False(t, ps[2].Restorable, "a walk with format 2")

	dir := t.TempDir()
	err = RestoreDir(ctx, dir, bucket, Point{Chain: 1, Segment: 2}, unsealed)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "format 2")
	require.ErrorContains(t, err, "format 1")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// scenario: ladder-classes — defaults, rebaseline 12h and a manual clock: bases written on a Monday, a Tuesday and
// twice on a Wednesday sit under weekly, daily, daily and hourly.
func TestScenarioLadderClasses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	monday := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	c := clock.NewManual(monday)
	bucket := newFakeBucket()
	bucket.clock = c
	db := openRawDB(t, t.TempDir())
	bk := newExporter(t, db, bucket, BackupConfig{Rebaseline: 12 * time.Hour,
		Retention: backup.Retention{Hourly: 48, Daily: 30, Weekly: 12}})
	bk.clock = c
	for _, step := range []time.Duration{0, 24 * time.Hour, 22 * time.Hour, 12 * time.Hour} {
		c.Advance(step)
		require.NoError(t, bk.Rebaseline(ctx))
	}
	l, err := listBases(ctx, bucket)
	require.NoError(t, err)
	slices.SortFunc(l.entries, func(a, b backup.Entry) int { return int(a.Generation) - int(b.Generation) })
	var classes []backup.Class
	for _, e := range l.entries {
		classes = append(classes, e.Class)
	}
	require.Equal(t, []backup.Class{backup.Weekly, backup.Daily, backup.Daily, backup.Hourly}, classes)
}

// scenario: restored-instance-starts-new-chain — chain n restored into B: B deletes k and ships, writing a new base n'
// and no segment of n; n' restored lacks k and every key n's records deleted.
func TestScenarioRestoredInstanceStartsNewChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bucket := newFakeBucket()
	a, aseams := openBackupKV(t, t.TempDir(), bucket)
	t.Cleanup(func() { _ = a.(io.Closer).Close() })
	putKeys(t, a, "k", "r", "x")
	ship(t, aseams)
	require.NoError(t, a.Delete(ctx, "r"))
	ship(t, aseams)

	dirB := t.TempDir()
	require.NoError(t, RestoreDir(ctx, dirB, bucket, Point{Chain: 1, Segment: 1}, unsealed))
	b, bseams := openBackupKV(t, dirB, bucket)
	t.Cleanup(func() { _ = b.(io.Closer).Close() })
	before := len(bucket.puts)
	require.NoError(t, b.Delete(ctx, "k"))
	ship(t, bseams)
	require.Equal(t, []uint64{1, 2}, bucket.chains(t))
	for _, k := range bucket.puts[before:] {
		require.False(t, strings.HasPrefix(k, incPrefix+"0000000001/"), "B put %s", k)
	}
	c := restoreDir(t, bucket, 2, 0)
	require.False(t, hasKey(t, c, "k"))
	require.False(t, hasKey(t, c, "r"))
	require.True(t, hasKey(t, c, "x"))
}

// ageSeal seals to r, the opener test's envelope.
func ageSeal(r age.Recipient) backup.Seal {
	return func(dst io.Writer) (io.WriteCloser, error) { return age.Encrypt(dst, r) }
}

// The walk (Decision 4): a gap of an incomplete segment is skipped, a re-shipped range loads, each manifest is opened
// with its own recipients, a since above the head breaks the chain, and a higher format refuses.
func TestChainWalk(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	db := openRawDB(t, t.TempDir())
	bucket := newFakeBucket()
	sealed := newExporter(t, db, bucket, BackupConfig{Seal: ageSeal(id.Recipient()), Recipients: []string{"fp-a"}})
	writeKeys(t, db, "base/", 5, 8)
	require.NoError(t, sealed.Rebaseline(ctx))
	writeKeys(t, db, "s1/", 5, 8)
	_, err = sealed.Ship(ctx)
	require.NoError(t, err)
	plain := newExporter(t, db, bucket, BackupConfig{ChunkBytes: 1 << 10})
	writeKeys(t, db, "s2/", 50, 64)
	bucket.mu.Lock()
	bucket.failPut = func(key string) error {
		if key == incDir(1, 2)+"part-00001" {
			return fault.Unavailablef("fakeBucket", "object storage down")
		}
		return nil
	}
	bucket.mu.Unlock()
	_, err = plain.Ship(ctx)
	require.Error(t, err, "segment 2 never completes")
	bucket.mu.Lock()
	bucket.failPut = nil
	bucket.mu.Unlock()
	writeKeys(t, db, "s3/", 5, 8)
	_, err = plain.Ship(ctx)
	require.NoError(t, err)
	rewriteManifest(t, bucket, incDir(1, 3), func(m *ChainManifest) { m.Since-- }) // a re-shipped range

	var calls [][]string
	var mu sync.Mutex
	open := func(rs []string) (backup.Unseal, error) {
		mu.Lock()
		calls = append(calls, rs)
		mu.Unlock()
		if len(rs) == 0 {
			return nil, nil
		}
		return func(src io.Reader) (io.Reader, error) { return age.Decrypt(src, id) }, nil
	}
	dir := t.TempDir()
	require.NoError(t, RestoreDir(ctx, dir, bucket, Point{Chain: 1, Segment: 3}, open))
	require.Equal(t, [][]string{{"fp-a"}, {"fp-a"}, nil}, calls, "one open per manifest, with its recipients")
	got := openRawDB(t, dir)
	for p, n := range map[string]int{"base/": 5, "s1/": 5, "s2/": 50, "s3/": 5} {
		require.Equal(t, n, countKeys(got, p), p)
	}

	writeKeys(t, db, "s4/", 5, 8)
	_, err = plain.Ship(ctx)
	require.NoError(t, err)
	rewriteManifest(t, bucket, incDir(1, 4), func(m *ChainManifest) { m.Since += 1000 })
	ps, err := ListPoints(ctx, bucket)
	require.NoError(t, err)
	restorable := map[uint64]bool{}
	for _, p := range ps {
		restorable[p.Segment] = p.Restorable
	}
	require.Equal(t, map[uint64]bool{0: true, 1: true, 3: true, 4: false}, restorable)
	err = RestoreDir(ctx, t.TempDir(), bucket, Point{Chain: 1, Segment: 4}, open)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "a since above the head breaks the chain")

	rewriteManifest(t, bucket, incDir(1, 1), func(m *ChainManifest) { m.Format = ChainFormat + 1 })
	err = RestoreDir(ctx, t.TempDir(), bucket, Point{Chain: 1, Segment: 3}, open)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// RestoreDir refuses before any write: a non-empty directory is fault.Conflict, a missing part or base
// fault.NotFound; a stored file that misses its SHA-256 is fault.Invalid and empties the directory.
func TestRestoreDirRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openRawDB(t, t.TempDir())
	bucket := newFakeBucket()
	bk := newExporter(t, db, bucket, BackupConfig{})
	writeKeys(t, db, "k/", 5, 8)
	require.NoError(t, bk.Rebaseline(ctx))
	base := baseDirOf(t, bucket, 1)

	full := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(full, "x"), nil, 0o600))
	require.Equal(t, fault.Conflict, fault.KindOf(RestoreDir(ctx, full, bucket, Point{Chain: 1}, unsealed)))
	require.Equal(t, fault.NotFound, fault.KindOf(RestoreDir(ctx, t.TempDir(), bucket, Point{Chain: 2}, unsealed)))

	bucket.mu.Lock()
	o := bucket.objs[base+"part-00000"]
	bucket.objs[base+"part-00000"] = fakeObj{data: append(bytes.Clone(o.data), 0), mod: o.mod}
	bucket.mu.Unlock()
	dir := t.TempDir()
	require.Equal(t, fault.Invalid, fault.KindOf(RestoreDir(ctx, dir, bucket, Point{Chain: 1}, unsealed)))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "the failed restore left files")

	bucket.mu.Lock()
	delete(bucket.objs, base+"part-00000")
	bucket.mu.Unlock()
	require.Equal(t, fault.NotFound, fault.KindOf(RestoreDir(ctx, t.TempDir(), bucket, Point{Chain: 1}, unsealed)))
}

// The operator's lifecycle rules per kv/ prefix (Decision 5).
func TestKVLifecycleRules(t *testing.T) {
	t.Parallel()
	require.Equal(t, map[string]int{"kv/inc/": 2, "kv/base/hourly/": 2, "kv/base/daily/": 30, "kv/base/weekly/": 84},
		LifecycleRules(backup.Retention{Hourly: 48, Daily: 30, Weekly: 12}))
	require.Equal(t, map[string]int{"kv/inc/": 3, "kv/base/hourly/": 3},
		LifecycleRules(backup.Retention{Hourly: 49}))
}

type gate struct{ held atomic.Bool }

func (g *gate) Held() bool { return g.held.Load() }

// While held, a due Ship or re-baseline writes nothing and calls no Record (ADR-0206 Decision 6); released, the
// re-baseline retries after RebaselineRetry and records its outcome.
func TestRunBackupSkipsWhileHeld(t *testing.T) {
	t.Parallel()
	bucket := newFakeBucket()
	g := &gate{}
	g.held.Store(true)
	var records atomic.Int32
	db := openRawDB(t, t.TempDir())
	writeKeys(t, db, "k/", 5, 8)
	b := newExporter(t, db, bucket, BackupConfig{Interval: 10 * time.Millisecond, Rebaseline: time.Hour,
		RebaselineRetry: 30 * time.Millisecond, Hold: g, Record: func(time.Time, error) { records.Add(1) }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunBackup(ctx, b, quiet())
	}()
	t.Cleanup(func() { cancel(); <-done })
	require.Never(t, func() bool { return len(bucket.keys(KVPrefix)) > 0 || records.Load() > 0 },
		200*time.Millisecond, 10*time.Millisecond, "a held run wrote or recorded")
	g.held.Store(false)
	require.Eventually(t, func() bool { return len(bucket.chains(t)) == 1 && records.Load() > 0 }, 10*time.Second, 5*time.Millisecond)
}

// A Ready refusal writes nothing and goes to Record with its cause; the KV serves on.
func TestReadyRefusalWritesNothing(t *testing.T) {
	t.Parallel()
	bucket := newFakeBucket()
	var got atomic.Value
	refusal := fault.Invalidf("backup.Ready", "kvstore.backup.singleWriter: the target ignores a create-if-absent put")
	kv, seams := openBackupKVWith(t, t.TempDir(), bucket, BackupConfig{Interval: 10 * time.Millisecond,
		Ready:  func(context.Context) (bool, error) { return false, refusal },
		Record: func(_ time.Time, err error) { got.Store(err) }})
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunBackup(ctx, seams.Backup, quiet())
	}()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { e, _ := got.Load().(error); return e != nil }, 10*time.Second, 5*time.Millisecond)
	require.ErrorContains(t, got.Load().(error), "kvstore.backup.singleWriter")
	putKeys(t, kv, "a")
	v, found, err := kv.Get(context.Background(), "a")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("v-a"), v)
	require.Empty(t, bucket.keys(""))
}

// scenario: every-timestamp-utc-millisecond (ADR-0196) — a chain manifest's at is written in the ADR-0196 form: a
// base stamped at …35.965999999 reads …35.965Z.
func TestEveryTimestampUTCMillisecond(t *testing.T) {
	t.Parallel()
	c := clock.NewManual(time.Date(2026, 10, 7, 22, 3, 35, 965999999, time.FixedZone("x", 2*3600)))
	bucket := newFakeBucket()
	db := openRawDB(t, t.TempDir())
	writeKeys(t, db, "k/", 3, 8)
	bk := newExporter(t, db, bucket, BackupConfig{})
	bk.clock = c
	require.NoError(t, bk.Rebaseline(context.Background()))
	raw, err := bucket.Get(context.Background(), baseDirOf(t, bucket, 1)+manifestName)
	require.NoError(t, err)
	require.Contains(t, string(raw), "at: \"2026-10-07T20:03:35.965Z\"")
}
