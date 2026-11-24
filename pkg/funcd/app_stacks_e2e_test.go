//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// The scenarios of ADR-0357 on the process driver and the real Node shim. App edge declares the Secret outputs with
// the key FRP_TOKEN, which its Job stack owns and binds, and the stack bunny, an object of the engineless kind
// InfraStack. The Function infrastack-reconciler, applied beside the App, reconciles the kind and answers as a stack
// does: a generation of a stack is Ready once an approval file names it, and a deleting stack goes once its destroy is
// approved. The test plays the approved run: it writes the approval and, as the stack's Job does, FRP_TOKEN.

// stackWithin bounds each wait of these scenarios.
const stackWithin = 20 * time.Second

// stackQuiet is how long a scenario checks that something does not happen: three supervision periods of
// stackPacing, and three resyncs of a stack.
const stackQuiet = 1500 * time.Millisecond

const stackTick = 50 * time.Millisecond

// stackPacing scales the rollout down: app.upgradeTimeout 3 s > runtime.bootTimeout 1 s > invoke.activationTimeout
// 500 ms (ADR-0200 Decision 10), and a supervision period of 500 ms, which paces a held prune and a stack's passes.
func stackPacing() funcd.Pacing {
	return funcd.Pacing{AppUpgradeTimeout: 3 * time.Second, BootTimeout: time.Second, ActivationTimeout: 500 * time.Millisecond,
		SupervisionPeriod: 500 * time.Millisecond}
}

// hookPacing is stackPacing for the scenarios with hooks, whose hook Functions keep a replica: a boot or a wake gets a
// few seconds, as the hooks suite paces them, so a loaded host fails no hook call. app.upgradeTimeout 6 s >
// runtime.bootTimeout 4 s > invoke.activationTimeout 3 s (ADR-0200 Decision 10).
func hookPacing() funcd.Pacing {
	return funcd.Pacing{AppUpgradeTimeout: 6 * time.Second, BootTimeout: 4 * time.Second, ActivationTimeout: 3 * time.Second,
		SupervisionPeriod: 500 * time.Millisecond}
}

// stackReconciler is the InfraStack kind's Function: its %q is the directory of the approvals.
const stackReconciler = `import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
const approvals = %q;
const approved = (name) => {
  const f = join(approvals, name);
  return existsSync(f) ? Number(readFileSync(f, 'utf8')) : 0;
};
const waits = (o, what) => ({ name: o.name, ok: false, reason: 'AwaitingApproval', message: what + ' waits for approval' });
export const handle = async (ctx, event) => ({
  results: (event.data?.objects ?? []).map((o) => {
    if (o.deleting) {
      return existsSync(join(approvals, o.name + '.destroy')) ? { name: o.name, ok: true } : waits(o, 'the destroy run');
    }
    return approved(o.name) >= o.generation ? { name: o.name, ok: true } : waits(o, 'the run of generation ' + o.generation);
  }),
});
`

// tunnelDigest pins the Tunnel kind's image, which tunnelEngines never pulls.
const tunnelDigest = "@sha256:0d1d2b3c4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"

// stackEnv is a platform with the InfraStack kind, reconciled by infrastack-reconciler, and the Tunnel kind, whose
// engines run on tunnelEngines; dir holds the approvals.
type stackEnv struct {
	*gcEnv
	st      store.Store
	admin   *sdk.Client
	engines *tunnelEngines
	dir     string
	// jobImage and jobDigest are the Job stack's handler, pushed as funcdctl push does.
	jobImage, jobDigest string
}

func startStacks(t *testing.T, pacing funcd.Pacing) *stackEnv {
	t.Helper()
	st := store.New(memory.New())
	engines := newTunnelEngines(t)
	e := &stackEnv{gcEnv: startGC(t, funcd.WithStore(st), funcd.WithDevAdminToken(), funcd.WithPacing(pacing),
		funcd.WithAddonRuntime(engines), funcd.WithAddonRegistries("docker.io")), st: st, engines: engines, dir: shortDataDir(t)}
	admin, err := sdk.New(e.api, sdk.WithToken(funcd.DevAdminToken))
	require.NoError(t, err)
	e.admin = admin
	writeStep(t, e.src, "infrastack-reconciler", fmt.Sprintf(stackReconciler, e.dir))
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "infrastack-reconciler", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", pushStepImage(t, e.layout, e.src, "infrastack-reconciler")
	fn.Spec.Replicas, fn.Spec.Scaling.MinReplicas = 1, 1
	e.apply(t, fn)
	for _, a := range []*v1.Addon{stackAddon(), tunnelAddon()} {
		_, err := admin.Apply(e.ctx, a)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			got, err := admin.Get(e.ctx, v1.KindAddon, "", a.Name)
			return err == nil && got.(*v1.Addon).Status.CurrentRevision != ""
		}, stackWithin, stackTick, "the Addon %s has a revision", a.Name)
	}
	e.writeOutputs(t, nil)
	e.jobImage, e.jobDigest = funcd.PushJob(t, e.layout, "stack", `export async function handle() { return {}; }`, "")
	return e
}

