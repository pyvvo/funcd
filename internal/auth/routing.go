package auth

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
)

// routing is the dispatch Authorizer (ADR-0074): an Action-bearing Request (a Cedar
// per-object question, e.g. kv::read on a KVTable) goes to the cedar driver; the coarse
// verb/kind/namespace form goes to the RBAC driver. This keeps the control-plane CRUD path
// on RBAC unchanged while data-plane resource access is decided by Cedar — one PDP port,
// two drivers behind it. Default-deny is preserved: each driver is itself default-deny.
type routing struct {
	rbac  Authorizer
	cedar Authorizer
}

// NewRoutingAuthorizer returns the Authorizer that routes Action-bearing requests to cedar
// and coarse requests to rbac (ADR-0074). Both are required. If cedar is nil, Action-bearing
// requests are denied (fail-closed), preserving default-deny.
func NewRoutingAuthorizer(rbac, cedar Authorizer) (Authorizer, error) {
	if rbac == nil {
		return nil, fault.Invalidf("auth.NewRoutingAuthorizer", "rbac authorizer is required")
	}
	return routing{rbac: rbac, cedar: cedar}, nil
}

// Authorize dispatches by request shape: Action set ⇒ cedar (per-object); else ⇒ rbac (coarse).
func (a routing) Authorize(ctx context.Context, req Request) (Decision, error) {
	if req.Action != "" {
		if a.cedar == nil {
			// Fail closed: an Action-bearing request with no cedar driver is denied.
			return Decision{Allowed: false, Reason: "no cedar driver configured for per-object authorization"}, nil
		}
		return a.cedar.Authorize(ctx, req)
	}
	return a.rbac.Authorize(ctx, req)
}
