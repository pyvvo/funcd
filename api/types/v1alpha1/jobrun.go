package v1alpha1

import (
	"encoding/json"
	"regexp"

	huma "github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// JobRun is a namespaced, status-bearing resource: one execution of a Job, which calls its handler at most once
// (ADR-0347 Decision 3). It is created through the API, as a WorkflowRun is.
type JobRun struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       JobRunSpec   `json:"spec"`
	Status     JobRunStatus `json:"status,omitempty"`
}

// JobRunSpec names the Job and carries the run input, both fixed at creation; Cancel and Reject may only turn true, and
// Approve names the preview a person approves (ADR-0348 Decisions 3 and 5).
type JobRunSpec struct {
	Job          ObjectName      `json:"job" doc:"The Job to run, in the run's namespace, fixed once the run is created. The run waits in Pending while that Job is missing or not Ready."`
	Input        json.RawMessage `json:"input,omitempty" doc:"The run input, the data of the event that the handler receives. Over job.payloadLimit bytes, or off the input schema in the Job's status.contract, it is refused when the run is created. It is fixed once the run is created."`
	Cancel       bool            `json:"cancel,omitempty" doc:"When true, the run is cancelled: a run that has not started ends Cancelled, and a running one has its worker stopped first, though its handler may have done part of its work. It may only turn true, and a run that has ended ignores it. funcdctl job cancel sets it."`
	Approve      string          `json:"approve,omitempty" pattern:"^sha256:[a-f0-9]{64}$" doc:"The digest of the preview that a person approves, status.preview.digest. A create that sets it is refused. An update may set or change it only while the run is AwaitingApproval and the digest is the run's preview's, else it is refused with 409, and only from a caller who may update the run's Job, else with 403. funcdctl job approve sets it."`
	Reject       bool            `json:"reject,omitempty" doc:"When true, the run ends Rejected and its handler never runs. A create that sets it is refused. An update may turn it true only while the run is Pending, Previewing or AwaitingApproval, and only from a caller who may update the run's Job; it may not turn false again. A rejection wins over an approval that funcd has not acted on yet. funcdctl job reject sets it."`
	RejectReason string          `json:"rejectReason,omitempty" maxLength:"512" doc:"Why the run is rejected: the message of its Ready condition once it ends Rejected. It is set with spec.reject, and a create that sets it is refused."`
}

// JobRunStatus is the run's observed state. Its Ready condition carries the reason and the message of its phase.
type JobRunStatus struct {
	Status `json:",inline"`
	// Phase shadows the embedded Status.Phase: a run has its own phases, as a WorkflowRun has.
	Phase       JobRunPhase     `json:"phase,omitempty" doc:"Where the run is: Pending while it waits for its Job or for a free slot, Previewing while its worker answers the code's preview, AwaitingApproval while it waits for a person to approve or reject that preview, and Running while its worker runs the handler. Succeeded, Failed, Cancelled and Rejected are final, and the Ready condition gives the reason."`
	JobRevision ObjectName      `json:"jobRevision,omitempty" doc:"The Revision of the Job that the run runs, pinned when it starts: an edit of the Job during the run changes nothing for it."`
	JobUID      UID             `json:"jobUID,omitempty" doc:"The UID of the run's Job, set when the run is created if the Job exists, else when it starts. A run whose Job is deleted, or re-created under a new UID, is cancelled."`
	ImageDigest string          `json:"imageDigest,omitempty" doc:"The digest of the image of the pinned Revision."`
	StartedBy   string          `json:"startedBy,omitempty" doc:"The subject of the credential that created the run, which funcd sets; empty when there was none. It records who started the run and grants nothing."`
	StartedAt   Timestamp       `json:"startedAt,omitzero" doc:"When the run started running."`
	EndedAt     Timestamp       `json:"endedAt,omitzero" doc:"When the run ended."`
	Result      json.RawMessage `json:"result,omitempty" doc:"The handler's answer, once the run Succeeded."`
	TraceID     string          `json:"traceId,omitempty" doc:"The run's W3C trace ID, 32 lowercase hex characters, the same for its preview and its run. funcdctl job logs reads the run's logs by it."`
	Preview     *JobRunPreview  `json:"preview,omitempty" doc:"The answer of the code's preview, which a person approves by its digest. Empty for a run of a Job with approval.bypass."`
	ApprovedBy  string          `json:"approvedBy,omitempty" doc:"The subject of the credential that approved the run, which funcd sets with approvedAt; empty with none. A preview made again with another document clears both."`
	ApprovedAt  Timestamp       `json:"approvedAt,omitzero" doc:"When a person approved the run's preview. funcd runs a run only on an approval it recorded."`
	RejectedBy  string          `json:"rejectedBy,omitempty" doc:"The subject of the credential that rejected the run, which funcd sets; empty with none."`
}

// JobRunPreview is the answer of a run's preview call (ADR-0348 Decision 2): the digest a person approves, the document
// and when it was made.
type JobRunPreview struct {
	Digest      string          `json:"digest" doc:"sha256: and the hex digest of the preview's bytes, which spec.approve names."`
	Document    json.RawMessage `json:"document" doc:"The JSON document that the preview answered, or the run's input when the code has no preview."`
	PreviewedAt Timestamp       `json:"previewedAt" doc:"When the preview answered. A preview older than its Job's approval.expiry is previewed again when it is approved, or when its run's slot frees after the approval."`
}

