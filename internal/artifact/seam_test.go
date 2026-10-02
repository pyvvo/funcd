package artifact_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
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

// scenario: materializer-satisfies-adr0030-seam (node-gated) — the OrasMaterializer is
// wired into ADR-0030's Function reconciler in place of the local-file driver; the
// reconciler pulls the pushed artifact by digest from a local OCI layout and the real Node
// shim runs it to Ready. Proves the P-V-1 + P-V-A seam composes, not just type-asserts.
func TestScenarioMaterializerSatisfiesADR0030SeamNode(t *testing.T) {
	st, r, gw := seamFunction(t, "export function handle(_, e) { return { echoed: e }; }\n")
	require.Equal(t, v1.PhaseReady, reconcileUntilSettled(t, r, st), "the oras-materialized artifact ran on the real shim to Ready")

	rs, err := gw.Routes(context.Background())
	require.NoError(t, err)
	require.Len(t, rs, 1)
	resp, err := http.Post(rs[0].Upstream, "application/json", strings.NewReader(`{"hello":"world"}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the shim served the oras-pulled handler over HTTP")
}

// A shim that takes longer to boot than the seam test's former fixed 5 s wait, as on a loaded CI runner, still
// reaches Ready: the wait ends on the reconciler's verdict, not on a deadline of the test's own.
func TestIssue393_SeamWaitsOutASlowShimBoot(t *testing.T) {
	st, r, _ := seamFunction(t, "await new Promise((resolve) => setTimeout(resolve, 6000));\nexport function handle() {}\n")
	require.Equal(t, v1.PhaseReady, reconcileUntilSettled(t, r, st), "a shim that boots in 6 s reached Ready")
}

// seamFunction pushes handler src to a local OCI layout (no registry) and creates Function "echo", pulled by digest,
// for a reconciler that materializes it with the OrasMaterializer and runs it on the real Node shim.
func seamFunction(t *testing.T, src string) (store.Store, *function.Reconciler, gateway.Gateway) {
	t.Helper()
	shim := langmod.NodeShim(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the node-gated seam test")
	}

	bundle := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(bundle, []byte(src), 0o600))
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"
	digest, err := artifact.Push(context.Background(), ref, bundle, nil, "", "")
	require.NoError(t, err)

	st := store.New(memory.New())
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	gw := embedded.New()
	r, err := function.NewReconciler(function.Deps{
		Store: st, Runtime: rt, Scheduler: sch, Gateway: gw, Validator: function.NewBasicValidator(),
		Materializer: artifact.NewOrasMaterializer(t.TempDir(), ""),
		ShimCommand:  []string{node, shim},
	})
	require.NoError(t, err)

	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "echo", "default", "rg1"
	fn.Spec.Replicas = 1
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Image, fn.Spec.ImageDigest = ref, digest
	_, err = st.Create(context.Background(), fn)
	require.NoError(t, err)
	return st, r, gw
}

// reconcileUntilSettled reconciles Function "echo" until it leaves Deploying and returns its phase. The reconciler
// bounds a boot itself (ADR-0030 §4b: a replica not ready within its boot timeout fails), so the wait sets no deadline
// of its own for a loaded runner to outlast (issue #393).
func reconcileUntilSettled(t *testing.T, r *function.Reconciler, st store.Store) v1.Phase {
	t.Helper()
	for {
		_, err := r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "echo"})
		require.NoError(t, err)
		got, err := st.Get(context.Background(), v1.KindFunction.GVK(), "default", "echo")
		require.NoError(t, err)
		if phase := got.(*v1.Function).Status.Phase; phase != v1.PhaseDeploying {
			return phase
		}
		time.Sleep(50 * time.Millisecond)
	}
}
