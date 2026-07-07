package v1alpha1

import (
	"encoding/json"

	huma "github.com/danielgtaylor/huma/v2"
	"github.com/green-0-rabbit/funcd/api/fault"
)

// WorkflowRun is a namespaced, status-bearing resource: one execution of a Workflow
// (ADR-0094, FEAT-0005/F64). Minted by `funcdctl workflow run`, a hand-applied
// manifest, or (later) a Sensor. The engine drives it to a terminal phase.
type WorkflowRun struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       WorkflowRunSpec   `json:"spec"`
	Status     WorkflowRunStatus `json:"status,omitempty"`
}

// WorkflowRunSpec is the desired state: which Workflow, the run input, and the
// declarative pause flag.
type WorkflowRunSpec struct {
	// Workflow names the Workflow in this run's namespace.
	Workflow ObjectName `json:"workflow"`
	// Input is the run input (validated against the workflow's cached contract at
	// admission); control-plane metadata, size-bounded by the engine.
	Input json.RawMessage `json:"input,omitempty"`
	// Paused requests a graceful pause: no new steps dispatch, in-flight steps finish
	// (set by `funcdctl workflow pause`, cleared by `resume`).
	Paused bool `json:"paused,omitempty"`
	// Cancel requests cancellation (ADR-0094): a declarative one-way intent, the same shape
	// as Paused. Set by `funcdctl workflow cancel`, it is observed by the run reconciler on
	// the controller workqueue (never a synchronous endpoint) which abandons in-flight work
	// and terminates the run Cancelled. Ignored once the run is already terminal.
	Cancel bool `json:"cancel,omitempty"`
	// Replay seeds this run from a finished source run's checkpoint (ADR-0107): the engine copies the
	// source's pinned spec/contract/input + the terminal steps outside the replay set, then re-runs
	// `from` + its descendants. When set, Input must be empty (the input comes from the source).
	Replay *ReplaySeed `json:"replay,omitempty"`
}

// ReplaySeed re-runs a finished source run from a chosen step (ADR-0107, F71): a new run seeded from
// the source's recorded checkpoint. Set by `funcdctl workflow replay`; observed by the run reconciler.
type ReplaySeed struct {
	// Run is the source run to replay (same namespace).
	Run ObjectName `json:"run"`
	// From is the step to re-run from; `from` and its descendants re-execute, upstream steps are reused.
	From ObjectName `json:"from"`
	// AllowDrift opts into re-running steps whose resolved artifact digest moved since the source run
	// (a re-materialized workflow) — "same data, current code". Default false ⇒ drift is rejected.
	AllowDrift bool `json:"allowDrift,omitempty"`
}

// WorkflowRunStatus is the coarse mirror of engine run state (Badger is the truth):
// the run phase (via the embedded Status) plus per-step summaries.
type WorkflowRunStatus struct {
	Status `json:",inline"`
	Steps  []RunStepStatus `json:"steps,omitempty"`
	// TraceID is the run's W3C trace (ADR-0102), mirrored from the engine record (ADR-0100) so
	// `describe` shows it and `funcdctl workflow logs <run>` (ADR-0106) resolves a run to its logs.
	TraceID string `json:"traceId,omitempty"` // 32 lowercase hex; empty ⇒ a legacy/traceless run
}

// RunStepStatus is one step's coarse execution state, including the ADR-0100 troubleshooting facts
// (timings → duration, populated attempt count, and the capped step-level failure cause).
type RunStepStatus struct {
	Name     ObjectName `json:"name"`
	Phase    StepPhase  `json:"phase,omitempty"`
	Attempts int        `json:"attempts,omitempty"`
	Revision string     `json:"revision,omitempty"`
	// StartedAt/EndedAt (unix nanos) give the step duration; Error is the raw step-level failure
	// cause, capped (Failed steps only) — the full text lives in the step's span + logs (ADR-0106).
	StartedAt int64  `json:"startedAt,omitempty"`
	EndedAt   int64  `json:"endedAt,omitempty"`
	Error     string `json:"error,omitempty"`
}

// StepPhase is a step's execution phase within a run.
type StepPhase string

const (
	StepPending   StepPhase = "Pending"
	StepRunning   StepPhase = "Running"
	StepSucceeded StepPhase = "Succeeded"
	StepFailed    StepPhase = "Failed"
	StepSkipped   StepPhase = "Skipped"
	StepCancelled StepPhase = "Cancelled"
)

// Schema carries StepPhase's enum into the generated OpenAPI (ADR-0048).
func (StepPhase) Schema(huma.Registry) *huma.Schema {
	return enumSchema(
		string(StepPending), string(StepRunning), string(StepSucceeded),
		string(StepFailed), string(StepSkipped), string(StepCancelled),
	)
}

// GroupVersionKind returns the constant GVK for WorkflowRun.
func (r *WorkflowRun) GroupVersionKind() GroupVersionKind { return KindWorkflowRun.GVK() }

// GetStatus returns the embedded Status for the controller's write-back seam.
func (r *WorkflowRun) GetStatus() *Status { return &r.Status.Status }

// Validate enforces the WorkflowRunSpec rules JSON Schema can't express (ADR-0094):
// the target workflow is named. Input-vs-contract validation is admission's job
// (it needs the cached contract), not a field rule.
func (r *WorkflowRun) Validate() error {
	const op = "WorkflowRun.Validate"
	if err := validateMeta(r.TypeMeta, &r.ObjectMeta, KindWorkflowRun); err != nil {
		return err
	}
	if r.Spec.Workflow == "" || !dnsLabel.MatchString(string(r.Spec.Workflow)) {
		return fault.Invalidf(op, "spec.workflow %q is not a valid DNS-1123 label", r.Spec.Workflow)
	}
	// Replay seed rules (ADR-0107): run/from are DNS-1123 labels, and the input must come from the
	// source run — not a second source of truth on the replay.
	if r.Spec.Replay != nil {
		if !dnsLabel.MatchString(string(r.Spec.Replay.Run)) {
			return fault.Invalidf(op, "spec.replay.run %q is not a valid DNS-1123 label", r.Spec.Replay.Run)
		}
		if !dnsLabel.MatchString(string(r.Spec.Replay.From)) {
			return fault.Invalidf(op, "spec.replay.from %q is not a valid DNS-1123 label", r.Spec.Replay.From)
		}
		if len(r.Spec.Input) > 0 {
			return fault.Invalidf(op, "spec.input must be empty when spec.replay is set (the input comes from the source run)")
		}
	}
	return nil
}
