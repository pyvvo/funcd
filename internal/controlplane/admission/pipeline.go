package admission

import (
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Pipeline runs the registered admissions for a write: all Mutating (threading the object), then
// all Validating. Construct once at server start with NewPipeline; it is read-only thereafter.
type Pipeline struct {
	mutating   []Admission
	validating []Admission
}

// NewPipeline registers admissions, partitioning by Phase; registration order within a phase is
// preserved (it is the deterministic tie-break).
func NewPipeline(admissions ...Admission) *Pipeline {
	p := &Pipeline{}
	for _, a := range admissions {
		if a.Phase() == Mutating {
			p.mutating = append(p.mutating, a)
		} else {
			p.validating = append(p.validating, a)
		}
	}
	return p
}

// Handles reports whether any registered admission applies to (gvk, op) — lets a caller skip an
// Old-object fetch on Delete when nothing admits deletes.
func (p *Pipeline) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	for _, a := range p.mutating {
		if a.Handles(gvk, op) {
			return true
		}
	}
	for _, a := range p.validating {
		if a.Handles(gvk, op) {
			return true
		}
	}
	return false
}

// Admit runs all Mutating admissions (threading req.Object — each may transform it), then all
// Validating ones (each sees the final object), skipping any whose Handles is false. The first
// fault error short-circuits. Returns the final (possibly-mutated) object.
func (p *Pipeline) Admit(ctx context.Context, req Request) (v1.Object, error) {
	obj := req.Object
	for _, a := range p.mutating {
		if !a.Handles(req.GVK, req.Operation) {
			continue
		}
		r := req
		r.Object = obj
		out, err := a.Admit(ctx, r)
		if err != nil {
			return nil, err
		}
		if out != nil {
			obj = out
		}
	}
	for _, a := range p.validating {
		if !a.Handles(req.GVK, req.Operation) {
			continue
		}
		r := req
		r.Object = obj
		if _, err := a.Admit(ctx, r); err != nil {
			return nil, err
		}
	}
	return obj, nil
}
