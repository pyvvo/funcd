package eventing_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// capturePublisher records every published CloudEvent (a real Publisher stub, no mock framework).
type capturePublisher struct {
	mu     sync.Mutex
	events []eventing.CloudEvent
}

func (c *capturePublisher) Publish(_ context.Context, ev eventing.CloudEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return nil
}

func (c *capturePublisher) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.events) }

func newStore() store.Store { return store.New(memory.New()) }

func timerEvent(name string, interval time.Duration) v1.TimerEvent {
	return v1.TimerEvent{Name: v1.ObjectName(name), Interval: interval}
}

func createTimerSource(t *testing.T, st store.Store, name string, events ...v1.TimerEvent) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindEventSource)
	require.True(t, ok)
	es := obj.(*v1.EventSource)
	es.Name, es.Namespace, es.ResourceGroup = v1.ObjectName(name), "team-a", "rg1"
	es.Spec.Timer = &v1.TimerSource{Events: events}
	_, err := st.Create(context.Background(), es)
	require.NoError(t, err)
}

func reqOf(name string) controller.Request {
	return controller.Request{GVK: v1.KindEventSource.GVK(), Namespace: "team-a", Name: v1.ObjectName(name)}
}

// scenario: timer-source-reconciles-ready — a timer source registers its named event(s) and reaches Ready.
func TestScenarioTimerSourceReconcilesReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore()
	createTimerSource(t, st, "nightly", timerEvent("tick", time.Minute))
	src, err := eventing.NewSource(eventing.Deps{Store: st, Publisher: &capturePublisher{}})
	require.NoError(t, err)

	_, err = src.Reconcile(ctx, reqOf("nightly"))
	require.NoError(t, err)
	require.Equal(t, 1, src.ActiveTimers())
	es, err := st.Get(ctx, v1.KindEventSource.GVK(), "team-a", "nightly")
	require.NoError(t, err)
	require.Equal(t, v1.PhaseReady, es.(*v1.EventSource).Status.Phase)
}

// scenario: named-event-emitted — a firing PUBLISHES a named CloudEvent (source URI + event type) and the
// Source records NO Invocation (a firing is a publish; the Invocation moves to the Sensor, ADR-0109).
func TestScenarioNamedEventEmitted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore()
	createTimerSource(t, st, "nightly", timerEvent("tick", time.Minute))
	pub := &capturePublisher{}
	src, err := eventing.NewSource(eventing.Deps{Store: st, Publisher: pub})
	require.NoError(t, err)

	require.NoError(t, src.Fire(ctx, "team-a", "nightly", "tick"))
	require.Equal(t, 1, pub.count(), "a firing publishes exactly one CloudEvent")
	got := pub.events[0]
	require.Equal(t, "1.0", got.SpecVersion)
	require.Equal(t, eventing.SourceURI("team-a", "nightly"), got.Source)
	require.Equal(t, "tick", got.Type, "type carries the event name")

	list, err := st.List(ctx, v1.KindInvocation.GVK(), store.ListOptions{})
	require.NoError(t, err)
	require.Empty(t, list.Items, "the Source publishes, it does not record an Invocation (that moves to the Sensor)")
}

// stubLister is a no-op BucketLister for reconcile tests (registration is what's asserted, not polling).
type stubLister struct{}

func (stubLister) List(context.Context, v1.NamespaceName, v1.ObjectName, string) ([]blob.Attributes, error) {
	return nil, nil
}

func createBlobSource(t *testing.T, st store.Store, name, bucket string, events ...v1.BlobEvent) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindEventSource)
	require.True(t, ok)
	es := obj.(*v1.EventSource)
	es.Name, es.Namespace, es.ResourceGroup = v1.ObjectName(name), "team-a", "rg1"
	es.Spec.Blob = &v1.BlobSource{Bucket: v1.ObjectName(bucket), Events: events}
	_, err := st.Create(context.Background(), es)
	require.NoError(t, err)
}

func createBucket(t *testing.T, st store.Store, name string) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindBucket)
	require.True(t, ok)
	b := obj.(*v1.Bucket)
	b.Name, b.Namespace, b.ResourceGroup = v1.ObjectName(name), "team-a", "rg1"
	_, err := st.Create(context.Background(), b)
	require.NoError(t, err)
}

