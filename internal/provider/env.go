package provider

import (
	"context"
	"log/slog"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/secrets"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// SecretResolver is the consumer-side seam (ADR-0002 interface placement) provider env-resolution
// depends on: EnvDeps takes this INTERFACE, not the concrete *secrets.Resolver — the per-provider
// controller's existing SecretResolver interface value + its test fakes satisfy it structurally, so
// both call sites (and their fakes) compile and the existing tests pass unchanged (ADR-0092).
type SecretResolver interface {
	ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

// EnvDeps carries what provider env-resolution needs; a controller supplies its own instances.
type EnvDeps struct {
	Secrets  SecretResolver                       // ADR-0057 PDP-authorized secret resolution (nil ⇒ secrets unsupported)
	Store    store.Store                          // ConfigMap reads
	Identity func(v1.NamespaceName) auth.Identity // the secret-injector identity for the read
	Logger   *slog.Logger                         // nil-tolerated (the guarded-merge silently drops)
}

// ResolveEnv assembles a provider instance's engine env from its bound ConfigMaps (config, non-
// sensitive — each read via store.Get(ctx, v1.KindConfigMap.GVK(), ns, name) → *v1.ConfigMap,
// merging cm.Spec.Data) + Secrets (sensitive, PDP-resolved via d.Secrets under d.Identity(ns)),
// guarded against reserved FUNCD_ keys (ADR-0092). Config is merged first, then secrets (so a
// secret may override a config default). Returns fault.Invalid if secrets are declared but
// d.Secrets is nil (unchanged catalog semantics). The caller composes provider-specific env
// (keypair, FUNCD_*) around the result — this helper owns only the spec.secrets+spec.config slice.
func ResolveEnv(ctx context.Context, d EnvDeps, ns v1.NamespaceName, config, secretNames []v1.ObjectName) (map[string]string, error) {
	const op = "provider.ResolveEnv"
	env := make(map[string]string)

	// spec.config (ConfigMaps, non-sensitive): merge Data keys into env.
	for _, name := range config {
		obj, gerr := d.Store.Get(ctx, v1.KindConfigMap.GVK(), ns, name)
		if gerr != nil {
			return nil, fault.Wrapf(gerr, fault.KindOf(gerr), op, "resolve config %q", name)
		}
		cm, ok := obj.(*v1.ConfigMap)
		if !ok {
			return nil, fault.Internalf(op, "object %s/%s is not a ConfigMap", ns, name)
		}
		secrets.MergeEnvGuarded(env, cm.Spec.Data, d.Logger)
	}

	// spec.secrets (Secrets, sensitive): resolve PDP-authorized → merge Data keys (after config).
	if len(secretNames) > 0 {
		if d.Secrets == nil {
			return nil, fault.Invalidf(op, "secret injection is not configured but %s declares %d secret(s)",
				ns, len(secretNames))
		}
		resolved, serr := d.Secrets.ResolveEnv(ctx, d.Identity(ns), ns, objectNames(secretNames))
		if serr != nil {
			return nil, fault.Wrapf(serr, fault.KindOf(serr), op, "resolve secrets")
		}
		secrets.MergeEnvGuarded(env, resolved, d.Logger)
	}
	return env, nil
}

// objectNames converts the typed spec field to the []string the SecretResolver takes.
func objectNames(names []v1.ObjectName) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = string(n)
	}
	return out
}
