package app_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/hold"
)

// scenario: held-rollout-deadline (the reconciler half) — G holds an AppRevision Deploying past app.upgradeTimeout.
// Restored, the platform boots held: a pass writes nothing and requeues. The release persists its time, and after
// two restarts before the App runs the revision fails a full timeout after the release.
func TestScenarioHeldRolloutDeadline(t *testing.T) {
	dir := t.TempDir()
	open := func() *hold.Hold {
		h, err := hold.Open(dir)
		require.NoError(t, err)
		return h
	}
	h := newHarness(t, nil, func(d *app.Deps) { d.UpgradeTimeout = 20 * time.Second })
	h.install(todoApp(nil))
	h.edit(setImage("oci-layout://never-starts:2"))
	require.Equal(t, 20*time.Second, h.reconcile().RequeueAfter)
	h.clk.Advance(time.Minute)

	require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(h.clk.Now())}))
	held := open()
	h.deps.Hold = held
	h.restart()
	before := h.versionsAll()
	require.Equal(t, controller.Result{RequeueAfter: controller.SupervisionPeriod}, h.reconcile())
	require.Equal(t, before, h.versionsAll(), "a held pass writes nothing")
	require.Equal(t, v1.PhaseDeploying, h.rev(2).Status.Phase)

	h.clk.Advance(time.Hour)
	require.NoError(t, held.Release(h.clk.Now()))
	for range 2 {
		h.deps.Hold = open()
		h.restart()
	}
	require.Equal(t, 20*time.Second, h.reconcile().RequeueAfter, "a full timeout from the release")
	require.Equal(t, v1.PhaseDeploying, h.rev(2).Status.Phase)
	h.clk.Advance(20*time.Second - time.Millisecond)
	require.Equal(t, time.Millisecond, h.reconcile().RequeueAfter)
	h.clk.Advance(time.Millisecond)
	h.reconcile()
	require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)
}
