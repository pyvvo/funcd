package addon_test

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/services/addon"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// ADR-0345: a Function of the namespace reconciles an engineless kind's objects, and a deletion waits for its destroy.

// gotObjects is one call the fake objects caller got.
type gotObjects struct {
	fn   v1.ObjectName
	call addon.ObjectsCall
}

// fakeObjectsCaller records each call and answers every object ok, or with answer when set, which may block on ctx.
type fakeObjectsCaller struct {
	mu     sync.Mutex
	calls  []gotObjects
	answer func(ctx context.Context, call addon.ObjectsCall) (addon.SettingsReply, error)
}

func (f *fakeObjectsCaller) Call(ctx context.Context, _ v1.NamespaceName, fn v1.ObjectName, call addon.ObjectsCall) (addon.SettingsReply, error) {
	f.mu.Lock()
	f.calls = append(f.calls, gotObjects{fn, call})
	answer := f.answer
	f.mu.Unlock()
	if answer != nil {
		return answer(ctx, call)
	}
	return answerAll(call, true, ""), nil
}

func (f *fakeObjectsCaller) got() []gotObjects {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeObjectsCaller) set(answer func(context.Context, addon.ObjectsCall) (addon.SettingsReply, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = answer
}

// answerAll answers each object of call with ok and reason.
func answerAll(call addon.ObjectsCall, ok bool, reason string) addon.SettingsReply {
	var r addon.SettingsReply
	for _, o := range call.Objects {
		r.Results = append(r.Results, addon.SettingsResult{Name: o.Name, OK: ok, Reason: reason})
	}
	return r
}

const stackSchema = `{"type":"object","properties":{"job":{"type":"string"}},"additionalProperties":false}`

// enginelessRig is the Addon infrastack, whose engineless kind InfraStack its Function infrastack-reconciler of
// namespace default reconciles; its clock moves only when a test moves it.
type enginelessRig struct {
	t         *testing.T
	ctx       context.Context
	st        store.Store
	r         *addon.Reconciler
	caller    *fakeObjectsCaller
	ended     chan controller.Request
	mu        sync.Mutex
	clock     time.Time
	collected []v1.NamespaceName
	// follow builds the reconciler as funcdctl dev does: every object follows its Addon's current revision.
	follow bool
}

func newEnginelessRig(t *testing.T) *enginelessRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	g := &enginelessRig{t: t, ctx: ctx, st: store.New(memory.New()), caller: &fakeObjectsCaller{},
		ended: make(chan controller.Request, 64), clock: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}
	t.Cleanup(func() { cancel(); _ = g.st.Close() })
	register(t, g.st, infraStackAddon())
	g.r = g.reconciler()
	g.function("infrastack-reconciler")
	return g
}

func infraStackAddon() *v1.Addon {
	return &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddon}, ObjectMeta: v1.ObjectMeta{Name: "infrastack"},
		Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "infrastack", Workloads: []v1.Workload{v1.WorkloadEngineless},
			Attributes: &v1.AddonAttributes{Kind: "InfraStack", Config: json.RawMessage(stackSchema),
				Function: &v1.AddonSettingsFunction{Ref: "infrastack-reconciler"}, Resync: v1.Duration(time.Minute)}}}}}
}

// reconciler is a reconciler of the rig's store, as a funcd start builds one: it knows nothing of earlier calls.
func (g *enginelessRig) reconciler() *addon.Reconciler {
	r, err := addon.NewReconciler(addon.Deps{Store: g.st, Provider: readyRuntime{}, RunsImages: true, SupervisionPeriod: time.Hour,
		Now: g.now, FollowCurrent: g.follow, Collect: func(_ context.Context, ns v1.NamespaceName) error {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.collected = append(g.collected, ns)
			return nil
		}})
	require.NoError(g.t, err)
	r.SetSettingsCaller(nil, func(req controller.Request) { g.ended <- req }, time.Second)
	r.SetObjectsCaller(g.caller)
	return r
}

