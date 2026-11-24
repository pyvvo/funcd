package app_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// ADR-0357 on the reconciler: an engineless part lags, a part that names a Secret one of the App's Jobs owns waits for
// its key, the deadline restarts when a wait ends, and the post-hooks and prune wait for every Lagging part.

// waitReason is the Lagging reason of a part that waits for todo-outputs's key (ADR-0357 Decision 2).
const waitReason = "SecretKeyMissing: Secret/todo-outputs FRP_TOKEN"

// withStack adds the stack todo-stack, an object of the engineless kind InfraStack (ADR-0345 Decision 1).
func withStack(a *v1.App) {
	a.Spec.Addons = []v1.AppAddon{{Name: "todo-stack", AddonObjectSpec: v1.AddonObjectSpec{Kind: "InfraStack"}}}
}

// withOutputs adds the Secret todo-outputs with the key FRP_TOKEN, the Job todo-tf that owns and binds it, and the
// Function todo-tunnel, which reads it.
func withOutputs(a *v1.App) {
	a.Spec.Secrets = append(a.Spec.Secrets, v1.SecretDeclaration{Name: "todo-outputs", Keys: []string{"FRP_TOKEN"}})
	a.Spec.Jobs = append(a.Spec.Jobs, v1.AppJob{Name: "todo-tf", JobSpec: v1.JobSpec{
		Runtime: "nodejs22", Handler: "handle", Image: "oci-layout://todo-tf:1",
		Bindings: v1.FunctionBindings{Secret: []v1.ObjectName{"todo-outputs"}},
	}})
	tunnel := apiSpec()
	tunnel.Bindings.Secret = []v1.ObjectName{"todo-outputs"}
	a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Name: "todo-tunnel", FunctionSpec: tunnel})
}

// stacks stores the Addon infrastack, whose engineless kind InfraStack todo-api reconciles, at its first revision.
func (h *harness) stacks() {
	h.t.Helper()
	a := h.create(&v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAddon.GVK().APIVersion(), Kind: v1.KindAddon},
		ObjectMeta: v1.ObjectMeta{Name: "infrastack"}, Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "infrastack",
			Workloads: []v1.Workload{v1.WorkloadEngineless}, Attributes: &v1.AddonAttributes{Kind: "InfraStack",
				Config: json.RawMessage(`{"type":"object"}`), Function: &v1.AddonSettingsFunction{Ref: "todo-api"}}}}}}).(*v1.Addon)
	a.Status.CurrentRevision = v1.AddonRevisionName("infrastack", 1)
	h.update(a)
}

// stackAnswers gives todo-stack the status its Function's answer writes at its current generation, with its first
// pass's mark: Ready when reason is "", else Pending with reason. A Ready stack pins the latest revision once it is
// applied, as the add-on reconciler moves it.
func (h *harness) stackAnswers(reason string) {
	h.t.Helper()
	if reason == "" {
		h.runPart("todo-stack")
	}
	o := h.get(v1.KindAddonObject, "todo-stack").(*v1.AddonObject)
	o.Status.Workload = v1.WorkloadEngineless
	if reason != "" {
		setStatus(o, v1.PhasePending, cond("Ready", v1.ConditionFalse, reason, o.Generation))
	}
	h.update(o)
}

// putOutputs creates or replaces the Secret todo-outputs, whose owner is owner, with data.
func (h *harness) putOutputs(owner v1.WorkloadRef, data map[string][]byte) {
	h.t.Helper()
	if cur := h.get(v1.KindSecret, "todo-outputs"); cur != nil {
		s := cur.(*v1.Secret)
		s.Spec.Owner, s.Spec.Data = owner, data
		h.update(s)
		return
	}
	h.create(&v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret},
		ObjectMeta: v1.ObjectMeta{Name: "todo-outputs", Namespace: ns, ResourceGroup: "rg1"},
		Spec:       v1.SecretSpec{Type: v1.SecretTypeOpaque, Owner: owner, Data: data}})
}

func frpToken() map[string][]byte { return map[string][]byte{"FRP_TOKEN": []byte("token-1")} }

// markReadyBut marks every part Ready but those named, which stay as they are.
func (h *harness) markReadyBut(names ...v1.ObjectName) {
	h.t.Helper()
	for _, c := range h.app().Status.Children {
		skip := c.State == v1.AppChildPruning
		for _, n := range names {
			skip = skip || c.Name == n
		}
		if !skip {
			h.markReady(c.Kind, c.Name)
		}
	}
}

