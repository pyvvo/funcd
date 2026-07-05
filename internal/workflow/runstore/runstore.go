// Package runstore is the durable-run-state PORT for the workflow engine (ADR-0094).
// The engine persists each WorkflowRun's authoritative state through this interface
// and never touches a storage backend directly, so the persistence engine is a
// swap: memory (tests/dev) and badger (production) are the V1 drivers, each in its
// own subpackage, both verified by the shared contract suite (Contract).
package runstore

import (
	"context"
	"encoding/json"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
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
	Phase     v1.Phase         `json:"phase"`
	Paused    bool             `json:"paused,omitempty"`
	Steps     []StepState      `json:"steps,omitempty"`
	StartedAt int64            `json:"startedAt,omitempty"`
	UpdatedAt int64            `json:"updatedAt,omitempty"`
	// PausedNanos accumulates time spent paused, excluded from the run-timeout clock.
	PausedNanos int64 `json:"pausedNanos,omitempty"`
}

// StepState is one step's persisted execution state.
type StepState struct {
	Name     v1.ObjectName   `json:"name"`
	Phase    v1.StepPhase    `json:"phase"`
	Attempts int             `json:"attempts,omitempty"`
	Revision string          `json:"revision,omitempty"` // pinned digest it executed
	Output   json.RawMessage `json:"output,omitempty"`   // small output or a by-reference key
}

// Terminal reports whether the run phase is a terminal state (used by List OpenOnly
// and retention GC).
func (r *Record) Terminal() bool {
	switch r.Phase {
	case "Succeeded", v1.PhaseFailed, "Cancelled":
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
