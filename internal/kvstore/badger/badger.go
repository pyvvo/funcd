// Package badger is the durable kvstore.KV driver (ADR-0066): a pure-Go Badger instance
// (separate from the metastore's, ADR-0065) with a single-writer group-commit gateway and
// per-store DropPrefix teardown. It also defines the Backup/CDC extension seams (ADR-0067/0068)
// — both absent by default, so the base driver writes no change-log and runs no background loop.
package badger

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/options"
	"golang.org/x/sync/errgroup"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/platform/diskfull"
	"github.com/pyvvo/funcd/internal/snapshot"
	snapbadger "github.com/pyvvo/funcd/internal/snapshot/badger"
)

const (
	defaultGCInterval = 5 * time.Minute
	memTableSize      = 16 << 20
	baseTableSize     = 8 << 20
	valueLogFileSize  = 64 << 20
	blockCacheSize    = 32 << 20
	indexCacheSize    = 32 << 20
)

// Op is the mutation kind a CDC change-log entry records.
type Op uint8

const (
	OpPut Op = iota
	OpDelete
)

// Backup is the OPT-IN DR seam (ADR-0067). nil/absent unless DR is configured.
type Backup interface {
	Ship(ctx context.Context) (cursor uint64, err error)
	Restore(ctx context.Context) error
}

// CDC is the OPT-IN change-feed seam (ADR-0068). OnWrite is the write-hook the gateway calls INSIDE the
// data txn so the change-log entry commits atomically with the data; Tail publishes from a durable cursor.
type CDC interface {
	OnWrite(txn *badger.Txn, key string, op Op, expiresAt uint64) error
	Tail(ctx context.Context) error
}

type config struct {
	sync       bool
	gcInterval time.Duration
	batchMax   int
	backup     Backup
	cdc        CDC
}

// Option tunes the driver.
type Option func(*config)

// WithSyncWrites toggles fsync-on-commit (default true).
func WithSyncWrites(s bool) Option { return func(c *config) { c.sync = s } }

// WithValueLogGCInterval sets the value-log GC cadence (default 5m; 0 disables).
func WithValueLogGCInterval(d time.Duration) Option { return func(c *config) { c.gcInterval = d } }

// WithBackup wires the opt-in DR seam (ADR-0067). Absent ⇒ no backup.
func WithBackup(b Backup) Option { return func(c *config) { c.backup = b } }

// WithCDC wires the opt-in change-feed seam (ADR-0068). Absent ⇒ no _cdc/ writes.
func WithCDC(cdc CDC) Option { return func(c *config) { c.cdc = cdc } }

// writeReq is one write the gateway applies. cond, when set, is checked in the write's txn against the committed key;
// reap, when set, makes it the sweeper's delete of the entry that expired at that second; versioned asks for the key's
// version once the txn committed; res is the outcome apply records.
type writeReq struct {
	key       string
	val       []byte
	del       bool
	expiresAt uint64
	cond      *kvstore.Write
	reap      uint64
	versioned bool
	res       kvstore.Result
	done      chan writeRes
}

type writeRes struct {
	res kvstore.Result
	err error
}

