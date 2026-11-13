// Package secret is the function-facing secret service (ADR-0249): it answers the local API's /secret routes with the
// keys and values of the Secrets a Function binds, on every read and with no cache, each Secret asked secret::read as
// the calling Function (ADR-0321), and the readiness check's question for each of them; and it writes one key of a
// bound Secret for its writers, asked secret::write as the calling Function (ADR-0327).
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
	CheckRead(ctx context.Context, id auth.Identity, ns v1.NamespaceName, name string) error
	CheckWrite(ctx context.Context, id auth.Identity, ns v1.NamespaceName, name string) error
}

// writeAttempts bounds a write's read-modify-write, as the control plane bounds its own (ownReadAttempts): a Conflict
// starts the attempt over, and the last one answers 409 (ADR-0327 Decision 4).
const writeAttempts = 5

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

// values resolves the Secrets fn's current spec binds (ADR-0249 Decision 2), each asked secret::read as fn (ADR-0321
// Decision 3), so a forbid on one fails the whole read; a later Secret's key wins and FUNCD_ keys are dropped, as in a
// worker's env (ADR-0092).
func (f *Facade) values(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName) (map[string]string, error) {
	function, err := f.function(ctx, ns, fn)
	if err != nil {
		return nil, err
	}
	names := function.Spec.Bindings.Secret
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil
	}
	resolved, err := f.resolver.ResolveEnv(ctx, secrets.WorkloadIdentity(v1.KindFunction, ns, fn), ns, objectNames(names))
	if err != nil {
		return nil, err
	}
	secrets.MergeEnvGuarded(out, resolved, nil)
	return out, nil
}

// CheckRead answers the readiness check for Secret name, which fn binds (ADR-0321 Decision 4): may fn read it.
func (f *Facade) CheckRead(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, name string) error {
	return f.resolver.CheckRead(ctx, secrets.WorkloadIdentity(v1.KindFunction, ns, fn), ns, name)
}

// Put sets key to value in Secret binding, which fn binds and may write (ADR-0327 Decision 4).
func (f *Facade) Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key, value string) error {
	return f.write(ctx, ns, fn, binding, func(data map[string][]byte) error {
		if k, p := secrets.EnvDataProblem(map[string][]byte{key: []byte(value)}); p != "" {
			return fault.Invalidf("secret.Put", "secret %q key %q %s", binding, k, p)
		}
		data[key] = []byte(value)
		return nil
	})
}

// Delete removes key from Secret binding, which fn binds and may write; a key the Secret lacks is no error.
func (f *Facade) Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) error {
	return f.write(ctx, ns, fn, binding, func(data map[string][]byte) error {
		delete(data, key)
		return nil
	})
}

// write changes one key of Secret binding as fn (ADR-0327 Decision 4). Each attempt reads the Secret, then asks
// secret::write, whose decision reads the stored owner after that read, and updates on the read's resourceVersion: a
// write lands only at a version the decision saw (docs/design/secret/key-write.fizz). A Conflict starts over, writeAttempts
// in all; the store's Update checks the keys as the API does.
func (f *Facade) write(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string, change func(map[string][]byte) error) error {
	const op = "secret.write"
	function, err := f.function(ctx, ns, fn)
	if err != nil {
		return err
	}
	if !slices.Contains(function.Spec.Bindings.Secret, v1.ObjectName(binding)) {
		return fault.Forbiddenf(op, "function %q in %q does not bind Secret %q", fn, ns, binding)
	}
	id := secrets.WorkloadIdentity(v1.KindFunction, ns, fn)
	for range writeAttempts {
		obj, err := f.store.Get(ctx, v1.KindSecret.GVK(), ns, v1.ObjectName(binding))
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "get secret %q", binding)
		}
		sec, ok := obj.(*v1.Secret)
		if !ok {
			return fault.Internalf(op, "object %s/%s is not a Secret", ns, binding)
		}
		if err := f.resolver.CheckWrite(ctx, id, ns, binding); err != nil {
			return err
		}
		if sec.Spec.Data == nil {
			sec.Spec.Data = map[string][]byte{}
		}
		if err := change(sec.Spec.Data); err != nil {
			return err
		}
		_, err = f.store.Update(ctx, sec)
		if fault.KindOf(err) != fault.Conflict {
			if err != nil {
				return fault.Wrapf(err, fault.KindOf(err), op, "update secret %q", binding)
			}
			return nil
		}
	}
	return fault.Conflictf(op, "secret %q changed under each of %d attempts to write it", binding, writeAttempts)
}

// function reads fn's current spec, whose bindings name what it may read and write (ADR-0249 Decision 2).
func (f *Facade) function(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName) (*v1.Function, error) {
	const op = "secret.function"
	obj, err := f.store.Get(ctx, v1.KindFunction.GVK(), ns, fn)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "get function %s/%s", ns, fn)
	}
	function, ok := obj.(*v1.Function)
	if !ok {
		return nil, fault.Internalf(op, "object %s/%s is not a Function", ns, fn)
	}
	return function, nil
}

func objectNames(names []v1.ObjectName) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = string(n)
	}
	return out
}
