// Package memory is the in-memory kvstore.KV driver (ADR-0019): a mutex-guarded map,
// for dev/e2e/tests. The JetStream / database-layer production drivers live behind the
// same port.
package memory

import (
	"context"
	"errors"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// entry is a stored value with the number of its write and its expiry, as the Badger driver keeps them.
type entry struct {
	val       []byte
	n         uint64
	expiresAt uint64
}

type driver struct {
	mu       sync.RWMutex
	m        map[string]entry
	last     uint64 // the number the last write took, so each write's is higher
	timeline string // minted per start: a memory store keeps nothing across one
}

// New returns an in-memory KV driver.
func New() kvstore.KV {
	return &driver{m: map[string]entry{}, timeline: kvstore.NewTimeline()}
}

// live returns key's entry unless it is absent or expired; d.mu is held.
func (d *driver) live(key string) (entry, bool) {
	e, ok := d.m[key]
	if !ok || kvstore.Expired(e.expiresAt, time.Now()) {
		return entry{}, false
	}
	return e, true
}

func (d *driver) version(key string, e entry) string { return kvstore.Version(d.timeline, key, e.n) }

// set stores value under key as a new write; d.mu is held for writing.
func (d *driver) set(key string, value []byte, expiresAt uint64) string {
	d.last++
	e := entry{val: slices.Clone(value), n: d.last, expiresAt: expiresAt}
	d.m[key] = e
	return d.version(key, e)
}

func (d *driver) Get(ctx context.Context, key string) ([]byte, bool, error) {
	e, found, err := d.GetEntry(ctx, key)
	return e.Value, found, err
}

// GetEntry reads key with its version and expiry (kvstore.Conditional); an expired key is not found.
func (d *driver) GetEntry(_ context.Context, key string) (kvstore.Entry, bool, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	e, ok := d.live(key)
	if !ok {
		return kvstore.Entry{}, false, nil
	}
	val := make([]byte, len(e.val)) // copy: callers must not alias the stored bytes
	copy(val, e.val)
	return kvstore.Entry{Value: val, Version: d.version(key, e), ExpiresAt: e.expiresAt}, true, nil
}

func (d *driver) Put(_ context.Context, key string, value []byte) error {
	d.mu.Lock()
	d.set(key, value, 0)
	d.mu.Unlock()
	return nil
}

func (d *driver) Delete(_ context.Context, key string) error {
	d.mu.Lock()
	delete(d.m, key)
	d.mu.Unlock()
	return nil
}

// PutIf writes value under key with w's expiry when w's condition holds (kvstore.Conditional).
func (d *driver) PutIf(_ context.Context, key string, value []byte, w kvstore.Write) (kvstore.Result, error) {
	if err := kvstore.CheckWrite("kvmemory.PutIf", w); err != nil {
		return kvstore.Result{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.live(key)
	cur := ""
	if ok {
		cur = d.version(key, e)
	}
	if (w.IfAbsent && ok) || (w.IfVersion != "" && cur != w.IfVersion) {
		return kvstore.Result{Version: cur}, nil
	}
	return kvstore.Result{Done: true, Version: d.set(key, value, w.ExpiryOf(time.Now()))}, nil
}

// DeleteIf deletes key when it exists at version (kvstore.Conditional).
func (d *driver) DeleteIf(_ context.Context, key string, version string) (kvstore.Result, error) {
	if version == "" {
		return kvstore.Result{}, fault.Invalidf("kvmemory.DeleteIf", "a conditional delete needs the key's version")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.live(key)
	if !ok || d.version(key, e) != version {
		cur := ""
		if ok {
			cur = d.version(key, e)
		}
		return kvstore.Result{Version: cur}, nil
	}
	delete(d.m, key)
	return kvstore.Result{Done: true}, nil
}

// Sweep deletes up to limit keys that expired by now and skip does not name (kvstore.Sweeper), the badger driver's
// analogue.
func (d *driver) Sweep(_ context.Context, now time.Time, limit int, skip func(string) bool) ([]string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var swept []string
	more := false
	for k, e := range d.m {
		if !kvstore.Expired(e.expiresAt, now) || skip(k) {
			continue
		}
		if len(swept) == limit {
			more = true
			break
		}
		delete(d.m, k)
		swept = append(swept, k)
	}
	sort.Strings(swept)
	return swept, more, nil
}

// DropPrefix wipes every key under prefix in one operation — the per-store teardown the KVStore
// reconciler calls on delete (ADR-0072). It is a concrete method beyond the kvstore.KV port (the
// badger driver's analogue), type-asserted at wiring.
func (d *driver) DropPrefix(prefix string) error {
	d.mu.Lock()
	for k := range d.m {
		if strings.HasPrefix(k, prefix) {
			delete(d.m, k)
		}
	}
	d.mu.Unlock()
	return nil
}

// Usage is the bytes of the live keys and values under prefix (ADR-0292), the badger driver's analogue.
func (d *driver) Usage(_ context.Context, prefix string) (int64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var n int64
	for k := range d.m {
		if e, ok := d.live(k); ok && strings.HasPrefix(k, prefix) {
			n += int64(len(k) + len(e.val))
		}
	}
	return n, nil
}

func (d *driver) List(_ context.Context, prefix string) ([]string, error) {
	d.mu.RLock()
	keys := make([]string, 0)
	for k := range d.m {
		if _, ok := d.live(k); ok && strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	d.mu.RUnlock()
	sort.Strings(keys)
	return keys, nil
}

// SnapshotPrefix exports one store (ADR-0209 Decision 7): its live keys without prefix, with their values and expiries,
// in key order, copied under one read lock and emitted after it; version "". A bad prefix is fault.Invalid before any
// emit.
func (d *driver) SnapshotPrefix(prefix string) snapshot.Source {
	return prefixSource{d: d, prefix: prefix}
}

// LoadPrefix loads one store (ADR-0222 Decision 7): each record next returns, until io.EOF, under prefix with its
// expiry; a record whose expiry passed is left out. A store holding a key is fault.Conflict before any write; a bad
// prefix is fault.Invalid.
func (d *driver) LoadPrefix(ctx context.Context, prefix string, next func() (snapshot.Record, error)) error {
	const op = "kvmemory.LoadPrefix"
	if err := kvstore.CheckStorePrefix(op, prefix); err != nil {
		return err
	}
	if keys, _ := d.List(ctx, prefix); len(keys) > 0 {
		return fault.Conflictf(op, "store %q is not empty: it holds %d key(s)", prefix, len(keys))
	}
	for {
		r, err := next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if kvstore.Expired(r.ExpiresAt, time.Now()) {
			continue
		}
		d.mu.Lock()
		d.set(prefix+string(r.Key), r.Value, r.ExpiresAt)
		d.mu.Unlock()
	}
}

type prefixSource struct {
	d      *driver
	prefix string
}

func (s prefixSource) Snapshot(ctx context.Context, emit func(snapshot.Record) error) (string, error) {
	if err := kvstore.CheckStorePrefix("kvmemory.SnapshotPrefix", s.prefix); err != nil {
		return "", err
	}
	var recs []snapshot.Record
	s.d.mu.RLock()
	for k := range s.d.m {
		rest, ok := strings.CutPrefix(k, s.prefix)
		if e, live := s.d.live(k); ok && live {
			recs = append(recs, snapshot.Record{Key: []byte(rest), Value: slices.Clone(e.val), ExpiresAt: e.expiresAt})
		}
	}
	s.d.mu.RUnlock()
	slices.SortFunc(recs, func(a, b snapshot.Record) int { return strings.Compare(string(a.Key), string(b.Key)) })
	for _, r := range recs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := emit(r); err != nil {
			return "", err
		}
	}
	return "", nil
}