func (g *enginelessRig) now() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.clock
}

func (g *enginelessRig) advance(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clock = g.clock.Add(d)
}

func (g *enginelessRig) function(name v1.ObjectName) {
	g.t.Helper()
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = name, "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/reconciler.mjs"
	created, err := g.st.Create(g.ctx, fn)
	require.NoError(g.t, err)
	fn = created.(*v1.Function)
	fn.Status.CurrentRevision = string(name) + "-1"
	_, err = g.st.Update(g.ctx, fn)
	require.NoError(g.t, err)
}

// stack stores the InfraStack name with deletion, its own Function for its kind when own is set.
func (g *enginelessRig) stack(name v1.ObjectName, deletion v1.DeletionPolicy, own v1.ObjectName) {
	g.t.Helper()
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Kind, o.Spec.Deletion = name, "default", "rg1", "InfraStack", deletion
	o.Spec.Config = json.RawMessage(`{"job":"` + string(name) + `"}`)
	if own != "" {
		o.Spec.Functions = []v1.AddonObjectFunction{{Kind: "InfraStack", Ref: own}}
	}
	_, err := g.st.Create(g.ctx, o)
	require.NoError(g.t, err)
}

// stored is the object name and whether it exists.
func (g *enginelessRig) stored(name v1.ObjectName) (*v1.AddonObject, bool) {
	g.t.Helper()
	obj, err := g.st.Get(g.ctx, v1.KindAddonObject.GVK(), "default", name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, false
	}
	require.NoError(g.t, err)
	return obj.(*v1.AddonObject), true
}

func (g *enginelessRig) object(name v1.ObjectName) *v1.AddonObject {
	g.t.Helper()
	o, ok := g.stored(name)
	require.True(g.t, ok, "%s exists", name)
	return o
}

// del deletes name as every delete path does, through gc.DeleteAddonObject, and reports whether it is held.
func (g *enginelessRig) del(name v1.ObjectName) bool {
	g.t.Helper()
	held, err := gc.DeleteAddonObject(g.ctx, g.st, g.object(name))
	require.NoError(g.t, err)
	return held != nil
}

func (g *enginelessRig) pass(name v1.ObjectName) controller.Result {
	g.t.Helper()
	res, err := g.r.Reconcile(g.ctx, controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: name})
	require.NoError(g.t, err)
	return res
}

// marked passes name until its first pass has stored its mark.
func (g *enginelessRig) marked(name v1.ObjectName) {
	g.t.Helper()
	g.pass(name)
	require.Equal(g.t, v1.WorkloadEngineless, g.object(name).Status.Workload)
}

// callEnds waits for the end of a call that sent name, which requeues it once it has recorded the end.
func (g *enginelessRig) callEnds(name v1.ObjectName) {
	g.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case req := <-g.ended:
			if req.Name == name {
				return
			}
		case <-deadline:
			require.Failf(g.t, "no call ended", "for %s", name)
		}
	}
}

func (g *enginelessRig) requireReason(name v1.ObjectName, reason string) {
	g.t.Helper()
	o := g.object(name)
	c, _ := o.Status.Conditions.Get("Ready")
	require.Equal(g.t, reason, c.Reason, "%s: %s", name, c.Message)
	require.Equal(g.t, v1.PhasePending, o.Status.Phase)
}

// lastCall is the last call the Function got.
func (g *enginelessRig) lastCall() gotObjects {
	g.t.Helper()
	calls := g.caller.got()
	require.NotEmpty(g.t, calls)
	return calls[len(calls)-1]
}

// names are the objects of a call, by name, with "~" before one sent as deleting.
func names(c addon.ObjectsCall) []string {
	out := make([]string, len(c.Objects))
	for i, o := range c.Objects {
		out[i] = string(o.Name)
		if o.Deleting {
			out[i] = "~" + out[i]
		}
	}
	return out
}

