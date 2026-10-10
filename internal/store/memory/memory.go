// Package memory is the pure-Go, cgo-free store.Engine: a process-local map
// guarded by a RWMutex. It is the in-memory test double and the engine behind
// the funcd.InMemory() e2e harness (ADR-0006 §4); badger is the persistent
// production engine. Update applies its writes atomically on commit.
package memory

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// loadBatch bounds the records one Load step writes under the lock.
const loadBatch = 1024

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

// Snapshot copies the map under the read lock and emits after releasing it, so writers wait only for the copy
// (ADR-0202 Decision 1). Records come in key order, Key = bucket+NUL+key; a stored value is never changed in
// place, so the copy may share it until emit gets a clone.
func (e *engine) Snapshot(ctx context.Context, emit func(snapshot.Record) error) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	e.mu.RLock()
	var recs []snapshot.Record
	for bucket, b := range e.data {
		for key, v := range b {
			recs = append(recs, snapshot.Record{Key: []byte(bucket + "\x00" + key), Value: v})
		}
	}
	e.mu.RUnlock()
	slices.SortFunc(recs, func(a, b snapshot.Record) int { return bytes.Compare(a.Key, b.Key) })
	for _, r := range recs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := emit(snapshot.Record{Key: r.Key, Value: cloneBytes(r.Value)}); err != nil {
			return "", err
		}
	}
	return "", nil
}

// Load fills an empty engine before store.New, loadBatch records per lock, and leaves the timeline record out,
// so New mints a new one (ADR-0202 Decision 3).
func (e *engine) Load(ctx context.Context, next func() (snapshot.Record, error)) error {
	const op = "memory.Load"
	if err := ctx.Err(); err != nil {
		return err
	}
	if !e.empty() {
		return fault.Conflictf(op, "the store is not empty")
	}
	tx := &txn{e: e, writes: map[pendingKey]pendingVal{}}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "read the next record")
		}
		i := bytes.LastIndexByte(r.Key, 0)
		if i < 0 {
			return fault.Invalidf(op, "record key %q has no bucket", r.Key)
		}
		pk := pendingKey{bucket: string(r.Key[:i]), key: string(r.Key[i+1:])}
		if store.IsTimelineRecord(pk.bucket, pk.key) {
			continue
		}
		tx.writes[pk] = pendingVal{val: cloneBytes(r.Value)}
		if len(tx.writes) == loadBatch {
			e.apply(tx)
			tx = &txn{e: e, writes: map[pendingKey]pendingVal{}}
		}
	}
	e.apply(tx)
	return nil
}

func (e *engine) empty() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, b := range e.data {
		if len(b) > 0 {
			return false
		}
	}
	return true
}

// apply commits one Load batch under the write lock.
func (e *engine) apply(tx *txn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	tx.commit()
}

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
