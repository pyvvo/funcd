package provider

import (
	"context"
	"log/slog"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/envresolve"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// SecretResolver is the consumer-side seam (ADR-0002 interface placement) provider env-resolution
// depends on: EnvDeps takes this INTERFACE, not the concrete *secrets.Resolver — the per-provider
// controller's existing SecretResolver interface value + its test fakes satisfy it structurally.
// It re-exports envresolve.SecretResolver (the resolver relocated to a neutral leaf, ADR-0093) so
// the catalog controller's wiring is untouched.
type SecretResolver = envresolve.SecretResolver

// EnvDeps carries what provider env-resolution needs; a controller supplies its own instances.
// Kept as the provider facade so the catalog controller is untouched (ADR-0093); it maps 1:1 to
// envresolve.Deps, which ResolveEnv delegates to.
type EnvDeps struct {
	Secrets  SecretResolver                       // ADR-0057 PDP-authorized secret resolution (nil ⇒ secrets unsupported)
	Store    store.Store                          // ConfigMap reads
	Identity func(v1.NamespaceName) auth.Identity // the secret-injector identity for the read
	Logger   *slog.Logger                         // nil-tolerated (the guarded-merge silently drops)
}

// ResolveEnv delegates to envresolve.ResolveEnv (ADR-0093 relocated the shared config+secret
// resolver to internal/envresolve so the Function reconciler + providers share one path). The
// catalog controller keeps calling provider.ResolveEnv; behavior is unchanged.
func ResolveEnv(ctx context.Context, d EnvDeps, ns v1.NamespaceName, config, secretNames []v1.ObjectName) (map[string]string, error) {
	return envresolve.ResolveEnv(ctx, envresolve.Deps{
		Secrets:  d.Secrets,
		Store:    d.Store,
		Identity: d.Identity,
		Logger:   d.Logger,
	}, ns, config, secretNames)
}
