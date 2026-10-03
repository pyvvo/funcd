package blob

import (
	"context"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/services"
)

// Binding is the resolved blob binding for a (caller, alias) pair (ADR-0127/0073): the target bucket +
// prefix sub-domain the alias names. The binding IS the capability — everything the facade needs to key
// the object and name the BlobPrefix the S3 PDP authorizes.
type Binding struct {
	Bucket v1.ObjectName
	Prefix string
}

// BindingResolver maps a caller function + alias to its (bucket, prefix) (ADR-0127). It reads the
// caller's Function.spec.blob (the binding IS the capability) — fault.Forbidden when the caller has no
// spec.blob entry for the alias (default-deny, ADR-0073).
type BindingResolver interface {
	Resolve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) (Binding, error)
}

// MetaReader is the read-only metastore view the resolver needs (a subset of store.Store), declared
// here so the blob package stays near-leaf; the wiring adapts the real store.Store to it.
type MetaReader interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}

// defaultResolverTTL bounds how long the resolver caches a resolution (ADR-0073: specs are low-churn,
// eventually-consistent within seconds). A miss is NOT cached (default-deny stays live).
const defaultResolverTTL = 5 * time.Second

// metaResolver is the default BindingResolver over the metastore, with a small TTL cache keyed by
// (ns, function, alias). Resolution is default-deny: the caller's spec.blob entry for the alias must
// exist; with none, Forbidden.
type metaResolver struct {
	r     MetaReader
	cache *services.BindingCache[Binding]
}

// NewResolver builds the metastore-backed BindingResolver. r is required.
func NewResolver(r MetaReader) (BindingResolver, error) {
	if r == nil {
		return nil, fault.Invalidf("services.blob.NewResolver", "meta reader is required")
	}
	return &metaResolver{r: r, cache: services.NewBindingCache[Binding](defaultResolverTTL)}, nil
}

// Resolve maps (ns, fn, alias) → Binding, default-deny. It reads the caller Function's spec.blob for the
// alias → (bucket, prefix). A hit is cached for ttl; a miss (Forbidden) is never cached. The bucket's
// existence + the prefix owner are resolved by the S3 PDP + the substrate bucket resolver downstream, so
// the resolver only proves the binding was declared.
func (m *metaResolver) Resolve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) (Binding, error) {
	const op = "services.blob.Resolver.Resolve"
	if b, ok := m.cache.Get(ns, fn, alias); ok {
		return b, nil
	}

	// The caller function's spec.blob is the capability list (default-deny on a miss).
	fobj, err := m.r.Get(ctx, v1.KindFunction.GVK(), ns, fn)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return Binding{}, fault.Forbiddenf(op, "function %q in %q not found; no blob binding for alias %q", fn, ns, alias)
		}
		return Binding{}, fault.Wrapf(err, fault.Internal, op, "get function %q", fn)
	}
	caller, ok := fobj.(*v1.Function)
	if !ok {
		return Binding{}, fault.Internalf(op, "object %q is not a Function", fn)
	}
	var bind *v1.FunctionBlob
	for i := range caller.Spec.Blob {
		if caller.Spec.Blob[i].Alias == alias {
			bind = &caller.Spec.Blob[i]
			break
		}
	}
	if bind == nil {
		return Binding{}, fault.Forbiddenf(op, "function %q in %q has no blob binding for alias %q", fn, ns, alias)
	}

	b := Binding{Bucket: bind.Bucket, Prefix: bind.Prefix}
	m.cache.Put(ns, fn, alias, b)
	return b, nil
}
