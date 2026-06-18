package eventing_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/activator"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/eventing"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

// fakeEndpoints is a real Endpoints stub (no mock framework): a fixed upstream/ready.
type fakeEndpoints struct {
	upstream string
	ready    bool
}

func (f fakeEndpoints) Upstream(_ context.Context, _ activator.FunctionRef) (string, bool, error) {
	return f.upstream, f.ready, nil
}

func newStore() store.Store { return store.New(memory.New()) }

func createTimerSource(t *testing.T, st store.Store, name, fn string, interval time.Duration) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindEventSource)
	require.True(t, ok)
	es := obj.(*v1.EventSource)
	es.Name = v1.ObjectName(name)
	es.Namespace = "team-a"
	es.ResourceGroup = "rg1"
	es.Spec.Type = v1.EventSourceTypeTimer
	es.Spec.Timer = &v1.TimerSpec{Interval: interval}
	es.Spec.Function = v1.ObjectName(fn)
	_, err := st.Create(context.Background(), es)
	require.NoError(t, err)
}

func reqOf(name string) controller.Request {
	return controller.Request{GVK: v1.KindEventSource.GVK(), Namespace: "team-a", Name: v1.ObjectName(name)}
}

// scenario: timer-fires-and-invokes.
func TestScenarioTimerFiresAndInvokes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var mu sync.Mutex
	var gotBody []byte
	var gotCT string
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotCT, hits = b, r.Header.Get("Content-Type"), hits+1
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	st := newStore()
	createTimerSource(t, st, "nightly", "report", time.Minute)

	src, err := eventing.NewSource(eventing.Deps{
		Store:     st,
		Endpoints: fakeEndpoints{upstream: srv.URL, ready: true},
	})
	require.NoError(t, err)

	require.NoError(t, src.Fire(ctx, "team-a", "nightly"))

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, hits, "function upstream invoked exactly once")
	require.Equal(t, "application/cloudevents+json", gotCT)
	var ev eventing.CloudEvent
	require.NoError(t, json.Unmarshal(gotBody, &ev))
	require.Equal(t, "1.0", ev.SpecVersion)
	require.Equal(t, "io.funcd.timer.tick", ev.Type)
}

// scenario: invocation-recorded.
func TestScenarioInvocationRecorded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	st := newStore()
	createTimerSource(t, st, "nightly", "report", time.Minute)
	src, err := eventing.NewSource(eventing.Deps{Store: st, Endpoints: fakeEndpoints{upstream: srv.URL, ready: true}})
	require.NoError(t, err)

	require.NoError(t, src.Fire(ctx, "team-a", "nightly"))

	// a successful invoke records exactly one Ready Invocation (start/end stamped).
	list, err := st.List(ctx, v1.KindInvocation.GVK(), store.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	inv := list.Items[0].(*v1.Invocation)
	require.Equal(t, v1.PhaseReady, inv.Status.Phase)
	require.False(t, inv.Status.StartTime.IsZero())
	require.False(t, inv.Status.EndTime.IsZero())
}

// scenario: not-ready-trigger-not-dropped.
func TestScenarioNotReadyTriggerNotDropped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore()
	createTimerSource(t, st, "nightly", "report", time.Minute)

	src, err := eventing.NewSource(eventing.Deps{Store: st, Endpoints: fakeEndpoints{ready: false}})
	require.NoError(t, err)

	err = src.Fire(ctx, "team-a", "nightly")
	require.Error(t, err)
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "not-ready upstream → Unavailable")

	// recorded, never silently dropped.
	list, err := st.List(ctx, v1.KindInvocation.GVK(), store.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	inv := list.Items[0].(*v1.Invocation)
	require.Equal(t, v1.PhaseFailed, inv.Status.Phase)
	require.NotEmpty(t, inv.Status.Error)
}

// scenario: eventsource-reconciles-timer.
func TestScenarioEventSourceReconcilesTimer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore()
	createTimerSource(t, st, "nightly", "report", time.Minute)

	src, err := eventing.NewSource(eventing.Deps{Store: st, Endpoints: fakeEndpoints{ready: false}})
	require.NoError(t, err)

	// timer source → registered + Ready.
	_, err = src.Reconcile(ctx, reqOf("nightly"))
	require.NoError(t, err)
	require.Equal(t, 1, src.ActiveTimers())
	es, err := st.Get(ctx, v1.KindEventSource.GVK(), "team-a", "nightly")
	require.NoError(t, err)
	require.Equal(t, v1.PhaseReady, es.(*v1.EventSource).Status.Phase)

	// http source is ignored (not registered as a timer).
	obj, ok := v1.NewObject(v1.KindEventSource)
	require.True(t, ok)
	hes := obj.(*v1.EventSource)
	hes.Name, hes.Namespace, hes.ResourceGroup = "webhook", "team-a", "rg1"
	hes.Spec.Type = v1.EventSourceTypeHTTP
	hes.Spec.Function = "report" // the target function (required, ADR-0048)
	_, err = st.Create(ctx, hes)
	require.NoError(t, err)
	_, err = src.Reconcile(ctx, reqOf("webhook"))
	require.NoError(t, err)
	require.Equal(t, 1, src.ActiveTimers(), "http source not registered")

	// delete → deregistered.
	cur, err := st.Get(ctx, v1.KindEventSource.GVK(), "team-a", "nightly")
	require.NoError(t, err)
	require.NoError(t, st.Delete(ctx, v1.KindEventSource.GVK(), "team-a", "nightly", cur.GetObjectMeta().ResourceVersion))
	_, err = src.Reconcile(ctx, reqOf("nightly"))
	require.NoError(t, err)
	require.Equal(t, 0, src.ActiveTimers())
}

// fakeWaker wakes a cold function to a fixed upstream (the activator's role, ADR-0033).
type fakeWaker struct {
	upstream string
	calls    int
}

func (f *fakeWaker) Wake(_ context.Context, _ eventing.FunctionRef) (string, error) {
	f.calls++
	return f.upstream, nil
}

// scenario: timer-wakes-cold-function (ADR-0033) — a timer at a scaled-to-zero function
// wakes it via the Waker and POSTs the CloudEvent; a successful Invocation is recorded
// (closing ADR-0023's deferred C3 wake — no longer Unavailable).
func TestScenarioTimerWakesColdFunction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore()
	createTimerSource(t, st, "nightly", "report", time.Minute)

	var got int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	waker := &fakeWaker{upstream: srv.URL}
	src, err := eventing.NewSource(eventing.Deps{
		Store: st, Endpoints: fakeEndpoints{ready: false}, Waker: waker,
	})
	require.NoError(t, err)

	require.NoError(t, src.Fire(ctx, "team-a", "nightly"), "a cold function is woken, not dropped")
	require.Equal(t, 1, waker.calls, "the trigger woke the function")
	require.Equal(t, 1, got, "the CloudEvent reached the woken upstream")

	list, err := st.List(ctx, v1.KindInvocation.GVK(), store.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, v1.PhaseReady, list.Items[0].(*v1.Invocation).Status.Phase, "recorded as a successful invocation")
}