// stackAddon registers InfraStack, an engineless kind whose objects infrastack-reconciler reconciles, called again
// every second.
func stackAddon() *v1.Addon {
	a := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAddon.GVK().APIVersion(), Kind: v1.KindAddon}, ObjectMeta: v1.ObjectMeta{Name: "infrastack"}}
	a.Spec.Runtimes = []v1.ShippedRuntime{{Name: "infrastack", Workloads: []v1.Workload{v1.WorkloadEngineless},
		Attributes: &v1.AddonAttributes{Kind: "InfraStack",
			Config:   json.RawMessage(`{"type":"object","properties":{"region":{"type":"string"}},"additionalProperties":false}`),
			Function: &v1.AddonSettingsFunction{Ref: "infrastack-reconciler"}, Resync: v1.Duration(time.Second)}}}
	return a
}

// tunnelAddon registers Tunnel, an engine kind whose engine reads FRP_TOKEN from the Secrets its objects bind, as the
// frp-bunny App's tunnel does.
func tunnelAddon() *v1.Addon {
	a := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAddon.GVK().APIVersion(), Kind: v1.KindAddon}, ObjectMeta: v1.ObjectMeta{Name: "tunnels"}}
	a.Spec.Runtimes = []v1.ShippedRuntime{{Name: "tunnel", Image: "docker.io/pyvvo/tunnel" + tunnelDigest, Workloads: []v1.Workload{v1.WorkloadEngine},
		Attributes: &v1.AddonAttributes{Kind: "Tunnel", Port: 7000, Readiness: v1.AddonReadiness{Path: "/healthz"}, SecretKeys: []string{"FRP_TOKEN"}}}}
	return a
}

// tunnelEngines runs the Tunnel kind's engines: each is Ready at once, served by one test server, and keeps the
// environment it started with.
type tunnelEngines struct {
	addr string
	mu   sync.Mutex
	env  map[provider.ProviderRef]map[string]string
}

func newTunnelEngines(t *testing.T) *tunnelEngines {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	return &tunnelEngines{addr: strings.TrimPrefix(srv.URL, "http://"), env: map[provider.ProviderRef]map[string]string{}}
}

func (r *tunnelEngines) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.env[spec.Ref] = spec.Env
	return provider.ProviderStatus{Running: 1, Ready: true, Address: r.addr}, nil
}

func (r *tunnelEngines) Teardown(context.Context, provider.ProviderRef) error { return nil }

// envOf is the environment the engine of ns/name started with, nil before it started.
func (r *tunnelEngines) envOf(ns v1.NamespaceName, name v1.ObjectName) map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.env[provider.ProviderRef{Namespace: ns, Name: name}]
}

// writeOutputs applies the Secret outputs, owned by the Job stack, with data: the stack's Job writes its outputs there
// (ADR-0356), which the test does in its place.
func (e *stackEnv) writeOutputs(t *testing.T, data map[string][]byte) {
	t.Helper()
	e.writeSecret(t, "outputs", v1.JobOwner("stack"), data)
}

func (e *stackEnv) writeSecret(t *testing.T, name v1.ObjectName, owner v1.WorkloadRef, data map[string][]byte) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindSecret)
	s := obj.(*v1.Secret)
	s.Name, s.Namespace, s.ResourceGroup = name, "default", "rg1"
	s.Spec.Owner, s.Spec.Data = owner, data
	e.apply(t, s)
}

// frpToken is the outputs Secret's data once the approved run wrote it.
func frpToken() map[string][]byte { return map[string][]byte{"FRP_TOKEN": []byte("token-1")} }

// approveStack approves the run of the stack name's generation gen, which then applies it.
func (e *stackEnv) approveStack(t *testing.T, name string, gen int64) {
	t.Helper()
	tmp := filepath.Join(e.dir, name+".tmp")
	require.NoError(t, os.WriteFile(tmp, []byte(fmt.Sprint(gen)), 0o600))
	require.NoError(t, os.Rename(tmp, filepath.Join(e.dir, name)))
}

// approveDestroy approves the destroy run of the stack name.
func (e *stackEnv) approveDestroy(t *testing.T, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(e.dir, name+".destroy"), nil, 0o600))
}

