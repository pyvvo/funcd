package function_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// scenario: recreated-function-retires-stale-workers — fn (handler A) is deleted and its Revision collected, then fn
// is re-applied (handler B) before its reconciler sees the delete: the pass stamps fn-1 afresh, retires the image-A
// worker labelled fn-1 first, and every call is served by the handler-B worker.
func TestScenarioRecreatedFunctionRetiresStaleWorkers(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.deployReady(t, "echo")
	ctx := context.Background()
	require.NoError(t, h.st.Delete(ctx, v1.KindFunction.GVK(), "default", "echo", ""))
	require.NoError(t, h.st.Delete(ctx, v1.KindRevision.GVK(), "default", "echo-1", ""), "the collector deletes the Revision")
	h.create(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleNEW" })
	h.reconcile(t, "echo")

	id := runtime.NewInstanceID("default", "echo", "echo-1", 0)
	require.True(t, h.rt.wasRemoved(id), "the deleted Function's worker left the runtime")
	require.Equal(t, "handleNEW", h.rt.specOf(id).Env["FUNCD_HANDLER"], "the replica runs the re-created spec")
	obj, err := h.st.Get(ctx, v1.KindRevision.GVK(), "default", "echo-1")
	require.NoError(t, err)
	require.Equal(t, "handleNEW", obj.(*v1.Revision).Spec.Handler)

	creates, _ := h.rt.counts()
	for range 3 {
		h.reconcile(t, "echo")
	}
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "the next passes keep the new worker")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "echo").Status.Phase)
}
