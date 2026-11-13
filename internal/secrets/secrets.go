// Package secrets is the secrets delivery resolver (ADR-0022): it reads a function's
// bound Secret resources (auto-decrypted by the store's at-rest encryptor) and returns
// their data as an env-var map for worker injection — PDP-authorized (the secrets PEP): a
// workload's read asks secret::read for each Secret as that workload (ADR-0321), any other
// read one coarse question for the namespace.
// The injection is the function reconciler's secret gate (ADR-0057, internal/function): it
// resolves the bound Secrets through this resolver before any worker is provisioned and
// merges them into the worker's env (V1 delivers env only; tmpfs is V2). The at-rest
// encryptor lives in internal/secrets/aesgcm.
package secrets

import (
	"context"
	"log/slog"

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
// returns their merged Data as an env-var map for worker injection. An identity that carries a
// principal asks secret::read for each Secret before its read (ADR-0321 Decision 3); one that
// carries none asks the coarse namespace question once. A value env delivery cannot
// carry (EnvDataProblem: not valid UTF-8, or a NUL byte) is fault.Invalid; the first such key
// in sorted order is named.
func (r *Resolver) ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error) {
	const op = "secrets.ResolveEnv"
	if id.Principal == nil {
		dec, err := r.authz.Authorize(ctx, auth.Request{Identity: id, Verb: auth.VerbGet, Kind: v1.KindSecret, Namespace: ns})
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "authorize")
		}
		if !dec.Allowed {
			return nil, fault.Forbiddenf(op, "read secrets in %q denied: %s", ns, dec.Reason)
		}
	}
	env := make(map[string]string)
	for _, name := range names {
		if id.Principal != nil {
			if err := r.authorizeRead(ctx, op, id, ns, name); err != nil {
				return nil, err
			}
		}
		obj, gerr := r.store.Get(ctx, v1.KindSecret.GVK(), ns, v1.ObjectName(name))
		if gerr != nil {
			return nil, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get secret %q", name)
		}
		sec, ok := obj.(*v1.Secret)
		if !ok {
			return nil, fault.Internalf(op, "object %s/%s is not a Secret", ns, name)
		}
		if k, p := EnvDataProblem(sec.Spec.Data); p != "" {
			return nil, fault.Invalidf(op, "secret %q key %q %s; env delivery cannot carry binary data (base64-encode it)", name, k, p)
		}
		for k, v := range sec.Spec.Data {
			env[k] = string(v)
		}
	}
	return env, nil
}

// authorizeRead asks secret::read on Secret name as id's workload principal (ADR-0321).
func (r *Resolver) authorizeRead(ctx context.Context, op string, id auth.Identity, ns v1.NamespaceName, name string) error {
	return r.authorize(ctx, op, id, ns, name, auth.ActionSecretRead, auth.VerbGet, "read")
}

// authorize asks action on Secret name as id's workload principal; a deny is fault.Forbidden naming what was denied.
func (r *Resolver) authorize(ctx context.Context, op string, id auth.Identity, ns v1.NamespaceName, name string, action auth.Action, verb auth.Verb, what string) error {
	dec, err := r.authz.Authorize(ctx, auth.Request{Identity: id, Verb: verb, Kind: v1.KindSecret, Namespace: ns,
		Action: action, Resource: &auth.EntityRef{Type: v1.KindSecret, Namespace: ns, Name: v1.ObjectName(name)}})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "authorize")
	}
	if !dec.Allowed {
		return fault.Forbiddenf(op, "%s secret %q in %q denied: %s", what, name, ns, dec.Reason)
	}
	return nil
}

// CheckRead is the readiness check's question for one bound Secret (ADR-0321 Decision 4): may id's workload principal
// read it. It reads no Secret, so a missing one passes and the start check alone answers for it.
func (r *Resolver) CheckRead(ctx context.Context, id auth.Identity, ns v1.NamespaceName, name string) error {
	return r.authorizeRead(ctx, "secrets.CheckRead", id, ns, name)
}

// CheckWrite asks secret::write on Secret name as id's workload principal (ADR-0327 Decision 1): may it write the
// Secret's keys. The decision reads the stored Secret's owner, so the caller asks after its own read of it.
func (r *Resolver) CheckWrite(ctx context.Context, id auth.Identity, ns v1.NamespaceName, name string) error {
	return r.authorize(ctx, "secrets.CheckWrite", id, ns, name, auth.ActionSecretWrite, auth.VerbUpdate, "write")
}

// InjectorIdentity is the namespace-scoped developer identity a Function's start check resolves its secrets as when no
// other is configured (ADR-0057 Decision 4): V1 is single-tenant per namespace, so the developer owns, and may read,
// its own namespace's secrets. A workload's own reads carry its principal on top of it (WorkloadIdentity, ADR-0321).
func InjectorIdentity(ns v1.NamespaceName) auth.Identity {
	return auth.Identity{
		Subject:    "system:secret-injector:" + string(ns),
		Role:       auth.RoleDeveloper,
		Namespaces: []v1.NamespaceName{ns},
	}
}

// WorkloadIdentity is a workload's reader identity (ADR-0321 Decision 3): the injector identity with the workload as its
// Cedar principal, so ResolveEnv and CheckRead ask secret::read for each Secret as that Function or Lake.
func WorkloadIdentity(kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) auth.Identity {
	id := InjectorIdentity(ns)
	id.Principal = &auth.EntityRef{Type: kind, Namespace: ns, Name: name}
	return id
}
