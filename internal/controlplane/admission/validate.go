package admission

import (
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// validateAdmission runs each object's own Validate() — the envelope + cross-field rules
// (ADR-0003 + the per-type Validate methods). It is the built-in Validating admission that
// absorbs the control plane's former inline obj.Validate() call (ADR-0063 refines ADR-0018).
type validateAdmission struct{}

// NewValidateAdmission returns the built-in Validating admission that runs obj.Validate()
// for every kind on Create and Update.
func NewValidateAdmission() Admission { return validateAdmission{} }

func (validateAdmission) Name() string { return "validate" }

func (validateAdmission) Phase() Phase { return Validating }

func (validateAdmission) Handles(_ v1.GroupVersionKind, op Operation) bool {
	return op == Create || op == Update
}

func (validateAdmission) Admit(_ context.Context, req Request) (v1.Object, error) {
	if err := req.Object.Validate(); err != nil {
		return nil, err // already a fault.Invalid
	}
	return req.Object, nil
}