func (h *harness) current() bool {
	a := h.app()
	return a.Status.LatestRevision != "" && a.Status.CurrentRevision == a.Status.LatestRevision
}

// Decision 1: an engineless part not Ready at its generation is Lagging with its own reason, or Progressing at an older
// generation; the App is Ready, its revision switches and no deadline fails it. Once Ready, the part's pin is judged as
// before (ADR-0339 Decision 4).
func TestEnginelessPartLags(t *testing.T) {
	h := newHarness(t, nil)
	h.stacks()
	h.create(todoApp(withStack))
	h.reconcile()
	h.markReadyBut("todo-stack")
	h.stackAnswers("AwaitingApproval")
	h.reconcile()
	require.Equal(t, v1.AppChild{Kind: "InfraStack", Name: "todo-stack", State: v1.AppChildLagging, Reason: "AwaitingApproval"},
		h.child("InfraStack", "todo-stack"))
	require.Equal(t, v1.ConditionTrue, h.ready().Status, h.ready().Message)
	require.True(t, h.current(), "the revision switches while the stack waits")
	require.Equal(t, v1.PhaseReady, h.app().Status.Phase)

	h.clk.Advance(10 * time.Minute)
	h.reconcile()
	require.True(t, h.current(), "no deadline fails the App for a stack that waits")

	h.edit(func(a *v1.App) { a.Spec.Addons[0].Config = json.RawMessage(`{"region":"de"}`) })
	h.reconcile()
	h.reconcile()
	require.Equal(t, v1.AppChild{Kind: "InfraStack", Name: "todo-stack", State: v1.AppChildLagging, Reason: "Progressing"},
		h.child("InfraStack", "todo-stack"), "a change not answered yet lags, Progressing")
	require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision, "the change switches on what is applied")
	require.Equal(t, v1.ConditionTrue, h.ready().Status)

	o := h.get(v1.KindAddonObject, "todo-stack").(*v1.AddonObject)
	o.Status.AppRevision = "todo-1"
	setStatus(o, v1.PhaseReady, cond("Ready", v1.ConditionTrue, "", o.Generation))
	h.update(o)
	h.reconcile()
	require.Equal(t, v1.AppChildPending, h.child("InfraStack", "todo-stack").State, "a Ready stack's pin is judged as before")
	h.stackAnswers("")
	h.reconcile()
	require.Equal(t, v1.AppChild{Kind: "InfraStack", Name: "todo-stack", State: v1.AppChildReady}, h.child("InfraStack", "todo-stack"))
}

// Decision 2: a part that names a Secret the App's Job owns, while the Secret lacks a declared key, is Lagging and not
// written, while the Job is; the App is Ready and its revision Applied. Once the Job writes the key, the part is
// written with no self-heal, and a stored part that waits again keeps its spec.
func TestPartWaitsForItsStacksOutputs(t *testing.T) {
	h := newHarness(t, nil)
	h.putOutputs(v1.JobOwner("todo-tf"), nil)
	h.create(todoApp(withOutputs))
	h.reconcile()
	require.Nil(t, h.get(v1.KindFunction, "todo-tunnel"), "the reader is not written")
	require.NotNil(t, h.get(v1.KindJob, "todo-tf"), "the Job that owns the Secret is written")
	require.Equal(t, v1.AppChild{Kind: v1.KindFunction, Name: "todo-tunnel", State: v1.AppChildLagging, Reason: waitReason},
		h.child(v1.KindFunction, "todo-tunnel"))

	h.markAllReady()
	h.reconcile()
	require.Equal(t, v1.ConditionTrue, h.ready().Status, h.ready().Message)
	require.True(t, h.current())
	requireCond(t, revCond(t, h.rev(1), "Applied"), v1.ConditionTrue, "", "")
	require.Nil(t, h.get(v1.KindFunction, "todo-tunnel"))

	h.putOutputs(v1.JobOwner("todo-tf"), frpToken())
	h.reconcile()
	require.NotNil(t, h.get(v1.KindFunction, "todo-tunnel"), "the key's write ends the wait")
	require.Equal(t, v1.AppChildPending, h.child(v1.KindFunction, "todo-tunnel").State)
	require.Nil(t, h.app().Status.LastSelfHeal, "the write that ends a wait is no self-heal")
	require.Equal(t, v1.PhaseDegraded, h.app().Status.Phase, "a current App is Degraded while the part boots")
	h.markAllReady()
	h.reconcile()
	require.Equal(t, v1.PhaseReady, h.app().Status.Phase)

	h.edit(func(a *v1.App) {
		a.Spec.Secrets[0].Keys = append(a.Spec.Secrets[0].Keys, "FRP_ADDR")
		a.Spec.Functions[1].Image = "oci-layout://todo-api:2"
	})
	h.reconcile()
	require.Equal(t, v1.AppChild{Kind: v1.KindFunction, Name: "todo-tunnel", State: v1.AppChildLagging,
		Reason: "SecretKeyMissing: Secret/todo-outputs FRP_ADDR"}, h.child(v1.KindFunction, "todo-tunnel"))
	require.Equal(t, "oci-layout://todo-api:1", h.get(v1.KindFunction, "todo-tunnel").(*v1.Function).Spec.Image,
		"a stored part that waits serves its current spec")
	require.True(t, h.current(), "the upgrade switches on what is applied")
}

