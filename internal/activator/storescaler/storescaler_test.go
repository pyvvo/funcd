package storescaler_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/activator/storescaler"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func putFunction(t *testing.T, st store.Store, name string, phase v1.Phase) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name = v1.ObjectName(name)
	fn.Namespace = "default"
	fn.ResourceGroup = "rg1"
	fn.Status.Phase = phase
	_, err := st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func phaseOf(t *testing.T, st store.Store, name string) v1.Phase {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindFunction.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Function).Status.Phase
}

func ref(name string) activator.FunctionRef {
	return activator.FunctionRef{Namespace: "default", Name: v1.ObjectName(name)}
}

// scenario: scaler-writes-phase — ScaleTo records the partitioned Phase edges
// (Idle→Deploying wake, *→Idle reclaim), idempotently and without an off-diagram flip.
func TestScenarioScalerWritesPhase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	putFunction(t, st, "f", v1.PhaseIdle)
	sc := storescaler.New(st)

	// Wake: Idle → Deploying.
	require.NoError(t, sc.ScaleTo(ctx, ref("f"), 1))
	require.Equal(t, v1.PhaseDeploying, phaseOf(t, st, "f"))

	// Idempotent: ScaleTo(1) again is a no-op (still Deploying, no error).
	require.NoError(t, sc.ScaleTo(ctx, ref("f"), 1))
	require.Equal(t, v1.PhaseDeploying, phaseOf(t, st, "f"))

	// Reclaim: * → Idle.
	require.NoError(t, sc.ScaleTo(ctx, ref("f"), 0))
	require.Equal(t, v1.PhaseIdle, phaseOf(t, st, "f"))

	// Edge-respecting: a wake on an already-Ready function does not flip an off-diagram edge.
	putFunction(t, st, "ready", v1.PhaseReady)
	require.NoError(t, sc.ScaleTo(ctx, ref("ready"), 1))
	require.Equal(t, v1.PhaseReady, phaseOf(t, st, "ready"), "no off-diagram Ready→Deploying")
}

// racingStore forces exactly one RV-precondition conflict: on the first Get it bumps the
// object's resourceVersion underneath (a concurrent writer) before returning the now-stale
// read, so the caller's next Update hits fault.Conflict and must re-read + retry.
type racingStore struct {
	store.Store
	mu    sync.Mutex
	gets  int
	raced bool
}

func (r *racingStore) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	obj, err := r.Store.Get(ctx, gvk, ns, name)
	if err != nil {
		return obj, err
	}
	r.mu.Lock()
	r.gets++
	doRace := !r.raced
	r.raced = true
	r.mu.Unlock()
	if doRace {
		// Concurrent writer: re-read a fresh copy and Update it (bumps RV) so the copy
		// we hand back is stale and the caller's Update will conflict exactly once.
		if fresh, gerr := r.Store.Get(ctx, gvk, ns, name); gerr == nil {
			// A REAL change so the RV bumps and the stale copy conflicts once. A byte-identical
			// write would coalesce to a no-op and bump nothing (ADR-0047).
			if fn, ok := fresh.(*v1.Function); ok {
				fn.Status.Conditions.Set(v1.Condition{Type: "ChaosRace", Status: v1.ConditionTrue})
			}
			_, _ = r.Update(ctx, fresh) // promoted (Update is not overridden); r.Store.Get above avoids Get recursion
		}
	}
	return obj, nil
}

func (r *racingStore) getCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gets
}

// scenario: scaler-conflict-retry — an RV bump between the scaler's read and write
// (fault.Conflict) is retried and the intended Phase still converges.
func TestScenarioScalerConflictRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := store.New(memory.New())
	putFunction(t, base, "racy", v1.PhaseIdle)
	racing := &racingStore{Store: base}
	sc := storescaler.New(racing)

	require.NoError(t, sc.ScaleTo(ctx, ref("racy"), 1), "conflict must be retried, not surfaced")
	require.Equal(t, v1.PhaseDeploying, phaseOf(t, base, "racy"), "intent converged despite the race")
	require.GreaterOrEqual(t, racing.getCount(), 2, "the conflict forced a re-read")
}