// scenario: blob-source-missing-bucket-not-ready (ADR-0119) — a `blob:` source whose Bucket does not exist
// is NotReady with a BucketNotFound condition (never Ready-but-not-polling) and requeues; once the Bucket
// exists it reconciles Ready and its events register on the watcher.
func TestScenarioBlobSourceMissingBucketNotReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore()
	createBlobSource(t, st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"})
	watcher, err := eventing.NewBlobWatcher(stubLister{}, &capturePublisher{}, eventing.NewMemWatermark(), time.Second, nil)
	require.NoError(t, err)
	src, err := eventing.NewSource(eventing.Deps{Store: st, Publisher: &capturePublisher{}, Blob: watcher})
	require.NoError(t, err)

	// Bucket missing ⇒ NotReady + requeue, nothing registered.
	res, err := src.Reconcile(ctx, reqOf("drops"))
	require.NoError(t, err)
	require.Positive(t, res.RequeueAfter, "a missing bucket requeues so a later-created bucket is picked up")
	require.Equal(t, 0, watcher.ActiveWatches(), "no events register while the bucket is missing")
	es, err := st.Get(ctx, v1.KindEventSource.GVK(), "team-a", "drops")
	require.NoError(t, err)
	cond, ok := es.(*v1.EventSource).Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "BucketNotFound", cond.Reason)

	// Create the Bucket ⇒ reconcile Ready + register.
	createBucket(t, st, "raw")
	_, err = src.Reconcile(ctx, reqOf("drops"))
	require.NoError(t, err)
	require.Equal(t, 1, watcher.ActiveWatches(), "the event registers once its bucket exists")
	es, err = st.Get(ctx, v1.KindEventSource.GVK(), "team-a", "drops")
	require.NoError(t, err)
	require.Equal(t, v1.PhaseReady, es.(*v1.EventSource).Status.Phase)
}

// scenario: multiple-named-events — two named events under one source register independently.
func TestScenarioMultipleNamedEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore()
	createTimerSource(t, st, "clock", timerEvent("fast", 100*time.Millisecond), timerEvent("slow", time.Hour))
	src, err := eventing.NewSource(eventing.Deps{Store: st, Publisher: &capturePublisher{}})
	require.NoError(t, err)

	_, err = src.Reconcile(ctx, reqOf("clock"))
	require.NoError(t, err)
	require.Equal(t, 2, src.ActiveTimers(), "both named events register on their own intervals")
}

// scenario: deregister-on-delete — deleting the source deregisters all its named events.
func TestScenarioDeregisterOnDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore()
	createTimerSource(t, st, "nightly", timerEvent("tick", time.Minute))
	src, err := eventing.NewSource(eventing.Deps{Store: st, Publisher: &capturePublisher{}})
	require.NoError(t, err)

	_, err = src.Reconcile(ctx, reqOf("nightly"))
	require.NoError(t, err)
	require.Equal(t, 1, src.ActiveTimers())

	cur, err := st.Get(ctx, v1.KindEventSource.GVK(), "team-a", "nightly")
	require.NoError(t, err)
	require.NoError(t, st.Delete(ctx, v1.KindEventSource.GVK(), "team-a", "nightly", cur.GetObjectMeta().ResourceVersion))
	_, err = src.Reconcile(ctx, reqOf("nightly"))
	require.NoError(t, err)
	require.Equal(t, 0, src.ActiveTimers())
}

