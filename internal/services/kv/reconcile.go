package kv

import (
	"context"
	"log/slog"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// condReady is the readiness condition the KVStore reconciler raises (ADR-0072).
const condReady v1.ConditionType = "Ready"

// PrefixDropper reclaims a deleted KVStore's prefix (ADR-0072): the driver's DropPrefix is a concrete
// method beyond the kvstore.KV port, type-asserted from the driver at wiring. Declared here so the kv
// package depends on no driver. The memory driver gets a real impl; a driver without it ⇒ a no-op.
type PrefixDropper interface {
	DropPrefix(prefix string) error
}

// ReconcilerDeps configures the KindKVStore reconciler (ADR-0072).
type ReconcilerDeps struct {
	Store  store.Store
	KV     PrefixDropper // nil ⇒ delete reclamation is a no-op
	Logger *slog.Logger
}

// Reconciler is the controller.Reconciler for KindKVStore (ADR-0072): present ⇒ Ready + grantRefs;
// absent (deleted) ⇒ DropPrefix(<ns>/<name>/).
type Reconciler struct {
	store  store.Store
	kv     PrefixDropper
	logger *slog.Logger
}

// NewReconciler builds the KVStore reconciler. Store is required; KV (the PrefixDropper) is optional.
func NewReconciler(d ReconcilerDeps) (*Reconciler, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("services.kv.NewReconciler", "store is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{store: d.Store, kv: d.KV, logger: logger.With("component", "services.kv.reconciler")}, nil
}

// Reconcile converges one KVStore. A present store reaches Ready with grantRefs = the count of Grants
// referencing it; a deleted store (NotFound) reclaims its prefix via DropPrefix.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "services.kv.Reconcile"
	obj, err := r.store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			// delete path: reclaim the store's prefix (ADR-0072 / ADR-0066 DropPrefix).
			if r.kv != nil {
				if derr := r.kv.DropPrefix(storePrefix(req.Namespace, req.Name)); derr != nil {
					return controller.Result{}, fault.Wrapf(derr, fault.KindOf(derr), op, "drop prefix for %s/%s", req.Namespace, req.Name)
				}
			}
			return controller.Result{}, nil
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get kvstore")
	}
	ks, ok := obj.(*v1.KVStore)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not a KVStore", req.Namespace, req.Name)
	}

	refs, err := r.countGrantRefs(ctx, ks.Namespace, ks.Name)
	if err != nil {
		return controller.Result{}, err
	}
	ks.Status.GrantRefs = refs
	ks.Status.Phase = v1.PhaseReady
	ks.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue})
	if _, uerr := r.store.Update(ctx, ks); uerr != nil {
		if fault.KindOf(uerr) == fault.Conflict {
			return controller.Result{}, nil // re-reconciled on the next watch event
		}
		return controller.Result{}, fault.Wrapf(uerr, fault.KindOf(uerr), op, "status write-back")
	}
	return controller.Result{}, nil
}

// countGrantRefs counts the Grants in ns whose spec.store is storeName.
func (r *Reconciler) countGrantRefs(ctx context.Context, ns v1.NamespaceName, storeName v1.ObjectName) (int, error) {
	const op = "services.kv.countGrantRefs"
	list, err := r.store.List(ctx, v1.KindGrant.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return 0, fault.Wrapf(err, fault.KindOf(err), op, "list grants in %q", ns)
	}
	n := 0
	for _, o := range list.Items {
		if g, ok := o.(*v1.Grant); ok && g.Spec.Store == storeName {
			n++
		}
	}
	return n, nil
}
