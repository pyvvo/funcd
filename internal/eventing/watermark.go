package eventing

import (
	"context"
	"maps"
	"sync"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// SeenList is the dedup record of one named blob event (ADR-0157), JSON in the event store (ADR-0201).
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
// ADR-0157). The durable driver is the event store's seen-list view (ADR-0201, internal/eventing/eventstore).
type Watermark interface {
	// Load never returns a nil Seen: a missing record, or one saved with "seen":null, loads with an empty map.
	Load(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName) (SeenList, error)
	Save(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName, s SeenList) error
	// Delete removes every event record of one source; a source with no record is not an error.
	Delete(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) error
	// ListSources returns each source that has at least one record, once (the start sweep, ADR-0157 Decision 8).
	ListSources(ctx context.Context) ([]SourceRef, error)
}

// MemWatermark is the in-process, non-durable Watermark driver: the event store's seen lists in memory (ADR-0201).
type MemWatermark struct {
	mu      sync.Mutex
	records map[eventKey]SeenList
}

// NewMemWatermark builds an empty in-memory Watermark driver.
func NewMemWatermark() *MemWatermark {
	return &MemWatermark{records: map[eventKey]SeenList{}}
}

// Load returns a copy of the stored SeenList for (ns, source, event).
func (m *MemWatermark) Load(_ context.Context, ns v1.NamespaceName, source, event v1.ObjectName) (SeenList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return withSeen(m.records[eventKey{ns: ns, source: source, event: event}]), nil
}

// Save stores a copy of s for (ns, source, event).
func (m *MemWatermark) Save(_ context.Context, ns v1.NamespaceName, source, event v1.ObjectName, s SeenList) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[eventKey{ns: ns, source: source, event: event}] = withSeen(s)
	return nil
}

// Delete removes every event record of one source.
func (m *MemWatermark) Delete(_ context.Context, ns v1.NamespaceName, source v1.ObjectName) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.records {
		if k.ns == ns && k.source == source {
			delete(m.records, k)
		}
	}
	return nil
}

// ListSources returns each source with at least one record, once.
func (m *MemWatermark) ListSources(_ context.Context) ([]SourceRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[SourceRef]bool{}
	var out []SourceRef
	for k := range m.records {
		ref := SourceRef{Namespace: k.ns, Name: k.source}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out, nil
}

// Range calls fn with a copy of every record, in no order, under the lock, so the records are one point in time
// (the event store's snapshot, ADR-0201 Decision 9); fn must not call m.
func (m *MemWatermark) Range(fn func(ns v1.NamespaceName, source, event v1.ObjectName, s SeenList)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.records {
		fn(k.ns, k.source, k.event, withSeen(s))
	}
}

// withSeen returns s with a fresh copy of its Seen map, never nil.
func withSeen(s SeenList) SeenList {
	seen := make(map[string]string, len(s.Seen))
	maps.Copy(seen, s.Seen)
	s.Seen = seen
	return s
}