// Issue #102: no Bucket event reaches the EventSource reconciler, so a Ready blob source must requeue to
// re-check its Bucket; the requeued pass after the Bucket is deleted marks it NotReady and deregisters it.
func TestIssue102_BlobSourceNotReadyAfterBucketDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore()
	createBucket(t, st, "raw")
	createBlobSource(t, st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"})
	watcher, err := eventing.NewBlobWatcher(stubLister{}, &capturePublisher{}, eventing.NewMemWatermark(), time.Second, nil)
	require.NoError(t, err)
	src, err := eventing.NewSource(eventing.Deps{Store: st, Publisher: &capturePublisher{}, Blob: watcher})
	require.NoError(t, err)

	res, err := src.Reconcile(ctx, reqOf("drops"))
	require.NoError(t, err)
	require.Equal(t, 1, watcher.ActiveWatches())
	require.Positive(t, res.RequeueAfter, "a Ready blob source requeues, else it stays Ready after its Bucket is deleted")

	b, err := st.Get(ctx, v1.KindBucket.GVK(), "team-a", "raw")
	require.NoError(t, err)
	require.NoError(t, st.Delete(ctx, v1.KindBucket.GVK(), "team-a", "raw", b.GetObjectMeta().ResourceVersion))
	_, err = src.Reconcile(ctx, reqOf("drops"))
	require.NoError(t, err)
	require.Equal(t, 0, watcher.ActiveWatches(), "a source whose Bucket is gone stops polling it")
	es, err := st.Get(ctx, v1.KindEventSource.GVK(), "team-a", "drops")
	require.NoError(t, err)
	cond, ok := es.(*v1.EventSource).Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "BucketNotFound", cond.Reason)
}

func getSource(t *testing.T, st store.Store, name string) *v1.EventSource {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindEventSource.GVK(), "team-a", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.EventSource)
}

func newBlobSource(t *testing.T, st store.Store) *eventing.Source {
	t.Helper()
	watcher, err := eventing.NewBlobWatcher(stubLister{}, &capturePublisher{}, eventing.NewMemWatermark(), time.Second, nil)
	require.NoError(t, err)
	src, err := eventing.NewSource(eventing.Deps{Store: st, Publisher: &capturePublisher{}, Blob: watcher})
	require.NoError(t, err)
	return src
}

