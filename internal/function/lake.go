package function

import (
	"context"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
)

// resolveLakeEnv resolves each spec.bindings.lake binding into its FUNCD_LAKE_<ALIAS>_URL for worker
// injection (ADR-0091 as reworked by ADR-0137). For every binding it Gets the bound Lake in the
// function's namespace and reads its status.endpoint — now the node-private lake PEP proxy URL
// (ADR-0137), no longer the engine. The per-function lake token the proxy verifies is the Lake hook's
// own env (lakeHook.own, ADR-0306 Decision 7); the shared QUACK_TOKEN is no longer handed to the
// function — the proxy holds it and swaps it in only after an allow.
//
// It is fail-closed on readiness: a bound lake that is not Ready yet returns (requeue=true) with no
// env — the caller holds the function Ready=False/LakeNotReady and requeues. Readiness is the
// lake's Phase, not a non-empty status.endpoint: while the engine starts, the lake reconciler
// publishes the raw engine address there, and injecting it would bypass the PEP proxy (the engine
// then rejects the per-function token). With lakeProxies set the URL is the lake's listener bound
// in this daemon run, never the stored status.endpoint, and a lake with none bound yet waits like a
// not-Ready one (ADR-0162 Decision 3), so no worker starts on a previous run's port (#662). Returns (nil,
// false, nil) when the function declares no lakes.
//
// The returned keys are written DIRECTLY into the worker env by the caller — NEVER through
// addBindingEnv, which writes only bound config keys, from which the FUNCD_ guard drops them. (Mirrors the lake
// reconciler, which sets its own FUNCD_QUACK_PORT/FUNCD_DUCKLAKE_CATALOG directly.)
func (r *Reconciler) resolveLakeEnv(ctx context.Context, fn *v1.Function) (env map[string]string, requeue bool, err error) {
	const op = "function.resolveLakeEnv"
	if len(fn.Spec.Bindings.Lake) == 0 {
		return nil, false, nil
	}
	out := make(map[string]string, len(fn.Spec.Bindings.Lake))
	for _, bnd := range fn.Spec.Bindings.Lake {
		obj, gerr := r.store.Get(ctx, v1.KindLake.GVK(), fn.Namespace, bnd.Lake)
		if gerr != nil {
			if fault.KindOf(gerr) == fault.NotFound {
				// The bound lake does not exist (yet). Admission rejects a binding to a
				// non-existent Lake at apply time; a delete-after-apply race is fail-closed:
				// requeue until it (re)appears rather than booting with the binding unpopulated.
				return nil, true, nil
			}
			return nil, false, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get lake %s/%s", fn.Namespace, bnd.Lake)
		}
		cs, ok := obj.(*v1.Lake)
		if !ok {
			return nil, false, fault.Internalf(op, "object %s/%s is not a Lake", fn.Namespace, bnd.Lake)
		}
		// Fail-closed on readiness: before Ready the endpoint is empty, or the raw engine address while the
		// engine starts. Requeue in both cases.
		if cs.Status.Phase != v1.PhaseReady {
			return nil, true, nil
		}
		url := cs.Status.Endpoint
		if r.lakeProxies != nil {
			var bound bool
			if url, bound = r.lakeProxies.ProxyURL(fn.Namespace, bnd.Lake); !bound {
				return nil, true, nil
			}
		}
		if url == "" {
			return nil, true, nil
		}
		out["FUNCD_LAKE_"+strings.ToUpper(bnd.Alias)+"_URL"] = url // the node-private lake PEP proxy (ADR-0137)
	}
	return out, false, nil
}

// lakesReady reports whether every Lake fn binds exists with phase Ready (ADR-0162 Decision 5): one store
// Get per binding, no write; true with no spec.bindings.lake and no read.
func (r *Reconciler) lakesReady(ctx context.Context, fn *v1.Function) bool {
	for _, bnd := range fn.Spec.Bindings.Lake {
		obj, err := r.store.Get(ctx, v1.KindLake.GVK(), fn.Namespace, bnd.Lake)
		if err != nil {
			return false
		}
		if cs, ok := obj.(*v1.Lake); !ok || cs.Status.Phase != v1.PhaseReady {
			return false
		}
	}
	return true
}

// MapLake maps a changed or deleted Lake to every Function of its namespace whose spec.bindings.lake
// names it, so a consumer's RevisionReady follows its lake within one pass (ADR-0162 Decision 5). A List error
// logs and maps nothing.
func (r *Reconciler) MapLake(ctx context.Context, obj v1.Object) []controller.Request {
	meta := obj.GetObjectMeta()
	res, err := r.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: meta.Namespace})
	if err != nil {
		r.logger.Warn("could not list the Functions a lake change affects", "namespace", meta.Namespace, "lake", meta.Name, "err", err)
		return nil
	}
	var out []controller.Request
	for _, o := range res.Items {
		fn, ok := o.(*v1.Function)
		if !ok {
			continue
		}
		for _, b := range fn.Spec.Bindings.Lake {
			if b.Lake == meta.Name {
				out = append(out, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: fn.Namespace, Name: fn.Name})
				break
			}
		}
	}
	return out
}

// addLakeExtensionDir injects DUCKDB_EXTENSION_DIRECTORY for a lake-consumer function (one
// declaring spec.bindings.lake) when a dir is configured — the dev analogue of the prod bundle's
// duckdb-ext (ADR-0089), letting the handler's `LOAD quack`/`ducklake` resolve the curated DuckDB
// extensions locally. Empty dir, or a function with no lake binding, gets nothing (prod path:
// the bundle carries duckdb-ext under FUNCD_BUNDLE_DIR, so lakeExtensionDir stays empty there).
func (r *Reconciler) addLakeExtensionDir(env map[string]string, fn *v1.Function) {
	if r.lakeExtensionDir == "" || len(fn.Spec.Bindings.Lake) == 0 {
		return
	}
	env["DUCKDB_EXTENSION_DIRECTORY"] = r.lakeExtensionDir
}
