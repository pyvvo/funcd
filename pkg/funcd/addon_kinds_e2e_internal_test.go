//go:build e2e

package funcd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/internal/testkit/poll"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// The scenarios of ADR-0358 on the process driver and the real Node shim: the Functions of the kinds' images run, and
// call the fake engine through funcd. A registry that is down holds the images: each lies in a local OCI layout, from
// which funcd reads its contract, and in the platform's artifact cache, from which its Functions run.

// kindWithin bounds a call's answer, its Function's cold start included.
const kindWithin = 20 * time.Second

// policiesSrc is the Function of rauthy's settings kinds: it sends its target's engine the config of the list's one
// object, or the engine's default once the list is empty, and answers each object ok.
const policiesSrc = `export async function handle(ctx, event) {
  const { target, objects } = event.data;
  const res = await ctx.addon.fetch(target.alias, '/auth/v1/password_policy', {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(objects[0]?.config ?? { lengthMin: 14 }),
  });
  if (!res.ok) throw new Error('the engine answered ' + res.status);
  return { results: objects.map((o) => ({ name: o.name, ok: true })) };
}
`

// stacksSrc is the Function of InfraStack: it starts the run that a stack's generation, or its deletion, asks of the
// Job it binds as stack, and answers ok once that run Succeeded.
const stacksSrc = `export async function handle(ctx, event) {
  const results = [];
  for (const o of event.data.objects) {
    const op = o.deleting ? 'destroy' : 'apply';
    const run = await ctx.job.start('stack', { op }, o.name + '-' + (o.deleting ? op : o.generation));
    const { phase } = await ctx.job.get('stack', run);
    results.push({ name: o.name, ok: phase === 'Succeeded', reason: 'Running' });
  }
  return { results };
}
`

// kindsEnv is an add-on platform that runs real Functions, and where the kinds' images lie.
type kindsEnv struct {
	*addonEnv
	artifacts string
	layouts   *layoutImages
}

// layoutImages reads each image from the OCI layout it was pushed to, in place of its registry, which answers once up.
type layoutImages struct {
	mu   sync.Mutex
	refs map[string]string
	down bool
}

func (l *layoutImages) Read(ctx context.Context, image string) (v1.RuntimeName, v1.WorkflowContract, error) {
	l.mu.Lock()
	ref, down := l.refs[image], l.down
	l.mu.Unlock()
	if down {
		return "", v1.WorkflowContract{}, fault.Unavailablef("test", "%s is down", downRegistry)
	}
	return addonImages{}.Read(ctx, ref)
}

func (l *layoutImages) set(image, ref string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refs[image] = ref
}

func (l *layoutImages) setDown(down bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.down = down
}

func startKinds(t *testing.T) *kindsEnv {
	t.Helper()
	node, err := exec.LookPath("node")
	require.NoError(t, err, "node serves the kinds' Functions")
	dir, err := os.MkdirTemp("", "funcd") // not t.TempDir(): a socket path under it would overrun the limit (#41)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	e := &kindsEnv{artifacts: dir, layouts: &layoutImages{refs: map[string]string{}}}
	e.addonEnv = startAddons(t, WithRuntimeShim(node, langmod.NodeShim(t)), WithArtifactStore(dir), WithAddonImages(e.layouts))
	return e
}

// push pushes src as a kind's image with its contract, and returns its name in the registry that is down.
func (e *kindsEnv) push(t *testing.T, src string, c v1.WorkflowContract) string {
	t.Helper()
	blob, err := artifact.ContractBlob(c.Input, []byte(`{}`))
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "kind.mjs")
	require.NoError(t, os.WriteFile(file, []byte(src), 0o600))
	layout := "oci-layout://" + t.TempDir()
	digest, err := artifact.Push(e.ctx, layout+":kind", file, blob, "nodejs22", "")
	require.NoError(t, err)
	image, ref := downRegistry+"/pyvvo/kind@"+digest, layout+"@"+digest
	e.layouts.set(image, ref)
	m, fn := artifact.NewOrasMaterializer(e.artifacts, v1.HostPlatform()), &v1.Function{}
	fn.Spec.Image, fn.Spec.ImageDigest = ref, digest
	_, err = m.Materialize(e.ctx, fn)
	require.NoError(t, err)
	_, err = m.Platforms(e.ctx, ref, digest)
	require.NoError(t, err)
	return image
}

