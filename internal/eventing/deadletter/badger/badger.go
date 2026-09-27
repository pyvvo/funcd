// Package badger is the dead-letter-queue deadletter driver (ADR-0118), backed by Badger v4 in a DEDICATED
// instance (never the metastore). It mirrors the ADR-0094 run-state driver: one driver serves every
// backend — New selects Badger's in-memory mode (tests/dev, hermetic — no files) or an on-disk directory
// (<dataDir>/deadletter, production). Key layout is dl/<ns>/<ulid>: the ULID makes a namespace's keys
// time-sortable, so cap eviction takes the oldest cheaply. The store imports NO bus — the DLQ guarantee is
// bus-driver-independent. The shared deadletter.Contract proves memory and this driver behave identically.
package badger

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/options"
	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
)

const op = "deadletter.badger"

const keyPrefix = "dl/"

// Config selects the Badger backend for the DLQ (mirrors internal/workflow/runstate/badger).
type Config struct {
	// InMemory runs Badger fully in memory (tests/dev) — no files, no disk.
	InMemory bool
	// Dir is the on-disk data directory when InMemory is false (production: <dataDir>/deadletter).
	Dir string
}

// store is the Badger-backed deadletter.Store.
type store struct {
	db *badger.DB
}

// New opens a dead-letter store on the configured backend and returns it as deadletter.Store.
func New(cfg Config) (deadletter.Store, error) {
	var bopts badger.Options
	if cfg.InMemory {
		bopts = badger.DefaultOptions("").WithInMemory(true)
	} else {
		// RAM-frugal profile matching the run-state / metastore engines (ADR-0065/0094).
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
		return nil, fault.Internalf(op, "opening dead-letter store (inMemory=%v): %v", cfg.InMemory, err)
	}
	return &store{db: db}, nil
}

func nsPrefix(ns v1.NamespaceName) []byte { return []byte(keyPrefix + string(ns) + "/") }

func recordKey(ns v1.NamespaceName, id string) []byte {
	return []byte(keyPrefix + string(ns) + "/" + id)
}

func (s *store) Put(_ context.Context, dl deadletter.DeadLetter) error {
	if dl.ID == "" {
		return fault.Invalidf(op, "dead letter must have an id")
	}
	b, err := json.Marshal(dl)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "marshalling dead letter %q", dl.ID)
	}
	if err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(recordKey(dl.Namespace, dl.ID), b)
	}); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "putting dead letter %q", dl.ID)
	}
	return nil
}

func (s *store) Get(_ context.Context, ns v1.NamespaceName, id string) (deadletter.DeadLetter, error) {
	var dl deadletter.DeadLetter
	err := s.db.View(func(txn *badger.Txn) error {
		item, gerr := txn.Get(recordKey(ns, id))
		if gerr != nil {
			return gerr
		}
		return item.Value(func(val []byte) error { return json.Unmarshal(val, &dl) })
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return deadletter.DeadLetter{}, fault.NotFoundf(op, "dead letter %q/%q not found", ns, id)
	}
	if err != nil {
		return deadletter.DeadLetter{}, fault.Wrapf(err, fault.Internal, op, "getting dead letter %q/%q", ns, id)
	}
	return dl, nil
}

func (s *store) List(_ context.Context, ns v1.NamespaceName) ([]deadletter.DeadLetter, error) {
	prefix := nsPrefix(ns)
	var out []deadletter.DeadLetter
	err := s.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var dl deadletter.DeadLetter
			if verr := it.Item().Value(func(val []byte) error { return json.Unmarshal(val, &dl) }); verr != nil {
				return verr
			}
			out = append(out, dl)
		}
		return nil
	})
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "listing dead letters in %q", ns)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID }) // newest first (ULID desc)
	return out, nil
}

func (s *store) Delete(_ context.Context, ns v1.NamespaceName, id string) error {
	if err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(recordKey(ns, id))
	}); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "deleting dead letter %q/%q", ns, id)
	}
	return nil
}

// entry is a key + the two fields the sweep needs (FailedAt for the TTL, the key's ns-prefix for the cap).
type entry struct {
	key      []byte
	ns       string
	failedAt time.Time
}

// SweepExpired iterates ALL namespaces once: TTL is global (evict FailedAt older than retention across
// every namespace), the count cap is per-namespace (keep the newest maxPerNS under each dl/<ns>/ prefix,
// evict the oldest over-cap). retention<=0 disables the TTL; maxPerNS<=0 disables the cap.
func (s *store) SweepExpired(_ context.Context, retention time.Duration, maxPerNS int) (int, error) {
	now := time.Now()
	// Collect every entry's key + FailedAt + namespace (iteration is prefix-ordered ⇒ within a ns, ascending
	// ULID = oldest first). Badger keys are copied out of the iterator (they alias the txn's buffer).
	byNS := map[string][]entry{}
	err := s.db.View(func(txn *badger.Txn) error {
		prefix := []byte(keyPrefix)
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var dl deadletter.DeadLetter
			if verr := it.Item().Value(func(val []byte) error { return json.Unmarshal(val, &dl) }); verr != nil {
				return verr
			}
			k := it.Item().KeyCopy(nil)
			byNS[string(dl.Namespace)] = append(byNS[string(dl.Namespace)], entry{key: k, ns: string(dl.Namespace), failedAt: dl.FailedAt})
		}
		return nil
	})
	if err != nil {
		return 0, fault.Wrapf(err, fault.Internal, op, "scanning dead letters for sweep")
	}

	var toDelete [][]byte
	for _, entries := range byNS {
		sort.Slice(entries, func(i, j int) bool { return string(entries[i].key) < string(entries[j].key) }) // oldest first
		deleted := map[int]bool{}
		if retention > 0 { // TTL
			for i, e := range entries {
				if now.Sub(e.failedAt) > retention {
					toDelete = append(toDelete, e.key)
					deleted[i] = true
				}
			}
		}
		if maxPerNS > 0 { // per-ns cap over the survivors
			var survivors []int
			for i := range entries {
				if !deleted[i] {
					survivors = append(survivors, i)
				}
			}
			if len(survivors) > maxPerNS {
				for _, i := range survivors[:len(survivors)-maxPerNS] { // oldest survivors over the cap
					toDelete = append(toDelete, entries[i].key)
				}
			}
		}
	}
	if len(toDelete) == 0 {
		return 0, nil
	}
	if derr := s.db.Update(func(txn *badger.Txn) error {
		for _, k := range toDelete {
			if drr := txn.Delete(k); drr != nil {
				return drr
			}
		}
		return nil
	}); derr != nil {
		return 0, fault.Wrapf(derr, fault.Internal, op, "evicting swept dead letters")
	}
	return len(toDelete), nil
}

func (s *store) Close() error {
	if err := s.db.Close(); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "closing dead-letter store")
	}
	return nil
}
