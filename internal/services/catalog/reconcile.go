package catalog

import (
	"context"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
)

// Reconcile converges one CatalogService (ADR-0086). A present CatalogService materializes a backing
// min-replica=1 `duckdb` Function (the declared spec.blob bindings → ADR-0085 injects the per-fn S3
// keypair), exposed via the EXISTING function→gateway path (ADR-0013 — NO Route resource), and
// reflects the backing Function's readiness while publishing the Quack endpoint in status. A deleted
// CatalogService (NotFound) best-effort deletes its backing Function.
//
// The reconciler writes NO data — DuckDB does, out-of-process, through the F47 S3 surface. In-process
// the backing Function never reaches Ready (no real image), which is expected: the live DuckDB
// scenarios run on the deferred node-gated lane.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "services.catalog.Reconcile"
	obj, err := r.store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			// delete path: best-effort delete the backing Function (idempotent — a missing one is fine).
			fnName := v1.ObjectName(backingFunctionName(string(req.Name)))
			if derr := r.store.Delete(ctx, v1.KindFunction.GVK(), req.Namespace, fnName, ""); derr != nil && fault.KindOf(derr) != fault.NotFound {
				return controller.Result{}, fault.Wrapf(derr, fault.KindOf(derr), op, "delete backing function %s/%s", req.Namespace, fnName)
			}
			return controller.Result{}, nil
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get catalogservice")
	}
	cs, ok := obj.(*v1.CatalogService)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not a CatalogService", req.Namespace, req.Name)
	}

	fnName := backingFunctionName(string(cs.Name))
	backing, err := r.ensureBackingFunction(ctx, cs, fnName)
	if err != nil {
		return controller.Result{}, err
	}

	// Reflect the backing Function's readiness; publish the Quack endpoint (its standard ingress path).
	cs.Status.Function = v1.ObjectName(fnName)
	cs.Status.Endpoint = quackEndpoint(fnName)
	if backing.Status.Phase == v1.PhaseReady {
		cs.Status.Phase = v1.PhaseReady
		cs.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue})
	} else {
		// In-process the backing duckdb Function never reaches Ready (no real image) — Pending is
		// expected; the live engine comes up only on the node-gated lane.
		cs.Status.Phase = v1.PhasePending
		cs.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "BackingFunctionNotReady"})
	}
	if _, uerr := r.store.Update(ctx, cs); uerr != nil {
		if fault.KindOf(uerr) == fault.Conflict {
			return controller.Result{}, nil // re-reconciled on the next watch event
		}
		return controller.Result{}, fault.Wrapf(uerr, fault.KindOf(uerr), op, "status write-back")
	}
	return controller.Result{}, nil
}

// ensureBackingFunction creates (idempotently — adopting an existing one on Conflict, the
// function.go Revision-creation pattern) the backing min-replica=1 `duckdb` Function projecting the
// CatalogService's spec.blob bindings, then returns the live Function (with its observed status).
//
// Single-writer invariant: funcd has NO rollout/surge field on FunctionSpec, so the ADR's "recreate
// rollout (max-surge 0)" intent is realized by Scaling.MinReplicas=1 + Replicas=1 (exactly one
// replica) — the platform's no-surge replacement keeps the single replica the sole catalog writer.
// The backing Function carries no artifact/handler: FunctionSpec.Validate deliberately skips field-
// presence (runtime/handler/artifact), so a runtime-only duckdb Function passes Validate + Create.
func (r *Reconciler) ensureBackingFunction(ctx context.Context, cs *v1.CatalogService, fnName string) (*v1.Function, error) {
	const op = "services.catalog.ensureBackingFunction"

	desired := &v1.Function{}
	desired.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
	desired.Name = v1.ObjectName(fnName)
	desired.Namespace = cs.Namespace
	desired.ResourceGroup = cs.ResourceGroup
	desired.Spec.Runtime = v1.RuntimeName(DuckDBRuntime)
	desired.Spec.Blob = cs.Spec.Blob
	// minReplicas=1 + replicas=1 = exactly one replica (the single catalog writer); no scale-to-zero.
	desired.Spec.Scaling = v1.Scaling{MinReplicas: 1}
	desired.Spec.Replicas = 1

	created, err := r.store.Create(ctx, desired)
	if err != nil {
		if fault.KindOf(err) == fault.Conflict {
			// adopt the existing backing Function (idempotent reconcile) — return its live status.
			existing, gerr := r.store.Get(ctx, v1.KindFunction.GVK(), cs.Namespace, v1.ObjectName(fnName))
			if gerr != nil {
				return nil, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get existing backing function %s/%s", cs.Namespace, fnName)
			}
			fn, ok := existing.(*v1.Function)
			if !ok {
				return nil, fault.Internalf(op, "backing %s/%s is not a Function", cs.Namespace, fnName)
			}
			return fn, nil
		}
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "create backing function %s/%s", cs.Namespace, fnName)
	}
	fn, ok := created.(*v1.Function)
	if !ok {
		return nil, fault.Internalf(op, "created backing %s/%s is not a Function", cs.Namespace, fnName)
	}
	r.logger.Info("materialized backing duckdb function for CatalogService",
		"catalogservice", cs.Name, "namespace", cs.Namespace, "function", fnName)
	return fn, nil
}
