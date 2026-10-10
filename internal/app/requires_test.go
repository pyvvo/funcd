package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// The ADR-0219 fixture: lakehouse declares the Bucket lake (Ready once it exists), and billing 1.3.0 requires lakehouse
// ^2.0.0, declares the Function invoice and uses lake through a ref; app.upgradeTimeout is 2m.
const requiresTimeout = 2 * time.Minute

func lakehouseApp(version string) *v1.App {
	a := bareApp("lakehouse", version)
	a.Spec.Buckets = []v1.AppBucket{{Name: "lake"}}
	return a
}

func billingApp(mutate func(*v1.App)) *v1.App {
	a := bareApp("billing", "1.3.0", needs("lakehouse", "^2.0.0"))
	a.Spec.Functions = []v1.AppFunction{{Name: "invoice", FunctionSpec: v1.FunctionSpec{
		Runtime: "nodejs22", Handler: "index.handler", Image: "oci-layout://invoice:1",
	}}}
	a.Spec.Buckets = []v1.AppBucket{{Ref: "lake"}}
	if mutate != nil {
		mutate(a)
	}
	return a
}

func newRequiresHarness(t *testing.T, st store.Store, opts ...func(*app.Deps)) *harness {
	t.Helper()
	timeout := func(d *app.Deps) { d.UpgradeTimeout = requiresTimeout }
	return newHarness(t, st, append([]func(*app.Deps){timeout}, opts...)...)
}

func (h *harness) passErr(name v1.ObjectName) (controller.Result, error) {
	return h.r.Reconcile(h.ctx, controller.Request{GVK: v1.KindApp.GVK(), Namespace: ns, Name: name})
}

func (h *harness) pass(name v1.ObjectName) controller.Result {
	h.t.Helper()
	res, err := h.passErr(name)
	require.NoError(h.t, err)
	return res
}

func (h *harness) named(name v1.ObjectName) *v1.App { return h.get(v1.KindApp, name).(*v1.App) }

func (h *harness) revisionOf(name v1.ObjectName, n int64) *v1.AppRevision {
	h.t.Helper()
	obj := h.get(v1.KindAppRevision, v1.AppRevisionName(name, n))
	require.NotNil(h.t, obj, "AppRevision %s-%d", name, n)
	return obj.(*v1.AppRevision)
}

func readyOf(t *testing.T, a *v1.App) v1.Condition {
	t.Helper()
	c, ok := a.Status.Conditions.Get("Ready")
	require.True(t, ok, "App/%s has no Ready condition", a.Name)
	return c
}

// serve creates lakehouse at version and brings it Ready: a first pass writes Bucket lake, the Bucket reconciler marks
// it Ready (ADR-0215), and the second pass switches.
func (h *harness) serve(version string) {
	h.t.Helper()
	h.create(lakehouseApp(version))
	h.pass("lakehouse")
	h.markReady(v1.KindBucket, "lake")
	h.pass("lakehouse")
	require.Equal(h.t, v1.PhaseReady, h.named("lakehouse").Status.Phase)
}

// upgradeLakehouse sets lakehouse's spec.version, as an apply past app-requires does, and reconciles it to current.
func (h *harness) upgradeLakehouse(version string) {
	h.t.Helper()
	a := h.named("lakehouse")
	a.Spec.Version = version
	h.update(a)
	h.pass("lakehouse")
	require.Equal(h.t, version, h.named("lakehouse").Status.Version)
}

// requireWaiting checks a waiting billing: billing-n Deploying without startedAt, Applied=False RequirementNotMet, the
// App Deploying with Ready=False RequirementNotMet, both with msg verbatim, and Function invoice not written.
func (h *harness) requireWaiting(n int64, msg string) {
	h.t.Helper()
	rev := h.revisionOf("billing", n)
	require.Equal(h.t, v1.PhaseDeploying, rev.Status.Phase)
	require.Nil(h.t, rev.Status.StartedAt, "a waiting revision has no startedAt")
	requireCond(h.t, revCond(h.t, rev, "Applied"), v1.ConditionFalse, "RequirementNotMet", msg)
	b := h.named("billing")
	require.Equal(h.t, v1.PhaseDeploying, b.Status.Phase)
	requireCond(h.t, readyOf(h.t, b), v1.ConditionFalse, "RequirementNotMet", msg)
	require.Nil(h.t, h.get(v1.KindFunction, "invoice"), "a waiting pass writes no part")
}

