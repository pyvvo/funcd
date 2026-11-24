package addon_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/services/addon"
)

// ADR-0345 Decision 1: an object joins a call only once its first pass stored its mark, an App's part too, which pins
// its revision before that pass.
func TestEnginelessCallCarriesOnlyMarkedObjects(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("site", v1.DeletionRetain, "")
	g.stack("later", v1.DeletionRetain, "")
	later := g.object("later")
	later.Status.AddonRevision = v1.AddonRevisionName("infrastack", 1)
	_, err := g.st.Update(g.ctx, later)
	require.NoError(t, err)
	g.marked("site")
	g.pass("site")
	g.callEnds("site")
	require.Equal(t, []string{"site"}, names(g.lastCall().call), "later has no mark yet")
}

// ADR-0345 Decision 2: a call's digest holds its Function's current revision, so a new revision sends the group again.
func TestEnginelessNewFunctionRevisionCallsAgain(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("site", v1.DeletionRetain, "")
	g.marked("site")
	g.pass("site")
	g.callEnds("site")
	obj, err := g.st.Get(g.ctx, v1.KindFunction.GVK(), "default", "infrastack-reconciler")
	require.NoError(t, err)
	fn := obj.(*v1.Function)
	fn.Status.CurrentRevision = "infrastack-reconciler-2"
	_, err = g.st.Update(g.ctx, fn)
	require.NoError(t, err)
	g.pass("site")
	g.callEnds("site")
	require.Len(t, g.caller.got(), 2)
	require.Equal(t, "change", g.lastCall().call.Cause)
}

// ADR-0345 Decision 5: a failed call leaves each object it sent ReconcileFailed, and an answer that leaves an object
// out leaves it NoResult.
func TestEnginelessCallOutcomes(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("site", v1.DeletionRetain, "")
	g.marked("site")
	g.caller.set(func(context.Context, addon.ObjectsCall) (addon.SettingsReply, error) {
		return addon.SettingsReply{}, fault.Unavailablef("test", "the Function is down")
	})
	g.pass("site")
	g.callEnds("site")
	g.requireReason("site", "ReconcileFailed")

	g.caller.set(func(context.Context, addon.ObjectsCall) (addon.SettingsReply, error) {
		return addon.SettingsReply{}, nil
	})
	g.advance(time.Second)
	g.pass("site")
	g.callEnds("site")
	g.requireReason("site", "NoResult")
}

// ADR-0345 Decision 5: after a failed call the group waits 1 s, then is called again with cause retry.
func TestEnginelessFailedCallBacksOff(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("site", v1.DeletionRetain, "")
	g.marked("site")
	g.caller.set(func(context.Context, addon.ObjectsCall) (addon.SettingsReply, error) {
		return addon.SettingsReply{}, fault.Unavailablef("test", "the Function is down")
	})
	g.pass("site")
	g.callEnds("site")
	res := g.pass("site")
	require.Len(t, g.caller.got(), 1, "no call within the backoff")
	require.Equal(t, time.Second, res.RequeueAfter)
	g.caller.set(nil)
	g.advance(time.Second)
	g.pass("site")
	g.callEnds("site")
	require.Equal(t, "retry", g.lastCall().call.Cause)
}

// ADR-0345 Decision 6: an ok to a call that carried an object as deleting removes that object only: one created again
// under its name, of another uid, stays though it is held too.
func TestEnginelessOkRemovesOnlyTheObjectItCarried(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("site", v1.DeletionDelete, "")
	g.marked("site")
	require.True(t, g.del("site"))
	first := g.object("site").UID
	release := make(chan struct{})
	g.caller.set(func(_ context.Context, c addon.ObjectsCall) (addon.SettingsReply, error) {
		<-release
		return answerAll(c, true, ""), nil
	})
	g.pass("site")
	require.Eventually(t, func() bool { return len(g.caller.got()) == 1 }, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, g.st.Delete(g.ctx, v1.KindAddonObject.GVK(), "default", "site", ""))
	g.stack("site", v1.DeletionDelete, "")
	again := g.object("site")
	require.NotEqual(t, first, again.UID)
	again.Status.Workload = v1.WorkloadEngineless
	now := v1.NewTimestamp(g.now())
	again.DeletionTime = &now
	_, err := g.st.Update(g.ctx, again)
	require.NoError(t, err)

	close(release)
	g.callEnds("site")
	o, exists := g.stored("site")
	require.True(t, exists, "the ok was for the first site")
	require.Equal(t, again.UID, o.UID)
}
