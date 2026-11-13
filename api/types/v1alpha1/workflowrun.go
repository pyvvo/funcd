package v1alpha1

import (
	"encoding/json"

	huma "github.com/danielgtaylor/huma/v2"
	"github.com/pyvvo/funcd/api/fault"
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
	Workflow ObjectName `json:"workflow" doc:"The Workflow to run, in the run's namespace, fixed once the run is created. The run stays Pending while that Workflow, a child it calls or a step's Function is missing or not ready."`
	// Input is the run input (validated against the workflow's cached contract at
	// admission); control-plane metadata, size-bounded by the engine.
	Input json.RawMessage `json:"input,omitempty" doc:"The run input: the first step receives it, and expressions read it as input. Over workflow.payloadLimit bytes, or off the input schema in the Workflow's status.contract, it is refused at apply or fails the run when it starts. It is fixed once the run is created; a replay sets none and takes its source's."`
	// Paused requests a graceful pause: no new steps dispatch, in-flight steps finish
	// (set by `funcdctl workflow pause`, cleared by `resume`). Ignored once the run is already
	// terminal: the run keeps its phase.
	Paused bool `json:"paused,omitempty" doc:"When true, the run pauses: no step or retry starts, the calls in flight finish, and paused time does not count toward the run's timeout. A run created paused waits to start, and a finished run ignores it. funcdctl workflow pause and resume set and clear it."`
	// Cancel requests cancellation (ADR-0094, ADR-0146): a declarative one-way intent, the same shape
	// as Paused. Set by `funcdctl workflow cancel`, it is observed by the run reconciler on
	// the controller workqueue (never a synchronous endpoint), which closes the in-flight step calls
	// (their late results are discarded), dispatches nothing more, runs no onFailure and terminates
	// the run Cancelled. Ignored once the run is already terminal.
	Cancel bool `json:"cancel,omitempty" doc:"When true, the run is cancelled: nothing more starts, the calls in flight are closed and their late answers dropped, though a call may already have done its work, and the run ends Cancelled without calling its onFailure step. It wins over paused, a finished run ignores it, and funcdctl workflow cancel sets it."`
	// Replay seeds this run from a finished source run's checkpoint (ADR-0107): the engine copies the
	// source's pinned spec/contract/input + the terminal steps outside the replay set, then re-runs
	// `from` + its descendants. When set, Input must be empty (the input comes from the source).
	Replay *ReplaySeed `json:"replay,omitempty" doc:"Starts the run from a finished run, as funcdctl workflow replay does: replay.from and each step that depends on it run again, and the others keep the source's outputs. It is fixed once the run is created and leaves input empty; a source that cannot seed the run fails it, and its ReplaySeeded condition says why."`
}

// ReplaySeed re-runs a finished source run from a chosen step (ADR-0107, F71): a new run seeded from
// the source's recorded checkpoint. Set by `funcdctl workflow replay`; observed by the run reconciler.
type ReplaySeed struct {
	// Run is the source run to replay (same namespace).
	Run ObjectName `json:"run" doc:"The finished run to replay: a run of the same Workflow in this namespace that workflow.retention has not removed yet."`
	// From is the step to re-run from; `from` and its descendants re-execute, upstream steps are reused.
	From ObjectName `json:"from" doc:"The step to re-run from: it and every step that depends on it run again, a step the source never ran runs too, and the others keep the source's outputs. It is not the onFailure step, and no step outside that set may have failed or been cancelled in the source."`
	// AllowDrift opts into re-running steps whose resolved artifact digest moved since the source run
	// (a re-materialized workflow) — "same data, current code". Default false ⇒ drift is rejected.
	AllowDrift bool `json:"allowDrift,omitempty" doc:"Lets the replay re-run an image step whose artifact digest changed since the source run, on the current artifact; a child workflow's steps count too. false, the default, fails the replay with DigestDrift instead."`
}