// edgeApp is App edge: the Secret outputs with the key FRP_TOKEN, which its Job stack owns and binds, and the stack
// bunny in region; edit adds what a scenario needs.
func (e *stackEnv) edgeApp(region string, edit func(*v1.App)) *v1.App {
	a := &v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: "edge", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.AppSpec{
			Secrets: []v1.SecretDeclaration{{Name: "outputs", Keys: []string{"FRP_TOKEN"}}},
			Addons: []v1.AppAddon{{Name: "bunny", AddonObjectSpec: v1.AddonObjectSpec{Kind: "InfraStack",
				Config: json.RawMessage(fmt.Sprintf(`{"region":%q}`, region))}}},
			Jobs: []v1.AppJob{{Name: "stack", JobSpec: v1.JobSpec{Runtime: "nodejs22", Handler: "handle", Image: e.jobImage, ImageDigest: e.jobDigest,
				Bindings: v1.FunctionBindings{Secret: []v1.ObjectName{"outputs"}}}}},
		},
	}
	if edit != nil {
		edit(a)
	}
	return a
}

// readsOutputs is the functions entry name, one replica up, running img and reading FRP_TOKEN from outputs.
func readsOutputs(name v1.ObjectName, img string) v1.AppFunction {
	return v1.AppFunction{Name: name, FunctionSpec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: img,
		Scaling: v1.Scaling{MinReplicas: 1}, Bindings: v1.FunctionBindings{Secret: []v1.ObjectName{"outputs"}}}}
}

// waitEdge waits until App edge satisfies cond, and returns it.
func (e *stackEnv) waitEdge(t *testing.T, what string, cond func(*v1.App) bool) *v1.App {
	t.Helper()
	var a *v1.App
	var last []v1.AppChild
	require.Eventually(t, func() bool {
		a = e.app(t, "edge")
		last = a.Status.Children
		return cond(a)
	}, stackWithin, stackTick, "App edge: %s; last children %+v", what, &last)
	return a
}

// lineOf is App a's status line of the part kind/name, the zero line when it has none.
func lineOf(a *v1.App, kind v1.Kind, name v1.ObjectName) v1.AppChild {
	for _, c := range a.Status.Children {
		if c.Kind == kind && c.Name == name {
			return c
		}
	}
	return v1.AppChild{}
}

func edgeLine(kind v1.Kind, name v1.ObjectName, state v1.AppChildState, reason string) v1.AppChild {
	return v1.AppChild{Kind: kind, Name: name, State: state, Reason: reason}
}

// bunnyAwaits is the line of the stack bunny while its run waits for an approval.
func bunnyAwaits() v1.AppChild {
	return edgeLine("InfraStack", "bunny", v1.AppChildLagging, "AwaitingApproval")
}

// waitsForToken is the line of a part that waits for FRP_TOKEN (ADR-0357 Decision 2).
func waitsForToken(kind v1.Kind, name v1.ObjectName) v1.AppChild {
	return edgeLine(kind, name, v1.AppChildLagging, "SecretKeyMissing: Secret/outputs FRP_TOKEN")
}

func isReady(a *v1.App) bool { return readyCondition(a).Status == v1.ConditionTrue }

// scenario: app-stack-approval-never-fails — App edge's stack bunny waits for an approval that nobody gives: edge-1
// switches while bunny lags and, past app.upgradeTimeout, is still current, the App Ready and bunny Lagging
// AwaitingApproval.
//
// oracle: docs/design/app/stack-rollout.fizz#NoFailWhileAStackWaits
// oracle: docs/design/app/stack-rollout.fizz#Switches
func TestScenarioAppStackApprovalNeverFails(t *testing.T) {
	t.Parallel()
	e := startStacks(t, stackPacing())
	e.apply(t, e.edgeApp("de", nil))
	e.waitEdge(t, "edge-1 switches while bunny waits for its approval", func(a *v1.App) bool {
		return a.Status.CurrentRevision == "edge-1" && lineOf(a, "InfraStack", "bunny") == bunnyAwaits()
	})
	started := e.appRevision(t, "edge-1").Status.StartedAt
	require.NotNil(t, started)
	deadline := time.Time(*started).Add(stackPacing().AppUpgradeTimeout)
	stays(t, func() bool {
		return e.app(t, "edge").Status.CurrentRevision == "edge-1" && e.appRevision(t, "edge-1").Status.Phase != v1.PhaseFailed
	}, time.Until(deadline)+time.Second, stackTick, "edge-1 stays current past app.upgradeTimeout")

	a := e.app(t, "edge")
	require.Equal(t, v1.PhaseReady, a.Status.Phase)
	require.True(t, isReady(a), "%+v", readyCondition(a))
	require.Equal(t, bunnyAwaits(), lineOf(a, "InfraStack", "bunny"))
	require.Equal(t, edgeLine(v1.KindJob, "stack", v1.AppChildReady, ""), lineOf(a, v1.KindJob, "stack"))
	require.Equal(t, v1.PhaseReady, e.appRevision(t, "edge-1").Status.Phase)
}