type driver struct {
	db        *badger.DB
	timeline  string
	cdc       CDC
	backup    Backup
	reqs      chan *writeReq
	stop      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// Open opens (creating if absent) a durable Badger-backed kvstore.KV at dir, starting the single-writer
// group-commit gateway and the value-log GC. Close drains the gateway, stops GC, and releases the DB.
func Open(dir string, opts ...Option) (kvstore.KV, error) {
	kv, _, err := OpenWithSeams(dir, nil, nil, opts...)
	return kv, err
}

// newConfig applies the options over the defaults (sync on, 5m GC, 256-batch).
func newConfig(opts []Option) config {
	cfg := config{sync: true, gcInterval: defaultGCInterval, batchMax: 256}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// openDB opens the RAM-frugal Badger instance at dir (the single place the tuned options live, so Open and
// the seam-wiring OpenWithSeams share one db profile).
func openDB(dir string, sync bool) (*badger.DB, error) {
	bopts := badger.DefaultOptions(dir).
		WithLoggingLevel(badger.ERROR).
		WithSyncWrites(sync).
		WithNumMemtables(2).
		WithMemTableSize(memTableSize).
		WithNumLevelZeroTables(1).
		WithNumLevelZeroTablesStall(3).
		WithBaseTableSize(baseTableSize).
		WithValueLogFileSize(valueLogFileSize).
		WithBlockCacheSize(blockCacheSize).
		WithIndexCacheSize(indexCacheSize).
		WithNumCompactors(2).
		WithCompression(options.None)
	db, err := badger.Open(bopts)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "kvbadger.Open", "open badger at %q", dir)
	}
	return db, nil
}

// startDriver wires the driver over an opened db (seams already set on cfg) and starts the gateway + GC.
// Seams are attached BEFORE the gateway goroutine starts, so CDC's OnWrite fires on the first write with
// no attach-after-start race.
func startDriver(db *badger.DB, cfg config, timeline string) *driver {
	d := &driver{db: db, timeline: timeline, cdc: cfg.cdc, backup: cfg.backup, reqs: make(chan *writeReq), stop: make(chan struct{})}
	d.wg.Add(1)
	go d.gateway(cfg.batchMax)
	if cfg.gcInterval > 0 {
		d.wg.Add(1)
		go d.gcLoop(cfg.gcInterval)
	}
	return d
}

