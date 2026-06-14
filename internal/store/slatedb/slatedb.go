//go:build slatedb

// The slatedb engine: a store.Engine over slatedb.io/slatedb-go (UniFFI/cgo).
// slatedb is a single keyspace, so (bucket, key) is encoded as bucket+NUL+key;
// Scan uses ScanPrefix(bucket+NUL). Update buffers writes into a slatedb
// WriteBatch applied atomically on commit, then flushes for durability. The
// store wrapper serializes writers, so the reads-before-writes pattern in each
// Update needs no engine-level serializable isolation.
package slatedb

import (
	"context"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/store"
	sdb "slatedb.io/slatedb-go/uniffi"
)

const (
	dbPath = "metastore"
	sep    = "\x00"
)

type engine struct {
	store *sdb.ObjectStore
	db    *sdb.Db
}

// Open resolves the object store from the URL (memory:///, file:///path,
// s3://bucket/prefix) and opens the metastore database on it.
func Open(objectStoreURL string) (store.Engine, error) {
	os, err := sdb.ObjectStoreResolve(objectStoreURL)
	if err != nil {
		return nil, fault.Internalf("slatedb.Open", "resolve object store %q: %v", objectStoreURL, err)
	}
	db, err := sdb.NewDbBuilder(dbPath, os).Build()
	if err != nil {
		os.Destroy()
		return nil, fault.Internalf("slatedb.Open", "open db at %q: %v", objectStoreURL, err)
	}
	return &engine{store: os, db: db}, nil
}

func (e *engine) View(ctx context.Context, fn func(store.Txn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(&txn{db: e.db})
}

func (e *engine) Update(ctx context.Context, fn func(store.Txn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	batch := sdb.NewWriteBatch()
	defer batch.Destroy()
	tx := &txn{db: e.db, batch: batch}
	if err := fn(tx); err != nil {
		return err // batch discarded — atomic all-or-nothing
	}
	if !tx.dirty {
		return nil
	}
	if _, err := e.db.Write(batch); err != nil {
		return fault.Internalf("slatedb.Update", "write batch: %v", err)
	}
	if err := e.db.Flush(); err != nil {
		return fault.Internalf("slatedb.Update", "flush: %v", err)
	}
	return nil
}

func (e *engine) Close() error {
	e.db.Destroy()
	e.store.Destroy()
	return nil
}

type txn struct {
	db    *sdb.Db
	batch *sdb.WriteBatch // nil for a read-only (View) txn
	dirty bool
}

func skey(bucket, key string) []byte { return []byte(bucket + sep + key) }

func (t *txn) Get(bucket, key string) ([]byte, bool, error) {
	v, err := t.db.Get(skey(bucket, key))
	if err != nil {
		return nil, false, fault.Internalf("slatedb.Get", "%v", err)
	}
	if v == nil {
		return nil, false, nil
	}
	return *v, true, nil
}

func (t *txn) Put(bucket, key string, val []byte) error {
	if t.batch == nil {
		return fault.Internalf("slatedb.Put", "write in a read-only transaction")
	}
	if err := t.batch.Put(skey(bucket, key), val); err != nil {
		return fault.Internalf("slatedb.Put", "%v", err)
	}
	t.dirty = true
	return nil
}

func (t *txn) Delete(bucket, key string) error {
	if t.batch == nil {
		return fault.Internalf("slatedb.Delete", "write in a read-only transaction")
	}
	if err := t.batch.Delete(skey(bucket, key)); err != nil {
		return fault.Internalf("slatedb.Delete", "%v", err)
	}
	t.dirty = true
	return nil
}

func (t *txn) Scan(bucket string, fn func(key string, val []byte) error) error {
	prefix := bucket + sep
	it, err := t.db.ScanPrefix([]byte(prefix))
	if err != nil {
		return fault.Internalf("slatedb.Scan", "%v", err)
	}
	defer it.Destroy()
	for {
		kv, err := it.Next()
		if err != nil {
			return fault.Internalf("slatedb.Scan", "iterate: %v", err)
		}
		if kv == nil {
			break
		}
		key := strings.TrimPrefix(string(kv.Key), prefix)
		if err := fn(key, kv.Value); err != nil {
			return err
		}
	}
	return nil
}