func (h *harness) quietPass(name v1.ObjectName) {
	h.t.Helper()
	before := h.versionsAll()
	h.pass(name)
	require.Equal(h.t, before, h.versionsAll(), "a repeated pass writes nothing")
}

// scenario: app-requires-waits (the reconciler half) — billing waits for lakehouse without a limit, writing no part
// and never failing; once lakehouse is Ready at 2.1.0, billing-1 gets startedAt, invoice is written and billing-1
// becomes current; both Apps show the requirement.
func TestScenarioAppRequiresWaits(t *testing.T) {
	h := newRequiresHarness(t, nil)
	h.create(billingApp(nil))
	const msg = "App/lakehouse does not exist; billing needs ^2.0.0"
	require.Equal(t, controller.SupervisionPeriod, h.pass("billing").RequeueAfter)
	h.requireWaiting(1, msg)
	require.Equal(t, []v1.AppRequirementState{{App: "lakehouse"}}, h.named("billing").Status.Requires)
	h.clk.Advance(3 * time.Minute)
	require.Equal(t, controller.SupervisionPeriod, h.pass("billing").RequeueAfter, "a waiting revision has no deadline")
	h.requireWaiting(1, msg)
	h.quietPass("billing")

	h.serve("2.1.0")
	require.Equal(t, []v1.ObjectName{"billing"}, h.named("lakehouse").Status.RequiredBy)
	met := h.clk.Now()
	require.Equal(t, requiresTimeout, h.pass("billing").RequeueAfter, "the deadline runs from startedAt")
	rev := h.revisionOf("billing", 1)
	require.NotNil(t, rev.Status.StartedAt)
	require.True(t, met.Equal(time.Time(*rev.Status.StartedAt)), "startedAt %v is the met pass's time %v", rev.Status.StartedAt, met)
	require.NotNil(t, h.get(v1.KindFunction, "invoice"))
	h.markReady(v1.KindFunction, "invoice")
	h.pass("billing")
	b := h.named("billing")
	require.Equal(t, v1.ObjectName("billing-1"), b.Status.CurrentRevision)
	require.Equal(t, v1.PhaseReady, b.Status.Phase)
	require.Equal(t, []v1.AppRequirementState{{App: "lakehouse", Version: "2.1.0", Met: true}}, b.Status.Requires)
	require.True(t, met.Equal(time.Time(*h.revisionOf("billing", 1).Status.StartedAt)), "startedAt is kept")
	h.quietPass("billing")
	h.quietPass("lakehouse")
}

// scenario: app-requires-version (the reconciler half) — lakehouse Ready at 1.9.0 holds billing; once lakehouse 2.1.0
// is current and Ready, billing rolls out.
func TestScenarioAppRequiresVersion(t *testing.T) {
	h := newRequiresHarness(t, nil)
	h.serve("1.9.0")
	h.create(billingApp(nil))
	h.pass("billing")
	h.requireWaiting(1, "App/lakehouse is 1.9.0; billing needs ^2.0.0")
	require.Equal(t, []v1.AppRequirementState{{App: "lakehouse", Version: "1.9.0"}}, h.named("billing").Status.Requires)

	h.upgradeLakehouse("2.1.0")
	h.pass("billing")
	require.NotNil(t, h.revisionOf("billing", 1).Status.StartedAt)
	h.markReady(v1.KindFunction, "invoice")
	h.pass("billing")
	require.Equal(t, v1.ObjectName("billing-1"), h.named("billing").Status.CurrentRevision)
}

// scenario: app-requires-any-version (the reconciler half) — a requirement without a range is met by a Ready App
// with no spec.version.
func TestScenarioAppRequiresAnyVersion(t *testing.T) {
	h := newRequiresHarness(t, nil)
	h.create(billingApp(func(a *v1.App) { a.Spec.Requires = []v1.AppRequirement{needs("lakehouse", "")} }))
	h.pass("billing")
	h.requireWaiting(1, "App/lakehouse does not exist; billing needs any version")
	h.serve("")
	h.pass("billing")
	h.markReady(v1.KindFunction, "invoice")
	h.pass("billing")
	b := h.named("billing")
	require.Equal(t, v1.ObjectName("billing-1"), b.Status.CurrentRevision)
	require.Equal(t, []v1.AppRequirementState{{App: "lakehouse", Met: true}}, b.Status.Requires)
}

