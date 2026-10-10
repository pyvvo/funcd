package eventstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/snapshot/snapshotcontract"
)

// modes are the two ways the store opens: on disk and in memory.
func modes() map[string]func(t *testing.T) Config {
	return map[string]func(t *testing.T) Config{
		"on disk":   func(t *testing.T) Config { return Config{Dir: t.TempDir()} },
		"in memory": func(*testing.T) Config { return Config{InMemory: true} },
	}
}

func open(t *testing.T, cfg Config) *Store {
	t.Helper()
	s, err := Open(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestWatermarkContract(t *testing.T) {
	for name, cfg := range modes() {
		t.Run(name, func(t *testing.T) {
			eventing.WatermarkContract(t, func(t *testing.T) eventing.Watermark { return open(t, cfg(t)).SeenLists() })
		})
	}
}

// TestSnapshotContract: the event store snapshots in one read and loads back, on disk and in memory (ADR-0202).
func TestSnapshotContract(t *testing.T) {
	for name, cfg := range modes() {
		t.Run(name, func(t *testing.T) {
			snapshotcontract.Run(t, func(t *testing.T) snapshotcontract.Subject { return dlSubject{open(t, cfg(t))} })
		})
	}
}

// dlSubject is an event store for snapshotcontract.Run: Set puts dead letter <name> with n attempts.
type dlSubject struct{ *Store }

func (s dlSubject) Set(ctx context.Context, name string, n int) error {
	return s.DeadLetters().Put(ctx, deadletter.DeadLetter{ID: name, Namespace: "ns", Attempts: n})
}

func (dlSubject) Value(r snapshot.Record) (string, int, bool) {
	if !bytes.HasPrefix(r.Key, []byte("dl/")) {
		return "", 0, false
	}
	var dl deadletter.DeadLetter
	if err := json.Unmarshal(r.Value, &dl); err != nil {
		return "", 0, false
	}
	return dl.ID, dl.Attempts, true
}

// TestSnapshotLoadsSeenLists: both tenants of a snapshot, in key order, load back into a store on disk or in
// memory, so SeenLists().Load returns the loaded list.
func TestSnapshotLoadsSeenLists(t *testing.T) {
	ctx := context.Background()
	lists := map[v1.ObjectName]eventing.SeenList{
		"ev":   {Bucket: "raw", Prefix: "drop/", UID: "u", Seen: map[string]string{"drop/a": "1-1"}},
		"ev-2": eventing.LargeSeenList(3 * partSize),
	}
	for from, fromCfg := range modes() {
		for to, toCfg := range modes() {
			t.Run(from+" to "+to, func(t *testing.T) {
				src := open(t, fromCfg(t))
				require.NoError(t, src.DeadLetters().Put(ctx, deadletter.DeadLetter{ID: "01A", Namespace: "lake", Attempts: 3}))
				for ev, l := range lists {
					require.NoError(t, src.SeenLists().Save(ctx, "lake", "drops", ev, eventing.SeenList{Bucket: "old"}))
					require.NoError(t, src.SeenLists().Save(ctx, "lake", "drops", ev, l))
				}
				var recs []snapshot.Record
				_, err := src.Snapshot(ctx, func(r snapshot.Record) error {
					if n := len(recs); n > 0 {
						require.Negative(t, bytes.Compare(recs[n-1].Key, r.Key), "%q follows %q", r.Key, recs[n-1].Key)
					}
					recs = append(recs, r)
					return nil
				})
				require.NoError(t, err)

				dst := open(t, toCfg(t))
				require.NoError(t, dst.Load(ctx, snapshotcontract.Feed(recs)))
				for ev, want := range lists {
					got, err := dst.SeenLists().Load(ctx, "lake", "drops", ev)
					require.NoError(t, err)
					require.Equal(t, want, got)
				}
				dl, err := dst.DeadLetters().Get(ctx, "lake", "01A")
				require.NoError(t, err)
				require.Equal(t, 3, dl.Attempts)
			})
		}
	}
}

func TestLoadRefusesAStoreHoldingASeenList(t *testing.T) {
	ctx := context.Background()
	for name, cfg := range modes() {
		t.Run(name, func(t *testing.T) {
			s := open(t, cfg(t))
			require.NoError(t, s.SeenLists().Save(ctx, "lake", "drops", "arrived", eventing.SeenList{Bucket: "raw"}))
			err := s.Load(ctx, snapshotcontract.Feed(nil))
			require.Equal(t, fault.Conflict, fault.KindOf(err), "Load into a store with a seen list: %v", err)
		})
	}
}

// TestSeenListParts: parts with no head are ignored by Load and gone after the next Save; a list over maxRecord
// fails and leaves the previous one.
func TestSeenListParts(t *testing.T) {
	ctx := context.Background()
	s := open(t, Config{Dir: t.TempDir()})
	key := headKey("lake", "drops", "arrived")
	require.NoError(t, s.db.Update(func(txn *badger.Txn) error {
		for _, k := range [][]byte{partKey(key, 1, 0), partKey(key, 1, 9), partKey(key, 7, 0)} {
			if err := txn.Set(k, []byte(`{"bucket":"stray"}`)); err != nil {
				return err
			}
		}
		return nil
	}))
	got, err := s.SeenLists().Load(ctx, "lake", "drops", "arrived")
	require.NoError(t, err)
	require.Equal(t, eventing.SeenList{Seen: map[string]string{}}, got, "parts with no head are ignored")

	want := eventing.LargeSeenList(partSize + 1)
	require.NoError(t, s.SeenLists().Save(ctx, "lake", "drops", "arrived", want))
	require.Equal(t, [][]byte{key, partKey(key, 1, 0), partKey(key, 1, 1)}, keys(t, s, key), "the Save removed every stray part")

	huge := eventing.SeenList{Bucket: "raw", Seen: map[string]string{"drop/a": strings.Repeat("x", maxRecord)}}
	err = s.SeenLists().Save(ctx, "lake", "drops", "arrived", huge)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a list over maxRecord: %v", err)
	got, err = s.SeenLists().Load(ctx, "lake", "drops", "arrived")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

// scenario: tenants-stay-apart — the retention sweep evicts only dead letters, and deleting the source removes its
// seen lists but no dead letter.
func TestScenarioTenantsStayApart(t *testing.T) {
	ctx := context.Background()
	for name, cfg := range modes() {
		t.Run(name, func(t *testing.T) {
			s := open(t, cfg(t))
			dlq, seen := s.DeadLetters(), s.SeenLists()
			now := time.Now()
			for i, age := range []time.Duration{3 * time.Hour, 2 * time.Hour, 0, 0, 0} {
				require.NoError(t, dlq.Put(ctx, deadletter.DeadLetter{
					ID: "01" + string(rune('A'+i)), Namespace: "lake", Source: "drops", Event: "arrived", FailedAt: v1.NewTimestamp(now.Add(-age)),
				}))
			}
			lists := map[v1.ObjectName]eventing.SeenList{
				"arrived": eventing.LargeSeenList(2 * partSize),
				"other":   {Bucket: "raw", Seen: map[string]string{"drop/a": "1-1"}},
			}
			for ev, l := range lists {
				require.NoError(t, seen.Save(ctx, "lake", "drops", ev, l))
			}

			evicted, err := dlq.SweepExpired(ctx, time.Hour, 2)
			require.NoError(t, err)
			require.Equal(t, 3, evicted, "two past the TTL and one over the cap")
			for ev, want := range lists {
				got, err := seen.Load(ctx, "lake", "drops", ev)
				require.NoError(t, err)
				require.Equal(t, want, got, "the sweep leaves seen list %s unchanged", ev)
			}

			require.NoError(t, seen.Delete(ctx, "lake", "drops"))
			srcs, err := seen.ListSources(ctx)
			require.NoError(t, err)
			require.Empty(t, srcs, "the delete removes the seen lists")
			left, err := dlq.List(ctx, "lake")
			require.NoError(t, err)
			require.Len(t, left, 2, "the delete keeps every remaining dead letter")
			_, err = s.Snapshot(ctx, func(r snapshot.Record) error {
				require.True(t, bytes.HasPrefix(r.Key, []byte("dl/")), "key %q is left", r.Key)
				return nil
			})
			require.NoError(t, err)
		})
	}
}

// scenario: rewrites-do-not-grow-disk — 400 saves of a 2 MiB list, with no restart and no Flatten, keep the event
// store's files under 384 MiB of disk blocks.
func TestScenarioRewritesDoNotGrowDisk(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, Config{Dir: dir})
	list := eventing.SeenList{Bucket: "raw", Prefix: "drop/", UID: "uid-1", Seen: map[string]string{}}
	for i := range 64 { // few large entries: the JSON encoding of a 2 MiB map of small ones dominates the run
		list.Seen[fmt.Sprintf("drop/%02d", i)] = strings.Repeat("v", 32<<10)
	}
	for i := 1; i <= 400; i++ {
		list.Seen["drop/rewrite"] = time.Unix(int64(i), 0).String()
		require.NoError(t, s.SeenLists().Save(ctx, "lake", "drops", "arrived", list))
		if i%25 == 0 {
			require.Less(t, diskBlocks(t, dir), int64(384<<20), "disk blocks after %d saves", i)
		}
	}
	got, err := s.SeenLists().Load(ctx, "lake", "drops", "arrived")
	require.NoError(t, err)
	require.Equal(t, list, got)
}

func diskBlocks(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	require.NoError(t, filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Sys().(*syscall.Stat_t).Blocks * 512
		return nil
	}))
	return total
}

