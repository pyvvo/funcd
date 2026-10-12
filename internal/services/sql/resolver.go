package sql

import (
	"context"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/services"
)

// Resolver maps a caller's alias to the database its spec.sql binds (ADR-0233 Decision 4). The binding is the read
// grant, so a missing alias is Forbidden.
type Resolver interface {
	Resolve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) (v1.ObjectName, error)
}

// MetaReader is the read-only metastore view the resolver needs.
type MetaReader interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}

// resolverTTL is how long a resolved binding is cached, as for KV; a miss is never cached.
const resolverTTL = 5 * time.Second

type metaResolver struct {
	r     MetaReader
	cache *services.BindingCache[v1.ObjectName]
}

// NewResolver builds the metastore-backed Resolver.
func NewResolver(r MetaReader) (Resolver, error) {
	if r == nil {
		return nil, fault.Invalidf("sql.NewResolver", "meta reader is required")
	}
	return &metaResolver{r: r, cache: services.NewBindingCache[v1.ObjectName](resolverTTL)}, nil
}

func (m *metaResolver) Resolve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) (v1.ObjectName, error) {
	const op = "sql.Resolver.Resolve"
	if db, ok := m.cache.Get(ns, fn, alias); ok {
		return db, nil
	}
	obj, err := m.r.Get(ctx, v1.KindFunction.GVK(), ns, fn)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return "", fault.Forbiddenf(op, "function %q in %q not found; no SQL binding for alias %q", fn, ns, alias)
	case err != nil:
		return "", fault.Wrapf(err, fault.Internal, op, "get function %q", fn)
	}
	caller, ok := obj.(*v1.Function)
	if !ok {
		return "", fault.Internalf(op, "object %q is not a Function", fn)
	}
	for _, b := range caller.Spec.SQL {
		if b.Alias == alias {
			m.cache.Put(ns, fn, alias, b.Database)
			return b.Database, nil
		}
	}
	return "", fault.Forbiddenf(op, "function %q in %q has no SQL binding for alias %q", fn, ns, alias)
}
