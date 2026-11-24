package function_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
)

const jobDigest = "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111"

// fakeInspector serves one contract blob for every image; nil blob is an image with none.
type fakeInspector struct {
	blob   []byte
	digest string
}

func (f *fakeInspector) Inspect(_ context.Context, _, digest string) ([]byte, error) {
	f.digest = digest
	if f.blob == nil {
		return nil, fault.NotFoundf("test", "the image carries no contract")
	}
	return f.blob, nil
}

type jobHarness struct {
	*harness
	inspector *fakeInspector
}

func newJobHarness(t *testing.T) *jobHarness {
	t.Helper()
	in := &fakeInspector{}
	h := newHarness(t, func(d *function.Deps) {
		d.Resolver = &fakeResolver{digest: jobDigest}
		d.Inspector = in
	})
	return &jobHarness{harness: h, inspector: in}
}

func (h *jobHarness) createJob(t *testing.T, name string, edit func(*v1.JobSpec)) *v1.Job {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindJob)
	job := obj.(*v1.Job)
	job.Name, job.Namespace, job.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	job.Spec = v1.JobSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci://example/" + name + ":v1"}
	if edit != nil {
		edit(&job.Spec)
	}
	created, err := h.st.Create(context.Background(), job)
	require.NoError(t, err)
	return created.(*v1.Job)
}