func keys(t *testing.T, s *Store, prefix []byte) [][]byte {
	t.Helper()
	var out [][]byte
	require.NoError(t, s.db.View(func(txn *badger.Txn) error {
		out = keysUnder(txn, prefix)
		return nil
	}))
	return out
}

// fires runs a BlobWatcher over marks for source lake/drops, event arrived on raw/drop/, while objs are listed,
// and returns the keys it fired after three polls.
func fires(t *testing.T, marks eventing.Watermark, objs ...string) []string {
	t.Helper()
	l := &lister{}
	for i, k := range objs {
		l.objs = append(l.objs, blob.Attributes{Key: k, Size: int64(i + 1), ModTime: time.Unix(1759536000, 0).UTC()})
	}
	p := &publisher{}
	w, err := eventing.NewBlobWatcher(l, p, marks, 5*time.Millisecond, nil)
	require.NoError(t, err)
	w.Register("lake", "drops", "uid-1", &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "arrived", Prefix: "drop/"}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool { return l.count() >= 3 }, 5*time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.keys
}

type lister struct {
	mu    sync.Mutex
	objs  []blob.Attributes
	polls int
}

func (l *lister) List(context.Context, v1.NamespaceName, v1.ObjectName, string) ([]blob.Attributes, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.polls++
	return l.objs, nil
}

func (l *lister) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.polls
}

type publisher struct {
	mu   sync.Mutex
	keys []string
}

func (p *publisher) Publish(_ context.Context, ev eventing.CloudEvent) error {
	var d eventing.BlobEventData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys = append(p.keys, d.Key)
	return nil
}
