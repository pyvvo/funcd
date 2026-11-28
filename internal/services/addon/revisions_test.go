package addon_test

import (
	"context"
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
	"github.com/pyvvo/funcd/internal/testkit/fakeaddon"
)

// ADR-0339: an App's part follows its App's revisions forward only, and an unused revision is removed.

const webAuthUID v1.UID = "uid-of-web-auth"

// revRig is the Addon rauthy over a memory store, with its reconciler and a fake runtime.
type revRig struct {
	ctx context.Context
	st  store.Store
	r   *addon.Reconciler
	rt  *fakeaddon.Runtime
}

// newRevRig is the Addon rauthy with the revisions rauthy-1 to rauthy-3, rauthy-3 current, each with GEN set to its
// name but rauthy-1's; the AppRevisions holder-1 and holder-2, of another App, record rauthy-1 and rauthy-2, so the
// Addon's reconciler keeps them.
func newRevRig(t *testing.T) *revRig {
	t.Helper()
	rig := newUnstampedRig(t)
	for i, rev := range []v1.ObjectName{"rauthy-1", "rauthy-2"} {
		rig.record(t, "holder", "uid-of-holder", int64(i+1), rev, v1.ConditionTrue)
	}
	rig.stamp(t)
	for _, gen := range []string{"rauthy-2", "rauthy-3"} {
		cur := rig.addon(t)
		cur.Spec.Runtimes[0].Attributes.Env = map[string]string{"GEN": gen}
		_, err := rig.st.Update(rig.ctx, cur)
		require.NoError(t, err)
		rig.stamp(t)
	}
	return rig
}

// newUnstampedRig is the Addon rauthy at its first generation, which its reconciler has not stamped yet.
func newUnstampedRig(t *testing.T) *revRig {
	t.Helper()
	ctx := context.Background()
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	a := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddon}, ObjectMeta: v1.ObjectMeta{Name: "rauthy"},
		Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "identityprovider", Image: "ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809", Workloads: []v1.Workload{v1.WorkloadEngine},
			Attributes: &v1.AddonAttributes{Kind: "IdentityProvider", Port: 8080, Readiness: v1.AddonReadiness{Path: "/ping"}}}}}}
	rt := fakeaddon.New(t)
	r, err := addon.NewReconciler(addon.Deps{Store: st, Provider: rt, RunsImages: true})
	require.NoError(t, err)
	_, err = st.Create(ctx, a)
	require.NoError(t, err)
	return &revRig{ctx: ctx, st: st, r: r, rt: rt}
}

func (g *revRig) addon(t *testing.T) *v1.Addon {
	t.Helper()
	obj, err := g.st.Get(g.ctx, v1.KindAddon.GVK(), "", "rauthy")
	require.NoError(t, err)
	return obj.(*v1.Addon)
}

// stamp runs one pass of the Addon's reconciler.
func (g *revRig) stamp(t *testing.T) controller.Result {
	t.Helper()
	res, err := g.r.Revisions().Reconcile(g.ctx, controller.Request{GVK: v1.KindAddon.GVK(), Name: "rauthy"})
	require.NoError(t, err)
	return res
}

func (g *revRig) revisionExists(t *testing.T, name v1.ObjectName) bool {
	t.Helper()
	_, err := g.st.Get(g.ctx, v1.KindAddonRevision.GVK(), "", name)
	if fault.KindOf(err) == fault.NotFound {
		return false
	}
	require.NoError(t, err)
	return true
}

// appRevision stores web-auth-<n>, recording rev for IdentityProvider, Applied as applied says.
func (g *revRig) appRevision(t *testing.T, n int64, rev v1.ObjectName, applied v1.ConditionStatus) {
	t.Helper()
	g.record(t, "web-auth", webAuthUID, n, rev, applied)
}