// gateway is THE single writer: it blocks for one request, greedy-drains whatever else is queued (up to
// batchMax), group-commits the batch (no fixed timer), and releases every waiter.
func (d *driver) gateway(batchMax int) {
	defer d.wg.Done()
	batch := make([]*writeReq, 0, batchMax)
	flush := func() {
		d.commit(batch)
		batch = batch[:0]
	}
	for {
		select {
		case r := <-d.reqs:
			batch = append(batch, r)
		case <-d.stop:
			for { // drain remaining requests, then exit
				select {
				case r := <-d.reqs:
					batch = append(batch, r)
					if len(batch) >= batchMax {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
		for draining := true; draining && len(batch) < batchMax; { // coalesce what's queued now
			select {
			case r := <-d.reqs:
				batch = append(batch, r)
			default:
				draining = false
			}
		}
		flush()
	}
}

// commit writes batch in as few Badger txns as it fits. A txn ends before a request whose key an earlier request in
// it wrote when either one is versioned, so a version names one write and a condition sees the committed key. A
// request that overflows the open txn (ErrTxnTooBig) starts the next txn, and a request Badger rejects on its own (an
// over-limit key) fails alone, so one caller's write never fails a co-batched caller's; a condition that does not hold
// is that request's own result. Requests that share a txn share its commit.
func (d *driver) commit(batch []*writeReq) {
	for len(batch) > 0 {
		n, staged, err := d.update(batch)
		switch {
		case staged:
			d.release(batch[:n], err)
		case n > 0:
			d.commit(batch[:n]) // the requests before batch[n] fit one txn
		default:
			batch[0].done <- writeRes{err: err} // batch[0] fails even in an empty txn
			n = 1
		}
		batch = batch[n:]
	}
}

// update stages batch's leading requests in one txn and commits it. staged reports that the first n requests
// staged, and err is then the commit's; otherwise batch[n] failed to stage with err and the txn was discarded.
func (d *driver) update(batch []*writeReq) (n int, staged bool, err error) {
	versioned := map[string]bool{} // each key staged in the txn, and whether a versioned request staged it
	err = d.db.Update(func(txn *badger.Txn) error {
		for _, r := range batch {
			if v, seen := versioned[r.key]; seen && (v || r.versioned) {
				break
			}
			if e := d.apply(txn, r); e != nil {
				return e
			}
			versioned[r.key] = versioned[r.key] || r.versioned
			n++
		}
		staged = true
		return nil
	})
	return n, staged, err
}

// apply stages one write in txn, unless its condition does not hold: r.res then holds the key's current version and
// nothing is staged. When a CDC seam is wired, the write's change-log entry goes IN THE SAME txn (the outbox property).
func (d *driver) apply(txn *badger.Txn, r *writeReq) error {
	r.res = kvstore.Result{}
	if r.reap != 0 {
		return d.applyReap(txn, r)
	}
	if r.versioned {
		cur, err := d.current(txn, r.key)
		if err != nil {
			return err
		}
		if r.cond != nil && !holds(cur, *r.cond) {
			r.res.Version = cur.Version
			return nil
		}
		if cur.ExpiresAt != 0 { // the entry this write replaces leaves the index with it
			if err := txn.Delete(expiryKey(cur.ExpiresAt, r.key)); err != nil {
				return err
			}
		}
	} else if err := dropIndexOf(txn, r.key); err != nil {
		return err
	}
	r.res.Done = true
	op, err := OpPut, error(nil)
	switch {
	case r.del:
		op, err = OpDelete, d.stageDelete(txn, r.key)
	case r.expiresAt != 0:
		err = txn.SetEntry(&badger.Entry{Key: []byte(r.key), Value: r.val, ExpiresAt: r.expiresAt})
		if err == nil {
			err = txn.Set(expiryKey(r.expiresAt, r.key), nil)
		}
	default:
		err = txn.Set([]byte(r.key), r.val)
	}
	if err != nil || d.cdc == nil {
		return err
	}
	return d.cdc.OnWrite(txn, r.key, op, r.expiresAt)
}

// applyReap is the sweeper's delete: it drops the index entry of the expiry r.reap and deletes the key unless a write
// since made it live or deleted it. Its newest entry is then the value that expired, or none when Badger's compaction
// removed it with every older version; either way the delete goes to the change feed and, with the backup seam wired,
// to the chain as a delete record (ADR-0195 Decision 1), which a restore needs when the chain holds an older value.
// stageDelete would write none: Badger reads an expired key as absent.
func (d *driver) applyReap(txn *badger.Txn, r *writeReq) error {
	if err := txn.Delete(expiryKey(r.reap, r.key)); err != nil {
		return err
	}
	ok, err := reapable(txn, r.key)
	if err != nil || !ok {
		return err
	}
	r.res.Done = true
	if err := txn.Delete([]byte(r.key)); err != nil {
		return err
	}
	if d.backup != nil {
		if err := txn.Set(delRecordKey(r.key), nil); err != nil {
			return err
		}
	}
	if d.cdc == nil {
		return nil
	}
	return d.cdc.OnWrite(txn, r.key, OpDelete, 0)
}

// reapable reports whether the sweep deletes key: its newest entry expired, or compaction left it no entry. A key whose
// newest entry is live or a delete keeps what it holds.
func reapable(txn *badger.Txn, key string) (bool, error) {
	found, _, expired := newest(txn, key)
	return !found || expired, nil
}

// newest reads key's newest entry, a delete and an expired value included, a value staged earlier in txn too: whether
// there is one, whether it is a value, live or expired, and whether it expired. Badger's Get reads an expired value as
// absent, though the chain may hold it.
func newest(txn *badger.Txn, key string) (found, value, expired bool) {
	opts := badger.DefaultIteratorOptions
	opts.AllVersions, opts.PrefetchValues, opts.Prefix = true, false, []byte(key)
	it := txn.NewIterator(opts)
	defer it.Close()
	it.Seek([]byte(key))
	if !it.ValidForPrefix([]byte(key)) || string(it.Item().Key()) != key {
		return false, false, false
	}
	item := it.Item()
	gone, exp := item.IsDeletedOrExpired(), item.ExpiresAt() != 0
	return true, !gone || exp, gone && exp
}

// current reads key's live entry, its value left out: the zero Entry when it is absent or expired. No earlier request
// of txn wrote key (update), so the read is of the committed key.
func (d *driver) current(txn *badger.Txn, key string) (kvstore.Entry, error) {
	item, err := txn.Get([]byte(key))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return kvstore.Entry{}, nil
	}
	if err != nil {
		return kvstore.Entry{}, err
	}
	return kvstore.Entry{Version: kvstore.Version(d.timeline, key, item.Version()), ExpiresAt: item.ExpiresAt()}, nil
}

// holds reports whether w's condition holds for the key's current entry cur.
func holds(cur kvstore.Entry, w kvstore.Write) bool {
	switch {
	case w.IfAbsent:
		return cur.Version == ""
	case w.IfVersion != "":
		return cur.Version == w.IfVersion
	}
	return true
}

// release answers rs, which one txn committed, or failed together with err. The versioned writes among them get the
// txn's version, read back from one of their keys: the gateway is the only writer of a data key, so it still holds
// the txn's version. A key a DropPrefix removed since reads as version 0. When the read fails, the writes are stored
// but rs get the error.
func (d *driver) release(rs []*writeReq, err error) {
	if err == nil {
		err = d.readVersion(rs)
	}
	for _, r := range rs {
		if err != nil {
			r.done <- writeRes{err: err}
			continue
		}
		r.done <- writeRes{res: r.res}
	}
}

func (d *driver) readVersion(rs []*writeReq) error {
	wrote := func(r *writeReq) bool { return r.versioned && r.res.Done && !r.del }
	i := slices.IndexFunc(rs, wrote)
	if i < 0 {
		return nil
	}
	var n uint64
	err := d.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(rs[i].key))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err == nil {
			n = item.Version()
		}
		return err
	})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "kvbadger.write", "read the version the write stored")
	}
	for _, r := range rs {
		if wrote(r) && n != 0 {
			r.res.Version = kvstore.Version(d.timeline, r.key, n)
		}
	}
	return nil
}