// scenario: engineless-apply — bunny-edge is Pending with Progressing, its first pass stores its mark and sends
// nothing, the next sends the Function a list that holds it, and it reports the answer's reason, then Ready at its
// generation.
func TestScenarioEnginelessApply(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("bunny-edge", v1.DeletionDelete, "")
	g.marked("bunny-edge")
	g.requireReason("bunny-edge", "Progressing")
	require.Empty(t, g.caller.got(), "no call carries an object before its mark is stored")

	g.caller.set(func(_ context.Context, c addon.ObjectsCall) (addon.SettingsReply, error) {
		return answerAll(c, false, "AwaitingApproval"), nil
	})
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	c := g.lastCall()
	require.Equal(t, v1.ObjectName("infrastack-reconciler"), c.fn)
	require.Equal(t, "InfraStack", c.call.Kind)
	require.Equal(t, "start", c.call.Cause)
	require.Len(t, c.call.Objects, 1)
	o := g.object("bunny-edge")
	require.Equal(t, addon.CallObject{Name: "bunny-edge", UID: o.UID, Generation: 1, Config: json.RawMessage(`{"job":"bunny-edge"}`)},
		c.call.Objects[0])
	g.requireReason("bunny-edge", "AwaitingApproval")

	g.caller.set(nil)
	g.advance(2 * time.Minute)
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	o = g.object("bunny-edge")
	ready, _ := o.Status.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionTrue, ready.Status)
	require.Equal(t, v1.PhaseReady, o.Status.Phase)
	require.Equal(t, int64(1), ready.ObservedGeneration)
}

// scenario: engineless-groups — of two InfraStacks, the one that names bunny-edge-reconciler goes to that Function
// alone, and the other to the kind's Function.
func TestScenarioEnginelessGroups(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.function("bunny-edge-reconciler")
	g.stack("bunny-edge", v1.DeletionRetain, "bunny-edge-reconciler")
	g.stack("site", v1.DeletionRetain, "")
	for _, name := range []v1.ObjectName{"bunny-edge", "site"} {
		g.marked(name)
		g.pass(name)
		g.callEnds(name)
	}
	byFn := map[v1.ObjectName][]string{}
	for _, c := range g.caller.got() {
		byFn[c.fn] = names(c.call)
	}
	require.Equal(t, map[v1.ObjectName][]string{"bunny-edge-reconciler": {"bunny-edge"}, "infrastack-reconciler": {"site"}}, byFn)
}

// scenario: engineless-resync — a Ready bunny-edge is sent again, cause resync, once the kind's resync elapses.
func TestScenarioEnginelessResync(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("bunny-edge", v1.DeletionRetain, "")
	g.marked("bunny-edge")
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	res := g.pass("bunny-edge")
	require.Len(t, g.caller.got(), 1, "nothing changed")
	require.Equal(t, time.Minute, res.RequeueAfter, "the pass comes back when the resync is due")
	g.advance(time.Minute)
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	require.Equal(t, "resync", g.lastCall().call.Cause)
}

// scenario: engineless-run-ends — a JobRun of the namespace that ends makes its groups due at once, cause run; one
// whose call is in flight is called once that call ends.
func TestScenarioEnginelessRunEnds(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("bunny-edge", v1.DeletionRetain, "")
	g.marked("bunny-edge")
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")

	run := &v1.JobRun{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindJobRun}}
	run.Name, run.Namespace, run.Spec.Job = "bunny-edge-apply-1", "default", "bunny-edge"
	run.Status.Phase = v1.JobRunRunning
	require.Empty(t, g.r.MapJobRun(g.ctx, run), "a run that has not ended changes nothing")
	run.Status.Phase = v1.JobRunSucceeded
	reqs := g.r.MapJobRun(g.ctx, run)
	require.Equal(t, []controller.Request{{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "bunny-edge"}}, reqs)
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	require.Equal(t, "run", g.lastCall().call.Cause)

	release := make(chan struct{})
	g.caller.set(func(ctx context.Context, c addon.ObjectsCall) (addon.SettingsReply, error) {
		<-release
		return answerAll(c, true, ""), nil
	})
	g.advance(time.Minute)
	g.pass("bunny-edge") // a resync call, held in flight
	require.Eventually(t, func() bool { return len(g.caller.got()) == 3 }, 5*time.Second, 5*time.Millisecond)
	g.r.MapJobRun(g.ctx, run)
	g.pass("bunny-edge")
	require.Len(t, g.caller.got(), 3, "no second call while one runs")
	g.caller.set(nil)
	close(release)
	g.callEnds("bunny-edge")
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	require.Equal(t, "run", g.lastCall().call.Cause, "the run's end is sent once the call in flight ended")
}

