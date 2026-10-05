package eventing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

const testUID v1.UID = "uid-1"

// newWatcher wires a BlobWatcher over the fake lister + capturing publisher + a KV-backed watermark, and
// registers one blob event (bucket `raw`, event `arrived`, prefix `drop/`) in namespace `lake`.
func newWatcher(t *testing.T, lister BucketLister, pub Publisher, marks Watermark) *BlobWatcher {
	t.Helper()
	w, err := NewBlobWatcher(lister, pub, marks, time.Second, nil)
	require.NoError(t, err)
	w.Register("lake", "drops", testUID, &v1.BlobSource{
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
	require.NoError(t, shared.Put(ctx, "s3/lake/raw/drop/b.parquet", []byte("parquet-bytes"), blob.PutOptions{}))
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
// emit once, and neither re-emits on a re-list.
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
	w.Register("lake", "drops", testUID, &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "a"}, {Name: "b"}}})
	require.Equal(t, 2, w.ActiveWatches())
	w.Register("lake", "drops", testUID, &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "a"}}}) // pruned to 1
	require.Equal(t, 1, w.ActiveWatches())
	w.Deregister("lake", "drops")
	require.Equal(t, 0, w.ActiveWatches())
}

// bucketView lists a prefix straight from one blob.Bucket.
type bucketView struct{ b blob.Bucket }

func (l bucketView) List(ctx context.Context, _ v1.NamespaceName, _ v1.ObjectName, prefix string) ([]blob.Attributes, error) {
	return l.b.List(ctx, prefix)
}

// firedKeys counts the published events per object key.
func firedKeys(t *testing.T, pub *capturePub) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, ev := range pub.snapshot() {
		out[mustData(t, ev).Key]++
	}
	return out
}

// scenario: out-of-order-visibility-never-loses
func TestScenarioOutOfOrderVisibilityNeverLoses(t *testing.T) {
	t.Run("an older object listed after a newer one fired", func(t *testing.T) {
		lister := &fakeLister{}
		pub := &capturePub{}
		w := newWatcher(t, lister, pub, mustWM(t))
		t1 := time.Unix(1759536000, 0).UTC()
		t2 := t1.Add(time.Millisecond)
		lister.set(blob.Attributes{Key: "drop/b", Size: 1, ModTime: t2})
		w.poll(context.Background())
		lister.set(blob.Attributes{Key: "drop/a", Size: 1, ModTime: t1}, blob.Attributes{Key: "drop/b", Size: 1, ModTime: t2})
		w.poll(context.Background())
		require.Equal(t, map[string]int{"drop/a": 1, "drop/b": 1}, firedKeys(t, pub))
	})

	const writers, perWriter = 8, 60
	for name, scheme := range map[string]string{"mem": "mem://", "file": "file"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			url := scheme
			if scheme == "file" {
				url = gocloud.FileURL(t.TempDir())
			}
			b, err := gocloud.Open(ctx, url)
			require.NoError(t, err)
			t.Cleanup(func() { _ = b.Close() })
			pub := &capturePub{}
			w := newWatcher(t, bucketView{b: b}, pub, mustWM(t))
			writeWhilePolling(t, b, w, writers, perWriter)
			w.poll(ctx)

			fired := firedKeys(t, pub)
			require.Len(t, fired, writers*perWriter, "every object fires at least once")
			if scheme == "mem://" {
				for key, n := range fired {
					require.Equal(t, 1, n, "%s fires exactly once on mem://", key)
				}
			}
		})
	}
}

// writeWhilePolling puts writers×perWriter objects from concurrent writers while w polls in a loop.
func writeWhilePolling(t *testing.T, b blob.Bucket, w *BlobWatcher, writers, perWriter int) {
	t.Helper()
	ctx := context.Background()
	done := make(chan struct{})
	polled := make(chan struct{})
	go func() {
		defer close(polled)
		for {
			select {
			case <-done:
				return
			default:
				w.poll(ctx)
			}
		}
	}()
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range perWriter {
				key := fmt.Sprintf("drop/w%d-%03d", i, j)
				if err := b.Put(ctx, key, []byte(key), blob.PutOptions{}); err != nil {
					t.Errorf("put %s: %v", key, err)
				}
			}
		}()
	}
	wg.Wait()
	close(done)
	<-polled
}

