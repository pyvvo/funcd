// Package rbac is the built-in namespace-scoped RBAC Authorizer driver (ADR-0018):
// admin / developer / viewer roles, default-deny. It is the V1 PDP engine; cedar-go
// is the deferred V2 driver behind the same auth.Authorizer port. Zero-dependency.
package rbac

import (
	"context"
	"fmt"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

type driver struct{}

// New returns the built-in namespace-scoped RBAC Authorizer (default-deny).
func New() auth.Authorizer {
	return driver{}
}

// Authorize applies the built-in RBAC rules. It never errors (the decision is total);
// the error return exists for the port (a policy-engine driver may fail to evaluate).
func (driver) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	clusterScoped := !req.Kind.Namespaced()

	switch req.Identity.Role {
	case auth.RoleAdmin:
		// Admin is cluster-wide: every verb, every namespace, incl. cluster-scoped kinds.
		return allow(), nil
	case auth.RoleDeveloper:
		if clusterScoped {
			return deny("developer may not act on cluster-scoped kind %q", req.Kind), nil
		}
		if !inNamespace(req.Identity, req.Namespace) {
			return deny("developer not scoped to namespace %q", req.Namespace), nil
		}
		return allow(), nil
	case auth.RoleViewer:
		if clusterScoped {
			return deny("viewer may not act on cluster-scoped kind %q", req.Kind), nil
		}
		// A credential is not read-only data: the viewer never reads a Secret (ADR-0171 Decision 9).
		if req.Kind == v1.KindSecret {
			return deny("viewer may not act on kind %q", req.Kind), nil
		}
		if !inNamespace(req.Identity, req.Namespace) {
			return deny("viewer not scoped to namespace %q", req.Namespace), nil
		}
		if req.Verb != auth.VerbGet && req.Verb != auth.VerbList {
			return deny("viewer is read-only (verb %q)", req.Verb), nil
		}
		return allow(), nil
	default:
		return deny("unknown role %q", req.Identity.Role), nil
	}
}

func inNamespace(id auth.Identity, ns v1.NamespaceName) bool {
	for _, n := range id.Namespaces {
		if n == ns {
			return true
		}
	}
	return false
}

func allow() auth.Decision { return auth.Decision{Allowed: true} }

func deny(format string, a ...any) auth.Decision {
	return auth.Decision{Allowed: false, Reason: fmt.Sprintf(format, a...)}
}