func (h *jobHarness) reconcileJob(t *testing.T, name string) controller.Result {
	t.Helper()
	res, err := h.r.Jobs().Reconcile(context.Background(), controller.Request{GVK: v1.KindJob.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	require.NoError(t, err)
	return res
}

func (h *jobHarness) getJob(t *testing.T, name string) *v1.Job {
	t.Helper()
	obj, err := h.st.Get(context.Background(), v1.KindJob.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Job)
}

func (h *jobHarness) editJob(t *testing.T, name string, edit func(*v1.JobSpec)) {
	t.Helper()
	job := h.getJob(t, name)
	edit(&job.Spec)
	_, err := h.st.Update(context.Background(), job)
	require.NoError(t, err)
}

func (h *jobHarness) revision(t *testing.T, name string) (*v1.Revision, bool) {
	t.Helper()
	obj, err := h.st.Get(context.Background(), v1.KindRevision.GVK(), "default", v1.ObjectName(name))
	if fault.KindOf(err) == fault.NotFound {
		return nil, false
	}
	require.NoError(t, err)
	return obj.(*v1.Revision), true
}

func (h *jobHarness) createRun(t *testing.T, name, job string, phase v1.JobRunPhase, pin v1.ObjectName) *v1.JobRun {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindJobRun)
	run := obj.(*v1.JobRun)
	run.Name, run.Namespace, run.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	run.Spec.Job = v1.ObjectName(job)
	run.Status.Phase, run.Status.JobRevision = phase, pin
	created, err := h.st.Create(context.Background(), run)
	require.NoError(t, err)
	return created.(*v1.JobRun)
}

func readyOf(t *testing.T, job *v1.Job) v1.Condition {
	t.Helper()
	c, ok := job.Status.Conditions.Get("Ready")
	require.True(t, ok, "the Job has a Ready condition")
	return c
}

// ADR-0347 Decision 2: a Job's pass stamps <job>-v<generation> naming the Job in spec.job, under its controller
// reference, with the digest resolved once, and the Job is Ready; a new generation stamps the next, and the revision
// that nothing pins any more is deleted.
func TestJobStampsARevisionPerGeneration(t *testing.T) {
	t.Parallel()
	h := newJobHarness(t)
	job := h.createJob(t, "hello", nil)
	h.reconcileJob(t, "hello")

	rev, ok := h.revision(t, "hello-v1")
	require.True(t, ok, "the first generation is stamped")
	require.Equal(t, v1.ObjectName("hello"), rev.Spec.Job)
	require.Equal(t, v1.ObjectRef{}, rev.Spec.Function, "a Job's Revision names no Function")
	require.True(t, v1.ControlledBy(rev.OwnerReferences, v1.KindJob, job.UID))
	require.Equal(t, int64(1), rev.Spec.Number)
	require.Equal(t, jobDigest, rev.Spec.ImageDigest, "the digest is resolved once, at the stamp")
	require.Equal(t, v1.RuntimeName("nodejs22"), rev.Spec.Runtime)
	require.Equal(t, "handle", rev.Spec.Handler)
	got := h.getJob(t, "hello")
	require.Equal(t, v1.ObjectName("hello-v1"), got.Status.CurrentRevision)
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
	require.Equal(t, v1.ConditionTrue, readyOf(t, got).Status)
	require.Equal(t, int64(1), readyOf(t, got).ObservedGeneration)

	h.editJob(t, "hello", func(s *v1.JobSpec) { s.Image = "oci://example/hello:v2" })
	h.reconcileJob(t, "hello")
	require.Equal(t, v1.ObjectName("hello-v2"), h.getJob(t, "hello").Status.CurrentRevision)
	_, ok = h.revision(t, "hello-v2")
	require.True(t, ok)
	_, ok = h.revision(t, "hello-v1")
	require.False(t, ok, "no run pins the old revision, so it goes")
}

// ADR-0347 Decision 2 and run.fizz PinnedRevisionKept: a revision a run that has not ended pins stays, and goes once
// the run ends; a run of another Job pins nothing of this one.
func TestJobKeepsTheRevisionsItsOpenRunsPin(t *testing.T) {
	t.Parallel()
	h := newJobHarness(t)
	h.createJob(t, "hello", nil)
	h.reconcileJob(t, "hello")
	run := h.createRun(t, "hello-1a2b3c4d", "hello", v1.JobRunRunning, "hello-v1")
	h.createRun(t, "other-5e6f7a8b", "other", v1.JobRunRunning, "hello-v2")

	h.editJob(t, "hello", func(s *v1.JobSpec) { s.Image = "oci://example/hello:v2" })
	h.reconcileJob(t, "hello")
	h.editJob(t, "hello", func(s *v1.JobSpec) { s.Image = "oci://example/hello:v3" })
	h.reconcileJob(t, "hello")
	_, ok := h.revision(t, "hello-v1")
	require.True(t, ok, "the running run keeps the revision it pinned")
	_, ok = h.revision(t, "hello-v2")
	require.False(t, ok, "another Job's run pins nothing of this Job")

	require.Equal(t, []controller.Request{{GVK: v1.KindJob.GVK(), Namespace: "default", Name: "hello"}},
		h.r.Jobs().MapRun(context.Background(), run), "a run's change reaches its Job")
	run.Status.Phase = v1.JobRunSucceeded
	_, err := h.st.Update(context.Background(), run)
	require.NoError(t, err)
	h.reconcileJob(t, "hello")
	_, ok = h.revision(t, "hello-v1")
	require.False(t, ok, "an ended run pins nothing")
	_, ok = h.revision(t, "hello-v3")
	require.True(t, ok, "the current revision stays")
}

// ADR-0347 Decision 2: the reconciler reads the revision's contract by digest into status.contract; where artifacts
// carry no contracts, as funcdctl dev's, an image with none leaves it unset (TestJobWithoutContractIsHeld holds it
// elsewhere), and one that does not compile holds the Job with ContractInvalid, as a Function's revision is held.
func TestJobReadsItsContractByDigest(t *testing.T) {
	t.Parallel()
	h := newJobHarness(t)
	h.inspector.blob = []byte(`{"input":{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}},` +
		`"output":{"type":"object"}}`)
	h.createJob(t, "hello", nil)
	h.reconcileJob(t, "hello")
	got := h.getJob(t, "hello")
	require.NotNil(t, got.Status.Contract)
	require.JSONEq(t, `{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`, string(got.Status.Contract.Input))
	require.Equal(t, jobDigest, h.inspector.digest, "the contract is read by the pinned digest")
	require.Equal(t, v1.ConditionTrue, readyOf(t, got).Status)

	h.inspector.blob = nil
	h.editJob(t, "hello", func(s *v1.JobSpec) { s.Image = "oci://example/hello:v2" })
	h.reconcileJob(t, "hello")
	got = h.getJob(t, "hello")
	require.Nil(t, got.Status.Contract, "where artifacts carry no contracts, an image with none leaves the input unchecked")
	require.Equal(t, v1.ConditionTrue, readyOf(t, got).Status, "there, a Job whose image has no contract is Ready")

	h.inspector.blob = []byte(`{"input":{"type":"object","pattern":"("}}`)
	h.editJob(t, "hello", func(s *v1.JobSpec) { s.Image = "oci://example/hello:v3" })
	h.reconcileJob(t, "hello")
	got = h.getJob(t, "hello")
	ready := readyOf(t, got)
	require.Equal(t, v1.ConditionFalse, ready.Status)
	require.Equal(t, "ContractInvalid", ready.Reason)
	require.Equal(t, v1.PhaseFailed, got.Status.Phase)
	require.Nil(t, got.Status.Contract)
}

// ADR-0347 Decision 2: the Job is Ready only once the checks a Function passes before its first worker pass, with
// the Function's reasons: the shape gate, and ADR-0306's hooks, whose root entries a Job owns as a Function does.
func TestJobGatesOnTheFunctionsChecks(t *testing.T) {
	t.Parallel()
	h := newJobHarness(t)
	h.createJob(t, "shapeless", func(s *v1.JobSpec) { s.Handler = "" })
	h.reconcileJob(t, "shapeless")
	got := h.getJob(t, "shapeless")
	require.Equal(t, "ShapeInvalid", readyOf(t, got).Reason)
	require.Equal(t, v1.PhaseFailed, got.Status.Phase)

	h.createJob(t, "secretive", func(s *v1.JobSpec) {
		s.SecretDeclarations = []v1.SecretDeclaration{{Name: "creds", Keys: []string{"TOKEN"}}}
	})
	res := h.reconcileJob(t, "secretive")
	got = h.getJob(t, "secretive")
	require.Equal(t, "SecretNotFound", readyOf(t, got).Reason)
	require.Equal(t, v1.PhasePending, got.Status.Phase)
	require.Positive(t, res.RequeueAfter, "a missing referent is looked at again")

	job := h.createJob(t, "keeper", func(s *v1.JobSpec) { s.KVStores = []v1.KVStoreEntry{{Name: "state"}} })
	h.reconcileJob(t, "keeper")
	require.Equal(t, v1.ConditionTrue, readyOf(t, h.getJob(t, "keeper")).Status)
	obj, err := h.st.Get(context.Background(), v1.KindKVStore.GVK(), "default", "state")
	require.NoError(t, err, "the Job writes the store it defines")
	var marked bool
	for _, ref := range obj.GetObjectMeta().OwnerReferences {
		marked = marked || (ref.Kind == v1.KindJob && ref.UID == job.UID)
	}
	require.True(t, marked, "the store carries the Job's marker")

	h.createJob(t, "squatter", func(s *v1.JobSpec) { s.KVStores = []v1.KVStoreEntry{{Name: "state"}} })
	h.reconcileJob(t, "squatter")
	ready := readyOf(t, h.getJob(t, "squatter"))
	require.Equal(t, "StoreNotOwned", ready.Reason)
	require.True(t, strings.Contains(ready.Message, "is not this Job's"), ready.Message)
}

// ADR-0172 Decision 5 for a Job (ADR-0347 Decision 2): a deleted namesake's Revision is dropped and stamped afresh, and
// one another kind controls fails the stamp.
func TestJobRevisionNameTaken(t *testing.T) {
	t.Parallel()
	h := newJobHarness(t)
	ctx := context.Background()
	stale := &v1.Revision{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRevision.GVK().APIVersion(), Kind: v1.KindRevision}}
	stale.Name, stale.Namespace, stale.ResourceGroup = "hello-v1", "default", "rg1"
	stale.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindJob, Namespace: "default", Name: "hello"},
		UID: "uid-of-a-deleted-job", Controller: true}}
	stale.Spec = v1.RevisionSpec{Job: "hello", Number: 1, Image: "oci://example/hello:v0", ImageDigest: jobDigest}
	_, err := h.st.Create(ctx, stale)
	require.NoError(t, err)
	job := h.createJob(t, "hello", nil)
	h.reconcileJob(t, "hello")
	h.reconcileJob(t, "hello")
	rev, ok := h.revision(t, "hello-v1")
	require.True(t, ok)
	require.True(t, v1.ControlledBy(rev.OwnerReferences, v1.KindJob, job.UID), "the namesake's revision is stamped afresh")
	require.Equal(t, "oci://example/hello:v1", rev.Spec.Image)

	taken := &v1.Revision{TypeMeta: stale.TypeMeta}
	taken.Name, taken.Namespace, taken.ResourceGroup = "other-v1", "default", "rg1"
	taken.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Namespace: "default", Name: "other-v1"},
		UID: "uid-f", Controller: true}}
	taken.Spec = v1.RevisionSpec{Function: v1.ObjectRef{Kind: v1.KindFunction, Namespace: "default", Name: "other-v1"}, Number: 1}
	_, err = h.st.Create(ctx, taken)
	require.NoError(t, err)
	h.createJob(t, "other", nil)
	h.reconcileJob(t, "other")
	ready := readyOf(t, h.getJob(t, "other"))
	require.Equal(t, "RevisionStampFailed", ready.Reason)
	require.Contains(t, ready.Message, `other-v1 is Function "other-v1"'s`)
}