// Decision 2: a pre-hook Function is written as before, so it never waits for a stack's outputs.
func TestPreHookNeverWaitsForOutputs(t *testing.T) {
	h := newHooked(t, nil)
	h.putOutputs(v1.JobOwner("todo-tf"), nil)
	h.create(hookedTodo("1.0.0", func(a *v1.App) {
		withOutputs(a)
		a.Spec.Functions[1].Bindings.Secret = []v1.ObjectName{"todo-outputs"}
	}))
	h.reconcile()
	require.NotNil(t, h.get(v1.KindFunction, "todo-migrate"), "the pre-hook Function is written in the pre-hook phase")
	require.Nil(t, h.get(v1.KindJob, "todo-tf"), "the pre-hook phase writes only what the pre-hooks need")
	h.markReady(v1.KindFunction, "todo-migrate")
	h.step()
	require.Len(t, h.inv.received(), 1, "the pre-hook is called before the stack's first apply")
	require.Equal(t, v1.ObjectName("todo-migrate"), h.inv.received()[0].fn)
}

// Decision 2: a declared Secret that lacks a key and that no Job of the App owns stops the pass before any write, as
// does a missing one; a Job the App names by ref counts as its Job.
func TestOtherSecretStillStops(t *testing.T) {
	for name, tc := range map[string]struct {
		owner  v1.WorkloadRef
		create bool
		reason string
		edit   func(*v1.App)
	}{
		"another Job":    {owner: v1.JobOwner("elsewhere"), create: true, reason: "SecretKeyMissing"},
		"a Function":     {owner: v1.FunctionOwner("todo-tf"), create: true, reason: "SecretKeyMissing"},
		"no owner":       {create: true, reason: "SecretKeyMissing"},
		"missing":        {reason: "SecretNotFound"},
		"a Job by ref":   {owner: v1.JobOwner("shared-tf"), create: true, edit: func(a *v1.App) { a.Spec.Jobs = append(a.Spec.Jobs, v1.AppJob{Ref: "shared-tf"}) }},
		"its Job, empty": {owner: v1.JobOwner("todo-tf"), create: true},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			if tc.create {
				h.putOutputs(tc.owner, nil)
			}
			h.create(todoApp(func(a *v1.App) {
				withOutputs(a)
				if tc.edit != nil {
					tc.edit(a)
				}
			}))
			h.reconcile()
			if tc.reason == "" {
				require.NotNil(t, h.get(v1.KindFunction, "todo-api"), "the pass goes on")
				require.Equal(t, v1.AppChildLagging, h.child(v1.KindFunction, "todo-tunnel").State)
				return
			}
			require.Zero(t, h.partCount(), "the pass stops before any write")
			require.Equal(t, tc.reason, h.ready().Reason)
			require.Contains(t, h.ready().Message, "Secret/todo-outputs: ")
		})
	}
}

