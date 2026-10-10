package badger

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/snapshot/snapshotcontract"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
)

// scenario: runstate-driver-conformance — the Badger driver in in-memory mode
// satisfies the port's shared contract (the same behavior a file backend gives).
func TestBadgerInMemoryContract(t *testing.T) {
	runstate.Contract(t, func(t *testing.T) runstate.Store { return open(t, Config{InMemory: true}) })
}

// The file backend opens at a temp dir and satisfies the same contract (durability
// path), proving memory↔file parity.
func TestBadgerFileContract(t *testing.T) {
	runstate.Contract(t, func(t *testing.T) runstate.Store { return open(t, Config{Dir: t.TempDir()}) })
}

// scenario: snapshot-is-one-read (run state, on disk and in memory) — the snapshot contract: a writer puts run
// a at i, then run b at i, while 200 snapshots run, and none holds b above a; the snapshot loads back into an
// empty run store.
func TestScenarioSnapshotIsOneRead(t *testing.T) {
	for name, cfg := range map[string]func(t *testing.T) Config{
		"on disk":   func(t *testing.T) Config { return Config{Dir: t.TempDir()} },
		"in memory": func(*testing.T) Config { return Config{InMemory: true} },
	} {
		t.Run(name, func(t *testing.T) {
			snapshotcontract.Run(t, func(t *testing.T) snapshotcontract.Subject {
				return runSubject{open(t, cfg(t))}
			})
		})
	}
}

func open(t *testing.T, cfg Config) runstate.Store {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New(%+v): %v", cfg, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// runSubject is a run store for snapshotcontract.Run: Set puts run <name> with n in its input.
type runSubject struct{ runstate.Store }

func (r runSubject) Set(ctx context.Context, name string, n int) error {
	return r.Put(ctx, &runstate.Record{Namespace: "ns", Name: v1.ObjectName(name), Phase: v1.RunRunning, Input: json.RawMessage(strconv.Itoa(n))})
}

func (r runSubject) Value(rec snapshot.Record) (string, int, bool) {
	if !bytes.HasPrefix(rec.Key, []byte(keyPrefix)) {
		return "", 0, false
	}
	var got runstate.Record
	if err := json.Unmarshal(rec.Value, &got); err != nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(string(got.Input))
	return string(got.Name), n, err == nil
}
