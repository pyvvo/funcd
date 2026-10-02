package function

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// newContainerReconciler builds a container-mode reconciler (EndpointNetnsFixedPort) so workerSpec
// takes the ADR-0032 bind-mount branch and we can assert its bundle env.
func newContainerReconciler(t *testing.T) *Reconciler {
	t.Helper()
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	r, err := NewReconciler(Deps{
		Store: store.New(memory.New()), Runtime: rt, Scheduler: sch,
		Gateway: embedded.New(), Validator: NewBasicValidator(),
		Materializer: fakeMat{}, ShimCommand: []string{"node", "shim.mjs"},
		EndpointMode: EndpointNetnsFixedPort,
		ImageFor:     func(rtName string) string { return "funcd/runtime-" + rtName + ":latest" },
	})
	require.NoError(t, err)
	return r
}

func pythonFn() *v1.Function {
	fn := sampleFn()
	fn.Spec.Runtime, fn.Spec.Handler = "python314", "handle"
	return fn
}

// scenario: bundle-sets-pythonpath-and-bundledir (process mode) — a python-family function gets
// FUNCD_BUNDLE_DIR + PYTHONPATH both pointing at the artifact dir (ADR-0089).
func TestScenarioBundleEnvProcessMode(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	spec := mustWorkerSpec(t, r, pythonFn(), "/art/bundle/handler.py", nil, nil)

	require.Equal(t, "/art/bundle", spec.Env["FUNCD_BUNDLE_DIR"], "the bundle root is Dir(artifactPath)")
	require.Equal(t, "/art/bundle", spec.Env["PYTHONPATH"], "python family imports vendored deps via PYTHONPATH")
}

// scenario: bundle-sets-pythonpath-and-bundledir (container mode) — same env, rooted at the
// in-container artifact dir (ADR-0032 bind mount).
func TestScenarioBundleEnvContainerMode(t *testing.T) {
	t.Parallel()
	r := newContainerReconciler(t)
	spec := mustWorkerSpec(t, r, pythonFn(), "/art/bundle/handler.py", nil, nil)

	require.Equal(t, containerArtifactDir, spec.Env["FUNCD_BUNDLE_DIR"], "container bundle root == the bind-mount target")
	require.Equal(t, containerArtifactDir, spec.Env["PYTHONPATH"])
}

// scenario: non-python runtime → FUNCD_BUNDLE_DIR set, PYTHONPATH ABSENT (PYTHONPATH is python-family only).
func TestScenarioBundleEnvNonPythonNoPythonpath(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	spec := mustWorkerSpec(t, r, sampleFn(), "/art/app.mjs", nil, nil) // sampleFn is nodejs22

	require.Equal(t, "/art", spec.Env["FUNCD_BUNDLE_DIR"], "FUNCD_BUNDLE_DIR is generic (any runtime)")
	_, hasPy := spec.Env["PYTHONPATH"]
	require.False(t, hasPy, "a non-python runtime gets no PYTHONPATH")
}

// scenario: single-file-no-env-regression — a single-file function gets the same bundle env
// harmlessly (the dir just holds the one file), and the reserved FUNCD_ keys still resolve.
func TestScenarioSingleFileBundleEnvHarmless(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	spec := mustWorkerSpec(t, r, sampleFn(), "/art/app.mjs", nil, nil)

	require.Equal(t, "/art/app.mjs", spec.Env["FUNCD_ARTIFACT"], "FUNCD_ARTIFACT unchanged for a single file")
	require.Equal(t, "app.handler", spec.Env["FUNCD_HANDLER"])
	require.Equal(t, "/art", spec.Env["FUNCD_BUNDLE_DIR"], "the bundle env is set but harmless")
}

// scenario: contract-env-process-mode (ADR-0123) — a delivered contract file in the bundle root
// sets FUNCD_CONTRACT_PATH to the host path so the shim compiles the schema at warm-up.
func TestScenarioContractEnvProcessMode(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".funcd-contract.json"), []byte(`{"input":{},"output":{}}`), 0o600))
	art := filepath.Join(root, "app.mjs")

	spec := mustWorkerSpec(t, r, sampleFn(), art, nil, nil)
	require.Equal(t, filepath.Join(root, ".funcd-contract.json"), spec.Env["FUNCD_CONTRACT_PATH"],
		"process mode points FUNCD_CONTRACT_PATH at the delivered host sidecar")
}

// scenario: contract-env-container-mode (ADR-0123) — the same delivery under the bind-mount target.
func TestScenarioContractEnvContainerMode(t *testing.T) {
	t.Parallel()
	r := newContainerReconciler(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "__funcd_contract.json"), []byte(`{"input":{},"output":{}}`), 0o600))
	art := filepath.Join(root, "handler.py")

	spec := mustWorkerSpec(t, r, pythonFn(), art, nil, nil)
	require.Equal(t, filepath.Join(containerArtifactDir, "__funcd_contract.json"), spec.Env["FUNCD_CONTRACT_PATH"],
		"container mode roots FUNCD_CONTRACT_PATH at the bind-mount target")
}

// scenario: no-contract-no-env — with no delivered contract file the env stays unset (dev/legacy),
// so the shim's fail-closed path fires only for a contracted function whose schema truly went missing.
func TestScenarioNoContractNoEnv(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	spec := mustWorkerSpec(t, r, sampleFn(), filepath.Join(t.TempDir(), "app.mjs"), nil, nil)
	_, has := spec.Env["FUNCD_CONTRACT_PATH"]
	require.False(t, has, "no delivered contract → FUNCD_CONTRACT_PATH unset")
}
