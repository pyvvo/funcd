// Package memory is the in-memory runstore driver (ADR-0094): a map guarded by a
// mutex, used by tests and dev. It makes the engine's scenario tests hermetic (no
// Badger files, no disk). Records are deep-copied in and out so callers never share
// storage-owned memory.
package memory

import (
	"context"
	"sync"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstore"
)

const op = "runstore.memory"

// store is the in-memory driver. New returns it as the runstore.Store port.
type store struct {
	mu   sync.RWMutex
	recs map[key]*runstore.Record
}

type key struct {
	ns   v1.NamespaceName
	name v1.ObjectName
}

// New returns an in-memory run store.
func New() runstore.Store {
	return &store{recs: make(map[key]*runstore.Record)}
}

func (s *store) Get(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (*runstore.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.recs[key{ns, name}]
	if !ok {
		return nil, fault.NotFoundf(op, "run %q/%q not found", ns, name)
	}
	return runstore.Clone(rec)
}

func (s *store) Put(_ context.Context, rec *runstore.Record) error {
	if rec == nil || rec.Name == "" {
		return fault.Invalidf(op, "run record must have a name")
	}
	cp, err := runstore.Clone(rec)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "cloning record")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[key{rec.Namespace, rec.Name}] = cp
	return nil
}

func (s *store) Delete(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.recs, key{ns, name})
	return nil
}

func (s *store) List(_ context.Context, opts runstore.ListOptions) ([]*runstore.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*runstore.Record
	for _, rec := range s.recs {
		if opts.Namespace != "" && rec.Namespace != opts.Namespace {
			continue
		}
		if opts.Workflow != "" && rec.Workflow != opts.Workflow {
			continue
		}
		if opts.OpenOnly && rec.Terminal() {
			continue
		}
		cp, err := runstore.Clone(rec)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "cloning record")
		}
		out = append(out, cp)
	}
	return out, nil
}

func (s *store) Close() error { return nil }
