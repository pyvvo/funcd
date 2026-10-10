// Package eventstore is eventing's one durable store (ADR-0201): one Badger instance holding the Sensor's dead
// letters under dl/<ns>/<ulid> (ADR-0118) and the BlobWatcher's seen lists under seen/ (ADR-0157), each tenant
// iterating only its own prefix. With a directory both tenants survive a restart; in memory both are lost, and the
// seen lists live in a MemWatermark. The store is one ADR-0202 snapshot unit: Snapshot reads both tenants at once.
package eventstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	dlbadger "github.com/pyvvo/funcd/internal/eventing/deadletter/badger"
	"github.com/pyvvo/funcd/internal/snapshot"
	snapshotbadger "github.com/pyvvo/funcd/internal/snapshot/badger"
)

const op = "eventstore"

// seenPrefix holds the seen lists: a head <ns>/<source>/<event> = {"gen","parts"}, and the parts of its JSON at
// <head>/<gen %016x>/<part %04x>.
const seenPrefix = "seen/"

// Each part stays below Badger's 1 MiB value threshold, so it lives in the LSM, whose compactions reclaim the
// rewrites (Decision 5); a record over maxRecord fails, as in ADR-0157.
const partSize, maxRecord, gcInterval = 512 << 10, 64 << 20, 5 * time.Minute

// Config opens the store in memory (InMemory) or in an on-disk directory (Dir).
type Config = dlbadger.Config

// Store is the event store; DeadLetters and SeenLists are its two tenants.
type Store struct {
	db   *badger.DB
	seen eventing.Watermark
	stop chan struct{}
	wg   sync.WaitGroup
}

// Open opens the event store with the dead-letter profile, and runs the value-log GC on disk.
func Open(cfg Config) (*Store, error) {
	db, err := badger.Open(dlbadger.Options(cfg))
	if err != nil {
		return nil, fault.Internalf(op, "open the event store (inMemory=%v): %v", cfg.InMemory, err)
	}
	s := &Store{db: db, stop: make(chan struct{})}
	if cfg.InMemory {
		s.seen = eventing.NewMemWatermark()
		return s, nil
	}
	s.seen = &seenLists{db: db}
	s.wg.Add(1)
	go s.gcLoop()
	return s, nil
}

// DeadLetters is the dl/ tenant; its Close is a no-op, the store's Close closes the instance.
func (s *Store) DeadLetters() deadletter.Store { return dlbadger.NewOnDB(s.db) }

// SeenLists is the seen/ tenant: heads and parts on disk, a MemWatermark in memory.
func (s *Store) SeenLists() eventing.Watermark { return s.seen }

// Close stops the GC loop, then closes the instance; the BlobWatcher and the Sensor must have stopped.
func (s *Store) Close() error {
	close(s.stop)
	s.wg.Wait()
	if err := s.db.Close(); err != nil {
		return fault.Internalf(op, "close the event store: %v", err)
	}
	return nil
}

// gcLoop reclaims value-log space, which only dead letters of 1 MiB or more use.
func (s *Store) gcLoop() {
	defer s.wg.Done()
	t := time.NewTicker(gcInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			for s.db.RunValueLogGC(0.5) == nil { //nolint:revive // reclaim while a rewrite is pending
			}
		}
	}
}

// Snapshot emits both tenants in key order and returns "" (ADR-0202 Source). On disk it is one View; in memory the
// seen lists are copied under the MemWatermark's lock first, then follow the View's dead letters as gen-1 heads and
// parts, the layout a Load on disk reads back.
func (s *Store) Snapshot(ctx context.Context, emit func(snapshot.Record) error) (string, error) {
	var seen []snapshot.Record
	if mem, ok := s.seen.(*eventing.MemWatermark); ok {
		var err error
		if seen, err = memRecords(mem); err != nil {
			return "", err
		}
	}
	if err := snapshotbadger.Snapshot(ctx, s.db, emit); err != nil {
		return "", err
	}
	for _, r := range seen {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := emit(r); err != nil {
			return "", err
		}
	}
	return "", nil
}

// Load fills an empty store from a snapshot, before its views are used (ADR-0202 Loader); a store holding a dead
// letter or a seen list gets fault.Conflict before any write. In memory the seen/ records go to the MemWatermark.
func (s *Store) Load(ctx context.Context, next func() (snapshot.Record, error)) error {
	mem, ok := s.seen.(*eventing.MemWatermark)
	if !ok {
		return snapshotbadger.Load(ctx, s.db, next, nil)
	}
	srcs, err := mem.ListSources(ctx)
	if err != nil {
		return err
	}
	if len(srcs) > 0 {
		return fault.Conflictf(op, "the event store is not empty")
	}
	staged := map[string][]byte{}
	deadLetters := func() (snapshot.Record, error) {
		for {
			r, err := next()
			if err != nil || !bytes.HasPrefix(r.Key, []byte(seenPrefix)) {
				return r, err
			}
			staged[string(r.Key)] = bytes.Clone(r.Value)
		}
	}
	if err := snapshotbadger.Load(ctx, s.db, deadLetters, nil); err != nil {
		return err
	}
	return loadStaged(ctx, mem, staged)
}