// stricterPolicyCall refuses the policy of lengthMin 8 that policyCall takes.
const stricterPolicyCall = `{"properties":{"provider":{"type":"string"},"lengthMin":{"type":"integer","minimum":12},"lengthMax":{"type":"integer"}},` +
	`"required":["provider","lengthMin","lengthMax"]}`

// waitSettled waits until ns/name is in phase with the Ready reason reason at its generation, and returns its message.
func (e *addonEnv) waitSettled(t *testing.T, name v1.ObjectName, phase v1.Phase, reason string) string {
	t.Helper()
	var c v1.Condition
	require.Eventually(t, func() bool {
		o, ok := e.object(t, "default", name)
		if !ok {
			return false
		}
		c, _ = o.Status.Conditions.Get("Ready")
		return o.Status.Phase == phase && c.Reason == reason && c.ObservedGeneration == o.Generation
	}, kindWithin, 20*time.Millisecond, "%s is %s with reason %q; last %+v", name, phase, reason, &c)
	return c.Message
}

// sent is the body of each call of the engines, in the order they came.
func (r *addonRuntime) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.bodies)
}

// bootsIn makes each engine that starts from now on answer after passes passes.
func (r *addonRuntime) bootsIn(passes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.boots = passes
}

// waitSent waits until the last body that an engine got holds lengthMin, as rauthy would enforce it.
func (e *addonEnv) waitSent(t *testing.T, lengthMin int) {
	t.Helper()
	var last string
	require.Eventually(t, func() bool {
		sent := e.rt.sent()
		if len(sent) == 0 {
			return false
		}
		last = sent[len(sent)-1]
		var c policyConfig
		return json.Unmarshal([]byte(last), &c) == nil && c.LengthMin == lengthMin
	}, kindWithin, 20*time.Millisecond, "the engine holds lengthMin %d; last %s", lengthMin, &last)
}

// rauthyKinds is rauthy's Addon whose RauthyPasswordPolicy names the image policies and whose RauthyClient names the
// Function clients of the namespace.
func rauthyKinds(policies string) *v1.Addon {
	a := rauthySettingsAddon("rauthy")
	kinds := a.Spec.Runtimes[0].Attributes.Settings
	kinds[0].Function.Image, kinds[1].Function = policies, v1.AddonFunction{Ref: "clients"}
	return a
}

// scenario: addon-function-image — a policy brings the Function of its kind's image, which applies it to login's engine
// through self; once the policy is deleted and the engine's default is back, the Function goes.
func TestScenarioAddonFunctionOnARealFunction(t *testing.T) {
	t.Parallel()
	e := startKinds(t)
	e.register(t, rauthyKinds(e.push(t, policiesSrc, settingsContract(policyCall, clientCall))))
	e.apply(t, e.dev, identityProvider("default"))
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 8, 64))
	require.True(t, created(status), "%d %s", status, detail)
	e.waitSettled(t, "strict", v1.PhaseReady, "")
	e.waitSent(t, 8)
	require.Contains(t, e.rt.calls(), "PUT /auth/v1/password_policy")

	status, detail = e.send(t, http.MethodDelete, fmt.Sprintf(policiesPath, "default")+"/strict", addonDevToken, nil)
	require.True(t, created(status), "%d %s", status, detail)
	e.waitSent(t, 14)
	e.waitFunction(t, "default", "login-rauthypasswordpolicy", false)
}