// stageDelete stages the delete of key and, while the backup seam is wired and key holds a value (a value staged
// earlier in txn counts, and so does one that expired, which the chain may hold), its delete record at the same commit
// version (ADR-0195 Decision 1), so a backup chain keeps the delete after compaction drops the delete marker.
func (d *driver) stageDelete(txn *badger.Txn, key string) error {
	if d.backup == nil {
		return txn.Delete([]byte(key))
	}
	_, value, _ := newest(txn, key)
	if err := txn.Delete([]byte(key)); err != nil || !value {
		return err
	}
	return txn.Set(delRecordKey(key), nil)
}

func (d *driver) gcLoop(interval time.Duration) {
	defer d.wg.Done()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
			for d.db.RunValueLogGC(0.5) == nil { //nolint:revive // reclaim while a rewrite is pending
			}
		}
	}
}

// submit hands a write to the gateway and waits for its commit (or ctx cancel).
func (d *driver) submit(ctx context.Context, r *writeReq) (kvstore.Result, error) {
	r.done = make(chan writeRes, 1)
	select {
	case d.reqs <- r:
	case <-ctx.Done():
		return kvstore.Result{}, ctx.Err()
	}
	select {
	case out := <-r.done:
		if out.err != nil {
			return kvstore.Result{}, diskfull.Wrapf(out.err, "kvbadger.write", "commit")
		}
		return out.res, nil
	case <-ctx.Done():
		return kvstore.Result{}, ctx.Err()
	}
}

func (d *driver) Get(ctx context.Context, key string) ([]byte, bool, error) {
	e, found, err := d.GetEntry(ctx, key)
	return e.Value, found, err
}

