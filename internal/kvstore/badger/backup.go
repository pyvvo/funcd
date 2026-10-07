package badger

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
)

// backup implements the ADR-0066 Backup seam (ADR-0067): version-watermarked incremental export of the KV
// Badger instance to a blob target, plus a periodic full re-baseline and a restore. It owns the export
// loop; correctness lives in the watermarked cursor, never in Badger's lossy Subscribe.
type backup struct {
	db         *badger.DB
	bucket     blob.Bucket
	chunkBytes int           // max bytes buffered per blob object (bounds re-baseline RSS)
	interval   time.Duration // incremental cadence
	fullEvery  time.Duration // full re-baseline cadence
	retry      time.Duration // the delay after a failed re-baseline (ADR-0195 Decision 6)
	logger     *slog.Logger
	loop       sync.Mutex // serializes Ship/Rebaseline — one exporter at a time, never overlapping
}

const (
	defaultChunkBytes = 64 << 20 // 64 MiB
	backupCursorKey   = Reserved + "backup/cursor"
	manifestKey       = "manifest.json"
	// exportNumGo caps the export's producers: each holds its own batch buffers, so Badger's default of 8
	// multiplies the export's peak heap (ADR-0067 Decision 2: a low Stream.NumGo caps the export RSS).
	exportNumGo = 1

	delRecordPrefix = Reserved + "backup/del/" // a record per key deleted through the gateway with backup on
	manifestFormat  = 1                        // the chain carries a record for every gateway delete
	delChunkBytes   = 1 << 20                  // the budget of one restore or prune transaction
	delEntryBytes   = 128                      // added to the length of each key a transaction deletes
	pruneRetries    = 3                        // retries of one prune chunk after badger.ErrConflict

	defaultRebaselineRetry = time.Hour
)

// BackupConfig configures the opt-in DR backup (ADR-0067, ADR-0195). Zero ChunkBytes ⇒ 64 MiB; zero
// RebaselineRetry ⇒ 1h; a nil Logger ⇒ slog.Default().
type BackupConfig struct {
	Interval        time.Duration
	Rebaseline      time.Duration
	RebaselineRetry time.Duration
	ChunkBytes      int
	Logger          *slog.Logger
}

// delRecordKey is the key of the delete record of key (ADR-0195 Decision 1).
func delRecordKey(key string) []byte { return []byte(delRecordPrefix + key) }

