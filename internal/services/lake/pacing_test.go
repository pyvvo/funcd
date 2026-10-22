package lake_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/provider"
	lakesvc "github.com/pyvvo/funcd/internal/services/lake"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
)

// lake.enginePollInterval re-checks an engine not answering its probe yet, and controller.referentPollInterval a
// lake whose Bucket is missing (ADR-0163).
func TestLakePollIntervalsFromDeps(t *testing.T) {
	ctx := context.Background()
	req := controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"}
	pace := func(d *lakesvc.ReconcilerDeps) {
		d.EnginePollInterval, d.ReferentPollInterval = 200*time.Millisecond, 100*time.Millisecond
	}
	t.Run("enginePollInterval", func(t *testing.T) {
		st := store.New(storemem.New())
		seedLakeBucket(t, st)
		r := newReconciler(t, st, &fakeProvider{status: provider.ProviderStatus{Running: 1, Address: "10.63.0.7:8080"}}, pace)
		_, err := st.Create(ctx, mkLake("lake"))
		require.NoError(t, err)
		res, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		require.Equal(t, 200*time.Millisecond, res.RequeueAfter)
	})
	t.Run("referentPollInterval", func(t *testing.T) {
		st := store.New(storemem.New())
		r := newReconciler(t, st, &fakeProvider{}, pace)
		_, err := st.Create(ctx, mkLake("lake"))
		require.NoError(t, err)
		res, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		require.Equal(t, 100*time.Millisecond, res.RequeueAfter, "the Bucket is not applied yet")
	})
}