// scenario: app-stack-change-keeps-applied-state — with bunny applied, an upgrade changes its region: edge-2 switches
// while bunny lags at its new generation, the App staying Ready, and bunny is Ready once that generation's run is
// approved.
func TestScenarioAppStackChangeKeepsAppliedState(t *testing.T) {
	t.Parallel()
	e := startStacks(t, stackPacing())
	e.approveStack(t, "bunny", 1)
	e.apply(t, e.edgeApp("de", nil))
	e.waitEdge(t, "edge-1 is current with bunny applied", func(a *v1.App) bool {
		return a.Status.CurrentRevision == "edge-1" && isReady(a) &&
			lineOf(a, "InfraStack", "bunny") == edgeLine("InfraStack", "bunny", v1.AppChildReady, "")
	})

	apps := watchKind(t, e.st, v1.KindApp)
	e.apply(t, e.edgeApp("fi", nil))
	e.waitEdge(t, "edge-2 switches while bunny lags", func(a *v1.App) bool {
		return a.Status.CurrentRevision == "edge-2" && lineOf(a, "InfraStack", "bunny").State == v1.AppChildLagging
	})
	stack := e.object(t, "InfraStack", "bunny").(*v1.AddonObject)
	require.Equal(t, int64(2), stack.Generation)
	require.JSONEq(t, `{"region":"fi"}`, string(stack.Spec.Config), "the new variables are written")
	seenRV(t, apps, "edge-2 switches", func(ev store.Event) bool {
		a, ok := ev.Object.(*v1.App)
		return ok && a.Name == "edge" && a.Status.CurrentRevision == "edge-2"
	})
	for _, ev := range apps.events(t) {
		if a, ok := ev.Object.(*v1.App); ok && a.Name == "edge" {
			require.True(t, isReady(a), "the App stays Ready through the upgrade: %+v", readyCondition(a))
		}
	}

	e.approveStack(t, "bunny", 2)
	a := e.waitEdge(t, "bunny is Ready at its new generation", func(a *v1.App) bool {
		return isReady(a) && lineOf(a, "InfraStack", "bunny") == edgeLine("InfraStack", "bunny", v1.AppChildReady, "")
	})
	require.Equal(t, v1.PhaseReady, a.Status.Phase)
	require.Equal(t, v1.ObjectName("edge-2"), a.Status.CurrentRevision)
}

// scenario: app-part-waits-for-outputs — App edge's Tunnel tunnel binds outputs, which holds no FRP_TOKEN: the Job
// stack and the stack bunny are written and tunnel is not, Lagging SecretKeyMissing: Secret/outputs FRP_TOKEN; once
// the approved run writes FRP_TOKEN, tunnel is written, its engine gets the token, and status.lastSelfHeal stays empty.
//
// oracle: docs/design/app/stack-rollout.fizz#ReaderWaitsForOutputs
// oracle: docs/design/app/stack-rollout.fizz#ConvergesOnceApproved
func TestScenarioAppPartWaitsForOutputs(t *testing.T) {
	t.Parallel()
	e := startStacks(t, stackPacing())
	e.apply(t, e.edgeApp("de", func(a *v1.App) {
		a.Spec.Addons = append(a.Spec.Addons, v1.AppAddon{Name: "tunnel", AddonObjectSpec: v1.AddonObjectSpec{Kind: "Tunnel",
			Bindings: v1.AddonObjectBindings{Secret: []v1.ObjectName{"outputs"}}}})
	}))
	e.waitEdge(t, "tunnel waits for FRP_TOKEN", func(a *v1.App) bool {
		return a.Status.CurrentRevision == "edge-1" && lineOf(a, "Tunnel", "tunnel") == waitsForToken("Tunnel", "tunnel")
	})
	require.True(t, e.exists(t, v1.KindJob, "stack"), "the Job that writes the outputs is written")
	require.True(t, e.exists(t, "InfraStack", "bunny"), "the stack is written")
	stays(t, func() bool { return !e.exists(t, "Tunnel", "tunnel") }, stackQuiet, stackTick, "tunnel is not written while FRP_TOKEN is missing")

	e.approveStack(t, "bunny", 1)
	e.writeOutputs(t, frpToken())
	a := e.waitEdge(t, "tunnel and bunny are Ready", func(a *v1.App) bool {
		return isReady(a) && lineOf(a, "Tunnel", "tunnel") == edgeLine("Tunnel", "tunnel", v1.AppChildReady, "") &&
			lineOf(a, "InfraStack", "bunny") == edgeLine("InfraStack", "bunny", v1.AppChildReady, "")
	})
	require.Equal(t, "token-1", e.engines.envOf("default", "tunnel")["FRP_TOKEN"], "the engine reads the output")
	require.Nil(t, a.Status.LastSelfHeal, "the write that ends the wait is no self-heal")
	require.Equal(t, v1.PhaseReady, a.Status.Phase)
}

