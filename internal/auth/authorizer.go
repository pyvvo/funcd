// Package auth is the authorization kernel (ADR-0018): the Authorizer port (PDP)
// every enforcement point (PEP) calls, plus the typed principal/verb/decision model.
// V1 ships the built-in namespace-RBAC driver (internal/auth/rbac); cedar-go is the
// deferred V2 driver behind the same port. The control-plane API server is the first
// PEP; later PEPs (service facades, gateway, bus, egress) call this same port.
package auth

import (
	"context"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// Role is a namespace-scoped RBAC role.
type Role string

const (
	// RoleAdmin is cluster-wide: every verb in every namespace + cluster-scoped kinds.
	RoleAdmin Role = "admin"
	// RoleDeveloper may read+write within its bound namespaces.
	RoleDeveloper Role = "developer"
	// RoleViewer may only read within its bound namespaces.
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

// Identity is an authenticated principal (resolved by authn from a token / API key).
type Identity struct {
	Subject    string
	Role       Role
	Namespaces []v1.NamespaceName // namespaces a developer/viewer may act in; ignored for admin
}

// Request is one authorization question.
type Request struct {
	Identity  Identity
	Verb      Verb
	Kind      v1.Kind
	Namespace v1.NamespaceName // empty for cluster-scoped kinds
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
