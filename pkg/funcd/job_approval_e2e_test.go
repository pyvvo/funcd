//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// The scenarios of ADR-0348 on the process driver and the real Node shim: a run of Job tofu, which has no
// approval.bypass, previews, waits for a person, and runs exactly the preview a person approved.

// approvalJob is the files that Job tofu's module reads and writes: its preview answers plan's content and counts its
// calls in previews, waiting for gate when gated; its handler counts its calls in calls and in calls-<run>, waits for
// hold when held, and answers its context's approval.
type approvalJob struct {
	h                     *shimRig
	st                    store.Store
	previews, calls, plan string
	gate, hold            gate
	api                   string // the control plane's URL, set by bootApprovalAs
}

func approvalModule(a *approvalJob, gated, held bool) string {
	waitFor := func(on bool, g gate) string {
		if !on {
			return "''"
		}
		return fmt.Sprintf("%q", string(g))
	}
	return fmt.Sprintf(`import { appendFileSync, existsSync, readFileSync } from 'node:fs';
const until = async (file) => { while (file !== '' && !existsSync(file)) await new Promise((r) => setTimeout(r, 20)); };
export async function preview(ctx) {
  appendFileSync(%q, 'x');
  await until(%s);
  return { plan: readFileSync(%q, 'utf8'), phase: ctx.run.phase, approval: ctx.approval };
}
export async function handle(ctx) {
  appendFileSync(%q, 'x');
  appendFileSync(%q + '-' + ctx.run.name, 'x');
  await until(%s);
  return { approval: ctx.approval, phase: ctx.run.phase };
}
`, a.previews, waitFor(gated, a.gate), a.plan, a.calls, a.calls, waitFor(held, a.hold))
}

// newApprovalJob is the files of a Job tofu module in a fresh directory, with plan "2 to add".
func newApprovalJob(t *testing.T, h *shimRig, st store.Store) *approvalJob {
	t.Helper()
	dir := t.TempDir()
	a := &approvalJob{h: h, st: st, previews: filepath.Join(dir, "previews"), calls: filepath.Join(dir, "calls"),
		plan: filepath.Join(dir, "plan"), gate: newGate(t), hold: gate(filepath.Join(dir, "hold"))}
	a.setPlan(t, "2 to add")
	return a
}

// deploy applies Job tofu on a's module, without approval.bypass; edit changes its spec.
func (a *approvalJob) deploy(t *testing.T, gated, held bool, edit func(*v1.JobSpec)) {
	t.Helper()
	a.h.deployJob(t, "tofu", approvalModule(a, gated, held), "", func(s *v1.JobSpec) {
		s.Approval.Bypass = false
		if edit != nil {
			edit(s)
		}
	})
	a.h.waitJob(t, "tofu")
}

// bootApproval runs a rig on a store of its own with opts and deploys Job tofu, its preview gated when gated; edit
// changes the Job's spec.
func bootApproval(t *testing.T, gated bool, edit func(*v1.JobSpec), opts ...funcd.Option) *approvalJob {
	t.Helper()
	st := store.New(memory.New())
	a := newApprovalJob(t, newShimRig(t, "", append([]funcd.Option{funcd.WithStore(st)}, opts...)...), st)
	a.deploy(t, gated, false, edit)
	return a
}

func (a *approvalJob) setPlan(t *testing.T, plan string) {
	t.Helper()
	require.NoError(t, os.WriteFile(a.plan, []byte(plan), 0o600))
}

// awaiting starts a run of tofu and waits until it waits for a person, with its preview.
func (a *approvalJob) awaiting(t *testing.T) *v1.JobRun {
	t.Helper()
	run := a.h.waitRun(t, a.h.startRun(t, "tofu", `{"op":"apply"}`), v1.JobRunAwaitingApproval)
	require.NotNil(t, run.Status.Preview)
	return run
}

// decide applies edit to run name, as funcdctl job approve and reject do, with c, or the rig's client when nil.
func (a *approvalJob) decide(t *testing.T, c *sdk.Client, name v1.ObjectName, edit func(*v1.JobRun)) error {
	t.Helper()
	if c == nil {
		c = a.h.c
	}
	run := a.h.getJobRun(t, string(name))
	edit(run)
	_, err := c.Apply(context.Background(), run)
	return err
}

func approve(digest string) func(*v1.JobRun) { return func(r *v1.JobRun) { r.Spec.Approve = digest } }

