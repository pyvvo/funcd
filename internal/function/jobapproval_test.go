package function_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
)

// The run reconciler's side of ADR-0348: a run of a Job without approval.bypass previews in a slot, waits for a person
// holding none, and runs exactly the preview that admission recorded an approval of, within the Job's expiry.

func noBypass(s *v1.JobSpec) { s.Approval.Bypass = false }

// previewRun answers run name's preview call with document and passes it, so it waits AwaitingApproval.
func (h *runHarness) previewRun(t *testing.T, name, document string) *v1.JobRun {
	t.Helper()
	h.waitCalled(t, name)
	h.exec.answerOf(v1.ObjectName(name)) <- function.NewPreviewAnswer(json.RawMessage(document))
	h.waitEnded(t, name)
	h.reconcileRun(t, name)
	return h.getRun(t, name)
}

// approve records an approval of digest by bob, as admission and the API server write it.
func (h *runHarness) approve(t *testing.T, name, digest string) {
	t.Helper()
	h.editRun(t, name, func(r *v1.JobRun) {
		r.Spec.Approve, r.Status.ApprovedBy, r.Status.ApprovedAt = digest, "bob", v1.NewTimestamp(h.clk.Now())
	})
}

// ADR-0348 Decisions 2 to 4: a run previews first, stored Previewing with its pin and trace before its executor
// starts; the preview's answer leaves it AwaitingApproval with the preview, holding no slot; an approval of that
// preview gives it a slot, and its apply keeps the pin and the trace.
func TestJobRunPreviewsThenRunsTheApproval(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.readyJob(t, "tofu", noBypass)
	h.run(t, "tofu-a", "tofu", `{"op":"plan"}`, nil)
	h.reconcileRun(t, "tofu-a")
	previewing := h.getRun(t, "tofu-a")
	require.Equal(t, v1.JobRunPreviewing, previewing.Status.Phase)
	require.Equal(t, v1.ObjectName("tofu-v1"), previewing.Status.JobRevision)
	require.Regexp(t, "^[0-9a-f]{32}$", previewing.Status.TraceID)
	require.True(t, previewing.Status.StartedAt.IsZero(), "the run has not started running")
	h.waitCalled(t, "tofu-a")
	require.Equal(t, v1.JobRunPreviewing, h.exec.storedOf("tofu-a"), "stored Previewing before its executor starts")

	h.clk.Advance(time.Second)
	awaiting := h.previewRun(t, "tofu-a", `{"summary":"2 to add"}`)
	require.Equal(t, v1.JobRunAwaitingApproval, awaiting.Status.Phase)
	require.NotNil(t, awaiting.Status.Preview)
	require.Regexp(t, "^sha256:[a-f0-9]{64}$", awaiting.Status.Preview.Digest)
	require.JSONEq(t, `{"summary":"2 to add"}`, string(awaiting.Status.Preview.Document))
	require.Equal(t, v1.NewTimestamp(h.clk.Now()), awaiting.Status.Preview.PreviewedAt)
	h.reconcileRun(t, "tofu-a")
	require.Equal(t, awaiting.ResourceVersion, h.getRun(t, "tofu-a").ResourceVersion, "a waiting run waits with no write")

	h.approve(t, "tofu-a", awaiting.Status.Preview.Digest)
	h.reconcileRun(t, "tofu-a")
	running := h.getRun(t, "tofu-a")
	require.Equal(t, v1.JobRunRunning, running.Status.Phase)
	require.Equal(t, previewing.Status.JobRevision, running.Status.JobRevision, "the apply keeps the pin")
	require.Equal(t, previewing.Status.TraceID, running.Status.TraceID, "the apply keeps the trace")
	require.Eventually(t, func() bool { return len(h.exec.kindsOf("tofu-a")) == 2 }, 5*time.Second, time.Millisecond)
	require.Equal(t, []function.RunCall{function.CallPreview, function.CallApply}, h.exec.kindsOf("tofu-a"))
	h.exec.answerOf("tofu-a") <- function.NewRunAnswer(v1.JobRunSucceeded, "", "", json.RawMessage(`{"applied":2}`))
	h.waitEnded(t, "tofu-a")
	h.reconcileRun(t, "tofu-a")
	requireRun(t, h.getRun(t, "tofu-a"), v1.JobRunSucceeded, "")
}

