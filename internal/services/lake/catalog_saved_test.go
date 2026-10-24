package lake_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
)

// catalogSavedRig is one Lake over a fake provider whose ready engine reports report.
type catalogSavedRig struct {
	st   store.Store
	prov *fakeProvider
}

func newCatalogSavedRig(t *testing.T) *catalogSavedRig {
	t.Helper()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	_, err := st.Create(context.Background(), mkLake("lake"))
	require.NoError(t, err)
	return &catalogSavedRig{st: st, prov: &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}}
}

// pass reconciles once with the engine reporting report (none when ready is false) and returns the stored Lake.
func (g *catalogSavedRig) pass(t *testing.T, report string, ready bool) *v1.Lake {
	t.Helper()
	g.prov.status.Report, g.prov.status.Ready = report, ready
	reconcileOnce(t, newReconciler(t, g.st, g.prov, nil), "lake")
	obj, err := g.st.Get(context.Background(), v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	return obj.(*v1.Lake)
}

func catalogSaved(t *testing.T, cs *v1.Lake) v1.Condition {
	t.Helper()
	c, ok := cs.Status.Conditions.Get("CatalogSaved")
	require.True(t, ok, "the Lake has a CatalogSaved condition")
	return c
}

// scenario: failing-saves-show-on-the-lake (ADR-0276) — a failing report sets CatalogSaved False/SavesFailing with the
// report as message, while the Lake stays Ready.
func TestScenarioFailingSavesShowOnTheLake(t *testing.T) {
	t.Parallel()
	g := newCatalogSavedRig(t)
	const failing = "failing since 2026-10-24T05:00:00+00:00: An error occurred (AccessDenied) when calling the PutObject operation"
	cs := g.pass(t, failing, true)
	c := catalogSaved(t, cs)
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "SavesFailing", c.Reason)
	require.Equal(t, failing, c.Message)
	require.Equal(t, cs.Generation, c.ObservedGeneration)
	require.Equal(t, v1.PhaseReady, cs.Status.Phase, "failing saves never change the phase")
	ready, ok := cs.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionTrue, ready.Status)
}

// scenario: saves-recover (ADR-0276) — a saved report turns CatalogSaved True/Saved.
func TestScenarioSavesRecover(t *testing.T) {
	t.Parallel()
	g := newCatalogSavedRig(t)
	g.pass(t, "failing since 2026-10-24T05:00:00+00:00: refused", true)
	c := catalogSaved(t, g.pass(t, "saved 2026-10-24T05:00:03+00:00", true))
	require.Equal(t, v1.ConditionTrue, c.Status)
	require.Equal(t, "Saved", c.Reason)
}

// scenario: replaced-engine-keeps-the-failure (ADR-0276) — while a replaced engine starts, with no report or no Ready
// engine, and on content that is no report, the condition keeps its value: no recovery is read without a save.
func TestScenarioReplacedEngineKeepsTheFailure(t *testing.T) {
	t.Parallel()
	g := newCatalogSavedRig(t)
	require.Equal(t, v1.ConditionFalse, catalogSaved(t, g.pass(t, "failing since 2026-10-24T05:00:00+00:00: refused", true)).Status)
	for _, step := range []struct {
		report string
		ready  bool
	}{{"", false}, {"", true}, {"garbage written by the sandbox", true}, {"failing since 2026-10-24T05:00:09+00:00: refused", true}} {
		require.Equal(t, v1.ConditionFalse, catalogSaved(t, g.pass(t, step.report, step.ready)).Status,
			"report %q (ready %v) must not read as a recovery", step.report, step.ready)
	}
}

// A Lake whose engine never reported has no CatalogSaved condition: nothing is claimed before a save.
func TestCatalogSavedNeedsAReport(t *testing.T) {
	t.Parallel()
	g := newCatalogSavedRig(t)
	_, ok := g.pass(t, "", true).Status.Conditions.Get("CatalogSaved")
	require.False(t, ok)
}
