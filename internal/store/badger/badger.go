// Package badger is the pure-Go store.Engine (ADR-0065): a Badger v4 LSM at a local
// directory, superseding ADR-0006's slatedb/cgo engine and restoring funcd's pure-Go
// static binary. (bucket, key) is encoded as bucket+NUL+key — NUL cannot occur in a
// GVK bucket or a namespace/name key, so Scan prefix-matches unambiguously. Update runs
// in one Badger transaction (atomic all-or-nothing); the store wrapper serializes writers
// above it (its writeMu), so no engine-level conflict arises in practice.
package badger

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/options"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/store"
)

const sep = "\x00"

type config struct {
	sync       bool
	gcInterval time.Duration
}

// Option tunes the engine.
type Option func(*config)

// WithSyncWrites toggles fsync-on-commit (default true — durable control-plane writes).
func WithSyncWrites(s bool) Option { return func(c *config) { c.sync = s } }

// WithValueLogGCInterval sets the value-log GC cadence (default 5m; 0 disables).
func WithValueLogGCInterval(d time.Duration) Option { return func(c *config) { c.gcInterval = d } }

type engine struct {
	db        *badger.DB
	stop      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// Open opens (creating if absent) a Badger-backed store.Engine at dir with the RAM-frugal
// metastore options profile (small memtables/caches, modest value-log, compression off,
// small resource blobs kept inline). Close flushes + releases the DB and stops GC.
func Open(dir string, opts ...Option) (store.Engine, error) {
	const op = "badger.Open"
	cfg := config{sync: true, gcInterval: 5 * time.Minute}
	for _, o := range opts {
		o(&cfg)
	}
	bopts := badger.DefaultOptions(dir).
		WithLoggingLevel(badger.ERROR).
		WithSyncWrites(cfg.sync).
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
		return nil, fault.Internalf(op, "open badger at %q: %v", dir, err)
	}
	e := &engine{db: db, stop: make(chan struct{})}
	if cfg.gcInterval > 0 {
		e.wg.Add(1)
		go e.gcLoop(cfg.gcInterval)
	}
	return e, nil
}

// gcLoop reclaims the value log periodically (RunValueLogGC returns nil while it rewrote a
// file, ErrNoRewrite when there is nothing to reclaim).
func (e *engine) gcLoop(interval time.Duration) {
	defer e.wg.Done()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			for e.db.RunValueLogGC(0.5) == nil { //nolint:revive // reclaim while a rewrite is pending
			}
		}
	}
}

func (e *engine) View(ctx context.Context, fn func(store.Txn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return e.db.View(func(bt *badger.Txn) error { return fn(&txn{bt: bt}) })
}

func (e *engine) Update(ctx context.Context, fn func(store.Txn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := e.db.Update(func(bt *badger.Txn) error { return fn(&txn{bt: bt, write: true}) })
	if errors.Is(err, badger.ErrConflict) { // defensive — the store wrapper serializes writers
		return fault.Conflictf("badger.Update", "transaction conflict")
	}
	return err
}

func (e *engine) Close() error {
	e.closeOnce.Do(func() { // idempotent: a double Close (e.g. platform + caller) must not panic
		close(e.stop)
		e.wg.Wait()
		if err := e.db.Close(); err != nil {
			e.closeErr = fault.Internalf("badger.Close", "%v", err)
		}
	})
	return e.closeErr
}

type txn struct {
	bt    *badger.Txn
	write bool
}

func skey(bucket, key string) []byte { return []byte(bucket + sep + key) }

func (t *txn) Get(bucket, key string) ([]byte, bool, error) {
	item, err := t.bt.Get(skey(bucket, key))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fault.Internalf("badger.Get", "%v", err)
	}
	v, err := item.ValueCopy(nil)
	if err != nil {
		return nil, false, fault.Internalf("badger.Get", "value: %v", err)
	}
	return v, true, nil
}

func (t *txn) Put(bucket, key string, val []byte) error {
	if !t.write {
		return fault.Internalf("badger.Put", "write in a read-only transaction")
	}
	if err := t.bt.Set(skey(bucket, key), val); err != nil {
		return fault.Internalf("badger.Put", "%v", err)
	}
	return nil
}

func (t *txn) Delete(bucket, key string) error {
	if !t.write {
		return fault.Internalf("badger.Delete", "write in a read-only transaction")
	}
	if err := t.bt.Delete(skey(bucket, key)); err != nil {
		return fault.Internalf("badger.Delete", "%v", err)
	}
	return nil
}

func (t *txn) Scan(bucket string, fn func(key string, val []byte) error) error {
	prefix := []byte(bucket + sep)
	opts := badger.DefaultIteratorOptions
	opts.Prefix = prefix
	it := t.bt.NewIterator(opts)
	defer it.Close()
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		item := it.Item()
		v, err := item.ValueCopy(nil)
		if err != nil {
			return fault.Internalf("badger.Scan", "value: %v", err)
		}
		key := strings.TrimPrefix(string(item.Key()), bucket+sep)
		if err := fn(key, v); err != nil {
			return err
		}
	}
	return nil
}