// scenario: addon-config-no-longer-fits — login moves to a revision whose contract refuses the policy: the call is
// refused before the Function runs, the policy is ReconcileFailed naming the path, and the engine keeps it; an edit that
// fits is applied by the next call.
func TestScenarioAddonConfigNoLongerFits(t *testing.T) {
	t.Parallel()
	e := startKinds(t)
	e.register(t, rauthyKinds(e.push(t, policiesSrc, settingsContract(policyCall, clientCall))))
	e.apply(t, e.dev, identityProvider("default"))
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 8, 64))
	require.True(t, created(status), "%d %s", status, detail)
	e.waitSettled(t, "strict", v1.PhaseReady, "")

	e.register(t, rauthyKinds(e.push(t, policiesSrc, settingsContract(stricterPolicyCall, clientCall))))
	e.moved(t, "rauthy-2")
	require.Contains(t, e.waitSettled(t, "strict", v1.PhasePending, "ReconcileFailed"), "/objects/0/config/lengthMin")
	e.waitSent(t, 8)
	status, detail = e.replace(t, policiesPath, policy("default", "strict", "login", 16, 64))
	require.True(t, created(status), "%d %s", status, detail)
	e.waitSettled(t, "strict", v1.PhaseReady, "")
	e.waitSent(t, 16)
}

// A policy off its contract, stored before its image was read: its first call is refused before the Function runs, though
// funcd creates the Function in the pass that has that call due.
func TestAddonFirstCallMeetsItsContract(t *testing.T) {
	t.Parallel()
	e := startKinds(t)
	e.layouts.setDown(true)
	e.apply(t, e.admin, rauthyKinds(e.push(t, policiesSrc, settingsContract(policyCall, clientCall))))
	e.waitAddon(t, "rauthy", "ArtifactNotReady")
	e.apply(t, e.dev, identityProvider("default"))
	status, detail := e.create(t, policiesPath, settingsObject("RauthyPasswordPolicy", "default", "strict", `{"provider":"login","lengthMin":"eight","lengthMax":64}`))
	require.True(t, created(status), "no contract judges it yet: %d %s", status, detail)
	e.waitReason(t, "default", "strict", v1.PhasePending, "ArtifactNotReady")

	e.layouts.setDown(false)
	require.Contains(t, e.waitSettled(t, "strict", v1.PhasePending, "ReconcileFailed"), "/objects/0/config/lengthMin")
	require.Empty(t, e.rt.sent(), "the engine keeps what it has")
}

// A policy that the contract of login's pin refuses, whose Function is deleted by hand: funcd puts it back, and its call
// is refused.
func TestAddonRecreatedFunctionMeetsItsContract(t *testing.T) {
	t.Parallel()
	e := startKinds(t)
	e.register(t, rauthyKinds(e.push(t, policiesSrc, settingsContract(policyCall, clientCall))))
	e.apply(t, e.dev, identityProvider("default"))
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 8, 64))
	require.True(t, created(status), "%d %s", status, detail)
	e.waitSettled(t, "strict", v1.PhaseReady, "")
	e.register(t, rauthyKinds(e.push(t, policiesSrc, settingsContract(stricterPolicyCall, clientCall))))
	e.moved(t, "rauthy-2")
	e.waitSettled(t, "strict", v1.PhasePending, "ReconcileFailed")
	sent, first := len(e.rt.sent()), e.function(t, "default", "login-rauthypasswordpolicy").UID
	require.NoError(t, e.dev.Delete(e.ctx, v1.KindFunction, "default", "login-rauthypasswordpolicy"))
	require.Eventually(t, func() bool {
		fn := e.function(t, "default", "login-rauthypasswordpolicy")
		return fn != nil && fn.UID != first && fn.Status.CurrentRevision != ""
	}, kindWithin, 20*time.Millisecond, "it is back")
	require.True(t, poll.Never(func() bool { return len(e.rt.sent()) > sent }, 500*time.Millisecond, 20*time.Millisecond),
		"the engine keeps what it has")
}

