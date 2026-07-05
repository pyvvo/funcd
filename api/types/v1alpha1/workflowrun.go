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
}

// WorkflowRunStatus is the coarse mirror of engine run state (Badger is the truth):
// the run phase (via the embedded Status) plus per-step summaries.
type WorkflowRunStatus struct {
	Status `json:",inline"`
	Steps  []RunStepStatus `json:"steps,omitempty"`
}

// RunStepStatus is one step's coarse execution state.
type RunStepStatus struct {
	Name     ObjectName `json:"name"`
	Phase    StepPhase  `json:"phase,omitempty"`
	Attempts int        `json:"attempts,omitempty"`
	Revision string     `json:"revision,omitempty"`
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
	return nil
}
