package badger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/version"
)

// The KV chain's layout under the target root, beside ADR-0203's gen/ (ADR-0209 Decision 2), and its format, a
// sequence of its own (Decision 4). defaultChunkBytes is also the maximum.
const (
	KVPrefix          = "kv/"
	ChainFormat       = 1
	defaultChunkBytes = 8 << 20

	basePrefix   = KVPrefix + "base/"
	incPrefix    = KVPrefix + "inc/"
	manifestName = "manifest.yaml"
	storeName    = "kv"
)

const (
	backupCursorKey = Reserved + "backup/cursor"
	// exportNumGo caps the export's producers: each holds its own batch buffers, so Badger's default of 8
	// multiplies the export's peak heap (ADR-0067 Decision 2: a low Stream.NumGo caps the export RSS).
	exportNumGo = 1

	delRecordPrefix = Reserved + "backup/del/" // a record per key deleted through the gateway with backup on
	delChunkBytes   = 1 << 20                  // the budget of one restore or prune transaction
	delEntryBytes   = 128                      // added to the length of each key a transaction deletes
	pruneRetries    = 3                        // retries of one prune chunk after badger.ErrConflict

	defaultRebaselineRetry = time.Hour
)

// BackupConfig configures the opt-in DR backup (ADR-0067, ADR-0195, ADR-0209). Zero ChunkBytes ⇒ 8 MiB; zero
// RebaselineRetry ⇒ 1h; a nil Logger ⇒ slog.Default(). Ready is the target's Ready then Conditional (ADR-0203), nil ⇒
// ready and conditional; Seal and Recipients are ADR-0204's, nil Seal ⇒ unsealed; Record is ADR-0205's
// Recorder("kv"), called after each run, nil ⇒ log only; Retention is the bases' ladder (Decision 5); Hold is
// ADR-0206's gate, nil ⇒ never held.
type BackupConfig struct {
	Interval, Rebaseline, RebaselineRetry time.Duration
	ChunkBytes                            int
	Logger                                *slog.Logger
	Ready                                 func(context.Context) (conditional bool, err error)
	Seal                                  backup.Seal
	Recipients                            []string
	Record                                func(start time.Time, err error)
	Retention                             backup.Retention
	Hold                                  interface{ Held() bool }
}

// ChainManifest is a base's or a segment's manifest.yaml, created last (Decision 4): Segment 0 is the base; Since
// and To are the Badger versions covered; At is when the export's read began; Store is the stored bytes; no
// Recipients ⇒ unsealed.
type ChainManifest struct {
	Format     int              `json:"format"`
	Chain      uint64           `json:"chain"`
	Segment    uint64           `json:"segment"`
	Since      uint64           `json:"since"`
	To         uint64           `json:"to"`
	At         v1.Timestamp     `json:"at"`
	Funcd      string           `json:"funcd"`
	Store      backup.StoreFile `json:"store"`
	Recipients []string         `json:"recipients,omitempty"`
}

// exporter implements the ADR-0066 Backup seam (ADR-0067, ADR-0209): version-watermarked incremental segments of
// one chain, a base per chain, every object put once and never read, overwritten or deleted. The chain state lives
// in the cursor record. It owns the export loop; correctness lives in the cursor, never in Badger's lossy Subscribe.
type exporter struct {
	db     *badger.DB
	bucket blob.Bucket
	cfg    BackupConfig
	logger *slog.Logger
	clock  clock.Clock
	loop   sync.Mutex // serializes Ship/Rebaseline — one exporter at a time, never overlapping
	// known is the chain whose complete base a listing showed or a re-baseline wrote, 0 for none; started holds the
	// complete bases of the start's listing until the first Ship reads them.
	known   uint64
	started map[uint64]bool
	// bases counts the bases written, so RunBackup restarts the re-baseline period after a Ship wrote one.
	bases atomic.Int64
}