// NewBackup builds the DR backup over an opened KV Badger instance. A nil bucket means DR was enabled
// without a target — fault.Invalid (never a silent half-configured backup).
func NewBackup(db *badger.DB, bucket blob.Bucket, cfg BackupConfig) (Backup, error) {
	if bucket == nil {
		return nil, fault.Invalidf("kvbadger.NewBackup", "backup enabled but target is empty")
	}
	cb := cfg.ChunkBytes
	if cb <= 0 {
		cb = defaultChunkBytes
	}
	retry := cfg.RebaselineRetry
	if retry <= 0 {
		retry = defaultRebaselineRetry
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &backup{
		db: db, bucket: bucket, chunkBytes: cb, interval: cfg.Interval, fullEvery: cfg.Rebaseline,
		retry: retry, logger: logger,
	}, nil
}

// segment is one exported range of blob parts (a base or an incremental). Parts are part-00000…part-NNNNN.
// At is when Rebaseline started a base's export; it is zero on an incremental and in a manifest written
// before the field existed, which reads as an unknown re-baseline time.
type segment struct {
	Prefix string       `json:"prefix"`
	Since  uint64       `json:"since"`
	To     uint64       `json:"to"`
	Parts  int          `json:"parts"`
	At     manifestTime `json:"at,omitzero"`
}

// manifestTime is a manifest instant (ADR-0196 Decision 10): written in the v1alpha1.Timestamp form, read from any
// RFC3339 value, so a manifest written before that form (RFC3339Nano) still loads.
type manifestTime v1.Timestamp

func (t manifestTime) IsZero() bool { return v1.Timestamp(t).IsZero() }

func (t manifestTime) MarshalJSON() ([]byte, error) { return v1.Timestamp(t).MarshalJSON() }

func (t *manifestTime) UnmarshalJSON(b []byte) error {
	var v time.Time
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*t = manifestTime(v1.NewTimestamp(v))
	return nil
}

// manifest is the ordered restore chain: the latest base then the incrementals after it. It is the
// authoritative segment list (blob has no rename), written only after every part is durably uploaded.
// Format is manifestFormat once a re-baseline has run with delete records (ADR-0195 Decision 5); an older
// binary drops it, which reads as 0 and makes the next start re-baseline.
type manifest struct {
	Format int       `json:"format,omitempty"`
	Base   *segment  `json:"base,omitempty"`
	Incs   []segment `json:"incs,omitempty"`
}

// Ship runs one incremental tick: export every change since the persisted cursor to a fresh blob segment,
// and advance the cursor ONLY after the manifest + all parts are durably uploaded. A crash/upload failure
// before that leaves the cursor unchanged, so the next run re-ships the interval (idempotent on restore).
// A store opened without the backup seam has no cursor (#808): a change made then may have left no version
// to export (a native DropPrefix, a compacted delete marker), so when a chain exists Ship re-baselines
// instead of appending to it.
func (b *backup) Ship(ctx context.Context) (uint64, error) {
	b.loop.Lock()
	defer b.loop.Unlock()
	const op = "kvbadger.backup.Ship"
	since, found, err := b.readCursor(ctx)
	if err != nil {
		return 0, err
	}
	man, err := b.loadManifest(ctx)
	if err != nil {
		return since, err
	}
	if !found && (man.Base != nil || len(man.Incs) > 0) {
		if err := b.rebaseline(ctx); err != nil {
			return since, err
		}
		return b.cursor(ctx)
	}
	prefix := fmt.Sprintf("inc/%020d", since)
	w := b.newChunkWriter(ctx, prefix)
	to, _, berr := b.export(w, since, false)
	if berr != nil {
		return since, fault.Internalf(op, "badger backup since %d: %v", since, berr)
	}
	if to <= since { // no new versions — drop any buffered framing, cursor unchanged
		w.discard()
		return since, nil
	}
	if err := w.Close(); err != nil { // an upload failure here keeps the cursor where it was
		return since, err
	}
	man.Format = min(man.Format, manifestFormat) // never claim a format this chain's base was not built with
	man.Incs = append(man.Incs, segment{Prefix: prefix, Since: since, To: to, Parts: w.parts})
	if err := b.saveManifest(ctx, man); err != nil {
		return since, err
	}
	if err := b.setCursor(ctx, to); err != nil {
		return since, err
	}
	return to, nil
}

// Rebaseline writes a fresh full base (db.Backup since 0) through the chunking writer with a low export
// concurrency, repoints the manifest at it (resetting the incremental chain), and prunes the superseded
// segments — bounding the restore chain and the on-disk backup set — then the delete records the base
// reflects (ADR-0195 Decision 2). A failed record prune is logged, not returned: the next re-baseline prunes them.
func (b *backup) Rebaseline(ctx context.Context) error {
	b.loop.Lock()
	defer b.loop.Unlock()
	return b.rebaseline(ctx)
}

// rebaseline is Rebaseline's body; the caller holds b.loop.
func (b *backup) rebaseline(ctx context.Context) error {
	const op = "kvbadger.backup.Rebaseline"
	old, err := b.loadManifest(ctx)
	if err != nil {
		return err
	}
	at, err := b.cursor(ctx)
	if err != nil {
		return err
	}
	started := time.Now().UTC()
	// the start time keeps the name unique: two re-baselines can start at one cursor (none after the store ran
	// without backup, #808), and a reused name would overwrite the live base's parts before the manifest moves
	prefix := fmt.Sprintf("base/%020d-%d", at, started.UnixNano())
	w := b.newChunkWriter(ctx, prefix)
	to, readTs, berr := b.export(w, 0, true)
	if berr != nil {
		return fault.Internalf(op, "badger full backup: %v", berr)
	}
	if err := w.Close(); err != nil {
		return err
	}
	man := manifest{Format: manifestFormat, Base: &segment{Prefix: prefix, Since: 0, To: to, Parts: w.parts, At: manifestTime(v1.NewTimestamp(started))}}
	if err := b.saveManifest(ctx, man); err != nil {
		return err
	}
	if err := b.setCursor(ctx, to); err != nil {
		return err
	}
	b.prune(ctx, old, prefix) // best-effort: drop the old base (unless reused) + old incrementals
	if err := b.pruneDelRecords(ctx, readTs); err != nil && ctx.Err() == nil {
		b.logger.Warn("kv backup delete-record prune failed; the next re-baseline prunes the rest", "err", err)
	}
	return nil
}

// Restore reconstructs the instance from the latest base then each incremental, in version order, deletes
// every key its delete records say was deleted later (ADR-0195 Decision 3), and only then sets the cursor to
// the chain's head so a ship from the restored instance continues the chain. Run into a fresh instance,
// before it takes writes.
func (b *backup) Restore(ctx context.Context) error {
	const op = "kvbadger.backup.Restore"
	man, err := b.loadManifest(ctx)
	if err != nil {
		return err
	}
	if man.Base == nil && len(man.Incs) == 0 {
		return fault.Invalidf(op, "no backup manifest to restore from")
	}
	if man.Base != nil {
		if err := b.loadSegment(ctx, *man.Base); err != nil {
			return err
		}
	}
	head := uint64(0)
	if man.Base != nil {
		head = man.Base.To
	}
	for _, s := range man.Incs {
		if err := b.loadSegment(ctx, s); err != nil {
			return err
		}
		head = s.To
	}
	if err := b.applyDelRecords(ctx); err != nil {
		return err
	}
	return b.setCursor(ctx, head)
}

// export streams every version > since to w and returns the version the cursor may advance to, and the read
// timestamp taken before the stream. It leaves out the backup's own cursor: each cursor write is a new
// version, so exporting it made every idle tick ship a segment (#790). Each Stream producer reads its own,
// later snapshot, so a version above the read timestamp can be exported while a write below it was missed
// (#806): the result is capped at that read timestamp, which every producer sees in full, and the next export
// ships the versions above it again. A base also leaves out every delete record whose newest version is at or
// below the read timestamp: the base reflects that delete (ADR-0195 Decision 2).
func (b *backup) export(w io.Writer, since uint64, base bool) (to, readTs uint64, err error) {
	snap := b.db.NewTransaction(false)
	defer snap.Discard()
	readTs = snap.ReadTs()
	recPrefix := []byte(delRecordPrefix)
	s := b.db.NewStream()
	s.LogPrefix = "kvbadger.backup"
	s.SinceTs = since
	s.NumGo = exportNumGo
	s.ChooseKey = func(item *badger.Item) bool {
		k := item.Key()
		if string(k) == backupCursorKey {
			return false
		}
		return !base || !bytes.HasPrefix(k, recPrefix) || item.Version() > readTs
	}
	to, err = s.Backup(w, since)
	if err != nil {
		return 0, 0, err
	}
	return min(to, readTs), readTs, nil
}

// applyDelRecords deletes every key whose newest version is strictly older than its delete record, one
// transaction per chunk. A key set again in the delete's transaction shares its version and stays. The records
// stay: these deletes write none, so dropping the records would let a later restore bring the keys back once
// compaction drops this instance's delete markers (ADR-0195 Decision 3).
func (b *backup) applyDelRecords(ctx context.Context) error {
	const op = "kvbadger.backup.Restore"
	stale := func(txn *badger.Txn, rec *badger.Item) ([]byte, error) {
		key := rec.Key()[len(delRecordPrefix):]
		item, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, nil
		}
		if err != nil || item.Version() >= rec.Version() {
			return nil, err
		}
		return bytes.Clone(key), nil
	}
	err := b.forDelRecordChunks(ctx, stale, func(keys [][]byte) error {
		return b.db.Update(func(txn *badger.Txn) error {
			for _, k := range keys {
				if err := txn.Delete(k); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil && ctx.Err() == nil {
		return fault.Internalf(op, "apply delete records: %v", err)
	}
	return err
}

// pruneDelRecords deletes the delete records at or below readTs, the deletes a new base reflects, one
// transaction per chunk. Each transaction re-reads its records and keeps one a later delete rewrote, and a
// chunk that conflicts with such a delete is retried up to pruneRetries times (ADR-0195 Decision 4).
func (b *backup) pruneDelRecords(ctx context.Context, readTs uint64) error {
	const op = "kvbadger.backup.pruneDelRecords"
	covered := func(_ *badger.Txn, rec *badger.Item) ([]byte, error) {
		if rec.Version() > readTs {
			return nil, nil
		}
		return rec.KeyCopy(nil), nil
	}
	err := b.forDelRecordChunks(ctx, covered, func(recs [][]byte) error {
		err := b.pruneChunk(recs, readTs)
		for try := 0; try < pruneRetries && errors.Is(err, badger.ErrConflict); try++ {
			err = b.pruneChunk(recs, readTs)
		}
		return err
	})
	if err != nil && ctx.Err() == nil {
		return fault.Internalf(op, "%v", err)
	}
	return err
}

// pruneChunk deletes, in one transaction, each of the records recs that is still at or below readTs.
func (b *backup) pruneChunk(recs [][]byte, readTs uint64) error {
	return b.db.Update(func(txn *badger.Txn) error {
		for _, k := range recs {
			item, err := txn.Get(k)
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if item.Version() > readTs {
				continue
			}
			if err := txn.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// forDelRecordChunks scans the delete records in key order and hands each chunk of the keys pick returns to
// apply, so a pass holds one chunk at most. A chunk is full before the sum of len(key) + delEntryBytes over its
// keys would pass delChunkBytes. The context is checked between chunks.
func (b *backup) forDelRecordChunks(
	ctx context.Context,
	pick func(txn *badger.Txn, rec *badger.Item) ([]byte, error),
	apply func(keys [][]byte) error,
) error {
	prefix := []byte(delRecordPrefix)
	from := prefix
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var chunk [][]byte
		size, done := 0, true
		err := b.db.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.PrefetchValues = false
			opts.Prefix = prefix
			it := txn.NewIterator(opts)
			defer it.Close()
			for it.Seek(from); it.ValidForPrefix(prefix); it.Next() {
				rec := it.Item()
				k, err := pick(txn, rec)
				if err != nil {
					return err
				}
				if k != nil {
					cost := len(k) + delEntryBytes
					if size+cost > delChunkBytes {
						done = false
						return nil
					}
					size += cost
					chunk = append(chunk, k)
				}
				from = append(rec.KeyCopy(nil), 0) // the least key after rec: where the next scan resumes
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(chunk) > 0 {
			if err := apply(chunk); err != nil {
				return err
			}
		}
		if done {
			return nil
		}
	}
}

func (b *backup) loadSegment(ctx context.Context, s segment) error {
	r := newPartReader(ctx, b.bucket, s.Prefix, s.Parts)
	if err := b.db.Load(r, 16); err != nil {
		return fault.Internalf("kvbadger.backup.Restore", "load segment %q: %v", s.Prefix, err)
	}
	return nil
}

// prune deletes the previous base (unless its name was reused) and every previous incremental from blob.
func (b *backup) prune(ctx context.Context, old manifest, keepBase string) {
	del := func(s segment) {
		for i := 0; i < s.Parts; i++ {
			_ = b.bucket.Delete(ctx, fmt.Sprintf("%s/part-%05d", s.Prefix, i))
		}
	}
	if old.Base != nil && old.Base.Prefix != keepBase {
		del(*old.Base)
	}
	for _, s := range old.Incs {
		del(s)
	}
}

// cursor reads the persisted version watermark (0 if none).
func (b *backup) cursor(ctx context.Context) (uint64, error) {
	v, _, err := b.readCursor(ctx)
	return v, err
}

// readCursor reads the persisted version watermark and whether one is set.
func (b *backup) readCursor(ctx context.Context) (uint64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	var v uint64
	found := false
	err := b.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(backupCursorKey))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return item.Value(func(val []byte) error {
			if len(val) == 8 {
				v = binary.BigEndian.Uint64(val)
			}
			return nil
		})
	})
	if err != nil {
		return 0, false, fault.Internalf("kvbadger.backup.cursor", "%v", err)
	}
	return v, found, nil
}

// dropBackupCursor deletes the backup cursor of a store opened without the backup seam, so a later backup
// does not resume a chain that missed this run's changes (#808): its next Ship re-baselines.
func dropBackupCursor(db *badger.DB) error {
	err := db.Update(func(txn *badger.Txn) error {
		_, err := txn.Get([]byte(backupCursorKey))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return txn.Delete([]byte(backupCursorKey))
	})
	if err != nil {
		return fault.Internalf("kvbadger.Open", "drop backup cursor: %v", err)
	}
	return nil
}

// setCursor persists the version watermark. Written directly (not via the gateway): a reserved key the
// user write-path never touches, so it cannot conflict with a data txn.
func (b *backup) setCursor(ctx context.Context, v uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	if err := b.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(backupCursorKey), buf[:])
	}); err != nil {
		return fault.Internalf("kvbadger.backup.setCursor", "%v", err)
	}
	return nil
}

func (b *backup) loadManifest(ctx context.Context) (manifest, error) {
	data, err := b.bucket.Get(ctx, manifestKey)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return manifest{}, nil
		}
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, fault.Internalf("kvbadger.backup.loadManifest", "decode manifest: %v", err)
	}
	return m, nil
}

func (b *backup) saveManifest(ctx context.Context, m manifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fault.Internalf("kvbadger.backup.saveManifest", "encode manifest: %v", err)
	}
	return b.bucket.Put(ctx, manifestKey, data, blob.PutOptions{})
}

