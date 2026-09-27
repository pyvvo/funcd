package v1alpha1

import (
	"github.com/pyvvo/funcd/api/fault"
)

// PrincipalKind is the kind of a principal a role is assigned to.
type PrincipalKind string

const (
	// PrincipalKindFunction is a Function's system-assigned identity.
	PrincipalKindFunction PrincipalKind = "Function"
	// PrincipalKindIdentity is a user-assigned managed Identity (ADR-0135).
	PrincipalKindIdentity PrincipalKind = "Identity"
)

// RoleRefKind selects a built-in role by name or a custom Role resource.
type RoleRefKind string

const (
	// RoleRefKindBuiltin references a built-in role by name (e.g. "Blob Data Writer").
	RoleRefKindBuiltin RoleRefKind = "BuiltinRole"
	// RoleRefKindRole references a custom Role resource by name.
	RoleRefKindRole RoleRefKind = "Role"
)

// ScopeKind is the granularity a grant applies at.
type ScopeKind string

const (
	// ScopeKindNamespace scopes a grant to every resource in a namespace (inherits down).
	ScopeKindNamespace ScopeKind = "Namespace"
	// ScopeKindBucketPrefix scopes a grant to one blob prefix ("<bucket>/<prefix>").
	ScopeKindBucketPrefix ScopeKind = "BucketPrefix"
	// ScopeKindKVStore scopes a grant to one KV store (or "<store>/<table>").
	ScopeKindKVStore ScopeKind = "KVStore"
	// ScopeKindCatalog scopes a grant to one catalog serving layer ("<catalog>") — ADR-0137, F102.
	ScopeKindCatalog ScopeKind = "Catalog"
)

// PrincipalRef names the grantee: a typed {kind, name}. This is the "principal side is no longer
// Function-only" generalization (ADR-0135/0136).
type PrincipalRef struct {
	Kind PrincipalKind `json:"kind" enum:"Function,Identity"`
	Name ObjectName    `json:"name"`
}

// RoleRef names the role: a built-in name or a custom Role.
type RoleRef struct {
	Kind RoleRefKind `json:"kind" enum:"BuiltinRole,Role"`
	Name string      `json:"name"`
}

// ScopeRef names the scope: a namespace, a bucket prefix, or a kv store. Name is the scope-relative name
// ("<bucket>/<prefix>" for BucketPrefix; the namespace is the assignment's own namespace).
type ScopeRef struct {
	Kind ScopeKind `json:"kind" enum:"Namespace,BucketPrefix,KVStore,Catalog"`
	Name string    `json:"name,omitempty"`
}

// AssignmentEntry is one (principal, role, scope) grant. Principal/Scope inherit the RolesAssignment's
// top-level defaults when omitted.
type AssignmentEntry struct {
	Principal *PrincipalRef `json:"principal,omitempty"`
	RoleRef   RoleRef       `json:"roleRef"`
	Scope     *ScopeRef     `json:"scope,omitempty"`
}

// RolesAssignment grants MANY roles in one object (ADR-0136, FEAT-0008/F101): each entry is a
// (principal, role, scope) triple with optional top-level principal/scope defaults — no per-role sprawl.
// A pure value-type (no status): the PDP compiles read/query/invoke entries to Cedar permits and the
// write entries to the single-writer `writers` set.
type RolesAssignment struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       RolesAssignmentSpec `json:"spec"`
}

// RolesAssignmentSpec carries the default principal/scope + the assignment entries.
type RolesAssignmentSpec struct {
	Principal   *PrincipalRef     `json:"principal,omitempty"`
	Scope       *ScopeRef         `json:"scope,omitempty"`
	Assignments []AssignmentEntry `json:"assignments"`
}

// GroupVersionKind returns the constant GVK for RolesAssignment.
func (ra *RolesAssignment) GroupVersionKind() GroupVersionKind { return KindRolesAssignment.GVK() }

// Validate performs envelope + structural validation: every entry resolves a principal + role + scope
// (own or default), with valid kinds.
func (ra *RolesAssignment) Validate() error {
	const op = "RolesAssignment.Validate"
	if err := validateMeta(ra.TypeMeta, &ra.ObjectMeta, KindRolesAssignment); err != nil {
		return err
	}
	if len(ra.Spec.Assignments) == 0 {
		return fault.Invalidf(op, "spec.assignments must have at least one entry")
	}
	for i, e := range ra.Spec.Assignments {
		p := e.Principal
		if p == nil {
			p = ra.Spec.Principal
		}
		if p == nil || p.Name == "" {
			return fault.Invalidf(op, "spec.assignments[%d] has no principal (and no top-level default)", i)
		}
		if p.Kind != PrincipalKindFunction && p.Kind != PrincipalKindIdentity {
			return fault.Invalidf(op, "spec.assignments[%d].principal.kind %q is invalid", i, p.Kind)
		}
		if e.RoleRef.Name == "" {
			return fault.Invalidf(op, "spec.assignments[%d].roleRef.name is required", i)
		}
		if e.RoleRef.Kind != RoleRefKindBuiltin && e.RoleRef.Kind != RoleRefKindRole {
			return fault.Invalidf(op, "spec.assignments[%d].roleRef.kind %q is invalid", i, e.RoleRef.Kind)
		}
		s := e.Scope
		if s == nil {
			s = ra.Spec.Scope
		}
		if s == nil {
			return fault.Invalidf(op, "spec.assignments[%d] has no scope (and no top-level default)", i)
		}
		switch s.Kind {
		case ScopeKindNamespace, ScopeKindBucketPrefix, ScopeKindKVStore, ScopeKindCatalog:
		default:
			return fault.Invalidf(op, "spec.assignments[%d].scope.kind %q is invalid", i, s.Kind)
		}
	}
	return nil
}