// memRecords lays the MemWatermark's lists out as gen-1 heads and parts, in key order.
func memRecords(mem *eventing.MemWatermark) ([]snapshot.Record, error) {
	type list struct {
		key []byte
		s   eventing.SeenList
	}
	var lists []list
	mem.Range(func(ns v1.NamespaceName, source, event v1.ObjectName, s eventing.SeenList) {
		lists = append(lists, list{key: headKey(ns, source, event), s: s})
	})
	var out []snapshot.Record
	for _, l := range lists {
		raw, err := json.Marshal(l.s)
		if err != nil {
			return nil, fault.Internalf(op, "encode seen list %s: %v", l.key, err)
		}
		chunks := split(raw)
		hv, err := json.Marshal(head{Gen: 1, Parts: len(chunks)})
		if err != nil {
			return nil, fault.Internalf(op, "encode head %s: %v", l.key, err)
		}
		out = append(out, snapshot.Record{Key: l.key, Value: hv})
		for i, c := range chunks {
			out = append(out, snapshot.Record{Key: partKey(l.key, 1, i), Value: c})
		}
	}
	slices.SortFunc(out, func(a, b snapshot.Record) int { return bytes.Compare(a.Key, b.Key) })
	return out, nil
}

// loadStaged saves each staged head's list into mem; parts with no head are left out.
func loadStaged(ctx context.Context, mem *eventing.MemWatermark, staged map[string][]byte) error {
	for key, val := range staged {
		rest := strings.TrimPrefix(key, seenPrefix)
		if strings.Count(rest, "/") != 2 {
			continue
		}
		var h head
		if err := json.Unmarshal(val, &h); err != nil {
			return fault.Invalidf(op, "decode head %s: %v", key, err)
		}
		var raw []byte
		for i := range h.Parts {
			part, ok := staged[string(partKey([]byte(key), h.Gen, i))]
			if !ok {
				return fault.Invalidf(op, "seen list %s misses part %d of generation %d", key, i, h.Gen)
			}
			raw = append(raw, part...)
		}
		var s eventing.SeenList
		if err := json.Unmarshal(raw, &s); err != nil {
			return fault.Invalidf(op, "decode seen list %s: %v", key, err)
		}
		names := strings.Split(rest, "/")
		if err := mem.Save(ctx, v1.NamespaceName(names[0]), v1.ObjectName(names[1]), v1.ObjectName(names[2]), s); err != nil {
			return err
		}
	}
	return nil
}

// head names the generation whose parts hold a seen list's JSON.
type head struct {
	Gen   uint64 `json:"gen"`
	Parts int    `json:"parts"`
}

func headKey(ns v1.NamespaceName, source, event v1.ObjectName) []byte {
	return []byte(seenPrefix + string(ns) + "/" + string(source) + "/" + string(event))
}

func partKey(head []byte, gen uint64, part int) []byte {
	return fmt.Appendf(bytes.Clone(head), "/%016x/%04x", gen, part)
}

// split cuts raw into partSize chunks.
func split(raw []byte) [][]byte {
	var out [][]byte
	for len(raw) > partSize {
		out = append(out, raw[:partSize])
		raw = raw[partSize:]
	}
	return append(out, raw)
}

// seenLists is the on-disk seen/ tenant. mu orders Saves and Deletes, so two writes never share a generation.
type seenLists struct {
	db *badger.DB
	mu sync.Mutex
}

