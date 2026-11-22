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
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/services/addon"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/poll"
)

// ADR-0341: a settings kind's Function applies a target's settings, called from the target's pass.

// readyRuntime runs every engine Ready at one address.
type readyRuntime struct{}

func (readyRuntime) Converge(context.Context, provider.ProviderSpec) (provider.ProviderStatus, error) {
	return provider.ProviderStatus{Ready: true, Address: "10.63.0.9:8080"}, nil
}
func (readyRuntime) Teardown(context.Context, provider.ProviderRef) error { return nil }

// gotCall is one call the fake caller got.
type gotCall struct {
	fn   v1.ObjectName
	call addon.SettingsCall
}

// fakeCaller records each call and answers every object ok, or with answer when set, which may block on ctx.
type fakeCaller struct {
	mu     sync.Mutex
	calls  []gotCall
	answer func(ctx context.Context, call addon.SettingsCall) (addon.SettingsReply, error)
}

func (f *fakeCaller) Call(ctx context.Context, _ v1.NamespaceName, fn v1.ObjectName, call addon.SettingsCall) (addon.SettingsReply, error) {
	f.mu.Lock()
	f.calls = append(f.calls, gotCall{fn, call})
	answer := f.answer
	f.mu.Unlock()
	if answer != nil {
		return answer(ctx, call)
	}
	return okAll(call), nil
}

func (f *fakeCaller) got() []gotCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeCaller) set(answer func(context.Context, addon.SettingsCall) (addon.SettingsReply, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = answer
}

func okAll(c addon.SettingsCall) addon.SettingsReply {
	var r addon.SettingsReply
	for _, o := range c.Objects {
		r.Results = append(r.Results, addon.SettingsResult{Name: o.Name, OK: true})
	}
	return r
}

const policySchema = `{"type":"object","properties":{"provider":{"type":"string"},"lengthMin":{"type":"integer"}},` +
	`"required":["provider"],"additionalProperties":false}`

// settingsRig is rauthy's Addon with the settings kind RauthyPasswordPolicy, whose Function is rauthy-settings, in
// namespace default; its clock moves only when a test moves it.
type settingsRig struct {
	t      *testing.T
	ctx    context.Context
	st     store.Store
	r      *addon.Reconciler
	caller *fakeCaller
	ended  chan controller.Request
	mu     sync.Mutex
	clock  time.Time
}

func newSettingsRig(t *testing.T, timeout time.Duration) *settingsRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	g := &settingsRig{t: t, ctx: ctx, st: store.New(memory.New()), caller: &fakeCaller{}, ended: make(chan controller.Request, 64),
		clock: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)}
	t.Cleanup(func() { cancel(); _ = g.st.Close() })
	register(t, g.st, &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddon}, ObjectMeta: v1.ObjectMeta{Name: "rauthy"},
		Spec: v1.AddonSpec{Kind: "IdentityProvider", Port: 8080, BasePath: "/auth/v1", Readiness: v1.AddonReadiness{Path: "/auth/v1/ping"},
			Image: "ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809",
			Settings: []v1.AddonSettings{{Kind: "RauthyPasswordPolicy", Target: "spec.config.provider", Resync: v1.Duration(time.Minute),
				Function: v1.AddonSettingsFunction{Ref: "rauthy-settings"}, Schema: json.RawMessage(policySchema)}}}})
	g.r = g.reconciler(timeout)
	g.target("login")
	g.function("rauthy-settings", map[string]v1.ObjectName{"idp": "login"})
	return g
}

// reconciler is a reconciler of the rig's store, as a funcd start builds one: it knows nothing of earlier calls.
func (g *settingsRig) reconciler(timeout time.Duration) *addon.Reconciler {
	r, err := addon.NewReconciler(addon.Deps{Store: g.st, Provider: readyRuntime{}, Modes: []string{v1.AddonRuntimeContainerd},
		SupervisionPeriod: time.Hour, Now: g.now})
	require.NoError(g.t, err)
	r.SetSettingsCaller(g.caller, func(req controller.Request) { g.ended <- req }, timeout)
	return r
}

func (g *settingsRig) now() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.clock
}

func (g *settingsRig) advance(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clock = g.clock.Add(d)
}

