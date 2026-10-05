package function

import (
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/envresolve"
	"github.com/pyvvo/funcd/internal/secrets"
)

// SecretResolver resolves a function's bound Secret names → an env-var map for worker
// injection, PDP-authorized for id (ADR-0022/0057). The reconciler depends on this local
// seam for resolution (ADR-0002 import discipline) — satisfied by *secrets.Resolver, wired in
// pkg/funcd; it uses internal/secrets only for the pure reserved-key guard (MergeEnvGuarded,
// no store dep, ADR-0092). A nil resolver on Deps disables secret injection: a function that
// declares spec.secrets then fails closed (SecretResolveFailed).
type SecretResolver interface {
	ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

// resolveBindingEnv resolves a function's bound ConfigMaps (spec.config, non-sensitive) +
// Secrets (spec.secrets, sensitive) into one guarded env-var map for worker injection, via the
// single shared resolver (ADR-0093, envresolve.ResolveEnv — config first, then secrets). It
// returns (nil, nil) when the function declares neither. Every other path is fail-closed: any
// resolver error (a missing ConfigMap → ErrConfig,
// or a PDP-deny / missing Secret / unconfigured resolver → ErrSecret) returns an error so
// Reconcile holds the function not-Ready (ConfigResolveFailed / SecretResolveFailed) with no worker.
func (r *Reconciler) resolveBindingEnv(ctx context.Context, fn *v1.Function) (map[string]string, error) {
	if len(fn.Spec.Config) == 0 && len(fn.Spec.Secrets) == 0 {
		return nil, nil
	}
	env, err := envresolve.ResolveEnv(ctx, envresolve.Deps{
		Secrets:  r.secrets,
		Store:    r.store,
		Identity: r.developerFor,
		Logger:   r.logger,
	}, fn.Namespace, fn.Spec.Config, fn.Spec.Secrets)
	if err != nil {
		return nil, err
	}
	return env, nil
}

// mergeSecretEnv merges resolved secret env vars into a worker's env with reserved-key
// precedence: a resolved key matching the FUNCD_ prefix guard is dropped (logged) so a secret
// can never shadow the shim contract (ADR-0057, reserved-env-not-overridable). Pure: it mutates
// only the passed env map.
func (r *Reconciler) mergeSecretEnv(env, secretEnv map[string]string) {
	secrets.MergeEnvGuarded(env, secretEnv, r.logger)
}

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