// JobRunPhase is a run's phase (ADR-0347, ADR-0348): Pending → [Previewing → AwaitingApproval → Pending] → Running →
// Succeeded | Failed | Cancelled; Rejected ends a run that never ran.
type JobRunPhase string

const (
	JobRunPending          JobRunPhase = "Pending"
	JobRunPreviewing       JobRunPhase = "Previewing"
	JobRunAwaitingApproval JobRunPhase = "AwaitingApproval"
	JobRunRunning          JobRunPhase = "Running"
	JobRunSucceeded        JobRunPhase = "Succeeded"
	JobRunFailed           JobRunPhase = "Failed"
	JobRunCancelled        JobRunPhase = "Cancelled"
	JobRunRejected         JobRunPhase = "Rejected"
)

// MaxJobRunRejectReason is the longest spec.rejectReason, in bytes (ADR-0348 Decision 5).
const MaxJobRunRejectReason = 512

// The reasons of a JobRun's Ready condition (ADR-0347 Contracts): Pending's, then Failed's, then Cancelled's.
const (
	JobRunJobNotFound          = "JobNotFound"
	JobRunJobNotReady          = "JobNotReady"
	JobRunQueued               = "Queued"
	JobRunStartFailed          = "StartFailed"
	JobRunInputSchemaMismatch  = "InputSchemaMismatch"
	JobRunHandlerFailed        = "HandlerFailed"
	JobRunPayloadLimitExceeded = "PayloadLimitExceeded"
	JobRunTimedOut             = "RunTimedOut"
	JobRunInterrupted          = "Interrupted"
	JobRunCancelledReason      = "Cancelled"
	JobRunJobDeleted           = "JobDeleted"
	JobRunRestored             = "Restored"
	// ADR-0348: a preview that answered an error, a stale approved preview made again, and a person's rejection.
	JobRunPreviewFailed  = "PreviewFailed"
	JobRunPreviewExpired = "PreviewExpired"
	JobRunRejectedReason = "Rejected"
)

// End ends the run in phase, a final one, at at: its Ready condition is True for Succeeded, else False with reason and
// message.
func (s *JobRunStatus) End(phase JobRunPhase, reason, message string, at Timestamp) {
	s.Phase, s.EndedAt = phase, at
	ready := ConditionFalse
	if phase == JobRunSucceeded {
		ready = ConditionTrue
	}
	s.Conditions.Set(Condition{Type: "Ready", Status: ready, Reason: reason, Message: message})
}

// Ended reports whether p is final: Succeeded, Failed, Cancelled or Rejected.
func (p JobRunPhase) Ended() bool {
	return p == JobRunSucceeded || p == JobRunFailed || p == JobRunCancelled || p == JobRunRejected
}

// Schema carries JobRunPhase's enum into the generated OpenAPI (ADR-0048).
func (JobRunPhase) Schema(huma.Registry) *huma.Schema {
	return enumSchema(string(JobRunPending), string(JobRunPreviewing), string(JobRunAwaitingApproval),
		string(JobRunRunning), string(JobRunSucceeded), string(JobRunFailed), string(JobRunCancelled), string(JobRunRejected))
}

// Approved reports whether a person's approval of the run's current preview is recorded: spec.approve names
// status.preview.digest and admission wrote status.approvedAt (ADR-0348 Decision 3).
func (r *JobRun) Approved() bool {
	return r.Spec.Approve != "" && r.Status.Preview != nil && r.Spec.Approve == r.Status.Preview.Digest &&
		!r.Status.ApprovedAt.IsZero()
}

// GroupVersionKind returns the constant GVK for JobRun.
func (r *JobRun) GroupVersionKind() GroupVersionKind { return KindJobRun.GVK() }

// GetStatus returns the embedded Status for the controller's write-back seam. Its Phase is unused: the run phase is
// JobRunStatus.Phase, which shadows it.
func (r *JobRun) GetStatus() *Status { return &r.Status.Status }

// Validate checks the envelope, that spec.job names a Job, that spec.approve is a sha256 digest and that
// spec.rejectReason fits 512 bytes. The input's size and its contract, and which fields a create or an update may set,
// are admission's: they need the config, the Job and the stored run.
func (r *JobRun) Validate() error {
	const op = "JobRun.Validate"
	if err := validateMeta(r.TypeMeta, &r.ObjectMeta, KindJobRun); err != nil {
		return err
	}
	if !dnsLabel.MatchString(string(r.Spec.Job)) {
		return fault.Invalidf(op, "spec.job %q is not a valid DNS-1123 label", r.Spec.Job)
	}
	if r.Spec.Approve != "" && !previewDigest.MatchString(r.Spec.Approve) {
		return fault.Invalidf(op, "spec.approve %q is not sha256: and 64 lowercase hex digits", r.Spec.Approve)
	}
	if n := len(r.Spec.RejectReason); n > MaxJobRunRejectReason {
		return fault.Invalidf(op, "spec.rejectReason is %d bytes, more than %d", n, MaxJobRunRejectReason)
	}
	return nil
}

// previewDigest is the form of a preview's digest, which spec.approve names (ADR-0348 Contracts).
var previewDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