// GetEntry reads key with its version and expiry (kvstore.Conditional); an expired key is not found.
func (d *driver) GetEntry(ctx context.Context, key string) (kvstore.Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return kvstore.Entry{}, false, err
	}
	var e kvstore.Entry
	var found bool
	err := d.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		e.Value, err = item.ValueCopy(nil)
		e.Version, e.ExpiresAt, found = kvstore.Version(d.timeline, key, item.Version()), item.ExpiresAt(), err == nil
		return err
	})
	if err != nil {
		return kvstore.Entry{}, false, fault.Wrapf(err, fault.Internal, "kvbadger.Get", "read the key")
	}
	return e, found, nil
}

func (d *driver) Put(ctx context.Context, key string, value []byte) error {
	cp := make([]byte, len(value)) // copy: the gateway commits asynchronously; callers must not alias
	copy(cp, value)
	_, err := d.submit(ctx, &writeReq{key: key, val: cp})
	return err
}

func (d *driver) Delete(ctx context.Context, key string) error {
	_, err := d.submit(ctx, &writeReq{key: key, del: true})
	return err
}

// PutIf writes value under key with w's TTL when w's condition holds (kvstore.Conditional). The gateway checks the
// condition in the txn that writes, so of racing writes conditional on one state exactly one is Done.
func (d *driver) PutIf(ctx context.Context, key string, value []byte, w kvstore.Write) (kvstore.Result, error) {
	if err := kvstore.CheckWrite("kvbadger.PutIf", w); err != nil {
		return kvstore.Result{}, err
	}
	r := &writeReq{key: key, val: bytes.Clone(value), expiresAt: w.ExpiryOf(time.Now()), versioned: true}
	if w.IfAbsent || w.IfVersion != "" {
		r.cond = &w
	}
	return d.submit(ctx, r)
}

// DeleteIf deletes key when it exists at version (kvstore.Conditional).
func (d *driver) DeleteIf(ctx context.Context, key string, version string) (kvstore.Result, error) {
	if version == "" {
		return kvstore.Result{}, fault.Invalidf("kvbadger.DeleteIf", "a conditional delete needs the key's version")
	}
	return d.submit(ctx, &writeReq{key: key, del: true, cond: &kvstore.Write{IfVersion: version}, versioned: true})
}

func (d *driver) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keys := make([]string, 0)
	p := []byte(prefix)
	err := d.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		opts.Prefix = p
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			k := it.Item().KeyCopy(nil)
			if !strings.HasPrefix(string(k), Reserved) { // never surface internal CDC/cursor keys
				keys = append(keys, string(k))
			}
		}
		return nil
	})
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "kvbadger.List", "list the keys")
	}
	sort.Strings(keys)
	return keys, nil
}

// Usage is the bytes of the keys and values under prefix (ADR-0292), a capability beyond the kvstore.KV port like
// DropPrefix: one read transaction, values unread.
func (d *driver) Usage(ctx context.Context, prefix string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var n int64
	p := []byte(prefix)
	err := d.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		opts.Prefix = p
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			n += int64(len(it.Item().Key())) + it.Item().ValueSize()
		}
		return nil
	})
	if err != nil {
		return 0, fault.Wrapf(err, fault.Internal, "kvbadger.Usage", "measure the keys")
	}
	return n, nil
}

// Reserved is the NUL-prefixed namespace for the driver's internal keys (the CDC outbox + its cursors,
// ADR-0068; the backup cursor and delete records, ADR-0067/0195). NUL cannot occur in a facade tenant key
// (<namespace>/<binding>/<key>), so List excludes it without ever hiding a real key.
const Reserved = "\x00"

// dropConcurrency bounds the concurrent gateway Deletes of a seam-wired DropPrefix, so the gateway
// group-commits them instead of paying one commit per key.
const dropConcurrency = 64