// WorkflowRunStatus is the coarse mirror of engine run state (Badger is the truth):
// the run phase plus per-step summaries.
type WorkflowRunStatus struct {
	Status `json:",inline"`
	// Phase shadows the embedded Status.Phase: a run has its own phases (ADR-0094), not the resource ones.
	Phase RunPhase        `json:"phase,omitempty" doc:"Where the run is in its lifecycle: Pending until it starts. Succeeded, Failed and Cancelled are final."`
	Steps []RunStepStatus `json:"steps,omitempty" doc:"Each step of the run in spec order, the onFailure step included: its phase, attempts, times and error."`
	// TraceID is the run's W3C trace (ADR-0102), mirrored from the engine record (ADR-0100) so
	// `describe` shows it and `funcdctl workflow logs <run>` (ADR-0106) resolves a run to its logs.
	TraceID string `json:"traceId,omitempty" doc:"The run's W3C trace ID, 32 lowercase hex characters, which every step call joins, a child workflow's included. funcdctl workflow logs reads the run's logs by it."` // 32 lowercase hex; empty ⇒ a legacy/traceless run
	// Pins binds the run to the step Function revisions it started with (ADR-0190): written before the run starts,
	// they hold those revisions while the run is not terminal.
	Pins []RevisionPin `json:"pins,omitempty" doc:"The Function revisions the run calls, pinned before it starts: one for each Function its steps call, ref targets and child workflows' steps included. Until the run ends or is cancelled, funcd keeps these revisions, and an edit to the Workflow or a Function does not change the code the run calls."`
	// WorkflowUID is the Workflow's UID at start (ADR-0190 Decision 10); empty for a run started before ADR-0190.
	WorkflowUID UID `json:"workflowUID,omitempty" doc:"The UID of the Workflow when the run started. A started run whose Workflow is deleted, or re-created under a new UID, is cancelled."`
}

// RevisionPin binds a run to one step Function revision (ADR-0190).
type RevisionPin struct {
	Function    ObjectName `json:"function" doc:"The name of a Function the run's steps call."`
	FunctionUID UID        `json:"functionUID" doc:"That Function's UID when the run started: a Function re-created under the name is not the pinned one, and a step that calls it fails."`
	Revision    ObjectName `json:"revision" doc:"The Revision the run calls: the Function's current revision when the run started."`
	ImageDigest string     `json:"imageDigest,omitempty" doc:"The digest of that Revision's image; empty for a file:// artifact, which has none."` // empty only for a file:// artifact
}

// RunPhase is a run's execution phase (ADR-0094): Pending → Running ⇄ Paused → Succeeded | Failed | Cancelled.
type RunPhase string

const (
	RunPending   RunPhase = "Pending"
	RunRunning   RunPhase = "Running"
	RunPaused    RunPhase = "Paused"
	RunSucceeded RunPhase = "Succeeded"
	RunFailed    RunPhase = "Failed"
	RunCancelled RunPhase = "Cancelled"
)

// Schema carries RunPhase's enum into the generated OpenAPI (ADR-0048).
func (RunPhase) Schema(huma.Registry) *huma.Schema {
	return enumSchema(
		string(RunPending), string(RunRunning), string(RunPaused),
		string(RunSucceeded), string(RunFailed), string(RunCancelled),
	)
}

// RunStepStatus is one step's coarse execution state, including the ADR-0100 troubleshooting facts
// (timings → duration, populated attempt count, and the capped step-level failure cause).
type RunStepStatus struct {
	Name     ObjectName `json:"name" doc:"The step's name."`
	Phase    StepPhase  `json:"phase,omitempty" doc:"Where the step is. It is Skipped when its when.condition is false, its join cannot be met or a step it depends on failed; a running step that a failure elsewhere stops goes back to Pending."`
	Attempts int        `json:"attempts,omitempty" doc:"The calls made to the step's Function, each carrying its number in the X-Funcd-Attempt header; 0 for a builtin or workflow step."`
	Revision string     `json:"revision,omitempty" doc:"The step's image with the digest it resolved to when the run started; empty for a builtin or workflow step. A replay compares it to detect a changed artifact."`
	// StartedAt/EndedAt (ADR-0196) give the step duration; Error is the raw step-level failure
	// cause, capped — the full text lives in the step's span + logs (ADR-0106) — on a Failed step, or
	// the cancel on a step that was running when the run was cancelled (ADR-0146).
	StartedAt Timestamp `json:"startedAt,omitzero" doc:"When the step started running."`
	EndedAt   Timestamp `json:"endedAt,omitzero" doc:"When the step succeeded, failed or was cancelled while running."`
	Error     string    `json:"error,omitempty" doc:"Why the step failed, or that it was cancelled while running: the first line of the cause, cut at 512 bytes."`
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

// GetStatus returns the embedded Status for the controller's write-back seam. Its Phase is unused: the run
// phase is WorkflowRunStatus.Phase, which shadows it.
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
