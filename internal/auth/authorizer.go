// Package auth is the authorization kernel (ADR-0018): the Authorizer port (PDP)
// every enforcement point (PEP) calls, plus the typed principal/verb/decision model.
// V1 ships the built-in namespace-RBAC driver (internal/auth/rbac); cedar-go is the
// deferred V2 driver behind the same port. The control-plane API server is the first
// PEP; later PEPs (service facades, gateway, bus, egress) call this same port.
package auth

import (
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Role is a namespace-scoped RBAC role.
type Role string

const (
	// RoleAdmin is cluster-wide: every verb in every namespace + cluster-scoped kinds.
	RoleAdmin Role = "admin"
	// RoleDeveloper may read+write within its bound namespaces.
	RoleDeveloper Role = "developer"
	// RoleViewer may only read within its bound namespaces, and never a Secret (ADR-0171).
	RoleViewer Role = "viewer"
)

// Verb is the action a request performs.
type Verb string

const (
	VerbGet    Verb = "get"
	VerbList   Verb = "list"
	VerbCreate Verb = "create"
	VerbUpdate Verb = "update"
	VerbDelete Verb = "delete"
)

// IsWrite reports whether the verb mutates state.
func (v Verb) IsWrite() bool {
	switch v {
	case VerbCreate, VerbUpdate, VerbDelete:
		return true
	default:
		return false
	}
}

// EntityRef names a typed Cedar entity (ADR-0074): a resource kind + its
// namespace/name, plus an optional sub-resource Path (e.g. a KVTable within a
// KVStore). It is the per-object principal/resource the cedar driver evaluates.
type EntityRef struct {
	Type      v1.Kind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
	Path      string // optional sub-resource, e.g. a table name; empty for whole-object refs
}

// Action is a Cedar-namespaced action (ADR-0074), e.g. "kv::read" / "kv::write".
// A Request that sets Action asks the cedar driver a per-object question; the
// empty Action routes to the coarse RBAC driver (back-compatible).
type Action string

const (
	// ActionKVRead is the Cedar action for a KV get/list (ADR-0074).
	ActionKVRead Action = "kv::read"
	// ActionKVWrite is the Cedar action for a KV put/delete (ADR-0074).
	ActionKVWrite Action = "kv::write"
	// ActionLinkInvoke is the Cedar action for a synchronous fn→fn invoke (ADR-0075):
	// principal = the caller Function, resource = the target Function.
	ActionLinkInvoke Action = "link::invoke"
	// ActionS3Read is the Cedar action for an S3 get/list/head over the blob substrate (ADR-0080):
	// a declared spec.blob binding grants it (binding-as-read-grant). Resource = a BlobPrefix.
	ActionS3Read Action = "s3::read"
	// ActionS3Write is the Cedar action for an S3 put/delete over the blob substrate (ADR-0080):
	// single-writer — permitted only when the principal IS the prefix owner. Resource = a BlobPrefix.
	ActionS3Write Action = "s3::write"
	// ActionEgressConnect is the Cedar action for an outbound worker connection (ADR-0117, F81):
	// principal = the caller Function (source-IP resolved), resource = a NetDestination. Default-deny —
	// a grant comes only from a compiled EgressPolicy; no built-in permit.
	ActionEgressConnect Action = "egress::connect"
	// ActionCatalogQuery is the Cedar action for a catalog SQL query over the Quack/DuckLake serving
	// path (ADR-0137, F102): a declared spec.catalogs binding grants it (binding-as-query-grant), and a
	// Catalog-scoped RolesAssignment grants it to an external Identity. Resource = a CatalogService.
	// Read-shaped (a Cedar permit, never a writers entry). The catalog PEP proxy is the enforcement point.
	ActionCatalogQuery Action = "catalog::query"
)

// Identity is an authenticated principal (resolved by authn from a token / API key).
type Identity struct {
	Subject    string
	Role       Role
	Namespaces []v1.NamespaceName // namespaces a developer/viewer may act in; ignored for admin
	// Principal is the Cedar principal (ADR-0074), e.g. Function::"<ns>/<name>", set
	// connection-scoped from the sandbox Ref (never client-asserted). nil ⇒ RBAC-only.
	Principal *EntityRef
}

// Request is one authorization question.
type Request struct {
	Identity  Identity
	Verb      Verb
	Kind      v1.Kind
	Namespace v1.NamespaceName // empty for cluster-scoped kinds
	// Action, when set, makes this a Cedar per-object decision (ADR-0074): the routing
	// authorizer dispatches it to the cedar driver. Empty ⇒ the coarse RBAC form above.
	Action Action
	// Resource is the target entity for a Cedar per-object decision (ADR-0074).
	Resource *EntityRef
}

// Decision is the PDP's answer; the PEP maps !Allowed to fault.Forbidden.
type Decision struct {
	Allowed bool
	Reason  string
}

// Authorizer is the single decision point (PDP) every PEP calls. Default-deny.
type Authorizer interface {
	Authorize(ctx context.Context, req Request) (Decision, error)
}
