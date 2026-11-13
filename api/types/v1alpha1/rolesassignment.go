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
	// ScopeKindLake scopes a grant to one lake serving layer ("<lake>") — ADR-0137, F102.
	ScopeKindLake ScopeKind = "Lake"
	// ScopeKindSQLDatabase scopes a grant to one SQL database ("<database>") — ADR-0233, F129.
	ScopeKindSQLDatabase ScopeKind = "SQLDatabase"
)

// PrincipalRef names the grantee: a typed {kind, name}. This is the "principal side is no longer
// Function-only" generalization (ADR-0135/0136).
type PrincipalRef struct {
	Kind PrincipalKind `json:"kind" enum:"Function,Identity" doc:"The kind of the principal: a Function, or an Identity for a caller outside funcd."`
	Name ObjectName    `json:"name" doc:"The principal's name, in the RolesAssignment's namespace. funcd does not check that it exists."`
}

// RoleRef names the role: a built-in name or a custom Role.
type RoleRef struct {
	Kind RoleRefKind `json:"kind" enum:"BuiltinRole,Role" doc:"Where the role comes from: BuiltinRole for one of funcd's built-in roles, Role for a Role of the RolesAssignment's namespace."`
	Name string      `json:"name" doc:"The role's name. A built-in role is one of Blob Data Reader, Blob Data Writer, KV Data Reader, KV Data Writer, SQL Data Reader, SQL Data Writer, Lake Query Reader, Lake Contributor, Function Invoker, Reader, Contributor and Owner; the last three cover Bucket and KV data only. A name that matches no role grants nothing."`
}

// ScopeRef names the scope: a namespace, a bucket prefix, or a kv store. Name is the scope-relative name
// ("<bucket>/<prefix>" for BucketPrefix; the namespace is the assignment's own namespace).
type ScopeRef struct {
	Kind ScopeKind `json:"kind" enum:"Namespace,BucketPrefix,KVStore,Lake,SQLDatabase" doc:"What the grant covers: with Namespace, every resource of the RolesAssignment's namespace; with another kind, the one resource that name gives, in that namespace."`
	Name string    `json:"name,omitempty" doc:"The resource for each kind: <bucket>/<prefix> for one prefix of a Bucket, <store> for every table of a KVStore or <store>/<table> for one, <lake> for a Lake and <database> for a SQLDatabase. A Namespace scope covers the whole namespace, whatever its name."`
}

// AssignmentEntry is one (principal, role, scope) grant. Principal/Scope inherit the RolesAssignment's
// top-level defaults when omitted.
type AssignmentEntry struct {
	Principal *PrincipalRef `json:"principal,omitempty" doc:"Who the entry grants its role to. When unset, spec.principal."`
	RoleRef   RoleRef       `json:"roleRef" doc:"The role the entry grants."`
	Scope     *ScopeRef     `json:"scope,omitempty" doc:"Where the entry's role applies. When unset, spec.scope."`
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
	Principal   *PrincipalRef     `json:"principal,omitempty" doc:"The principal of each entry that sets none."`
	Scope       *ScopeRef         `json:"scope,omitempty" doc:"The scope of each entry that sets none."`
	Assignments []AssignmentEntry `json:"assignments" doc:"The grants, at least one: each entry grants one role to a principal at a scope. funcd refuses an entry that sets no principal while spec.principal is unset, and likewise for the scope."`
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
		case ScopeKindNamespace, ScopeKindBucketPrefix, ScopeKindKVStore, ScopeKindLake, ScopeKindSQLDatabase:
		default:
			return fault.Invalidf(op, "spec.assignments[%d].scope.kind %q is invalid", i, s.Kind)
		}
	}
	return nil
}
