// Package snapshot is the platform-store snapshot seam (ADR-0202): each platform store (the event store, the
// metastore, the run state) reads its whole content in one read transaction as a Source, and an empty store of
// the same port fills from such a stream as a Loader. Cut reads the three stores in the order that makes the gap
// between them safe; ADR-0203 writes what it emits.
package snapshot

import "context"

// Record is one entry of a store, its key and its latest value as stored.
type Record struct{ Key, Value []byte }

// Source emits every record in key order from ONE read transaction with ONE producer and returns the store's
// resourceVersion at that read ("" for a store without one). An emit error ends it; emit must not call the store.
type Source interface {
	Snapshot(ctx context.Context, emit func(Record) error) (string, error)
}

// Loader writes the records next returns, until io.EOF, in bounded batches; a non-empty store gets fault.Conflict
// before any write. Load is not atomic: after a failed Load the store is unusable and the caller discards it.
type Loader interface {
	Load(ctx context.Context, next func() (Record, error)) error
}

// Cut snapshots events, meta, then runs, back to back, emitting each record with its source, and returns meta's
// resourceVersion; the first error ends it (ADR-0202 Decision 2). A later read sees every commit an earlier one
// saw: a WorkflowRun in the cut has its run record, and an event fired between the reads replays rather than
// being lost.
func Cut(ctx context.Context, events, meta, runs Source, emit func(src Source, r Record) error) (string, error) {
	read := func(src Source) (string, error) {
		return src.Snapshot(ctx, func(r Record) error { return emit(src, r) })
	}
	if _, err := read(events); err != nil {
		return "", err
	}
	rv, err := read(meta)
	if err != nil {
		return "", err
	}
	if _, err := read(runs); err != nil {
		return "", err
	}
	return rv, nil
}
