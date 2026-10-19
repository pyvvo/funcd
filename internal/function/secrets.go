package function

import (
	"context"
	"strings"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/envresolve"
)

// SecretResolver resolves a function's bound Secret names → a key-value map, PDP-authorized for id
// (ADR-0022/0057). The reconciler resolves them before it starts a worker, so an unreadable Secret
// holds the Function (SecretResolveFailed), though the values reach the handler only through ctx.secret
// (ADR-0249). It depends on this local seam (ADR-0002 import discipline) — satisfied by
// *secrets.Resolver, wired in pkg/funcd. A nil resolver on Deps disables secrets: a function that
// declares spec.bindings.secret then fails closed (SecretResolveFailed).
type SecretResolver interface {
	ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

// resolveBindingEnv resolves a function's bound ConfigMaps (spec.config, non-sensitive) +
// Secrets (spec.secrets, sensitive) into one guarded env-var map for worker injection, via the
// single shared resolver (ADR-0093, envresolve.Resolve — config first, then secrets), with the keys of each kind
// (ADR-0245). It returns an empty Resolved when the function declares neither. Every other path is fail-closed: any
// resolver error (a missing ConfigMap → ErrConfig,
// or a PDP-deny / missing Secret / unconfigured resolver → ErrSecret) returns an error so
// Reconcile holds the function not-Ready (ConfigResolveFailed / SecretResolveFailed) with no worker.
func (r *Reconciler) resolveBindingEnv(ctx context.Context, fn *v1.Function) (envresolve.Resolved, error) {
	if len(fn.Spec.Bindings.Config) == 0 && len(fn.Spec.Bindings.Secret) == 0 {
		return envresolve.Resolved{}, nil
	}
	return envresolve.Resolve(ctx, envresolve.Deps{
		Secrets:  r.secrets,
		Store:    r.store,
		Identity: r.developerFor,
		Logger:   r.logger,
	}, fn.Namespace, fn.Spec.Bindings.Config, fn.Spec.Bindings.Secret)
}

// addBindingEnv writes the values of a worker's bound config keys into its env, with FUNCD_CONFIG_KEYS listing them
// (ADR-0245). No secret value reaches the worker: a handler reads its secrets from funcd through ctx.secret (ADR-0249
// Decision 3), so a config key a bound Secret also holds stays out too. ConfigKeys hold no FUNCD_ key (the guard
// dropped them in envresolve.Resolve). Pure: it mutates only the passed env map.
func (r *Reconciler) addBindingEnv(env map[string]string, b envresolve.Resolved) {
	for _, k := range b.ConfigKeys {
		env[k] = b.Env[k]
	}
	if len(b.ConfigKeys) > 0 {
		env["FUNCD_CONFIG_KEYS"] = strings.Join(b.ConfigKeys, ",")
	}
}

// secretNames converts the typed spec field to the []string the Resolver takes.
func secretNames(names []v1.ObjectName) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = string(n)
	}
	return out
}
