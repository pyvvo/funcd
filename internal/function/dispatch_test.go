package function_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// scenario: runtime-selects-shim — one daemon, two curated languages (ADR-0049). The reconciler
// launches a python* function with the python shim and a nodejs* function with the node shim,
// selecting by fn.Spec.Runtime family; everything else about the worker (artifact/handler env,
// portfile, readiness) is identical. Proven by the WorkerSpec.Command the runtime is asked to run.
func TestScenarioRuntimeSelectsShim(t *testing.T) {
	nodeCmd := []string{"node", "/opt/funcd/shim.mjs"}
	pyCmd := []string{"python3", "/opt/funcd/shim-python/funcd_shim_entry.py"}

	st := store.New(memory.New())
	rt := newFakeRuntime("127.0.0.1", 1) // endpoint irrelevant — we assert the recorded spec, not readiness
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	r, err := function.NewReconciler(function.Deps{
		Store: st, Runtime: rt, Scheduler: sch, Gateway: embedded.New(),
		Validator:            function.NewBasicValidator(),
		Materializer:         function.NewFileMaterializer(),
		ShimCommand:          nodeCmd,
		ShimCommandsByFamily: map[string][]string{"python": pyCmd},
	})
	require.NoError(t, err)

	provision := func(name, runtimeName, ext string) {
		art := filepath.Join(t.TempDir(), "handler"+ext)
		require.NoError(t, os.WriteFile(art, []byte("handler"), 0o600))
		obj, ok := v1.NewObject(v1.KindFunction)
		require.True(t, ok)
		fn, ok := obj.(*v1.Function)
		require.True(t, ok)
		fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
		fn.Spec.Replicas = 1
		fn.Spec.Runtime = v1.RuntimeName(runtimeName)
		fn.Spec.Handler = "handle"
		fn.Spec.Image = "file://" + art
		_, cerr := st.Create(context.Background(), fn)
		require.NoError(t, cerr)
		// One reconcile provisions the worker (records the WorkerSpec); readiness then requeues.
		_, _ = r.Reconcile(context.Background(),
			controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	}

	provision("node-fn", "nodejs22", ".mjs")
	provision("py-fn", "python312", ".py")

	nodeSpec, ok := rt.specFor("node-fn")
	require.True(t, ok, "node worker provisioned")
	require.True(t, slices.Equal(nodeSpec.Command, nodeCmd),
		"node-fn must launch with the node shim, got %v", nodeSpec.Command)

	pySpec, ok := rt.specFor("py-fn")
	require.True(t, ok, "python worker provisioned")
	require.True(t, slices.Equal(pySpec.Command, pyCmd),
		"py-fn must launch with the python shim, got %v", pySpec.Command)
}

// Issue #371: with no python shim registered, a python Function is not launched under the node shim, solo or pooled:
// it is Failed with RuntimeUnavailable, which names the runtime, and no worker is created.
func TestIssue371_PythonFunctionWithoutPythonShimIsRuntimeUnavailable(t *testing.T) {
	t.Parallel()
	for name, worker := range map[string]string{"solo": "", "pooled": "w1"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, func(d *function.Deps) {
				d.PoolShimCommand = []string{"node", "/opt/funcd/pool.mjs"}
			})
			h.create(t, "py-fn", func(fn *v1.Function) {
				fn.Spec.Runtime = "python314"
				fn.Spec.Pooling.Worker = worker
			})
			h.reconcile(t, "py-fn")

			require.Equal(t, v1.PhaseFailed, h.getFn(t, "py-fn").Status.Phase)
			ready := h.condition(t, "py-fn", "Ready")
			require.Equal(t, "RuntimeUnavailable", ready.Reason)
			require.Equal(t, `runtime "python314" is not available on this node: no shim is registered for it`, ready.Message)
			creates, _ := h.rt.counts()
			require.Zero(t, creates, "no worker is created under another language's shim")
		})
	}
}