// scenario: app-late-reader-gets-full-timeout — while the booting Function slow holds edge-1, the approved run writes
// FRP_TOKEN 3 s into app.upgradeTimeout 6 s; relay, which reads it, is written then and boots past the first deadline,
// which has moved to a full app.upgradeTimeout after that write, so edge-1 switches once both are Ready. It runs about
// 8 s: slow and relay each boot for 3.5 s, under a runtime.bootTimeout of 5 s, so that relay is still booting at the
// first deadline.
//
// oracle: docs/design/app/stack-rollout.fizz#NoFailWhileAStackWaits
func TestScenarioAppLateReaderGetsFullTimeout(t *testing.T) {
	t.Parallel()
	pacing := funcd.Pacing{AppUpgradeTimeout: 6 * time.Second, BootTimeout: 5 * time.Second, ActivationTimeout: 500 * time.Millisecond,
		SupervisionPeriod: 500 * time.Millisecond}
	e := startStacks(t, pacing)
	writeStep(t, e.src, "slow", `await new Promise((resolve) => setTimeout(resolve, 3500));
export async function handle(event) { return event; }`)
	img := pushStepImage(t, e.layout, e.src, "slow")
	slow := readsOutputs("slow", img)
	slow.Bindings.Secret = nil
	e.apply(t, e.edgeApp("de", func(a *v1.App) { a.Spec.Functions = []v1.AppFunction{slow, readsOutputs("relay", img)} }))
	e.waitEdge(t, "slow boots and relay waits", func(a *v1.App) bool {
		return lineOf(a, v1.KindFunction, "slow").State == v1.AppChildPending && lineOf(a, v1.KindFunction, "relay") == waitsForToken(v1.KindFunction, "relay")
	})
	started := e.appRevision(t, "edge-1").Status.StartedAt
	require.NotNil(t, started)
	first := time.Time(*started).Add(pacing.AppUpgradeTimeout)

	time.Sleep(time.Until(time.Time(*started).Add(3 * time.Second))) // the run ends half way to the deadline
	a := e.app(t, "edge")
	require.Equal(t, v1.AppChildPending, lineOf(a, v1.KindFunction, "slow").State, "slow still holds edge-1")
	require.Empty(t, a.Status.CurrentRevision)
	wrote := time.Now()
	e.writeOutputs(t, frpToken())

	a = e.waitEdge(t, "edge-1 switches once slow and relay are Ready", func(a *v1.App) bool { return a.Status.CurrentRevision == "edge-1" })
	require.NotNil(t, a.Status.LagEndedAt, "the end of relay's wait is a start term of the deadline")
	ended := time.Time(*a.Status.LagEndedAt)
	require.True(t, !ended.Before(wrote.Truncate(time.Millisecond)) && ended.Before(first), "relay's wait ends at %s, after the write at %s", ended, wrote)
	rev := e.waitRevisionPhase(t, "edge-1", v1.PhaseReady, stackWithin)
	switched := time.Time(condition(rev, "Current").LastTransitionTime)
	require.True(t, switched.After(first), "edge-1 switches at %s, past its first deadline %s", switched, first)
	require.True(t, switched.Before(ended.Add(pacing.AppUpgradeTimeout)), "edge-1 switches at %s, within a full app.upgradeTimeout of %s", switched, ended)
	require.Equal(t, edgeLine(v1.KindFunction, "relay", v1.AppChildReady, ""), lineOf(e.app(t, "edge"), v1.KindFunction, "relay"))
}

// scenario: app-pre-hook-ignores-stack — App edge's pre-hook Function migrate binds outputs, which holds no FRP_TOKEN:
// migrate is written and called as before, and finds no output, before the stack bunny is written at all.
func TestScenarioAppPreHookIgnoresStack(t *testing.T) {
	t.Parallel()
	e := startStacks(t, hookPacing())
	f := newHookFiles(t)
	writeStep(t, e.src, "migrate", fmt.Sprintf(`import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
export async function handle(ctx, event) {
  writeFileSync(join(%q, event.id), (await ctx.secret.get('FRP_TOKEN')) ?? 'absent');
  return { ok: true };
}`, f.calls))
	migrate := readsOutputs("migrate", pushStepImage(t, e.layout, e.src, "migrate"))
	revs, stacks := watchKind(t, e.st, v1.KindAppRevision), watchKind(t, e.st, v1.KindAddonObject)
	e.apply(t, e.edgeApp("de", func(a *v1.App) {
		a.Spec.Functions = []v1.AppFunction{migrate}
		a.Spec.Hooks = &v1.AppHooks{PreApply: []v1.AppHook{{Function: "migrate"}}}
	}))
	e.waitEdge(t, "edge-1 switches", func(a *v1.App) bool {
		return a.Status.CurrentRevision == "edge-1" && lineOf(a, "InfraStack", "bunny").State == v1.AppChildLagging
	})

	one := e.appRevision(t, "edge-1")
	require.Len(t, one.Status.Hooks, 1, "migrate is called once")
	require.Equal(t, "preApply", one.Status.Hooks[0].Point)
	require.Equal(t, v1.PhaseReady, one.Status.Hooks[0].Phase)
	ents, err := os.ReadDir(f.calls)
	require.NoError(t, err)
	require.Len(t, ents, 1)
	found, err := os.ReadFile(filepath.Join(f.calls, ents[0].Name()))
	require.NoError(t, err)
	require.Equal(t, "absent", string(found), "the pre-hook finds no output before the first apply")
	recorded := seenRV(t, revs, "edge-1 records migrate's call", hookRecorded("edge-1", v1.PhaseReady))
	written := seenRV(t, stacks, "bunny is written", func(ev store.Event) bool {
		return ev.Type == store.Added && ev.Object.GetObjectMeta().Name == "bunny"
	})
	require.Less(t, recorded, written, "the pre-hook runs before the stack is written")
}

