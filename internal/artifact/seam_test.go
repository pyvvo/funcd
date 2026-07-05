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

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/artifact"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/function"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/scheduler/singlenode"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

// scenario: materializer-satisfies-adr0030-seam (node-gated) — the OrasMaterializer is
// wired into ADR-0030's Function reconciler in place of the local-file driver; the
// reconciler pulls the pushed artifact by digest from a local OCI layout and the real Node
// shim runs it to Ready. Proves the P-V-1 + P-V-A seam composes, not just type-asserts.
func TestScenarioMaterializerSatisfiesADR0030SeamNode(t *testing.T) {
	shim, err := filepath.Abs(filepath.Join("..", "..", "shim", "nodejs", "shim.mjs"))
	require.NoError(t, err)
	if _, serr := os.Stat(shim); serr != nil {
		t.Skipf("shim not found at %s", shim)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the node-gated seam test")
	}

	// Push a real handler bundle to a local OCI layout (no registry).
	bundle := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle(_, e) { return { echoed: e }; }\n"), 0o600))
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"
	digest, err := artifact.Push(context.Background(), ref, bundle, nil, "")
	require.NoError(t, err)

	st := store.New(memory.New())
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	sch, err := singlenode.New("local")
	require.NoError(t, err)
	gw := embedded.New()
	r, err := function.NewReconciler(function.Deps{
		Store: st, Runtime: rt, Scheduler: sch, Gateway: gw, Validator: function.NewBasicValidator(),
		Materializer: artifact.NewOrasMaterializer(t.TempDir()), // the oras driver, not the file stand-in
		ShimCommand:  []string{node, shim},
	})
	require.NoError(t, err)

	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "echo", "default", "rg1"
	fn.Spec.Replicas = 1
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Artifact = v1.ArtifactRef{URI: ref, Digest: digest} // pulled by digest
	_, err = st.Create(context.Background(), fn)
	require.NoError(t, err)

	var phase v1.Phase
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, rerr := r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "echo"})
		require.NoError(t, rerr)
		got, gerr := st.Get(context.Background(), v1.KindFunction.GVK(), "default", "echo")
		require.NoError(t, gerr)
		phase = got.(*v1.Function).Status.Phase
		if phase == v1.PhaseReady {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, v1.PhaseReady, phase, "the oras-materialized artifact ran on the real shim to Ready")

	rs, err := gw.Routes(context.Background())
	require.NoError(t, err)
	require.Len(t, rs, 1)
	resp, err := http.Post(rs[0].Upstream, "application/json", strings.NewReader(`{"hello":"world"}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the shim served the oras-pulled handler over HTTP")
}
