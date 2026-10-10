package funcd

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvbadger "github.com/pyvvo/funcd/internal/kvstore/badger"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	bstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const legacySeenKey = "_eventing/blobwatch/default/drops/arrived"

func eventingDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// eventingPlatform builds a platform with the given options over an in-memory base and no worker runtime.
func eventingPlatform(t *testing.T, opts ...Option) *Platform {
	t.Helper()
	base := []Option{
		InMemory(), WithLogger(slog.New(slog.DiscardHandler)), WithoutLogCompaction(),
		WithRuntime(&recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}), WithBlobPollInterval(10 * time.Millisecond),
	}
	p, err := New(append(base, opts...)...)
	require.NoError(t, err)
	return p
}

// fileStart starts a platform whose metastore, blob store and event store live under dir, with the default KV
// engine (memory), as storage.mode: file does.
func fileStart(t *testing.T, dir string) *Platform {
	t.Helper()
	eng, err := bstore.Open(filepath.Join(dir, "store"), bstore.WithValueLogGCInterval(0))
	require.NoError(t, err)
	blobDir := filepath.Join(dir, "blob")
	require.NoError(t, os.MkdirAll(blobDir, 0o700))
	bucket, err := gocloud.Open(context.Background(), gocloud.FileURL(blobDir))
	require.NoError(t, err)
	return eventingPlatform(t, WithStore(store.New(eng)), WithBlob(bucket), WithDeadLetterQueue(filepath.Join(dir, "deadletter"), 3, 0, 0))
}

// watchDrops reconciles the blob EventSource default/drops, then runs the BlobWatcher until fired holds want and
// returns every key it fired.
func watchDrops(t *testing.T, p *Platform, want string) []string {
	t.Helper()
	ctx := context.Background()
	_, err := p.eventing.Reconcile(ctx, controller.Request{GVK: v1.KindEventSource.GVK(), Namespace: "default", Name: "drops"})
	require.NoError(t, err)
	var mu sync.Mutex
	var fired []string
	cancelSub := p.eventFanout.Subscribe("default", "drops", "arrived", func(_ context.Context, ev eventing.CloudEvent) {
		var d eventing.BlobEventData
		if json.Unmarshal(ev.Data, &d) == nil {
			mu.Lock()
			fired = append(fired, d.Key)
			mu.Unlock()
		}
	})
	defer cancelSub()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.blobWatcher.Run(runCtx) }()
	require.Eventually(t, func() bool {
		rec, err := p.eventStore.SeenLists().Load(ctx, "default", "drops", "arrived")
		return err == nil && rec.Seen[want] != ""
	}, 10*time.Second, 10*time.Millisecond, "%s fires and is recorded", want)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	mu.Lock()
	defer mu.Unlock()
	return fired
}

// scenario: restart-keeps-eventing-state — with file storage and the default KV engine, a restart keeps the seen
// list (drop/a does not fire again, a new drop/b fires once) and the dead letter, listed with the same id.
func TestScenarioRestartKeepsEventingState(t *testing.T) {
	ctx := context.Background()
	dir := eventingDir(t)
	const id = "01JZ0000000000000000000000"

	p := fileStart(t, dir)
	bkt, _ := v1.NewObject(v1.KindBucket)
	bkt.(*v1.Bucket).Name, bkt.(*v1.Bucket).Namespace, bkt.(*v1.Bucket).ResourceGroup = "raw", "default", "rg1"
	create(t, p.cfg.store, bkt)
	obj, _ := v1.NewObject(v1.KindEventSource)
	es := obj.(*v1.EventSource)
	es.Name, es.Namespace, es.ResourceGroup = "drops", "default", "rg1"
	es.Spec.Blob = &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "arrived", Prefix: "drop/"}}}
	create(t, p.cfg.store, es)
	require.NoError(t, p.cfg.blob.Put(ctx, "s3/default/raw/drop/a", []byte("a"), blob.PutOptions{}))
	require.Equal(t, []string{"drop/a"}, watchDrops(t, p, "drop/a"))
	require.NoError(t, p.deadLetters.Put(ctx, deadletter.DeadLetter{ID: id, Namespace: "default", Sensor: "notifier", Attempts: 3, FailedAt: v1.NewTimestamp(time.Now())}))
	require.NoError(t, p.Shutdown(ctx))

	p = fileStart(t, dir)
	t.Cleanup(func() { _ = p.Shutdown(ctx) })
	require.NoError(t, p.cfg.blob.Put(ctx, "s3/default/raw/drop/b", []byte("b"), blob.PutOptions{}))
	require.Equal(t, []string{"drop/b"}, watchDrops(t, p, "drop/b"))

	srv := httptest.NewServer(p.httpServer.Handler)
	defer srv.Close()
	c, err := sdk.New(srv.URL, sdk.WithToken(DevToken))
	require.NoError(t, err)
	listed, err := c.DeadLetters(ctx, "default")
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, id, listed[0].ID)
}