// putLakehouse stores lakehouse at version with status, as its reconciler would have left it.
func (h *harness) putLakehouse(version string, status v1.AppStatus) {
	h.t.Helper()
	a := h.create(lakehouseApp(version)).(*v1.App)
	a.Status = status
	h.update(a)
}

func lakeStatus(phase v1.Phase, current, latest v1.ObjectName, version string) v1.AppStatus {
	return v1.AppStatus{Status: v1.Status{Phase: phase}, CurrentRevision: current, LatestRevision: latest, Version: version}
}

// Decision 2: the first unmet requirement's message, verbatim, for each way a requirement is not met, checked in order.
func TestAppRequirementMessages(t *testing.T) {
	ready := lakeStatus(v1.PhaseReady, "lakehouse-1", "lakehouse-1", "2.1.0")
	for _, tc := range []struct {
		name    string
		rng     string
		version string
		status  *v1.AppStatus
		msg     string
	}{
		{"missing", "^2.0.0", "", nil, "App/lakehouse does not exist; billing needs ^2.0.0"},
		{"missing, any version", "", "", nil, "App/lakehouse does not exist; billing needs any version"},
		{"no version", "^2.0.0", "", &ready, "App/lakehouse has no SemVer version; billing needs ^2.0.0"},
		{"a free label", "^2.0.0", "beta", &ready, "App/lakehouse has no SemVer version; billing needs ^2.0.0"},
		{"a lenient version", "^2.0.0", "v2.1.0", &ready, "App/lakehouse has no SemVer version; billing needs ^2.0.0"},
		{"outside the range", "^2.0.0", "3.0.0", &ready, "App/lakehouse is 3.0.0; billing needs ^2.0.0"},
		{"not Ready", "^2.0.0", "2.1.0", new(lakeStatus(v1.PhaseDegraded, "lakehouse-1", "lakehouse-1", "2.1.0")),
			"App/lakehouse is not Ready (Degraded); billing needs ^2.0.0"},
		{"no phase", "^2.0.0", "2.1.0", &v1.AppStatus{}, "App/lakehouse is not Ready (none); billing needs ^2.0.0"},
		{"Ready at an older status.version", "^2.0.0", "2.1.0", new(lakeStatus(v1.PhaseReady, "lakehouse-1", "lakehouse-1", "1.9.0")),
			"App/lakehouse has not rolled out its spec yet; billing needs ^2.0.0"},
		{"Ready while a newer revision rolls out", "^2.0.0", "2.1.0", new(lakeStatus(v1.PhaseReady, "lakehouse-1", "lakehouse-2", "2.1.0")),
			"App/lakehouse has not rolled out its spec yet; billing needs ^2.0.0"},
		{"not Ready, any version", "", "", new(lakeStatus(v1.PhaseFailed, "", "lakehouse-1", "")),
			"App/lakehouse is not Ready (Failed); billing needs any version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newRequiresHarness(t, nil)
			if tc.status != nil {
				h.putLakehouse(tc.version, *tc.status)
			}
			h.create(billingApp(func(a *v1.App) { a.Spec.Requires = []v1.AppRequirement{needs("lakehouse", tc.rng)} }))
			h.pass("billing")
			h.requireWaiting(1, tc.msg)
			want := v1.AppRequirementState{App: "lakehouse"}
			if tc.status != nil {
				want.Version = tc.version
			}
			require.Equal(t, []v1.AppRequirementState{want}, h.named("billing").Status.Requires)
		})
	}
}

// Decisions 2 and 5: status.requires has one line per entry in order, and the message is the first unmet entry's.
func TestAppRequirementsInOrder(t *testing.T) {
	h := newRequiresHarness(t, nil)
	h.serve("2.1.0")
	h.create(billingApp(func(a *v1.App) {
		a.Spec.Requires = []v1.AppRequirement{needs("lakehouse", "^2.0.0"), needs("ledger", ">=1.0.0"), needs("audit", "")}
	}))
	h.pass("billing")
	h.requireWaiting(1, "App/ledger does not exist; billing needs >=1.0.0")
	require.Equal(t, []v1.AppRequirementState{{App: "lakehouse", Version: "2.1.0", Met: true}, {App: "ledger"}, {App: "audit"}},
		h.named("billing").Status.Requires)
}