// scenario: engineless-waits — with no Function infrastack-reconciler, bunny-edge is Pending with FunctionNotFound and
// no call carries it; with its Addon deleted after its first pass, AddonNotFound; deleted meanwhile, it is held until
// they are back, or until retain is set.
func TestScenarioEnginelessWaits(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	require.NoError(t, g.st.Delete(g.ctx, v1.KindFunction.GVK(), "default", "infrastack-reconciler", ""))
	g.stack("bunny-edge", v1.DeletionDelete, "")
	g.marked("bunny-edge")
	g.pass("bunny-edge")
	g.requireReason("bunny-edge", "FunctionNotFound")
	require.True(t, g.del("bunny-edge"), "a marked object with deletion: delete is held")
	g.pass("bunny-edge")
	g.requireReason("bunny-edge", "FunctionNotFound")
	require.Empty(t, g.caller.got(), "no call carries it")

	g.function("infrastack-reconciler")
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	require.Equal(t, []string{"~bunny-edge"}, names(g.lastCall().call), "once its Function is back, the call carries it as deleting")

	g.stack("site", v1.DeletionDelete, "")
	g.marked("site")
	require.NoError(t, g.st.Delete(g.ctx, v1.KindAddon.GVK(), "", "infrastack", ""))
	g.pass("site")
	g.requireReason("site", "AddonNotFound")
	require.True(t, g.del("site"))
	g.pass("site")
	g.requireReason("site", "AddonNotFound")
	o := g.object("site")
	o.Spec.Deletion = v1.DeletionRetain
	_, err := g.st.Update(g.ctx, o)
	require.NoError(t, err)
	g.pass("site")
	_, exists := g.stored("site")
	require.False(t, exists, "retain lets a held object go, with its Addon gone")
}

// scenario: engineless-delete-held — bunny-edge, deleted past its first pass with deletion: delete, stays with
// metadata.deletionTimestamp set and Deleting; its next call carries it as deleting, it reports the Function's
// Destroying, and it goes on the Function's ok. Deleted before its first pass, an object goes at once.
func TestScenarioEnginelessDeleteHeld(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("early", v1.DeletionDelete, "")
	require.False(t, g.del("early"), "with no mark, no call can have carried it")
	_, exists := g.stored("early")
	require.False(t, exists)

	g.stack("bunny-edge", v1.DeletionDelete, "")
	g.marked("bunny-edge")
	require.True(t, g.del("bunny-edge"), "held whether or not a call carried it")
	require.True(t, g.del("bunny-edge"), "a second delete finds it held")
	o := g.object("bunny-edge")
	require.NotNil(t, o.DeletionTime)
	release := make(chan struct{})
	g.caller.set(func(_ context.Context, c addon.ObjectsCall) (addon.SettingsReply, error) {
		<-release
		return answerAll(c, false, "Destroying"), nil
	})
	g.pass("bunny-edge")
	g.requireReason("bunny-edge", "Deleting")
	close(release)
	g.callEnds("bunny-edge")
	require.Equal(t, []string{"~bunny-edge"}, names(g.lastCall().call))
	g.requireReason("bunny-edge", "Destroying")

	g.caller.set(nil)
	g.advance(time.Minute)
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	_, exists = g.stored("bunny-edge")
	require.False(t, exists, "the ok to a call that carried it as deleting removes it")
}