// scenario: app-post-hook-after-stack — edge-1 switches while the stack bunny lags and relay waits for FRP_TOKEN, and
// the post-hook warm is called only once bunny is Ready and relay is written and Ready, in either order: with the key
// written first, relay is Ready while bunny still lags; with the approved run writing the key as it applies bunny,
// bunny is Ready while relay still boots.
//
// oracle: docs/design/app/stack-rollout.fizz#PostHookAfterStacks
func TestScenarioAppPostHookAfterStack(t *testing.T) {
	t.Parallel()
	t.Run("the key first", func(t *testing.T) {
		t.Parallel()
		e := startStacks(t, hookPacing())
		f := e.warmHook(t, readsOutputs("relay", e.image(t)))
		e.waitEdge(t, "edge-1 switches while bunny and relay lag", func(a *v1.App) bool {
			return a.Status.CurrentRevision == "edge-1" && lineOf(a, "InfraStack", "bunny") == bunnyAwaits() &&
				lineOf(a, v1.KindFunction, "relay") == waitsForToken(v1.KindFunction, "relay")
		})
		stays(t, func() bool { return f.count(t) == 0 }, stackQuiet, stackTick, "warm is not called while bunny and relay lag")

		e.writeOutputs(t, frpToken())
		e.waitEdge(t, "relay is written and Ready", func(a *v1.App) bool {
			return lineOf(a, v1.KindFunction, "relay") == edgeLine(v1.KindFunction, "relay", v1.AppChildReady, "")
		})
		stays(t, func() bool { return f.count(t) == 0 }, stackQuiet, stackTick, "warm is not called while bunny lags")

		stacks, revs := watchKind(t, e.st, v1.KindAddonObject), watchKind(t, e.st, v1.KindAppRevision)
		e.approveStack(t, "bunny", 1)
		e.warmCalled(t, f)
		require.Less(t, seenRV(t, stacks, "bunny is Ready", readyWrite("bunny")), seenRV(t, revs, "edge-1 records warm's call",
			hookRecorded("edge-1", v1.PhaseReady)), "warm runs after bunny is Ready")
	})
	t.Run("the approved run writes the key", func(t *testing.T) {
		t.Parallel()
		e := startStacks(t, funcd.Pacing{AppUpgradeTimeout: 10 * time.Second, BootTimeout: 8 * time.Second,
			ActivationTimeout: 3 * time.Second, SupervisionPeriod: 500 * time.Millisecond})
		booted := filepath.Join(shortDataDir(t), "booted")
		writeStep(t, e.src, "relay", fmt.Sprintf(`import { existsSync } from 'node:fs';
while (!existsSync(%q)) await new Promise((resolve) => setTimeout(resolve, 50));
export async function handle(event) { return event; }`, booted))
		f := e.warmHook(t, readsOutputs("relay", pushStepImage(t, e.layout, e.src, "relay")))
		e.waitEdge(t, "edge-1 switches while bunny and relay lag", func(a *v1.App) bool {
			return a.Status.CurrentRevision == "edge-1" && lineOf(a, "InfraStack", "bunny") == bunnyAwaits() &&
				lineOf(a, v1.KindFunction, "relay") == waitsForToken(v1.KindFunction, "relay")
		})

		fns, revs := watchKind(t, e.st, v1.KindFunction), watchKind(t, e.st, v1.KindAppRevision)
		e.approveStack(t, "bunny", 1)
		e.writeOutputs(t, frpToken())
		e.waitEdge(t, "bunny is Ready while relay boots", func(a *v1.App) bool {
			return lineOf(a, "InfraStack", "bunny") == edgeLine("InfraStack", "bunny", v1.AppChildReady, "") &&
				lineOf(a, v1.KindFunction, "relay").State == v1.AppChildPending
		})
		stays(t, func() bool { return f.count(t) == 0 }, stackQuiet, stackTick, "warm is not called while relay boots")

		require.NoError(t, os.WriteFile(booted, nil, 0o600))
		e.warmCalled(t, f)
		require.Less(t, seenRV(t, fns, "relay is Ready", readyWrite("relay")), seenRV(t, revs, "edge-1 records warm's call",
			hookRecorded("edge-1", v1.PhaseReady)), "warm runs after relay is Ready")
	})
}