// record stores the AppRevision <app>-<n> of the App app of uid, recording rev for IdentityProvider, Applied as applied
// says; an empty rev records nothing for the kind, as a stamp does while its Addon has no revision.
func (g *revRig) record(t *testing.T, app v1.ObjectName, uid v1.UID, n int64, rev v1.ObjectName, applied v1.ConditionStatus) {
	t.Helper()
	ref := v1.ObjectRef{Kind: v1.KindApp, Namespace: "default", Name: app}
	ar := &v1.AppRevision{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAppRevision},
		ObjectMeta: v1.ObjectMeta{Name: v1.AppRevisionName(app, n), Namespace: "default", ResourceGroup: "rg1",
			OwnerReferences: []v1.OwnerReference{{ObjectRef: ref, UID: uid, Controller: true}}},
		Spec: v1.AppRevisionSpec{App: ref, Number: n}}
	if rev != "" {
		ar.Spec.AddonRevisions = []v1.AppAddonRevision{{Kind: "IdentityProvider", Revision: rev}}
	}
	ar.Status.Conditions.Set(v1.Condition{Type: "Applied", Status: applied})
	obj, err := g.st.Get(g.ctx, v1.KindAppRevision.GVK(), "default", ar.Name)
	if fault.KindOf(err) == fault.NotFound {
		_, err = g.st.Create(g.ctx, ar)
		require.NoError(t, err)
		return
	}
	require.NoError(t, err)
	cur := obj.(*v1.AppRevision)
	cur.Status.Conditions.Set(v1.Condition{Type: "Applied", Status: applied})
	_, err = g.st.Update(g.ctx, cur)
	require.NoError(t, err)
}

// part stores web-auth-login, web-auth's part, once.
func (g *revRig) part(t *testing.T) {
	t.Helper()
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Kind = "web-auth-login", "default", "rg1", "IdentityProvider"
	o.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: "default", Name: "web-auth"},
		UID: webAuthUID, Controller: true}}
	_, err := g.st.Create(g.ctx, o)
	require.NoError(t, err)
}

// pass reconciles the part and returns the App revision and the AddonRevision it pins, and the engine's GEN.
func (g *revRig) pass(t *testing.T) (v1.ObjectName, v1.ObjectName, string) {
	t.Helper()
	_, err := g.r.Reconcile(g.ctx, controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "web-auth-login"})
	require.NoError(t, err)
	obj, err := g.st.Get(g.ctx, v1.KindAddonObject.GVK(), "default", "web-auth-login")
	require.NoError(t, err)
	o := obj.(*v1.AddonObject)
	var gen string
	if e := g.rt.Engine(provider.ProviderRef{Namespace: o.Namespace, Name: o.Name}); e != nil {
		_, _, env := e.State()
		gen = env["GEN"]
	}
	return o.Status.AppRevision, o.Status.AddonRevision, gen
}

// scenario: addon-revision-no-step-back — a part on web-auth-2's record stays there when a later pass of web-auth-2
// stops, its Applied False, and when an older AppRevision is pruned.
//
// oracle: docs/design/addon/revisions.fizz#NoMoveByItself
func TestScenarioAddonRevisionNoStepBack(t *testing.T) {
	t.Parallel()
	g := newRevRig(t)
	g.part(t)
	g.appRevision(t, 1, "rauthy-1", v1.ConditionTrue)
	g.appRevision(t, 2, "rauthy-2", v1.ConditionTrue)
	app, rev, gen := g.pass(t)
	require.Equal(t, []v1.ObjectName{"web-auth-2", "rauthy-2"}, []v1.ObjectName{app, rev})
	require.Equal(t, "rauthy-2", gen, "the engine runs rauthy-2's spec")

	g.appRevision(t, 2, "rauthy-2", v1.ConditionFalse)
	app, rev, _ = g.pass(t)
	require.Equal(t, []v1.ObjectName{"web-auth-2", "rauthy-2"}, []v1.ObjectName{app, rev}, "a stopped pass moves nothing")
	require.NoError(t, g.st.Delete(g.ctx, v1.KindAppRevision.GVK(), "default", "web-auth-1", ""))
	app, rev, _ = g.pass(t)
	require.Equal(t, []v1.ObjectName{"web-auth-2", "rauthy-2"}, []v1.ObjectName{app, rev}, "a pruned revision moves nothing")
}

