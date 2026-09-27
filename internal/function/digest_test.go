package function_test

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// fakeResolver records calls and returns a fixed digest / error (ADR-0035 resolver seam).
type fakeResolver struct {
	mu     sync.Mutex
	digest string
	err    error
	calls  int
}

func (f *fakeResolver) Resolve(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.digest, f.err
}

func (f *fakeResolver) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// recordingMaterializer captures the digest it is asked to materialize (the pinned one).
type recordingMaterializer struct {
	mu   sync.Mutex
	seen string
}

func (m *recordingMaterializer) Materialize(_ context.Context, fn *v1.Function) (string, error) {
	m.mu.Lock()
	m.seen = fn.Spec.ImageDigest
	m.mu.Unlock()
	return "/tmp/x", nil
}

func (m *recordingMaterializer) digest() string { m.mu.Lock(); defer m.mu.Unlock(); return m.seen }

// digestHarness wires a reconciler with a resolver + a recording materializer over a fake
// runtime, so the resolve-and-pin path (ADR-0035) is exercised in pure Go.
type digestHarness struct {
	r   *function.Reconciler
	st  store.Store
	res *fakeResolver
	mat *recordingMaterializer
}

func newDigestHarness(t *testing.T, res *fakeResolver) *digestHarness {
	t.Helper()
	st := store.New(memory.New())
	rt := newFakeRuntime("127.0.0.1", 8080)
	sch, err := singlenode.New("local")
	require.NoError(t, err)
	mat := &recordingMaterializer{}
	r, err := function.NewReconciler(function.Deps{
		Store: st, Runtime: rt, Scheduler: sch, Gateway: embedded.New(), Validator: function.NewBasicValidator(),
		Materializer: mat, ShimCommand: []string{"node", "shim"}, Resolver: res,
	})
	require.NoError(t, err)
	return &digestHarness{r: r, st: st, res: res, mat: mat}
}

func (h *digestHarness) applyFn(t *testing.T, name, uri, digest string) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Image, fn.Spec.ImageDigest = uri, digest
	fn.Spec.Replicas = 1
	fn.Spec.Scaling = v1.Scaling{MinReplicas: 1}
	_, err := h.st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func (h *digestHarness) reconcile(t *testing.T, name string) {
	t.Helper()
	_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	require.NoError(t, err)
}

func (h *digestHarness) revisionDigest(t *testing.T, fn string, gen int) string {
	t.Helper()
	obj, err := h.st.Get(context.Background(), v1.KindRevision.GVK(), "default", v1.ObjectName(fnRev(fn, gen)))
	require.NoError(t, err)
	return obj.(*v1.Revision).Spec.ImageDigest
}

func (h *digestHarness) specDigest(t *testing.T, name string) string {
	t.Helper()
	obj, err := h.st.Get(context.Background(), v1.KindFunction.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Function).Spec.ImageDigest
}

func fnRev(name string, gen int) string { return name + "-" + strconv.Itoa(gen) }

// scenario: deploy-without-digest — an OCI ref with no digest is resolved + pinned into the
// immutable Revision, and materialization uses that pinned digest. The user's spec stays
// digest-free (the resolved digest is never written back to the Function spec).
func TestScenarioDeployWithoutDigest(t *testing.T) {
	t.Parallel()
	h := newDigestHarness(t, &fakeResolver{digest: "sha256:abc"})
	h.applyFn(t, "echo", "oci-layout://x:v1", "")
	h.reconcile(t, "echo")

	require.Equal(t, "sha256:abc", h.revisionDigest(t, "echo", 1), "the resolved digest is pinned in Revision 1")
	require.Equal(t, "sha256:abc", h.mat.digest(), "materialized with the pinned digest")
	require.Empty(t, h.specDigest(t, "echo"), "the resolved digest is NOT written back to the Function spec")
	require.Equal(t, 1, h.res.count(), "resolved exactly once")
}

// scenario: tag-move-does-not-drift — once a Revision is stamped at digest D, a later
// reconcile (even if the tag now resolves D') never re-resolves; Revision 1 keeps D.
func TestScenarioTagMoveDoesNotDrift(t *testing.T) {
	t.Parallel()
	res := &fakeResolver{digest: "sha256:D"}
	h := newDigestHarness(t, res)
	h.applyFn(t, "echo", "oci-layout://x:v1", "")
	h.reconcile(t, "echo")
	require.Equal(t, "sha256:D", h.revisionDigest(t, "echo", 1))

	res.mu.Lock()
	res.digest = "sha256:DPRIME" // the tag is re-pushed to new bytes
	res.mu.Unlock()
	h.reconcile(t, "echo")

	require.Equal(t, "sha256:D", h.revisionDigest(t, "echo", 1), "the stamped Revision never drifts")
	require.Equal(t, 1, h.res.count(), "an existing Revision is never re-resolved")
}

// scenario: explicit-digest-honored — a Function that pins its own digest is used as-is; the
// resolver is never called.
func TestScenarioExplicitDigestHonored(t *testing.T) {
	t.Parallel()
	h := newDigestHarness(t, &fakeResolver{digest: "sha256:RESOLVED"})
	h.applyFn(t, "echo", "oci-layout://x:v1", "sha256:PINNED")
	h.reconcile(t, "echo")

	require.Equal(t, "sha256:PINNED", h.revisionDigest(t, "echo", 1), "the explicit digest is honored")
	require.Equal(t, "sha256:PINNED", h.mat.digest())
	require.Zero(t, h.res.count(), "no resolution when the digest is explicit")
}

// scenario: unresolvable-ref-fails — a resolver error → Phase=Failed + ArtifactUnresolved
// (never a silent or wrong deploy, never an empty-digest materialize).
func TestScenarioUnresolvableRefFails(t *testing.T) {
	t.Parallel()
	h := newDigestHarness(t, &fakeResolver{err: fault.NotFoundf("test", "no such ref")})
	h.applyFn(t, "echo", "oci-layout://missing:v1", "")
	h.reconcile(t, "echo")

	obj, err := h.st.Get(context.Background(), v1.KindFunction.GVK(), "default", "echo")
	require.NoError(t, err)
	fn := obj.(*v1.Function)
	require.Equal(t, v1.PhaseFailed, fn.Status.Phase)
	c, ok := fn.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, "ArtifactUnresolved", c.Reason)
	require.Empty(t, h.mat.digest(), "never materialized")
}