// scenario: engineless-delete-in-flight — an ok to a call that carried bunny-edge live, which answers after its
// deletion, removes nothing: it stays held, and the next call carries it as deleting.
// oracle: docs/design/addon/engineless-delete.fizz#NoStackGoesUndestroyed
func TestScenarioEnginelessDeleteInFlight(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("bunny-edge", v1.DeletionDelete, "")
	g.marked("bunny-edge")
	release := make(chan struct{})
	g.caller.set(func(ctx context.Context, c addon.ObjectsCall) (addon.SettingsReply, error) {
		<-release
		return answerAll(c, true, ""), nil
	})
	g.pass("bunny-edge")
	require.Eventually(t, func() bool { return len(g.caller.got()) == 1 }, 5*time.Second, 5*time.Millisecond)
	require.True(t, g.del("bunny-edge"))
	close(release)
	g.callEnds("bunny-edge")
	o, exists := g.stored("bunny-edge")
	require.True(t, exists, "a call sent before the deletion removes nothing")
	require.NotNil(t, o.DeletionTime)

	g.caller.set(nil)
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	calls := g.caller.got()
	require.Equal(t, []string{"bunny-edge"}, names(calls[0].call))
	require.Equal(t, []string{"~bunny-edge"}, names(calls[1].call))
	require.Equal(t, "change", calls[1].call.Cause)
	_, exists = g.stored("bunny-edge")
	require.False(t, exists)
}

// scenario: engineless-retain — with retain, the default, bunny-edge goes at once and the next list leaves it out; a
// deleting one set to retain goes at its next pass with no call, and one written otherwise stays deleting.
func TestScenarioEnginelessRetain(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("bunny-edge", "", "")
	g.stack("site", v1.DeletionRetain, "")
	for _, name := range []v1.ObjectName{"bunny-edge", "site"} {
		g.marked(name)
	}
	g.pass("site")
	g.callEnds("site")
	require.Equal(t, []string{"bunny-edge", "site"}, names(g.lastCall().call))
	require.False(t, g.del("bunny-edge"), "retain, the default, holds nothing")
	g.pass("site")
	g.callEnds("site")
	require.Equal(t, []string{"site"}, names(g.lastCall().call), "the next list leaves it out")

	g.stack("held", v1.DeletionDelete, "")
	g.marked("held")
	require.True(t, g.del("held"))
	o := g.object("held")
	o.Spec.Config = json.RawMessage(`{"job":"other"}`)
	_, err := g.st.Update(g.ctx, o)
	require.NoError(t, err)
	require.NotNil(t, g.object("held").DeletionTime, "a write to a deleting object keeps it deleting")
	calls := len(g.caller.got())
	o = g.object("held")
	o.Spec.Deletion = v1.DeletionRetain
	_, err = g.st.Update(g.ctx, o)
	require.NoError(t, err)
	g.pass("held")
	_, exists := g.stored("held")
	require.False(t, exists, "retain lets it go")
	require.Len(t, g.caller.got(), calls, "with no call")
}

// scenario: engineless-restart — after funcd stops while bunny-edge is deleting, the first pass sends the list,
// cause start, with bunny-edge deleting, and the hold goes on until the Function's ok.
// oracle: docs/design/addon/engineless-delete.fizz#DeletedAppGoes
func TestScenarioEnginelessRestart(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("bunny-edge", v1.DeletionDelete, "")
	g.marked("bunny-edge")
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	require.True(t, g.del("bunny-edge"))

	g.r = g.reconciler() // funcd stops and starts again: what it knew of its calls is gone, the hold is stored
	g.caller.set(func(_ context.Context, c addon.ObjectsCall) (addon.SettingsReply, error) {
		return answerAll(c, false, "Destroying"), nil
	})
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	c := g.lastCall()
	require.Equal(t, "start", c.call.Cause)
	require.Equal(t, []string{"~bunny-edge"}, names(c.call))
	g.requireReason("bunny-edge", "Destroying")

	g.caller.set(nil)
	g.advance(time.Minute)
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	_, exists := g.stored("bunny-edge")
	require.False(t, exists, "the hold ends on the Function's ok")
}