func (g *settingsRig) target(name v1.ObjectName, own ...v1.AddonObjectFunction) {
	g.t.Helper()
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Kind, o.Spec.Functions = name, "default", "rg1", "IdentityProvider", own
	_, err := g.st.Create(g.ctx, o)
	require.NoError(g.t, err)
}

// function stores the Function name binding each alias to its object, at revision <name>-<rev>.
func (g *settingsRig) function(name v1.ObjectName, binds map[string]v1.ObjectName) {
	g.t.Helper()
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = name, "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/settings.mjs"
	for alias, obj := range binds {
		fn.Spec.Bindings.Addon = append(fn.Spec.Bindings.Addon, v1.FunctionAddon{Alias: alias, Object: obj})
	}
	created, err := g.st.Create(g.ctx, fn)
	require.NoError(g.t, err)
	g.revise(created.(*v1.Function).Name, string(name)+"-1")
}

func (g *settingsRig) revise(name v1.ObjectName, rev string) {
	g.t.Helper()
	obj, err := g.st.Get(g.ctx, v1.KindFunction.GVK(), "default", name)
	require.NoError(g.t, err)
	fn := obj.(*v1.Function)
	fn.Status.CurrentRevision = rev
	_, err = g.st.Update(g.ctx, fn)
	require.NoError(g.t, err)
}

func (g *settingsRig) policy(name, target v1.ObjectName, lengthMin int) {
	g.t.Helper()
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Kind = name, "default", "rg1", "RauthyPasswordPolicy"
	o.Spec.Config = json.RawMessage(`{"provider":"` + string(target) + `","lengthMin":` + itoa(lengthMin) + `}`)
	_, err := g.st.Create(g.ctx, o)
	require.NoError(g.t, err)
}

func (g *settingsRig) edit(name v1.ObjectName, lengthMin int) {
	g.t.Helper()
	o := g.object(name)
	var cfg map[string]json.RawMessage
	require.NoError(g.t, json.Unmarshal(o.Spec.Config, &cfg))
	cfg["lengthMin"] = json.RawMessage(itoa(lengthMin))
	o.Spec.Config, _ = json.Marshal(cfg)
	_, err := g.st.Update(g.ctx, o)
	require.NoError(g.t, err)
}

func (g *settingsRig) remove(kind v1.Kind, name v1.ObjectName) {
	g.t.Helper()
	require.NoError(g.t, g.st.Delete(g.ctx, kind.GVK(), "default", name, ""))
}

func (g *settingsRig) object(name v1.ObjectName) *v1.AddonObject {
	g.t.Helper()
	obj, err := g.st.Get(g.ctx, v1.KindAddonObject.GVK(), "default", name)
	require.NoError(g.t, err)
	return obj.(*v1.AddonObject)
}

// pass runs one pass of the object name.
func (g *settingsRig) pass(name v1.ObjectName) controller.Result {
	g.t.Helper()
	res, err := g.r.Reconcile(g.ctx, controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: name})
	require.NoError(g.t, err)
	return res
}

// callEnds waits for the end of a call for target, which requeues it once it has recorded the end.
func (g *settingsRig) callEnds(target v1.ObjectName) {
	g.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case req := <-g.ended:
			if req.Name == target {
				return
			}
		case <-deadline:
			require.Failf(g.t, "no call ended", "for %s", target)
		}
	}
}

// ready is the object's Ready condition, and its phase.
func (g *settingsRig) ready(name v1.ObjectName) (v1.Condition, v1.Phase) {
	g.t.Helper()
	o := g.object(name)
	c, _ := o.Status.Conditions.Get("Ready")
	return c, o.Status.Phase
}

func (g *settingsRig) requireReason(name v1.ObjectName, reason string) {
	g.t.Helper()
	c, phase := g.ready(name)
	require.Equal(g.t, reason, c.Reason, "%s: %s", name, c.Message)
	require.Equal(g.t, v1.PhasePending, phase)
	require.Equal(g.t, g.object(name).Generation, c.ObservedGeneration)
}

