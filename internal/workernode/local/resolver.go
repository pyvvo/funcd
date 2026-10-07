package local

import (
	"context"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// defaultTimeout bounds a sync invoke whose link sets no timeout.
const defaultTimeout = 30 * time.Second

// FunctionStore is the read-only store access the resolver needs (a subset of store.Store).
type FunctionStore interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}

// storeResolver resolves a caller's link alias against its Function in the store. The declared link
// IS the capability grant (default-deny): no matching link ⇒ fault.Forbidden. Workload-identity PDP
// (ADR-0018) for cross-namespace links is V2; V1.1 is same-namespace, link-as-grant. (ADR-0064)
type storeResolver struct{ store FunctionStore }

// NewResolver returns the store-backed link Resolver.
func NewResolver(s FunctionStore) Resolver { return storeResolver{store: s} }

func (r storeResolver) Resolve(ctx context.Context, caller Ref, alias string) (Ref, time.Duration, error) {
	const op = "workernode.local.resolve"
	obj, err := r.store.Get(ctx, v1.KindFunction.GVK(), caller.Namespace, caller.Function)
	if err != nil {
		return Ref{}, 0, fault.Wrapf(err, fault.Internal, op, "load caller function %s/%s", caller.Namespace, caller.Function)
	}
	fn, ok := obj.(*v1.Function)
	if !ok {
		return Ref{}, 0, fault.Internalf(op, "caller %s/%s is not a Function", caller.Namespace, caller.Function)
	}
	for _, l := range fn.Spec.Links {
		if l.Alias == alias {
			timeout := time.Duration(l.Timeout)
			if timeout <= 0 {
				timeout = defaultTimeout
			}
			// Same-namespace target (cross-namespace needs a Grant, V2).
			return Ref{Namespace: caller.Namespace, Function: l.Target}, timeout, nil
		}
	}
	return Ref{}, 0, fault.Forbiddenf(op, "caller %s/%s declares no link %q (no link is no grant)", caller.Namespace, caller.Function, alias)
}