// Decision 3: the end of a wait while the revision deploys restarts its deadline, so a part written then gets a full
// app.upgradeTimeout; a wait that ends after the switch sets nothing, and an older end never moves a newer deadline.
func TestLagEndRestartsTheDeadline(t *testing.T) {
	h := newHarness(t, nil)
	h.putOutputs(v1.JobOwner("todo-tf"), nil)
	h.create(todoApp(withOutputs))
	h.reconcile()
	h.markReadyBut("todo-api", "todo-tunnel")
	h.clk.Advance(4 * time.Minute)
	h.putOutputs(v1.JobOwner("todo-tf"), frpToken())
	h.reconcile()
	ended := h.clk.Now()
	require.NotNil(t, h.get(v1.KindFunction, "todo-tunnel"))
	require.NotNil(t, h.app().Status.LagEndedAt, "the write that drops the Lagging line sets lagEndedAt")
	require.True(t, time.Time(*h.app().Status.LagEndedAt).Equal(ended))

	h.clk.Advance(2 * time.Minute)
	h.reconcile()
	require.Equal(t, v1.PhaseDeploying, h.rev(1).Status.Phase, "the deadline counts from the end of the wait")
	h.clk.Advance(3*time.Minute + time.Second)
	h.reconcile()
	require.Equal(t, v1.PhaseFailed, h.rev(1).Status.Phase, "a full app.upgradeTimeout after the wait ended")

	h.edit(upgradeTo("2.0.0"))
	h.reconcile()
	h.markAllReady()
	h.reconcile()
	require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)
	h.edit(func(a *v1.App) { a.Spec.Secrets[0].Keys = append(a.Spec.Secrets[0].Keys, "FRP_ADDR") })
	h.reconcile()
	h.markAllReady()
	h.reconcile()
	require.Equal(t, v1.ObjectName("todo-3"), h.app().Status.CurrentRevision)
	h.putOutputs(v1.JobOwner("todo-tf"), map[string][]byte{"FRP_TOKEN": []byte("t"), "FRP_ADDR": []byte("a")})
	h.reconcile()
	require.True(t, time.Time(*h.app().Status.LagEndedAt).Equal(ended), "a wait that ends after the switch sets nothing")

	h.clk.Advance(time.Minute)
	h.edit(upgradeTo("4.0.0"))
	h.reconcile()
	h.clk.Advance(5*time.Minute + time.Second)
	h.reconcile()
	require.Equal(t, v1.PhaseFailed, h.rev(4).Status.Phase, "an older lagEndedAt never moves a newer revision's deadline")
}

// conflictOnce answers the next App update with a Conflict once armed, as a write between a pass's writes does.
type conflictOnce struct {
	store.Store
	armed atomic.Bool
}

func (s *conflictOnce) Update(ctx context.Context, o v1.Object) (v1.Object, error) {
	if _, ok := o.(*v1.App); ok && s.armed.CompareAndSwap(true, false) {
		return nil, fault.Conflictf("store.Update", "app changed")
	}
	return s.Store.Update(ctx, o)
}

// Decision 3: a pass that writes the part whose wait ended and then loses its App status write to a Conflict loses
// nothing: the next pass reads the stored Lagging line again and sets lagEndedAt.
func TestLagEndSurvivesACutPass(t *testing.T) {
	st := &conflictOnce{Store: store.New(memory.New())}
	h := newHarness(t, st)
	h.putOutputs(v1.JobOwner("todo-tf"), nil)
	h.create(todoApp(withOutputs))
	h.reconcile()
	h.markReadyBut("todo-api", "todo-tunnel")
	h.reconcile()
	h.putOutputs(v1.JobOwner("todo-tf"), frpToken())
	st.armed.Store(true)
	h.clk.Advance(time.Minute)
	h.reconcile()
	require.NotNil(t, h.get(v1.KindFunction, "todo-tunnel"), "the cut pass wrote the part")
	require.Equal(t, v1.AppChildLagging, h.child(v1.KindFunction, "todo-tunnel").State, "its App status write was lost")
	require.Nil(t, h.app().Status.LagEndedAt)

	h.clk.Advance(time.Second)
	h.reconcile()
	require.NotNil(t, h.app().Status.LagEndedAt, "the next pass reads the trigger from the stored App status")
	require.True(t, time.Time(*h.app().Status.LagEndedAt).Equal(h.clk.Now()))
	require.Equal(t, v1.AppChildPending, h.child(v1.KindFunction, "todo-tunnel").State)
}