// chainState is the cursor record: the cursor, the chain n and the next segment number m (Decision 3).
type chainState struct{ cursor, chain, next uint64 }

// delRecordKey is the key of the delete record of key (ADR-0195 Decision 1).
func delRecordKey(key string) []byte { return []byte(delRecordPrefix + key) }

// NewBackup builds the DR backup over an opened KV Badger instance. A nil bucket, ChunkBytes above 8 MiB, or a Seal
// without Recipients (or the reverse) is fault.Invalid (never a silent half-configured backup).
func NewBackup(db *badger.DB, bucket blob.Bucket, cfg BackupConfig) (Backup, error) {
	const op = "kvbadger.NewBackup"
	switch {
	case bucket == nil:
		return nil, fault.Invalidf(op, "backup enabled but target is empty")
	case cfg.ChunkBytes > defaultChunkBytes:
		return nil, fault.Invalidf(op, "kvstore.backup.chunkBytes is %d: want at most %d (8 MiB)", cfg.ChunkBytes, defaultChunkBytes)
	case (cfg.Seal == nil) != (len(cfg.Recipients) == 0):
		return nil, fault.Invalidf(op, "a seal needs its recipients, and recipients a seal")
	}
	if cfg.ChunkBytes <= 0 {
		cfg.ChunkBytes = defaultChunkBytes
	}
	if cfg.RebaselineRetry <= 0 {
		cfg.RebaselineRetry = defaultRebaselineRetry
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &exporter{db: db, bucket: bucket, cfg: cfg, logger: cfg.Logger, clock: clock.System()}, nil
}

// LifecycleRules is the expiry in days the operator sets per kv/ prefix (Decision 5): kv/inc/ and kv/base/hourly/
// ⌈hourly/24⌉, kv/base/daily/ daily and kv/base/weekly/ 7 × weekly; a class unused has none.
func LifecycleRules(r backup.Retention) map[string]int {
	rules := map[string]int{incPrefix: (r.Hourly + 23) / 24, basePrefix + string(backup.Hourly) + "/": (r.Hourly + 23) / 24}
	if r.Daily > 0 {
		rules[basePrefix+string(backup.Daily)+"/"] = r.Daily
	}
	if r.Weekly > 0 {
		rules[basePrefix+string(backup.Weekly)+"/"] = 7 * r.Weekly
	}
	return rules
}

func baseDir(class backup.Class, n uint64) string {
	return fmt.Sprintf("%s%s/%010d/", basePrefix, class, n)
}

func incDir(n, m uint64) string { return fmt.Sprintf("%s%010d/%010d/", incPrefix, n, m) }

// Ship runs one incremental tick (Decision 3): it awaits Ready, stores next+1 before the segment's first put,
// writes kv/inc/<n>/<next>/ and sets the cursor only after the manifest; a failure leaves the cursor, so the next
// Ship writes the next number with the same since. An idle tick writes nothing (#790). It re-baselines instead when
// the record is absent (#808), 8 bytes long (legacy, restored, older binary), or names a chain whose complete base
// the start's listing lacks.
func (b *exporter) Ship(ctx context.Context) (uint64, error) {
	b.loop.Lock()
	defer b.loop.Unlock()
	const op = "kvbadger.backup.Ship"
	st, full, err := b.readState(ctx)
	if err != nil {
		return 0, err
	}
	conditional, err := b.ready(ctx)
	if err != nil {
		return st.cursor, err
	}
	if full && b.known != st.chain {
		complete := b.started
		b.started = nil
		if complete == nil {
			l, err := listBases(ctx, b.bucket)
			if err != nil {
				return st.cursor, err
			}
			complete = l.complete
		}
		if complete[st.chain] {
			b.known = st.chain
		}
	}
	if !full || b.known != st.chain {
		if err := b.rebaseline(ctx, conditional); err != nil {
			return st.cursor, err
		}
		st, _, err = b.readState(ctx)
		return st.cursor, err
	}
	at := b.clock.Now()
	w := b.newChunkWriter(ctx, incDir(st.chain, st.next), conditional, func() error {
		return b.writeState(chainState{cursor: st.cursor, chain: st.chain, next: st.next + 1})
	})
	sealed, err := w.seal(b.cfg.Seal)
	if err != nil {
		return st.cursor, err
	}
	to, _, berr := b.export(sealed, st.cursor, false)
	if berr != nil {
		return st.cursor, fault.Internalf(op, "badger backup since %d: %v", st.cursor, berr)
	}
	if to <= st.cursor { // no new versions: nothing put unless a part was full, cursor unchanged
		return st.cursor, w.err
	}
	m := ChainManifest{Chain: st.chain, Segment: st.next, Since: st.cursor, To: to, At: v1.NewTimestamp(at)}
	if err := b.finish(ctx, w, sealed, m); err != nil {
		return st.cursor, err
	}
	if err := b.writeState(chainState{cursor: to, chain: st.chain, next: st.next + 1}); err != nil {
		return st.cursor, err
	}
	return to, nil
}

// Rebaseline awaits Ready and writes a new chain's base (Decision 3), then prunes the delete records it reflects.
func (b *exporter) Rebaseline(ctx context.Context) error {
	b.loop.Lock()
	defer b.loop.Unlock()
	conditional, err := b.ready(ctx)
	if err != nil {
		return err
	}
	return b.rebaseline(ctx, conditional)
}

// rebaseline lists kv/base/ once and seeks kv/inc/ for a higher chain, since a base can expire before its
// incrementals; it writes chain n' = 1 + the highest n held, complete or not, under its ladder class, stores
// to ‖ n' ‖ 1, then prunes the records the base reflects (ADR-0195 Decision 4): a failed prune is logged, not
// returned, and the next re-baseline prunes them. The caller holds b.loop.
func (b *exporter) rebaseline(ctx context.Context, conditional bool) error {
	const op = "kvbadger.backup.Rebaseline"
	l, err := listBases(ctx, b.bucket)
	if err != nil {
		return err
	}
	high, err := highestInc(ctx, b.bucket, l.highest)
	if err != nil {
		return err
	}
	n, at := high+1, b.clock.Now()
	class := backup.ClassFor(l.entries, at, b.cfg.Retention)
	w := b.newChunkWriter(ctx, baseDir(class, n), conditional, nil)
	sealed, err := w.seal(b.cfg.Seal)
	if err != nil {
		return err
	}
	to, readTs, berr := b.export(sealed, 0, true)
	if berr != nil {
		return fault.Internalf(op, "badger full backup: %v", berr)
	}
	if err := b.finish(ctx, w, sealed, ChainManifest{Chain: n, To: to, At: v1.NewTimestamp(at)}); err != nil {
		return err
	}
	if err := b.writeState(chainState{cursor: to, chain: n, next: 1}); err != nil {
		return err
	}
	b.known = n
	b.bases.Add(1)
	if err := b.pruneDelRecords(ctx, readTs); err != nil && ctx.Err() == nil {
		b.logger.Warn("kv backup delete-record prune failed; the next re-baseline prunes the rest", "err", err)
	}
	return nil
}

// finish closes the seal and the last part, then puts the manifest of m, created last.
func (b *exporter) finish(ctx context.Context, w *chunkWriter, sealed io.WriteCloser, m ChainManifest) error {
	if err := sealed.Close(); err != nil {
		return w.fail(err)
	}
	if err := w.Close(); err != nil {
		return err
	}
	m.Format, m.Funcd, m.Recipients = ChainFormat, version.Version, slices.Clone(b.cfg.Recipients)
	m.Store = backup.StoreFile{Name: storeName, Parts: w.parts, Bytes: w.n, SHA256: hex.EncodeToString(w.h.Sum(nil))}
	data, err := yaml.Marshal(m)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "kvbadger.backup", "encode the manifest")
	}
	return w.put(ctx, w.prefix+manifestName, data)
}

