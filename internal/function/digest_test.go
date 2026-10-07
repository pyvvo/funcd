package function_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime"
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

// moveTo moves the tag to digest d.
func (f *fakeResolver) moveTo(d string) { f.mu.Lock(); defer f.mu.Unlock(); f.digest = d }

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
	sch, err := singlenode.New("local", v1.HostPlatform())
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

// Issue 703: a registry error at Revision stamp fails a Function that runs no worker as ArtifactUnresolved (ADR-0035),
// and the gate is re-checked every supervision period, so the Function becomes Ready once the registry is back.
func TestIssue703_TransientResolveErrorIsRetried(t *testing.T) {
	t.Parallel()
	down := fault.Unavailablef("oras.Resolve", "dial tcp registry:443: connect: connection refused")
	t.Run("pass", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch, pinning(&fakeResolver{err: down}, &pinRecorder{}))
		h.create(t, "greeter", func(*v1.Function) {})
		got := h.reconcile(t, "greeter")
		require.Equal(t, v1.PhaseFailed, h.getFn(t, "greeter").Status.Phase)
		h.requireCondition(t, "greeter", "Ready", v1.ConditionFalse, "ArtifactUnresolved")
		require.Equal(t, testPeriod, got.RequeueAfter, "the gate is re-checked every supervision period")
	})
	t.Run("controller", func(t *testing.T) {
		t.Parallel()
		res := &fakeResolver{err: down}
		h := newShimHarness(t, http.StatusOK, false, withSwitch, pinning(res, &pinRecorder{}))
		ctrl, err := controller.New(controller.Deps{Store: h.st})
		require.NoError(t, err)
		ctrl.Register(v1.KindFunction.GVK(), h.r)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- ctrl.Run(ctx) }()
		t.Cleanup(func() { cancel(); <-done })

		h.create(t, "greeter", func(*v1.Function) {})
		require.Eventually(t, func() bool {
			c, ok := h.getFn(t, "greeter").Status.Conditions.Get("Ready")
			return ok && c.Reason == "ArtifactUnresolved"
		}, 5*time.Second, 10*time.Millisecond)
		res.mu.Lock()
		res.err, res.digest = nil, "sha256:A"
		res.mu.Unlock()
		require.Eventually(t, func() bool {
			return h.getFn(t, "greeter").Status.Phase == v1.PhaseReady
		}, 5*time.Second, 10*time.Millisecond, "the Function recovers without a re-apply")
		rev, err := h.revision(t, "greeter-1")
		require.NoError(t, err)
		require.Equal(t, "sha256:A", rev.Spec.ImageDigest)
	})
}

// Issue 14: a Function deleted and then applied again under its name restarts at generation 1, the deleted Function's
// revision 1 still in the store. The re-created Function's revision 1 pins the artifact its own manifest names.
func TestIssue14_RecreatedFunctionRunsItsOwnArtifact(t *testing.T) {
	t.Parallel()
	res := &fakeResolver{digest: "sha256:V1"}
	h := newDigestHarness(t, res)
	h.applyFn(t, "greeter", "oci-layout://x:greeter", "")
	h.reconcile(t, "greeter")
	require.Equal(t, "sha256:V1", h.revisionDigest(t, "greeter", 1))

	require.NoError(t, h.st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "greeter", ""))
	h.reconcile(t, "greeter")

	res.mu.Lock()
	res.digest = "sha256:V2"
	res.mu.Unlock()
	h.applyFn(t, "greeter", "oci-layout://x:greeter-v2", "")
	h.reconcile(t, "greeter")

	obj, err := h.st.Get(context.Background(), v1.KindRevision.GVK(), "default", "greeter-1")
	require.NoError(t, err)
	rev := obj.(*v1.Revision)
	require.Equal(t, "oci-layout://x:greeter-v2", rev.Spec.Image, "revision 1 is the re-created Function's")
	require.Equal(t, "sha256:V2", rev.Spec.ImageDigest, "revision 1 pins the re-created Function's artifact")
	require.Equal(t, "sha256:V2", h.mat.digest(), "the worker runs the re-created Function's artifact")
	obj, err = h.st.Get(context.Background(), v1.KindFunction.GVK(), "default", "greeter")
	require.NoError(t, err)
	require.Equal(t, "greeter-1", obj.(*v1.Function).Status.CurrentRevision)
}

// pinRecorder materializes through the harness's materializer and records every digest it is asked for.
type pinRecorder struct {
	next function.Materializer
	mu   sync.Mutex
	seen []string
}

