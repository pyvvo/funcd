package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// viewClock is a fixed clock the view tests advance.
type viewClock struct{ t time.Time }

func (c *viewClock) now() time.Time      { return c.t }
func (c *viewClock) add(d time.Duration) { c.t = c.t.Add(d) }
func newViewClock() *viewClock           { return &viewClock{t: time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)} }
func stamp(t time.Time) *v1.Timestamp    { s := v1.NewTimestamp(t); return &s }
func child(kind v1.Kind, name v1.ObjectName, state v1.AppChildState, reason string) v1.AppChild {
	return v1.AppChild{Kind: kind, Name: name, State: state, Reason: reason}
}

// shopApp is an App whose Function binds its KVStore and its Lake, whose Route calls the Function and whose Sensor
// listens to its EventSource.
func shopApp() *v1.App {
	a := &v1.App{ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "shop"}, Spec: v1.AppSpec{Version: "1.4.0",
		KV:    []v1.KVStoreEntry{{Name: "cart"}},
		Lakes: []v1.LakeEntry{{Name: "events"}},
		Functions: []v1.AppFunction{{Name: "api", FunctionSpec: v1.FunctionSpec{Bindings: v1.FunctionBindings{
			KV: []v1.FunctionKV{{Store: "cart"}}, Lake: []v1.FunctionLake{{Lake: "events"}}}}}},
		EventSources: []v1.AppEventSource{{Name: "orders"}},
		Sensors: []v1.AppSensor{{Name: "watch", SensorSpec: v1.SensorSpec{On: []v1.Dependency{{Name: "new", Source: "orders"}},
			Do: []v1.Action{{Name: "call", On: "new", Function: "api"}}}}},
		Routes: []v1.AppRoute{{Name: "api", RouteSpec: v1.RouteSpec{Rules: []v1.RouteRule{{Path: "/api",
			Backend: v1.RouteBackend{Function: "api"}}}}}},
	}}
	a.Status.Children = []v1.AppChild{
		child(v1.KindKVStore, "cart", v1.AppChildReady, ""),
		child(v1.KindFunction, "api", v1.AppChildPending, "LakeNotReady"),
		child(v1.KindLake, "events", v1.AppChildPending, "Progressing"),
		child(v1.KindEventSource, "orders", v1.AppChildPending, "Progressing"),
		child(v1.KindSensor, "watch", v1.AppChildPending, "Progressing"),
		child(v1.KindRoute, "api", v1.AppChildReady, ""),
	}
	return a
}

func shopRevision(a *v1.App, started time.Time) *v1.AppRevision {
	rev := &v1.AppRevision{ObjectMeta: v1.ObjectMeta{Namespace: a.Namespace, Name: "shop-7"}}
	rev.Spec.Spec = a.Spec
	rev.Status.Phase = v1.PhaseDeploying
	rev.Status.StartedAt, rev.Status.Deadline = stamp(started), stamp(started.Add(5*time.Minute))
	return rev
}

// requireLines fails unless got holds each of want, one after the other.
func requireLines(t *testing.T, got string, want ...string) {
	t.Helper()
	at := 0
	for _, w := range want {
		i := strings.Index(got[at:], w)
		require.GreaterOrEqual(t, i, 0, "%q after %q in:\n%s", w, got[:at], got)
		at += i + len(w)
	}
}