func (g *settingsRig) requireReady(name v1.ObjectName, generation int64) {
	g.t.Helper()
	c, phase := g.ready(name)
	require.Equal(g.t, v1.ConditionTrue, c.Status, "%s: %s %s", name, c.Reason, c.Message)
	require.Equal(g.t, v1.PhaseReady, phase)
	require.Equal(g.t, generation, c.ObservedGeneration)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// scenario: settings-apply — a policy for a Ready login is Progressing, the Function gets a call whose list holds it,
// and the answer makes it Ready at its generation.
func TestScenarioSettingsApply(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.policy("strict", "login", 16)
	g.pass("login")
	g.callEnds("login")
	g.pass("strict")
	g.requireReady("strict", 1)

	calls := g.caller.got()
	require.Len(t, calls, 1)
	c := calls[0]
	require.Equal(t, v1.ObjectName("rauthy-settings"), c.fn)
	require.Equal(t, "start", c.call.Cause)
	require.Equal(t, addon.SettingsTarget{Kind: "IdentityProvider", Name: "login", Alias: "idp"}, c.call.Target)
	require.Len(t, c.call.Objects, 1)
	require.Equal(t, v1.ObjectName("strict"), c.call.Objects[0].Name)
	require.Equal(t, g.object("strict").UID, c.call.Objects[0].UID)
	require.Equal(t, int64(1), c.call.Objects[0].Generation)
	require.JSONEq(t, `{"provider":"login","lengthMin":16}`, string(c.call.Objects[0].Config))

	g.policy("lax", "login", 8)
	g.pass("lax")
	g.requireReason("lax", "Progressing")
}

// scenario: settings-edit — an edited policy is Progressing, the next call carries its new generation, and its Ready
// condition follows the answer to that call.
func TestScenarioSettingsEdit(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, 5*time.Second)
	g.policy("strict", "login", 16)
	release := make(chan struct{})
	g.caller.set(func(_ context.Context, c addon.SettingsCall) (addon.SettingsReply, error) {
		<-release
		return okAll(c), nil
	})
	g.pass("login")
	g.edit("strict", 18)
	close(release)
	g.callEnds("login")
	_, phase := g.ready("strict")
	require.NotEqual(t, v1.PhaseReady, phase, "the answer for generation 1 marks generation 2 nothing")
	g.caller.set(nil)
	g.pass("login")
	g.callEnds("login")
	g.requireReady("strict", 2)

	g.edit("strict", 20)
	g.pass("strict")
	g.requireReason("strict", "Progressing")
	g.caller.set(func(_ context.Context, c addon.SettingsCall) (addon.SettingsReply, error) {
		return addon.SettingsReply{Results: []addon.SettingsResult{{Name: "strict", OK: false, Reason: "TooLong", Message: "rauthy takes at most 16"}}}, nil
	})
	g.pass("login")
	g.callEnds("login")
	calls := g.caller.got()
	require.Equal(t, "change", calls[2].call.Cause)
	require.Equal(t, int64(3), calls[2].call.Objects[0].Generation)
	g.requireReason("strict", "TooLong")
	c, _ := g.ready("strict")
	require.Equal(t, "rauthy takes at most 16", c.Message)
}

// scenario: settings-recreate — a policy deleted and created again under its name is a new object: the answer for the
// old one marks nothing Ready, and the next call carries the new one at generation 1.
//
// oracle: docs/design/addon/settings.fizz#ReadyMeansApplied
func TestScenarioSettingsRecreate(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, 5*time.Second)
	g.policy("strict", "login", 16)
	release := make(chan struct{})
	g.caller.set(func(_ context.Context, c addon.SettingsCall) (addon.SettingsReply, error) {
		<-release
		return okAll(c), nil
	})
	g.pass("login")
	oldUID := g.object("strict").UID
	g.remove(v1.KindAddonObject, "strict")
	g.policy("strict", "login", 12)
	close(release)
	g.callEnds("login")
	c, _ := g.ready("strict")
	require.NotEqual(t, v1.ConditionTrue, c.Status, "the answer for the old object marks the new one nothing")

	g.caller.set(nil)
	g.pass("login")
	g.callEnds("login")
	calls := g.caller.got()
	require.Equal(t, "change", calls[1].call.Cause)
	require.NotEqual(t, oldUID, calls[1].call.Objects[0].UID)
	require.Equal(t, int64(1), calls[1].call.Objects[0].Generation)
	g.requireReady("strict", 1)
}

