// Package memory is the in-memory deadletter.Store driver (ADR-0118): a small pure-Go map with no files,
// for cross-platform tests and the InMemory preset. It holds the same behavior the badger driver does —
// both are proven by the shared deadletter.Contract — so a test never needs Badger to exercise the DLQ path.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
)

const op = "deadletter.memory"

// store is the in-memory deadletter.Store: records keyed by namespace then id.
type store struct {
	mu   sync.Mutex
	recs map[v1.NamespaceName]map[string]deadletter.DeadLetter
}

// New builds an in-memory dead-letter store.
func New() deadletter.Store {
	return &store{recs: map[v1.NamespaceName]map[string]deadletter.DeadLetter{}}
}

func (s *store) Put(_ context.Context, dl deadletter.DeadLetter) error {
	if dl.ID == "" {
		return fault.Invalidf(op, "dead letter must have an id")
	}
	cp, err := deadletter.Clone(dl)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "clone dead letter %q", dl.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recs[dl.Namespace] == nil {
		s.recs[dl.Namespace] = map[string]deadletter.DeadLetter{}
	}
	s.recs[dl.Namespace][dl.ID] = cp
	return nil
}

func (s *store) Update(_ context.Context, dl deadletter.DeadLetter) error {
	cp, err := deadletter.Clone(dl)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "clone dead letter %q", dl.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.recs[dl.Namespace][dl.ID]; !ok {
		return fault.NotFoundf(op, "dead letter %q/%q not found", dl.Namespace, dl.ID)
	}
	s.recs[dl.Namespace][dl.ID] = cp
	return nil
}

func (s *store) Get(_ context.Context, ns v1.NamespaceName, id string) (deadletter.DeadLetter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dl, ok := s.recs[ns][id]
	if !ok {
		return deadletter.DeadLetter{}, fault.NotFoundf(op, "dead letter %q/%q not found", ns, id)
	}
	return deadletter.Clone(dl)
}

func (s *store) List(_ context.Context, ns v1.NamespaceName) ([]deadletter.DeadLetter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]deadletter.DeadLetter, 0, len(s.recs[ns]))
	for _, dl := range s.recs[ns] {
		cp, err := deadletter.Clone(dl)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "clone dead letter %q", dl.ID)
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID }) // newest first (ULID desc)
	return out, nil
}

func (s *store) Delete(_ context.Context, ns v1.NamespaceName, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.recs[ns], id)
	return nil
}

func (s *store) SweepExpired(_ context.Context, retention time.Duration, maxPerNS int) (int, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	evicted := 0
	for ns, byID := range s.recs {
		// TTL: global horizon — evict entries older than retention.
		if retention > 0 {
			for id, dl := range byID {
				if now.Sub(dl.FailedAt) > retention {
					delete(byID, id)
					evicted++
				}
			}
		}
		// Cap: per-namespace — keep the newest maxPerNS, evict the oldest over-cap.
		if maxPerNS > 0 && len(byID) > maxPerNS {
			ids := make([]string, 0, len(byID))
			for id := range byID {
				ids = append(ids, id)
			}
			sort.Strings(ids) // ascending ⇒ oldest first (ULID time-sortable)
			for _, id := range ids[:len(byID)-maxPerNS] {
				delete(byID, id)
				evicted++
			}
		}
		if len(byID) == 0 {
			delete(s.recs, ns)
		}
	}
	return evicted, nil
}

func (s *store) Close() error { return nil }
