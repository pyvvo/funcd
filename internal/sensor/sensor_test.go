package sensor_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/sensor"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// fakeInvoker records the functions it was asked to invoke (a real Invoker stub, no mock framework).
type fakeInvoker struct {
	mu    sync.Mutex
	calls []v1.ObjectName
}

func (f *fakeInvoker) Invoke(_ context.Context, _ v1.NamespaceName, fn v1.ObjectName, _ eventing.CloudEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fn)
	return nil
}
func (f *fakeInvoker) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func harness(t *testing.T) (store.Store, *eventing.Fanout, *fakeInvoker, *sensor.Reconciler) {
	t.Helper()
	st := store.New(memory.New())
	fan := eventing.NewFanout()
	inv := &fakeInvoker{}
	r, err := sensor.NewReconciler(sensor.Deps{Store: st, Subscriber: fan, Invoker: inv})
	require.NoError(t, err)
	return st, fan, inv, r
}

func dep(name, source, event string) v1.Dependency {
	return v1.Dependency{Name: v1.ObjectName(name), Source: v1.ObjectName(source), Event: v1.ObjectName(event)}
}

func createSensor(t *testing.T, st store.Store, name string, on []v1.Dependency, do []v1.Action) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindSensor)
	require.True(t, ok)
	se := obj.(*v1.Sensor)
	se.Name, se.Namespace, se.ResourceGroup = v1.ObjectName(name), "team-a", "rg1"
	se.Spec.On, se.Spec.Do = on, do
	_, err := st.Create(context.Background(), se)
	require.NoError(t, err)
}

func reqOf(name string) controller.Request {
	return controller.Request{GVK: v1.KindSensor.GVK(), Namespace: "team-a", Name: v1.ObjectName(name)}
}

// fire publishes a named CloudEvent (optionally with data) as if the EventSource ticked.
func fire(t *testing.T, fan *eventing.Fanout, source, event, data string) {
	t.Helper()
	ev, err := eventing.NewNamedEvent("team-a", v1.ObjectName(source), v1.ObjectName(event))
	require.NoError(t, err)
	if data != "" {
		ev.Data = json.RawMessage(data)
	}
	require.NoError(t, fan.Publish(context.Background(), ev))
}

func runs(t *testing.T, st store.Store) []*v1.WorkflowRun {
	t.Helper()
	list, err := st.List(context.Background(), v1.KindWorkflowRun.GVK(), store.ListOptions{})
	require.NoError(t, err)
	out := make([]*v1.WorkflowRun, 0, len(list.Items))
	for _, o := range list.Items {
		out = append(out, o.(*v1.WorkflowRun))
	}
	return out
}

func invocations(t *testing.T, st store.Store) int {
	t.Helper()
	list, err := st.List(context.Background(), v1.KindInvocation.GVK(), store.ListOptions{})
	require.NoError(t, err)
	return len(list.Items)
}

// scenario: sensor-reconciles-ready — a well-formed Sensor reaches Ready and subscribes (proven by a
// subsequent firing triggering the action).
func TestSensorReconcilesReady(t *testing.T) {
	st, fan, _, r := harness(t)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "nightly", "tick")},
		[]v1.Action{{Name: "run", On: "d", Workflow: "wf"}})
	_, err := r.Reconcile(context.Background(), reqOf("s"))
	require.NoError(t, err)
	obj, _ := st.Get(context.Background(), v1.KindSensor.GVK(), "team-a", "s")
	c, ok := obj.(*v1.Sensor).Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionTrue, c.Status)
	fire(t, fan, "nightly", "tick", "")
	require.Len(t, runs(t, st), 1, "the subscribed action fired")
}

// scenario: event-starts-workflow — a firing creates a WorkflowRun of the action's workflow.
func TestEventStartsWorkflow(t *testing.T) {
	st, fan, _, r := harness(t)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "build", On: "d", Workflow: "ci"}})
	_, _ = r.Reconcile(context.Background(), reqOf("s"))
	fire(t, fan, "git", "push", "")
	rs := runs(t, st)
	require.Len(t, rs, 1)
	require.Equal(t, v1.ObjectName("ci"), rs[0].Spec.Workflow)
}

// scenario: event-invokes-function — a firing invokes the function and records an Invocation.
func TestEventInvokesFunction(t *testing.T) {
	st, fan, inv, r := harness(t)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "notify", On: "d", Function: "mailer"}})
	_, _ = r.Reconcile(context.Background(), reqOf("s"))
	fire(t, fan, "git", "push", "")
	require.Equal(t, 1, inv.count(), "the function was invoked")
	require.Equal(t, 1, invocations(t, st), "an Invocation was recorded (never silently dropped)")
}