// backdate moves run name's preview into the past by ago, past an expiry of less than ago.
func (a *approvalJob) backdate(t *testing.T, name v1.ObjectName, ago time.Duration) {
	t.Helper()
	ctx := context.Background()
	obj, err := a.st.Get(ctx, v1.KindJobRun.GVK(), "default", name)
	require.NoError(t, err)
	run := obj.(*v1.JobRun)
	run.Status.Preview.PreviewedAt = v1.NewTimestamp(time.Now().Add(-ago))
	_, err = a.st.Update(ctx, run)
	require.NoError(t, err)
}

// handlerApproval is what the handler answered of its context: the approval it ran under and the call's phase.
type handlerApproval struct {
	Approval *struct {
		ApprovedBy string          `json:"approvedBy"`
		Digest     string          `json:"digest"`
		Preview    json.RawMessage `json:"preview"`
	} `json:"approval"`
	Phase string `json:"phase"`
}

func resultOf(t *testing.T, run *v1.JobRun) handlerApproval {
	t.Helper()
	var got handlerApproval
	require.NoError(t, json.Unmarshal(run.Status.Result, &got), "%s", run.Status.Result)
	return got
}

// scenario: job-run-waits-for-approval — a run of a Job without approval.bypass goes Previewing, then AwaitingApproval
// with its preview's digest, document and time, and its handler has not run.
func TestScenarioJobRunWaitsForApproval(t *testing.T) {
	t.Parallel()
	a := bootApproval(t, true, nil)
	name := a.h.startRun(t, "tofu", `{"op":"apply"}`)
	a.h.waitRun(t, name, v1.JobRunPreviewing)
	a.gate.open(t)
	run := a.h.waitRun(t, name, v1.JobRunAwaitingApproval)
	require.Regexp(t, "^sha256:[a-f0-9]{64}$", run.Status.Preview.Digest)
	require.JSONEq(t, `{"plan":"2 to add","phase":"preview","approval":null}`, string(run.Status.Preview.Document))
	require.False(t, run.Status.Preview.PreviewedAt.IsZero())
	stays(t, func() bool {
		return a.h.getJobRun(t, name).Status.Phase == v1.JobRunAwaitingApproval && callsOf(t, a.calls) == 0
	}, 300*time.Millisecond, 50*time.Millisecond, "the run waits for a person, and its handler has not run")
	require.Zero(t, runWorkers(t, a.h.rt, name), "a waiting run holds no worker")
}

// scenario: job-approve-runs-the-preview — an approval of the run's preview digest runs it to Succeeded, its handler's
// context.approval holding the approver, the digest and the preview's document.
func TestScenarioJobApproveRuns(t *testing.T) {
	t.Parallel()
	a := bootApproval(t, false, nil)
	run := a.awaiting(t)
	require.NoError(t, a.decide(t, nil, run.Name, approve(run.Status.Preview.Digest)))
	ended := a.h.waitRun(t, string(run.Name), v1.JobRunSucceeded)
	got := resultOf(t, ended)
	require.Equal(t, "run", got.Phase)
	require.NotNil(t, got.Approval)
	require.Equal(t, "dev", got.Approval.ApprovedBy)
	require.Equal(t, run.Status.Preview.Digest, got.Approval.Digest)
	require.JSONEq(t, string(run.Status.Preview.Document), string(got.Approval.Preview))
	require.Equal(t, "dev", ended.Status.ApprovedBy)
	require.Equal(t, run.Status.TraceID, ended.Status.TraceID, "the preview and the apply run in one trace")
	// oracle: docs/design/job/approval.fizz#NoApprovalStuck
	require.True(t, ended.Status.Phase.Ended(), "the approved run ends")
	require.Equal(t, 1, callsOf(t, a.previews))
	require.Equal(t, 1, callsOf(t, a.calls), "the handler ran once")
}

