// Package secrets is the secrets delivery resolver (ADR-0022): it reads a function's
// bound Secret resources (auto-decrypted by the store's at-rest encryptor) and returns
// their data as an env-var map for worker injection — PDP-authorized (the secrets PEP).
// The injection is the function reconciler's secret gate (ADR-0057, internal/function): it
// resolves the bound Secrets through this resolver before any worker is provisioned and
// merges them into the worker's env (V1 delivers env only; tmpfs is V2). The at-rest
// encryptor lives in internal/secrets/aesgcm.
package secrets

import (
	"context"
	"log/slog"
	"unicode/utf8"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/store"
)

// Deps configures the secrets resolver (internal component, ADR-0002 §1).
type Deps struct {
	Store      store.Store
	Authorizer auth.Authorizer
	Logger     *slog.Logger
}

// Resolver reads decrypted Secrets and produces the env map the worker injects.
type Resolver struct {
	store  store.Store
	authz  auth.Authorizer
	logger *slog.Logger
}

// NewResolver builds the secrets resolver. Store + Authorizer are required.
func NewResolver(d Deps) (*Resolver, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("secrets.NewResolver", "store is required")
	}
	if d.Authorizer == nil {
		return nil, fault.Invalidf("secrets.NewResolver", "authorizer is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Resolver{store: d.Store, authz: d.Authorizer, logger: logger.With("component", "secrets")}, nil
}

// ResolveEnv reads the named Secrets in ns (store-decrypted), PDP-authorized (read), and
// returns their merged Data as an env-var map for worker injection. A value that is not valid
// UTF-8 is fault.Invalid: env delivery would hand the worker U+FFFD instead of the bytes.
func (r *Resolver) ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error) {
	const op = "secrets.ResolveEnv"
	dec, err := r.authz.Authorize(ctx, auth.Request{Identity: id, Verb: auth.VerbGet, Kind: v1.KindSecret, Namespace: ns})
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "authorize")
	}
	if !dec.Allowed {
		return nil, fault.Forbiddenf(op, "read secrets in %q denied: %s", ns, dec.Reason)
	}
	env := make(map[string]string)
	for _, name := range names {
		obj, gerr := r.store.Get(ctx, v1.KindSecret.GVK(), ns, v1.ObjectName(name))
		if gerr != nil {
			return nil, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get secret %q", name)
		}
		sec, ok := obj.(*v1.Secret)
		if !ok {
			return nil, fault.Internalf(op, "object %s/%s is not a Secret", ns, name)
		}
		for k, v := range sec.Spec.Data {
			if !utf8.Valid(v) {
				return nil, fault.Invalidf(op, "secret %q key %q is not valid UTF-8; env delivery cannot carry binary data (base64-encode it)", name, k)
			}
			env[k] = string(v)
		}
	}
	return env, nil
}
