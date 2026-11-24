package admission_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

func jobRun(input string) *v1.JobRun {
	r := &v1.JobRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun}}
	r.Name, r.Namespace, r.ResourceGroup = "hello-1a2b3c4d", "default", "rg1"
	r.Spec.Job = "hello"
	if input != "" {
		r.Spec.Input = json.RawMessage(input)
	}
	return r
}

// jobGetter returns job for the run's Job, or NotFound when it is nil.
type jobGetter struct{ job *v1.Job }

func (g jobGetter) Get(_ context.Context, gvk v1.GroupVersionKind, _ v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if g.job == nil || gvk != v1.KindJob.GVK() || name != g.job.Name {
		return nil, fault.NotFoundf("test", "Job %q not found", name)
	}
	return g.job, nil
}

func jobWithContract(input string) *v1.Job {
	j := &v1.Job{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJob.GVK().APIVersion(), Kind: v1.KindJob}}
	j.Name, j.Namespace = "hello", "default"
	if input != "" {
		j.Status.Contract = &v1.WorkflowContract{Input: json.RawMessage(input)}
	}
	return j
}

// ADR-0347 Decision 3 and ADR-0148: an input over job.payloadLimit is refused with 413 at creation.
func TestJobRunPayloadAdmission(t *testing.T) {
	t.Parallel()
	a := admission.NewJobRunPayloadAdmission(16)
	require.True(t, a.Handles(v1.KindJobRun.GVK(), admission.Create))
	require.False(t, a.Handles(v1.KindJobRun.GVK(), admission.Update), "the input is fixed: a cancel of an older run passes")
	require.False(t, a.Handles(v1.KindWorkflowRun.GVK(), admission.Create))
	ctx := context.Background()
	create := func(input string) error {
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindJobRun.GVK(), Object: jobRun(input)})
		return err
	}
	require.NoError(t, create(`{"name":"Ada"}`), "16 bytes is at the limit")
	require.NoError(t, create(""))
	err := create(`{"name":"Adaline"}`)
	require.Equal(t, fault.PayloadTooLarge, fault.KindOf(err))
	require.Equal(t, http.StatusRequestEntityTooLarge, fault.ToProblem(err).Status)
	require.ErrorContains(t, err, "run input 18 bytes exceeds job.payloadLimit 16")
}

// ADR-0347 Decision 3: an input off the Job's status.contract is refused with 422 naming each path; a missing Job or
// one without a contract leaves it unchecked.
func TestJobRunContractAdmission(t *testing.T) {
	t.Parallel()
	const schema = `{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"count":{"type":"integer"}}}`
	ctx := context.Background()
	admit := func(g jobGetter, input string) error {
		a := admission.NewJobRunContractAdmission(g)
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindJobRun.GVK(), Object: jobRun(input)})
		return err
	}
	unprocessable := func(t *testing.T, err error, locations ...string) {
		t.Helper()
		var se huma.StatusError
		require.True(t, errors.As(err, &se), "%v is a huma status error", err)
		require.Equal(t, http.StatusUnprocessableEntity, se.GetStatus())
		require.ErrorContains(t, err, "run input does not match the input schema of Job hello (InputSchemaMismatch)")
		model, ok := se.(*huma.ErrorModel)
		require.True(t, ok)
		var got []string
		for _, d := range model.Errors {
			got = append(got, d.Location)
		}
		require.Equal(t, locations, got)
	}

	withContract := jobGetter{job: jobWithContract(schema)}
	unprocessable(t, admit(withContract, `{}`), "body.spec.input.name")
	unprocessable(t, admit(withContract, `{"name":7,"count":"x"}`), "body.spec.input.count", "body.spec.input.name")
	unprocessable(t, admit(withContract, ""), "body.spec.input")
	require.NoError(t, admit(withContract, `{"name":"Ada"}`))

	edited := jobWithContract(schema)
	edited.Generation = 2
	edited.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionTrue, ObservedGeneration: 1})
	require.NoError(t, admit(jobGetter{job: edited}, `{}`), "an edited Job's contract is an earlier generation's (#756)")
	edited.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionTrue, ObservedGeneration: 2})
	unprocessable(t, admit(jobGetter{job: edited}, `{}`), "body.spec.input.name")
	require.NoError(t, admit(jobGetter{}, `{}`), "a missing Job leaves the input to the run's start")
	require.NoError(t, admit(jobGetter{job: jobWithContract("")}, `{}`), "a Job with no contract yet, held with ContractMissing, leaves the input to the run's start")

	a := admission.NewJobRunContractAdmission(withContract)
	require.True(t, a.Handles(v1.KindJobRun.GVK(), admission.Create))
	require.False(t, a.Handles(v1.KindJobRun.GVK(), admission.Update))
}