// scenario: addon-revision-superseded — with web-auth-2 Applied and web-auth-3 stamped, its pre-hooks running, the part
// keeps web-auth-2's record and its engine; once web-auth-3 is Applied, the part pins web-auth-3's record.
//
// oracle: docs/design/addon/revisions.fizz#MovesOnlyToTheLatestApplied
func TestScenarioAddonRevisionSuperseded(t *testing.T) {
	t.Parallel()
	g := newRevRig(t)
	g.part(t)
	g.appRevision(t, 2, "rauthy-2", v1.ConditionTrue)
	_, rev, _ := g.pass(t)
	require.Equal(t, v1.ObjectName("rauthy-2"), rev)

	g.appRevision(t, 3, "rauthy-3", v1.ConditionUnknown)
	app, rev, _ := g.pass(t)
	require.Equal(t, []v1.ObjectName{"web-auth-2", "rauthy-2"}, []v1.ObjectName{app, rev}, "a superseded revision is never a target")
	g.appRevision(t, 3, "rauthy-3", v1.ConditionTrue)
	app, rev, gen := g.pass(t)
	require.Equal(t, []v1.ObjectName{"web-auth-3", "rauthy-3"}, []v1.ObjectName{app, rev})
	require.Equal(t, "rauthy-3", gen, "the engine runs rauthy-3's spec")
}

// An App revision stamped while its Addon had no revision records none for the kind, and its part pins the Addon's
// current revision: once the Addon's first revision is stamped, the part that already follows that App revision takes
// it (#1536).
func TestIssue1536_PartTakesTheAddonsFirstRevision(t *testing.T) {
	t.Parallel()
	g := newUnstampedRig(t)
	g.part(t)
	g.appRevision(t, 1, "", v1.ConditionTrue)
	app, rev, _ := g.pass(t)
	require.Equal(t, []v1.ObjectName{"web-auth-1", ""}, []v1.ObjectName{app, rev}, "no revision to pin yet")

	g.stamp(t)
	app, rev, _ = g.pass(t)
	require.Equal(t, []v1.ObjectName{"web-auth-1", "rauthy-1"}, []v1.ObjectName{app, rev}, "the Addon's first revision")
	require.NotNil(t, g.rt.Engine(provider.ProviderRef{Namespace: "default", Name: "web-auth-login"}), "its engine runs")

	cur := g.addon(t)
	cur.Spec.Runtimes[0].Attributes.Env = map[string]string{"GEN": "rauthy-2"}
	_, err := g.st.Update(g.ctx, cur)
	require.NoError(t, err)
	g.stamp(t)
	app, rev, gen := g.pass(t)
	require.Equal(t, []v1.ObjectName{"web-auth-1", "rauthy-1"}, []v1.ObjectName{app, rev}, "the pin does not follow the Addon by itself")
	require.Empty(t, gen, "the engine keeps rauthy-1's spec")
}

// A part waits Pending, Progressing, until its App's first revision is Applied (ADR-0339 Decision 3).
func TestAddonRevisionPartWaitsForItsFirstRecord(t *testing.T) {
	t.Parallel()
	g := newRevRig(t)
	g.part(t)
	g.appRevision(t, 1, "rauthy-1", v1.ConditionUnknown)
	app, rev, _ := g.pass(t)
	require.Empty(t, app)
	require.Empty(t, rev)
	obj, err := g.st.Get(g.ctx, v1.KindAddonObject.GVK(), "default", "web-auth-login")
	require.NoError(t, err)
	c, _ := obj.(*v1.AddonObject).Status.Conditions.Get("Ready")
	require.Equal(t, "Progressing", c.Reason)
	require.Contains(t, c.Message, "App web-auth")
}

