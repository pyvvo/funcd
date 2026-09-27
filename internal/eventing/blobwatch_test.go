package eventing

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
)

// fakeLister is a scripted BucketLister: it returns the objects whose Key is under the requested prefix,
// so a test controls exactly what a poll observes (incl. ModTime, for the dedup/tie-break rules).
type fakeLister struct {
	mu   sync.Mutex
	objs []blob.Attributes
}

func (l *fakeLister) set(objs ...blob.Attributes) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.objs = objs
}

func (l *fakeLister) List(_ context.Context, _ v1.NamespaceName, _ v1.ObjectName, prefix string) ([]blob.Attributes, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []blob.Attributes
	for _, o := range l.objs {
		if strings.HasPrefix(o.Key, prefix) {
			out = append(out, o)
		}
	}
	return out, nil
}

// capturePub is a Publisher that records every published CloudEvent for assertion.
type capturePub struct {
	mu     sync.Mutex
	events []CloudEvent
}

func (p *capturePub) Publish(_ context.Context, ev CloudEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
	return nil
}

func (p *capturePub) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

func (p *capturePub) snapshot() []CloudEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]CloudEvent(nil), p.events...)
}

// newWatcher wires a BlobWatcher over the fake lister + capturing publisher + a KV-backed watermark, and
// registers one blob event (bucket `raw`, event `arrived`, prefix `drop/`) in namespace `lake`.
func newWatcher(t *testing.T, lister BucketLister, pub Publisher, marks Watermark) *BlobWatcher {
	t.Helper()
	w, err := NewBlobWatcher(lister, pub, marks, time.Second, nil)
	require.NoError(t, err)
	w.Register("lake", "drops", &v1.BlobSource{
		Bucket: "raw",
		Events: []v1.BlobEvent{{Name: "arrived", Prefix: "drop/", On: []v1.BlobEventType{v1.BlobCreated}}},
	})
	return w
}

// scenario: object-created-emits-event — an object under the watched prefix fires ONE named CloudEvent
// whose envelope + data == {bucket,key,size,version,time} describe the landed object.
func TestScenarioObjectCreatedEmitsEvent(t *testing.T) {
	lister := &fakeLister{}
	pub := &capturePub{}
	wm, err := NewKVWatermark(kvmemory.New())
	require.NoError(t, err)
	w := newWatcher(t, lister, pub, wm)

	mt := time.Now().UTC().Truncate(time.Second)
	lister.set(blob.Attributes{Key: "drop/a.parquet", Size: 42, ModTime: mt})
	w.poll(context.Background())

	require.Equal(t, 1, pub.count(), "one object under the prefix fires exactly one event")
	ev := pub.snapshot()[0]
	require.Equal(t, SourceURI("lake", "drops"), ev.Source)
	require.Equal(t, "arrived", ev.Type)
	var d BlobEventData
	require.NoError(t, json.Unmarshal(ev.Data, &d))
	require.Equal(t, "raw", d.Bucket)
	require.Equal(t, "drop/a.parquet", d.Key)
	require.Equal(t, int64(42), d.Size)
	require.NotEmpty(t, d.Version, "a (ModTime,Size) fingerprint is carried as data.version")
	require.False(t, d.Time.IsZero())
}

// scenario: dedup-no-refire — a re-list of an unchanged, already-emitted object does NOT re-emit.
func TestScenarioDedupNoRefire(t *testing.T) {
	lister := &fakeLister{}
	pub := &capturePub{}
	wm, err := NewKVWatermark(kvmemory.New())
	require.NoError(t, err)
	w := newWatcher(t, lister, pub, wm)

	obj := blob.Attributes{Key: "drop/a.parquet", Size: 10, ModTime: time.Now().UTC().Truncate(time.Second)}
	lister.set(obj)
	w.poll(context.Background())
	require.Equal(t, 1, pub.count())

	w.poll(context.Background()) // same object, re-listed
	w.poll(context.Background())
	require.Equal(t, 1, pub.count(), "the watermark suppresses the re-seen object")
}

// scenario: prefix-scoped — an object OUTSIDE the watched prefix never fires.
func TestScenarioPrefixScoped(t *testing.T) {
	lister := &fakeLister{}
	pub := &capturePub{}
	wm, err := NewKVWatermark(kvmemory.New())
	require.NoError(t, err)
	w := newWatcher(t, lister, pub, wm)

	lister.set(blob.Attributes{Key: "other/c.parquet", Size: 7, ModTime: time.Now().UTC()})
	w.poll(context.Background())
	require.Equal(t, 0, pub.count(), "an object outside prefix drop/ does not fire")

	lister.set(
		blob.Attributes{Key: "other/c.parquet", Size: 7, ModTime: time.Now().UTC()},
		blob.Attributes{Key: "drop/in.parquet", Size: 8, ModTime: time.Now().UTC()},
	)
	w.poll(context.Background())
	require.Equal(t, 1, pub.count(), "only the in-prefix object fires")
	require.Equal(t, "drop/in.parquet", mustData(t, pub.snapshot()[0]).Key)
}

