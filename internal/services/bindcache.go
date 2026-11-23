package services

import (
	"context"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// minSweepLen is the floor of BindingCache.sweepAt: a Put sweeps expired entries once the cache reaches
// twice its size after the last sweep, so the cache stays bounded by the live bindings at amortized O(1)
// per insert (#223).
const minSweepLen = 64

// BindingCache is the TTL cache the per-type binding resolvers (kv, blob) keep of resolved bindings,
// keyed by (namespace, caller, alias), the caller a Function or a Job by kind and name (ADR-0351 Decision 2). Only resolved bindings are put: a miss is never cached, so
// default-deny stays live (ADR-0073).
type BindingCache[V comparable] struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]cachedBinding[V]
	sweepAt int
}

type cachedBinding[V comparable] struct {
	v      V
	expiry time.Time
}

// NewBindingCache builds a BindingCache whose entries live for ttl.
func NewBindingCache[V comparable](ttl time.Duration) *BindingCache[V] {
	return &BindingCache[V]{ttl: ttl, entries: map[string]cachedBinding[V]{}}
}

func bindingKey(ns v1.NamespaceName, caller v1.WorkloadRef, alias string) string {
	return string(ns) + "\x00" + string(caller.Kind) + "\x00" + string(caller.Name) + "\x00" + alias
}

// Get returns the live binding cached for (ns, fn, alias), dropping an expired one.
func (c *BindingCache[V]) Get(ns v1.NamespaceName, fn v1.WorkloadRef, alias string) (V, bool) {
	key := bindingKey(ns, fn, alias)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		if time.Now().Before(e.expiry) {
			return e.v, true
		}
		delete(c.entries, key)
	}
	var zero V
	return zero, false
}

// Put caches v for (ns, fn, alias) for the cache's ttl.
func (c *BindingCache[V]) Put(ns v1.NamespaceName, fn v1.WorkloadRef, alias string, v V) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.sweepAt {
		for k, e := range c.entries {
			if !now.Before(e.expiry) {
				delete(c.entries, k)
			}
		}
		c.sweepAt = max(2*len(c.entries), minSweepLen)
	}
	c.entries[bindingKey(ns, fn, alias)] = cachedBinding[V]{v: v, expiry: now.Add(c.ttl)}
}

// Len is the number of entries held, expired ones included until a Get or a sweep drops them.
func (c *BindingCache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// MetaReader is the metastore read a caller's bindings need.
type MetaReader interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}

// CallerBindings reads the bindings of caller, the Function or the Job of ns that a worker acts as (ADR-0351 Decision
// 2): what it may reach. It answers the store's error, fault.NotFound for a caller not stored; a caller of any other
// kind is Forbidden.
func CallerBindings(ctx context.Context, r MetaReader, ns v1.NamespaceName, caller v1.WorkloadRef) (*v1.FunctionBindings, error) {
	const op = "services.CallerBindings"
	if caller.Kind != v1.KindFunction && caller.Kind != v1.KindJob {
		return nil, fault.Forbiddenf(op, "%s is no Function and no Job", caller)
	}
	obj, err := r.Get(ctx, caller.Kind.GVK(), ns, caller.Name)
	if err != nil {
		return nil, err
	}
	switch o := obj.(type) {
	case *v1.Function:
		return &o.Spec.Bindings, nil
	case *v1.Job:
		return &o.Spec.Bindings, nil
	}
	return nil, fault.Internalf(op, "object %s/%s is no %s", ns, caller.Name, caller.Kind)
}

// Principal is caller as the PDP's principal (ADR-0351 Decision 2).
func Principal(ns v1.NamespaceName, caller v1.WorkloadRef) *auth.EntityRef {
	return &auth.EntityRef{Type: caller.Kind, Namespace: ns, Name: caller.Name}
}
