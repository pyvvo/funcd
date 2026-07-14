package v1alpha1

import (
	"github.com/green-0-rabbit/funcd/api/fault"
)

// Role is a named, reusable permission set (ADR-0136, FEAT-0008/F101): a set of funcd data-plane action
// strings a RolesAssignment grants to a principal at a scope. Custom roles complement the fixed built-in
// catalog (Blob Data Reader/Writer, KV Data Reader/Writer, Function Invoker, Reader/Contributor/Owner).
// A pure value-type (no status), like Grant — the PDP compiles it, the reconciler does not touch it.
type Role struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       RoleSpec `json:"spec"`
}

// RoleSpec is the action set. Actions are funcd data-plane action strings ("s3::read", "s3::write",
// "kv::read", "kv::write", "link::invoke") — kept as strings so api/types imports no internal package.
type RoleSpec struct {
	Actions []string `json:"actions"`
}

// GroupVersionKind returns the constant GVK for Role.
func (r *Role) GroupVersionKind() GroupVersionKind { return KindRole.GVK() }

// Validate performs envelope + structural validation: at least one action.
func (r *Role) Validate() error {
	const op = "Role.Validate"
	if err := validateMeta(r.TypeMeta, &r.ObjectMeta, KindRole); err != nil {
		return err
	}
	if len(r.Spec.Actions) == 0 {
		return fault.Invalidf(op, "spec.actions must list at least one action")
	}
	return nil
}
