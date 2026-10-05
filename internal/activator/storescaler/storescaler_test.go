package storescaler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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

func rvOf(t *testing.T, st store.Store, name string) string {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindFunction.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Function).ResourceVersion
}

// scenario: scaler-writes-phase — ScaleTo records the partitioned Phase edges
// (Idle→Deploying wake, Ready→Idle reclaim), idempotently and without an off-diagram flip;
// a reclaim of a Deploying function writes nothing (ADR-0169).
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

	// No reclaim of a woken function that has not served yet: ScaleTo(0) on Deploying writes nothing.
	rv := rvOf(t, st, "f")
	require.NoError(t, sc.ScaleTo(ctx, ref("f"), 0))
	require.Equal(t, v1.PhaseDeploying, phaseOf(t, st, "f"))
	require.Equal(t, rv, rvOf(t, st, "f"))

	// Reclaim: Ready → Idle.
	putFunction(t, st, "served", v1.PhaseReady)
	require.NoError(t, sc.ScaleTo(ctx, ref("served"), 0))
	require.Equal(t, v1.PhaseIdle, phaseOf(t, st, "served"))

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

// coldEndpoints never reports a ready upstream, as for a Failed function whose worker is not restarted (ADR-0142).
type coldEndpoints struct{}

func (coldEndpoints) Upstream(context.Context, activator.FunctionRef) (string, bool, error) {
	return "", false, nil
}

// failShape writes what the reconciler writes when the shim cannot load the handler (finish in internal/function).
func failShape(ctx context.Context, st store.Store, name string) error {
	obj, err := st.Get(ctx, v1.KindFunction.GVK(), "default", v1.ObjectName(name))
	if err != nil {
		return err
	}
	fn := obj.(*v1.Function)
	fn.Status.Phase = v1.PhaseFailed
	fn.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionFalse, Reason: "ShapeInvalid"})
	_, err = st.Update(ctx, fn)
	return err
}

// failsAfterWake is the real scaler followed by the reconciler failing the function it just woke.
type failsAfterWake struct {
	activator.Scaler
	st store.Store
}

func (s failsAfterWake) ScaleTo(ctx context.Context, fn activator.FunctionRef, replicas int) error {
	if err := s.Scaler.ScaleTo(ctx, fn, replicas); err != nil {
		return err
	}
	return failShape(ctx, s.st, string(fn.Name))
}

// call serves one call to name through a real activator.
func call(t *testing.T, st store.Store, sc activator.Scaler, name string) *httptest.ResponseRecorder {
	t.Helper()
	a, err := activator.New(activator.Deps{
		Store:             st,
		Endpoints:         coldEndpoints{},
		Scaler:            sc,
		ActivationTimeout: 2 * time.Second,
	})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, activator.WithFunction(httptest.NewRequest(http.MethodPost, "/", nil), ref(name)))
	return rec
}

// Issue #142: a call to a Failed (ShapeInvalid) function is answered at once with the function's state, instead of
// being held for the whole activation timeout and then told the function "did not become ready".
func TestIssue142_FailedFunctionIsAnsweredWithItsState(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	putFunction(t, st, "broken", v1.PhaseFailed)
	require.NoError(t, failShape(context.Background(), st, "broken"))

	rec := call(t, st, storescaler.New(st), "broken")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "function default/broken is Failed (ShapeInvalid)")
	require.NotContains(t, rec.Body.String(), "did not become ready", "the call must not wait out the activation timeout")
	require.Equal(t, v1.PhaseFailed, phaseOf(t, st, "broken"), "a wake does not move a Failed function")
}

// Issue #142: a scale-to-zero function the reconciler fails after the wake, because its shim cannot load the handler,
// is answered as soon as it is Failed, not at the end of the activation timeout.
func TestIssue142_FunctionFailedDuringActivationIsAnsweredAtOnce(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	putFunction(t, st, "late", v1.PhaseIdle)

	rec := call(t, st, failsAfterWake{Scaler: storescaler.New(st), st: st}, "late")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "function default/late is Failed (ShapeInvalid)")
	require.NotContains(t, rec.Body.String(), "did not become ready", "the call must not wait out the activation timeout")
}

// The reclaim edge moves only a phase activator.Reclaimable admits to Idle and writes nothing for any other: a Failed,
// Deploying or Terminating Function is never reclaimed (ADR-0169 Decision 3).
func TestReclaimEdgeAllowList(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ from, to v1.Phase }{
		"empty":       {"", v1.PhaseIdle},
		"pending":     {v1.PhasePending, v1.PhaseIdle},
		"ready":       {v1.PhaseReady, v1.PhaseIdle},
		"degraded":    {v1.PhaseDegraded, v1.PhaseIdle},
		"idle":        {v1.PhaseIdle, v1.PhaseIdle},
		"failed":      {v1.PhaseFailed, v1.PhaseFailed},
		"deploying":   {v1.PhaseDeploying, v1.PhaseDeploying},
		"terminating": {v1.PhaseTerminating, v1.PhaseTerminating},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			st := store.New(memory.New())
			putFunction(t, st, "f", tc.from)
			rv := rvOf(t, st, "f")
			require.NoError(t, storescaler.New(st).ScaleTo(context.Background(), ref("f"), 0))
			require.Equal(t, tc.to, phaseOf(t, st, "f"))
			if tc.to != v1.PhaseIdle || tc.from == v1.PhaseIdle {
				require.Equal(t, rv, rvOf(t, st, "f"), "no write")
			}
		})
	}
}