func (m *pinRecorder) Materialize(ctx context.Context, fn *v1.Function) (string, error) {
	m.mu.Lock()
	m.seen = append(m.seen, fn.Spec.ImageDigest)
	m.mu.Unlock()
	return m.next.Materialize(ctx, fn)
}

func (m *pinRecorder) digests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.seen)
}

// pinning makes a shim harness resolve tags with res and record each digest it materializes in rec.
func pinning(res *fakeResolver, rec *pinRecorder) func(*function.Deps) {
	return func(d *function.Deps) {
		d.Resolver = res
		rec.next = d.Materializer
		d.Materializer = rec
	}
}

// revisionHashed is ADR-0172's shortened Revision name: name cut to keep, 8 hex digits of SHA-256 of name, gen.
func revisionHashed(name string, keep int, gen string) string {
	sum := sha256.Sum256([]byte(name))
	return name[:keep] + "-" + hex.EncodeToString(sum[:])[:8] + "-" + gen
}

// bumpTimeout changes name's spec n times, so its generation grows by n.
func (h *shimHarness) bumpTimeout(t *testing.T, name string, n int) {
	t.Helper()
	for range n {
		h.apply(t, name, func(fn *v1.Function) { fn.Spec.Timeout += v1.Duration(time.Second) })
	}
}

func (h *shimHarness) revision(t *testing.T, name string) (*v1.Revision, error) {
	t.Helper()
	obj, err := h.st.Get(context.Background(), v1.KindRevision.GVK(), "default", v1.ObjectName(name))
	if err != nil {
		return nil, err
	}
	return obj.(*v1.Revision), nil
}

// failMissingRevision deploys greeter at sha256:A, deletes greeter-1 through the store, moves the tag to sha256:B,
// stops the worker and reconciles.
func failMissingRevision(t *testing.T) (*shimHarness, *fakeResolver, *pinRecorder, controller.Result) {
	t.Helper()
	res, rec := &fakeResolver{digest: "sha256:A"}, &pinRecorder{}
	h := newShimHarness(t, http.StatusOK, false, withSwitch, pinning(res, rec))
	h.create(t, "greeter", func(*v1.Function) {})
	h.reconcile(t, "greeter")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "greeter").Status.Phase)
	require.NoError(t, h.st.Delete(context.Background(), v1.KindRevision.GVK(), "default", "greeter-1", ""))
	res.moveTo("sha256:B")
	h.rt.exit("greeter", runtime.StateFailed, time.Hour)
	return h, res, rec, h.reconcile(t, "greeter")
}

// scenario: missing-revision-fails-closed (ADR-0172).
func TestScenarioMissingRevisionFailsClosed(t *testing.T) {
	t.Parallel()
	h, res, rec, result := failMissingRevision(t)

	require.Equal(t, v1.PhaseFailed, h.getFn(t, "greeter").Status.Phase)
	require.Equal(t, testPeriod, result.RequeueAfter, "the gate is retried at the supervision period")
	for _, typ := range []v1.ConditionType{"Ready", "RevisionReady"} {
		c := h.condition(t, "greeter", typ)
		require.Equal(t, v1.ConditionFalse, c.Status, typ)
		require.Equal(t, "RevisionMissing", c.Reason, typ)
	}
	require.Equal(t, 1, res.count(), "nothing is resolved")
	_, err := h.revision(t, "greeter-1")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "nothing is created")
	for _, byReplica := range h.rt.revisionStates("greeter") {
		for _, st := range byReplica {
			require.True(t, st.Terminal(), "no worker runs")
		}
	}
	require.Empty(t, h.routes(t), "no route")
	require.NotContains(t, rec.digests(), "sha256:B")
}

// A missing Revision is a gate failure like any other (ADR-0161 Decision 2): the serving revision's listening workers
// keep a Ready Function Ready with their count, a running one keeps it Degraded, and none is replaced.
func TestRevisionMissingCountsServingWorkers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		replicas int
		held     int
		phase    v1.Phase
		ready    v1.ConditionStatus
		want     int
	}{
		{"listening", 2, 1, v1.PhaseReady, v1.ConditionTrue, 1},
		{"running-not-listening", 1, 0, v1.PhaseDegraded, v1.ConditionFalse, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withSwitch)
			h.create(t, "greeter", func(fn *v1.Function) { fn.Spec.Replicas = tc.replicas })
			h.reconcile(t, "greeter")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "greeter").Status.Phase)
			require.NoError(t, h.st.Delete(context.Background(), v1.KindRevision.GVK(), "default", "greeter-1", ""))
			h.rt.hold(replicaID("greeter", 1, tc.held), true)
			creates, _ := h.rt.counts()

			res := h.reconcile(t, "greeter")
			fn := h.getFn(t, "greeter")
			require.Equal(t, tc.phase, fn.Status.Phase)
			require.Equal(t, tc.want, fn.Status.Replicas)
			require.Equal(t, tc.ready, h.condition(t, "greeter", "Ready").Status)
			h.requireCondition(t, "greeter", "RevisionReady", v1.ConditionFalse, "RevisionMissing")
			require.Equal(t, testPeriod, res.RequeueAfter)
			after, _ := h.rt.counts()
			require.Equal(t, creates, after, "no worker is replaced")
		})
	}
}

