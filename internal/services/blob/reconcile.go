package blob

import (
	"context"
	"log/slog"
	"reflect"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/health"
	"github.com/pyvvo/funcd/internal/store"
)

// ReconcilerDeps configures the Bucket reconciler (ADR-0215 Decision 7). Store is required; a nil Health is always
// Ready.
type ReconcilerDeps struct {
	Store  store.Store
	Health *health.Prober
	Logger *slog.Logger
}

// Reconciler is the controller.Reconciler for KindBucket: it writes only a Bucket's status, from the blob storage
// probe.
type Reconciler struct {
	store  store.Store
	health *health.Prober
	logger *slog.Logger
}

// NewReconciler builds the Bucket reconciler.
func NewReconciler(d ReconcilerDeps) (*Reconciler, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("services.blob.NewReconciler", "store is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{store: d.Store, health: d.Health, logger: logger.With("component", "services.blob.reconciler")}, nil
}

// Reconcile sets a Bucket Ready while the blob storage probe passes, else Degraded StorageUnreachable, at its
// generation. It writes only a status that changed and never requeues: the prober enqueues every Bucket when its
// result flips.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "services.blob.Reconcile"
	obj, err := r.store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return controller.Result{}, nil
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get bucket")
	}
	b, ok := obj.(*v1.Bucket)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not a Bucket", req.Namespace, req.Name)
	}
	next := v1.BucketStatus{Status: health.StoreStatus(b.Status.Status, r.health.Result(health.TargetBlob), b.Generation)}
	if reflect.DeepEqual(next, b.Status) {
		return controller.Result{}, nil
	}
	b.Status = next
	if _, err := r.store.Update(ctx, b); err != nil {
		if fault.KindOf(err) == fault.Conflict {
			return controller.Result{}, nil // the write that conflicted brings the next pass
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "status write-back")
	}
	return controller.Result{}, nil
}