// scenario: app-view-deploy — on a terminal the deploy view is a box with the revision, the plan's counts, the parts
// ready, the elapsed time and the deadline, and a row per part with its action, state, reason and time; the next
// poll redraws it in place, and on a narrow terminal no line is wider than the terminal.
func TestScenarioAppViewDeploy(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	v := newAppView(&out, true, clk.now)
	a := shopApp()
	rev := shopRevision(a, clk.t)
	plan := map[v1.ObjectRef]v1.PlanAction{
		{Kind: v1.KindKVStore, Name: "cart"}: v1.PlanUpdate, {Kind: v1.KindFunction, Name: "api"}: v1.PlanUpdate,
		{Kind: v1.KindLake, Name: "events"}: v1.PlanCreate,
	}
	clk.add(21 * time.Second)
	require.NoError(t, v.deploy(a, rev, plan, nil, appParts(a), nil))
	first := out.String()
	requireLines(t, first,
		"● funcd app deploy", "shop 1.4.0 · revision shop-7 · Ctrl-C stops the wait",
		"plan  1 create · 2 update · 0 prune · 0 hooks",
		"2/6 ready", "21s", "deadline in 4m39s",
		"PARTS", "✓ KVStore/cart", "update", "Ready", "0s",
		"◐ Function/api", "update", "Pending LakeNotReady", "Lake/events", "0s…",
		"◐ Lake/events", "create", "Pending Progressing",
	)
	require.NotContains(t, first, "\x1b[", "a buffer's renderer draws no color and the first draw moves no cursor")

	clk.add(4 * time.Second)
	a.Status.Children[2] = child(v1.KindLake, "events", v1.AppChildReady, "")
	require.NoError(t, v.deploy(a, rev, plan, nil, appParts(a), nil))
	second := out.String()[len(first):]
	require.True(t, strings.HasPrefix(second, fmt.Sprintf("\x1b[%dA\x1b[J", strings.Count(first, "\n"))), "the redraw moves up over the last draw")
	requireLines(t, second, "3/6 ready", "✓ Lake/events", "create", "Ready", "4s")

	t.Run("narrow terminal", func(t *testing.T) {
		t.Parallel()
		var narrow bytes.Buffer
		nv := newAppView(&narrow, true, clk.now)
		nv.width = func() int { return 40 }
		require.NoError(t, nv.deploy(shopApp(), rev, plan, nil, appParts(a), nil))
		for _, l := range strings.Split(strings.TrimRight(narrow.String(), "\n"), "\n") {
			require.LessOrEqual(t, lipgloss.Width(l), 40, "%q", l)
		}
	})
}

// scenario: app-view-lines — off a terminal a deploy prints ADR-0217's lines, a settled part's line ending with its
// time, and no ANSI escape.
func TestScenarioAppViewLines(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	v := newAppView(&out, false, clk.now)
	w := &deployWatch{children: map[string]string{}, requires: map[v1.ObjectName]string{}, times: v.clock}
	a := shopApp()
	rev := shopRevision(a, clk.t)
	cli := &cli{out: &out}
	require.NoError(t, cli.report(w, a, rev))
	clk.add(14 * time.Second)
	a.Status.Children[1] = child(v1.KindFunction, "api", v1.AppChildReady, "")
	require.NoError(t, cli.report(w, a, rev))
	got := out.String()
	requireLines(t, got, "shop-7\n", "KVStore/cart Ready after 0s\n", "Function/api Pending LakeNotReady\n",
		"Function/api Ready after 14s\n")
	require.NotContains(t, got, "\x1b")
}

// scenario: app-view-waits-on — a Pending Function names the Pending Lake it binds, and a Pending Sensor the Pending
// EventSource it listens to.
func TestScenarioAppViewWaitsOn(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	a := shopApp()
	require.NoError(t, newAppView(&out, true, clk.now).deploy(a, shopRevision(a, clk.t), nil, nil, appParts(a), nil))
	got := out.String()
	requireLines(t, got, "◐ Function/api", "Lake/events")
	requireLines(t, got, "◐ Sensor/watch", "EventSource/orders")
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "Route/api") {
			require.NotContains(t, l, "Function/api", "a Ready part waits on nothing")
		}
	}
}

// scenario: app-view-requires — while a required App is Deploying and another does not exist, a row per requirement
// shows its version, its range and why it is unmet, the header says no deadline runs, and no part row is drawn.
func TestScenarioAppViewRequires(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	a := shopApp()
	a.Spec.Requires = []v1.AppRequirement{{App: "payments", Version: "^2.0"}, {App: "billing", Version: "^1.0"}}
	a.Status.Children = nil
	payments := &v1.App{ObjectMeta: v1.ObjectMeta{Name: "payments"}, Spec: v1.AppSpec{Version: "2.1.0"}}
	payments.Status.Phase = v1.PhaseDeploying
	required := map[v1.ObjectName]*v1.App{"payments": payments, "billing": nil}
	clk.add(time.Minute)
	require.NoError(t, newAppView(&out, true, clk.now).deploy(a, nil, nil, required, appParts(a), nil))
	requireLines(t, out.String(),
		"waiting for 2 of 2 required Apps · no deadline yet",
		"REQUIRES", "◐ App/payments", "2.1.0", "^2.0", "is not Ready (Deploying)",
		"✗ App/billing", "–", "^1.0", "does not exist",
		"PARTS", "⋯ 6 parts wait for the required Apps; none is written and no hook runs",
	)
}