// warmHook applies App edge with reader and the post-hook warm, which keeps a replica, and returns the files warm marks
// each of its calls in.
func (e *stackEnv) warmHook(t *testing.T, reader v1.AppFunction) hookFiles {
	t.Helper()
	f := newHookFiles(t)
	writeStep(t, e.src, "warm", fmt.Sprintf(`import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
export async function handle(ctx, event) { writeFileSync(join(%q, event.id), ''); return { ok: true }; }`, f.calls))
	warm := v1.AppFunction{Name: "warm", FunctionSpec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle",
		Image: pushStepImage(t, e.layout, e.src, "warm"), Scaling: v1.Scaling{MinReplicas: 1}}}
	e.apply(t, e.edgeApp("de", func(a *v1.App) {
		a.Spec.Functions = []v1.AppFunction{reader, warm}
		a.Spec.Hooks = &v1.AppHooks{PostApply: []v1.AppHook{{Function: "warm"}}}
	}))
	return f
}

// warmCalled waits until edge-1 records one Ready postApply call of warm, and checks that warm was called once.
func (e *stackEnv) warmCalled(t *testing.T, f hookFiles) {
	t.Helper()
	require.Eventually(t, func() bool {
		hooks := e.appRevision(t, "edge-1").Status.Hooks
		return len(hooks) == 1 && hooks[0].Phase == v1.PhaseReady
	}, stackWithin, stackTick, "warm is called")
	require.Equal(t, 1, f.count(t), "warm is called once")
	require.Equal(t, "postApply", e.appRevision(t, "edge-1").Status.Hooks[0].Point)
}

// readyWrite matches a write in which the part name is Ready: an add-on object's Ready condition True, or a Function's
// phase Ready.
func readyWrite(name v1.ObjectName) func(store.Event) bool {
	return func(ev store.Event) bool {
		switch o := ev.Object.(type) {
		case *v1.AddonObject:
			c, _ := o.Status.Conditions.Get("Ready")
			return o.Name == name && c.Status == v1.ConditionTrue
		case *v1.Function:
			return o.Name == name && o.Status.Phase == v1.PhaseReady
		}
		return false
	}
}

// seenRV waits until w has recorded a write that match accepts, since a read can show a write before the watch delivers
// it, and returns the resourceVersion of the first such write.
func seenRV(t *testing.T, w *kindWatch, what string, match func(store.Event) bool) uint64 {
	t.Helper()
	require.Eventually(t, func() bool { return slices.ContainsFunc(w.seen(), match) }, stackWithin, stackTick,
		"the watch records a write where %s", what)
	return firstRV(t, w.seen(), what, match)
}

// scenario: app-prune-waits-for-lagging — an upgrade drops the Function old and adds new, which binds outputs before
// the stack's Job has written FRP_TOKEN: edge-2 switches, and old stays Pruning, with no reason, and serves while new
// lags; it goes only once new is written and Ready. App edge declares no stack part here, so new is the only part
// that lags.
//
// oracle: docs/design/app/stack-rollout.fizz#OldPartServesUntilReplaced
func TestScenarioAppPruneWaitsForLagging(t *testing.T) {
	t.Parallel()
	e := startStacks(t, stackPacing())
	img := e.image(t)
	old := readsOutputs("old", img)
	old.Bindings.Secret = nil
	e.apply(t, e.edgeApp("de", func(a *v1.App) { a.Spec.Addons, a.Spec.Functions = nil, []v1.AppFunction{old} }))
	e.waitEdge(t, "edge-1 is current and Ready", func(a *v1.App) bool {
		return a.Status.CurrentRevision == "edge-1" && isReady(a) && lineOf(a, v1.KindFunction, "old").State == v1.AppChildReady
	})

	e.apply(t, e.edgeApp("de", func(a *v1.App) { a.Spec.Addons, a.Spec.Functions = nil, []v1.AppFunction{readsOutputs("new", img)} }))
	pruning := edgeLine(v1.KindFunction, "old", v1.AppChildPruning, "")
	e.waitEdge(t, "edge-2 switches while new lags", func(a *v1.App) bool {
		return a.Status.CurrentRevision == "edge-2" && lineOf(a, v1.KindFunction, "new") == waitsForToken(v1.KindFunction, "new") &&
			lineOf(a, v1.KindFunction, "old") == pruning
	})
	stays(t, func() bool {
		return lineOf(e.app(t, "edge"), v1.KindFunction, "old") == pruning && e.invoke(t, "old") == http.StatusOK
	}, stackQuiet, stackTick, "old stays Pruning, and serves, while new lags")

	fns := watchKind(t, e.st, v1.KindFunction)
	e.writeOutputs(t, frpToken())
	e.waitGone(t, v1.KindFunction, "old")
	pruned := seenRV(t, fns, "old is deleted", func(ev store.Event) bool {
		return ev.Type == store.Deleted && ev.Object.GetObjectMeta().Name == "old"
	})
	replaced := seenRV(t, fns, "new is Ready", readyWrite("new"))
	require.Less(t, replaced, pruned, "old goes only once new is Ready")
	e.waitEdge(t, "edge-2 is Ready with no Pruning line", func(a *v1.App) bool {
		return isReady(a) && lineOf(a, v1.KindFunction, "old") == v1.AppChild{}
	})
}