func (b *exporter) ready(ctx context.Context) (bool, error) {
	if b.cfg.Ready == nil {
		return true, ctx.Err()
	}
	return b.cfg.Ready(ctx)
}

// export streams every version > since to w and returns the version the cursor may advance to, and the read
// timestamp taken before the stream. It leaves out the backup's own cursor: each cursor write is a new
// version, so exporting it made every idle tick ship a segment (#790). Each Stream producer reads its own,
// later snapshot, so a version above the read timestamp can be exported while a write below it was missed
// (#806): the result is capped at that read timestamp, which every producer sees in full, and the next export
// ships the versions above it again. A base also leaves out every delete record whose newest version is at or
// below the read timestamp: the base reflects that delete (ADR-0195 Decision 2).
func (b *exporter) export(w io.Writer, since uint64, base bool) (to, readTs uint64, err error) {
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
func (b *exporter) applyDelRecords(ctx context.Context) error {
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
func (b *exporter) pruneDelRecords(ctx context.Context, readTs uint64) error {
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
func (b *exporter) pruneChunk(recs [][]byte, readTs uint64) error {
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
func (b *exporter) forDelRecordChunks(
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

// readState reads the cursor record; full is false when it is absent or 8 bytes long (legacy, restored, an older
// binary), which reads as the cursor alone.
func (b *exporter) readState(ctx context.Context) (st chainState, full bool, err error) {
	if err := ctx.Err(); err != nil {
		return chainState{}, false, err
	}
	err = b.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(backupCursorKey))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			switch len(val) {
			case 24:
				full = true
				st = chainState{cursor: binary.BigEndian.Uint64(val), chain: binary.BigEndian.Uint64(val[8:]),
					next: binary.BigEndian.Uint64(val[16:])}
			case 8:
				st.cursor = binary.BigEndian.Uint64(val)
			}
			return nil
		})
	})
	if err != nil {
		return chainState{}, false, fault.Internalf("kvbadger.backup.cursor", "%v", err)
	}
	return st, full, nil
}