// scenario: rewritten-object-fires-again
func TestScenarioRewrittenObjectFiresAgain(t *testing.T) {
	lister := &fakeLister{}
	pub := &capturePub{}
	w := newWatcher(t, lister, pub, mustWM(t))
	mt := time.Unix(1759536000, 0).UTC()
	lister.set(blob.Attributes{Key: "drop/a", Size: 10, ModTime: mt})
	w.poll(context.Background())

	rewrites := []blob.Attributes{
		{Key: "drop/a", Size: 10, ModTime: mt.Add(time.Second)},
		{Key: "drop/a", Size: 11, ModTime: mt.Add(time.Second)},
	}
	for i, o := range rewrites {
		lister.set(o)
		w.poll(context.Background())
		require.Equal(t, i+2, pub.count(), "a new ModTime or Size fires once more")
		require.Equal(t, versionOf(o), mustData(t, pub.snapshot()[i+1]).Version)
		w.poll(context.Background())
		require.Equal(t, i+2, pub.count(), "listed unchanged next, nothing fires")
	}
}

// scenario: deleted-object-pruned
func TestScenarioDeletedObjectPruned(t *testing.T) {
	ctx := context.Background()
	lister := &fakeLister{}
	pub := &capturePub{}
	wm := mustWM(t)
	w := newWatcher(t, lister, pub, wm)
	obj := blob.Attributes{Key: "drop/a", Size: 10, ModTime: time.Unix(1759536000, 0).UTC()}
	lister.set(obj)
	w.poll(ctx)
	require.Equal(t, 1, pub.count())

	lister.set()
	w.poll(ctx)
	rec, err := wm.Load(ctx, "lake", "drops", "arrived")
	require.NoError(t, err)
	require.NotContains(t, rec.Seen, "drop/a", "a key no longer listed leaves the record")

	lister.set(obj)
	w.poll(ctx)
	require.Equal(t, 2, pub.count(), "written again with the same ModTime and Size, it fires")
}

func TestBlobWatcherInvalidUTF8KeySkipped(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	lister := &fakeLister{}
	pub := &capturePub{}
	wm := mustWM(t)
	w, err := NewBlobWatcher(lister, pub, wm, time.Second, slog.New(slog.NewTextHandler(&logs, nil)))
	require.NoError(t, err)
	w.Register("lake", "drops", testUID, &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "arrived", Prefix: "drop/"}}})
	mt := time.Unix(1759536000, 0).UTC()
	lister.set(blob.Attributes{Key: "drop/\xff.bin", Size: 1, ModTime: mt}, blob.Attributes{Key: "drop/ok", Size: 1, ModTime: mt})
	w.poll(ctx)
	w.poll(ctx)

	require.Equal(t, map[string]int{"drop/ok": 1}, firedKeys(t, pub))
	rec, err := wm.Load(ctx, "lake", "drops", "arrived")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"drop/ok": versionOf(blob.Attributes{Size: 1, ModTime: mt})}, rec.Seen)
	require.Equal(t, 1, strings.Count(logs.String(), "not valid UTF-8"), "one warn per key per process")
}

// scenario: dropped-event-readded-resumes
func TestScenarioDroppedEventReaddedResumes(t *testing.T) {
	ctx := context.Background()
	lister := &fakeLister{}
	pub := &capturePub{}
	wm := mustWM(t)
	w := newWatcher(t, lister, pub, wm)
	both := &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "arrived", Prefix: "drop/"}, {Name: "other", Prefix: "other/"}}}
	w.Register("lake", "drops", testUID, both)
	lister.set(blob.Attributes{Key: "drop/a", Size: 1, ModTime: time.Unix(1759536000, 0).UTC()})
	w.poll(ctx)
	require.Equal(t, 1, pub.count())

	w.Register("lake", "drops", testUID, &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "other", Prefix: "other/"}}})
	w.poll(ctx)
	w.Register("lake", "drops", testUID, both)
	rec, err := wm.Load(ctx, "lake", "drops", "arrived")
	require.NoError(t, err)
	require.Contains(t, rec.Seen, "drop/a", "the dropped event's record is kept")
	w.poll(ctx)
	require.Equal(t, 1, pub.count(), "the re-added event resumes; nothing fires")
}

