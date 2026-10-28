package lake_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/provider"
	lakesvc "github.com/pyvvo/funcd/internal/services/lake"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
)

// scenario: lake-scratch-refused-below-floor — the engine asks for its scratch and the catalog cap that sizes its
// spills; when the free-space floor refuses the scratch, the Lake is not Ready with a reason that names it, and a
// later pass tries again.
func TestScenarioLakeScratchRefusedBelowFloor(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	prov := &fakeProvider{err: fault.InsufficientStoragef("provider.Converge",
		"the data filesystem lacks the space for the scratch of default/lake")}
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) {
		d.ScratchBytes = 1 << 30
		d.CatalogMaxBytes = 256 << 20
		d.SaveInterval = time.Second
	})
	_, err := st.Create(ctx, mkLake("lake"))
	require.NoError(t, err)

	res, err := r.Reconcile(ctx, controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"})
	require.NoError(t, err)
	require.Positive(t, res.RequeueAfter, "a later pass tries again once space is freed")
	spec := prov.lastSpec(t)
	require.Equal(t, &provider.ScratchSpec{Path: "/var/funcd/ducklake", SizeBytes: 1 << 30}, spec.Scratch)
	require.Equal(t, "268435456", spec.Env["FUNCD_LAKE_CATALOG_MAX_BYTES"])

	obj, err := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	cond, ok := obj.(*v1.Lake).Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "ScratchSpaceLacking", cond.Reason)
	require.Contains(t, cond.Message, "lake.scratchSize")
}

func TestNoScratchAskedWhenUnset(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1}}
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) { d.SaveInterval = time.Second })
	_, err := st.Create(ctx, mkLake("lake"))
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"})
	require.NoError(t, err)
	spec := prov.lastSpec(t)
	require.Nil(t, spec.Scratch, "funcdctl dev's process engine and the in-memory path ask no scratch")
	require.NotContains(t, spec.Env, "FUNCD_LAKE_CATALOG_MAX_BYTES")
}