// ADR-0347 Decision 3: spec.job and spec.input are fixed at creation, spec.cancel may only turn true, and any other
// change of the spec is refused as invalid.
func TestJobRunSpecAdmission(t *testing.T) {
	t.Parallel()
	a := admission.NewJobRunSpecAdmission()
	require.True(t, a.Handles(v1.KindJobRun.GVK(), admission.Update))
	require.False(t, a.Handles(v1.KindJobRun.GVK(), admission.Create))
	ctx := context.Background()
	update := func(old, next *v1.JobRun) error {
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Update, GVK: v1.KindJobRun.GVK(), Old: old, Object: next})
		return err
	}
	old := jobRun(`{"name": "Ada"}`)
	cancel := jobRun(`{"name":"Ada"}`)
	cancel.Spec.Cancel = true
	require.NoError(t, update(old, cancel), "a cancel turns true, the input compared as JSON")
	require.NoError(t, update(cancel, cancel), "a cancelled run stays cancelled")
	tagged := jobRun(`{"name":"Ada"}`)
	tagged.Tags = v1.Tags{"team": "a"}
	require.NoError(t, update(old, tagged), "metadata is not the spec")

	otherJob := jobRun(`{"name":"Ada"}`)
	otherJob.Spec.Job = "other"
	otherInput := jobRun(`{"name":"Bob"}`)
	noInput := jobRun("")
	for name, c := range map[string]struct {
		old, next *v1.JobRun
		want      string
	}{
		"another job":   {old, otherJob, "spec.job and spec.input are fixed"},
		"another input": {old, otherInput, "spec.job and spec.input are fixed"},
		"input dropped": {old, noInput, "spec.job and spec.input are fixed"},
		"cancel undone": {cancel, old, "spec.cancel may only turn true"},
	} {
		err := update(c.old, c.next)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.Equal(t, http.StatusBadRequest, fault.ToProblem(err).Status, name)
		require.True(t, strings.Contains(err.Error(), c.want), "%s: %v", name, err)
	}
}

// ADR-0348 Decisions 3 and 5: jobrun-spec lets an approval, a rejection and its reason change, which jobrun-approval
// checks.
func TestJobRunSpecAdmissionLetsADecisionThrough(t *testing.T) {
	t.Parallel()
	old := jobRun(`{"name":"Ada"}`)
	decided := jobRun(`{"name":"Ada"}`)
	decided.Spec.Approve, decided.Spec.Reject, decided.Spec.RejectReason = "sha256:"+strings.Repeat("a", 64), true, "no"
	_, err := admission.NewJobRunSpecAdmission().Admit(context.Background(), admission.Request{Operation: admission.Update,
		GVK: v1.KindJobRun.GVK(), Object: decided, Old: old})
	require.NoError(t, err)
}

