package eventing_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/eventing"
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
