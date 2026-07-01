package function

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/scheduler/singlenode"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

// newContainerReconciler builds a container-mode reconciler (EndpointNetnsFixedPort) so workerSpec
// takes the ADR-0032 bind-mount branch and we can assert its bundle env.
func newContainerReconciler(t *testing.T) *Reconciler {
	t.Helper()
	sch, err := singlenode.New("local")
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
	spec := r.workerSpec(pythonFn(), 0, "/art/bundle/handler.py", nil)

	require.Equal(t, "/art/bundle", spec.Env["FUNCD_BUNDLE_DIR"], "the bundle root is Dir(artifactPath)")
	require.Equal(t, "/art/bundle", spec.Env["PYTHONPATH"], "python family imports vendored deps via PYTHONPATH")
}

// scenario: bundle-sets-pythonpath-and-bundledir (container mode) — same env, rooted at the
// in-container artifact dir (ADR-0032 bind mount).
func TestScenarioBundleEnvContainerMode(t *testing.T) {
	t.Parallel()
	r := newContainerReconciler(t)
	spec := r.workerSpec(pythonFn(), 0, "/art/bundle/handler.py", nil)

	require.Equal(t, containerArtifactDir, spec.Env["FUNCD_BUNDLE_DIR"], "container bundle root == the bind-mount target")
	require.Equal(t, containerArtifactDir, spec.Env["PYTHONPATH"])
}

// scenario: non-python runtime → FUNCD_BUNDLE_DIR set, PYTHONPATH ABSENT (PYTHONPATH is python-family only).
func TestScenarioBundleEnvNonPythonNoPythonpath(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	spec := r.workerSpec(sampleFn(), 0, "/art/app.mjs", nil) // sampleFn is nodejs22

	require.Equal(t, "/art", spec.Env["FUNCD_BUNDLE_DIR"], "FUNCD_BUNDLE_DIR is generic (any runtime)")
	_, hasPy := spec.Env["PYTHONPATH"]
	require.False(t, hasPy, "a non-python runtime gets no PYTHONPATH")
}

// scenario: single-file-no-env-regression — a single-file function gets the same bundle env
// harmlessly (the dir just holds the one file), and the reserved FUNCD_ keys still resolve.
func TestScenarioSingleFileBundleEnvHarmless(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	spec := r.workerSpec(sampleFn(), 0, "/art/app.mjs", nil)

	require.Equal(t, "/art/app.mjs", spec.Env["FUNCD_ARTIFACT"], "FUNCD_ARTIFACT unchanged for a single file")
	require.Equal(t, "app.handler", spec.Env["FUNCD_HANDLER"])
	require.Equal(t, "/art", spec.Env["FUNCD_BUNDLE_DIR"], "the bundle env is set but harmless")
}