// scenario: job-approve-outdated-digest — once the run's preview is d2, an approval of d1 is answered 409 naming d2, and
// nothing changes.
func TestScenarioJobApproveOutdatedDigest(t *testing.T) {
	t.Parallel()
	a := bootApproval(t, false, func(s *v1.JobSpec) { s.Approval.Expiry = v1.MinJobApprovalExpiry })
	run := a.awaiting(t)
	d1 := run.Status.Preview.Digest
	a.backdate(t, run.Name, 2*time.Minute)
	a.setPlan(t, "3 to add")
	require.NoError(t, a.decide(t, nil, run.Name, approve(d1)), "d1 is the run's preview when it is approved")
	var again *v1.JobRun
	require.Eventually(t, func() bool {
		again = a.h.getJobRun(t, string(run.Name))
		return again.Status.Phase == v1.JobRunAwaitingApproval && again.Status.Preview.Digest != d1
	}, runWithin, 20*time.Millisecond, "the stale preview is made again, now d2")
	d2 := again.Status.Preview.Digest

	err := a.decide(t, nil, run.Name, approve(d1))
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, d2, "the 409 names the current digest")
	stays(t, func() bool {
		r := a.h.getJobRun(t, string(run.Name))
		return r.ResourceVersion == again.ResourceVersion && r.Status.Phase == v1.JobRunAwaitingApproval
	}, 300*time.Millisecond, 50*time.Millisecond, "nothing changes")
	require.Zero(t, callsOf(t, a.calls))
}

// scenario: job-approve-at-creation-refused — a run created with spec.approve set is refused with 422, and no run
// exists.
func TestScenarioJobApproveAtCreation(t *testing.T) {
	t.Parallel()
	a := bootApproval(t, false, nil)
	run := &v1.JobRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun},
		ObjectMeta: v1.ObjectMeta{Name: "tofu-early", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.JobRunSpec{Job: "tofu", Approve: "sha256:" + fmt.Sprintf("%064d", 1)}}
	_, err := a.h.c.Create(context.Background(), run)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a 422 is Invalid to the SDK: %v", err)
	require.ErrorContains(t, err, "body.spec.approve")
	// oracle: docs/design/job/approval.fizz#ApprovedByAPerson
	_, err = a.h.c.Get(context.Background(), v1.KindJobRun, "default", "tofu-early")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "no run exists, so none runs on an approval no person gave")
	require.Zero(t, callsOf(t, a.calls))
}

// scenario: job-approve-refused-to-viewer — a viewer of the namespace who approves or rejects a run is answered 403.
func TestScenarioJobApproveRefusedToViewer(t *testing.T) {
	t.Parallel()
	const developer, viewer = "e2e-approval-developer", "e2e-approval-viewer"
	a := bootApprovalAs(t, developer, funcd.WithCredentials(
		funcd.Credential{Token: developer, Role: "developer", Namespaces: []string{"default"}},
		funcd.Credential{Token: viewer, Role: "viewer", Namespaces: []string{"default"}}))
	run := a.awaiting(t)
	view, err := sdk.New(a.api, sdk.WithToken(viewer))
	require.NoError(t, err)
	for _, edit := range []func(*v1.JobRun){approve(run.Status.Preview.Digest), func(r *v1.JobRun) { r.Spec.Reject = true }} {
		err := a.decide(t, view, run.Name, edit)
		require.Equal(t, fault.Forbidden, fault.KindOf(err), "%v", err)
	}
	after := a.h.getJobRun(t, string(run.Name))
	require.Equal(t, v1.JobRunAwaitingApproval, after.Status.Phase)
	require.Empty(t, after.Spec.Approve)
	require.False(t, after.Spec.Reject)
}

// bootApprovalAs is bootApproval whose rig's client carries token, which opts' credentials answer.
func bootApprovalAs(t *testing.T, token string, opts ...funcd.Option) *approvalJob {
	t.Helper()
	st := store.New(memory.New())
	h, base := shimRigBase(t, "")
	p, err := funcd.New(append(append(base, funcd.WithStore(st)), opts...)...)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})
	h.connect(t, p)
	h.c, err = sdk.New("http://"+p.Addr(), sdk.WithToken(token))
	require.NoError(t, err)
	a := newApprovalJob(t, h, st)
	a.api = "http://" + p.Addr()
	a.deploy(t, false, false, nil)
	return a
}