// scenario: addon-revision-retired — a revision that no object pins and no AppRevision records is removed; a pinned,
// a recorded and the current revision stay, and the Addon's reconciler requeues while one that is not current stays.
//
// oracle: docs/design/addon/revisions.fizz#PinsExist
// oracle: docs/design/addon/revisions.fizz#UnusedAreRemoved
func TestScenarioAddonRevisionRetired(t *testing.T) {
	t.Parallel()
	g := newRevRig(t)
	g.part(t)
	g.appRevision(t, 1, "rauthy-1", v1.ConditionTrue)
	g.pass(t) // pins rauthy-1, the record of web-auth-1
	g.appRevision(t, 2, "rauthy-2", v1.ConditionUnknown)
	for _, h := range []v1.ObjectName{"holder-1", "holder-2"} {
		require.NoError(t, g.st.Delete(g.ctx, v1.KindAppRevision.GVK(), "default", h, ""))
	}
	res := g.stamp(t)
	for _, n := range []v1.ObjectName{"rauthy-1", "rauthy-2", "rauthy-3"} {
		require.True(t, g.revisionExists(t, n), "%s: pinned, recorded or current", n)
	}
	require.NotZero(t, res.RequeueAfter, "a revision that is not current remains")

	require.NoError(t, g.st.Delete(g.ctx, v1.KindAppRevision.GVK(), "default", "web-auth-2", ""))
	g.stamp(t)
	require.False(t, g.revisionExists(t, "rauthy-2"), "no pin and no record: removed")
	require.NoError(t, g.st.Delete(g.ctx, v1.KindAppRevision.GVK(), "default", "web-auth-1", ""))
	g.stamp(t)
	require.True(t, g.revisionExists(t, "rauthy-1"), "no record left, but the part still pins it")

	require.NoError(t, g.st.Delete(g.ctx, v1.KindAddonObject.GVK(), "default", "web-auth-login", ""))
	res = g.stamp(t)
	require.False(t, g.revisionExists(t, "rauthy-1"))
	require.True(t, g.revisionExists(t, "rauthy-3"), "the current revision is never removed")
	require.Zero(t, res.RequeueAfter, "only the current one is left")
}

// On a worker node a revision that its control plane retired is a copy marked deleted: it stays while an object of the
// node pins it or an AppRevision of the node records it, so a pin that the control plane had not seen yet keeps its
// revision, and it goes after (FEAT-0002).
func TestARetiredRevisionsCopyStaysWhileTheNodeUsesIt(t *testing.T) {
	t.Parallel()
	g := newRevRig(t)
	g.part(t)
	g.appRevision(t, 1, "rauthy-1", v1.ConditionTrue)
	g.pass(t) // pins rauthy-1
	for _, h := range []v1.ObjectName{"holder-1", "holder-2"} {
		require.NoError(t, g.st.Delete(g.ctx, v1.KindAppRevision.GVK(), "default", h, ""))
	}
	follow := func(name v1.ObjectName) controller.Result {
		res, err := g.r.Followed().Reconcile(g.ctx, controller.Request{GVK: v1.KindAddonRevision.GVK(), Name: name})
		require.NoError(t, err)
		return res
	}
	retire := func(name v1.ObjectName) {
		obj, err := g.st.Get(g.ctx, v1.KindAddonRevision.GVK(), "", name)
		require.NoError(t, err)
		rev := obj.(*v1.AddonRevision)
		now := v1.NewTimestamp(time.Now())
		rev.DeletionTime = &now
		_, err = g.st.Update(g.ctx, rev)
		require.NoError(t, err)
	}
	follow("rauthy-2")
	require.True(t, g.revisionExists(t, "rauthy-2"), "the control plane still holds it")
	retire("rauthy-2")
	follow("rauthy-2")
	require.False(t, g.revisionExists(t, "rauthy-2"), "retired, and nothing on the node uses it")

	retire("rauthy-1")
	require.NotZero(t, follow("rauthy-1").RequeueAfter)
	require.True(t, g.revisionExists(t, "rauthy-1"), "the part pins it")
	_, _, gen := g.pass(t)
	require.Empty(t, gen, "its engine keeps rauthy-1's spec")
	require.NoError(t, g.st.Delete(g.ctx, v1.KindAddonObject.GVK(), "default", "web-auth-login", ""))
	require.NotZero(t, follow("rauthy-1").RequeueAfter, "web-auth-1 records it")
	require.NoError(t, g.st.Delete(g.ctx, v1.KindAppRevision.GVK(), "default", "web-auth-1", ""))
	follow("rauthy-1")
	require.False(t, g.revisionExists(t, "rauthy-1"))
	follow("rauthy-1")
}