// scenario: addon-config-no-longer-fits, while the Function sleeps, as it does five minutes after a call, and login's
// engine takes a while to answer: the call that its readiness has due waits for the new image's revision, and is refused.
func TestScenarioAddonConfigNoLongerFitsASleepingFunction(t *testing.T) {
	t.Parallel()
	e := startKinds(t)
	e.rt.bootsIn(3)
	e.register(t, rauthyKinds(e.push(t, policiesSrc, settingsContract(policyCall, clientCall))))
	e.apply(t, e.dev, identityProvider("default"))
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 8, 64))
	require.True(t, created(status), "%d %s", status, detail)
	e.waitSettled(t, "strict", v1.PhaseReady, "")
	require.Eventually(t, func() bool {
		fn := e.function(t, "default", "login-rauthypasswordpolicy")
		if fn.Status.Phase == v1.PhaseIdle && fn.Status.ServingRevision == "" {
			return true
		}
		fn.Status.Phase = v1.PhaseIdle
		_, _ = e.p.cfg.store.Update(e.ctx, fn)
		return false
	}, kindWithin, 50*time.Millisecond, "the Function sleeps")
	sent := len(e.rt.sent())

	// Another bundle, so another image of another revision, whose code the call would wake.
	e.register(t, rauthyKinds(e.push(t, policiesSrc+"\n", settingsContract(stricterPolicyCall, clientCall))))
	e.moved(t, "rauthy-2")
	require.Contains(t, e.waitSettled(t, "strict", v1.PhasePending, "ReconcileFailed"), "/objects/0/config/lengthMin")
	require.Len(t, e.rt.sent(), sent, "the engine keeps what it has")
}

// A stack off its contract, stored before its image was read: its first call is refused, so its Function starts no run.
func TestEnginelessFirstCallMeetsItsContract(t *testing.T) {
	t.Parallel()
	e := startKinds(t)
	e.layouts.setDown(true)
	e.applyStackJob(t)
	e.apply(t, e.admin, e.stacksAddon(t))
	e.waitAddon(t, "infrastack", "ArtifactNotReady")
	stack := edgeStack()
	stack.Spec.Config = json.RawMessage(`{"zone":"off the contract"}`)
	e.apply(t, e.dev, stack)
	e.waitReason(t, "default", "bunny-edge", v1.PhasePending, "ArtifactNotReady")

	e.layouts.setDown(false)
	require.Contains(t, e.waitSettled(t, "bunny-edge", v1.PhasePending, "ReconcileFailed"), "zone")
	runs, err := e.admin.List(e.ctx, v1.KindJobRun, "default")
	require.NoError(t, err)
	require.Empty(t, runs)
}

// scenario: addon-function-ref — no schema judges a write of a ref's object, and a call off that Function's contract
// fails, the object naming the path.
func TestScenarioAddonFunctionRefOnARealFunction(t *testing.T) {
	t.Parallel()
	e := startKinds(t)
	e.register(t, rauthyKinds(e.push(t, policiesSrc, settingsContract(policyCall, clientCall))))
	e.apply(t, e.dev, identityProvider("default"))
	clients := settingsFn("default", "login")
	clients.Name, clients.Spec.Image = "clients", e.push(t, policiesSrc, settingsContract(policyCall,
		`{"properties":{"provider":{"type":"string"},"clientId":{"type":"string","minLength":3}},"required":["provider","clientId"]}`))
	_, clients.Spec.ImageDigest, _ = strings.Cut(clients.Spec.Image, "@")
	e.apply(t, e.dev, clients)
	status, detail := e.create(t, clientsPath, client("default", "web", "w"))
	require.True(t, created(status), "%d %s", status, detail)
	require.Contains(t, e.waitSettled(t, "web", v1.PhasePending, "ReconcileFailed"), "/objects/0/config/clientId")
}

// An App that holds login and its policy turns Ready: a settings part records the App's revision it follows.
func TestAppWithASettingsPartTurnsReady(t *testing.T) {
	t.Parallel()
	e := startKinds(t)
	e.register(t, rauthyKinds(e.push(t, policiesSrc, settingsContract(policyCall, clientCall))))
	app := loginApp("default", "web-auth")
	app.Spec.Addons = append(app.Spec.Addons, v1.AppAddon{Name: "web-auth-strict", AddonObjectSpec: v1.AddonObjectSpec{
		Kind: "RauthyPasswordPolicy", Config: policy("default", "", "web-auth-login", 18, 64).Spec.Config}})
	e.apply(t, e.dev, app)
	e.waitApp(t, "default", "web-auth")
	e.waitSent(t, 18)
}

