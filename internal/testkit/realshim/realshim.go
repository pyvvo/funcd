// Package realshim brings the real Node shim up under a function.Reconciler for the node-gated tests (ADR-0030 node
// lane). It is the one copy of that setup, so a fix to it reaches every package that runs the shim (issue #506).
package realshim

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
)

// Ready creates Function "echo" (nodejs22, handler handle, 1 replica) on image, pinned to digest when it is set, for
// a reconciler that materializes it with mat and runs it on the real Node shim through the process driver. It
// reconciles until the Function reports Ready and returns the gateway that routes to it. It skips the test when node
// is not on PATH.
func Ready(t testing.TB, mat function.Materializer, image, digest string) gateway.Gateway {
	t.Helper()
	shim := langmod.NodeShim(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the node-gated shim lane")
	}

	st := store.New(memory.New())
	rt := process.New(nil)
	t.Cleanup(func() { _ = rt.Close() })
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	gw := embedded.New()
	r, err := function.NewReconciler(function.Deps{
		Store: st, Runtime: rt, Scheduler: sch, Gateway: gw, Validator: function.NewBasicValidator(),
		Materializer: mat,
		ShimCommand:  []string{node, shim},
	})
	require.NoError(t, err)

	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "echo", "default", "rg1"
	fn.Spec.Replicas = 1
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Image, fn.Spec.ImageDigest = image, digest
	_, err = st.Create(context.Background(), fn)
	require.NoError(t, err)

	// The reconciler fails a replica not ready within its boot timeout (ADR-0030 §4b), so the wait sets no deadline
	// of its own for a loaded runner to outlast (issues #393 and #452).
	req := controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "echo"}
	for {
		_, err = r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		got, err := st.Get(context.Background(), v1.KindFunction.GVK(), "default", "echo")
		require.NoError(t, err)
		if phase := got.(*v1.Function).Status.Phase; phase != v1.PhaseDeploying {
			require.Equal(t, v1.PhaseReady, phase, "the real Node shim booted and reported readiness")
			return gw
		}
		time.Sleep(50 * time.Millisecond)
	}
}