// ADR-0348 Decision 1: a run of a Job with approval.bypass calls no preview and runs at once.
func TestJobRunBypassRunsAtOnce(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.readyJob(t, "hello", nil)
	h.run(t, "hello-a", "hello", "", nil)
	h.reconcileRun(t, "hello-a")
	require.Equal(t, v1.JobRunRunning, h.getRun(t, "hello-a").Status.Phase)
	h.waitCalled(t, "hello-a")
	require.Equal(t, []function.RunCall{function.CallApply}, h.exec.kindsOf("hello-a"))
	require.Nil(t, h.getRun(t, "hello-a").Status.Preview)
}

// approval.fizz ApprovedByAPerson: an approval that admission did not record, with no approvedAt, starts nothing.
func TestJobRunApprovalNotRecordedStartsNothing(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.readyJob(t, "tofu", noBypass)
	h.run(t, "tofu-a", "tofu", "", nil)
	h.reconcileRun(t, "tofu-a")
	awaiting := h.previewRun(t, "tofu-a", `{"summary":"x"}`)
	h.editRun(t, "tofu-a", func(r *v1.JobRun) { r.Spec.Approve = awaiting.Status.Preview.Digest })
	h.reconcileRun(t, "tofu-a")
	require.Equal(t, v1.JobRunAwaitingApproval, h.getRun(t, "tofu-a").Status.Phase)
	require.Len(t, h.exec.kindsOf("tofu-a"), 1, "no apply")
}