// scenario: app-view-hooks — a pre-hook whose call is recorded is done with its Invocation's time, a post-hook with
// none is due, and a retried hook shows its last call.
func TestScenarioAppViewHooks(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	a := shopApp()
	a.Spec.Hooks = &v1.AppHooks{PreApply: []v1.AppHook{{Function: "migrate"}, {Function: "seed"}},
		PostApply: []v1.AppHook{{Function: "notify"}}}
	rev := shopRevision(a, clk.t)
	rev.Status.Hooks = []v1.AppHookCall{
		{Point: "preApply", Function: "migrate", Invocation: "inv-1", Phase: v1.PhaseReady},
		{Point: "preApply", Function: "seed", Invocation: "inv-2", Phase: v1.PhaseFailed},
		{Point: "preApply", Function: "seed", Invocation: "inv-3", Phase: v1.PhaseReady},
	}
	inv := func(d time.Duration) *v1.Invocation {
		i := &v1.Invocation{}
		i.Status.StartTime, i.Status.EndTime = v1.NewTimestamp(clk.t), v1.NewTimestamp(clk.t.Add(d))
		return i
	}
	calls := map[v1.ObjectName]*v1.Invocation{"inv-1": inv(6 * time.Second), "inv-3": inv(2 * time.Second)}
	require.NoError(t, newAppView(&out, true, clk.now).deploy(a, rev, nil, nil, appParts(a), calls))
	requireLines(t, out.String(), "HOOKS",
		"✓ preApply Function/migrate", "done", "6s",
		"✓ preApply Function/seed", "done", "2s",
		"◐ postApply Function/notify", "due",
	)
}

// scenario: app-view-delete — a delete shows each object deleted with its time, a store that a Function still present
// binds waiting with InUse, and a retained store kept.
func TestScenarioAppViewDelete(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	v := newAppView(&out, true, clk.now)
	destroyer := &v1.Function{ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "net-destroy"},
		Spec: v1.FunctionSpec{Bindings: v1.FunctionBindings{KV: []v1.FunctionKV{{Store: "state"}}}}}
	store := &v1.KVStore{ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "state"}}
	noted := []v1.ObjectRef{{Kind: v1.KindFunction, Name: "api"}, {Kind: v1.KindFunction, Name: "net-destroy"},
		{Kind: v1.KindKVStore, Name: "state"}}
	present := map[v1.ObjectRef]v1.Object{noted[1]: destroyer, noted[2]: store}
	clk.add(3 * time.Second)
	require.NoError(t, v.remove("shop", noted, present, nil))
	requireLines(t, out.String(), "● funcd app delete", "shop · 1 of 3 gone · 3s",
		"✓ Function/api", "deleted", "3s",
		"◐ Function/net-destroy", "waiting",
		"◐ KVStore/state", "waiting", "InUse: Function/net-destroy",
	)
	clk.add(2 * time.Second)
	require.NoError(t, v.remove("shop", noted, nil, []v1.ObjectRef{{Kind: v1.KindBucket, Name: "assets"}}))
	requireLines(t, out.String(), "shop · 3 of 3 gone · 5s", "■ Bucket/assets", "kept")
}

// A failed dry run, or a plan that holds a stop in any of the Planner's shapes, draws no plan line and no action
// (ADR-0362 Decision 2, ADR-0220 Decision 5).
func TestAppViewPlanFallback(t *testing.T) {
	t.Parallel()
	require.Nil(t, actions(nil))
	for _, stop := range []v1.PlanPart{
		{Kind: v1.KindKVStore, Name: "cart", Action: v1.PlanUpdate, Reason: "ChildNotOwned"},
		{Kind: v1.KindAppRevision, Name: "shop-7", Action: v1.PlanCreate, Reason: "ChildNotOwned"},
		{Kind: v1.KindSecret, Name: "keys", Reason: "SecretNotFound"},
	} {
		require.Nil(t, actions(&v1.AppPlan{Parts: []v1.PlanPart{{Kind: v1.KindFunction, Name: "api", Action: v1.PlanUpdate}, stop}}), "%+v", stop)
	}
	require.Equal(t, map[v1.ObjectRef]v1.PlanAction{{Kind: v1.KindFunction, Name: "api"}: v1.PlanUpdate},
		actions(&v1.AppPlan{Parts: []v1.PlanPart{{Kind: v1.KindFunction, Name: "api", Action: v1.PlanUpdate}}}))
	clk := newViewClock()
	var out bytes.Buffer
	a := shopApp()
	require.NoError(t, newAppView(&out, true, clk.now).deploy(a, shopRevision(a, clk.t), actions(nil), nil, appParts(a), nil))
	require.NotContains(t, out.String(), "plan ")
}