// Issue #148: the reconciler's status write must not carry a defaulted spec (that bumps the generation past
// the condition it stamps), a spec edit must refresh the Ready condition's observedGeneration, and a source
// that stops being a blob source must not keep the blob-only Ready condition.
func TestIssue148_StatusMatchesCurrentSpec(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("ready reconcile keeps the user's spec", func(t *testing.T) {
		t.Parallel()
		st := newStore()
		createBucket(t, st, "raw")
		createBlobSource(t, st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"})
		src := newBlobSource(t, st)
		for range 2 {
			_, err := src.Reconcile(ctx, reqOf("drops"))
			require.NoError(t, err)
		}
		es := getSource(t, st, "drops")
		require.Equal(t, int64(1), es.Generation, "a status write must not bump the generation")
		require.Empty(t, es.Spec.Blob.Events[0].On, "the default is not written into the stored spec")
		cond, ok := es.Status.Conditions.Get("Ready")
		require.True(t, ok)
		require.Equal(t, v1.ConditionTrue, cond.Status)
		require.Equal(t, es.Generation, cond.ObservedGeneration)
	})

	t.Run("spec edit refreshes the condition", func(t *testing.T) {
		t.Parallel()
		st := newStore()
		createBucket(t, st, "raw")
		createBlobSource(t, st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/", On: []v1.BlobEventType{v1.BlobCreated}})
		src := newBlobSource(t, st)
		_, err := src.Reconcile(ctx, reqOf("drops"))
		require.NoError(t, err)

		es := getSource(t, st, "drops")
		es.Spec.Blob.Events[0].Prefix = "in/"
		_, err = st.Update(ctx, es)
		require.NoError(t, err)
		_, err = src.Reconcile(ctx, reqOf("drops"))
		require.NoError(t, err)
		es = getSource(t, st, "drops")
		require.Equal(t, int64(2), es.Generation)
		cond, ok := es.Status.Conditions.Get("Ready")
		require.True(t, ok)
		require.Equal(t, es.Generation, cond.ObservedGeneration, "the Ready condition observes the edited spec")
	})

	for _, bucket := range []string{"missing", "raw"} {
		t.Run("timer source drops the blob condition/bucket "+bucket, func(t *testing.T) {
			t.Parallel()
			st := newStore()
			createBucket(t, st, "raw")
			createBlobSource(t, st, "drops", bucket, v1.BlobEvent{Name: "arrived"})
			src := newBlobSource(t, st)
			_, err := src.Reconcile(ctx, reqOf("drops"))
			require.NoError(t, err)

			es := getSource(t, st, "drops")
			es.Spec.Blob, es.Spec.Timer = nil, &v1.TimerSource{Events: []v1.TimerEvent{timerEvent("tick", time.Minute)}}
			_, err = st.Update(ctx, es)
			require.NoError(t, err)
			_, err = src.Reconcile(ctx, reqOf("drops"))
			require.NoError(t, err)
			require.Equal(t, 1, src.ActiveTimers())
			es = getSource(t, st, "drops")
			require.Equal(t, v1.PhaseReady, es.Status.Phase)
			cond, ok := es.Status.Conditions.Get("Ready")
			require.False(t, ok, "a timer source keeps no blob Ready condition, got %+v", cond)
		})
	}
}

// scriptLister is a BucketLister scripted per Bucket, filtered by prefix.
type scriptLister struct {
	mu   sync.Mutex
	objs map[v1.ObjectName][]blob.Attributes
}

func (l *scriptLister) set(bucket v1.ObjectName, objs ...blob.Attributes) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.objs == nil {
		l.objs = map[v1.ObjectName][]blob.Attributes{}
	}
	l.objs[bucket] = objs
}

func (l *scriptLister) List(_ context.Context, _ v1.NamespaceName, bucket v1.ObjectName, prefix string) ([]blob.Attributes, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []blob.Attributes
	for _, o := range l.objs[bucket] {
		if strings.HasPrefix(o.Key, prefix) {
			out = append(out, o)
		}
	}
	return out, nil
}

// blobRig is a Source + BlobWatcher over a memory store, a scripted lister and a KV-backed watermark.
type blobRig struct {
	st      store.Store
	kv      kvstore.KV
	lister  *scriptLister
	pub     *capturePublisher
	watcher *eventing.BlobWatcher
	src     *eventing.Source
}

func newBlobRig(t *testing.T, marks eventing.Watermark) *blobRig {
	t.Helper()
	r := &blobRig{st: newStore(), kv: kvmemory.New(), lister: &scriptLister{}, pub: &capturePublisher{}}
	if marks == nil {
		wm, err := eventing.NewKVWatermark(r.kv)
		require.NoError(t, err)
		marks = wm
	}
	var err error
	r.watcher, err = eventing.NewBlobWatcher(r.lister, r.pub, marks, time.Second, nil)
	require.NoError(t, err)
	r.src, err = eventing.NewSource(eventing.Deps{Store: r.st, Publisher: &capturePublisher{}, Blob: r.watcher})
	require.NoError(t, err)
	return r
}

func (r *blobRig) reconcile(t *testing.T, name string) {
	t.Helper()
	_, err := r.src.Reconcile(context.Background(), reqOf(name))
	require.NoError(t, err)
}

func (r *blobRig) poll() { r.watcher.PollOnce(context.Background()) }

func (r *blobRig) records(t *testing.T, source string) []string {
	t.Helper()
	keys, err := r.kv.List(context.Background(), "_eventing/blobwatch/team-a/"+source+"/")
	require.NoError(t, err)
	return keys
}

// fired lists "<bucket>/<key>" of every published blob event, in order.
func (r *blobRig) fired(t *testing.T) []string {
	t.Helper()
	r.pub.mu.Lock()
	defer r.pub.mu.Unlock()
	out := make([]string, 0, len(r.pub.events))
	for _, ev := range r.pub.events {
		var d eventing.BlobEventData
		require.NoError(t, json.Unmarshal(ev.Data, &d))
		out = append(out, d.Bucket+"/"+d.Key)
	}
	return out
}

func (r *blobRig) update(t *testing.T, name string, edit func(es *v1.EventSource)) {
	t.Helper()
	es := getSource(t, r.st, name)
	edit(es)
	_, err := r.st.Update(context.Background(), es)
	require.NoError(t, err)
}

func deleteObject(t *testing.T, st store.Store, kind v1.Kind, name string) {
	t.Helper()
	cur, err := st.Get(context.Background(), kind.GVK(), "team-a", v1.ObjectName(name))
	require.NoError(t, err)
	require.NoError(t, st.Delete(context.Background(), kind.GVK(), "team-a", v1.ObjectName(name), cur.GetObjectMeta().ResourceVersion))
}

func object(key string) blob.Attributes {
	return blob.Attributes{Key: key, Size: 1, ModTime: time.Unix(1759536000, 0).UTC()}
}

// scenario: re-point-back-fills
func TestScenarioRePointBackFills(t *testing.T) {
	t.Parallel()
	t.Run("prefix", func(t *testing.T) {
		t.Parallel()
		r := newBlobRig(t, nil)
		createBucket(t, r.st, "raw")
		r.lister.set("raw", object("drop/a.parquet"), object("other/b.parquet"))
		createBlobSource(t, r.st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"})
		r.reconcile(t, "drops")
		r.poll()
		r.update(t, "drops", func(es *v1.EventSource) { es.Spec.Blob.Events[0].Prefix = "other/" })
		r.reconcile(t, "drops")
		r.poll()
		require.Equal(t, []string{"raw/drop/a.parquet", "raw/other/b.parquet"}, r.fired(t))
	})
	t.Run("bucket", func(t *testing.T) {
		t.Parallel()
		r := newBlobRig(t, nil)
		createBucket(t, r.st, "raw")
		createBucket(t, r.st, "raw2")
		r.lister.set("raw", object("drop/a.parquet"))
		r.lister.set("raw2", object("drop/a.parquet"))
		createBlobSource(t, r.st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"})
		r.reconcile(t, "drops")
		r.poll()
		r.update(t, "drops", func(es *v1.EventSource) { es.Spec.Blob.Bucket = "raw2" })
		r.reconcile(t, "drops")
		r.poll()
		require.Equal(t, []string{"raw/drop/a.parquet", "raw2/drop/a.parquet"}, r.fired(t))
	})
}

// scenario: re-create-back-fills
func TestScenarioReCreateBackFills(t *testing.T) {
	t.Parallel()
	for name, reconcileDelete := range map[string]bool{"two reconciles": true, "one reconcile": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newBlobRig(t, nil)
			createBucket(t, r.st, "raw")
			r.lister.set("raw", object("drop/a.parquet"))
			createBlobSource(t, r.st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"})
			r.reconcile(t, "drops")
			r.poll()

			deleteObject(t, r.st, v1.KindEventSource, "drops")
			if reconcileDelete {
				r.reconcile(t, "drops")
			}
			createBlobSource(t, r.st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"})
			r.reconcile(t, "drops")
			r.poll()
			require.Equal(t, []string{"raw/drop/a.parquet", "raw/drop/a.parquet"}, r.fired(t))
		})
	}
}

// scenario: source-delete-deletes-record
func TestScenarioSourceDeleteDeletesRecord(t *testing.T) {
	t.Parallel()
	r := newBlobRig(t, nil)
	createBucket(t, r.st, "raw")
	r.lister.set("raw", object("drop/a.parquet"))
	createBlobSource(t, r.st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"}, v1.BlobEvent{Name: "other", Prefix: "other/"})
	r.reconcile(t, "drops")
	r.poll()
	require.Len(t, r.records(t, "drops"), 2)

	deleteObject(t, r.st, v1.KindEventSource, "drops")
	r.reconcile(t, "drops")
	require.Empty(t, r.records(t, "drops"))
	require.Equal(t, 0, r.watcher.ActiveWatches())
}

// scenario: kind-change-deletes-record
func TestScenarioKindChangeDeletesRecord(t *testing.T) {
	t.Parallel()
	r := newBlobRig(t, nil)
	createBucket(t, r.st, "raw")
	r.lister.set("raw", object("drop/a.parquet"), object("drop/b.parquet"))
	createBlobSource(t, r.st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"})
	r.reconcile(t, "drops")
	r.poll()
	require.NotEmpty(t, r.records(t, "drops"))

	blobSpec := getSource(t, r.st, "drops").Spec.Blob
	r.update(t, "drops", func(es *v1.EventSource) {
		es.Spec.Blob, es.Spec.Timer = nil, &v1.TimerSource{Events: []v1.TimerEvent{timerEvent("tick", time.Minute)}}
	})
	r.reconcile(t, "drops")
	require.Empty(t, r.records(t, "drops"))

	r.update(t, "drops", func(es *v1.EventSource) { es.Spec.Blob, es.Spec.Timer = blobSpec, nil })
	r.reconcile(t, "drops")
	r.poll()
	require.Len(t, r.fired(t), 4, "back to blob:, every object fires")
}

// scenario: bucket-miss-keeps-record
func TestScenarioBucketMissKeepsRecord(t *testing.T) {
	t.Parallel()
	r := newBlobRig(t, nil)
	createBucket(t, r.st, "raw")
	r.lister.set("raw", object("drop/a.parquet"))
	createBlobSource(t, r.st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"})
	r.reconcile(t, "drops")
	r.poll()

	deleteObject(t, r.st, v1.KindBucket, "raw")
	r.reconcile(t, "drops")
	require.Equal(t, 0, r.watcher.ActiveWatches())
	require.NotEmpty(t, r.records(t, "drops"), "a missing Bucket keeps the record")

	createBucket(t, r.st, "raw")
	r.reconcile(t, "drops")
	r.poll()
	require.Len(t, r.fired(t), 1, "the returning Bucket fires nothing again")
}

// scenario: start-sweep-deletes-orphans
func TestScenarioStartSweepDeletesOrphans(t *testing.T) {
	t.Parallel()
	r := newBlobRig(t, nil)
	wm, err := eventing.NewKVWatermark(r.kv)
	require.NoError(t, err)
	rec := eventing.SeenList{Bucket: "raw", Prefix: "drop/", Seen: map[string]string{"drop/a": "1-1"}}
	for _, src := range []v1.ObjectName{"drops", "drops2"} {
		require.NoError(t, wm.Save(context.Background(), "team-a", src, "arrived", rec))
	}
	createBlobSource(t, r.st, "drops2", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"})

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- r.watcher.Run(ctx) }()
	require.Eventually(t, func() bool { return len(r.records(t, "drops")) == 0 }, 5*time.Second, 5*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-stopped, context.Canceled)
	require.Len(t, r.records(t, "drops2"), 1, "a source the store holds keeps its records")
}

// failingSave is a MemWatermark whose Save fails while fail is set.
type failingSave struct {
	*eventing.MemWatermark
	fail atomic.Bool
}

func (f *failingSave) Save(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName, s eventing.SeenList) error {
	if f.fail.Load() {
		return errors.New("value too large")
	}
	return f.MemWatermark.Save(ctx, ns, source, event, s)
}

// scenario: save-failure-visible
func TestScenarioSaveFailureVisible(t *testing.T) {
	t.Parallel()
	wm := &failingSave{MemWatermark: eventing.NewMemWatermark()}
	r := newBlobRig(t, wm)
	createBucket(t, r.st, "raw")
	r.lister.set("raw", object("drop/a.parquet"))
	createBlobSource(t, r.st, "drops", "raw", v1.BlobEvent{Name: "arrived", Prefix: "drop/"})
	r.reconcile(t, "drops")

	wm.fail.Store(true)
	r.poll()
	cond, ok := getSource(t, r.st, "drops").Status.Conditions.Get("SeenListSaved")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "SaveFailed", cond.Reason)
	require.Equal(t, "arrived: value too large", cond.Message)

	wm.fail.Store(false)
	r.poll()
	_, ok = getSource(t, r.st, "drops").Status.Conditions.Get("SeenListSaved")
	require.False(t, ok, "a successful Save removes the condition")

	wm.fail.Store(true)
	r.lister.set("raw", object("drop/a.parquet"), object("drop/b.parquet"))
	r.poll()
	_, ok = getSource(t, r.st, "drops").Status.Conditions.Get("SeenListSaved")
	require.True(t, ok)
	deleteObject(t, r.st, v1.KindBucket, "raw")
	r.reconcile(t, "drops")
	_, ok = getSource(t, r.st, "drops").Status.Conditions.Get("SeenListSaved")
	require.False(t, ok, "the BucketNotFound status write removes the condition")
}
