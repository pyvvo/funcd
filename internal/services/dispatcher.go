// Package services holds the platform service pattern (ADR-0019): the single
// KindService reconciler (the Dispatcher) that routes each Service to the TypeHandler
// registered for its spec.type, plus the shared status write-back. A service type (KV,
// blob, secrets, …) contributes a TypeHandler — never its own Service reconciler — so
// they never collide on the gvk (ADR-0015 is one-reconciler-per-gvk). The facades live
// in per-type subpackages (internal/services/kv, …).
package services

import (
	"context"
	"log/slog"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// TypeHandler is the per-service-type reconcile logic. It provisions the instance /
// binding for one Service of its Type; the Dispatcher does the shared status write-back.
type TypeHandler interface {
	Type() v1.ServiceType
	Reconcile(ctx context.Context, svc *v1.Service) (controller.Result, error)
}

// Dispatcher is the single controller.Reconciler for KindService: it routes by spec.type.
type Dispatcher struct {
	store    store.Store
	logger   *slog.Logger
	handlers map[v1.ServiceType]TypeHandler
}

// NewDispatcher builds the Service dispatcher. Register it once on the engine for
// KindService. Handlers are keyed by their Type(); an unregistered type is a no-op.
func NewDispatcher(st store.Store, logger *slog.Logger, handlers ...TypeHandler) (*Dispatcher, error) {
	if st == nil {
		return nil, fault.Invalidf("services.NewDispatcher", "store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	m := make(map[v1.ServiceType]TypeHandler, len(handlers))
	for _, h := range handlers {
		m[h.Type()] = h
	}
	return &Dispatcher{store: st, logger: logger.With("component", "services"), handlers: m}, nil
}

// Reconcile reads the Service, routes it to its type's handler (no-op if unregistered),
// then writes Status.Phase=Ready on success (idempotent — the ADR-0015 contract).
func (d *Dispatcher) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	obj, err := d.store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return controller.Result{}, nil // deleted — nothing to do
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), "services.Reconcile", "get service")
	}
	svc, ok := obj.(*v1.Service)
	if !ok {
		return controller.Result{}, fault.Internalf("services.Reconcile", "object %s/%s is not a Service", req.Namespace, req.Name)
	}
	h, ok := d.handlers[svc.Spec.Type]
	if !ok {
		return controller.Result{}, nil // unregistered service type — no-op
	}
	res, err := h.Reconcile(ctx, svc)
	if err != nil {
		return res, err
	}
	if svc.Status.Phase != v1.PhaseReady {
		svc.Status.Phase = v1.PhaseReady
		if _, uerr := d.store.Update(ctx, svc); uerr != nil {
			if fault.KindOf(uerr) == fault.Conflict {
				return controller.Result{Requeue: true}, nil // re-reconcile on the fresh RV
			}
			return controller.Result{}, fault.Wrapf(uerr, fault.KindOf(uerr), "services.Reconcile", "status write-back")
		}
	}
	return res, nil
}
