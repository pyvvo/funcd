package app_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// deleteRace deletes the App todo and its part todo-api the first time a pass reads that part once armed: the App's
// delete and the owner GC's pass landing between the pass's read of the App and its read of the parts.
type deleteRace struct {
	store.Store
	armed atomic.Bool
}

func (s *deleteRace) Get(ctx context.Context, gvk v1.GroupVersionKind, n v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if gvk.Kind == v1.KindFunction && name == "todo-api" && s.armed.CompareAndSwap(true, false) {
		if err := s.Delete(ctx, v1.KindApp.GVK(), n, "todo", ""); err != nil {
			return nil, err
		}
		if err := s.Delete(ctx, gvk, n, name, ""); err != nil {
			return nil, err
		}
	}
	return s.Store.Get(ctx, gvk, n, name)
}

// A pass that read the App before its delete writes back no part the owner GC already collected, and ends without an
// error: the race that left Function todo-api behind in TestScenarioDeletionPolicyRetain on CI.
func TestAppDeletedDuringAPassLeavesNoPart(t *testing.T) {
	st := &deleteRace{Store: store.New(memory.New())}
	h := newHarness(t, st)
	h.install(todoApp(nil))
	st.armed.Store(true)
	_, err := h.reconcileErr()
	require.Nil(t, h.get(v1.KindApp, "todo"))
	require.Nil(t, h.get(v1.KindFunction, "todo-api"), "no part of a deleted App is written back")
	require.NoError(t, err)
}
