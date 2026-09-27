package admission

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth/cedar"
)

// --- policy-validity (ADR-0074): Create/Update on Policy ----------------------------------------

type policyValidity struct{}

// NewPolicyValidityAdmission returns the Validating admission that enforces ADR-0074's Policy rules
// on a Policy Create/Update: spec.cedar PARSES as Cedar and references only the curated actions +
// entity types (kv::read/kv::write; Function/KVStore/KVTable). An un-parseable or off-schema Policy
// is rejected (fault.Invalid) before it reaches the metastore — so the cedar driver can rely on
// every stored Policy compiling. (Shape cloned from the link/KV validity admissions.)
func NewPolicyValidityAdmission() Admission { return policyValidity{} }

func (policyValidity) Name() string { return "policy-validity" }
func (policyValidity) Phase() Phase { return Validating }

func (policyValidity) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindPolicy.GVK() && (op == Create || op == Update)
}

func (policyValidity) Admit(_ context.Context, req Request) (v1.Object, error) {
	const op = "admission.policy-validity"
	pol, ok := req.Object.(*v1.Policy)
	if !ok {
		return req.Object, nil
	}
	if err := cedar.ValidateCedar(pol.Spec.Cedar); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "Policy %q has invalid Cedar", pol.Name)
	}
	return req.Object, nil
}