// scenario: reapply-recovers-missing-revision (ADR-0172).
func TestScenarioReapplyRecoversMissingRevision(t *testing.T) {
	t.Parallel()
	h, _, rec, _ := failMissingRevision(t)

	h.bumpTimeout(t, "greeter", 1)
	h.reconcile(t, "greeter")
	fn := h.getFn(t, "greeter")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, "greeter-2", fn.Status.CurrentRevision)
	rev, err := h.revision(t, "greeter-2")
	require.NoError(t, err)
	require.Equal(t, "sha256:B", rev.Spec.ImageDigest)
	require.Equal(t, "sha256:B", rec.digests()[len(rec.digests())-1])
}

// scenario: recreated-function-stamps-afresh (ADR-0172).
func TestScenarioRecreatedFunctionStampsAfresh(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		old  string
		keep func(t *testing.T, h *shimHarness, rev *v1.Revision)
	}{
		{"owned", func(*testing.T, *shimHarness, *v1.Revision) {}},
		{"ref-less", func(t *testing.T, h *shimHarness, rev *v1.Revision) {
			rev.OwnerReferences = nil
			_, err := h.st.Update(context.Background(), rev)
			require.NoError(t, err)
		}},
		{"collected", func(t *testing.T, h *shimHarness, rev *v1.Revision) {
			require.NoError(t, h.st.Delete(context.Background(), v1.KindRevision.GVK(), "default", rev.Name, ""))
		}},
	} {
		t.Run(tc.old, func(t *testing.T) {
			t.Parallel()
			res, rec := &fakeResolver{digest: "sha256:A"}, &pinRecorder{}
			h := newShimHarness(t, http.StatusOK, false, withSwitch, pinning(res, rec))
			h.create(t, "greeter", func(*v1.Function) {})
			h.reconcile(t, "greeter")
			for range 2 {
				h.bumpTimeout(t, "greeter", 1)
				h.reconcile(t, "greeter")
			}
			require.Equal(t, int64(3), h.getFn(t, "greeter").Generation)
			require.NoError(t, h.st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "greeter", ""))
			h.reconcile(t, "greeter")
			for _, name := range []string{"greeter-1", "greeter-2", "greeter-3"} {
				rev, err := h.revision(t, name)
				require.NoError(t, err)
				tc.keep(t, h, rev)
			}

			res.moveTo("sha256:B")
			h.create(t, "greeter", func(*v1.Function) {})
			h.reconcile(t, "greeter")
			fn := h.getFn(t, "greeter")
			require.Equal(t, v1.PhaseReady, fn.Status.Phase)
			require.Equal(t, "greeter-1", fn.Status.CurrentRevision)
			rev, err := h.revision(t, "greeter-1")
			require.NoError(t, err)
			require.True(t, v1.ControlledBy(rev.OwnerReferences, v1.KindFunction, fn.UID), "greeter-1 names the new UID")
			require.Equal(t, "sha256:B", rev.Spec.ImageDigest, "stamped from the tag's current digest")
			for _, c := range fn.Status.Conditions {
				require.NotEqual(t, "RevisionMissing", c.Reason, c.Type)
			}
		})
	}
}