// scenario: job-stale-preview-previewed-again — a preview older than approval.expiry, when it is approved or when its
// slot frees after the approval, is previewed again, with PreviewExpired: the run runs when the new preview has the
// approved digest, and waits for a new approval when it differs.
func TestScenarioJobStalePreview(t *testing.T) {
	t.Parallel()
	t.Run("same preview at the approval", func(t *testing.T) {
		t.Parallel()
		a := bootApproval(t, false, func(s *v1.JobSpec) { s.Approval.Expiry = v1.MinJobApprovalExpiry })
		run := a.awaiting(t)
		a.backdate(t, run.Name, 2*time.Minute)
		require.NoError(t, a.decide(t, nil, run.Name, approve(run.Status.Preview.Digest)))
		ended := a.h.waitRun(t, string(run.Name), v1.JobRunSucceeded)
		// oracle: docs/design/job/approval.fizz#AppliesWhatWasApproved
		require.Equal(t, run.Status.Preview.Digest, resultOf(t, ended).Approval.Digest, "the approved digest runs")
		require.Equal(t, 2, callsOf(t, a.previews), "the stale preview was made again")
		require.Equal(t, 1, callsOf(t, a.calls))
	})
	t.Run("another preview at the approval", func(t *testing.T) {
		t.Parallel()
		a := bootApproval(t, false, func(s *v1.JobSpec) { s.Approval.Expiry = v1.MinJobApprovalExpiry })
		run := a.awaiting(t)
		a.backdate(t, run.Name, 2*time.Minute)
		a.setPlan(t, "3 to add")
		require.NoError(t, a.decide(t, nil, run.Name, approve(run.Status.Preview.Digest)))
		var again *v1.JobRun
		require.Eventually(t, func() bool {
			again = a.h.getJobRun(t, string(run.Name))
			return again.Status.Phase == v1.JobRunAwaitingApproval && again.Status.Preview.Digest != run.Status.Preview.Digest
		}, runWithin, 20*time.Millisecond, "the preview is made again with another document")
		// oracle: docs/design/job/approval.fizz#NoStaleApply
		require.Empty(t, again.Status.ApprovedBy, "the approval of the old preview is cleared")
		require.Zero(t, callsOf(t, a.calls), "no apply of the stale approval")
		require.NoError(t, a.decide(t, nil, run.Name, approve(again.Status.Preview.Digest)), "a new approval")
		require.JSONEq(t, `{"plan":"3 to add","phase":"preview","approval":null}`,
			string(resultOf(t, a.h.waitRun(t, string(run.Name), v1.JobRunSucceeded)).Approval.Preview))
	})
	t.Run("stale when the slot frees", func(t *testing.T) {
		t.Parallel()
		st := store.New(memory.New())
		a := newApprovalJob(t, newShimRig(t, "", funcd.WithStore(st)), st)
		a.deploy(t, false, true, func(s *v1.JobSpec) { s.Approval.Expiry = v1.MinJobApprovalExpiry })
		t.Cleanup(func() { a.hold.open(t) })
		blocker, waiting := a.awaiting(t), a.awaiting(t)
		require.NoError(t, a.decide(t, nil, blocker.Name, approve(blocker.Status.Preview.Digest)))
		a.h.waitRun(t, string(blocker.Name), v1.JobRunRunning)
		require.NoError(t, a.decide(t, nil, waiting.Name, approve(waiting.Status.Preview.Digest)))
		require.Eventually(t, func() bool {
			r := a.h.getJobRun(t, string(waiting.Name))
			reason, _ := runReason(r)
			return r.Status.Phase == v1.JobRunPending && reason == v1.JobRunQueued
		}, runWithin, 20*time.Millisecond, "the approved run waits Queued for the slot")
		a.backdate(t, waiting.Name, 2*time.Minute)
		previews := callsOf(t, a.previews)
		a.hold.open(t)
		ended := a.h.waitRun(t, string(waiting.Name), v1.JobRunSucceeded)
		require.Equal(t, waiting.Status.Preview.Digest, resultOf(t, ended).Approval.Digest)
		require.Equal(t, previews+1, callsOf(t, a.previews), "the preview that went stale while it waited was made again")
		require.Equal(t, waiting.Status.TraceID, ended.Status.TraceID, "the run keeps its trace")
	})
}

// scenario: job-reject-ends-run — a rejection with a reason ends the run Rejected, with that reason and rejectedBy, and
// its handler never ran.
func TestScenarioJobRejectEndsRun(t *testing.T) {
	t.Parallel()
	a := bootApproval(t, false, nil)
	run := a.awaiting(t)
	require.NoError(t, a.decide(t, nil, run.Name, func(r *v1.JobRun) { r.Spec.Reject, r.Spec.RejectReason = true, "too wide" }))
	ended := a.h.waitRun(t, string(run.Name), v1.JobRunRejected)
	reason, msg := runReason(ended)
	require.Equal(t, v1.JobRunRejectedReason, reason)
	require.Equal(t, "too wide", msg)
	require.Equal(t, "dev", ended.Status.RejectedBy)
	// oracle: docs/design/job/approval.fizz#RejectedNeverRuns
	stays(t, func() bool { return callsOf(t, a.calls) == 0 }, 300*time.Millisecond, 50*time.Millisecond,
		"a rejected run's handler never runs")
	require.Equal(t, fault.Conflict, fault.KindOf(a.decide(t, nil, run.Name, approve(run.Status.Preview.Digest))),
		"a rejected run takes no approval")
}

