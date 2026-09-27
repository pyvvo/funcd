// Package authn is the edge authentication PEP (ADR-0113, F77): for a data-plane request whose
// target has an `authenticated` stance, it extracts a bearer token, resolves it to a
// connection-scoped Identity (never client-asserted), and DELEGATES the allow/deny decision to the
// existing auth.Authorizer (RBAC namespace-scope in V1; fine-grained Cedar edge policy in V2). It
// enforces, never decides. It is invoked INSIDE the data-plane serving path — the first action, after
// the target (ns, function) resolves and before store.Get + the activator — so a 401/403 never wakes
// a sandbox and never leaks whether a function exists.
package authn

import (
	"context"
	"net/http"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/auth"
)

const op = "edge.authn"

// Credentials resolves an opaque bearer token / API key to an Identity — a local one-method
// interface (structurally satisfied by the control-plane static store) so the edge does not import
// internal/controlplane.
type Credentials interface {
	Lookup(ctx context.Context, token string) (auth.Identity, error)
}

// Deps are the Enforcer's dependencies.
type Deps struct {
	Creds Credentials     // token → Identity (ADR-0018 scheme)
	Authz auth.Authorizer // the routing PDP (RBAC now, Cedar later)
}

// Enforcer authenticates + authorizes a data-plane request for a resolved target function.
type Enforcer struct {
	creds Credentials
	authz auth.Authorizer
}

// New builds the Enforcer.
func New(d Deps) (*Enforcer, error) {
	if d.Creds == nil {
		return nil, fault.Invalidf("authn.New", "credentials are required")
	}
	if d.Authz == nil {
		return nil, fault.Invalidf("authn.New", "authorizer is required")
	}
	return &Enforcer{creds: d.Creds, authz: d.Authz}, nil
}

// Enforce returns nil to proceed, or a fault (Unauthorized→401 / Forbidden→403) to reject. For an
// `open` stance it is a no-op (anonymous). For `authenticated` it authenticates the bearer and
// delegates the namespace-scope decision to the Authorizer.
func (e *Enforcer) Enforce(ctx context.Context, r *http.Request, target activator.FunctionRef, stance v1.AuthMode) error {
	if stance != v1.AuthAuthenticated {
		return nil // open (or unset → open)
	}
	token := bearerToken(r)
	if token == "" {
		return fault.Unauthorizedf(op, "missing bearer token or API key")
	}
	id, err := e.creds.Lookup(ctx, token)
	if err != nil {
		return fault.Unauthorizedf(op, "invalid credential")
	}
	// Delegate to the PDP: is this identity authorized for the target function's namespace? (Action
	// empty ⇒ RBAC namespace scope. Verb get: edge invoke ≈ access, coarse-by-design in V1.)
	dec, err := e.authz.Authorize(ctx, auth.Request{
		Identity:  id,
		Verb:      auth.VerbGet,
		Kind:      v1.KindFunction,
		Namespace: target.Namespace,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "authorize edge invoke")
	}
	if !dec.Allowed {
		return fault.Forbiddenf(op, "not authorized to invoke %s/%s: %s", target.Namespace, target.Name, dec.Reason)
	}
	return nil
}

// bearerToken extracts the credential (ADR-0018 scheme): Authorization: Bearer <t> (case-insensitive)
// or X-Api-Key.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		const prefix = "Bearer "
		if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
			return strings.TrimSpace(h[len(prefix):])
		}
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}