// DropPrefix wipes every key under prefix — the per-store teardown (O(store)). It is a driver capability
// beyond the flat kvstore.KV port; the KV reconciler may type-assert for it. Badger's native DropPrefix
// writes no tombstone, so with a CDC or Backup seam wired each key is deleted through the gateway instead:
// the change feed (ADR-0068) and the incremental backup (ADR-0067) must both see the deletion.
func (d *driver) DropPrefix(prefix string) error {
	if d.cdc == nil && d.backup == nil {
		index, err := d.indexUnder(prefix)
		if err != nil {
			return err
		}
		if err := d.db.DropPrefix([]byte(prefix)); err != nil {
			return diskfull.Wrapf(err, "kvbadger.DropPrefix", "drop %q", prefix)
		}
		return d.dropIndexKeys(prefix, index)
	}
	keys, err := d.List(context.Background(), prefix)
	if err != nil {
		return err
	}
	g, ctx := errgroup.WithContext(context.Background())
	g.SetLimit(dropConcurrency)
	for _, k := range keys {
		g.Go(func() error { return d.Delete(ctx, k) })
	}
	return g.Wait()
}

// SnapshotPrefix exports one store (ADR-0209 Decision 7) beyond the kvstore.KV port: one read transaction and one
// iterator over prefix, each live key without prefix with its value and its expiry, in key order, version "". It needs
// no backup seam and carries no delete record. A bad prefix is fault.Invalid before any emit.
func (d *driver) SnapshotPrefix(prefix string) snapshot.Source {
	return prefixSource{db: d.db, prefix: prefix}
}

type prefixSource struct {
	db     *badger.DB
	prefix string
}

func (s prefixSource) Snapshot(ctx context.Context, emit func(snapshot.Record) error) (string, error) {
	if err := kvstore.CheckStorePrefix("kvbadger.SnapshotPrefix", s.prefix); err != nil {
		return "", err
	}
	return "", snapbadger.SnapshotPrefix(ctx, s.db, []byte(s.prefix), emit)
}

// LoadPrefix loads one store (ADR-0222 Decision 7), the reverse of SnapshotPrefix: each record next returns, until
// io.EOF, goes under prefix through the gateway with its expiry, so the change feed and the export see it; a record
// whose expiry passed is left out. A store holding a key is fault.Conflict before any write; a bad prefix is
// fault.Invalid.
func (d *driver) LoadPrefix(ctx context.Context, prefix string, next func() (snapshot.Record, error)) error {
	const op = "kvbadger.LoadPrefix"
	if err := kvstore.CheckStorePrefix(op, prefix); err != nil {
		return err
	}
	keys, err := d.List(ctx, prefix)
	if err != nil {
		return err
	}
	if len(keys) > 0 {
		return fault.Conflictf(op, "store %q is not empty: it holds %d key(s)", prefix, len(keys))
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(dropConcurrency)
	for {
		r, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.Join(err, g.Wait())
		}
		if gctx.Err() != nil {
			break
		}
		if kvstore.Expired(r.ExpiresAt, time.Now()) {
			continue
		}
		w := &writeReq{key: prefix + string(r.Key), val: bytes.Clone(r.Value), expiresAt: r.ExpiresAt}
		g.Go(func() error {
			_, err := d.submit(gctx, w)
			return err
		})
	}
	return g.Wait()
}

func (d *driver) Close() error {
	d.closeOnce.Do(func() { // idempotent: a double Close must not panic on the gateway/stop channel
		close(d.stop)
		d.wg.Wait()
		if c, ok := d.cdc.(interface{ release() }); ok { // return the CDC seq lease before closing the db
			c.release()
		}
		if bk, ok := d.backup.(*exporter); ok { // Badger panics when an export reads a closed db: let a running Ship or Rebaseline finish
			bk.loop.Lock()
			defer bk.loop.Unlock()
		}
		if err := d.db.Close(); err != nil {
			d.closeErr = fault.Internalf("kvbadger.Close", "%v", err)
		}
	})
	return d.closeErr
}