// scenario: settings-drift — with nothing changed, the next resync sends the same list again, so a Function puts back
// what someone changed at the engine's own API.
//
// oracle: docs/design/addon/settings.fizz#Converges
func TestScenarioSettingsDrift(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.policy("strict", "login", 16)
	g.pass("login")
	g.callEnds("login")
	res := g.pass("login")
	require.Len(t, g.caller.got(), 1, "no change, no resync yet: no call")
	require.Equal(t, time.Minute, res.RequeueAfter, "the pass requeues the target at the resync")
	g.advance(time.Minute)
	g.pass("login")
	g.callEnds("login")
	calls := g.caller.got()
	require.Len(t, calls, 2)
	require.Equal(t, "resync", calls[1].call.Cause)
	require.Equal(t, calls[0].call.Objects, calls[1].call.Objects)
}

// scenario: settings-delete — once the policy is deleted, the next call carries an empty list.
func TestScenarioSettingsDelete(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.policy("strict", "login", 16)
	g.pass("login")
	g.callEnds("login")
	g.remove(v1.KindAddonObject, "strict")
	g.pass("strict")
	g.pass("login")
	g.callEnds("login")
	calls := g.caller.got()
	require.Equal(t, "change", calls[1].call.Cause)
	require.Empty(t, calls[1].call.Objects)
	require.NotNil(t, calls[1].call.Objects, "the list is empty, not absent")
}

// scenario: settings-hung-function — a call that does not answer holds no pass: another engine turns Ready meanwhile,
// no second call starts, the call ends at addons.settingsTimeout with ReconcileFailed, and funcd calls again after a
// backoff.
//
// oracle: docs/design/addon/settings.fizz#OneCallAtATime
func TestScenarioSettingsHungFunction(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, 300*time.Millisecond)
	g.policy("strict", "login", 16)
	g.target("other")
	g.caller.set(func(ctx context.Context, _ addon.SettingsCall) (addon.SettingsReply, error) {
		<-ctx.Done()
		return addon.SettingsReply{}, ctx.Err()
	})
	g.pass("login")
	require.Eventually(t, func() bool { return len(g.caller.got()) == 1 }, 5*time.Second, 5*time.Millisecond)
	start := time.Now()
	g.pass("other")
	require.Less(t, time.Since(start), 250*time.Millisecond, "another engine's pass waits on no call")
	_, phase := g.ready("other")
	require.Equal(t, v1.PhaseReady, phase)
	g.pass("login")
	require.True(t, poll.Never(func() bool { return len(g.caller.got()) > 1 }, 100*time.Millisecond, 5*time.Millisecond),
		"one call at a time per target and kind")

	g.callEnds("login")
	g.requireReason("strict", "ReconcileFailed")
	c, _ := g.ready("strict")
	require.Contains(t, c.Message, "did not answer within addons.settingsTimeout 300ms")

	g.caller.set(nil)
	res := g.pass("login")
	require.Len(t, g.caller.got(), 1, "the retry waits for its backoff")
	require.Equal(t, time.Second, res.RequeueAfter, "the first backoff is 1s")
	g.advance(time.Second)
	g.pass("login")
	g.callEnds("login")
	calls := g.caller.got()
	require.Equal(t, "retry", calls[1].call.Cause)
	g.requireReady("strict", 1)
}

// scenario: settings-restart — a call in flight when funcd stops writes nothing, and the first pass after the next
// start sends the list again, with cause start.
func TestScenarioSettingsRestart(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, 5*time.Second)
	g.policy("strict", "login", 16)
	stopped := make(chan struct{})
	g.caller.set(func(ctx context.Context, _ addon.SettingsCall) (addon.SettingsReply, error) {
		defer close(stopped)
		<-ctx.Done()
		return addon.SettingsReply{}, ctx.Err()
	})
	stop, cancel := context.WithCancel(g.ctx)
	_, err := g.r.Reconcile(stop, controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "login"})
	require.NoError(t, err)
	g.pass("strict")
	g.requireReason("strict", "Progressing")
	cancel()
	<-stopped
	require.True(t, poll.Never(func() bool { c, _ := g.ready("strict"); return c.Reason != "Progressing" },
		200*time.Millisecond, 20*time.Millisecond), "funcd's stop writes nothing from the call")

	g.caller.set(nil)
	g.r = g.reconciler(time.Second)
	g.pass("login")
	g.callEnds("login")
	calls := g.caller.got()
	require.Equal(t, "start", calls[1].call.Cause)
	g.requireReady("strict", 1)
}