// Decision 5: status.requiredBy lists every App that requires this one, sorted by name, whether or not met.
func TestAppRequiredBySorted(t *testing.T) {
	h := newRequiresHarness(t, nil)
	for _, name := range []v1.ObjectName{"zeta", "alpha", "mid"} {
		h.create(bareApp(name, "1.0.0", needs("lakehouse", "^9.0.0")))
	}
	h.create(bareApp("other", "1.0.0", needs("ledger", "")))
	h.serve("2.1.0")
	require.Equal(t, []v1.ObjectName{"alpha", "mid", "zeta"}, h.named("lakehouse").Status.RequiredBy)
	require.Nil(t, h.named("lakehouse").Status.Requires)
}

// Decisions 3 and 4: an App without requires gets startedAt at the stamp, its spec and status marshal as before, and
// the deadline runs from the stamp.
func TestAppWithoutRequiresStartsAtTheStamp(t *testing.T) {
	h := newHarness(t, nil)
	stamped := h.clk.Now()
	h.create(todoApp(nil))
	require.Equal(t, 5*time.Minute, h.reconcile().RequeueAfter)
	rev := h.rev(1)
	require.NotNil(t, rev.Status.StartedAt)
	require.True(t, stamped.Equal(time.Time(*rev.Status.StartedAt)))
	spec, err := json.Marshal(h.app().Spec)
	require.NoError(t, err)
	require.NotContains(t, string(spec), "requires")
	st, err := json.Marshal(h.app().Status)
	require.NoError(t, err)
	require.NotContains(t, string(st), "requires")
	require.NotContains(t, string(st), "requiredBy")
}

// spy records each Create and Update that succeeded, an AppRevision update carrying startedAt marked so, and counts
// the Lists of Apps.
type spy struct {
	store.Store
	mu       sync.Mutex
	writes   []string
	appLists int
}

func (s *spy) note(verb string, obj v1.Object) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := fmt.Sprintf("%s %s/%s", verb, obj.GroupVersionKind().Kind, obj.GetObjectMeta().Name)
	if r, ok := obj.(*v1.AppRevision); ok && r.Status.StartedAt != nil {
		w += " started"
	}
	s.writes = append(s.writes, w)
}

func (s *spy) Create(ctx context.Context, obj v1.Object) (v1.Object, error) {
	out, err := s.Store.Create(ctx, obj)
	if err == nil {
		s.note("create", out)
	}
	return out, err
}

func (s *spy) Update(ctx context.Context, obj v1.Object) (v1.Object, error) {
	out, err := s.Store.Update(ctx, obj)
	if err == nil {
		s.note("update", out)
	}
	return out, err
}

func (s *spy) List(ctx context.Context, gvk v1.GroupVersionKind, opts store.ListOptions) (store.List, error) {
	if gvk.Kind == v1.KindApp {
		s.mu.Lock()
		s.appLists++
		s.mu.Unlock()
	}
	return s.Store.List(ctx, gvk, opts)
}

func (s *spy) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes, s.appLists = nil, 0
}

// Decision 3: the met pass stores startedAt with an AppRevision status update before any part write; a new
// Reconciler keeps it, and the deadline counts from it, not from the stamp.
func TestAppRequiresStartedAtBeforeAnyPartWrite(t *testing.T) {
	st := &spy{Store: store.New(memory.New())}
	h := newRequiresHarness(t, st)
	h.create(billingApp(nil))
	h.pass("billing")
	h.clk.Advance(10 * time.Minute)
	h.serve("2.1.0")
	st.reset()
	h.pass("billing")
	started := slices.Index(st.writes, "update AppRevision/billing-1 started")
	written := slices.Index(st.writes, "create Function/invoice")
	require.GreaterOrEqual(t, started, 0, "%v", st.writes)
	require.Greater(t, written, started, "startedAt is stored before any part write: %v", st.writes)

	h.clk.Advance(time.Minute)
	h.restart()
	require.Equal(t, requiresTimeout-time.Minute, h.pass("billing").RequeueAfter, "a new Reconciler reads the stored startedAt")
	h.clk.Advance(time.Minute - time.Millisecond)
	require.Equal(t, time.Millisecond, h.pass("billing").RequeueAfter)
	h.clk.Advance(time.Millisecond)
	h.pass("billing")
	require.Equal(t, v1.PhaseFailed, h.revisionOf("billing", 1).Status.Phase, "invoice never got Ready within the timeout")
}

