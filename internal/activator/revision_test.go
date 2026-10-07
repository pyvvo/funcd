package activator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// storeHeld stores Function f (current and serving f-2) with scaling and its Revision f-1 in phase p, and returns a
// ref pinned to f-1, a held revision (ADR-0190).
func storeHeld(t *testing.T, st store.Store, scaling v1.Scaling, p v1.Phase, reason string) activator.FunctionRef {
	t.Helper()
	ctx := context.Background()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "f", "default", "rg1"
	fn.Spec.Scaling = scaling
	fn.Status.Phase, fn.Status.CurrentRevision, fn.Status.ServingRevision = v1.PhaseReady, "f-2", "f-2"
	created, err := st.Create(ctx, fn)
	require.NoError(t, err)
	uid := created.GetObjectMeta().UID
	obj, ok = v1.NewObject(v1.KindRevision)
	require.True(t, ok)
	rev := obj.(*v1.Revision)
	rev.Name, rev.Namespace, rev.ResourceGroup = "f-1", "default", "rg1"
	rev.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Name: "f"}, UID: uid, Controller: true}}
	rev.Status.Phase = p
	rev.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionFalse, Reason: reason})
	_, err = st.Create(ctx, rev)
	require.NoError(t, err)
	return activator.FunctionRef{Namespace: "default", Name: "f", Revision: "f-1", UID: uid}
}

// refScaler records each ScaleTo's ref and target.
type refScaler struct {
	mu    sync.Mutex
	calls []activator.FunctionRef
	reps  []int
}

func (s *refScaler) ScaleTo(_ context.Context, fn activator.FunctionRef, replicas int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls, s.reps = append(s.calls, fn), append(s.reps, replicas)
	return nil
}

func (s *refScaler) recorded() ([]activator.FunctionRef, []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]activator.FunctionRef(nil), s.calls...), append([]int(nil), s.reps...)
}

// Single-flight is per revision (ADR-0190 Decision 5): concurrent cold wakes pinned to one revision share one
// ScaleTo, and a wake of the Function by name is an activation of its own.
func TestWakeSingleFlightPerRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep := &fakeEndpoints{}
	sc := &refScaler{}
	a := newActivator(t, activator.Deps{Endpoints: ep, Scaler: sc, ActivationTimeout: 10 * time.Second, PollInterval: time.Millisecond})
	pinned := activator.FunctionRef{Namespace: "default", Name: "f", Revision: "f-1", UID: "u1"}
	byName := activator.FunctionRef{Namespace: "default", Name: "f"}

	var wg sync.WaitGroup
	for _, ref := range []activator.FunctionRef{pinned, pinned, pinned, byName} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.Wake(ctx, ref)
			require.NoError(t, err)
		}()
	}
	require.Eventually(t, func() bool { calls, _ := sc.recorded(); return len(calls) == 2 }, 5*time.Second, time.Millisecond)
	ep.setReady("http://10.0.0.9:8080")
	wg.Wait()
	calls, _ := sc.recorded()
	require.ElementsMatch(t, []activator.FunctionRef{pinned, byName}, calls, "one activation per revision, one for the Function")
}

// Idle reclaim is per revision (ADR-0190 Decisions 6 and 10): a held revision idle past its Function's IdleTimeout, or
// past the step Functions' five minutes when the Function has none, is scaled to zero by its pinned ref, whatever the
// Function's replica floor, which keeps the Function itself up.
func TestReclaimIdleReclaimsHeldRevision(t *testing.T) {
	t.Parallel()
	for _, sc := range []v1.Scaling{{MinReplicas: 1, IdleTimeout: v1.Duration(time.Hour)}, {}} {
		ctx := context.Background()
		st := store.New(memory.New())
		held := storeHeld(t, st, sc, v1.PhaseReady, "")
		clk := &stepClock{t: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
		rs := &refScaler{}
		a := newActivator(t, activator.Deps{Store: st, Endpoints: &fakeEndpoints{upstream: "http://10.0.0.9:8080", ready: true}, Scaler: rs, Clock: clk})

		_, err := a.Wake(ctx, held)
		require.NoError(t, err)
		require.NoError(t, a.ReclaimIdle(ctx))
		calls, _ := rs.recorded()
		require.Empty(t, calls, "recent activity keeps the held revision")

		clk.advance(2 * time.Hour)
		require.NoError(t, a.ReclaimIdle(ctx))
		calls, reps := rs.recorded()
		require.Equal(t, []activator.FunctionRef{held}, calls, "only the held revision is reclaimed (idle timeout %s)", sc.IdleTimeout)
		require.Equal(t, []int{0}, reps)
	}
}

// Activity is per revision (ADR-0190 Decision 5): a call pinned to a held revision leaves its Function's idle window
// running, while one pinned to the serving revision keeps the Function up.
func TestPinnedCallCountsForItsRevision(t *testing.T) {
	t.Parallel()
	const idle = time.Hour
	for _, tc := range []struct {
		revision  v1.ObjectName
		reclaimed bool
	}{{"f-1", true}, {"f-2", false}} {
		ctx := context.Background()
		st := store.New(memory.New())
		held := storeHeld(t, st, v1.Scaling{IdleTimeout: v1.Duration(idle)}, v1.PhaseReady, "")
		clk := &stepClock{t: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
		rs := &refScaler{}
		a := newActivator(t, activator.Deps{Store: st, Endpoints: &fakeEndpoints{upstream: "http://10.0.0.9:8080", ready: true}, Scaler: rs, Clock: clk})
		require.NoError(t, a.ReclaimIdle(ctx))

		clk.advance(idle / 2)
		pinned := held
		pinned.Revision = tc.revision
		_, err := a.Wake(ctx, pinned)
		require.NoError(t, err)
		clk.advance(idle * 3 / 4)
		require.NoError(t, a.ReclaimIdle(ctx))
		calls, _ := rs.recorded()
		byName := activator.FunctionRef{Namespace: "default", Name: "f"}
		if tc.reclaimed {
			require.Equal(t, []activator.FunctionRef{byName}, calls, "a held revision's call is not its Function's activity")
		} else {
			require.Empty(t, calls, "a call pinned to the serving revision keeps its Function up")
		}
	}
}

// A cold wake pinned to a held revision that turns Failed is answered at once naming the revision, not by the
// Function's phase (ADR-0190 Decision 5).
func TestWakeFailsOnFailedHeldRevision(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	held := storeHeld(t, st, v1.Scaling{}, v1.PhaseFailed, "ShapeInvalid")
	a := newActivator(t, activator.Deps{Store: st, Endpoints: &fakeEndpoints{}, Scaler: &refScaler{}, ActivationTimeout: 10 * time.Second, PollInterval: time.Millisecond})

	start := time.Now()
	_, err := a.Wake(context.Background(), held)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.ErrorContains(t, err, `revision "f-1" of function default/f is Failed (ShapeInvalid)`)
	require.Less(t, time.Since(start), 5*time.Second, "answered without waiting out the activation timeout")
}