// scenario: settings-function-refuses — a result with ok false reports its reason, and an object the answer leaves out
// reports NoResult.
func TestScenarioSettingsFunctionRefuses(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.policy("strict", "login", 16)
	g.policy("lax", "login", 8)
	g.caller.set(func(context.Context, addon.SettingsCall) (addon.SettingsReply, error) {
		return addon.SettingsReply{Results: []addon.SettingsResult{
			{Name: "strict", OK: false, Reason: "PolicyRejected", Message: "rauthy refused it"},
			{Name: "unknown", OK: true},
		}}, nil
	})
	g.pass("login")
	g.callEnds("login")
	g.requireReason("strict", "PolicyRejected")
	g.requireReason("lax", "NoResult")
}

// scenario: settings-waits — with no login, login not Ready, no Function, a Function that does not bind login, or no
// Addon of the kind, the policy waits with the reason, even after an earlier Ready, and no call is made for it.
func TestScenarioSettingsWaits(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.policy("orphan", "nobody", 16)
	g.pass("orphan")
	g.requireReason("orphan", "TargetNotFound")

	g.policy("strict", "login", 16)
	g.pass("login")
	g.callEnds("login")
	g.pass("strict")
	g.requireReady("strict", 1)

	g.remove(v1.KindFunction, "rauthy-settings")
	g.pass("strict")
	g.requireReason("strict", "FunctionNotFound")
	g.function("rauthy-settings", map[string]v1.ObjectName{"idp": "admin-login"})
	g.pass("strict")
	g.requireReason("strict", "FunctionNotBound")
	calls := len(g.caller.got())
	g.pass("login")
	require.Len(t, g.caller.got(), calls, "no call for a target the Function does not bind")

	o := g.object("login")
	o.Status.Phase = v1.PhasePending
	_, err := g.st.Update(g.ctx, o)
	require.NoError(t, err)
	g.pass("strict")
	g.requireReason("strict", "TargetNotReady")

	g.remove(v1.KindAddonObject, "login")
	g.pass("strict")
	g.requireReason("strict", "TargetNotFound")

	require.NoError(t, g.st.Delete(g.ctx, v1.KindAddon.GVK(), "", "rauthy", ""))
	g.pass("strict")
	g.requireReason("strict", "AddonNotFound")
}

// A wait that ends makes the object Progressing until the next answer, and the call follows (Decision 4).
func TestSettingsWaitEndsProgressing(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.remove(v1.KindFunction, "rauthy-settings")
	g.policy("strict", "login", 16)
	g.pass("login")
	require.Empty(t, g.caller.got(), "nothing is called with no Function")
	g.pass("strict")
	g.requireReason("strict", "FunctionNotFound")
	g.function("rauthy-settings", map[string]v1.ObjectName{"idp": "login"})
	g.pass("strict")
	g.requireReason("strict", "Progressing")
	g.pass("login")
	g.callEnds("login")
	g.requireReady("strict", 1)
}

// scenario: settings-function-change — a new revision of the Function, or a new alias for login, sends the list again.
func TestScenarioSettingsFunctionChange(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.policy("strict", "login", 16)
	g.pass("login")
	g.callEnds("login")
	g.revise("rauthy-settings", "rauthy-settings-2")
	g.pass("login")
	g.callEnds("login")
	obj, err := g.st.Get(g.ctx, v1.KindFunction.GVK(), "default", "rauthy-settings")
	require.NoError(t, err)
	fn := obj.(*v1.Function)
	fn.Spec.Bindings.Addon = []v1.FunctionAddon{{Alias: "login-idp", Object: "login"}}
	_, err = g.st.Update(g.ctx, fn)
	require.NoError(t, err)
	g.pass("login")
	g.callEnds("login")
	calls := g.caller.got()
	require.Len(t, calls, 3)
	require.Equal(t, "change", calls[1].call.Cause)
	require.Equal(t, "change", calls[2].call.Cause)
	require.Equal(t, "login-idp", calls[2].call.Target.Alias)
}