// ADR-0347 Decision 1 and ADR-0262 Decision 8: a deleted Job's pass reaches no stored Job and fails nothing.
func TestJobDeletedPassIsQuiet(t *testing.T) {
	t.Parallel()
	h := newJobHarness(t)
	require.Equal(t, controller.Result{}, h.reconcileJob(t, "gone"))
}

// ADR-0347 Decision 1 and ADR-0248 Decision 5: a ConfigMap a Job no longer defines goes once the Job is Ready again,
// as a Function's does, and one another object still names stays.
func TestJobPrunesTheConfigMapsItNoLongerDefines(t *testing.T) {
	t.Parallel()
	h := newJobHarness(t)
	h.createJob(t, "tuned", func(s *v1.JobSpec) {
		s.ConfigMaps = []v1.ConfigMapEntry{{Name: "settings", ConfigMapSpec: v1.ConfigMapSpec{Data: map[string]string{"A": "1"}}}}
	})
	h.reconcileJob(t, "tuned")
	stored := v1.AppConfigMapName("settings", v1.ConfigMapSpec{Data: map[string]string{"A": "1"}})
	_, err := h.st.Get(context.Background(), v1.KindConfigMap.GVK(), "default", stored)
	require.NoError(t, err, "the Job defines its ConfigMap")

	h.editJob(t, "tuned", func(s *v1.JobSpec) { s.ConfigMaps = nil })
	h.reconcileJob(t, "tuned")
	_, err = h.st.Get(context.Background(), v1.KindConfigMap.GVK(), "default", stored)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "a ConfigMap the Job no longer defines goes")
}

