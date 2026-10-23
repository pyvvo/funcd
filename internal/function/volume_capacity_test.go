package function_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/testkit/poll"
	"github.com/pyvvo/funcd/internal/volume"
)

// TestScenarioEmptyDirOverCapacityIsNotReady: a worker whose emptyDir does not fit the capacity is not created, and
// the Function is Ready=False with reason VolumeCapacity (ADR-0262 Decision 5, scenario capacity-not-overcommitted).
func TestScenarioEmptyDirOverCapacityIsNotReady(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, 200, false, func(d *function.Deps) {
		m, err := volume.New(volume.Options{Root: t.TempDir(), CapacityBytes: 1 << 20})
		require.NoError(t, err)
		d.Volumes = m
	})
	h.create(t, "thumbs", func(fn *v1.Function) {
		fn.Spec.Volumes = []v1.VolumeEntry{{Name: "cache", Type: v1.VolumeEmptyDir, SizeBytes: 2 << 20}}
	})
	h.reconcile(t, "thumbs")
	h.requireCondition(t, "thumbs", "Ready", v1.ConditionFalse, "VolumeCapacity")
	_, created := h.rt.specFor("thumbs")
	require.False(t, created, "no worker is created without its emptyDir")
}

// cartWithState is a harness with durable volumes and Function cart, which declares local volume state.
func cartWithState(t *testing.T) *shimHarness {
	t.Helper()
	h := newShimHarness(t, 200, false, func(d *function.Deps) {
		m, err := volume.New(volume.Options{Root: t.TempDir()})
		require.NoError(t, err)
		d.Volumes, d.VolumesDurable = m, true
	})
	h.create(t, "cart", func(fn *v1.Function) {
		fn.Spec.Volumes = []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, SizeBytes: 1 << 20}}
	})
	return h
}

// liveWorkers counts name's workers that have not ended.
func liveWorkers(h *shimHarness, name v1.ObjectName) int {
	n := 0
	for _, replicas := range h.rt.revisionStates(name) {
		for _, st := range replicas {
			if !st.Terminal() {
				n++
			}
		}
	}
	return n
}

// TestIssue1206_RestoredVolumeClearsDataMissing: a Succeeded Restore into a lost volume clears DataMissing on the next
// pass, though the Function was steady; it replaces no worker, since the Restore loaded the volume while none ran
// (#1206).
func TestIssue1206_RestoredVolumeClearsDataMissing(t *testing.T) {
	t.Parallel()
	h := cartWithState(t)
	h.reconcile(t, "cart")
	h.reconcile(t, "cart")
	before := h.creates()
	require.NotZero(t, before, "cart runs a worker")
	fn := h.getFn(t, "cart")
	fn.Status.Conditions.Set(v1.Condition{Type: "DataMissing", Status: v1.ConditionTrue, Reason: "NoVolume", // as the first pass after a loss sets it
		Message: "lost volumes: state; the Function was Ready before and these local volumes are gone, so they start empty: run a workload Restore into each"})
	_, err := h.st.Update(t.Context(), fn)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond) // the Restore's creation follows the condition's transition
	_, err = h.st.Create(t.Context(), &v1.Restore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRestore.GVK().APIVersion(), Kind: v1.KindRestore},
		ObjectMeta: v1.ObjectMeta{Name: "back", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.RestoreSpec{Backup: "b1", Source: v1.RestoreSource{CredentialsSecret: "reader", IdentitySecret: "owner-key"},
			Members: []v1.RestoreMember{{Kind: v1.KindFunction, Name: "cart", Volume: "state", InPlace: true}}},
		Status: v1.RestoreStatus{Phase: v1.RunSucceeded}})
	require.NoError(t, err)
	h.reconcile(t, "cart")
	h.requireCondition(t, "cart", "DataMissing", v1.ConditionFalse, "Restored")
	require.Equal(t, before, h.creates(), "clearing DataMissing replaces no worker")
}