// hookPub records events and runs onFirst inside the first Publish, while a poll is in flight.
type hookPub struct {
	capturePub
	onFirst func()
	once    sync.Once
}

func (p *hookPub) Publish(ctx context.Context, ev CloudEvent) error {
	err := p.capturePub.Publish(ctx, ev)
	p.once.Do(p.onFirst)
	return err
}

func TestBlobWatcherPurgeDuringPoll(t *testing.T) {
	mt := time.Unix(1759536000, 0).UTC()
	src := &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "arrived", Prefix: "drop/"}}}
	cases := map[string]struct {
		during      func(t *testing.T, w *BlobWatcher)
		recordUID   v1.UID
		nextPollPub int
	}{
		"purge": {
			during: func(t *testing.T, w *BlobWatcher) {
				t.Helper()
				require.NoError(t, w.Purge(context.Background(), "lake", "drops"))
			},
		},
		"purge then same-UID register": {
			during: func(t *testing.T, w *BlobWatcher) {
				t.Helper()
				require.NoError(t, w.Purge(context.Background(), "lake", "drops"))
				w.Register("lake", "drops", testUID, src)
			},
			recordUID:   testUID,
			nextPollPub: 2,
		},
		"new-UID register": {
			during: func(_ *testing.T, w *BlobWatcher) {
				w.Register("lake", "drops", "uid-2", src)
			},
			recordUID:   "uid-2",
			nextPollPub: 2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			kv := kvmemory.New()
			wm, err := NewKVWatermark(kv)
			require.NoError(t, err)
			lister := &fakeLister{}
			lister.set(blob.Attributes{Key: "drop/a", Size: 1, ModTime: mt}, blob.Attributes{Key: "drop/b", Size: 1, ModTime: mt})
			pub := &hookPub{}
			w := newWatcher(t, lister, pub, wm)
			pub.onFirst = func() { tc.during(t, w) }

			w.poll(ctx)
			require.Equal(t, 1, pub.count(), "the stale poll publishes nothing more")
			keys, err := kv.List(ctx, kvWatermarkPrefix+"lake/drops/")
			require.NoError(t, err)
			require.Empty(t, keys, "the stale poll saves nothing")

			w.poll(ctx)
			require.Equal(t, 1+tc.nextPollPub, pub.count())
			rec, err := wm.Load(ctx, "lake", "drops", "arrived")
			require.NoError(t, err)
			require.Equal(t, tc.recordUID, rec.UID)
		})
	}
}

// hookSave is a Watermark whose Save runs during, then fails.
type hookSave struct {
	Watermark
	during func()
}

func (h *hookSave) Save(context.Context, v1.NamespaceName, v1.ObjectName, v1.ObjectName, SeenList) error {
	h.during()
	return errors.New("value too large")
}

func TestBlobWatcherSaveOutcomeOnlyForLiveEntry(t *testing.T) {
	cases := map[string]func(w *BlobWatcher){
		"event dropped by Register": func(w *BlobWatcher) {
			w.Register("lake", "drops", testUID, &v1.BlobSource{Bucket: "raw", Events: []v1.BlobEvent{{Name: "other", Prefix: "other/"}}})
		},
		"source deregistered": func(w *BlobWatcher) { w.Deregister("lake", "drops") },
	}
	for name, during := range cases {
		t.Run(name, func(t *testing.T) {
			lister := &fakeLister{}
			lister.set(blob.Attributes{Key: "drop/a", Size: 1, ModTime: time.Unix(1759536000, 0).UTC()})
			wm := &hookSave{Watermark: mustWM(t)}
			w := newWatcher(t, lister, &capturePub{}, wm)
			wm.during = func() { during(w) }
			var reported []v1.ObjectName
			w.SetHooks(WatchHooks{SaveFailing: func(_ context.Context, _ SourceRef, failing map[v1.ObjectName]error) error {
				for ev := range failing {
					reported = append(reported, ev)
				}
				return nil
			}})

			w.poll(context.Background())
			require.NotContains(t, reported, v1.ObjectName("arrived"), "a Save that fails after its event left the watch is not recorded")
		})
	}
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
