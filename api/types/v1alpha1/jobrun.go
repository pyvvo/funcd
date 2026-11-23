package v1alpha1

import (
	"encoding/json"

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

// JobRunSpec names the Job and carries the run input, both fixed at creation; Cancel may only turn true.
type JobRunSpec struct {
	Job    ObjectName      `json:"job" doc:"The Job to run, in the run's namespace, fixed once the run is created. The run waits in Pending while that Job is missing or not Ready."`
	Input  json.RawMessage `json:"input,omitempty" doc:"The run input, the data of the event that the handler receives. Over job.payloadLimit bytes, or off the input schema in the Job's status.contract, it is refused when the run is created. It is fixed once the run is created."`
	Cancel bool            `json:"cancel,omitempty" doc:"When true, the run is cancelled: a run that has not started ends Cancelled, and a running one has its worker stopped first, though its handler may have done part of its work. It may only turn true, and a run that has ended ignores it. funcdctl job cancel sets it."`
}

// JobRunStatus is the run's observed state. Its Ready condition carries the reason and the message of its phase.
type JobRunStatus struct {
	Status `json:",inline"`
	// Phase shadows the embedded Status.Phase: a run has its own phases, as a WorkflowRun has.
	Phase       JobRunPhase     `json:"phase,omitempty" doc:"Where the run is: Pending while it waits for its Job or for a free slot, Running while its worker runs the handler. Succeeded, Failed and Cancelled are final, and the Ready condition gives the reason."`
	JobRevision ObjectName      `json:"jobRevision,omitempty" doc:"The Revision of the Job that the run runs, pinned when it starts: an edit of the Job during the run changes nothing for it."`
	JobUID      UID             `json:"jobUID,omitempty" doc:"The UID of the run's Job, set when the run is created if the Job exists, else when it starts. A run whose Job is deleted, or re-created under a new UID, is cancelled."`
	ImageDigest string          `json:"imageDigest,omitempty" doc:"The digest of the image of the pinned Revision."`
	StartedBy   string          `json:"startedBy,omitempty" doc:"The subject of the credential that created the run, which funcd sets; empty when there was none. It records who started the run and grants nothing."`
	StartedAt   Timestamp       `json:"startedAt,omitzero" doc:"When the run started running."`
	EndedAt     Timestamp       `json:"endedAt,omitzero" doc:"When the run ended."`
	Result      json.RawMessage `json:"result,omitempty" doc:"The handler's answer, once the run Succeeded."`
	TraceID     string          `json:"traceId,omitempty" doc:"The run's W3C trace ID, 32 lowercase hex characters. funcdctl job logs reads the run's logs by it."`
}

// JobRunPhase is a run's phase (ADR-0347): Pending → Running → Succeeded | Failed | Cancelled.
type JobRunPhase string

const (
	JobRunPending   JobRunPhase = "Pending"
	JobRunRunning   JobRunPhase = "Running"
	JobRunSucceeded JobRunPhase = "Succeeded"
	JobRunFailed    JobRunPhase = "Failed"
	JobRunCancelled JobRunPhase = "Cancelled"
)

// Schema carries JobRunPhase's enum into the generated OpenAPI (ADR-0048).
func (JobRunPhase) Schema(huma.Registry) *huma.Schema {
	return enumSchema(string(JobRunPending), string(JobRunRunning), string(JobRunSucceeded), string(JobRunFailed),
		string(JobRunCancelled))
}

// GroupVersionKind returns the constant GVK for JobRun.
func (r *JobRun) GroupVersionKind() GroupVersionKind { return KindJobRun.GVK() }

// GetStatus returns the embedded Status for the controller's write-back seam. Its Phase is unused: the run phase is
// JobRunStatus.Phase, which shadows it.
func (r *JobRun) GetStatus() *Status { return &r.Status.Status }

// Validate checks the envelope and that spec.job names a Job. The input's size and its contract, and the fields fixed
// at creation, are admission's: they need the config, the Job and the stored run.
func (r *JobRun) Validate() error {
	if err := validateMeta(r.TypeMeta, &r.ObjectMeta, KindJobRun); err != nil {
		return err
	}
	if !dnsLabel.MatchString(string(r.Spec.Job)) {
		return fault.Invalidf("JobRun.Validate", "spec.job %q is not a valid DNS-1123 label", r.Spec.Job)
	}
	return nil
}
