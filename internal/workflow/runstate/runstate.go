// Package runstate is the durable-run-state PORT for the workflow engine (ADR-0094).
// The engine persists each WorkflowRun's authoritative state through this interface
// and never touches a storage backend directly, so the persistence engine is a
// swap: memory (tests/dev) and badger (production) are the V1 drivers, each in its
// own subpackage, both verified by the shared contract suite (Contract).
package runstate

import (
	"context"
	"encoding/json"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Record is the authoritative durable state of one workflow run: the pinned input,
// the per-step execution state, and the run phase. It is control-plane metadata
// (JSON-serializable, size-bounded by the engine); the engine is the single writer
// per run. Timestamps are unix nanoseconds supplied by the engine (the store holds
// no clock).
type Record struct {
	Namespace v1.NamespaceName `json:"namespace"`
	Name      v1.ObjectName    `json:"name"`
	Workflow  v1.ObjectName    `json:"workflow"`
	Input     json.RawMessage  `json:"input,omitempty"`
	// Spec is the WorkflowSpec snapshotted at run start (ADR-0094 "pinned at start"): the
	// step graph, images/digests, and per-step + run policies the run executes against. Resume
	// and recovery rebuild from THIS pinned spec, never the live Workflow — so a mid-run spec
	// edit or artifact re-push leaves an in-flight run on its pinned graph and digests.
	Spec v1.WorkflowSpec `json:"spec,omitempty"`
	// Contract is the workflow's derived I/O contract pinned at run start (ADR-0098): the run-start
	// InputSchemaMismatch check reads it, and Resume uses this copy — immune to a mid-run re-derive.
	Contract *v1.WorkflowContract `json:"contract,omitempty"`
	// StepContracts are the per-step I/O contracts pinned at run start (the ADR-0098 cache): a when:
	// binds an absent parent-output field to its schema default from them (ADR-0095).
	StepContracts map[v1.ObjectName]v1.WorkflowContract `json:"stepContracts,omitempty"`
	// Depth is the sub-workflow nesting depth (ADR-0099): 0 for a top-level run, +1 per child. The
	// engine caps it (Config.MaxSubworkflowDepth) so a reference cycle fails cleanly, not by overflow.
	Depth int `json:"depth,omitempty"`
	// TraceID / RootSpanID are the run's W3C trace context (ADR-0102): minted at run start and persisted
	// so every step dispatch (and every retry, and every resumed step) propagates the SAME traceparent —
	// one trace per run. RootSpanID surfaces as each dispatch's parent span-id (steps parent on the run
	// root). Reused by Resume, never re-minted. Empty ⇒ no traceparent (additive/legacy).
	TraceID    string `json:"traceId,omitempty"`    // 32 lowercase hex (16 bytes)
	RootSpanID string `json:"rootSpanId,omitempty"` // 16 lowercase hex (8 bytes)
	// RootParentID is the run-root span's parent (ADR-0104): "" for a top-level run; the PARENT run's
	// RootSpanID for a sub-workflow child, so a composition (parent + inline children) is one nested trace.
	RootParentID string `json:"rootParentId,omitempty"` // 16 hex; empty ⇒ the run is a trace root
	// SourceRun / SourceFrom are replay provenance (ADR-0107): when this record was seeded from a
	// finished source run, SourceRun names it and SourceFrom the step the replay re-ran from. Empty ⇒
	// an ordinary run. The record is otherwise a normal checkpoint.
	SourceRun  v1.ObjectName `json:"sourceRun,omitempty"`
	SourceFrom v1.ObjectName `json:"sourceFrom,omitempty"`
	Phase      v1.RunPhase   `json:"phase"`
	Paused     bool          `json:"paused,omitempty"`
	Steps      []StepState   `json:"steps,omitempty"`
	StartedAt  int64         `json:"startedAt,omitempty"`
	UpdatedAt  int64         `json:"updatedAt,omitempty"`
	// PausedNanos accumulates time spent paused, excluded from the run-timeout clock.
	PausedNanos int64 `json:"pausedNanos,omitempty"`
	// PausedAt is when the current pause began (unix nanos); Resume adds the interval to PausedNanos.
	PausedAt int64 `json:"pausedAt,omitempty"`
	// Error is the run's failure cause, capped like a step's (ADR-0100): set when the run ends Failed,
	// including a failure no step carries (the run-start InputSchemaMismatch gate, RunTimedOut).
	Error string `json:"error,omitempty"`
	// RunUID is the uid of the WorkflowRun that started the run. A WorkflowRun deleted and re-created
	// under the same name has a new uid, so it never adopts this record. Empty for an inline sub-workflow
	// child run and for a record written before the uid was stamped (such a record is matched by name).
	RunUID v1.UID `json:"runUid,omitempty"`
}

// StepState is one step's persisted execution state.
type StepState struct {
	Name     v1.ObjectName   `json:"name"`
	Phase    v1.StepPhase    `json:"phase"`
	Attempts int             `json:"attempts,omitempty"`
	Revision string          `json:"revision,omitempty"` // the resolved digest-pinned image this step executed (ADR-0107)
	Output   json.RawMessage `json:"output,omitempty"`   // small output or a by-reference key
	// SpanID is the engine-minted trace span-id for this step (ADR-0105): minted once at run start so a
	// successor parents on it, and restored on Resume so the edge stays stable across a restart.
	SpanID string `json:"spanId,omitempty"` // 16 hex
	// Troubleshooting lineage (ADR-0100), stamped by the engine at the step's terminal transition and
	// mirrored to WorkflowRun.status.steps[]: when the step started/ended (→ duration) and the raw
	// step-level failure cause (CAPPED to maxStatusError, Failed steps only). Attempts (above) is now
	// populated. Restored across Resume for already-terminal steps so a resumed run keeps its history.
	StartedAt int64  `json:"startedAt,omitempty"` // unix nanos
	EndedAt   int64  `json:"endedAt,omitempty"`   // unix nanos
	Error     string `json:"error,omitempty"`     // the raw step-level failure cause, capped
}

// Terminal reports whether the run phase is a terminal state (used by List OpenOnly
// and retention GC).
func (r *Record) Terminal() bool {
	switch r.Phase {
	case v1.RunSucceeded, v1.RunFailed, v1.RunCancelled:
		return true
	default:
		return false
	}
}

// ListOptions narrows a List: by namespace, by the owning workflow (the
// `funcdctl workflow runs` filter), and to non-terminal runs (recovery scan).
type ListOptions struct {
	Namespace v1.NamespaceName
	Workflow  v1.ObjectName // empty ⇒ any workflow
	OpenOnly  bool          // true ⇒ only non-terminal runs
}

// Store is the durable run-state port. Implementations are the single source of
// truth for run state; WorkflowRun.status is a derived mirror the engine writes.
type Store interface {
	// Get returns the run record, or fault.NotFound if absent.
	Get(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*Record, error)
	// Put creates or replaces a run record (the write-ahead intent precedes dispatch).
	Put(ctx context.Context, rec *Record) error
	// Delete removes a run record (retention GC); absent is not an error.
	Delete(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error
	// List returns matching records (recovery scan + workflow-filtered listing).
	List(ctx context.Context, opts ListOptions) ([]*Record, error)
	// Close releases the driver's resources.
	Close() error
}

// Clone deep-copies a record so drivers never alias caller-owned memory.
func Clone(r *Record) (*Record, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var out Record
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
