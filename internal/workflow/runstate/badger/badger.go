// Package badger is the run-state runstate driver (ADR-0094), backed by Badger v4.
// One driver serves every backend: New selects Badger's in-memory mode (tests/dev,
// hermetic — no files) or an on-disk directory (production durability). The backend
// is passed at New, so swapping memory↔file is a config change, not a code change —
// and the shared runstate.Contract proves both behave identically.
package badger

import (
	"context"
	"encoding/json"
	"errors"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/options"
	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstate"
)

const op = "runstate.badger"

const keyPrefix = "run/"

// Config selects the Badger backend for the run store.
type Config struct {
	// InMemory runs Badger fully in memory (tests/dev) — no files, no disk.
	InMemory bool
	// Dir is the on-disk data directory when InMemory is false (production).
	Dir string
}

// store is the Badger-backed runstate.Store.
type store struct {
	db *badger.DB
}

// New opens a run store on the configured backend and returns it as runstate.Store.
func New(cfg Config) (runstate.Store, error) {
	var bopts badger.Options
	if cfg.InMemory {
		bopts = badger.DefaultOptions("").WithInMemory(true)
	} else {
		// RAM-frugal profile matching the KV/metastore engines (ADR-0065/0066).
		bopts = badger.DefaultOptions(cfg.Dir).
			WithSyncWrites(true).
			WithNumMemtables(2).
			WithMemTableSize(16 << 20).
			WithValueLogFileSize(64 << 20).
			WithBlockCacheSize(16 << 20).
			WithIndexCacheSize(16 << 20).
			WithCompression(options.None)
	}
	bopts = bopts.WithLoggingLevel(badger.ERROR)
	db, err := badger.Open(bopts)
	if err != nil {
		return nil, fault.Internalf(op, "opening run store (inMemory=%v): %v", cfg.InMemory, err)
	}
	return &store{db: db}, nil
}

func recordKey(ns v1.NamespaceName, name v1.ObjectName) []byte {
	return []byte(keyPrefix + string(ns) + "/" + string(name))
}

func (s *store) Get(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (*runstate.Record, error) {
	var rec runstate.Record
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(recordKey(ns, name))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error { return json.Unmarshal(val, &rec) })
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil, fault.NotFoundf(op, "run %q/%q not found", ns, name)
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "getting run %q/%q", ns, name)
	}
	return &rec, nil
}

func (s *store) Put(_ context.Context, rec *runstate.Record) error {
	if rec == nil || rec.Name == "" {
		return fault.Invalidf(op, "run record must have a name")
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "marshalling run %q", rec.Name)
	}
	if err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(recordKey(rec.Namespace, rec.Name), b)
	}); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "putting run %q", rec.Name)
	}
	return nil
}

func (s *store) Delete(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	if err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(recordKey(ns, name))
	}); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "deleting run %q/%q", ns, name)
	}
	return nil
}

func (s *store) List(_ context.Context, opts runstate.ListOptions) ([]*runstate.Record, error) {
	prefix := []byte(keyPrefix)
	if opts.Namespace != "" {
		prefix = []byte(keyPrefix + string(opts.Namespace) + "/")
	}
	var out []*runstate.Record
	err := s.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var rec runstate.Record
			if err := it.Item().Value(func(val []byte) error { return json.Unmarshal(val, &rec) }); err != nil {
				return err
			}
			if opts.Workflow != "" && rec.Workflow != opts.Workflow {
				continue
			}
			if opts.OpenOnly && rec.Terminal() {
				continue
			}
			r := rec
			out = append(out, &r)
		}
		return nil
	})
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "listing runs")
	}
	return out, nil
}

func (s *store) Close() error {
	if err := s.db.Close(); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "closing run store")
	}
	return nil
}