// Decision 3: when the startedAt update meets a Conflict or fails, the pass writes no part and requeues; the next pass
// stores startedAt.
func TestAppRequiresStartedAtUpdateFails(t *testing.T) {
	for name, err := range map[string]error{
		"a Conflict": fault.Conflictf("store.Update", "AppRevision %q resourceVersion mismatch", "billing-1"),
		"a failure":  errInjected,
	} {
		t.Run(name, func(t *testing.T) {
			st := &failing{Store: store.New(memory.New())}
			h := newRequiresHarness(t, st)
			h.create(billingApp(nil))
			h.pass("billing")
			h.serve("2.1.0")
			st.arm(v1.KindAppRevision, "billing-1", err)
			res, perr := h.passErr("billing")
			if fault.KindOf(err) == fault.Conflict {
				require.NoError(t, perr)
				require.Equal(t, controller.Result{Requeue: true}, res)
			} else {
				require.Error(t, perr)
			}
			require.Nil(t, h.revisionOf("billing", 1).Status.StartedAt)
			require.Nil(t, h.get(v1.KindFunction, "invoice"), "no part is written")

			h.pass("billing")
			require.NotNil(t, h.revisionOf("billing", 1).Status.StartedAt)
			require.NotNil(t, h.get(v1.KindFunction, "invoice"))
		})
	}
}

// Decision 4: a hold's ReleasedAt alone starts no deadline for a waiting revision; once met, the deadline runs from
// startedAt.
func TestAppRequiresHoldReleaseStartsNoDeadline(t *testing.T) {
	hold := &fakeHold{}
	h := newRequiresHarness(t, nil, func(d *app.Deps) { d.Hold = hold })
	h.create(billingApp(nil))
	h.pass("billing")
	h.clk.Advance(time.Minute)
	hold.released = h.clk.Now()
	h.clk.Advance(5 * time.Minute)
	require.Equal(t, controller.SupervisionPeriod, h.pass("billing").RequeueAfter)
	h.requireWaiting(1, "App/lakehouse does not exist; billing needs ^2.0.0")
	require.Equal(t, v1.PhaseDeploying, h.revisionOf("billing", 1).Status.Phase)

	h.serve("2.1.0")
	require.Equal(t, requiresTimeout, h.pass("billing").RequeueAfter)
}

// Decision 3: a waiting revision that a newer stamp supersedes turns Failed Superseded; the newer one waits too.
func TestAppRequiresSupersededWhileWaiting(t *testing.T) {
	h := newRequiresHarness(t, nil)
	h.create(billingApp(nil))
	h.pass("billing")
	b := h.named("billing")
	b.Spec.Version = "1.3.1"
	h.update(b)
	h.pass("billing")
	one := h.revisionOf("billing", 1)
	require.Equal(t, v1.PhaseFailed, one.Status.Phase)
	requireCond(t, revCond(t, one, "Current"), v1.ConditionFalse, "Superseded", "superseded by billing-2")
	h.requireWaiting(2, "App/lakehouse does not exist; billing needs ^2.0.0")
}

// Decision 4: a requirement lost after startedAt neither stops the rollout nor changes the phase or Ready of the
// started or current revision; status.requires shows it.
func TestAppRequirementLostAfterStart(t *testing.T) {
	h := newRequiresHarness(t, nil)
	h.serve("2.1.0")
	h.create(billingApp(nil))
	h.pass("billing")
	require.NotNil(t, h.revisionOf("billing", 1).Status.StartedAt)

	h.upgradeLakehouse("3.0.0")
	h.markReady(v1.KindFunction, "invoice")
	h.pass("billing")
	b := h.named("billing")
	require.Equal(t, v1.ObjectName("billing-1"), b.Status.CurrentRevision, "the started rollout goes on")
	require.Equal(t, v1.PhaseReady, b.Status.Phase)
	require.Equal(t, []v1.AppRequirementState{{App: "lakehouse", Version: "3.0.0"}}, b.Status.Requires)

	lake := h.named("lakehouse")
	lake.Status.Phase = v1.PhaseDegraded
	h.update(lake)
	h.pass("billing")
	require.Equal(t, v1.PhaseReady, h.named("billing").Status.Phase, "a required App's phase does not reach a current dependent")
	require.Equal(t, v1.ConditionTrue, readyOf(t, h.named("billing")).Status)
}