// scenario: job-bypass-runs-at-once — a run of a Job with approval.bypass calls no preview, runs at once, and its
// context.approval is none.
func TestScenarioJobBypassRunsAtOnce(t *testing.T) {
	t.Parallel()
	a := bootApproval(t, false, func(s *v1.JobSpec) { s.Approval.Bypass = true })
	run := a.h.waitRun(t, a.h.startRun(t, "tofu", `{"op":"apply"}`), v1.JobRunSucceeded)
	got := resultOf(t, run)
	require.Nil(t, got.Approval)
	require.Equal(t, "run", got.Phase)
	require.Nil(t, run.Status.Preview)
	require.Zero(t, callsOf(t, a.previews), "no preview")
}

// scenario: job-restart-previews-again — a run Previewing when funcd restarts previews again.
func TestScenarioJobRestartPreviewsAgain(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	boot := func() (*shimRig, func()) {
		h, opts := shimRigBase(t, "")
		p, err := funcd.New(append(opts, funcd.WithStore(st))...)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- p.Run(ctx) }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			cancel()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Error("platform Run did not return after cancel")
			}
		}
		t.Cleanup(stop)
		h.connect(t, p)
		return h, stop
	}
	h, stop := boot()
	a := newApprovalJob(t, h, st)
	a.deploy(t, true, false, nil)
	name := h.startRun(t, "tofu", `{"op":"apply"}`)
	h.waitRun(t, name, v1.JobRunPreviewing)
	require.Eventually(t, func() bool { return callsOf(t, a.previews) == 1 }, runWithin, 20*time.Millisecond, "the preview runs")
	stop()

	a.gate.open(t)
	again, _ := boot()
	run := again.waitRun(t, name, v1.JobRunAwaitingApproval)
	require.Equal(t, 2, callsOf(t, a.previews), "the preview was called again")
	require.NotNil(t, run.Status.Preview)
	require.Zero(t, callsOf(t, a.calls))
}

// scenario: job-without-preview-export — a module that exports no preview previews the run's input.
func TestScenarioJobWithoutPreviewExport(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "plain", "export function handle() { return { ran: true }; }\n", "",
		func(s *v1.JobSpec) { s.Approval.Bypass = false })
	h.waitJob(t, "plain")
	run := h.waitRun(t, h.startRun(t, "plain", `{"op":"apply","count":2}`), v1.JobRunAwaitingApproval)
	require.JSONEq(t, `{"op":"apply","count":2}`, string(run.Status.Preview.Document), "the preview is the input")
}

// scenario: job-recreated-cancels-waiting-run — an approved run waiting for a slot ends Cancelled with JobDeleted once
// its Job is deleted and created again, and no handler is called for it.
func TestScenarioJobRecreatedCancelsWaitingRun(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	a := newApprovalJob(t, newShimRig(t, "", funcd.WithStore(st)), st)
	a.deploy(t, false, true, nil)
	t.Cleanup(func() { a.hold.open(t) })
	first, second := a.awaiting(t), a.awaiting(t)
	require.NoError(t, a.decide(t, nil, first.Name, approve(first.Status.Preview.Digest)))
	a.h.waitRun(t, string(first.Name), v1.JobRunRunning)
	require.NoError(t, a.decide(t, nil, second.Name, approve(second.Status.Preview.Digest)))
	require.Eventually(t, func() bool {
		reason, _ := runReason(a.h.getJobRun(t, string(second.Name)))
		return reason == v1.JobRunQueued
	}, runWithin, 20*time.Millisecond, "the approved run waits for the slot")

	require.NoError(t, a.h.c.Delete(context.Background(), v1.KindJob, "default", "tofu"))
	require.Eventually(t, func() bool { return a.h.getJob(t, "tofu") == nil }, runWithin, 20*time.Millisecond)
	a.deploy(t, false, false, nil)
	run := a.h.waitRun(t, string(second.Name), v1.JobRunCancelled)
	reason, msg := runReason(run)
	require.Equal(t, v1.JobRunJobDeleted, reason, msg)
	stays(t, func() bool { return callsOf(t, a.calls+"-"+string(second.Name)) == 0 }, 300*time.Millisecond,
		50*time.Millisecond, "no handler is called for the waiting run")
}
