package function

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/envresolve"
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
	rt := process.New(nil)
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
	spec := mustWorkerSpec(t, r, pythonFn(), "/art/bundle/handler.py", envresolve.Resolved{}, nil)

	require.Equal(t, "/art/bundle", spec.Env["FUNCD_BUNDLE_DIR"], "the bundle root is Dir(artifactPath)")
	require.Equal(t, "/art/bundle", spec.Env["PYTHONPATH"], "python family imports vendored deps via PYTHONPATH")
}

// scenario: bundle-sets-pythonpath-and-bundledir (container mode) — same env, rooted at the
// in-container artifact dir (ADR-0032 bind mount).
func TestScenarioBundleEnvContainerMode(t *testing.T) {
	t.Parallel()
	r := newContainerReconciler(t)
	spec := mustWorkerSpec(t, r, pythonFn(), "/art/bundle/handler.py", envresolve.Resolved{}, nil)

	require.Equal(t, containerArtifactDir, spec.Env["FUNCD_BUNDLE_DIR"], "container bundle root == the bind-mount target")
	require.Equal(t, containerArtifactDir, spec.Env["PYTHONPATH"])
}

// scenario: non-python runtime → FUNCD_BUNDLE_DIR set, PYTHONPATH ABSENT (PYTHONPATH is python-family only).
func TestScenarioBundleEnvNonPythonNoPythonpath(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	spec := mustWorkerSpec(t, r, sampleFn(), "/art/app.mjs", envresolve.Resolved{}, nil) // sampleFn is nodejs22

	require.Equal(t, "/art", spec.Env["FUNCD_BUNDLE_DIR"], "FUNCD_BUNDLE_DIR is generic (any runtime)")
	_, hasPy := spec.Env["PYTHONPATH"]
	require.False(t, hasPy, "a non-python runtime gets no PYTHONPATH")
}

// scenario: single-file-no-env-regression — a single-file function gets the same bundle env
// harmlessly (the dir just holds the one file), and the reserved FUNCD_ keys still resolve.
func TestScenarioSingleFileBundleEnvHarmless(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	spec := mustWorkerSpec(t, r, sampleFn(), "/art/app.mjs", envresolve.Resolved{}, nil)

	require.Equal(t, "/art/app.mjs", spec.Env["FUNCD_ARTIFACT"], "FUNCD_ARTIFACT unchanged for a single file")
	require.Equal(t, "app.handler", spec.Env["FUNCD_HANDLER"])
	require.Equal(t, "/art", spec.Env["FUNCD_BUNDLE_DIR"], "the bundle env is set but harmless")
}

// scenario: funcd-names-no-contract (solo) — with a delivered contract in the bundle root, a solo worker's env has no
// FUNCD_CONTRACT_PATH in process or in container mode, and funcd writes no open contract beside the blob (ADR-0304).
func TestScenarioFuncdNamesNoContract(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		r                  *Reconciler
		fn                 *v1.Function
		contract, artifact string
	}{
		"process":   {newShimReconciler(t, nil), sampleFn(), ".funcd-contract.json", "app.mjs"},
		"container": {newContainerReconciler(t), pythonFn(), "__funcd_contract.json", "handler.py"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, tc.contract), []byte(`{"input":{},"output":{}}`), 0o600))
			spec := mustWorkerSpec(t, tc.r, tc.fn, filepath.Join(root, tc.artifact), envresolve.Resolved{}, nil)
			_, has := spec.Env["FUNCD_CONTRACT_PATH"]
			require.False(t, has, "the worker's env names no contract")
			require.NoFileExists(t, filepath.Join(root, ".funcd-contract-open.json"), "funcd writes no open contract")
		})
	}
}
