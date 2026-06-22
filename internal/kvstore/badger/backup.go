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

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/blob"
)

// backup implements the ADR-0066 Backup seam (ADR-0067): version-watermarked incremental export of the KV
// Badger instance to a blob target, plus a periodic full re-baseline and a restore. It owns the export
// loop; correctness lives in the watermarked cursor, never in Badger's lossy Subscribe.
type backup struct {
	db         *badger.DB
	bucket     blob.Bucket
	chunkBytes int           // max bytes buffered per blob object (bounds re-baseline RSS)
	interval   time.Duration // incremental cadence
	rebaseline time.Duration // full re-baseline cadence
	loop       sync.Mutex    // serializes Ship/Rebaseline — one exporter at a time, never overlapping
}

const (
	defaultChunkBytes = 64 << 20            // 64 MiB
	backupCursorKey   = Reserved + "backup/cursor"
	manifestKey       = "manifest.json"
)

// BackupConfig configures the opt-in DR backup (ADR-0067). Zero ChunkBytes ⇒ 64 MiB.
type BackupConfig struct {
	Interval   time.Duration
	Rebaseline time.Duration
	ChunkBytes int
}

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
	return &backup{db: db, bucket: bucket, chunkBytes: cb, interval: cfg.Interval, rebaseline: cfg.Rebaseline}, nil
}

// segment is one exported range of blob parts (a base or an incremental). Parts are part-00000…part-NNNNN.
type segment struct {
	Prefix string `json:"prefix"`
	Since  uint64 `json:"since"`
	To     uint64 `json:"to"`
	Parts  int    `json:"parts"`
}

// manifest is the ordered restore chain: the latest base then the incrementals after it. It is the
// authoritative segment list (blob has no rename), written only after every part is durably uploaded.
type manifest struct {
	Base *segment  `json:"base,omitempty"`
	Incs []segment `json:"incs,omitempty"`
}

// Ship runs one incremental tick: export every change since the persisted cursor to a fresh blob segment,
// and advance the cursor ONLY after the manifest + all parts are durably uploaded. A crash/upload failure
// before that leaves the cursor unchanged, so the next run re-ships the interval (idempotent on restore).
func (b *backup) Ship(ctx context.Context) (uint64, error) {
	b.loop.Lock()
	defer b.loop.Unlock()
	const op = "kvbadger.backup.Ship"
	since, err := b.cursor(ctx)
	if err != nil {
		return 0, err
	}
	man, err := b.loadManifest(ctx)
	if err != nil {
		return since, err
	}
	prefix := fmt.Sprintf("inc/%020d", since)
	w := b.newChunkWriter(ctx, prefix)
	to, berr := b.db.Backup(w, since)
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
// segments — bounding the restore chain and the on-disk backup set.
func (b *backup) Rebaseline(ctx context.Context) error {
	b.loop.Lock()
	defer b.loop.Unlock()
	const op = "kvbadger.backup.Rebaseline"
	old, err := b.loadManifest(ctx)
	if err != nil {
		return err
	}
	at, err := b.cursor(ctx)
	if err != nil {
		return err
	}
	prefix := fmt.Sprintf("base/%020d", at)
	w := b.newChunkWriter(ctx, prefix)
	to, berr := b.db.Backup(w, 0)
	if berr != nil {
		return fault.Internalf(op, "badger full backup: %v", berr)
	}
	if err := w.Close(); err != nil {
		return err
	}
	man := manifest{Base: &segment{Prefix: prefix, Since: 0, To: to, Parts: w.parts}}
	if err := b.saveManifest(ctx, man); err != nil {
		return err
	}
	if err := b.setCursor(ctx, to); err != nil {
		return err
	}
	b.prune(ctx, old, prefix) // best-effort: drop the old base (unless reused) + old incrementals
	return nil
}

// Restore reconstructs the instance from the latest base then each incremental, in version order. Idempotent
// (Badger Load is last-writer-wins per key-version). Run into a fresh instance.
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
	for _, s := range man.Incs {
		if err := b.loadSegment(ctx, s); err != nil {
			return err
		}
	}
	return nil
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
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var v uint64
	err := b.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(backupCursorKey))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			if len(val) == 8 {
				v = binary.BigEndian.Uint64(val)
			}
			return nil
		})
	})
	if err != nil {
		return 0, fault.Internalf("kvbadger.backup.cursor", "%v", err)
	}
	return v, nil
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
	return b.bucket.Put(ctx, manifestKey, data)
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
	if err := w.bucket.Put(w.ctx, name, chunk); err != nil {
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
	rebaseline := bk.rebaseline
	if rebaseline <= 0 {
		rebaseline = 24 * time.Hour
	}
	inc := time.NewTicker(interval)
	defer inc.Stop()
	reb := time.NewTicker(rebaseline)
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
			if err := bk.Rebaseline(ctx); err != nil && ctx.Err() == nil {
				logger.Error("kv backup re-baseline failed", "err", err)
			}
		}
	}
}
