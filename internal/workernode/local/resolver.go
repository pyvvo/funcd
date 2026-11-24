package local

import (
	"context"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/services"
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

func (r storeResolver) Resolve(ctx context.Context, caller Ref, alias string) (Ref, v1.FunctionProtocol, time.Duration, error) {
	const op = "workernode.local.resolve"
	bindings, err := CallerBindings(ctx, r.store, caller)
	if err != nil {
		return Ref{}, "", 0, fault.Wrapf(err, fault.Internal, op, "load caller %s", caller)
	}
	for _, l := range bindings.Links {
		if l.Alias != alias {
			continue
		}
		timeout := time.Duration(l.Timeout)
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		// Same-namespace target (cross-namespace needs a Grant, V2).
		target := FunctionRef(caller.Namespace, l.Target)
		protocol, err := r.protocolOf(ctx, target)
		if err != nil {
			return Ref{}, "", 0, err
		}
		return target, protocol, timeout, nil
	}
	return Ref{}, "", 0, ErrNoLink.Errorf(op, "caller %s declares no link %q (no link is no grant)", caller, alias)
}

// protocolOf reads how target is called (ADR-0243), so invoke and fetch each refuse the other kind of target.
func (r storeResolver) protocolOf(ctx context.Context, target Ref) (v1.FunctionProtocol, error) {
	const op = "workernode.local.resolve"
	obj, err := r.store.Get(ctx, v1.KindFunction.GVK(), target.Namespace, target.Name)
	if fault.KindOf(err) == fault.NotFound {
		return "", ErrLinkTargetMissing.Errorf(op, "link target %s does not exist", target)
	}
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), op, "load link target %s", target)
	}
	fn, ok := obj.(*v1.Function)
	if !ok {
		return "", fault.Internalf(op, "link target %s is not a Function", target)
	}
	return fn.Spec.Protocol.Normalized(), nil
}

// CallerBindings reads the bindings of caller's workload, its Function or its Job (ADR-0351 Decision 2): what it may
// reach. A ref of any other kind is refused.
func CallerBindings(ctx context.Context, store FunctionStore, caller Ref) (*v1.FunctionBindings, error) {
	const op = "workernode.local.CallerBindings"
	if !caller.Valid() {
		return nil, fault.Forbiddenf(op, "caller %q of kind %q is no Function and no Job's run", caller, caller.Kind)
	}
	return services.CallerBindings(ctx, store, caller.Namespace, caller.Workload())
}
