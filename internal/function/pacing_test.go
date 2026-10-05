package function_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/pooling"
	"github.com/pyvvo/funcd/internal/runtime"
)

// The ADR-0163 Deps fields pace the Function reconciler: the referent wait, bounded by the supervision period, the
// drain's re-check, and the boot timeout that judges a solo replica and bounds every pool host's load.
func TestPacingDepsPaceTheFunctionReconciler(t *testing.T) {
	t.Parallel()
	waiting := func(t *testing.T, opt func(*function.Deps)) time.Duration {
		h := newHarness(t, opt)
		h.createFn(t, "fn", 1, true)
		fn := h.getFn(t, "fn")
		fn.Spec.KV = []v1.FunctionKV{{Alias: "cache", Store: "nostore", Table: "t"}}
		_, err := h.st.Update(context.Background(), fn)
		require.NoError(t, err)
		res, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "fn"})
		require.NoError(t, err)
		cond, ok := h.getFn(t, "fn").Status.Conditions.Get("Ready")
		require.True(t, ok)
		require.Equal(t, "KVStoreNotFound", cond.Reason)
		return res.RequeueAfter
	}
	t.Run("referentPollInterval", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, 100*time.Millisecond, waiting(t, func(d *function.Deps) { d.ReferentPollInterval = 100 * time.Millisecond }))
		require.Equal(t, 2*time.Second, waiting(t, func(*function.Deps) {}), "0 keeps 2s")
	})
	t.Run("bounded by supervisionPeriod", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, 50*time.Millisecond, waiting(t, func(d *function.Deps) {
			d.ReferentPollInterval, d.SupervisionPeriod = 100*time.Millisecond, 50*time.Millisecond
		}))
	})
	t.Run("drainPollInterval", func(t *testing.T) {
		t.Parallel()
		drainWait := func(t *testing.T, poll time.Duration) time.Duration {
			h := newShimHarness(t, http.StatusOK, false, func(d *function.Deps) { d.DrainPollInterval = poll })
			h.createFn(t, "echo")
			h.reconcile(t, "echo")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "echo").Status.Phase)
			h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
			h.reconcile(t, "echo")
			require.Equal(t, "echo-1", h.getFn(t, "echo").Status.DrainingRevision)
			return h.reconcile(t, "echo").RequeueAfter
		}
		require.Equal(t, 100*time.Millisecond, drainWait(t, 100*time.Millisecond), "re-checked within the interval, before the 2s hand-out settle ends")
		require.Equal(t, time.Second, drainWait(t, 0), "0 keeps 1s")
	})
	t.Run("bootTimeout stops a solo replica that never listens", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withPeriod, func(d *function.Deps) { d.BootTimeout = 5 * time.Second })
		h.deployReady(t, "stall")
		h.rt.exit("stall", runtime.StateFailed, time.Minute)
		h.rt.hold(runtime.NewInstanceID("default", "stall", "stall-1", 0), true)
		h.reconcile(t, "stall")
		require.Equal(t, v1.PhaseDegraded, h.getFn(t, "stall").Status.Phase, "the replacement boots")

		h.rt.exitRevision("stall", "stall-1", 0, runtime.StateRunning, 5*time.Second)
		h.reconcile(t, "stall")
		require.Equal(t, runtime.StateStopped, h.rt.revisionStates("stall")["stall-1"][0], "stopped once it has not listened for bootTimeout")
		require.Contains(t, h.condition(t, "stall", "Ready").Message, "did not listen within 5s")
	})
	t.Run("bootTimeout judges a solo replica that never becomes ready", func(t *testing.T) {
		t.Parallel()
		const boot = time.Second
		h := newShimHarness(t, http.StatusOK, false, withPeriod, func(d *function.Deps) { d.BootTimeout = boot })
		_, setStatus := h.rt.serveRevision(t, "stall-1", http.StatusOK)
		h.deployReady(t, "stall")
		h.rt.exit("stall", runtime.StateFailed, time.Minute)
		setStatus(http.StatusServiceUnavailable)
		h.reconcile(t, "stall")
		require.Equal(t, v1.PhaseDegraded, h.getFn(t, "stall").Status.Phase, "the replacement listens but is not ready")

		h.rt.exitRevision("stall", "stall-1", 0, runtime.StateRunning, boot)
		res := h.reconcile(t, "stall")
		require.Equal(t, 200*time.Millisecond, res.RequeueAfter, "kept while the Function has been Degraded for less than bootTimeout")

		time.Sleep(boot)
		h.reconcile(t, "stall")
		require.Contains(t, h.condition(t, "stall", "Ready").Message, "did not become ready within 1s")
		require.NotEqual(t, runtime.StateRunning, h.rt.revisionStates("stall")["stall-1"][0], "stopped once Degraded for bootTimeout")
	})
	t.Run("bootTimeout bounds the pool host's load", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) { d.BootTimeout = 2 * time.Second })
		h.create(t, "a", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w" })
		h.pools(t, "a")
		key, ok := pooling.ParsePool("default", h.getFn(t, "a").Status.Pool)
		require.True(t, ok)
		spec, ok := h.rt.specFor(v1.ObjectName("__pool__nodejs22__w__" + key.AccessHash))
		require.True(t, ok)
		require.Equal(t, "2000", spec.Env["FUNCD_POOL_LOAD_TIMEOUT_MS"])
	})
}
