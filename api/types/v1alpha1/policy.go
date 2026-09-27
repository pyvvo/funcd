package v1alpha1

import (
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// Policy is a namespaced authorization resource (ADR-0074): its spec is Cedar policy text (one or
// more permit/forbid statements) the cedar driver (internal/auth/cedar) compiles into the PDP's
// PolicySet. It is how an operator grants a function fine-grained resource access — e.g. permit a
// Function to kv::read a KVStore. Reads are DEFAULT-DENY: with no permitting Policy, a kv::read is
// Forbidden; owner-write is a built-in rule (not a user Policy). Structural Validate checks the
// envelope + that the text is non-empty; the Cedar parse + curated-schema check is the
// policy-validity admission (ADR-0063), which the driver relies on so a stored Policy compiles.
type Policy struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       PolicySpec `json:"spec"`
}

// PolicySpec is the desired state of a Policy (ADR-0074): the Cedar policy text.
type PolicySpec struct {
	// Cedar is one or more Cedar permit/forbid statements (validated by the policy-validity admission
	// against the curated action/entity schema — kv::read/kv::write; Function/KVStore/KVTable).
	Cedar string `json:"cedar"`
}

// GroupVersionKind returns the constant GVK for Policy.
func (p *Policy) GroupVersionKind() GroupVersionKind { return KindPolicy.GVK() }

// Validate performs envelope validation via the shared validateMeta helper, then the one structural
// rule JSON Schema can't express: spec.cedar is non-empty. The Cedar parse + curated-schema check is
// the policy-validity admission (ADR-0074), not structural Validate.
func (p *Policy) Validate() error {
	const op = "Policy.Validate"
	if err := validateMeta(p.TypeMeta, &p.ObjectMeta, KindPolicy); err != nil {
		return err
	}
	if strings.TrimSpace(p.Spec.Cedar) == "" {
		return fault.Invalidf(op, "spec.cedar must not be empty")
	}
	return nil
}
