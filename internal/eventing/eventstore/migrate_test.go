package eventstore

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvbadger "github.com/pyvvo/funcd/internal/kvstore/badger"
)

const legacyKey = legacyPrefix + "lake/drops/arrived"

// previousRelease returns a durable KV holding the ADR-0157 seen list a watcher saved after firing drop/a.
func previousRelease(t *testing.T) kvstore.KV {
	t.Helper()
	prev := eventing.NewMemWatermark()
	require.Equal(t, []string{"drop/a"}, fires(t, prev, "drop/a"))
	rec, err := prev.Load(context.Background(), "lake", "drops", "arrived")
	require.NoError(t, err)
	raw, err := json.Marshal(rec)
	require.NoError(t, err)
	kv, err := kvbadger.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	require.NoError(t, kv.Put(context.Background(), legacyKey, raw))
	return kv
}

func legacyKeys(t *testing.T, kv kvstore.KV) []string {
	t.Helper()
	keys, err := kv.List(context.Background(), legacyPrefix)
	require.NoError(t, err)
	return keys
}

// scenario: upgrade-moves-seen-lists — the seen list the previous release kept in a badger KV moves into the event
// store: drop/a does not fire, no key is left under _eventing/blobwatch/, and a new drop/b fires once.
func TestScenarioUpgradeMovesSeenLists(t *testing.T) {
	kv := previousRelease(t)
	s := open(t, Config{Dir: t.TempDir()})

	moved, err := MigrateSeenLists(context.Background(), kv, s)
	require.NoError(t, err)
	require.Equal(t, 1, moved)
	require.Empty(t, legacyKeys(t, kv))
	require.Equal(t, []string{"drop/b"}, fires(t, s.SeenLists(), "drop/a", "drop/b"))
	require.Empty(t, fires(t, s.SeenLists(), "drop/a", "drop/b"))
}

// scenario: interrupted-move-resumes — a move that copied and stopped before its deletes, then a newer list the
// watcher saved: the next start fires nothing twice, deletes the KV keys and keeps the newer list.
func TestScenarioInterruptedMoveResumes(t *testing.T) {
	ctx := context.Background()
	kv := previousRelease(t)
	s := open(t, Config{Dir: t.TempDir()})

	moved, err := MigrateSeenLists(ctx, failDelete{kv}, s)
	require.Error(t, err, "the move stops before its deletes")
	require.Equal(t, 1, moved)
	require.Len(t, legacyKeys(t, kv), 1)
	require.Equal(t, []string{"drop/b"}, fires(t, s.SeenLists(), "drop/a", "drop/b"))
	newer, err := s.SeenLists().Load(ctx, "lake", "drops", "arrived")
	require.NoError(t, err)

	moved, err = MigrateSeenLists(ctx, kv, s)
	require.NoError(t, err)
	require.Zero(t, moved, "the event store's list is not overwritten")
	require.Empty(t, legacyKeys(t, kv))
	kept, err := s.SeenLists().Load(ctx, "lake", "drops", "arrived")
	require.NoError(t, err)
	require.Equal(t, newer, kept)
	require.Empty(t, fires(t, s.SeenLists(), "drop/a", "drop/b"))
}

// scenario: move-failure-stops-start — a KV that fails to list _eventing/blobwatch/ fails the move, naming it, and
// the KV records stay.
func TestScenarioMoveFailureStopsStart(t *testing.T) {
	ctx := context.Background()
	kv := previousRelease(t)
	s := open(t, Config{Dir: t.TempDir()})

	_, err := MigrateSeenLists(ctx, failList{kv}, s)
	require.ErrorContains(t, err, "seen-list move")
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	_, found, err := kv.Get(ctx, legacyKey)
	require.NoError(t, err)
	require.True(t, found, "the KV record stays")
	srcs, err := s.SeenLists().ListSources(ctx)
	require.NoError(t, err)
	require.Empty(t, srcs)
}

// TestMigrateSkipsCursorRecords: an ADR-0119 Cursor record (no bucket) is not copied, and is deleted with the rest.
func TestMigrateSkipsCursorRecords(t *testing.T) {
	ctx := context.Background()
	kv := previousRelease(t)
	require.NoError(t, kv.Put(ctx, legacyPrefix+"lake/drops/cursor", []byte(`{"maxModTime":"2026-01-01T00:00:00Z","keysAtMax":["drop/a"]}`)))
	s := open(t, Config{Dir: t.TempDir()})

	moved, err := MigrateSeenLists(ctx, kv, s)
	require.NoError(t, err)
	require.Equal(t, 1, moved)
	cursor, err := s.SeenLists().Load(ctx, "lake", "drops", "cursor")
	require.NoError(t, err)
	require.Empty(t, cursor.Bucket, "the Cursor record is not copied")
	require.Empty(t, legacyKeys(t, kv))
}

type failList struct{ kvstore.KV }

func (failList) List(context.Context, string) ([]string, error) {
	return nil, fault.Unavailablef("failList.List", "kv unavailable")
}

type failDelete struct{ kvstore.KV }

func (failDelete) Delete(context.Context, string) error {
	return fault.Unavailablef("failDelete.Delete", "kv unavailable")
}