// Load reads the head and its parts in one View; no head loads an empty list with a non-nil Seen.
func (l *seenLists) Load(_ context.Context, ns v1.NamespaceName, source, event v1.ObjectName) (eventing.SeenList, error) {
	key := headKey(ns, source, event)
	var raw []byte
	err := l.db.View(func(txn *badger.Txn) error {
		h, found, err := readHead(txn, key)
		if err != nil || !found {
			return err
		}
		for i := range h.Parts {
			item, err := txn.Get(partKey(key, h.Gen, i))
			if err != nil {
				return fmt.Errorf("part %d of generation %d: %w", i, h.Gen, err)
			}
			if err := item.Value(func(v []byte) error { raw = append(raw, v...); return nil }); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return eventing.SeenList{}, fault.Internalf(op, "load seen list %s: %v", key, err)
	}
	var s eventing.SeenList
	if raw != nil {
		if err := json.Unmarshal(raw, &s); err != nil {
			return eventing.SeenList{}, fault.Internalf(op, "decode seen list %s: %v", key, err)
		}
	}
	if s.Seen == nil {
		s.Seen = map[string]string{}
	}
	return s, nil
}

// Save writes the next generation's parts, then sets the head and deletes every other key of the event in one
// transaction, so a stopped Save leaves the previous list and the next Save removes what it left.
func (l *seenLists) Save(_ context.Context, ns v1.NamespaceName, source, event v1.ObjectName, s eventing.SeenList) error {
	key := headKey(ns, source, event)
	raw, err := json.Marshal(s)
	if err != nil {
		return fault.Internalf(op, "encode seen list %s: %v", key, err)
	}
	if len(raw) > maxRecord {
		return fault.Invalidf(op, "seen list %s is %d bytes, over the %d-byte limit", key, len(raw), maxRecord)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var gen uint64
	if err := l.db.View(func(txn *badger.Txn) error {
		h, _, err := readHead(txn, key)
		gen = h.Gen + 1
		return err
	}); err != nil {
		return fault.Internalf(op, "read head %s: %v", key, err)
	}
	chunks := split(raw)
	wrote := make(map[string]bool, len(chunks))
	wb := l.db.NewWriteBatch()
	defer wb.Cancel()
	for i, c := range chunks {
		pk := partKey(key, gen, i)
		wrote[string(pk)] = true
		if err := wb.Set(pk, c); err != nil {
			return fault.Internalf(op, "write part %d of %s: %v", i, key, err)
		}
	}
	if err := wb.Flush(); err != nil {
		return fault.Internalf(op, "write the parts of %s: %v", key, err)
	}
	hv, err := json.Marshal(head{Gen: gen, Parts: len(chunks)})
	if err != nil {
		return fault.Internalf(op, "encode head %s: %v", key, err)
	}
	if err := l.db.Update(func(txn *badger.Txn) error {
		for _, k := range keysUnder(txn, append(bytes.Clone(key), '/')) {
			if !wrote[string(k)] {
				if err := txn.Delete(k); err != nil {
					return err
				}
			}
		}
		return txn.Set(key, hv)
	}); err != nil {
		return fault.Internalf(op, "write head %s: %v", key, err)
	}
	return nil
}

// Delete removes every head and part of one source in one transaction.
func (l *seenLists) Delete(_ context.Context, ns v1.NamespaceName, source v1.ObjectName) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	prefix := []byte(seenPrefix + string(ns) + "/" + string(source) + "/")
	if err := l.db.Update(func(txn *badger.Txn) error {
		for _, k := range keysUnder(txn, prefix) {
			if err := txn.Delete(k); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fault.Internalf(op, "delete the seen lists of %s/%s: %v", ns, source, err)
	}
	return nil
}

// ListSources is one key-only iteration over seen/, reduced to distinct <ns>/<source> pairs in key order.
func (l *seenLists) ListSources(_ context.Context) ([]eventing.SourceRef, error) {
	var out []eventing.SourceRef
	err := l.db.View(func(txn *badger.Txn) error {
		for _, k := range keysUnder(txn, []byte(seenPrefix)) {
			names := strings.SplitN(strings.TrimPrefix(string(k), seenPrefix), "/", 3)
			if len(names) != 3 {
				continue
			}
			ref := eventing.SourceRef{Namespace: v1.NamespaceName(names[0]), Name: v1.ObjectName(names[1])}
			if n := len(out); n == 0 || out[n-1] != ref { // a source's keys share a prefix, so they are adjacent
				out = append(out, ref)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fault.Internalf(op, "list the seen-list sources: %v", err)
	}
	return out, nil
}

func readHead(txn *badger.Txn, key []byte) (head, bool, error) {
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return head{}, false, nil
	}
	if err != nil {
		return head{}, false, err
	}
	var h head
	if err := item.Value(func(v []byte) error { return json.Unmarshal(v, &h) }); err != nil {
		return head{}, false, fmt.Errorf("decode head: %w", err)
	}
	return h, true, nil
}

// keysUnder returns a copy of every key under prefix, from a key-only iterator.
func keysUnder(txn *badger.Txn, prefix []byte) [][]byte {
	opts := badger.DefaultIteratorOptions
	opts.PrefetchValues = false
	opts.Prefix = prefix
	it := txn.NewIterator(opts)
	defer it.Close()
	var out [][]byte
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		out = append(out, it.Item().KeyCopy(nil))
	}
	return out
}
