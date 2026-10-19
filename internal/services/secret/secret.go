// Package secret is the function-facing secret service (ADR-0249): it answers the local API's /secret routes with the
// keys and values of the Secrets a Function binds, resolved through the same PDP-checked resolver the reconciler uses,
// on every read and with no cache.
package secret

import (
	"context"
	"maps"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/secrets"
	"github.com/pyvvo/funcd/internal/store"
)

// Resolver resolves Secret names into one key-value map for id (ADR-0057); *secrets.Resolver satisfies it.
type Resolver interface {
	ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

// Deps carries what the Facade needs.
type Deps struct {
	Store    store.Store
	Resolver Resolver
}

// Facade implements local.Secrets.
type Facade struct {
	store    store.Store
	resolver Resolver
}

// NewFacade builds the Facade; both deps are required.
func NewFacade(d Deps) (*Facade, error) {
	const op = "secret.NewFacade"
	if d.Store == nil || d.Resolver == nil {
		return nil, fault.Invalidf(op, "a store and a resolver are required")
	}
	return &Facade{store: d.Store, resolver: d.Resolver}, nil
}

// List returns the sorted keys of the Secrets fn binds, an empty list when none (never nil, so the route answers
// {"keys": []}).
func (f *Facade) List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName) ([]string, error) {
	values, err := f.values(ctx, ns, fn)
	if err != nil {
		return nil, err
	}
	return append([]string{}, slices.Sorted(maps.Keys(values))...), nil
}

// Get returns key's value among the Secrets fn binds, or found false when none holds it.
func (f *Facade) Get(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, key string) (string, bool, error) {
	values, err := f.values(ctx, ns, fn)
	if err != nil {
		return "", false, err
	}
	v, ok := values[key]
	return v, ok, nil
}

// values resolves the Secrets fn's current spec binds (ADR-0249 Decision 2), under the namespace identity the
// reconciler reads them as; a later Secret's key wins and FUNCD_ keys are dropped, as in a worker's env (ADR-0092).
func (f *Facade) values(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName) (map[string]string, error) {
	const op = "secret.values"
	obj, err := f.store.Get(ctx, v1.KindFunction.GVK(), ns, fn)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "get function %s/%s", ns, fn)
	}
	function, ok := obj.(*v1.Function)
	if !ok {
		return nil, fault.Internalf(op, "object %s/%s is not a Function", ns, fn)
	}
	names := function.Spec.Bindings.Secret
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil
	}
	resolved, err := f.resolver.ResolveEnv(ctx, secrets.InjectorIdentity(ns), ns, objectNames(names))
	if err != nil {
		return nil, err
	}
	secrets.MergeEnvGuarded(out, resolved, nil)
	return out, nil
}

func objectNames(names []v1.ObjectName) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = string(n)
	}
	return out
}