// cursor reads the persisted version watermark (0 if none).
func (b *exporter) cursor(ctx context.Context) (uint64, error) {
	st, _, err := b.readState(ctx)
	return st.cursor, err
}

// writeState stores cursor ‖ n ‖ next, three big-endian uint64. Written directly (not via the gateway): a reserved
// key the user write-path never touches, so it cannot conflict with a data txn. It ignores a cancel: what it records
// is already durable on the target.
func (b *exporter) writeState(st chainState) error {
	val := binary.BigEndian.AppendUint64(nil, st.cursor)
	val = binary.BigEndian.AppendUint64(val, st.chain)
	return setCursorRecord(b.db, binary.BigEndian.AppendUint64(val, st.next))
}

// setCursorRecord stores the cursor record val; a restore stores the 8-byte cursor alone (Decision 6).
func setCursorRecord(db *badger.DB, val []byte) error {
	if err := db.Update(func(txn *badger.Txn) error { return txn.Set([]byte(backupCursorKey), val) }); err != nil {
		return fault.Internalf("kvbadger.backup.setCursor", "%v", err)
	}
	return nil
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

// chunkWriter is the io.Writer an export streams into: it buffers up to limit bytes, then puts a part, so neither
// an incremental nor a re-baseline ever buffers a whole segment (blob.Put is whole-object). It hashes and counts the
// stored bytes, and runs first before its first put.
type chunkWriter struct {
	ctx         context.Context
	bucket      blob.Bucket
	prefix      string
	limit       int
	conditional bool
	first       func() error
	buf         bytes.Buffer
	h           hash.Hash
	n           int64
	parts       int
	err         error
}

func (b *exporter) newChunkWriter(ctx context.Context, prefix string, conditional bool, first func() error) *chunkWriter {
	return &chunkWriter{ctx: ctx, bucket: b.bucket, prefix: prefix, limit: b.cfg.ChunkBytes, conditional: conditional,
		first: first, h: sha256.New()}
}

// seal wraps w in one ADR-0204 stream; nil seal ⇒ w as is.
func (w *chunkWriter) seal(seal backup.Seal) (io.WriteCloser, error) {
	if seal == nil {
		return nopCloser{w}, nil
	}
	sealed, err := seal(w)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "kvbadger.backup", "seal")
	}
	return sealed, nil
}