// scenario: memory-mode-forgets — with storage.mode: memory, a restart leaves the event store with no dead letter
// and no seen list.
func TestScenarioMemoryModeForgets(t *testing.T) {
	ctx := context.Background()
	p := eventingPlatform(t, WithDeadLetterQueue("", 3, 0, 0))
	require.NoError(t, p.eventStore.SeenLists().Save(ctx, "default", "drops", "arrived", eventing.SeenList{Bucket: "raw", Seen: map[string]string{"drop/a": "1-1"}}))
	require.NoError(t, p.deadLetters.Put(ctx, deadletter.DeadLetter{ID: "01A", Namespace: "default"}))
	require.Equal(t, 3, snapshotRecords(t, p), "a head, a part and a dead letter before the restart")
	require.NoError(t, p.Shutdown(ctx))

	p = eventingPlatform(t, WithDeadLetterQueue("", 3, 0, 0))
	t.Cleanup(func() { _ = p.Shutdown(ctx) })
	require.Zero(t, snapshotRecords(t, p))
}

// scenario: memory-store-keeps-kv-keys — a library platform with a durable KV and no event-store directory does not
// run the move, so the KV keeps the seen-list keys.
func TestScenarioMemoryStoreKeepsKVKeys(t *testing.T) {
	ctx := context.Background()
	kv := legacyKV(t)
	p := eventingPlatform(t, WithKVStore(kv))
	t.Cleanup(func() { _ = p.Shutdown(ctx) })

	_, found, err := kv.Get(ctx, legacySeenKey)
	require.NoError(t, err)
	require.True(t, found, "the KV keeps the key")
	require.Zero(t, snapshotRecords(t, p), "no seen list moved into the event store")
}

// TestNewRunsTheSeenListMove: an event store with a directory takes the KV's seen lists at New, and a failed move
// fails New, naming it, with the KV records kept.
func TestNewRunsTheSeenListMove(t *testing.T) {
	ctx := context.Background()
	t.Run("moved", func(t *testing.T) {
		kv := legacyKV(t)
		p := eventingPlatform(t, WithKVStore(kv), WithDeadLetterQueue(filepath.Join(eventingDir(t), "deadletter"), 3, 0, 0))
		t.Cleanup(func() { _ = p.Shutdown(ctx) })
		rec, err := p.eventStore.SeenLists().Load(ctx, "default", "drops", "arrived")
		require.NoError(t, err)
		require.Equal(t, "1-1", rec.Seen["drop/a"])
		keys, err := kv.List(ctx, "_eventing/blobwatch/")
		require.NoError(t, err)
		require.Empty(t, keys)
	})
	t.Run("failed", func(t *testing.T) {
		kv := legacyKV(t)
		_, err := New(InMemory(), WithLogger(slog.New(slog.DiscardHandler)), WithKVStore(failingList{kv}),
			WithDeadLetterQueue(filepath.Join(eventingDir(t), "deadletter"), 3, 0, 0))
		require.ErrorContains(t, err, "seen-list move")
		_, found, err := kv.Get(ctx, legacySeenKey)
		require.NoError(t, err)
		require.True(t, found, "the KV keeps the record")
	})
}

// legacyKV returns a durable KV holding an ADR-0157 seen list of default/drops/arrived, as an earlier release kept.
func legacyKV(t *testing.T) kvstore.KV {
	t.Helper()
	kv, err := kvbadger.Open(filepath.Join(eventingDir(t), "kv"), kvbadger.WithValueLogGCInterval(0))
	require.NoError(t, err)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	require.NoError(t, kv.Put(context.Background(), legacySeenKey, []byte(`{"bucket":"raw","prefix":"drop/","uid":"u","seen":{"drop/a":"1-1"}}`)))
	return kv
}

type failingList struct{ kvstore.KV }

func (failingList) List(context.Context, string) ([]string, error) {
	return nil, fault.Unavailablef("failingList.List", "kv unavailable")
}

func snapshotRecords(t *testing.T, p *Platform) int {
	t.Helper()
	n := 0
	_, err := p.eventStore.Snapshot(context.Background(), func(snapshot.Record) error { n++; return nil })
	require.NoError(t, err)
	return n
}
