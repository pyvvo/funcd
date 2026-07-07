// Package admission is the control-plane admission framework (ADR-0063): a two-phase pipeline
// (mutate-all → validate-all) of single-purpose Admissions run on every resource write, after
// authz and before the store. It generalizes the former inline obj.Validate() admit step
// (ADR-0018) and opens the cross-resource/Delete extension point (ADR-0064 links consume it).
//
// It is a near-leaf: it imports api/types, internal/auth, api/fault only — it nests under
// internal/controlplane but must NOT import its parent package (no cycle).
package admission

import (
	"context"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
)

// Operation is the resource write being admitted.
type Operation string

const (
	Create Operation = "create"
	Update Operation = "update"
	Delete Operation = "delete"
)

// Phase orders an admission: all Mutating run (threading the object), then all Validating.
type Phase int

const (
	// Mutating admissions may return a changed object; they run first.
	Mutating Phase = iota
	// Validating admissions are read-only checks; they run on the final object.
	Validating
)

// Request is one admission review. Object is the incoming desired object (nil on Delete); Old is
// the stored object (nil on Create; set on Update/Delete). GVK is the route's kind (authoritative,
// not body-derived). Identity is the authenticated caller (authz already passed).
type Request struct {
	Operation Operation
	GVK       v1.GroupVersionKind
	Object    v1.Object
	Old       v1.Object
	Identity  auth.Identity
}

// Admission reviews a write before it is persisted. Deterministic and side-effect-free.
// A Validating admission returns req.Object on allow, or a fault error (Invalid/Forbidden/Conflict)
// on deny. A Mutating admission returns the object to carry forward, or a fault error.
type Admission interface {
	// Name identifies the admission in errors and ordering (stable, kebab-case).
	Name() string
	// Phase is Mutating or Validating.
	Phase() Phase
	// Handles reports whether this admission applies to the given GVK + operation.
	Handles(gvk v1.GroupVersionKind, op Operation) bool
	// Admit reviews req and returns the (possibly-mutated) object, or a fault error denying the write.
	Admit(ctx context.Context, req Request) (v1.Object, error)
}
