package kv

import (
	"context"
	"sync"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// Binding is the resolved KV grant for a (caller, binding) pair (ADR-0072): the target store, the
// access mode, and the store's per-op caps — everything the facade needs to authorize + bound a call.
type Binding struct {
	Store         v1.ObjectName
	Mode          v1.KVMode
	MaxValueBytes int64
	MaxKeyBytes   int
}

// Binder resolves a caller (namespace + function) + binding alias to its granted store + mode + caps
// (ADR-0072). fault.Forbidden when no Grant matches (default-deny — a function gets no KV implicitly).
type Binder interface {
	Resolve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string) (Binding, error)
}

// MetaReader is the read-only metastore view the Binder needs (a subset of store.Store), declared
// here so the kv package stays near-leaf; the wiring adapts the real store.Store to it.
type MetaReader interface {
	List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error)
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}

// defaultBinderTTL bounds how long the Binder caches a resolution (ADR-0072: grants/caps are
// low-churn, eventually-consistent within seconds). A miss is NOT cached (default-deny stays live).
const defaultBinderTTL = 5 * time.Second

// metaBinder is the default Binder over the metastore, with a small TTL cache keyed by
// (ns, function, binding). Resolution is default-deny: the first matching Grant in the caller's
// namespace wins; with none, Forbidden.
type metaBinder struct {
	r   MetaReader
	ttl time.Duration

	mu    sync.Mutex
	cache map[string]cachedBinding
}

type cachedBinding struct {
	b      Binding
	expiry time.Time
}

// NewBinder builds the metastore-backed Binder. r is required.
func NewBinder(r MetaReader) (Binder, error) {
	if r == nil {
		return nil, fault.Invalidf("services.kv.NewBinder", "meta reader is required")
	}
	return &metaBinder{r: r, ttl: defaultBinderTTL, cache: map[string]cachedBinding{}}, nil
}

func binderKey(ns v1.NamespaceName, fn v1.ObjectName, binding string) string {
	return string(ns) + "\x00" + string(fn) + "\x00" + binding
}

// Resolve maps (ns, fn, binding) → Binding, default-deny. It finds the Grant whose subject is fn and
// whose binding alias matches, then reads the target store's caps. A hit is cached for ttl; a miss
// (Forbidden) is never cached.
func (m *metaBinder) Resolve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string) (Binding, error) {
	const op = "services.kv.Binder.Resolve"
	key := binderKey(ns, fn, binding)

	m.mu.Lock()
	if c, ok := m.cache[key]; ok && time.Now().Before(c.expiry) {
		m.mu.Unlock()
		return c.b, nil
	}
	m.mu.Unlock()

	grants, err := m.r.List(ctx, v1.KindGrant.GVK(), ns)
	if err != nil {
		return Binding{}, fault.Wrapf(err, fault.Internal, op, "list grants in %q", ns)
	}
	var match *v1.Grant
	for _, o := range grants {
		g, ok := o.(*v1.Grant)
		if ok && g.Spec.Function == fn && g.Spec.Binding == binding {
			match = g
			break
		}
	}
	if match == nil {
		return Binding{}, fault.Forbiddenf(op, "function %q in %q has no Grant for binding %q", fn, ns, binding)
	}

	// Read the target store's caps (apply defaults). A dangling Grant (store gone) is Forbidden too.
	obj, err := m.r.Get(ctx, v1.KindKVStore.GVK(), ns, match.Spec.Store)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return Binding{}, fault.Forbiddenf(op, "Grant for binding %q references missing store %q", binding, match.Spec.Store)
		}
		return Binding{}, fault.Wrapf(err, fault.Internal, op, "get kvstore %q", match.Spec.Store)
	}
	ks, ok := obj.(*v1.KVStore)
	if !ok {
		return Binding{}, fault.Internalf(op, "object %q is not a KVStore", match.Spec.Store)
	}
	b := Binding{
		Store:         match.Spec.Store,
		Mode:          match.Spec.Mode,
		MaxValueBytes: ks.Spec.EffectiveMaxValueBytes(),
		MaxKeyBytes:   ks.Spec.EffectiveMaxKeyBytes(),
	}

	m.mu.Lock()
	m.cache[key] = cachedBinding{b: b, expiry: time.Now().Add(m.ttl)}
	m.mu.Unlock()
	return b, nil
}