// ADR-0348 Decisions 3 and 5 (scenarios job-approve-at-creation-refused, job-approve-outdated-digest,
// job-approve-refused-to-viewer): no approval or rejection at creation (422); an approval only of the preview of a run
// AwaitingApproval (409, naming the current digest) and a rejection only of a run that has not started running (409),
// each only from a caller who may update the run's Job (403); spec.approve never cleared, spec.reject one-way and its
// reason set with it (422).
func TestJobRunApprovalAdmission(t *testing.T) {
	t.Parallel()
	a := admission.NewJobRunApprovalAdmission(rbac.New())
	require.True(t, a.Handles(v1.KindJobRun.GVK(), admission.Create))
	require.True(t, a.Handles(v1.KindJobRun.GVK(), admission.Update))
	require.False(t, a.Handles(v1.KindWorkflowRun.GVK(), admission.Update))
	ctx := context.Background()
	d1, d2 := "sha256:"+strings.Repeat("1", 64), "sha256:"+strings.Repeat("2", 64)
	developer := auth.Identity{Subject: "bob", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"default"}}
	viewer := auth.Identity{Subject: "vic", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"default"}}

	for _, edit := range []func(*v1.JobRun){
		func(r *v1.JobRun) { r.Spec.Approve = d1 },
		func(r *v1.JobRun) { r.Spec.Reject = true },
		func(r *v1.JobRun) { r.Spec.RejectReason = "no" },
	} {
		r := jobRun("")
		edit(r)
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindJobRun.GVK(), Object: r, Identity: developer})
		var se huma.StatusError
		require.True(t, errors.As(err, &se), "%v", err)
		require.Equal(t, http.StatusUnprocessableEntity, se.GetStatus())
		require.ErrorContains(t, err, "a person approves or rejects it")
	}
	_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindJobRun.GVK(), Object: jobRun(""), Identity: viewer})
	require.NoError(t, err)

	stored := func(phase v1.JobRunPhase, digest string, edit func(*v1.JobRun)) *v1.JobRun {
		r := jobRun("")
		r.Status.Phase = phase
		if digest != "" {
			r.Status.Preview = &v1.JobRunPreview{Digest: digest, Document: json.RawMessage(`{}`)}
		}
		if edit != nil {
			edit(r)
		}
		return r
	}
	update := func(id auth.Identity, old *v1.JobRun, edit func(*v1.JobRun)) error {
		r := *old
		r.Status.Preview = nil
		edit(&r)
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Update, GVK: v1.KindJobRun.GVK(), Object: &r, Old: old, Identity: id})
		return err
	}
	approve := func(d string) func(*v1.JobRun) { return func(r *v1.JobRun) { r.Spec.Approve = d } }
	reject := func(r *v1.JobRun) { r.Spec.Reject, r.Spec.RejectReason = true, "too wide" }

	awaiting := stored(v1.JobRunAwaitingApproval, d2, nil)
	require.NoError(t, update(developer, awaiting, approve(d2)))
	err = update(developer, awaiting, approve(d1))
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.ErrorContains(t, err, d2, "the 409 names the current digest")
	for _, phase := range []v1.JobRunPhase{"", v1.JobRunPending, v1.JobRunPreviewing, v1.JobRunRunning, v1.JobRunSucceeded} {
		err = update(developer, stored(phase, d2, nil), approve(d2))
		require.Equal(t, fault.Conflict, fault.KindOf(err), "approve while %q", phase)
	}
	require.Equal(t, fault.Conflict, fault.KindOf(update(developer, stored(v1.JobRunAwaitingApproval, "", nil), approve(d2))),
		"no preview yet")
	require.Equal(t, fault.Forbidden, fault.KindOf(update(viewer, awaiting, approve(d2))), "a viewer does not approve")

	for _, phase := range []v1.JobRunPhase{"", v1.JobRunPending, v1.JobRunPreviewing, v1.JobRunAwaitingApproval} {
		require.NoError(t, update(developer, stored(phase, d2, nil), reject), "reject while %q", phase)
	}
	for _, phase := range []v1.JobRunPhase{v1.JobRunRunning, v1.JobRunSucceeded, v1.JobRunRejected} {
		require.Equal(t, fault.Conflict, fault.KindOf(update(developer, stored(phase, d2, nil), reject)), "reject while %q", phase)
	}
	require.Equal(t, fault.Forbidden, fault.KindOf(update(viewer, awaiting, reject)), "a viewer does not reject")

	approved := stored(v1.JobRunRunning, d2, approve(d2))
	require.Equal(t, fault.Invalid, fault.KindOf(update(developer, approved, approve(""))), "an approval is never cleared")
	rejected := stored(v1.JobRunAwaitingApproval, d2, reject)
	require.Equal(t, fault.Invalid, fault.KindOf(update(developer, rejected, func(r *v1.JobRun) { r.Spec.Reject = false })))
	require.Equal(t, fault.Invalid, fault.KindOf(update(developer, rejected, func(r *v1.JobRun) { r.Spec.RejectReason = "other" })),
		"a reason is set with the rejection only")
	require.Equal(t, fault.Invalid, fault.KindOf(update(developer, awaiting, func(r *v1.JobRun) { r.Spec.RejectReason = "why" })))
	require.NoError(t, update(viewer, approved, func(r *v1.JobRun) { r.Spec.Cancel = true }), "a cancel is no decision")

	// A preview made again with another document leaves spec.approve d1 with no recorded approval: approving d1
	// again is an approval of another preview (409, naming d2), while a cancel or a rejection of the run is taken.
	stale := stored(v1.JobRunAwaitingApproval, d2, approve(d1))
	err = update(developer, stale, approve(d1))
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, d2)
	require.NoError(t, update(developer, stale, approve(d2)))
	require.NoError(t, update(developer, stale, func(r *v1.JobRun) { r.Spec.Cancel = true }))
	require.NoError(t, update(developer, stale, reject))
	recorded := stored(v1.JobRunAwaitingApproval, d2, func(r *v1.JobRun) {
		r.Spec.Approve, r.Status.ApprovedAt = d2, v1.NewTimestamp(time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC))
	})
	require.NoError(t, update(viewer, recorded, func(r *v1.JobRun) { r.Tags = v1.Tags{"a": "b"} }),
		"an update that keeps a recorded approval approves nothing")
	require.True(t, admission.JobRunApproves(stale, stale), "keeping an unrecorded approval approves anew")
	require.False(t, admission.JobRunApproves(recorded, recorded))
}