// TestIssue1206_StopWorkersKeepsTheFunctionWorkerless: while a Restore loads a Function's volume no worker of the
// Function runs and no pass creates one; once the Restore releases it, the next pass starts its worker again (#1206).
func TestIssue1206_StopWorkersKeepsTheFunctionWorkerless(t *testing.T) {
	t.Parallel()
	h := cartWithState(t)
	h.reconcile(t, "cart")
	h.reconcile(t, "cart")
	require.Equal(t, 1, liveWorkers(h, "cart"), "cart runs a worker")
	release, err := h.r.StopWorkers(t.Context(), "default", "cart")
	require.NoError(t, err)
	require.Zero(t, liveWorkers(h, "cart"), "StopWorkers returns once no worker runs")
	before := h.creates()
	h.reconcile(t, "cart")
	require.Equal(t, before, h.creates(), "no pass creates a worker while the volume loads")
	require.Zero(t, liveWorkers(h, "cart"))
	release()
	h.reconcile(t, "cart")
	require.Equal(t, 1, liveWorkers(h, "cart"), "the next pass starts the worker again")
}

// TestIssue1206_StopWorkersWaitsForAPassCreatingWorkers: a pass that began creating the Function's worker before the
// Restore asked ends first, and StopWorkers stops the worker it created too (#1206).
func TestIssue1206_StopWorkersWaitsForAPassCreatingWorkers(t *testing.T) {
	t.Parallel()
	h := cartWithState(t)
	creating, proceed := make(chan struct{}), make(chan struct{})
	h.rt.mu.Lock()
	h.rt.onCreate = func(runtime.WorkerSpec) {
		close(creating)
		<-proceed
	}
	h.rt.mu.Unlock()
	passed := make(chan error, 1)
	go func() {
		_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "cart"})
		passed <- err
	}()
	select {
	case <-creating:
	case err := <-passed:
		t.Fatalf("the pass ended without creating a worker: %v", err)
	}
	h.rt.mu.Lock()
	h.rt.onCreate = nil
	h.rt.mu.Unlock()
	type stop struct {
		release func()
		err     error
	}
	stopped := make(chan stop, 1)
	go func() {
		release, err := h.r.StopWorkers(context.Background(), "default", "cart")
		stopped <- stop{release, err}
	}()
	require.True(t, poll.Never(func() bool { return len(stopped) > 0 }, 100*time.Millisecond, 5*time.Millisecond),
		"StopWorkers returned while a pass was creating a worker")
	close(proceed)
	require.NoError(t, <-passed)
	s := <-stopped
	require.NoError(t, s.err)
	require.Zero(t, liveWorkers(h, "cart"), "the worker that pass created is stopped too")
	s.release()
}

// stealingVolumes books another volume right after each Release, as a Function waiting for capacity can on its pass.
type stealingVolumes struct {
	volume.Manager
	steal func()
}

func (s *stealingVolumes) Release(ctx context.Context, id volume.ID) error {
	err := s.Manager.Release(ctx, id)
	if s.steal != nil {
		s.steal()
	}
	return err
}

// TestIssue1207_CrashedWorkerKeepsItsEmptyDirSpace: a pass that replaces a crashed worker hands the space of its
// emptyDir to the new worker; a volume waiting for capacity cannot take it while the pass runs (#1207).
func TestIssue1207_CrashedWorkerKeepsItsEmptyDirSpace(t *testing.T) {
	t.Parallel()
	var inner volume.Manager
	s := &stealingVolumes{}
	h := newShimHarness(t, 200, false, func(d *function.Deps) {
		m, err := volume.New(volume.Options{Root: t.TempDir(), CapacityBytes: 2 << 20})
		require.NoError(t, err)
		inner, s.Manager = m, m
		d.Volumes = s
	})
	h.create(t, "thumbs", func(fn *v1.Function) {
		fn.Spec.Volumes = []v1.VolumeEntry{{Name: "cache", Type: v1.VolumeEmptyDir, SizeBytes: 1 << 20}}
	})
	h.reconcile(t, "thumbs")
	h.reconcile(t, "thumbs")
	before := h.creates()
	require.NotZero(t, before, "thumbs runs a worker")
	waiting := volume.ID{Namespace: "default", Function: "big", Name: "state"}
	require.ErrorIs(t, inner.Book(context.Background(), waiting, 2<<20), volume.ErrCapacity)
	s.steal = func() { _ = inner.Book(context.Background(), waiting, 2<<20) }
	h.rt.exit("thumbs", runtime.StateFailed, time.Hour)
	require.Eventually(t, func() bool {
		h.reconcile(t, "thumbs")
		return h.creates() > before
	}, 5*time.Second, 10*time.Millisecond, "the crashed worker is replaced with its emptyDir")
}