// The plan line counts the plan's hooks, not the spec's.
func TestAppViewPlanCountsThePlansHooks(t *testing.T) {
	t.Parallel()
	plan := &v1.AppPlan{Parts: []v1.PlanPart{{Kind: v1.KindLake, Name: "events", Action: v1.PlanCreate}}, Hooks: []string{"migrate"}}
	require.Equal(t, "1 create · 0 update · 0 prune · 1 hooks", planCounts(actions(plan), plan))
}

// Server text reaches the box escaped, as the lines escape it: a child's reason, a required App's version and the
// App's own version (ADR-0362 Decision 2).
func TestAppViewEscapesServerText(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	a := shopApp()
	a.Spec.Version = "1.4.0\x1b[31m"
	a.Status.Children[1].Reason = "Bad\x1b[2J"
	a.Spec.Requires = []v1.AppRequirement{{App: "payments", Version: "^2.0"}}
	payments := &v1.App{ObjectMeta: v1.ObjectMeta{Name: "payments"}, Spec: v1.AppSpec{Version: "2.1.0\x1b[1m"}}
	require.NoError(t, newAppView(&out, true, clk.now).deploy(a, shopRevision(a, clk.t), nil,
		map[v1.ObjectName]*v1.App{"payments": payments}, appParts(a), nil))
	require.NotContains(t, out.String(), "\x1b", "no escape byte reaches the terminal")
	require.Contains(t, out.String(), `Bad\x1b[2J`)
}

// A started revision draws its parts even when a required App turns unmet later, and an upgrade that waits draws the
// waiting line, not the previous revision's parts (ADR-0219 Decision 3).
func TestAppViewRequiresWaitOnlyBeforeTheStart(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	a := shopApp()
	a.Spec.Requires = []v1.AppRequirement{{App: "billing", Version: "^1.0"}}
	required := map[v1.ObjectName]*v1.App{"billing": nil}
	var started bytes.Buffer
	require.NoError(t, newAppView(&started, true, clk.now).deploy(a, shopRevision(a, clk.t), nil, required, appParts(a), nil))
	require.NotContains(t, started.String(), "waiting for")
	requireLines(t, started.String(), "✗ App/billing", "does not exist", "PARTS", "✓ KVStore/cart")

	rev := shopRevision(a, clk.t)
	rev.Status.StartedAt = nil
	var waiting bytes.Buffer
	require.NoError(t, newAppView(&waiting, true, clk.now).deploy(a, rev, nil, required, appParts(a), nil))
	requireLines(t, waiting.String(), "waiting for 1 of 1 required Apps", "PARTS", "⋯ 6 parts wait for the required Apps")
	require.NotContains(t, waiting.String(), "KVStore/cart  ", "no part row of the previous revision")
}

// A Lagging part counts apart from the ready ones and keeps its running time; past the deadline the progress line
// names the App's Ready reason.
func TestAppViewLaggingAndPastTheDeadline(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	a := shopApp()
	a.Status.Children = append(a.Status.Children, child(v1.KindAddonObject, "net", v1.AppChildLagging, "AwaitingApproval"))
	a.Status.Conditions = readyIs(v1.ConditionFalse, "ChildNotReady")
	rev := shopRevision(a, clk.t)
	v := newAppView(&out, true, clk.now)
	require.NoError(t, v.deploy(a, rev, nil, nil, appParts(a), nil))
	clk.add(6 * time.Minute)
	require.NoError(t, v.deploy(a, rev, nil, nil, appParts(a), nil))
	got := out.String()
	requireLines(t, got, "2/7 ready · 1 lagging", "◐ AddonObject/net", "Lagging", "AwaitingApproval", "6m0s…")
	requireLines(t, got, "· ChildNotReady")
}

