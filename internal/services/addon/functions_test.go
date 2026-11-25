package addon_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/services/addon"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/poll"
)

// ADR-0358: a kind's Function comes from its image, and the image's contract is the kind's schema.

// The images of the tests: what each says is in the rig's fakeImages.
const (
	ones      = "1111111111111111111111111111111111111111111111111111111111111111"
	settings1 = "ghcr.io/pyvvo/rauthy-settings@sha256:" + ones
	settings2 = "ghcr.io/pyvvo/rauthy-settings@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	stacks1   = "ghcr.io/pyvvo/infrastack-reconciler@sha256:" + ones
)

// fakeImages answers the images it holds, and fails the others as a registry that is down.
type fakeImages struct {
	mu   sync.Mutex
	says map[string]v1.WorkflowContract
	asks int
	hook func() // runs once, in the middle of the next read
}

func (f *fakeImages) Read(_ context.Context, image string) (v1.RuntimeName, v1.WorkflowContract, error) {
	f.mu.Lock()
	hook := f.hook
	f.hook = nil
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asks++
	c, ok := f.says[image]
	if !ok {
		return "", v1.WorkflowContract{}, fault.Unavailablef("test", "the registry is down")
	}
	return "nodejs22", c, nil
}

func (f *fakeImages) set(image string, c v1.WorkflowContract) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.says[image] = c
}