// Decision 4: a post-hook starts only in a pass that wrote no part, while no part is Lagging or Pending.
func TestPostHookWaitsForEveryLaggingPart(t *testing.T) {
	h := newHooked(t, nil)
	h.stacks()
	h.putOutputs(v1.JobOwner("todo-tf"), nil)
	h.create(todoApp(func(a *v1.App) {
		withStack(a)
		withOutputs(a)
		a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Name: "todo-warm", FunctionSpec: apiSpec()})
		a.Spec.Hooks = &v1.AppHooks{PostApply: []v1.AppHook{{Function: "todo-warm"}}}
	}))
	h.reconcile()
	h.markReadyBut("todo-stack")
	h.stackAnswers("AwaitingApproval")
	h.step()
	require.True(t, h.current())
	h.step()
	h.noCall()

	h.putOutputs(v1.JobOwner("todo-tf"), frpToken())
	h.step()
	h.noCall()
	require.NotNil(t, h.get(v1.KindFunction, "todo-tunnel"), "the pass wrote the reader")
	h.step()
	h.noCall()
	require.Equal(t, v1.AppChildPending, h.child(v1.KindFunction, "todo-tunnel").State, "no call while a part is Pending")
	h.markReadyBut("todo-stack")
	h.step()
	h.noCall()
	require.Equal(t, v1.AppChildLagging, h.child("InfraStack", "todo-stack").State, "no call while the stack lags")

	h.stackAnswers("")
	h.step()
	require.Len(t, h.inv.received(), 1, "the post-hook starts once the stack is applied and its readers are Ready")
	require.Equal(t, v1.ObjectName("todo-warm"), h.inv.received()[0].fn)
}

// Decision 4: prune keeps a dropped part, a Pruning child with no reason, while a part lags: the stack or a reader.
func TestPruneWaitsForLagging(t *testing.T) {
	h := newHarness(t, nil)
	h.stacks()
	h.putOutputs(v1.JobOwner("todo-tf"), nil)
	h.install(todoApp(func(a *v1.App) {
		a.Spec.Routes = append(a.Spec.Routes, v1.AppRoute{Name: "todo-legacy", RouteSpec: v1.RouteSpec{
			Rules: []v1.RouteRule{{Path: "/legacy", Backend: v1.RouteBackend{Function: "todo-api"}}},
		}})
	}))
	h.edit(func(a *v1.App) {
		a.Spec.Routes = a.Spec.Routes[:1]
		withOutputs(a)
	})
	h.reconcile()
	h.markAllReady()
	h.reconcile()
	require.True(t, h.current())
	require.NotNil(t, h.get(v1.KindRoute, "todo-legacy"), "a reader lags: the dropped Route stays")
	require.Equal(t, v1.AppChild{Kind: v1.KindRoute, Name: "todo-legacy", State: v1.AppChildPruning}, h.child(v1.KindRoute, "todo-legacy"))

	h.putOutputs(v1.JobOwner("todo-tf"), frpToken())
	h.edit(withStack)
	h.reconcile()
	h.markReadyBut("todo-stack")
	h.stackAnswers("AwaitingApproval")
	h.reconcile()
	require.True(t, h.current())
	require.NotNil(t, h.get(v1.KindRoute, "todo-legacy"), "the stack lags: the dropped Route stays")

	h.stackAnswers("")
	h.reconcile()
	require.Nil(t, h.get(v1.KindRoute, "todo-legacy"), "pruned once nothing lags")
}

// Decision 5: the plan shows no stop for a Secret its Job owns that lacks a key, and lists the parts that wait for it as
// any part; a Secret no Job of the App owns still shows its stop.
func TestPlanListsTheWaitingParts(t *testing.T) {
	h := newHarness(t, nil)
	h.putOutputs(v1.JobOwner("todo-tf"), nil)
	plan := h.plan(todoApp(withOutputs))
	require.Contains(t, plan.Parts, v1.PlanPart{Kind: v1.KindFunction, Name: "todo-tunnel", Action: v1.PlanCreate})
	require.Contains(t, plan.Parts, v1.PlanPart{Kind: v1.KindJob, Name: "todo-tf", Action: v1.PlanCreate})
	for _, l := range plan.Parts {
		require.Empty(t, l.Reason, "no stop: %+v", l)
	}

	h.putOutputs(v1.JobOwner("elsewhere"), nil)
	require.Equal(t, []v1.PlanPart{{Kind: v1.KindSecret, Name: "todo-outputs", Reason: "SecretKeyMissing"}},
		h.plan(todoApp(withOutputs)).Parts)
}