// scenario: app-view-lagging — a deploy that exits while parts lag counts them apart on the progress line, and its
// last draw, or a line in a pipe, names each with the funcdctl get that shows it: an add-on kind at its plural, a
// kind of funcd's in lower case. No draw before the last names them.
func TestScenarioAppViewLagging(t *testing.T) {
	t.Parallel()
	lagging := func() *v1.App {
		a := &v1.App{ObjectMeta: v1.ObjectMeta{Namespace: "edge", Name: "tunnel"}, Spec: v1.AppSpec{Version: "0.1.0"}}
		a.Status.CurrentRevision = "shop-7"
		a.Status.Children = []v1.AppChild{
			child(v1.KindBucket, "state", v1.AppChildReady, ""),
			child("InfraStack", "stack", v1.AppChildLagging, "Progressing"),
			child(v1.KindFunction, "hello", v1.AppChildNotStarted, ""),
			child(v1.KindFunction, "reader", v1.AppChildLagging, "SecretKeyMissing: Secret/outputs FRP_TOKEN"),
		}
		a.Status.Conditions = readyIs(v1.ConditionUnknown, "NotStarted")
		return a
	}
	clk := newViewClock()

	var live bytes.Buffer
	v := newAppView(&live, true, clk.now)
	a := lagging()
	require.NoError(t, v.deploy(a, shopRevision(a, clk.t), nil, nil, appParts(a), nil))
	polled := live.String()
	requireLines(t, polled, "2/4 ready · 2 lagging", "◐ InfraStack/stack", "Lagging Progressing", "App/tunnel Ready=Unknown NotStarted")
	require.NotContains(t, polled, "goes on", "a poll's draw names no part that goes on")
	require.NoError(t, v.done())
	requireLines(t, live.String()[len(polled):], "2/4 ready · 2 lagging", "App/tunnel Ready=Unknown NotStarted",
		"◐ InfraStack/stack goes on after this command", "funcdctl get infrastacks stack -n edge",
		"◐ Function/reader goes on after this command", "funcdctl get function reader -n edge")

	var pipe bytes.Buffer
	pv := newAppView(&pipe, false, clk.now)
	w := &deployWatch{children: map[string]string{}, requires: map[v1.ObjectName]string{}, times: pv.clock}
	require.NoError(t, (&cli{out: &pipe}).show(t.Context(), nil, w, pv, lagging(), nil))
	require.NoError(t, pv.done())
	requireLines(t, pipe.String(), "App/tunnel Ready=Unknown NotStarted\n",
		"InfraStack/stack goes on after this command: funcdctl get infrastacks stack -n edge\n",
		"Function/reader goes on after this command: funcdctl get function reader -n edge\n")
	require.NotContains(t, pipe.String(), "\x1b")
}

// A hook whose last call failed shows ✗ failed.
func TestAppViewFailedHook(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	a := shopApp()
	a.Spec.Hooks = &v1.AppHooks{PreApply: []v1.AppHook{{Function: "migrate"}}}
	rev := shopRevision(a, clk.t)
	rev.Status.Hooks = []v1.AppHookCall{{Point: "preApply", Function: "migrate", Invocation: "inv-1", Phase: v1.PhaseFailed}}
	require.NoError(t, newAppView(&out, true, clk.now).deploy(a, rev, nil, nil, appParts(a), nil))
	requireLines(t, out.String(), "✗ preApply Function/migrate", "failed")
}

// An object that stays shows its own Ready reason; InUse names only what the GC's protections read, so a Route that
// names a Function holds nothing.
func TestAppViewDeleteReasons(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	stack := &v1.AddonObject{ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "net"}}
	stack.Status.Conditions = readyIs(v1.ConditionFalse, "Deleting")
	fn := &v1.Function{ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "api"}}
	route := &v1.Route{ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "web"},
		Spec: v1.RouteSpec{Rules: []v1.RouteRule{{Backend: v1.RouteBackend{Function: "api"}}}}}
	noted := []v1.ObjectRef{{Kind: "InfraStack", Name: "net"}, {Kind: v1.KindFunction, Name: "api"}, {Kind: v1.KindRoute, Name: "web"}}
	present := map[v1.ObjectRef]v1.Object{noted[0]: stack, noted[1]: fn, noted[2]: route}
	require.NoError(t, newAppView(&out, true, clk.now).remove("shop", noted, present, nil))
	got := out.String()
	requireLines(t, got, "◐ InfraStack/net", "waiting", "Deleting")
	require.NotContains(t, got, "InUse: Route/web", "a Route does not hold a Function from the GC")
}