// scenario: restart-no-replay — the watermark is PERSISTED, so a fresh watcher over the same KV does not
// re-emit the whole prefix after a restart.
func TestScenarioRestartNoReplay(t *testing.T) {
	kv := kvmemory.New()
	lister := &fakeLister{}
	lister.set(
		blob.Attributes{Key: "drop/a.parquet", Size: 1, ModTime: time.Now().UTC().Add(-2 * time.Second).Truncate(time.Second)},
		blob.Attributes{Key: "drop/b.parquet", Size: 2, ModTime: time.Now().UTC().Add(-1 * time.Second).Truncate(time.Second)},
	)

	wm1, err := NewKVWatermark(kv)
	require.NoError(t, err)
	pub1 := &capturePub{}
	w1 := newWatcher(t, lister, pub1, wm1)
	w1.poll(context.Background())
	require.Equal(t, 2, pub1.count(), "first run emits both objects once")

	// "Restart": a brand-new watcher + watermark over the SAME KV, same registration, same listing.
	wm2, err := NewKVWatermark(kv)
	require.NoError(t, err)
	pub2 := &capturePub{}
	w2 := newWatcher(t, lister, pub2, wm2)
	w2.poll(context.Background())
	require.Equal(t, 0, pub2.count(), "a restart reloads the persisted watermark and replays nothing")
}

// scenario: external-s3-write-detected — an object WRITTEN BY ANOTHER PATH (here, straight into the shared
// substrate at the s3-gateway key layout `s3/<ns>/<bucket>/…`, as an external SigV4 PUT lands) is detected
// by the poll over the SAME s3BucketFor view — proving detection rides List alone, writer-agnostic.
func TestScenarioExternalS3WriteDetected(t *testing.T) {
	ctx := context.Background()
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = shared.Close() })

	pub := &capturePub{}
	wm, err := NewKVWatermark(kvmemory.New())
	require.NoError(t, err)
	w := newWatcher(t, s3ViewLister{shared: shared}, pub, wm)

	// A write the watcher never made, landing where the ADR-0080 S3 frontend puts a `raw` object under `drop/`.
	require.NoError(t, shared.Put(ctx, "s3/lake/raw/drop/b.parquet", []byte("parquet-bytes")))
	w.poll(ctx)

	require.Equal(t, 1, pub.count(), "the poll detects the external write over the shared substrate view")
	require.Equal(t, "drop/b.parquet", mustData(t, pub.snapshot()[0]).Key)
}

// s3ViewLister resolves (ns, bucket) to the SAME prefixed substrate view s3BucketFor builds in pkg/funcd —
// the view external S3-frontend writes land in — and lists a prefix over it.
type s3ViewLister struct{ shared blob.Bucket }

func (l s3ViewLister) List(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName, prefix string) ([]blob.Attributes, error) {
	return blob.Prefixed(l.shared, "s3/"+string(ns)+"/"+string(bucket)+"/").List(ctx, prefix)
}

// TestBlobWatcherTieBreak covers the same-timestamp tie-break: two objects sharing the newest ModTime both
// emit once, and neither re-emits on a re-list (the bounded KeysAtMax tie-set).
func TestBlobWatcherTieBreak(t *testing.T) {
	lister := &fakeLister{}
	pub := &capturePub{}
	wm, err := NewKVWatermark(kvmemory.New())
	require.NoError(t, err)
	w := newWatcher(t, lister, pub, wm)

	mt := time.Now().UTC().Truncate(time.Second)
	lister.set(
		blob.Attributes{Key: "drop/a.parquet", Size: 1, ModTime: mt},
		blob.Attributes{Key: "drop/b.parquet", Size: 2, ModTime: mt},
	)
	w.poll(context.Background())
	require.Equal(t, 2, pub.count(), "both objects at the newest timestamp emit")

	w.poll(context.Background())
	require.Equal(t, 2, pub.count(), "neither re-emits — the tie-set is remembered")

	// A newer object advances the max and emits once.
	lister.set(
		blob.Attributes{Key: "drop/a.parquet", Size: 1, ModTime: mt},
		blob.Attributes{Key: "drop/b.parquet", Size: 2, ModTime: mt},
		blob.Attributes{Key: "drop/c.parquet", Size: 3, ModTime: mt.Add(time.Second)},
	)
	w.poll(context.Background())
	require.Equal(t, 3, pub.count(), "only the newer object emits")
}

// TestBlobWatcherRegisterDeregister covers registration bookkeeping: a source registers its events and
// deregisters them on delete/kind-change (no further polling).
func TestBlobWatcherRegisterDeregister(t *testing.T) {
	w, err := NewBlobWatcher(&fakeLister{}, &capturePub{}, mustWM(t), time.Second, nil)
	require.NoError(t, err)
	require.Equal(t, 0, w.ActiveWatches())
	w.Register("lake", "drops", &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "a"}, {Name: "b"}}})
	require.Equal(t, 2, w.ActiveWatches())
	w.Register("lake", "drops", &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "a"}}}) // pruned to 1
	require.Equal(t, 1, w.ActiveWatches())
	w.Deregister("lake", "drops")
	require.Equal(t, 0, w.ActiveWatches())
}

func mustWM(t *testing.T) Watermark {
	t.Helper()
	wm, err := NewKVWatermark(kvmemory.New())
	require.NoError(t, err)
	return wm
}

func mustData(t *testing.T, ev CloudEvent) BlobEventData {
	t.Helper()
	var d BlobEventData
	require.NoError(t, json.Unmarshal(ev.Data, &d))
	return d
}
