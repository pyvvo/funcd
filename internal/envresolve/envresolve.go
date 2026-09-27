// Package envresolve is the single config+secret env-resolver shared by the Function
// reconciler and the provider framework (ADR-0093, relocating ADR-0092's helper to a
// neutral leaf). It reads bound ConfigMaps (non-sensitive, a plain store read) and Secrets
// (sensitive, PDP-resolved), guarded-merging both into one worker-env map, config first
// then secrets (so a secret overrides a config default). It imports only internal/secrets
// (the pure reserved-key guard) + internal/store — never internal/function or
// internal/provider — so both consumers can depend on it without inverting the layering.
package envresolve

import (
	"context"
	"errors"
	"log/slog"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/secrets"
	"github.com/pyvvo/funcd/internal/store"
)

// SecretResolver is the consumer-side seam (ADR-0002 interface placement) env-resolution
// depends on: Deps takes this INTERFACE, not the concrete *secrets.Resolver — the per-consumer
// controller's SecretResolver interface value + its test fakes satisfy it structurally, so
// both call sites (and their fakes) compile and the existing tests pass unchanged (ADR-0092).
type SecretResolver interface {
	ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

// Deps carries what env-resolution needs; a consumer supplies its own instances.
type Deps struct {
	Secrets  SecretResolver                       // ADR-0057 PDP-authorized secret resolution (nil ⇒ secrets unsupported)
	Store    store.Store                          // ConfigMap reads
	Identity func(v1.NamespaceName) auth.Identity // the secret-injector identity for the read
	Logger   *slog.Logger                         // nil-tolerated (the guarded-merge silently drops)
}

// Sentinels so a caller can attribute a failure to the config vs secret side (errors.Is), for
// a Ready reason (ADR-0093 §4). ErrConfig wraps a ConfigMap-read failure; ErrSecret wraps a
// Secret-resolution failure. The returned error still carries its underlying fault kind, so
// fault.KindOf(err) is preserved — the sentinel is joined alongside, not in place of, the fault.
var (
	ErrConfig = errors.New("config resolution failed") // wraps a ConfigMap read failure
	ErrSecret = errors.New("secret resolution failed") // wraps a Secret resolution failure
)

// ResolveEnv assembles env from bound ConfigMaps (config, non-sensitive — each read via
// store.Get(ctx, v1.KindConfigMap.GVK(), ns, name) → *v1.ConfigMap, merging cm.Spec.Data) +
// Secrets (sensitive, PDP-resolved via d.Secrets under d.Identity(ns)), guarded against
// reserved FUNCD_ keys (ADR-0092). Config is merged first, then secrets (so a secret may
// override a config default). Returns fault.Invalid if secrets are declared but d.Secrets is
// nil (unchanged catalog semantics). On failure the returned error is joined with ErrConfig or
// ErrSecret (ADR-0093 §4) so a caller can attribute it via errors.Is while keeping the fault
// kind. This is the single resolver for Functions AND providers.
func ResolveEnv(ctx context.Context, d Deps, ns v1.NamespaceName, config, secretNames []v1.ObjectName) (map[string]string, error) {
	const op = "envresolve.ResolveEnv"
	env := make(map[string]string)

	// config (ConfigMaps, non-sensitive): merge Data keys into env.
	for _, name := range config {
		obj, gerr := d.Store.Get(ctx, v1.KindConfigMap.GVK(), ns, name)
		if gerr != nil {
			return nil, errors.Join(fault.Wrapf(gerr, fault.KindOf(gerr), op, "resolve config %q", name), ErrConfig)
		}
		cm, ok := obj.(*v1.ConfigMap)
		if !ok {
			return nil, errors.Join(fault.Internalf(op, "object %s/%s is not a ConfigMap", ns, name), ErrConfig)
		}
		secrets.MergeEnvGuarded(env, cm.Spec.Data, d.Logger)
	}

	// secrets (Secrets, sensitive): resolve PDP-authorized → merge Data keys (after config).
	if len(secretNames) > 0 {
		if d.Secrets == nil {
			return nil, errors.Join(fault.Invalidf(op, "secret injection is not configured but %s declares %d secret(s)",
				ns, len(secretNames)), ErrSecret)
		}
		resolved, serr := d.Secrets.ResolveEnv(ctx, d.Identity(ns), ns, objectNames(secretNames))
		if serr != nil {
			return nil, errors.Join(fault.Wrapf(serr, fault.KindOf(serr), op, "resolve secrets"), ErrSecret)
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