// A box taller than the terminal folds its settled rows into one, so a redraw stays on the screen.
func TestAppViewFoldsToTheTerminal(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	v := newAppView(&out, true, clk.now)
	v.height = func() int { return 12 }
	a := shopApp()
	require.NoError(t, v.deploy(a, shopRevision(a, clk.t), nil, nil, appParts(a), nil))
	requireLines(t, out.String(), "◐ Function/api", "✓ 2 more ready")
	require.NotContains(t, out.String(), "✓ KVStore/cart")
}

// Only a terminal other than TERM=dumb draws live (ADR-0362 Decision 1).
func TestAppViewLiveFile(t *testing.T) {
	t.Parallel()
	require.True(t, liveFile("xterm-256color", true))
	require.False(t, liveFile("dumb", true))
	require.False(t, liveFile("xterm-256color", false))
	require.False(t, liveTerminal(&bytes.Buffer{}))
}

// Off a terminal, a requirement's change prints a line of its own (ADR-0362 Decision 6).
func TestAppViewRequirementLines(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	v := newAppView(&out, false, clk.now)
	a := shopApp()
	a.Spec.Requires = []v1.AppRequirement{{App: "payments", Version: "^2.0"}}
	payments := &v1.App{ObjectMeta: v1.ObjectMeta{Name: "payments"}, Spec: v1.AppSpec{Version: "2.1.0"}}
	payments.Status.Phase = v1.PhaseDeploying
	w := &deployWatch{children: map[string]string{}, requires: map[v1.ObjectName]string{}, times: v.clock,
		required: map[v1.ObjectName]*v1.App{"payments": payments}}
	cli := &cli{out: &out}
	require.NoError(t, cli.report(w, a, nil))
	payments.Status.Phase, payments.Status.CurrentRevision, payments.Status.LatestRevision, payments.Status.Version = v1.PhaseReady, "p-1", "p-1", "2.1.0"
	require.NoError(t, cli.report(w, a, nil))
	require.NoError(t, cli.report(w, a, nil))
	require.Equal(t, "App/shop requires App/payments: is not Ready (Deploying)\nApp/shop requires App/payments: met\n", out.String())
}

// A folded box keeps its Lagging rows, which still wait, and the last draw, at done, shows every row again.
func TestAppViewFoldKeepsLaggingAndUnfoldsAtTheEnd(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	v := newAppView(&out, true, clk.now)
	v.height = func() int { return 12 }
	a := shopApp()
	a.Status.Children = append(a.Status.Children, child(v1.KindAddonObject, "net", v1.AppChildLagging, "AwaitingApproval"))
	require.NoError(t, v.deploy(a, shopRevision(a, clk.t), nil, nil, appParts(a), nil))
	folded := out.String()
	requireLines(t, folded, "◐ AddonObject/net", "✓ 2 more ready")
	require.NoError(t, v.done())
	last := out.String()[len(folded):]
	requireLines(t, last, "✓ KVStore/cart", "◐ AddonObject/net")
	require.NotContains(t, last, "more ready")
}

// A delete taller than the terminal folds its deleted rows, and its reasons and the App's past-deadline reason reach
// the terminal escaped.
func TestAppViewDeleteFoldAndEscapes(t *testing.T) {
	t.Parallel()
	clk := newViewClock()
	var out bytes.Buffer
	v := newAppView(&out, true, clk.now)
	v.height = func() int { return 6 }
	stack := &v1.AddonObject{ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "net"}}
	stack.Status.Conditions = readyIs(v1.ConditionFalse, "Deleting\x1b[2J")
	noted := []v1.ObjectRef{{Kind: v1.KindFunction, Name: "a"}, {Kind: v1.KindFunction, Name: "b"}, {Kind: "InfraStack", Name: "net"}}
	require.NoError(t, v.remove("shop", noted, map[v1.ObjectRef]v1.Object{noted[2]: stack}, nil))
	got := out.String()
	requireLines(t, got, "✓ 2 more deleted")
	require.Contains(t, got, `Deleting\x1b[2J`)
	require.NotContains(t, got, "\x1b[2J", "the reason is escaped")

	var past bytes.Buffer
	a := shopApp()
	a.Status.Conditions = readyIs(v1.ConditionFalse, "ChildNotReady\x1b[1m")
	rev := shopRevision(a, clk.t.Add(-10*time.Minute))
	require.NoError(t, newAppView(&past, true, clk.now).deploy(a, rev, nil, nil, appParts(a), nil))
	require.Contains(t, past.String(), `ChildNotReady\x1b[1m`)
	require.NotContains(t, past.String(), "\x1b[1m")
}