// chunkWriter is the io.Writer db.Backup streams into: it buffers up to chunkBytes then flushes a part to
// blob, so neither an incremental nor a re-baseline ever buffers a whole segment (blob.Put is whole-object).
type chunkWriter struct {
	ctx    context.Context
	bucket blob.Bucket
	prefix string
	limit  int
	buf    bytes.Buffer
	parts  int
	err    error
}

func (b *backup) newChunkWriter(ctx context.Context, prefix string) *chunkWriter {
	return &chunkWriter{ctx: ctx, bucket: b.bucket, prefix: prefix, limit: b.chunkBytes}
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, _ := w.buf.Write(p)
	for w.buf.Len() >= w.limit {
		if err := w.flush(w.limit); err != nil {
			w.err = err
			return n, err
		}
	}
	return n, nil
}

func (w *chunkWriter) flush(n int) error {
	chunk := make([]byte, n)
	copy(chunk, w.buf.Next(n))
	name := fmt.Sprintf("%s/part-%05d", w.prefix, w.parts)
	if err := w.bucket.Put(w.ctx, name, chunk, blob.PutOptions{}); err != nil {
		return fault.Internalf("kvbadger.backup.flush", "upload %q: %v", name, err)
	}
	w.parts++
	return nil
}

// Close flushes the final partial part. An upload error surfaces here (so Ship/Rebaseline can decline to
// advance the cursor).
func (w *chunkWriter) Close() error {
	if w.err != nil {
		return w.err
	}
	if w.buf.Len() > 0 {
		return w.flush(w.buf.Len())
	}
	return nil
}