// fixedPlatforms says every artifact is built for one platform.
type fixedPlatforms struct{ p v1.OCIPlatform }

func (f fixedPlatforms) Platforms(context.Context, string, string) ([]v1.OCIPlatform, error) {
	return []v1.OCIPlatform{f.p}, nil
}

// ADR-0347 Decision 2 and ADR-0145: an artifact built for no platform the node runs holds the Job, as it holds a
// Function, before any other gate.
func TestJobPlatformGate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(d *function.Deps) {
		d.Resolver = &fakeResolver{digest: jobDigest}
		d.Platforms = fixedPlatforms{p: "plan9/sparc"}
	})
	jh := &jobHarness{harness: h, inspector: &fakeInspector{}}
	jh.createJob(t, "elsewhere", nil)
	jh.reconcileJob(t, "elsewhere")
	got := jh.getJob(t, "elsewhere")
	require.Equal(t, "NoMatchingPlatform", readyOf(t, got).Reason)
	require.Equal(t, v1.PhaseFailed, got.Status.Phase)
}

// A platform with no materializer runs no worker of its own: its Functions get a stand-in worker that serves no call,
// and a run of a Job ends Failed with StartFailed, saying that this platform runs no workers, rather than crashing its
// executor: from its preview, or at once with approval.bypass (ADR-0348 Decision 2).
func TestJobRunOnAPlatformThatRunsNoWorkers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		bypass bool
		first  v1.JobRunPhase
	}{
		{"preview", false, v1.JobRunPreviewing},
		{"bypass", true, v1.JobRunRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newJobHarness(t)
			h.createJob(t, "hello", func(s *v1.JobSpec) { s.Approval.Bypass = tc.bypass })
			h.reconcileJob(t, "hello")
			require.Equal(t, v1.ConditionTrue, readyOf(t, h.getJob(t, "hello")).Status)
			ended := make(chan v1.ObjectName, 4)
			runs := h.r.Runs(function.RunsConfig{MaxRunning: 4, PayloadLimit: 1 << 10, Retention: time.Hour,
				Enqueue: func(req controller.Request) { ended <- req.Name }})
			t.Cleanup(runs.Close)
			obj, _ := v1.NewObject(v1.KindJobRun)
			run := obj.(*v1.JobRun)
			run.Name, run.Namespace, run.ResourceGroup, run.Spec.Job = "hello-a", "default", "rg1", "hello"
			_, err := h.st.Create(context.Background(), run)
			require.NoError(t, err)
			pass := func() *v1.JobRun {
				_, err := runs.Reconcile(context.Background(), controller.Request{GVK: v1.KindJobRun.GVK(), Namespace: "default", Name: "hello-a"})
				require.NoError(t, err)
				got, err := h.st.Get(context.Background(), v1.KindJobRun.GVK(), "default", "hello-a")
				require.NoError(t, err)
				return got.(*v1.JobRun)
			}
			require.Equal(t, tc.first, pass().Status.Phase)
			select {
			case <-ended:
			case <-time.After(10 * time.Second):
				t.Fatal("the run's executor did not end")
			}
			got := pass()
			require.Equal(t, v1.JobRunFailed, got.Status.Phase)
			c, ok := got.Status.Conditions.Get("Ready")
			require.True(t, ok)
			require.Equal(t, v1.JobRunStartFailed, c.Reason)
			require.Contains(t, c.Message, "runs no workers")
		})
	}
}