// ADR-0345 Decision 8: once an App's last engineless part is gone, the reconciler asks the collector for a pass over
// its namespace, so the App's other parts go then.
func TestEnginelessPartGoneCollectsItsNamespace(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("bunny-edge", v1.DeletionDelete, "")
	o := g.object("bunny-edge")
	o.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Name: "edge"}, UID: "app-uid", Controller: true}}
	o.Status.AddonRevision = v1.AddonRevisionName("infrastack", 1) // an App's part pins its App revision's record
	_, err := g.st.Update(g.ctx, o)
	require.NoError(t, err)
	g.marked("bunny-edge")
	require.True(t, g.del("bunny-edge"))
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	_, exists := g.stored("bunny-edge")
	require.False(t, exists)
	g.pass("bunny-edge") // the deletion's event
	g.mu.Lock()
	defer g.mu.Unlock()
	require.Equal(t, []v1.NamespaceName{"default"}, g.collected)
}

// ADR-0345 Decision 2: a Function's change requeues the engineless objects it reconciles, by spec.functions or by its
// kind's function.ref.
func TestEnginelessFunctionChangeRequeuesItsObjects(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("bunny-edge", v1.DeletionRetain, "bunny-edge-reconciler")
	g.stack("site", v1.DeletionRetain, "")
	fn := &v1.Function{ObjectMeta: v1.ObjectMeta{Name: "infrastack-reconciler", Namespace: "default"}}
	require.Equal(t, []controller.Request{{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "site"}},
		g.r.MapEnginelessFunction(g.ctx, fn))
	fn.Name = "bunny-edge-reconciler"
	require.Equal(t, []controller.Request{{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "bunny-edge"}},
		g.r.MapEnginelessFunction(g.ctx, fn))
}

// ADR-0345 Decision 1: an object's Function is its pinned revision's, so a Function's change requeues an object that
// pins an older revision naming it, after the Addon's current function.ref moved to another Function.
func TestEnginelessFunctionChangeRequeuesByPinnedRevision(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("site", v1.DeletionRetain, "")
	g.marked("site")
	require.Equal(t, v1.AddonRevisionName("infrastack", 1), g.object("site").Status.AddonRevision)
	cur, err := g.st.Get(g.ctx, v1.KindAddon.GVK(), "", "infrastack")
	require.NoError(t, err)
	a := cur.(*v1.Addon)
	a.Spec.Runtimes[0].Attributes.Function = &v1.AddonSettingsFunction{Ref: "next-reconciler"}
	_, err = g.st.Update(g.ctx, a)
	require.NoError(t, err)
	fn := &v1.Function{ObjectMeta: v1.ObjectMeta{Name: "infrastack-reconciler", Namespace: "default"}}
	require.Equal(t, []controller.Request{{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "site"}},
		g.r.MapEnginelessFunction(g.ctx, fn), "site pins revision 1, whose Function this is")
}