// scenario: input-projection — ${{ event.data.* }} + literals build the run input.
func TestInputProjection(t *testing.T) {
	st, fan, _, r := harness(t)
	input := json.RawMessage(`{"repo":"${{ event.data.repository }}","kind":"push"}`)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "build", On: "d", Workflow: "ci", Input: input}})
	_, _ = r.Reconcile(context.Background(), reqOf("s"))
	fire(t, fan, "git", "push", `{"repository":"acme/x"}`)
	rs := runs(t, st)
	require.Len(t, rs, 1)
	var got map[string]interface{}
	require.NoError(t, json.Unmarshal(rs[0].Spec.Input, &got))
	require.Equal(t, "acme/x", got["repo"], "projected from event.data.repository")
	require.Equal(t, "push", got["kind"], "literal passed through")
}

// scenario: input-absent-passes-event-data — no input ⇒ the target gets the event data verbatim.
func TestInputAbsentPassesEventData(t *testing.T) {
	st, fan, _, r := harness(t)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "build", On: "d", Workflow: "ci"}})
	_, _ = r.Reconcile(context.Background(), reqOf("s"))
	fire(t, fan, "git", "push", `{"a":1}`)
	rs := runs(t, st)
	require.Len(t, rs, 1)
	require.JSONEq(t, `{"a":1}`, string(rs[0].Spec.Input))
}

// scenario: scheduled-workflow-start (F68) — a timer-tick firing on a workflow action creates a run.
func TestScheduledWorkflowStart(t *testing.T) {
	st, fan, _, r := harness(t)
	createSensor(t, st, "nightly-rollup", []v1.Dependency{dep("nightly", "clock", "tick")},
		[]v1.Action{{Name: "rollup", On: "nightly", Workflow: "daily-rollup"}})
	_, _ = r.Reconcile(context.Background(), reqOf("nightly-rollup"))
	fire(t, fan, "clock", "tick", "") // a timer tick
	rs := runs(t, st)
	require.Len(t, rs, 1)
	require.Equal(t, v1.ObjectName("daily-rollup"), rs[0].Spec.Workflow)
}

// scenario: bad-static-input-not-ready — a ${{ }} referencing a non-event root ⇒ NotReady, no subscription.
func TestBadStaticInputNotReady(t *testing.T) {
	st, fan, _, r := harness(t)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "build", On: "d", Workflow: "ci", Input: json.RawMessage(`{"x":"${{ foo.bar }}"}`)}})
	_, err := r.Reconcile(context.Background(), reqOf("s"))
	require.NoError(t, err)
	obj, _ := st.Get(context.Background(), v1.KindSensor.GVK(), "team-a", "s")
	c, ok := obj.(*v1.Sensor).Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "InvalidInput", c.Reason)
	fire(t, fan, "git", "push", "")
	require.Empty(t, runs(t, st), "a NotReady sensor is not subscribed")
}

// scenario: delete-unsubscribes — after delete, a firing triggers nothing.
func TestDeleteUnsubscribes(t *testing.T) {
	st, fan, _, r := harness(t)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "build", On: "d", Workflow: "ci"}})
	_, _ = r.Reconcile(context.Background(), reqOf("s"))
	cur, _ := st.Get(context.Background(), v1.KindSensor.GVK(), "team-a", "s")
	require.NoError(t, st.Delete(context.Background(), v1.KindSensor.GVK(), "team-a", "s", cur.GetObjectMeta().ResourceVersion))
	_, err := r.Reconcile(context.Background(), reqOf("s"))
	require.NoError(t, err)
	fire(t, fan, "git", "push", "")
	require.Empty(t, runs(t, st), "a deleted sensor's subscriptions are cancelled")
}

// scenario: reconcile-is-idempotent — re-reconciling a Ready sensor keeps ONE subscription per dep; one
// firing ⇒ exactly one execution (no duplicate subscriptions from resync).
func TestReconcileIsIdempotent(t *testing.T) {
	st, fan, _, r := harness(t)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "build", On: "d", Workflow: "ci"}})
	for i := 0; i < 3; i++ { // repeated resyncs, no spec change
		_, err := r.Reconcile(context.Background(), reqOf("s"))
		require.NoError(t, err)
	}
	fire(t, fan, "git", "push", "")
	require.Len(t, runs(t, st), 1, "re-reconcile must not create duplicate subscriptions")
}

// scenario: stateless-independent-firings — two firings ⇒ two independent runs.
func TestStatelessIndependentFirings(t *testing.T) {
	st, fan, _, r := harness(t)
	createSensor(t, st, "s", []v1.Dependency{dep("d", "git", "push")},
		[]v1.Action{{Name: "build", On: "d", Workflow: "ci"}})
	_, _ = r.Reconcile(context.Background(), reqOf("s"))
	fire(t, fan, "git", "push", "")
	fire(t, fan, "git", "push", "")
	require.Len(t, runs(t, st), 2, "each firing is independent (no dedup)")
}
