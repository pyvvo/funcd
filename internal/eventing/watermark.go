package eventing

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/kvstore"
)

// SeenList is the dedup record of one named blob event (ADR-0157), JSON in kvstore.KV.
type SeenList struct {
	Bucket v1.ObjectName     `json:"bucket"`
	Prefix string            `json:"prefix"`
	UID    v1.UID            `json:"uid"`
	Seen   map[string]string `json:"seen"` // object key → the data.version it fired with (versionOf)
}

// SourceRef names one EventSource that has blob event records.
type SourceRef struct {
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}

// Watermark persists per-event dedup state so a re-list and a restart never re-emit a seen object (ADR-0119,
// ADR-0157). The V1 driver stores one JSON SeenList per (ns, source, event) over internal/kvstore.KV.
type Watermark interface {
	// Load never returns a nil Seen: a missing record, or one saved with "seen":null, loads with an empty map.
	Load(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName) (SeenList, error)
	Save(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName, s SeenList) error
	// Delete removes every event record of one source; a source with no record is not an error.
	Delete(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) error
	// ListSources returns each source that has at least one record, once (the start sweep, ADR-0157 Decision 8).
	ListSources(ctx context.Context) ([]SourceRef, error)
}

// kvWatermarkPrefix namespaces the watermark records under the shared KV substrate so they never collide
// with the function-facing KV key space (<namespace>/<binding>/<key>).
const kvWatermarkPrefix = "_eventing/blobwatch/"

// KVWatermark is the V1 Watermark driver (ADR-0119, ADR-0157): one JSON SeenList per (ns, source, event) over
// the in-tree kvstore.KV substrate — durable get/put with its own backup path, so no second persistence
// mechanism.
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

// MemWatermark is the in-process, non-durable Watermark driver, held to the same contract as KVWatermark.
type MemWatermark struct {
	mu      sync.Mutex
	records map[string]SeenList
}

// NewMemWatermark builds an empty in-memory Watermark driver.
func NewMemWatermark() *MemWatermark {
	return &MemWatermark{records: map[string]SeenList{}}
}

// Load returns a copy of the stored SeenList for (ns, source, event).
func (m *MemWatermark) Load(_ context.Context, ns v1.NamespaceName, source, event v1.ObjectName) (SeenList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return withSeen(m.records[watermarkKey(ns, source, event)]), nil
}

// Save stores a copy of s for (ns, source, event).
func (m *MemWatermark) Save(_ context.Context, ns v1.NamespaceName, source, event v1.ObjectName, s SeenList) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[watermarkKey(ns, source, event)] = withSeen(s)
	return nil
}

// Delete removes every event record of one source.
func (m *MemWatermark) Delete(_ context.Context, ns v1.NamespaceName, source v1.ObjectName) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := sourcePrefix(ns, source)
	for k := range m.records {
		if strings.HasPrefix(k, prefix) {
			delete(m.records, k)
		}
	}
	return nil
}

// ListSources returns each source with at least one record, once.
func (m *MemWatermark) ListSources(_ context.Context) ([]SourceRef, error) {
	m.mu.Lock()
	keys := make([]string, 0, len(m.records))
	for k := range m.records {
		keys = append(keys, k)
	}
	m.mu.Unlock()
	return sourcesOf(keys), nil
}

// withSeen returns s with a fresh copy of its Seen map, never nil.
func withSeen(s SeenList) SeenList {
	seen := make(map[string]string, len(s.Seen))
	maps.Copy(seen, s.Seen)
	s.Seen = seen
	return s
}

// watermarkKey is the stable KV key for one named event's record.
func watermarkKey(ns v1.NamespaceName, source, event v1.ObjectName) string {
	return sourcePrefix(ns, source) + string(event)
}

// sourcePrefix ends in "/" so deleting source drops never matches source drops2.
func sourcePrefix(ns v1.NamespaceName, source v1.ObjectName) string {
	return kvWatermarkPrefix + string(ns) + "/" + string(source) + "/"
}

// sourcesOf reduces record keys to their distinct (namespace, source) pairs, in first-seen order.
func sourcesOf(keys []string) []SourceRef {
	seen := map[SourceRef]bool{}
	var out []SourceRef
	for _, k := range keys {
		parts := strings.SplitN(strings.TrimPrefix(k, kvWatermarkPrefix), "/", 3)
		if len(parts) != 3 {
			continue
		}
		ref := SourceRef{Namespace: v1.NamespaceName(parts[0]), Name: v1.ObjectName(parts[1])}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

// Load returns the persisted SeenList for (ns, source, event); a missing record is an empty one (no error).
func (w *KVWatermark) Load(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName) (SeenList, error) {
	raw, found, err := w.kv.Get(ctx, watermarkKey(ns, source, event))
	if err != nil {
		return SeenList{}, fault.Wrapf(err, fault.KindOf(err), "eventing.Watermark.Load", "get seen list %s/%s/%s", ns, source, event)
	}
	var s SeenList
	if found {
		if uerr := json.Unmarshal(raw, &s); uerr != nil {
			return SeenList{}, fault.Internalf("eventing.Watermark.Load", "decode seen list %s/%s/%s: %v", ns, source, event, uerr)
		}
	}
	if s.Seen == nil {
		s.Seen = map[string]string{}
	}
	return s, nil
}

// Save persists the SeenList for (ns, source, event), overwriting the prior record.
func (w *KVWatermark) Save(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName, s SeenList) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return fault.Internalf("eventing.Watermark.Save", "encode seen list %s/%s/%s: %v", ns, source, event, err)
	}
	if perr := w.kv.Put(ctx, watermarkKey(ns, source, event), raw); perr != nil {
		return fault.Wrapf(perr, fault.KindOf(perr), "eventing.Watermark.Save", "put seen list %s/%s/%s", ns, source, event)
	}
	return nil
}

// Delete removes every event record of one source.
func (w *KVWatermark) Delete(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) error {
	keys, err := w.kv.List(ctx, sourcePrefix(ns, source))
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "eventing.Watermark.Delete", "list records of %s/%s", ns, source)
	}
	for _, k := range keys {
		if derr := w.kv.Delete(ctx, k); derr != nil {
			return fault.Wrapf(derr, fault.KindOf(derr), "eventing.Watermark.Delete", "delete record %s", k)
		}
	}
	return nil
}

// ListSources returns each source with at least one record, once: one List over the record prefix.
func (w *KVWatermark) ListSources(ctx context.Context) ([]SourceRef, error) {
	keys, err := w.kv.List(ctx, kvWatermarkPrefix)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "eventing.Watermark.ListSources", "list records")
	}
	return sourcesOf(keys), nil
}