// discard drops the buffer without uploading (the no-delta path).
func (w *chunkWriter) discard() { w.buf.Reset() }

// partReader streams a segment's parts sequentially into db.Load, holding at most one part in memory.
type partReader struct {
	ctx    context.Context
	bucket blob.Bucket
	prefix string
	parts  int
	idx    int
	cur    *bytes.Reader
	err    error
}

func newPartReader(ctx context.Context, bucket blob.Bucket, prefix string, parts int) *partReader {
	return &partReader{ctx: ctx, bucket: bucket, prefix: prefix, parts: parts}
}

func (r *partReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	for {
		if r.cur != nil && r.cur.Len() > 0 {
			return r.cur.Read(p)
		}
		if r.idx >= r.parts {
			return 0, io.EOF
		}
		name := fmt.Sprintf("%s/part-%05d", r.prefix, r.idx)
		data, err := r.bucket.Get(r.ctx, name)
		if err != nil {
			r.err = fault.Internalf("kvbadger.backup.partReader", "fetch %q: %v", name, err)
			return 0, r.err
		}
		r.cur = bytes.NewReader(data)
		r.idx++
	}
}

// RunBackup drives the incremental + re-baseline cadence of a backup from NewBackup until ctx is cancelled
// (the daemon starts it in a goroutine when DR is enabled). A nil/non-*backup b is a no-op.
func RunBackup(ctx context.Context, b Backup, logger *slog.Logger) {
	bk, ok := b.(*backup)
	if !ok || bk == nil {
		return
	}
	interval := bk.interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	rebaseline := bk.fullEvery
	if rebaseline <= 0 {
		rebaseline = 24 * time.Hour
	}
	inc := time.NewTicker(interval)
	defer inc.Stop()
	reb := time.NewTimer(bk.untilRebaseline(ctx, rebaseline))
	defer reb.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-inc.C:
			if _, err := bk.Ship(ctx); err != nil && ctx.Err() == nil {
				logger.Error("kv backup ship failed", "err", err)
			}
		case <-reb.C:
			next := bk.retry // a failed re-baseline retries after rebaselineRetry, never in a loop of full exports
			if err := bk.Rebaseline(ctx); err != nil {
				if ctx.Err() == nil {
					logger.Error("kv backup re-baseline failed", "err", err)
				}
			} else {
				next = bk.untilRebaseline(ctx, rebaseline)
			}
			reb.Reset(next)
		}
	}
}

// untilRebaseline returns how long until the next re-baseline is due: one period after the time the manifest's
// base records, which survives a restart (#807). A manifest without a base or a recorded time, one of an older
// format (a chain without delete records, ADR-0195 Decision 5), or one that cannot be read, is due now.
func (b *backup) untilRebaseline(ctx context.Context, period time.Duration) time.Duration {
	man, err := b.loadManifest(ctx)
	if err != nil || man.Base == nil || man.Base.At.IsZero() || man.Format < manifestFormat {
		return 0
	}
	return max(time.Until(time.Time(man.Base.At).Add(period)), 0)
}