// applyStackJob stores the Job bunny-edge, whose runs end at once.
func (e *kindsEnv) applyStackJob(t *testing.T) {
	t.Helper()
	ref, digest := PushJob(t, t.TempDir(), "bunny-edge", "export function handle(ctx, event) { return { op: event.data?.op ?? null }; }\n", "")
	job := &v1.Job{TypeMeta: tmeta(v1.KindJob), ObjectMeta: v1.ObjectMeta{Name: "bunny-edge", Namespace: "default", ResourceGroup: "rg1"}}
	job.Spec.Runtime, job.Spec.Handler, job.Spec.Image, job.Spec.ImageDigest = "nodejs22", "handle", ref, digest
	job.Spec.Approval.Bypass = true
	e.apply(t, e.admin, job)
}

// stacksAddon is InfraStack's Addon, whose Function comes from stacksSrc's image.
func (e *kindsEnv) stacksAddon(t *testing.T) *v1.Addon {
	t.Helper()
	a := infraStackAddon()
	a.Spec.Runtimes[0].Attributes.Function = &v1.AddonFunction{Image: e.push(t, stacksSrc,
		v1.WorkflowContract{Input: json.RawMessage(kindCall("InfraStack", `{"properties":{"region":{"type":"string"}}}`))})}
	a.Spec.Runtimes[0].Attributes.Resync = v1.Duration(time.Second)
	return a
}

// edgeStack is the stack bunny-edge, which binds the Job bunny-edge as stack.
func edgeStack() *v1.AddonObject {
	stack := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "InfraStack"}}
	stack.Name, stack.Namespace, stack.ResourceGroup = "bunny-edge", "default", "rg1"
	stack.Spec.Config = json.RawMessage(`{"region":"fra"}`)
	stack.Spec.Bindings.Job = []v1.FunctionJob{{Alias: "stack", Job: "bunny-edge"}}
	return stack
}

// scenario: engineless-function-image — the stack bunny-edge binds the Job bunny-edge as stack, and the Function
// bunny-edge-infrastack, created from the kind's image, starts that Job's run; deleted with deletion: delete, the stack
// and its Function stay until the destroy run Succeeded.
func TestScenarioEnginelessFunctionOnARealFunction(t *testing.T) {
	t.Parallel()
	e := startKinds(t)
	e.applyStackJob(t)
	e.register(t, e.stacksAddon(t))
	stack := edgeStack()
	stack.Spec.Deletion = v1.DeletionDelete
	e.apply(t, e.dev, stack)
	e.waitSettled(t, "bunny-edge", v1.PhaseReady, "")
	runs, err := e.p.cfg.store.Get(e.ctx, v1.KindJobRun.GVK(), "default", "bunny-edge-1")
	require.NoError(t, err)
	require.Equal(t, `Function::"default/bunny-edge-infrastack"`, runs.(*v1.JobRun).Status.StartedBy)

	var held bool
	require.NoError(t, e.dev.Delete(e.ctx, "InfraStack", "default", "bunny-edge", sdk.Held(&held)))
	require.True(t, held, "the stack is held until its destroy run Succeeded")
	require.NotNil(t, e.function(t, "default", "bunny-edge-infrastack"), "its Function stays with it")
	require.Eventually(t, func() bool {
		_, there := e.object(t, "default", "bunny-edge")
		return !there && e.function(t, "default", "bunny-edge-infrastack") == nil
	}, kindWithin, 50*time.Millisecond, "its Function's ok lets both go")
	objs, err := e.admin.List(e.ctx, v1.KindJobRun, "default")
	require.NoError(t, err)
	require.True(t, slices.ContainsFunc(objs, func(o v1.Object) bool { return o.GetName() == "bunny-edge-destroy" }), "the destroy run")
}