// Decision 3: a waiting pass reaches neither the ownership check of the parts nor the Secret check; once met, it does.
func TestAppRequiresWaitBeforeOwnershipAndSecrets(t *testing.T) {
	h := newRequiresHarness(t, nil)
	h.create(&v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "invoice", Namespace: ns, ResourceGroup: "hand"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "index.handler", Image: "oci-layout://hand:1"},
	})
	h.create(billingApp(func(a *v1.App) {
		a.Spec.Secrets = []v1.AppSecret{{Name: "stripe", Keys: []string{"STRIPE_API_KEY"}}}
	}))
	h.pass("billing")
	requireCond(t, readyOf(t, h.named("billing")), v1.ConditionFalse, "RequirementNotMet",
		"App/lakehouse does not exist; billing needs ^2.0.0")

	h.serve("2.1.0")
	h.pass("billing")
	require.Equal(t, "ChildNotOwned", readyOf(t, h.named("billing")).Reason)
}

// Decision 6: MapRequires requeues, both ways, the Apps a changed or deleted App requires and those that require it,
// and the Apps whose status.requiredBy names it, so an Update that removes or renames a requirement reaches them.
func TestMapRequires(t *testing.T) {
	h := newRequiresHarness(t, nil)
	h.create(billingApp(nil))
	h.create(bareApp("audit", "1.0.0", needs("lakehouse", "")))
	h.serve("2.1.0")
	require.Equal(t, []v1.ObjectName{"audit", "billing"}, h.named("lakehouse").Status.RequiredBy)
	names := func(obj v1.Object) []v1.ObjectName {
		var out []v1.ObjectName
		for _, r := range h.r.MapRequires(h.ctx, obj) {
			require.Equal(t, v1.KindApp.GVK(), r.GVK)
			require.Equal(t, ns, r.Namespace)
			out = append(out, r.Name)
		}
		slices.Sort(out)
		return out
	}
	require.Equal(t, []v1.ObjectName{"audit", "billing"}, names(h.named("lakehouse")), "its dependents")
	require.Equal(t, []v1.ObjectName{"lakehouse"}, names(h.named("billing")), "the App it requires")
	require.Nil(t, names(h.get(v1.KindBucket, "lake")), "not an App")

	b := h.named("billing")
	b.Spec.Requires = []v1.AppRequirement{needs("warehouse", "")}
	h.update(b)
	require.Equal(t, []v1.ObjectName{"lakehouse", "warehouse"}, names(h.named("billing")), "a renamed requirement")
	b = h.named("billing")
	b.Spec.Requires = nil
	h.update(b)
	require.Equal(t, []v1.ObjectName{"lakehouse"}, names(h.named("billing")), "a removed requirement, through status.requiredBy")
	h.pass("lakehouse")
	require.Equal(t, []v1.ObjectName{"audit"}, h.named("lakehouse").Status.RequiredBy, "requiredBy drops the dependent")

	audit := h.named("audit")
	require.NoError(t, h.st.Delete(h.ctx, v1.KindApp.GVK(), ns, "audit", ""))
	require.Equal(t, []v1.ObjectName{"lakehouse"}, names(audit), "a deleted dependent")
	h.pass("lakehouse")
	require.Nil(t, h.named("lakehouse").Status.RequiredBy)
}

// Decision 5: a paused pass lists no App and keeps status.requires and status.requiredBy until the resume.
func TestAppRequiresPausedKeepsStatus(t *testing.T) {
	st := &spy{Store: store.New(memory.New())}
	h := newRequiresHarness(t, st)
	h.create(billingApp(nil))
	h.pass("billing")
	h.serve("2.1.0")
	setPaused := func(name v1.ObjectName, on bool) {
		a := h.named(name)
		a.Spec.Paused = on
		h.update(a)
	}
	setPaused("billing", true)
	setPaused("lakehouse", true)
	h.create(bareApp("audit", "1.0.0", needs("lakehouse", "")))

	st.reset()
	h.pass("billing")
	h.pass("lakehouse")
	require.Zero(t, st.appLists, "a paused pass lists no App")
	require.Equal(t, []v1.AppRequirementState{{App: "lakehouse"}}, h.named("billing").Status.Requires)
	require.Equal(t, []v1.ObjectName{"billing"}, h.named("lakehouse").Status.RequiredBy)

	setPaused("billing", false)
	setPaused("lakehouse", false)
	h.pass("billing")
	h.pass("lakehouse")
	require.Equal(t, []v1.AppRequirementState{{App: "lakehouse", Version: "2.1.0", Met: true}}, h.named("billing").Status.Requires)
	require.Equal(t, []v1.ObjectName{"audit", "billing"}, h.named("lakehouse").Status.RequiredBy)
}
