// Package badger is the durable kvstore.KV driver (ADR-0066): a pure-Go Badger instance
// (separate from the metastore's, ADR-0065) with a single-writer group-commit gateway and
// per-store DropPrefix teardown. It also defines the Backup/CDC extension seams (ADR-0067/0068)
// — both absent by default, so the base driver writes no change-log and runs no background loop.
package badger

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/options"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/kvstore"
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
	OnWrite(txn *badger.Txn, key string, op Op) error
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

type writeReq struct {
	key  string
	val  []byte
	del  bool
	done chan error
}

type driver struct {
	db        *badger.DB
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
	cfg := newConfig(opts)
	db, err := openDB(dir, cfg.sync)
	if err != nil {
		return nil, err
	}
	return startDriver(db, cfg), nil
}

// newConfig applies the options over the defaults (sync on, 5m GC, 256-batch).
func newConfig(opts []Option) config {
	cfg := config{sync: true, gcInterval: 5 * time.Minute, batchMax: 256}
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
		WithMemTableSize(16 << 20).
		WithNumLevelZeroTables(1).
		WithNumLevelZeroTablesStall(3).
		WithBaseTableSize(8 << 20).
		WithValueLogFileSize(64 << 20).
		WithBlockCacheSize(32 << 20).
		WithIndexCacheSize(32 << 20).
		WithNumCompactors(2).
		WithCompression(options.None)
	db, err := badger.Open(bopts)
	if err != nil {
		return nil, fault.Internalf("kvbadger.Open", "open badger at %q: %v", dir, err)
	}
	return db, nil
}

// startDriver wires the driver over an opened db (seams already set on cfg) and starts the gateway + GC.
// Seams are attached BEFORE the gateway goroutine starts, so CDC's OnWrite fires on the first write with
// no attach-after-start race.
func startDriver(db *badger.DB, cfg config) *driver {
	d := &driver{db: db, cdc: cfg.cdc, backup: cfg.backup, reqs: make(chan *writeReq), stop: make(chan struct{})}
	d.wg.Add(1)
	go d.gateway(cfg.batchMax)
	if cfg.gcInterval > 0 {
		d.wg.Add(1)
		go d.gcLoop(cfg.gcInterval)
	}
	return d
}

// gateway is THE single writer: it blocks for one request, greedy-drains whatever else is queued (up to
// batchMax) into one Badger txn (group commit — no fixed timer), commits, and releases every waiter. When a
// CDC seam is wired, each write also appends its change-log entry IN THE SAME txn (the outbox property).
func (d *driver) gateway(batchMax int) {
	defer d.wg.Done()
	batch := make([]*writeReq, 0, batchMax)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		err := d.db.Update(func(txn *badger.Txn) error {
			for _, r := range batch {
				if r.del {
					if e := txn.Delete([]byte(r.key)); e != nil {
						return e
					}
					if d.cdc != nil {
						if e := d.cdc.OnWrite(txn, r.key, OpDelete); e != nil {
							return e
						}
					}
					continue
				}
				if e := txn.Set([]byte(r.key), r.val); e != nil {
					return e
				}
				if d.cdc != nil {
					if e := d.cdc.OnWrite(txn, r.key, OpPut); e != nil {
						return e
					}
				}
			}
			return nil
		})
		for _, r := range batch {
			r.done <- err
		}
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
func (d *driver) submit(ctx context.Context, r *writeReq) error {
	r.done = make(chan error, 1)
	select {
	case d.reqs <- r:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-r.done:
		if err != nil {
			return fault.Internalf("kvbadger.write", "%v", err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *driver) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	var out []byte
	var found bool
	err := d.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		out, err = item.ValueCopy(nil)
		found = err == nil
		return err
	})
	if err != nil {
		return nil, false, fault.Internalf("kvbadger.Get", "%v", err)
	}
	return out, found, nil
}

func (d *driver) Put(ctx context.Context, key string, value []byte) error {
	cp := make([]byte, len(value)) // copy: the gateway commits asynchronously; callers must not alias
	copy(cp, value)
	return d.submit(ctx, &writeReq{key: key, val: cp})
}

func (d *driver) Delete(ctx context.Context, key string) error {
	return d.submit(ctx, &writeReq{key: key, del: true})
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
		return nil, fault.Internalf("kvbadger.List", "%v", err)
	}
	sort.Strings(keys)
	return keys, nil
}

// Reserved is the NUL-prefixed namespace for the driver's internal keys (the CDC outbox + its cursors,
// ADR-0068). NUL cannot occur in a facade tenant key (<namespace>/<binding>/<key>), so List excludes it
// without ever hiding a real key. The CDC seam writes under this prefix.
const Reserved = "\x00"

// DropPrefix wipes every key under prefix in one operation — the per-store teardown (O(store)). It is a
// driver capability beyond the flat kvstore.KV port; the KV reconciler may type-assert for it.
func (d *driver) DropPrefix(prefix string) error {
	if err := d.db.DropPrefix([]byte(prefix)); err != nil {
		return fault.Internalf("kvbadger.DropPrefix", "%v", err)
	}
	return nil
}

func (d *driver) Close() error {
	d.closeOnce.Do(func() { // idempotent: a double Close must not panic on the gateway/stop channel
		close(d.stop)
		d.wg.Wait()
		if err := d.db.Close(); err != nil {
			d.closeErr = fault.Internalf("kvbadger.Close", "%v", err)
		}
	})
	return d.closeErr
}
