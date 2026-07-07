// Package memory is the pure-Go, cgo-free store.Engine: a process-local map
// guarded by a RWMutex. It is the in-memory test double and the engine behind
// the funcd.InMemory() e2e harness (ADR-0006 §4); badger is the persistent
// production engine. Update applies its writes atomically on commit.
package memory

import (
	"context"
	"sync"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// New returns a fresh, empty in-memory engine.
func New() store.Engine {
	return &engine{data: map[string]map[string][]byte{}}
}

type engine struct {
	mu   sync.RWMutex
	data map[string]map[string][]byte // bucket -> key -> value
}

func (e *engine) View(ctx context.Context, fn func(store.Txn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return fn(&txn{e: e})
}

func (e *engine) Update(ctx context.Context, fn func(store.Txn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	tx := &txn{e: e, writes: map[pendingKey]pendingVal{}}
	if err := fn(tx); err != nil {
		return err // pending writes discarded — atomic all-or-nothing
	}
	tx.commit()
	return nil
}

func (e *engine) Close() error { return nil }

type pendingKey struct{ bucket, key string }

type pendingVal struct {
	val []byte
	del bool
}

type txn struct {
	e      *engine
	writes map[pendingKey]pendingVal // nil for a read-only (View) txn
}

func (tx *txn) Get(bucket, key string) ([]byte, bool, error) {
	if tx.writes != nil {
		if w, ok := tx.writes[pendingKey{bucket, key}]; ok {
			if w.del {
				return nil, false, nil
			}
			return cloneBytes(w.val), true, nil
		}
	}
	b := tx.e.data[bucket]
	if b == nil {
		return nil, false, nil
	}
	v, ok := b[key]
	if !ok {
		return nil, false, nil
	}
	return cloneBytes(v), true, nil
}

func (tx *txn) Put(bucket, key string, val []byte) error {
	if tx.writes == nil {
		return fault.Internalf("memory.Put", "write in a read-only transaction")
	}
	tx.writes[pendingKey{bucket, key}] = pendingVal{val: cloneBytes(val)}
	return nil
}

func (tx *txn) Delete(bucket, key string) error {
	if tx.writes == nil {
		return fault.Internalf("memory.Delete", "write in a read-only transaction")
	}
	tx.writes[pendingKey{bucket, key}] = pendingVal{del: true}
	return nil
}

func (tx *txn) Scan(bucket string, fn func(key string, val []byte) error) error {
	seen := map[string]bool{}
	for pk, pv := range tx.writes {
		if pk.bucket != bucket {
			continue
		}
		seen[pk.key] = true
		if pv.del {
			continue
		}
		if err := fn(pk.key, cloneBytes(pv.val)); err != nil {
			return err
		}
	}
	for key, v := range tx.e.data[bucket] {
		if seen[key] {
			continue
		}
		if err := fn(key, cloneBytes(v)); err != nil {
			return err
		}
	}
	return nil
}

func (tx *txn) commit() {
	for pk, pv := range tx.writes {
		if pv.del {
			if b := tx.e.data[pk.bucket]; b != nil {
				delete(b, pk.key)
			}
			continue
		}
		b := tx.e.data[pk.bucket]
		if b == nil {
			b = map[string][]byte{}
			tx.e.data[pk.bucket] = b
		}
		b[pk.key] = pv.val
	}
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}
