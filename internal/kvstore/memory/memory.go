// Package memory is the in-memory kvstore.KV driver (ADR-0019): a mutex-guarded map,
// for dev/e2e/tests. The JetStream / database-layer production drivers live behind the
// same port.
package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/pyvvo/funcd/internal/kvstore"
)

type driver struct {
	mu sync.RWMutex
	m  map[string][]byte
}

// New returns an in-memory KV driver.
func New() kvstore.KV {
	return &driver{m: map[string][]byte{}}
}

func (d *driver) Get(_ context.Context, key string) ([]byte, bool, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	v, ok := d.m[key]
	if !ok {
		return nil, false, nil
	}
	out := make([]byte, len(v)) // copy: callers must not alias the stored bytes
	copy(out, v)
	return out, true, nil
}

func (d *driver) Put(_ context.Context, key string, value []byte) error {
	cp := make([]byte, len(value))
	copy(cp, value)
	d.mu.Lock()
	d.m[key] = cp
	d.mu.Unlock()
	return nil
}

func (d *driver) Delete(_ context.Context, key string) error {
	d.mu.Lock()
	delete(d.m, key)
	d.mu.Unlock()
	return nil
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

func (d *driver) List(_ context.Context, prefix string) ([]string, error) {
	d.mu.RLock()
	keys := make([]string, 0)
	for k := range d.m {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	d.mu.RUnlock()
	sort.Strings(keys)
	return keys, nil
}
