// Package snapshotcontract is the shared conformance suite for snapshot.Source and snapshot.Loader (ADR-0202):
// every platform store runs it, on disk and in memory, so each one snapshots in one read and loads back alike.
package snapshotcontract

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// minSnapshots is how many snapshots the one-read case takes while a writer commits.
const minSnapshots = 200

// Subject is one store under test, reached through its own port.
type Subject interface {
	snapshot.Source
	snapshot.Loader
	// Set commits name=n through the store's port, in a transaction of its own.
	Set(ctx context.Context, name string, n int) error
	// Value returns the name and n of a record Set wrote; ok is false for any other record.
	Value(r snapshot.Record) (name string, n int, ok bool)
}

// Run runs the suite. newSubject returns an empty store each call; its Load must work until its first Set.
func Run(t *testing.T, newSubject func(t *testing.T) Subject) {
	t.Helper()
	t.Run("snapshot-is-one-read", func(t *testing.T) { testOneRead(t, newSubject(t)) })
	t.Run("snapshot-loads-back", func(t *testing.T) { testLoadsBack(t, newSubject) })
	t.Run("load-refuses-a-non-empty-store", func(t *testing.T) { testLoadRefusesNonEmpty(t, newSubject) })
	t.Run("emit-error-ends-the-snapshot", func(t *testing.T) { testEmitErrorEnds(t, newSubject(t)) })
	t.Run("next-error-ends-the-load", func(t *testing.T) { testNextErrorEnds(t, newSubject(t)) })
}

// testOneRead: a writer commits a=i, then b=i in a second transaction; a snapshot that read b after a's
// commit of the same round, or later, would hold b above a.
func testOneRead(t *testing.T, s Subject) {
	ctx, cancel := context.WithCancel(context.Background())
	var (
		wg       sync.WaitGroup
		writeErr error
		started  = make(chan struct{})
		stopped  = make(chan struct{})
	)
	defer func() { cancel(); wg.Wait() }()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stopped)
		for i := 1; ctx.Err() == nil; i++ {
			if writeErr = s.Set(ctx, "a", i); writeErr != nil {
				return
			}
			if writeErr = s.Set(ctx, "b", i); writeErr != nil {
				return
			}
			if i == 2 {
				close(started)
			}
		}
	}()
	select {
	case <-started:
	case <-stopped:
		t.Fatalf("writer: %v", writeErr)
	}
	for range minSnapshots {
		if values, _ := collect(t, s); values["b"] > values["a"] {
			t.Fatalf("a snapshot holds b=%d above a=%d: it was not one read", values["b"], values["a"])
		}
	}
	cancel()
	wg.Wait()
	if writeErr != nil && !errors.Is(writeErr, context.Canceled) {
		t.Fatalf("writer: %v", writeErr)
	}
}

func testLoadsBack(t *testing.T, newSubject func(t *testing.T) Subject) {
	ctx := context.Background()
	src := newSubject(t)
	for i, name := range []string{"a", "b", "c"} {
		if err := src.Set(ctx, name, i+1); err != nil {
			t.Fatalf("Set %s: %v", name, err)
		}
	}
	_, want := collect(t, src)
	dst := newSubject(t)
	if err := dst.Load(ctx, Feed(want)); err != nil {
		t.Fatalf("Load into an empty store: %v", err)
	}
	values, got := collect(t, dst)
	requireSameRecords(t, want, got)
	if values["a"] != 1 || values["b"] != 2 || values["c"] != 3 {
		t.Fatalf("loaded values %v, want a=1 b=2 c=3", values)
	}
}

func testLoadRefusesNonEmpty(t *testing.T, newSubject func(t *testing.T) Subject) {
	ctx := context.Background()
	src := newSubject(t)
	if err := src.Set(ctx, "a", 1); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_, recs := collect(t, src)
	dst := newSubject(t)
	if err := dst.Set(ctx, "z", 9); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_, before := collect(t, dst)
	if err := dst.Load(ctx, Feed(recs)); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("Load into a non-empty store: %v, want a conflict", err)
	}
	_, after := collect(t, dst)
	requireSameRecords(t, before, after)
}

func testEmitErrorEnds(t *testing.T, s Subject) {
	ctx := context.Background()
	for i, name := range []string{"a", "b", "c"} {
		if err := s.Set(ctx, name, i+1); err != nil {
			t.Fatalf("Set %s: %v", name, err)
		}
	}
	stop := errors.New("stop")
	var emits int
	_, err := s.Snapshot(ctx, func(snapshot.Record) error {
		emits++
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("Snapshot returned %v, want the emit error", err)
	}
	if emits != 1 {
		t.Fatalf("Snapshot emitted %d records after an emit error, want 1", emits)
	}
}

func testNextErrorEnds(t *testing.T, s Subject) {
	broken := errors.New("broken stream")
	err := s.Load(context.Background(), func() (snapshot.Record, error) { return snapshot.Record{}, broken })
	if !errors.Is(err, broken) {
		t.Fatalf("Load returned %v, want the stream's error", err)
	}
}

// Feed returns a Loader's next over recs.
func Feed(recs []snapshot.Record) func() (snapshot.Record, error) {
	i := 0
	return func() (snapshot.Record, error) {
		if i == len(recs) {
			return snapshot.Record{}, io.EOF
		}
		i++
		return recs[i-1], nil
	}
}

// collect snapshots s, checks the key order, and returns the Set values it holds and every record.
func collect(t *testing.T, s Subject) (map[string]int, []snapshot.Record) {
	t.Helper()
	values := map[string]int{}
	var recs []snapshot.Record
	if _, err := s.Snapshot(context.Background(), func(r snapshot.Record) error {
		if n := len(recs); n > 0 && bytes.Compare(recs[n-1].Key, r.Key) >= 0 {
			t.Errorf("key %q follows %q: not in key order", r.Key, recs[n-1].Key)
		}
		recs = append(recs, r)
		if name, n, ok := s.Value(r); ok {
			values[name] = n
		}
		return nil
	}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return values, recs
}

func requireSameRecords(t *testing.T, want, got []snapshot.Record) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d records, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i].Key, want[i].Key) || !bytes.Equal(got[i].Value, want[i].Value) {
			t.Fatalf("record %d is %q=%q, want %q=%q", i, got[i].Key, got[i].Value, want[i].Key, want[i].Value)
		}
	}
}
