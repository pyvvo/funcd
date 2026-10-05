package function_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/store"
)

// holdRun stores an open WorkflowRun whose status pins revision rev of fn (ADR-0190).
func holdRun(t *testing.T, st store.Store, name string, fn *v1.Function, rev v1.ObjectName) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindWorkflowRun)
	require.True(t, ok)
	run := obj.(*v1.WorkflowRun)
	run.Name, run.Namespace, run.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	run.Spec.Workflow = "flow"
	created, err := st.Create(context.Background(), run)
	require.NoError(t, err)
	run = created.(*v1.WorkflowRun)
	run.Status.Phase = v1.RunRunning
	run.Status.Pins = []v1.RevisionPin{{Function: fn.Name, FunctionUID: fn.UID, Revision: rev}}
	_, err = st.Update(context.Background(), run)
	require.NoError(t, err)
}

func cancelRun(t *testing.T, st store.Store, name string) {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindWorkflowRun.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	run := obj.(*v1.WorkflowRun)
	run.Spec.Cancel = true
	_, err = st.Update(context.Background(), run)
	require.NoError(t, err)
}

func pinnedUpstream(h *shimHarness, fn *v1.Function, rev v1.ObjectName) (string, bool, error) {
	return h.r.Endpoints().Upstream(context.Background(), activator.FunctionRef{Namespace: "default", Name: fn.Name, Revision: rev, UID: fn.UID})
}

// scenario: cancel-releases-revision (ADR-0190) — a revision an open run holds keeps its worker after a redeploy and
// serves the run's pinned calls; once the run is cancelled the next pass retires that worker, and the Revision stays.
func TestScenarioCancelReleasesRevision(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	url1, _ := h.rt.serveRevision(t, "echo-1", http.StatusOK)
	h.deployReady(t, "echo")
	holdRun(t, h.st, "r1", h.getFn(t, "echo"), "echo-1")

	url2, _ := h.rt.serveRevision(t, "echo-2", http.StatusOK)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	h.reconcile(t, "echo")
	settle()
	h.reconcile(t, "echo")
	fn := h.getFn(t, "echo")
	require.Equal(t, "echo-2", fn.Status.ServingRevision)
	require.Empty(t, fn.Status.DrainingRevision, "a held revision does not block the next switch")
	require.Contains(t, h.rt.revisionStates("echo"), v1.ObjectName("echo-1"), "the held revision keeps its worker")
	up, ready, err := pinnedUpstream(h, fn, "echo-1")
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, url1, up, "a pinned call gets its revision")
	up, _ = h.upstream(t, "echo")
	require.Equal(t, url2, up, "an unpinned call gets the serving revision")
	h.reconcile(t, "echo")
	require.Contains(t, h.rt.revisionStates("echo"), v1.ObjectName("echo-1"), "the steady state does not skip a held revision")

	cancelRun(t, h.st, "r1")
	h.reconcile(t, "echo")
	require.NotContains(t, h.rt.revisionStates("echo"), v1.ObjectName("echo-1"), "the next pass retires the released revision")
	_, err = h.st.Get(context.Background(), v1.KindRevision.GVK(), "default", "echo-1")
	require.NoError(t, err, "the Revision object stays")
}

// A pinned ref never falls back to the serving revision: a Function re-created under another UID, or a Revision
// that is gone, is fault.NotFound naming the pin (ADR-0190 Decision 4).
func TestUpstreamPinnedRevisionGone(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.rt.serveRevision(t, "echo-1", http.StatusOK)
	h.deployReady(t, "echo")
	fn := h.getFn(t, "echo")

	stale := *fn
	stale.UID = "another-uid"
	_, ready, err := pinnedUpstream(h, &stale, "echo-1")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.False(t, ready)
	require.Contains(t, err.Error(), `pinned revision "echo-1"`)

	_, _, err = pinnedUpstream(h, fn, "echo-9")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.Contains(t, err.Error(), `pinned revision "echo-9"`)

	require.NoError(t, h.st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "echo", ""))
	_, _, err = pinnedUpstream(h, fn, "echo-1")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "a deleted Function fails its pinned calls")
}
