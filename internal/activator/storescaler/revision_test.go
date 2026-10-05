package storescaler_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/activator/storescaler"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// putHeld stores Function f in phase fnPhase, current and serving f-2, and its Revisions f-1 (in phase revPhase) and
// f-2; it returns refs pinned to f-1, a held revision, and to f-2, the current one (ADR-0190).
func putHeld(t *testing.T, st store.Store, fnPhase, revPhase v1.Phase) (held, current activator.FunctionRef) {
	t.Helper()
	ctx := context.Background()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "f", "default", "rg1"
	fn.Status.Phase, fn.Status.CurrentRevision, fn.Status.ServingRevision = fnPhase, "f-2", "f-2"
	created, err := st.Create(ctx, fn)
	require.NoError(t, err)
	uid := created.GetObjectMeta().UID
	for _, name := range []v1.ObjectName{"f-1", "f-2"} {
		obj, ok := v1.NewObject(v1.KindRevision)
		require.True(t, ok)
		rev := obj.(*v1.Revision)
		rev.Name, rev.Namespace, rev.ResourceGroup = name, "default", "rg1"
		rev.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Name: "f"}, UID: uid, Controller: true}}
		if name == "f-1" {
			rev.Status.Phase = revPhase
			rev.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionFalse, Reason: "ShapeInvalid"})
		}
		_, err := st.Create(ctx, rev)
		require.NoError(t, err)
	}
	return activator.FunctionRef{Namespace: "default", Name: "f", Revision: "f-1", UID: uid},
		activator.FunctionRef{Namespace: "default", Name: "f", Revision: "f-2", UID: uid}
}

func revPhaseOf(t *testing.T, st store.Store, name v1.ObjectName) v1.Phase {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindRevision.GVK(), "default", name)
	require.NoError(t, err)
	return obj.(*v1.Revision).Status.Phase
}

// A wake pinned to a held revision writes that Revision's Idle→Deploying intent and never the Function's phase;
// its idle reclaim moves the Revision back to Idle (ADR-0190 Decisions 5 and 6).
func TestScaleToHeldRevisionWritesRevisionIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	held, _ := putHeld(t, st, v1.PhaseFailed, v1.PhaseIdle)
	sc := storescaler.New(st)
	rv := rvOf(t, st, "f")

	require.NoError(t, sc.ScaleTo(ctx, held, 1))
	require.Equal(t, v1.PhaseDeploying, revPhaseOf(t, st, "f-1"))
	require.Equal(t, v1.PhaseFailed, phaseOf(t, st, "f"), "a Failed Function does not refuse another revision's wake")
	require.Equal(t, rv, rvOf(t, st, "f"), "the Function is not written")

	require.NoError(t, sc.ScaleTo(ctx, held, 0))
	require.Equal(t, v1.PhaseDeploying, revPhaseOf(t, st, "f-1"), "a booting revision is not reclaimed")

	obj, err := st.Get(ctx, v1.KindRevision.GVK(), "default", "f-1")
	require.NoError(t, err)
	rev := obj.(*v1.Revision)
	rev.Status.Phase = v1.PhaseReady
	_, err = st.Update(ctx, rev)
	require.NoError(t, err)
	require.NoError(t, sc.ScaleTo(ctx, held, 0))
	require.Equal(t, v1.PhaseIdle, revPhaseOf(t, st, "f-1"))
	require.Equal(t, rv, rvOf(t, st, "f"), "the Function is not written")
}

// A wake pinned to a Failed held revision is refused naming it; one pinned to a Function with another UID is
// fault.NotFound and wakes no namesake; one pinned to the current revision is the Function's own wake.
func TestScaleToPinnedRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	st := store.New(memory.New())
	held, _ := putHeld(t, st, v1.PhaseReady, v1.PhaseFailed)
	err := storescaler.New(st).ScaleTo(ctx, held, 1)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.ErrorContains(t, err, `revision "f-1" of function default/f is Failed (ShapeInvalid)`)

	st = store.New(memory.New())
	held, _ = putHeld(t, st, v1.PhaseIdle, v1.PhaseIdle)
	held.UID = "another"
	err = storescaler.New(st).ScaleTo(ctx, held, 1)
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.Equal(t, v1.PhaseIdle, phaseOf(t, st, "f"))
	require.Equal(t, v1.PhaseIdle, revPhaseOf(t, st, "f-1"))

	st = store.New(memory.New())
	_, current := putHeld(t, st, v1.PhaseIdle, v1.PhaseIdle)
	require.NoError(t, storescaler.New(st).ScaleTo(ctx, current, 1))
	require.Equal(t, v1.PhaseDeploying, phaseOf(t, st, "f"))
	require.Equal(t, v1.PhaseIdle, revPhaseOf(t, st, "f-1"))
}