// onlyAllows is an authorizer that allows one verb on one kind, and records each request it got.
type onlyAllows struct {
	verb auth.Verb
	kind v1.Kind
	got  *[]auth.Request
}

func (a onlyAllows) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	*a.got = append(*a.got, req)
	if req.Verb == a.verb && req.Kind == a.kind {
		return auth.Decision{Allowed: true}, nil
	}
	return auth.Decision{Reason: "not that verb on that kind"}, nil
}

// ADR-0348 Decision 3: the approver's right is update on the run's Job, in the run's namespace, and not update on the
// JobRun, which the API server checked before admission.
func TestJobRunApprovalAsksUpdateOnTheJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := "sha256:" + strings.Repeat("2", 64)
	old := jobRun("")
	old.Status.Phase, old.Status.Preview = v1.JobRunAwaitingApproval, &v1.JobRunPreview{Digest: d, Document: json.RawMessage(`{}`)}
	approved := *old
	approved.Status.Preview = nil
	approved.Spec.Approve = d
	for _, c := range []struct {
		kind v1.Kind
		want fault.Kind
	}{{v1.KindJob, ""}, {v1.KindJobRun, fault.Forbidden}} {
		var got []auth.Request
		a := admission.NewJobRunApprovalAdmission(onlyAllows{verb: auth.VerbUpdate, kind: c.kind, got: &got})
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Update, GVK: v1.KindJobRun.GVK(), Object: &approved, Old: old,
			Identity: auth.Identity{Subject: "bob"}})
		require.Equal(t, c.want, fault.KindOf(err), "update allowed on %s: %v", c.kind, err)
		require.Len(t, got, 1)
		require.Equal(t, auth.Request{Identity: auth.Identity{Subject: "bob"}, Verb: auth.VerbUpdate, Kind: v1.KindJob,
			Namespace: "default"}, got[0])
	}
}
