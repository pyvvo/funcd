package kv

import (
	"context"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Binding is the resolved KV binding for a (caller, alias) pair (ADR-0073): the target store + table,
// the table's owner (single writer), and the store's per-op caps — everything the facade needs to
// authorize + bound a call.
type Binding struct {
	Store         v1.ObjectName
	Table         string
	Owner         v1.ObjectName
	MaxValueBytes int64
	MaxKeyBytes   int
}

// BindingResolver maps a caller function + alias to its (store, table) + owner + caps (ADR-0073). It
// reads the caller's Function.spec.kv (the binding IS the capability) and the target KVStore.
// fault.Forbidden when the caller has no spec.kv entry for the alias (default-deny).
type BindingResolver interface {
	Resolve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) (Binding, error)
}

// MetaReader is the read-only metastore view the resolver needs (a subset of store.Store), declared
// here so the kv package stays near-leaf; the wiring adapts the real store.Store to it.
type MetaReader interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}

// defaultResolverTTL bounds how long the resolver caches a resolution (ADR-0073: specs/caps are
// low-churn, eventually-consistent within seconds). A miss is NOT cached (default-deny stays live).
const defaultResolverTTL = 5 * time.Second

// metaResolver is the default BindingResolver over the metastore, with a small TTL cache keyed by
// (ns, function, alias). Resolution is default-deny: the caller's spec.kv entry for the alias must
// exist; with none, Forbidden.
type metaResolver struct {
	r   MetaReader
	ttl time.Duration

	mu    sync.Mutex
	cache map[string]cachedBinding
}

type cachedBinding struct {
	b      Binding
	expiry time.Time
}

// NewResolver builds the metastore-backed BindingResolver. r is required.
func NewResolver(r MetaReader) (BindingResolver, error) {
	if r == nil {
		return nil, fault.Invalidf("services.kv.NewResolver", "meta reader is required")
	}
	return &metaResolver{r: r, ttl: defaultResolverTTL, cache: map[string]cachedBinding{}}, nil
}

func resolverKey(ns v1.NamespaceName, fn v1.ObjectName, alias string) string {
	return string(ns) + "\x00" + string(fn) + "\x00" + alias
}

// Resolve maps (ns, fn, alias) → Binding, default-deny. It reads the caller Function's spec.kv for the
// alias → (store, table), then reads the target store for the table's owner + caps. A hit is cached for
// ttl; a miss (Forbidden) is never cached.
func (m *metaResolver) Resolve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) (Binding, error) {
	const op = "services.kv.Resolver.Resolve"
	key := resolverKey(ns, fn, alias)

	m.mu.Lock()
	if c, ok := m.cache[key]; ok && time.Now().Before(c.expiry) {
		m.mu.Unlock()
		return c.b, nil
	}
	m.mu.Unlock()

	// The caller function's spec.kv is the capability list (default-deny on a miss).
	fobj, err := m.r.Get(ctx, v1.KindFunction.GVK(), ns, fn)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return Binding{}, fault.Forbiddenf(op, "function %q in %q not found; no KV binding for alias %q", fn, ns, alias)
		}
		return Binding{}, fault.Wrapf(err, fault.Internal, op, "get function %q", fn)
	}
	caller, ok := fobj.(*v1.Function)
	if !ok {
		return Binding{}, fault.Internalf(op, "object %q is not a Function", fn)
	}
	var bind *v1.FunctionKV
	for i := range caller.Spec.KV {
		if caller.Spec.KV[i].Alias == alias {
			bind = &caller.Spec.KV[i]
			break
		}
	}
	if bind == nil {
		return Binding{}, fault.Forbiddenf(op, "function %q in %q has no KV binding for alias %q", fn, ns, alias)
	}

	// Read the target store for the table's owner + caps. A dangling binding (store/table gone) is
	// Forbidden too.
	sobj, err := m.r.Get(ctx, v1.KindKVStore.GVK(), ns, bind.Store)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return Binding{}, fault.Forbiddenf(op, "binding %q references missing store %q", alias, bind.Store)
		}
		return Binding{}, fault.Wrapf(err, fault.Internal, op, "get kvstore %q", bind.Store)
	}
	ks, ok := sobj.(*v1.KVStore)
	if !ok {
		return Binding{}, fault.Internalf(op, "object %q is not a KVStore", bind.Store)
	}
	var owner v1.ObjectName
	found := false
	for _, tb := range ks.Spec.Tables {
		if tb.Name == bind.Table {
			owner = tb.Owner
			found = true
			break
		}
	}
	if !found {
		return Binding{}, fault.Forbiddenf(op, "binding %q references missing table %q in store %q", alias, bind.Table, bind.Store)
	}

	b := Binding{
		Store:         bind.Store,
		Table:         bind.Table,
		Owner:         owner,
		MaxValueBytes: ks.Spec.EffectiveMaxValueBytes(),
		MaxKeyBytes:   ks.Spec.EffectiveMaxKeyBytes(),
	}
	m.mu.Lock()
	m.cache[key] = cachedBinding{b: b, expiry: time.Now().Add(m.ttl)}
	m.mu.Unlock()
	return b, nil
}