// scenario: long-name-deploys-with-hashed-revision (ADR-0172).
func TestScenarioLongNameDeploysWithHashedRevision(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("long-name", 7)[:62]
	for _, tc := range []struct {
		pooling string
		name    string
		bumps   int
		want    string
	}{
		{"solo", long, 0, revisionHashed(long, 52, "1")},
		{"pooled", long, 0, revisionHashed(long, 52, "1")},
		{"solo", long[:61], 9, revisionHashed(long[:61], 51, "10")},
	} {
		t.Run(tc.pooling+"-"+strconv.Itoa(len(tc.name))+"-"+strconv.Itoa(tc.bumps+1), func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
			h.create(t, tc.name, func(fn *v1.Function) {
				if tc.pooling == "pooled" {
					fn.Spec.Pooling.Worker = "w"
				}
			})
			h.bumpTimeout(t, tc.name, tc.bumps)
			var stamped *v1.Revision
			for _, restart := range []bool{false, true} {
				if restart {
					h.restart(t)
				}
				h.reconcile(t, tc.name)
				fn := h.getFn(t, tc.name)
				require.Equal(t, v1.PhaseReady, fn.Status.Phase)
				require.Equal(t, tc.want, fn.Status.CurrentRevision)
				rev, err := h.revision(t, tc.want)
				require.NoError(t, err)
				if stamped != nil {
					require.Equal(t, stamped.ResourceVersion, rev.ResourceVersion, "the restarted daemon reuses the Revision")
				}
				stamped = rev
			}
			list, err := h.st.List(context.Background(), v1.KindRevision.GVK(), store.ListOptions{Namespace: "default"})
			require.NoError(t, err)
			require.Len(t, list.Items, 1, "one Revision, stable across a restart")
			if tc.pooling == "pooled" {
				require.Contains(t, h.poolManifest(t, poolOf("w")), `"`+tc.name+`"`)
			}
		})
	}
}

// restart is a daemon restart: a new Reconciler over the same store and options, its runtime listing no worker.
func (h *shimHarness) restart(t *testing.T) {
	t.Helper()
	h.rt.forget()
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	h.gw = embedded.New()
	h.deps.Scheduler, h.deps.Gateway = sch, h.gw
	h.r, err = function.NewReconciler(h.deps)
	require.NoError(t, err)
}

// scenario: short-name-revision-unchanged (ADR-0172).
func TestScenarioShortNameRevisionUnchanged(t *testing.T) {
	t.Parallel()
	n61 := strings.Repeat("short-name", 7)[:61]
	for _, tc := range []struct {
		name  string
		bumps int
		want  string
	}{
		{"greeter", 0, "greeter-1"},
		{n61, 8, n61 + "-9"},
	} {
		h := newShimHarness(t, http.StatusOK, false, withSwitch)
		h.create(t, tc.name, func(*v1.Function) {})
		h.bumpTimeout(t, tc.name, tc.bumps)
		h.reconcile(t, tc.name)
		require.Equal(t, tc.want, h.getFn(t, tc.name).Status.CurrentRevision)
		_, err := h.revision(t, tc.want)
		require.NoError(t, err)
	}
}

// scenario: revision-name-taken-writes-status (ADR-0172).
func TestScenarioRevisionNameTakenWritesStatus(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("long-name", 7)[:62]
	taker := revisionHashed(long, 52, "1")
	taker = taker[:len(taker)-len("-1")]
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.create(t, taker, func(*v1.Function) {})
	h.reconcile(t, taker)
	require.Equal(t, v1.PhaseReady, h.getFn(t, taker).Status.Phase)
	before, err := h.revision(t, taker+"-1")
	require.NoError(t, err)

	h.create(t, long, func(*v1.Function) {})
	_, err = h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: v1.ObjectName(long)})
	require.ErrorIs(t, err, function.ErrRevisionStampFailed)
	require.Equal(t, v1.PhaseFailed, h.getFn(t, long).Status.Phase)
	c := h.condition(t, long, "Ready")
	require.Equal(t, "RevisionStampFailed", c.Reason)
	require.Contains(t, c.Message, taker)
	require.Equal(t, v1.PhaseReady, h.getFn(t, taker).Status.Phase)
	after, err := h.revision(t, taker+"-1")
	require.NoError(t, err)
	require.Equal(t, before.ResourceVersion, after.ResourceVersion, "the other Function's Revision is unchanged")
}

// scenario: revision-create-refused-writes-status (ADR-0172).
func TestScenarioRevisionCreateRefusedWritesStatus(t *testing.T) {
	t.Parallel()
	refused := fault.Invalidf("store.Create", "the store refuses this Revision")
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.Store = function.RefusingStore{Store: d.Store, CreateErr: refused}
	})
	h.create(t, "greeter", func(*v1.Function) {})
	for range 2 {
		_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "greeter"})
		require.True(t, errors.Is(err, function.ErrRevisionStampFailed), "the pass returns the stamp error, retried: %v", err)
		require.Equal(t, v1.PhaseFailed, h.getFn(t, "greeter").Status.Phase)
		c := h.condition(t, "greeter", "RevisionReady")
		require.Equal(t, "RevisionStampFailed", c.Reason, "a retry keeps the gate's reason")
		require.Contains(t, c.Message, "the store refuses this Revision")
	}
}