// ADR-0348 Decision 4 and approval.fizz AppliesWhatWasApproved, NoStaleApply: an approved preview older than the Job's
// expiry when the slot frees is previewed again in that slot, with PreviewExpired; the same document goes on to Running
// in that slot, another clears the approval and waits for a new one.
func TestJobRunStalePreviewIsPreviewedAgain(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		again string
		runs  bool
	}{
		"same document":    {again: `{"summary":"2 to add"}`, runs: true},
		"another document": {again: `{"summary":"3 to add"}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newRunHarness(t, 4)
			h.readyJob(t, "tofu", func(s *v1.JobSpec) { s.Approval = v1.JobApproval{Expiry: v1.Duration(time.Hour)} })
			h.run(t, "tofu-a", "tofu", "", nil)
			h.reconcileRun(t, "tofu-a")
			first := h.previewRun(t, "tofu-a", `{"summary":"2 to add"}`)

			h.clk.Advance(time.Hour + time.Second)
			h.approve(t, "tofu-a", first.Status.Preview.Digest)
			h.reconcileRun(t, "tofu-a")
			again := h.getRun(t, "tofu-a")
			requireRun(t, again, v1.JobRunPreviewing, v1.JobRunPreviewExpired)
			require.Equal(t, first.Status.TraceID, again.Status.TraceID)

			h.clk.Advance(time.Second)
			h.exec.answerOf("tofu-a") <- function.NewPreviewAnswer(json.RawMessage(c.again))
			h.waitEnded(t, "tofu-a")
			h.reconcileRun(t, "tofu-a")
			after := h.getRun(t, "tofu-a")
			require.Equal(t, v1.NewTimestamp(h.clk.Now()), after.Status.Preview.PreviewedAt)
			if c.runs {
				// oracle: docs/design/job/approval.fizz#AppliesWhatWasApproved
				require.Equal(t, v1.JobRunRunning, after.Status.Phase)
				require.Equal(t, first.Status.Preview.Digest, after.Spec.Approve)
				require.Equal(t, "bob", after.Status.ApprovedBy)
				require.Eventually(t, func() bool { return len(h.exec.kindsOf("tofu-a")) == 3 }, 5*time.Second, time.Millisecond)
				require.Equal(t, []function.RunCall{function.CallPreview, function.CallPreview, function.CallApply}, h.exec.kindsOf("tofu-a"))
				return
			}
			// oracle: docs/design/job/approval.fizz#NoStaleApply
			require.Equal(t, v1.JobRunAwaitingApproval, after.Status.Phase)
			require.NotEqual(t, first.Status.Preview.Digest, after.Status.Preview.Digest)
			require.Empty(t, after.Status.ApprovedBy)
			require.True(t, after.Status.ApprovedAt.IsZero())
			h.reconcileRun(t, "tofu-a")
			require.Equal(t, v1.JobRunAwaitingApproval, h.getRun(t, "tofu-a").Status.Phase)
			require.Len(t, h.exec.kindsOf("tofu-a"), 2, "no apply of the old approval")
		})
	}
}

// ADR-0348 Decision 4 and approval.fizz NoStaleApply (mutant no-slot-time-check): an approved run waits Queued for a
// slot, and a preview that its expiry passed while it waited is previewed again when the slot frees.
func TestJobRunExpiryIsCheckedWhenTheSlotFrees(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 1)
	h.readyJob(t, "tofu", func(s *v1.JobSpec) { s.Approval = v1.JobApproval{Expiry: v1.Duration(time.Hour)} })
	h.readyJob(t, "busy", nil)
	h.run(t, "tofu-a", "tofu", "", nil)
	h.reconcileRun(t, "tofu-a")
	first := h.previewRun(t, "tofu-a", `{"summary":"2 to add"}`)

	h.run(t, "busy-a", "busy", "", nil)
	h.reconcileRun(t, "busy-a")
	require.Equal(t, v1.JobRunRunning, h.getRun(t, "busy-a").Status.Phase, "the waiting run held no slot")
	h.approve(t, "tofu-a", first.Status.Preview.Digest)
	h.reconcileRun(t, "tofu-a")
	queued := h.getRun(t, "tofu-a")
	requireRun(t, queued, v1.JobRunPending, v1.JobRunQueued)
	require.Equal(t, first.Status.Preview.Digest, queued.Spec.Approve, "a queued run keeps its approval")

	h.clk.Advance(time.Hour + time.Second)
	h.exec.answerOf("busy-a") <- function.NewRunAnswer(v1.JobRunSucceeded, "", "", nil)
	h.waitEnded(t, "busy-a")
	h.reconcileRun(t, "busy-a")
	h.reconcileRun(t, "tofu-a")
	requireRun(t, h.getRun(t, "tofu-a"), v1.JobRunPreviewing, v1.JobRunPreviewExpired)
	require.Eventually(t, func() bool { return len(h.exec.kindsOf("tofu-a")) == 2 }, 5*time.Second, time.Millisecond)
	require.Equal(t, []function.RunCall{function.CallPreview, function.CallPreview}, h.exec.kindsOf("tofu-a"))
}

// A Job edit that sets approval.bypass while its runs wait, which no ADR clause decides (the Jobs chapter's uncovered
// row): the bypass is read from the Job when a slot frees, so an approved run that waits for one runs at once, its old
// preview unchecked, while a run AwaitingApproval keeps waiting for a person.
func TestJobRunBypassSetWhileRunsWait(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 1)
	h.readyJob(t, "tofu", func(s *v1.JobSpec) { s.Approval = v1.JobApproval{Expiry: v1.Duration(time.Hour)} })
	h.readyJob(t, "busy", nil)
	h.run(t, "tofu-a", "tofu", "", nil)
	h.reconcileRun(t, "tofu-a")
	first := h.previewRun(t, "tofu-a", `{"summary":"2 to add"}`)
	h.run(t, "tofu-w", "tofu", "", nil)
	h.reconcileRun(t, "tofu-w")
	require.Equal(t, v1.JobRunAwaitingApproval, h.previewRun(t, "tofu-w", `{"summary":"1 to change"}`).Status.Phase)

	h.run(t, "busy-a", "busy", "", nil)
	h.reconcileRun(t, "busy-a")
	require.Equal(t, v1.JobRunRunning, h.getRun(t, "busy-a").Status.Phase)
	h.approve(t, "tofu-a", first.Status.Preview.Digest)
	h.reconcileRun(t, "tofu-a")
	requireRun(t, h.getRun(t, "tofu-a"), v1.JobRunPending, v1.JobRunQueued)

	job := h.getJob(t, "tofu")
	job.Spec.Approval.Bypass = true
	_, err := h.st.Update(context.Background(), job)
	require.NoError(t, err)
	h.reconcileJob(t, "tofu")
	require.Equal(t, v1.ConditionTrue, readyOf(t, h.getJob(t, "tofu")).Status)

	h.clk.Advance(time.Hour + time.Second)
	h.exec.answerOf("busy-a") <- function.NewRunAnswer(v1.JobRunSucceeded, "", "", nil)
	h.waitEnded(t, "busy-a")
	h.reconcileRun(t, "busy-a")
	h.reconcileRun(t, "tofu-a")
	require.Equal(t, v1.JobRunRunning, h.getRun(t, "tofu-a").Status.Phase, "the approved run runs at once, its old preview unchecked")
	require.Eventually(t, func() bool { return len(h.exec.kindsOf("tofu-a")) == 2 }, 5*time.Second, time.Millisecond)
	require.Equal(t, []function.RunCall{function.CallPreview, function.CallApply}, h.exec.kindsOf("tofu-a"))
	h.reconcileRun(t, "tofu-w")
	require.Equal(t, v1.JobRunAwaitingApproval, h.getRun(t, "tofu-w").Status.Phase, "a run AwaitingApproval keeps waiting")
}

// ADR-0348 Decision 2: a Previewing run holds a slot as a Running one does, and one AwaitingApproval holds none; the
// run that leaves its slot to wait for a person reaches the runs that wait for one.
func TestJobRunPreviewHoldsASlot(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.readyJob(t, "tofu", noBypass)
	h.run(t, "tofu-a", "tofu", "", nil)
	h.run(t, "tofu-b", "tofu", "", nil)
	h.reconcileRun(t, "tofu-a")
	h.reconcileRun(t, "tofu-b")
	c := requireRun(t, h.getRun(t, "tofu-b"), v1.JobRunPending, v1.JobRunQueued)
	require.Contains(t, c.Message, "concurrency")

	awaiting := h.previewRun(t, "tofu-a", `{}`)
	require.Equal(t, []controller.Request{{GVK: v1.KindJobRun.GVK(), Namespace: "default", Name: "tofu-b"}},
		h.runs.MapRun(context.Background(), awaiting), "a run that waits for a person frees its slot")
	h.reconcileRun(t, "tofu-b")
	require.Equal(t, v1.JobRunPreviewing, h.getRun(t, "tofu-b").Status.Phase)
}

// ADR-0348 Decision 5 and approval.fizz RejectedNeverRuns: a rejection ends a Pending, Previewing or AwaitingApproval
// run Rejected with its reason, stopping a preview's worker, and wins over an approval no pass acted on yet.
func TestJobRunRejection(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.readyJob(t, "tofu", func(s *v1.JobSpec) { s.Approval.Bypass, s.Concurrency = false, 3 })
	for _, name := range []string{"tofu-p", "tofu-v", "tofu-w"} {
		h.run(t, name, "tofu", "", nil)
	}
	reject := func(name, reason string) {
		h.editRun(t, name, func(r *v1.JobRun) { r.Spec.Reject, r.Spec.RejectReason, r.Status.RejectedBy = true, reason, "carol" })
	}

	reject("tofu-p", "")
	h.reconcileRun(t, "tofu-p")
	c := requireRun(t, h.getRun(t, "tofu-p"), v1.JobRunRejected, v1.JobRunRejectedReason)
	require.Contains(t, c.Message, "rejected")

	h.reconcileRun(t, "tofu-v")
	h.waitCalled(t, "tofu-v")
	reject("tofu-v", "too wide")
	h.reconcileRun(t, "tofu-v")
	h.waitEnded(t, "tofu-v")
	h.reconcileRun(t, "tofu-v")
	c = requireRun(t, h.getRun(t, "tofu-v"), v1.JobRunRejected, v1.JobRunRejectedReason)
	require.Equal(t, "too wide", c.Message)
	require.True(t, h.exec.stoppedOf("tofu-v"), "the preview's worker is stopped")

	h.reconcileRun(t, "tofu-w")
	awaiting := h.previewRun(t, "tofu-w", `{"summary":"x"}`)
	h.approve(t, "tofu-w", awaiting.Status.Preview.Digest)
	reject("tofu-w", "after all, no")
	h.reconcileRun(t, "tofu-w")
	// oracle: docs/design/job/approval.fizz#RejectedNeverRuns
	run := h.getRun(t, "tofu-w")
	requireRun(t, run, v1.JobRunRejected, v1.JobRunRejectedReason)
	require.Equal(t, "carol", run.Status.RejectedBy)
	for _, name := range []v1.ObjectName{"tofu-p", "tofu-v", "tofu-w"} {
		require.NotContains(t, h.exec.kindsOf(name), function.CallApply, "%s: the handler never ran", name)
	}
}

// ADR-0348 Decision 2 and 5: a cancel stops a preview's worker and ends the run Cancelled, and ends a run that waits for
// a person Cancelled.
func TestJobRunCancelWhilePreviewingOrWaiting(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.readyJob(t, "tofu", func(s *v1.JobSpec) { s.Approval.Bypass, s.Concurrency = false, 2 })
	h.run(t, "tofu-v", "tofu", "", nil)
	h.run(t, "tofu-w", "tofu", "", nil)
	h.reconcileRun(t, "tofu-v")
	h.reconcileRun(t, "tofu-w")
	h.previewRun(t, "tofu-w", `{}`)
	h.waitCalled(t, "tofu-v")
	for _, name := range []string{"tofu-v", "tofu-w"} {
		h.editRun(t, name, func(r *v1.JobRun) { r.Spec.Cancel = true })
	}
	h.reconcileRun(t, "tofu-v")
	h.waitEnded(t, "tofu-v")
	h.reconcileRun(t, "tofu-v")
	requireRun(t, h.getRun(t, "tofu-v"), v1.JobRunCancelled, v1.JobRunCancelledReason)
	require.True(t, h.exec.stoppedOf("tofu-v"))
	h.reconcileRun(t, "tofu-w")
	requireRun(t, h.getRun(t, "tofu-w"), v1.JobRunCancelled, v1.JobRunCancelledReason)
}

// ADR-0348 Decision 2: Boot puts a run stored Previewing back to Pending, keeping its pin, its trace and its approval,
// so its preview is called again; a pass that meets one with no executor in this process does the same.
func TestJobRunBootPreviewsAgain(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.readyJob(t, "tofu", func(s *v1.JobSpec) { s.Approval.Bypass, s.Concurrency = false, 2 })
	h.run(t, "tofu-v", "tofu", "", func(r *v1.JobRun) {
		r.Status.Phase, r.Status.JobRevision, r.Status.TraceID = v1.JobRunPreviewing, "tofu-v1", "0123456789abcdef0123456789abcdef"
	})
	require.NoError(t, h.runs.Boot(context.Background()))
	run := h.getRun(t, "tofu-v")
	require.Equal(t, v1.JobRunPending, run.Status.Phase)
	require.Equal(t, v1.ObjectName("tofu-v1"), run.Status.JobRevision, "the pin is kept")
	require.Equal(t, "0123456789abcdef0123456789abcdef", run.Status.TraceID, "the trace is kept")
	h.reconcileRun(t, "tofu-v")
	require.Equal(t, v1.JobRunPreviewing, h.getRun(t, "tofu-v").Status.Phase)
	h.waitCalled(t, "tofu-v")
	require.Equal(t, []function.RunCall{function.CallPreview}, h.exec.kindsOf("tofu-v"), "the preview is called again")

	restarted := function.RunsWith(h.r, function.RunsConfig{MaxRunning: 4}, h.exec.launch)
	_, err := restarted.Reconcile(context.Background(), controller.Request{GVK: v1.KindJobRun.GVK(), Namespace: "default", Name: "tofu-v"})
	require.NoError(t, err)
	require.Equal(t, v1.JobRunPending, h.getRun(t, "tofu-v").Status.Phase, "a preview no executor holds is called again")
}

// ADR-0348 Decision 4: a run that holds a pin, waiting for a person or approved and waiting for a slot, ends Cancelled
// with JobDeleted when its Job is deleted or re-created, or its pin goes, before any further call.
func TestJobRunWaitingRunOfAGoneJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newRunHarness(t, 4)
	h.readyJob(t, "tofu", func(s *v1.JobSpec) { s.Approval.Bypass, s.Concurrency = false, 3 })
	for _, name := range []string{"tofu-w", "tofu-q", "tofu-r"} {
		h.run(t, name, "tofu", "", nil)
		h.reconcileRun(t, name)
		h.previewRun(t, name, `{"name":"`+name+`"}`)
	}

	require.NoError(t, h.st.Delete(ctx, v1.KindRevision.GVK(), "default", "tofu-v1", ""))
	h.reconcileRun(t, "tofu-r")
	c := requireRun(t, h.getRun(t, "tofu-r"), v1.JobRunCancelled, v1.JobRunJobDeleted)
	require.Contains(t, c.Message, "tofu-v1", "a waiting run whose pin is gone")

	q := h.getRun(t, "tofu-q")
	h.approve(t, "tofu-q", q.Status.Preview.Digest)
	require.NoError(t, h.st.Delete(ctx, v1.KindJob.GVK(), "default", "tofu", ""))
	h.readyJob(t, "tofu", noBypass)
	for _, name := range []string{"tofu-w", "tofu-q"} {
		h.reconcileRun(t, name)
		requireRun(t, h.getRun(t, name), v1.JobRunCancelled, v1.JobRunJobDeleted)
		require.Len(t, h.exec.kindsOf(v1.ObjectName(name)), 1, "%s: no call after its preview", name)
	}
}
