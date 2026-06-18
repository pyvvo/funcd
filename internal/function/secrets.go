package function

import (
	"context"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
)

// SecretResolver resolves a function's bound Secret names → an env-var map for worker
// injection, PDP-authorized for id (ADR-0022/0057). The reconciler depends on this local
// seam, never on the internal/secrets feature directly (ADR-0002 import discipline) —
// satisfied by *secrets.Resolver, wired in pkg/funcd. A nil resolver on Deps disables secret
// injection: a function that declares spec.secrets then fails closed (SecretResolveFailed).
type SecretResolver interface {
	ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

// resolveSecretEnv resolves a function's bound secrets into an env-var map for worker
// injection (ADR-0057). It returns (nil, nil) when the function declares no secrets. Every
// other path is fail-closed: an unconfigured resolver, a pooled function (whose shared worker
// env can't isolate per-function secrets), or any ResolveEnv error (PDP-deny / missing Secret)
// returns an error so Reconcile holds the function not-Ready (SecretResolveFailed) with no worker.
func (r *Reconciler) resolveSecretEnv(ctx context.Context, fn *v1.Function, pooled bool) (map[string]string, error) {
	const op = "function.resolveSecretEnv"
	if len(fn.Spec.Secrets) == 0 {
		return nil, nil
	}
	if r.secrets == nil {
		return nil, fault.Invalidf(op, "secret injection is not configured but %s/%s declares %d secret(s)",
			fn.Namespace, fn.Name, len(fn.Spec.Secrets))
	}
	if pooled {
		return nil, fault.Invalidf(op, "secret injection is not supported for pooled functions in V1 (%s/%s); run it solo",
			fn.Namespace, fn.Name)
	}
	env, err := r.secrets.ResolveEnv(ctx, r.developerFor(fn.Namespace), fn.Namespace, secretNames(fn.Spec.Secrets))
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "resolve secrets")
	}
	return env, nil
}

// mergeSecretEnv merges resolved secret env vars into a worker's env with reserved-key
// precedence: a resolved key matching the FUNCD_ prefix guard is dropped (logged) so a secret
// can never shadow the shim contract (ADR-0057, reserved-env-not-overridable). Pure: it mutates
// only the passed env map.
func (r *Reconciler) mergeSecretEnv(env, secretEnv map[string]string) {
	for k, v := range secretEnv {
		if isReservedFuncdKey(k) {
			r.logger.Warn("dropping secret env key that collides with a reserved FUNCD_ key", "key", k)
			continue
		}
		env[k] = v
	}
}

// isReservedFuncdKey reports whether an env key is reserved by the runtime shim contract
// (FUNCD_ARTIFACT/HANDLER/PORT/POOL_MANIFEST/PORTFILE, …). A prefix guard (not an explicit
// set) keeps every current and future reserved key protected without editing this function —
// a resolved secret can never shadow a reserved key (ADR-0057, reserved-env-not-overridable).
func isReservedFuncdKey(k string) bool { return strings.HasPrefix(k, "FUNCD_") }

// secretNames converts the typed spec field to the []string the Resolver takes.
func secretNames(names []v1.ObjectName) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = string(n)
	}
	return out
}

// defaultDeveloperFor synthesizes the namespace-scoped developer identity the PDP read uses
// when Deps.DeveloperFor is unset (ADR-0057 Decision 4). V1 is single-tenant per namespace,
// so the developer owns (and may read) its own namespace's secrets; recording the real applier
// identity on the Function is a V2 refinement, isolated to this provider.
func defaultDeveloperFor(ns v1.NamespaceName) auth.Identity {
	return auth.Identity{
		Subject:    "system:secret-injector:" + string(ns),
		Role:       auth.RoleDeveloper,
		Namespaces: []v1.NamespaceName{ns},
	}
}