func (w *chunkWriter) put(ctx context.Context, key string, data []byte) error {
	if err := w.bucket.Put(ctx, key, data, blob.PutOptions{IfNotExist: w.conditional}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "kvbadger.backup", "put %q", key)
	}
	return nil
}

func (w *chunkWriter) fail(err error) error {
	if w.err == nil {
		w.err = err
	}
	return w.err
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, _ := w.buf.Write(p)
	for w.buf.Len() >= w.limit {
		if err := w.flush(w.limit); err != nil {
			return n, w.fail(err)
		}
	}
	return n, nil
}

func (w *chunkWriter) flush(n int) error {
	if w.parts == 0 && w.first != nil {
		if err := w.first(); err != nil {
			return err
		}
	}
	chunk := bytes.Clone(w.buf.Next(n))
	if err := w.put(w.ctx, fmt.Sprintf("%spart-%05d", w.prefix, w.parts), chunk); err != nil {
		return err
	}
	w.h.Write(chunk)
	w.n += int64(len(chunk))
	w.parts++
	return nil
}

// Close puts the final part; an empty file is one empty part. An upload error surfaces here, so Ship and
// Rebaseline decline to advance the cursor.
func (w *chunkWriter) Close() error {
	if w.err != nil {
		return w.err
	}
	if w.buf.Len() > 0 || w.parts == 0 {
		return w.fail(w.flush(w.buf.Len()))
	}
	return nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// RunBackup drives the incremental + re-baseline cadence of a backup from NewBackup until ctx is cancelled
// (the daemon starts it in a goroutine when DR is enabled). Each outcome goes to the log and Record; while held a
// due run writes nothing and records nothing, Ships recheck each interval and a re-baseline after RebaselineRetry
// (Decision 6). A nil/non-exporter b is a no-op.
func RunBackup(ctx context.Context, b Backup, logger *slog.Logger) {
	bk, ok := b.(*exporter)
	if !ok || bk == nil {
		return
	}
	interval := bk.cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	rebaseline := bk.cfg.Rebaseline
	if rebaseline <= 0 {
		rebaseline = 24 * time.Hour
	}
	inc := time.NewTicker(interval)
	defer inc.Stop()
	reb := time.NewTimer(bk.untilRebaseline(ctx, rebaseline))
	defer reb.Stop()
	held := func() bool { return bk.cfg.Hold != nil && bk.cfg.Hold.Held() }
	for {
		select {
		case <-ctx.Done():
			return
		case <-inc.C:
			if held() {
				continue
			}
			start, bases := bk.clock.Now(), bk.bases.Load()
			_, err := bk.Ship(ctx)
			if ctx.Err() != nil {
				return
			}
			if bk.bases.Load() != bases { // the Ship wrote a base: the period starts again
				reb.Reset(rebaseline)
			}
			if err != nil {
				logger.Error("kv backup ship failed", "err", err)
			}
			bk.record(start, err)
		case <-reb.C:
			if held() {
				reb.Reset(bk.cfg.RebaselineRetry)
				continue
			}
			start, next := bk.clock.Now(), rebaseline
			err := bk.Rebaseline(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil { // a failed re-baseline retries after rebaselineRetry, never in a loop of full exports
				logger.Error("kv backup re-baseline failed", "err", err)
				next = bk.cfg.RebaselineRetry
			}
			bk.record(start, err)
			reb.Reset(next)
		}
	}
}

func (b *exporter) record(start time.Time, err error) {
	if b.cfg.Record != nil {
		b.cfg.Record(start, err)
	}
}

// untilRebaseline lists kv/base/ (the start's listing) and returns how long until a re-baseline is due: one period
// after the newest complete base manifest's ModTime, which survives a restart (#807); none, or a failed listing, is
// due now.
func (b *exporter) untilRebaseline(ctx context.Context, period time.Duration) time.Duration {
	b.loop.Lock()
	defer b.loop.Unlock()
	l, err := listBases(ctx, b.bucket)
	if err != nil {
		return 0
	}
	b.started = l.complete
	if l.newest.IsZero() {
		return 0
	}
	return max(l.newest.Add(period).Sub(b.clock.Now()), 0)
}

// baseListing is what one listing of kv/base/ shows: the highest chain of any key, the complete bases as ladder
// entries and by chain, and the newest complete manifest's ModTime.
type baseListing struct {
	highest  uint64
	entries  []backup.Entry
	complete map[uint64]bool
	newest   time.Time
}

func listBases(ctx context.Context, b blob.Bucket) (baseListing, error) {
	items, err := b.List(ctx, basePrefix)
	if err != nil {
		return baseListing{}, err
	}
	l := baseListing{complete: map[uint64]bool{}}
	for _, a := range items {
		k, ok := parseKVKey(a.Key)
		if !ok || k.base == "" {
			continue
		}
		l.highest = max(l.highest, k.chain)
		if k.name == manifestName {
			l.complete[k.chain] = true
			l.entries = append(l.entries, backup.Entry{Generation: k.chain, Class: k.base, Complete: true, At: a.ModTime})
			if a.ModTime.After(l.newest) {
				l.newest = a.ModTime
			}
		}
	}
	return l, nil
}

// highestInc seeks kv/inc/ for chains above high, one key at a time (ADR-0184), and returns the highest.
func highestInc(ctx context.Context, b blob.Bucket, high uint64) (uint64, error) {
	after := fmt.Sprintf("%s%010d0", incPrefix, high)
	for {
		items, _, err := b.ListAfter(ctx, incPrefix, after, 1)
		if err != nil || len(items) == 0 {
			return high, err
		}
		after = items[0].Key
		if k, ok := parseKVKey(after); ok && k.base == "" {
			high = max(high, k.chain)
			after = fmt.Sprintf("%s%010d0", incPrefix, k.chain)
		}
	}
}

// kvKey is a parsed key under kv/: base names the class of a base's key, "" for a segment's; segment is 0 for a
// base; part is -1 for a key that is no part.
type kvKey struct {
	base           backup.Class
	chain, segment uint64
	name           string
	part           int
}

func parseKVKey(key string) (kvKey, bool) {
	var k kvKey
	var nums []string
	if rest, ok := strings.CutPrefix(key, basePrefix); ok {
		f := strings.Split(rest, "/")
		if len(f) != 3 || f[0] == "" {
			return kvKey{}, false
		}
		k.base, nums, k.name = backup.Class(f[0]), f[1:2], f[2]
	} else if rest, ok := strings.CutPrefix(key, incPrefix); ok {
		f := strings.Split(rest, "/")
		if len(f) != 3 {
			return kvKey{}, false
		}
		nums, k.name = f[:2], f[2]
	} else {
		return kvKey{}, false
	}
	for i, s := range nums {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil || len(s) != 10 {
			return kvKey{}, false
		}
		if i == 0 {
			k.chain = v
		} else {
			k.segment = v
		}
	}
	k.part = -1
	if p, ok := strings.CutPrefix(k.name, "part-"); ok {
		if i, err := strconv.Atoi(p); err == nil && i >= 0 {
			k.part = i
		}
	}
	return k, true
}
