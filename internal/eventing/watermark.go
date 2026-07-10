package eventing

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/kvstore"
)

// Cursor is the compact, bounded watermark for one named blob event (ADR-0119): the newest ModTime seen and
// the key set AT that timestamp (the tie-break set). A listed object is NEW iff its ModTime > MaxModTime, or
// (ModTime == MaxModTime and its key is not in KeysAtMax). KeysAtMax resets when MaxModTime advances, so it
// stays bounded to the objects sharing the newest timestamp — enough for Created-only dedup.
type Cursor struct {
	MaxModTime time.Time `json:"maxModTime"`
	KeysAtMax  []string  `json:"keysAtMax"`
}

// Watermark persists per-event dedup state so a re-list and a restart never re-emit a seen object (ADR-0119).
// The V1 driver stores one JSON record per (ns, source, event) over internal/kvstore.KV.
type Watermark interface {
	Load(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName) (Cursor, error)
	Save(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName, c Cursor) error
}

// kvWatermarkPrefix namespaces the watermark records under the shared KV substrate so they never collide
// with the function-facing KV key space (<namespace>/<binding>/<key>).
const kvWatermarkPrefix = "_eventing/blobwatch/"

// KVWatermark is the V1 Watermark driver (ADR-0119): one small JSON Cursor per (ns, source, event) over the
// in-tree kvstore.KV substrate — durable get/put with its own backup path, so no second persistence
// mechanism. A missing record loads as the zero Cursor (nothing seen yet).
type KVWatermark struct {
	kv kvstore.KV
}

// NewKVWatermark builds the KV-backed Watermark driver over kv (required).
func NewKVWatermark(kv kvstore.KV) (*KVWatermark, error) {
	if kv == nil {
		return nil, fault.Invalidf("eventing.NewKVWatermark", "kv is required")
	}
	return &KVWatermark{kv: kv}, nil
}

// MemWatermark is an in-process Watermark driver (ADR-0119): a mutex-guarded map of cursors. It is the
// non-durable sibling of KVWatermark — used by tests and any deployment that does not need cross-restart
// dedup. Held to the same Watermark contract.
type MemWatermark struct {
	mu      sync.Mutex
	cursors map[string]Cursor
}

// NewMemWatermark builds an empty in-memory Watermark driver.
func NewMemWatermark() *MemWatermark {
	return &MemWatermark{cursors: map[string]Cursor{}}
}

// Load returns the stored Cursor for (ns, source, event); an absent key is the zero Cursor.
func (m *MemWatermark) Load(_ context.Context, ns v1.NamespaceName, source, event v1.ObjectName) (Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursors[watermarkKey(ns, source, event)], nil
}

// Save stores the Cursor for (ns, source, event) (a defensive copy of the slice).
func (m *MemWatermark) Save(_ context.Context, ns v1.NamespaceName, source, event v1.ObjectName, c Cursor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.KeysAtMax = append([]string(nil), c.KeysAtMax...)
	m.cursors[watermarkKey(ns, source, event)] = c
	return nil
}

// watermarkKey is the stable KV key for one named event's cursor.
func watermarkKey(ns v1.NamespaceName, source, event v1.ObjectName) string {
	return kvWatermarkPrefix + string(ns) + "/" + string(source) + "/" + string(event)
}

// Load returns the persisted Cursor for (ns, source, event); a missing record is the zero Cursor (no error).
func (w *KVWatermark) Load(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName) (Cursor, error) {
	raw, found, err := w.kv.Get(ctx, watermarkKey(ns, source, event))
	if err != nil {
		return Cursor{}, fault.Wrapf(err, fault.KindOf(err), "eventing.Watermark.Load", "get cursor %s/%s/%s", ns, source, event)
	}
	if !found {
		return Cursor{}, nil
	}
	var c Cursor
	if uerr := json.Unmarshal(raw, &c); uerr != nil {
		return Cursor{}, fault.Internalf("eventing.Watermark.Load", "decode cursor %s/%s/%s: %v", ns, source, event, uerr)
	}
	return c, nil
}

// Save persists the advanced Cursor for (ns, source, event), overwriting the prior record.
func (w *KVWatermark) Save(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName, c Cursor) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return fault.Internalf("eventing.Watermark.Save", "encode cursor %s/%s/%s: %v", ns, source, event, err)
	}
	if perr := w.kv.Put(ctx, watermarkKey(ns, source, event), raw); perr != nil {
		return fault.Wrapf(perr, fault.KindOf(perr), "eventing.Watermark.Save", "put cursor %s/%s/%s", ns, source, event)
	}
	return nil
}