// ADR-0339 Decision 3, as ADR-0345 Decision 1 applies it to an engineless object: an App's part whose spec an upgrade
// leaves unchanged still follows the App's newer applied revision. The pass stores the moved pin although the part's
// Ready condition does not change, and the next call's answer keeps it.
func TestEnginelessPartPinFollowsItsApp(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	const appUID v1.UID = "edge-uid"
	app := v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: "default", Name: "edge"}, UID: appUID,
		Controller: true, BlockOwnerDeletion: true}
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Kind = "bunny", "default", "rg1", "InfraStack"
	o.Spec.Config, o.OwnerReferences = json.RawMessage(`{"job":"bunny"}`), []v1.OwnerReference{app}
	_, err := g.st.Create(g.ctx, o)
	require.NoError(t, err)
	applied := func(n int64) {
		t.Helper()
		r := &v1.AppRevision{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAppRevision}}
		r.Name, r.Namespace, r.ResourceGroup, r.OwnerReferences = v1.AppRevisionName("edge", n), "default", "rg1", []v1.OwnerReference{app}
		r.Spec = v1.AppRevisionSpec{App: app.ObjectRef, Number: n}
		created, err := g.st.Create(g.ctx, r)
		require.NoError(t, err)
		r = created.(*v1.AppRevision)
		r.Status.Conditions.Set(v1.Condition{Type: "Applied", Status: v1.ConditionTrue})
		_, err = g.st.Update(g.ctx, r)
		require.NoError(t, err)
	}
	applied(1)
	g.caller.set(func(_ context.Context, c addon.ObjectsCall) (addon.SettingsReply, error) {
		return answerAll(c, true, ""), nil
	})
	g.marked("bunny")
	g.pass("bunny")
	g.callEnds("bunny")
	require.Equal(t, v1.PhaseReady, g.object("bunny").Status.Phase)
	require.Equal(t, v1.ObjectName("edge-1"), g.object("bunny").Status.AppRevision)

	applied(2)
	g.pass("bunny")
	got := g.object("bunny")
	require.Equal(t, v1.ObjectName("edge-2"), got.Status.AppRevision, "the part follows its App's applied revision")
	require.Equal(t, v1.PhaseReady, got.Status.Phase, "a moved pin leaves the answer for its generation")

	g.advance(2 * time.Minute)
	g.pass("bunny")
	g.callEnds("bunny")
	require.Equal(t, v1.ObjectName("edge-2"), g.object("bunny").Status.AppRevision, "the next answer keeps the stored pin")
}

// ADR-0339 Decision 7, under funcdctl dev, as ADR-0345 Decision 1 applies it to an engineless object: the object follows
// its Addon's current revision, and the pass stores the moved AddonRevision although its App revision, which it has
// none of, and its Ready condition do not change.
func TestEnginelessPinFollowsTheCurrentRevisionUnderDev(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.follow = true
	g.r = g.reconciler()
	g.stack("bunny", v1.DeletionRetain, "")
	g.caller.set(func(_ context.Context, c addon.ObjectsCall) (addon.SettingsReply, error) {
		return answerAll(c, true, ""), nil
	})
	g.marked("bunny")
	g.pass("bunny")
	g.callEnds("bunny")
	require.Equal(t, v1.PhaseReady, g.object("bunny").Status.Phase)
	require.Equal(t, v1.AddonRevisionName("infrastack", 1), g.object("bunny").Status.AddonRevision)

	obj, err := g.st.Get(g.ctx, v1.KindAddon.GVK(), "", "infrastack")
	require.NoError(t, err)
	a := obj.(*v1.Addon)
	a.Spec.Runtimes[0].Attributes.Resync = v1.Duration(2 * time.Minute)
	_, err = g.st.Update(g.ctx, a)
	require.NoError(t, err)
	_, err = g.r.Revisions().Reconcile(g.ctx, controller.Request{GVK: v1.KindAddon.GVK(), Name: "infrastack"})
	require.NoError(t, err)
	obj, err = g.st.Get(g.ctx, v1.KindAddon.GVK(), "", "infrastack")
	require.NoError(t, err)
	current := obj.(*v1.Addon).Status.CurrentRevision
	require.NotEqual(t, v1.AddonRevisionName("infrastack", 1), current, "the change stamps a new current revision")

	g.pass("bunny")
	got := g.object("bunny")
	require.Equal(t, current, got.Status.AddonRevision, "the moved AddonRevision is stored")
	require.Empty(t, got.Status.AppRevision)
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
}