// callContract is a contract whose calls carry objects of kind, whose config has properties.
func callContract(kind, properties string) v1.WorkflowContract {
	return v1.WorkflowContract{Input: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{` +
		`"kind":{"type":"string","enum":["` + kind + `"]},"cause":{"type":"string"},"objects":{"type":"array","items":{"type":"object",` +
		`"additionalProperties":false,"properties":{"name":{"type":"string"},"config":{"type":"object","additionalProperties":false,` +
		`"properties":{` + properties + `},"required":["provider"]}}}}}}`)}
}

const policyProperties = `"provider":{"type":"string"},"lengthMin":{"type":"integer"}`

// newImageRig is rauthy's Addon whose RauthyPasswordPolicy names the image settings1, its pass run once, and the
// IdentityProvider login, for which nothing is created.
func newImageRig(t *testing.T) *settingsRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	g := &settingsRig{t: t, ctx: ctx, st: store.New(memory.New()), caller: &fakeCaller{}, ended: make(chan controller.Request, 64),
		images: &fakeImages{says: map[string]v1.WorkflowContract{settings1: callContract("RauthyPasswordPolicy", policyProperties)}},
		clock:  time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}
	t.Cleanup(func() { cancel(); _ = g.st.Close() })
	_, err := g.st.Create(ctx, &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddon}, ObjectMeta: v1.ObjectMeta{Name: "rauthy"},
		Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "identityprovider", Image: "ghcr.io/pyvvo/funcd-rauthy@sha256:" + strings.Repeat("a", 64),
			Workloads: []v1.Workload{v1.WorkloadEngine}, Attributes: &v1.AddonAttributes{Kind: "IdentityProvider", Port: 8080,
				Readiness: v1.AddonReadiness{Path: "/auth/v1/ping"}, Settings: []v1.AddonSettings{{Kind: "RauthyPasswordPolicy",
					Target: "spec.config.provider", Resync: v1.Duration(time.Minute), Function: v1.AddonFunction{Image: settings1}}}}}}}})
	require.NoError(t, err)
	g.r = g.reconciler(time.Second)
	g.addonPass()
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Kind = "login", "default", "rg1", "IdentityProvider"
	_, err = g.st.Create(ctx, o)
	require.NoError(t, err)
	return g
}

// addonPass runs one pass of the Addon rauthy.
func (g *settingsRig) addonPass() controller.Result {
	g.t.Helper()
	res, err := g.r.Revisions().Reconcile(g.ctx, controller.Request{GVK: v1.KindAddon.GVK(), Name: "rauthy"})
	require.NoError(g.t, err)
	return res
}

func (g *settingsRig) addon() *v1.Addon {
	g.t.Helper()
	obj, err := g.st.Get(g.ctx, v1.KindAddon.GVK(), "", "rauthy")
	require.NoError(g.t, err)
	return obj.(*v1.Addon)
}

// editAddon edits the Addon's settings kinds and runs its pass.
func (g *settingsRig) editAddon(edit func(at *v1.AddonAttributes)) {
	g.t.Helper()
	a := g.addon()
	edit(a.Spec.Runtimes[0].Attributes)
	_, err := g.st.Update(g.ctx, a)
	require.NoError(g.t, err)
	g.addonPass()
}

// editLogin changes login's spec, as an edit of it does, so that its next pass moves its pin.
func (g *settingsRig) editLogin(edit func(s *v1.AddonObjectSpec)) {
	g.t.Helper()
	o := g.object("login")
	edit(&o.Spec)
	_, err := g.st.Update(g.ctx, o)
	require.NoError(g.t, err)
}

// created is the Function name, nil when there is none.
func (g *settingsRig) created(name v1.ObjectName) *v1.Function {
	g.t.Helper()
	obj, err := g.st.Get(g.ctx, v1.KindFunction.GVK(), "default", name)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	require.NoError(g.t, err)
	return obj.(*v1.Function)
}

// stampFunctions does the Function controller's part for each Function that funcd created: it stamps the revision of its
// spec, which a call waits for.
func stampFunctions(t *testing.T, st store.Store) {
	t.Helper()
	list, err := st.List(context.Background(), v1.KindFunction.GVK(), store.ListOptions{})
	require.NoError(t, err)
	for _, item := range list.Items {
		fn := item.(*v1.Function)
		if c, ok := v1.ControllerOf(fn.OwnerReferences); !ok || c.Kind != v1.KindAddonObject {
			continue
		}
		fn.Status.CurrentRevision = string(v1.FunctionRevisionName(fn.Name, fn.Generation))
		_, err := st.Update(context.Background(), fn)
		require.NoError(t, err)
	}
}

// converge passes login, then stamps what that pass created, whose event brings login's next pass.
func (g *settingsRig) converge() {
	g.t.Helper()
	g.pass("login")
	stampFunctions(g.t, g.st)
	g.pass("login")
}

// applied passes login until its call made name Ready.
func (g *settingsRig) applied(name v1.ObjectName) {
	g.t.Helper()
	g.converge()
	g.callEnds("login")
	g.requireReady(name, g.object(name).Generation)
}

// noCall requires that no call starts, where one would on a goroutine of its own.
func (g *settingsRig) noCall(why string) {
	g.t.Helper()
	n := len(g.caller.got())
	require.True(g.t, poll.Never(func() bool { return len(g.caller.got()) > n }, 50*time.Millisecond, 5*time.Millisecond), why)
}

// lastCall is the last call the settings Functions got.
func (g *settingsRig) lastCall() gotCall {
	g.t.Helper()
	calls := g.caller.got()
	require.NotEmpty(g.t, calls)
	return calls[len(calls)-1]
}

const policyFunction = v1.ObjectName("login-rauthypasswordpolicy")

// scenario: addon-function-image — a policy that names login brings the Function login-rauthypasswordpolicy, controlled
// by login, on the runtime its artifact declares, with the export handle, no address of its own, and bindings of login
// and its Secrets; its answer makes the policy Ready. Its image says the schema.
func TestScenarioAddonFunctionImage(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.policy("strict", "login", 16)
	g.pass("strict")
	g.applied("strict")

	fn, login := g.created(policyFunction), g.object("login")
	require.NotNil(t, fn)
	require.True(t, v1.ControlledBy(fn.OwnerReferences, v1.KindAddonObject, login.UID))
	require.Equal(t, v1.ResourceGroupName("rg1"), fn.ResourceGroup)
	require.Equal(t, v1.RuntimeName("nodejs22"), fn.Spec.Runtime)
	require.Equal(t, "handle", fn.Spec.Handler)
	require.Equal(t, settings1, fn.Spec.Image)
	require.Equal(t, "sha256:"+ones, fn.Spec.ImageDigest, "the digest that the Addon pins")
	require.Equal(t, v1.Scaling{IdleTimeout: v1.Duration(5 * time.Minute)}, fn.Spec.Scaling)
	require.Empty(t, fn.Spec.Pooling.Worker, "in a worker of its own")
	require.False(t, *fn.Spec.Ingress.Enabled, "no address of its own")
	require.Equal(t, []v1.FunctionAddon{{Alias: "self", Object: "login"}}, fn.Spec.Bindings.Addon)
	calls := g.caller.got()
	require.Len(t, calls, 1)
	require.Equal(t, policyFunction, calls[0].fn)
	require.Equal(t, "self", calls[0].call.Target.Alias)

	rev, err := g.st.Get(g.ctx, v1.KindAddonRevision.GVK(), "", v1.AddonRevisionName("rauthy", 1))
	require.NoError(t, err)
	kc := rev.(*v1.AddonRevision).ContractOf("RauthyPasswordPolicy")
	require.Equal(t, v1.RuntimeName("nodejs22"), kc.Runtime)
	require.JSONEq(t, `{"type":"object","additionalProperties":false,"properties":{`+policyProperties+`},"required":["provider"]}`,
		string(addon.KindSchema(kc.Contract, "RauthyPasswordPolicy")))
	c, _ := g.addon().Status.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionTrue, c.Status, c.Message)
}

// A pass with nothing to put back writes no Function.
func TestAddonFunctionIsWrittenOnce(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.policy("strict", "login", 16)
	g.applied("strict")
	before := g.created(policyFunction).ResourceVersion
	g.advance(time.Hour)
	g.pass("login")
	g.callEnds("login")
	g.pass("login")
	require.Equal(t, before, g.created(policyFunction).ResourceVersion)
}

// scenario: addon-function-on-demand — with no policy login has no Function and nothing is called, so what rauthy's own
// API set stays; a policy brings the Function, and once it is deleted and the empty list applied, the Function goes and
// nothing is called again. A call that failed keeps it, and so does a funcd start, whose first pass sends the list again.
// oracle: docs/design/addon/settings.fizz#NoObjectNoCall
// oracle: docs/design/addon/settings.fizz#NothingLeftBehind
func TestScenarioAddonFunctionOnDemand(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	for range 3 {
		g.pass("login")
		g.advance(time.Hour)
	}
	require.Nil(t, g.created(policyFunction), "no policy ever named login")
	require.Empty(t, g.caller.got(), "nothing is called")

	g.policy("strict", "login", 16)
	g.applied("strict")
	g.remove(v1.KindAddonObject, "strict")
	g.caller.set(func(context.Context, addon.SettingsCall) (addon.SettingsReply, error) {
		return addon.SettingsReply{}, fault.Unavailablef("test", "rauthy refused the default")
	})
	g.pass("login")
	g.callEnds("login")
	g.pass("login")
	require.NotNil(t, g.created(policyFunction), "the default is not back")

	g.caller.set(nil)
	g.r = g.reconciler(time.Second) // a funcd start: it knows no call
	g.pass("login")
	g.callEnds("login")
	calls := g.caller.got()
	require.Equal(t, "start", calls[len(calls)-1].call.Cause)
	require.Empty(t, calls[len(calls)-1].call.Objects, "the empty list")
	g.pass("login")
	require.Nil(t, g.created(policyFunction), "the default is back")
	for range 3 {
		g.advance(time.Hour)
		g.pass("login")
	}
	require.Len(t, g.caller.got(), len(calls), "nothing is called again")

	g.policy("again", "login", 18)
	g.applied("again")
	require.NotNil(t, g.created(policyFunction), "a new policy brings it again")
}

// scenario: addon-function-follows-the-pin — a newer image of the Addon leaves login's Function as it is, until an edit
// of login moves its pin; its next call has cause change.
func TestScenarioAddonFunctionFollowsThePin(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.images.set(settings2, callContract("RauthyPasswordPolicy", policyProperties+`,"maxAge":{"type":"integer"}`))
	g.policy("strict", "login", 16)
	g.applied("strict")

	g.editAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function.Image = settings2 })
	g.pass("login")
	require.Equal(t, settings1, g.created(policyFunction).Spec.Image, "login pins rauthy-1")
	before := len(g.caller.got())

	g.editLogin(func(s *v1.AddonObjectSpec) { s.Resources.Memory = "64Mi" })
	g.converge()
	require.Equal(t, settings2, g.created(policyFunction).Spec.Image, "login moved to rauthy-2")
	g.callEnds("login")
	require.Len(t, g.caller.got(), before+1)
	require.Equal(t, "change", g.lastCall().call.Cause)
}

// scenario: addon-function-repaired — a created Function that is deleted or differs is put back while a policy names
// login; a user's Function of that name is left as it is, and the policy waits with FunctionNotOwned until it is gone.
func TestScenarioAddonFunctionRepaired(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.policy("strict", "login", 16)
	g.applied("strict")
	g.remove(v1.KindFunction, policyFunction)
	g.pass("login")
	require.NotNil(t, g.created(policyFunction), "deleted by hand, it is back")
	fn := g.created(policyFunction)
	fn.Spec.Image = settings2
	_, err := g.st.Update(g.ctx, fn)
	require.NoError(t, err)
	g.pass("login")
	require.Equal(t, settings1, g.created(policyFunction).Spec.Image, "what differs is put back")

	other := newImageRig(t)
	other.function(policyFunction, map[string]v1.ObjectName{"idp": "login"})
	other.policy("strict", "login", 16)
	other.pass("login")
	other.pass("strict")
	other.requireReason("strict", "FunctionNotOwned")
	require.Equal(t, "file:///tmp/settings.mjs", other.created(policyFunction).Spec.Image, "a user's Function is left as it is")
	require.Empty(t, other.caller.got(), "and is not called")
	require.Contains(t, other.r.MapFunction(other.ctx, other.created(policyFunction)),
		controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "login"}, "its removal brings login's pass")
	other.remove(v1.KindFunction, policyFunction)
	other.applied("strict")
}

// scenario: addon-function-own — login's own Function in spec.functions is called, and the one funcd created goes.
func TestScenarioAddonFunctionOwn(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.policy("strict", "login", 16)
	g.applied("strict")
	g.function("my-settings", map[string]v1.ObjectName{"idp": "login"})
	g.editLogin(func(s *v1.AddonObjectSpec) {
		s.Functions = []v1.AddonObjectFunction{{Kind: "RauthyPasswordPolicy", Ref: "my-settings"}}
	})
	g.pass("login")
	g.callEnds("login")
	require.Equal(t, v1.ObjectName("my-settings"), g.lastCall().fn)
	require.Equal(t, "idp", g.lastCall().call.Target.Alias)
	require.Nil(t, g.created(policyFunction), "funcd's own goes")
	require.NotNil(t, g.created("my-settings"))
}

// scenario: addon-kind-not-in-the-pin — a kind that an admin adds to a live Addon waits, with no call, until an edit of
// login moves its pin to a revision that declares it.
func TestScenarioAddonKindNotInThePin(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.pass("login")
	g.images.set(settings2, callContract("RauthyClient", policyProperties))
	g.editAddon(func(at *v1.AddonAttributes) {
		at.Settings = append(at.Settings, v1.AddonSettings{Kind: "RauthyClient", Target: "spec.config.provider", Function: v1.AddonFunction{Image: settings2}})
	})
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Kind = "web", "default", "rg1", "RauthyClient"
	o.Spec.Config = json.RawMessage(`{"provider":"login"}`)
	_, err := g.st.Create(g.ctx, o)
	require.NoError(t, err)
	g.pass("login")
	g.pass("web")
	g.requireReason("web", "FunctionNotFound")
	require.Empty(t, g.caller.got())
	require.Nil(t, g.created("login-rauthyclient"))

	g.editLogin(func(s *v1.AddonObjectSpec) { s.Resources.Memory = "64Mi" })
	g.applied("web")
	require.NotNil(t, g.created("login-rauthyclient"))
}

// scenario: addon-image-unread — while a registry does not answer, the Addon is Ready=False with ArtifactNotReady and its
// revision is current, login's engine runs, and a policy waits with ArtifactNotReady; once the registry answers, the
// Addon's next pass reads the image, whose revision's change brings the policy's pass.
func TestScenarioAddonImageUnread(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.editAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function.Image = settings2 })
	require.Equal(t, v1.AddonRevisionName("rauthy", 2), g.addon().Status.CurrentRevision, "a deploy waits for no registry")
	c, _ := g.addon().Status.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "ArtifactNotReady", c.Reason)
	require.Contains(t, c.Message, "RauthyPasswordPolicy")
	require.Positive(t, g.addonPass().RequeueAfter, "tried again")
	g.pass("login")
	require.Equal(t, v1.PhaseReady, g.object("login").Status.Phase, "the engine runs")
	g.policy("strict", "login", 16)
	g.pass("login")
	g.pass("strict")
	g.requireReason("strict", "ArtifactNotReady")
	require.Nil(t, g.created(policyFunction))

	g.images.set(settings2, callContract("RauthyPasswordPolicy", policyProperties))
	g.addonPass()
	c, _ = g.addon().Status.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionTrue, c.Status, c.Message)
	rev, err := g.st.Get(g.ctx, v1.KindAddonRevision.GVK(), "", v1.AddonRevisionName("rauthy", 2))
	require.NoError(t, err)
	require.Contains(t, g.r.MapAddonRevision(g.ctx, rev), controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "strict"})
	asks := g.images.asks
	g.addonPass()
	require.Equal(t, asks, g.images.asks, "an image is read once")
	g.applied("strict")
}

// An image of a revision that is no longer current is read too, while an object pins it.
func TestAddonImageOfAnOlderRevisionIsRead(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.editAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function.Image = settings2 })
	g.pass("login") // pins rauthy-2, whose image is not read
	g.editAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function.Image = settings1 })
	g.images.set(settings2, callContract("RauthyPasswordPolicy", policyProperties))
	g.addonPass()
	rev, err := g.st.Get(g.ctx, v1.KindAddonRevision.GVK(), "", v1.AddonRevisionName("rauthy", 2))
	require.NoError(t, err)
	require.NotNil(t, rev.(*v1.AddonRevision).ContractOf("RauthyPasswordPolicy"))
}

// scenario: addon-constraint-unfit — a rule or a constraint over a field that the contract lacks makes the Addon
// Ready=False with SettingsInvalid, naming it; its revision is current, and what runs is not touched.
func TestScenarioAddonConstraintUnfit(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(st *v1.AddonSettings){
		"a rule": func(st *v1.AddonSettings) {
			st.Constraints.Rules = []v1.AddonRule{{Rule: "${{ object.spec.config.maxAge > 1 }}", Message: "too young"}}
		},
		"a unique field":     func(st *v1.AddonSettings) { st.Constraints.Unique = []string{"spec.config.maxAge"} },
		"an immutable field": func(st *v1.AddonSettings) { st.Constraints.Immutable = []string{"spec.config.maxAge"} },
	} {
		g := newImageRig(t)
		g.policy("strict", "login", 16)
		g.applied("strict")
		g.editAddon(func(at *v1.AddonAttributes) { edit(&at.Settings[0]) })
		c, _ := g.addon().Status.Conditions.Get("Ready")
		require.Equal(t, "SettingsInvalid", c.Reason, "%s: %s", name, c.Message)
		require.Contains(t, c.Message, "maxAge", name)
		calls := len(g.caller.got())
		g.pass("login")
		require.Len(t, g.caller.got(), calls, "%s: no list changes", name)
		g.requireReady("strict", 1)
	}
}

// A contract says a kind's schema as a call's shape, or as the member of a union discriminated by kind that names it; one
// that says neither says nothing.
func TestKindSchema(t *testing.T) {
	t.Parallel()
	require.Nil(t, addon.KindSchema(v1.WorkflowContract{}, "RauthyClient"))
	require.Nil(t, addon.KindSchema(v1.WorkflowContract{Input: json.RawMessage(`{}`)}, "RauthyClient"))
	require.Nil(t, addon.KindSchema(v1.WorkflowContract{Input: json.RawMessage(`[]`)}, "RauthyClient"))
	a, b := callContract("RauthyPasswordPolicy", policyProperties), callContract("RauthyClient", `"provider":{"type":"string"}`)
	union := v1.WorkflowContract{Input: json.RawMessage(`{"oneOf":[` + string(a.Input) + `,` + string(b.Input) + `],"discriminator":{"propertyName":"kind"}}`)}
	require.JSONEq(t, string(addon.KindSchema(b, "RauthyClient")), string(addon.KindSchema(union, "RauthyClient")))
	require.JSONEq(t, string(addon.KindSchema(a, "RauthyPasswordPolicy")), string(addon.KindSchema(union, "rauthypasswordpolicy")), "whatever the case")
	require.Nil(t, addon.KindSchema(union, "RauthyGroup"))
}

// newImageStacks is InfraStack's Addon whose Function comes from the image stacks1, its pass run once.
func newImageStacks(t *testing.T) *enginelessRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	g := &enginelessRig{t: t, ctx: ctx, st: store.New(memory.New()), caller: &fakeObjectsCaller{},
		images: &fakeImages{says: map[string]v1.WorkflowContract{stacks1: callContract("InfraStack", `"provider":{"type":"string"}`)}},
		ended:  make(chan controller.Request, 64), clock: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}
	t.Cleanup(func() { cancel(); _ = g.st.Close() })
	a := infraStackAddon()
	a.Spec.Runtimes[0].Attributes.Function = &v1.AddonFunction{Image: stacks1}
	_, err := g.st.Create(ctx, a)
	require.NoError(t, err)
	g.r = g.reconciler()
	_, err = g.r.Revisions().Reconcile(ctx, controller.Request{GVK: v1.KindAddon.GVK(), Name: "infrastack"})
	require.NoError(t, err)
	return g
}

// converge passes name, then stamps what that pass created, whose event brings name's next pass.
func (g *enginelessRig) converge(name v1.ObjectName) {
	g.t.Helper()
	g.pass(name)
	stampFunctions(g.t, g.st)
	g.pass(name)
}

// scenario: engineless-function-image — the stack bunny-edge, which binds the Job bunny-edge as stack, brings the
// Function bunny-edge-infrastack, which binds that Job and no object, and alone gets the stack's list; deleted with
// deletion: delete, the stack and its Function stay until the Function's ok.
func TestScenarioEnginelessFunctionImage(t *testing.T) {
	t.Parallel()
	g := newImageStacks(t)
	for _, name := range []v1.ObjectName{"bunny-edge", "site"} {
		g.stack(name, v1.DeletionDelete, "")
		o := g.object(name)
		o.Spec.Bindings.Job = []v1.FunctionJob{{Alias: "stack", Job: name}}
		_, err := g.st.Update(g.ctx, o)
		require.NoError(t, err)
		g.marked(name)
	}
	g.converge("bunny-edge")
	g.callEnds("bunny-edge")
	call := g.lastCall()
	require.Equal(t, v1.ObjectName("bunny-edge-infrastack"), call.fn)
	require.Equal(t, []string{"bunny-edge"}, names(call.call), "a stack's Function gets that stack alone")
	obj, err := g.st.Get(g.ctx, v1.KindFunction.GVK(), "default", "bunny-edge-infrastack")
	require.NoError(t, err)
	fn := obj.(*v1.Function)
	require.True(t, v1.ControlledBy(fn.OwnerReferences, v1.KindAddonObject, g.object("bunny-edge").UID))
	require.Equal(t, []v1.FunctionJob{{Alias: "stack", Job: "bunny-edge"}}, fn.Spec.Bindings.Job)
	require.Empty(t, fn.Spec.Bindings.Addon, "it binds no object: none runs an engine")
	require.False(t, *fn.Spec.Ingress.Enabled)

	g.caller.set(func(_ context.Context, c addon.ObjectsCall) (addon.SettingsReply, error) {
		return answerAll(c, false, "Destroying"), nil
	})
	require.True(t, g.del("bunny-edge"), "held")
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	g.requireReason("bunny-edge", "Destroying")
	_, err = g.st.Get(g.ctx, v1.KindFunction.GVK(), "default", "bunny-edge-infrastack")
	require.NoError(t, err, "its Function stays")
	g.caller.set(nil)
	g.advance(time.Hour)
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	_, there := g.stored("bunny-edge")
	require.False(t, there, "its Function's ok lets it go")
}

// An engineless object whose image is not read yet, or whose Function's name is another's, waits with the reason.
func TestEnginelessImageWaits(t *testing.T) {
	t.Parallel()
	g := newImageStacks(t)
	g.function("site-infrastack")
	g.stack("site", v1.DeletionRetain, "")
	g.marked("site")
	g.pass("site")
	g.requireReason("site", "FunctionNotOwned")

	down := newImageStacks(t)
	obj, err := down.st.Get(down.ctx, v1.KindAddon.GVK(), "", "infrastack")
	require.NoError(t, err)
	a := obj.(*v1.Addon)
	a.Spec.Runtimes[0].Attributes.Function.Image = settings2
	_, err = down.st.Update(down.ctx, a)
	require.NoError(t, err)
	_, err = down.r.Revisions().Reconcile(down.ctx, controller.Request{GVK: v1.KindAddon.GVK(), Name: "infrastack"})
	require.NoError(t, err)
	down.stack("site", v1.DeletionRetain, "")
	down.marked("site")
	down.pass("site")
	down.requireReason("site", "ArtifactNotReady")
	require.Empty(t, down.caller.got())
}