// scenario: app-dropped-stack-destroyed — an upgrade drops the stack bunny, set to deletion: delete: edge-2 switches,
// and bunny stays Pruning, Deleting, held at its deletion, until its destroy run is approved; then it goes.
func TestScenarioAppDroppedStackDestroyed(t *testing.T) {
	t.Parallel()
	e := startStacks(t, stackPacing())
	e.approveStack(t, "bunny", 1)
	e.apply(t, e.edgeApp("de", func(a *v1.App) { a.Spec.Addons[0].Deletion = v1.DeletionDelete }))
	e.waitEdge(t, "edge-1 is current with bunny applied", func(a *v1.App) bool {
		return a.Status.CurrentRevision == "edge-1" && lineOf(a, "InfraStack", "bunny") == edgeLine("InfraStack", "bunny", v1.AppChildReady, "")
	})

	e.apply(t, e.edgeApp("de", func(a *v1.App) { a.Spec.Addons = nil }))
	held := edgeLine("InfraStack", "bunny", v1.AppChildPruning, "Deleting")
	e.waitEdge(t, "edge-2 switches and holds bunny", func(a *v1.App) bool {
		return a.Status.CurrentRevision == "edge-2" && lineOf(a, "InfraStack", "bunny") == held
	})
	stays(t, func() bool {
		obj, err := e.c.Get(e.ctx, "InfraStack", "default", "bunny")
		return err == nil && obj.GetObjectMeta().DeletionTime != nil && lineOf(e.app(t, "edge"), "InfraStack", "bunny") == held
	}, stackQuiet, stackTick, "bunny is held until its destroy run is approved")

	e.approveDestroy(t, "bunny")
	e.waitGone(t, "InfraStack", "bunny")
	e.waitEdge(t, "edge-2 is Ready with no Pruning line", func(a *v1.App) bool {
		return isReady(a) && lineOf(a, "InfraStack", "bunny") == v1.AppChild{}
	})
}

// scenario: app-other-secret-still-stops — App edge also declares billing, owned by a Job it does not declare, and
// audit, owned by nothing, each lacking API_KEY: a pass stops with SecretKeyMissing at each in turn and writes no part,
// as before, while outputs, which its Job stack owns, stops none; once both hold API_KEY, the parts are written. Its
// app.upgradeTimeout of 20 s outlasts both stops, which would otherwise fail edge-1, as ADR-0213 Decision 8 has it.
func TestScenarioAppOtherSecretStillStops(t *testing.T) {
	t.Parallel()
	pacing := stackPacing()
	pacing.AppUpgradeTimeout = 20 * time.Second
	e := startStacks(t, pacing)
	e.writeSecret(t, "billing", v1.JobOwner("elsewhere"), nil)
	e.writeSecret(t, "audit", v1.WorkloadRef{}, nil)
	e.apply(t, e.edgeApp("de", func(a *v1.App) {
		a.Spec.Secrets = append(a.Spec.Secrets, v1.SecretDeclaration{Name: "billing", Keys: []string{"API_KEY"}},
			v1.SecretDeclaration{Name: "audit", Keys: []string{"API_KEY"}})
	}))
	noPart := func() bool { return !e.exists(t, v1.KindJob, "stack") && !e.exists(t, "InfraStack", "bunny") }
	for _, s := range []struct {
		name  v1.ObjectName
		owner v1.WorkloadRef
	}{{"billing", v1.JobOwner("elsewhere")}, {"audit", v1.WorkloadRef{}}} {
		a := e.waitEdge(t, "the pass stops at "+string(s.name), func(a *v1.App) bool {
			c := readyCondition(a)
			return c.Status == v1.ConditionFalse && c.Reason == "SecretKeyMissing" &&
				c.Message == "Secret/"+string(s.name)+": lacks the declared key API_KEY"
		})
		require.Equal(t, v1.PhaseDeploying, a.Status.Phase)
		stays(t, noPart, stackQuiet, stackTick, "no part is written while "+string(s.name)+" lacks API_KEY")
		e.writeSecret(t, s.name, s.owner, map[string][]byte{"API_KEY": []byte("k")})
	}
	e.waitEdge(t, "edge-1 switches once billing and audit are complete", func(a *v1.App) bool {
		return a.Status.CurrentRevision == "edge-1" && isReady(a) && lineOf(a, "InfraStack", "bunny") == bunnyAwaits()
	})
	require.True(t, e.exists(t, v1.KindJob, "stack"))
}