// scenario: settings-function-per-target — a target's own Function in spec.functions gets its list, and a target that
// names none goes to the kind's Function.
func TestScenarioSettingsFunctionPerTarget(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.target("web-auth-login", v1.AddonObjectFunction{Kind: "RauthyPasswordPolicy", Ref: "web-auth-settings"})
	g.target("admin-auth-login", v1.AddonObjectFunction{Kind: "RauthyPasswordPolicy", Ref: "admin-auth-settings"})
	g.function("web-auth-settings", map[string]v1.ObjectName{"idp": "web-auth-login"})
	g.function("admin-auth-settings", map[string]v1.ObjectName{"idp": "admin-auth-login"})
	for _, target := range []v1.ObjectName{"web-auth-login", "admin-auth-login", "login"} {
		g.policy(target+"-policy", target, 16)
		g.pass(target + "-policy")
		g.pass(target)
		g.callEnds(target)
		g.pass(target + "-policy")
		g.requireReady(target+"-policy", 1)
	}
	got := map[v1.ObjectName]v1.ObjectName{}
	for _, c := range g.caller.got() {
		got[c.call.Target.Name] = c.fn
	}
	require.Equal(t, map[v1.ObjectName]v1.ObjectName{"web-auth-login": "web-auth-settings",
		"admin-auth-login": "admin-auth-settings", "login": "rauthy-settings"}, got)
}

// A failed call's backoff doubles, at most the resync (Decision 5), and a change sends at once.
func TestSettingsBackoff(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.policy("strict", "login", 16)
	g.caller.set(func(context.Context, addon.SettingsCall) (addon.SettingsReply, error) {
		return addon.SettingsReply{}, fault.Unavailablef("test", "the engine is down")
	})
	var waits []time.Duration
	for range 8 {
		g.pass("login")
		g.callEnds("login")
		res := g.pass("login")
		waits = append(waits, res.RequeueAfter)
		g.advance(res.RequeueAfter)
	}
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		32 * time.Second, time.Minute, time.Minute}, waits, "at most the kind's resync of 1m")
	g.requireReason("strict", "ReconcileFailed")
	n := len(g.caller.got())
	g.edit("strict", 20)
	g.pass("login")
	g.callEnds("login")
	require.Len(t, g.caller.got(), n+1, "a change does not wait for the backoff")
	require.Equal(t, "change", g.caller.got()[n].call.Cause)
}

// ADR-0341 Decision 4: a policy's change reaches its target, a target's change its policies, and a Function's change
// the policies it applies and their targets.
func TestMapSettingsAndFunction(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.target("web-auth-login", v1.AddonObjectFunction{Kind: "RauthyPasswordPolicy", Ref: "web-auth-settings"})
	g.policy("strict", "login", 16)
	g.policy("web", "web-auth-login", 16)
	req := func(name v1.ObjectName) controller.Request {
		return controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: name}
	}
	require.Equal(t, []controller.Request{req("login")}, g.r.MapSettings(g.ctx, g.object("strict")))
	require.Equal(t, []controller.Request{req("strict")}, g.r.MapSettings(g.ctx, g.object("login")))
	kindFn := &v1.Function{ObjectMeta: v1.ObjectMeta{Name: "rauthy-settings", Namespace: "default"}}
	require.ElementsMatch(t, []controller.Request{req("login"), req("strict")}, g.r.MapFunction(g.ctx, kindFn))
	ownFn := &v1.Function{ObjectMeta: v1.ObjectMeta{Name: "web-auth-settings", Namespace: "default"}}
	require.ElementsMatch(t, []controller.Request{req("web-auth-login"), req("web")}, g.r.MapFunction(g.ctx, ownFn))
	rauthy, err := g.st.Get(g.ctx, v1.KindAddon.GVK(), "", "rauthy")
	require.NoError(t, err)
	require.ElementsMatch(t, []controller.Request{req("login"), req("web-auth-login"), req("strict"), req("web")},
		g.r.MapAddon(g.ctx, rauthy))
}
