package function

import (
	"context"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	cataloggw "github.com/green-0-rabbit/funcd/internal/catalog/gateway"
)

// resolveCatalogEnv resolves each spec.catalogs binding into the FUNCD_CATALOG_<ALIAS>_URL/_TOKEN env
// pair for worker injection (ADR-0091 as reworked by ADR-0137). For every binding it Gets the bound
// CatalogService in the function's namespace, reads its status.endpoint — now the node-private catalog
// PEP proxy URL (ADR-0137), no longer the engine — and derives the per-function catalog token
// DeriveCatalogToken(master, ns, fn): a MAC-authenticated bearer the proxy constant-time-verifies to
// this Function principal, then PEPs catalog::query per query. The shared QUACK_TOKEN is no longer
// handed to the function — the proxy holds it and swaps it in only after an allow.
//
// It is fail-closed on readiness: a bound catalog with no status.endpoint yet (still deploying, or the
// proxy not yet Ensured) returns (requeue=true) with no env — the caller holds the function
// Ready=False/CatalogNotReady and requeues, rather than injecting an empty URL. Returns (nil, false,
// nil) when the function declares no catalogs.
//
// The returned keys are written DIRECTLY into the worker env by the caller — NEVER through
// mergeSecretEnv, whose FUNCD_ reserved-key guard would silently drop them. (Mirrors the catalog
// reconciler, which sets its own FUNCD_QUACK_PORT/FUNCD_DUCKLAKE_CATALOG directly.)
func (r *Reconciler) resolveCatalogEnv(ctx context.Context, fn *v1.Function) (env map[string]string, requeue bool, err error) {
	const op = "function.resolveCatalogEnv"
	if len(fn.Spec.Catalogs) == 0 {
		return nil, false, nil
	}
	// The per-function catalog token is keyed by (ns, fn), so it is the SAME across every binding of
	// this function; the proxy for each catalog resolves it to this Function principal, then PEPs
	// catalog::query on THAT catalog. Derived once here.
	token, terr := cataloggw.DeriveCatalogToken(r.catalogMaster, fn.Namespace, fn.Name)
	if terr != nil {
		return nil, false, fault.Wrapf(terr, fault.KindOf(terr), op, "derive catalog token for %s/%s", fn.Namespace, fn.Name)
	}
	out := make(map[string]string, len(fn.Spec.Catalogs)*2)
	for _, bnd := range fn.Spec.Catalogs {
		obj, gerr := r.store.Get(ctx, v1.KindCatalogService.GVK(), fn.Namespace, bnd.Catalog)
		if gerr != nil {
			if fault.KindOf(gerr) == fault.NotFound {
				// The bound catalog does not exist (yet). Admission rejects a binding to a
				// non-existent CatalogService at apply time; a delete-after-apply race is fail-closed:
				// requeue until it (re)appears rather than booting with the binding unpopulated.
				return nil, true, nil
			}
			return nil, false, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get catalogservice %s/%s", fn.Namespace, bnd.Catalog)
		}
		cs, ok := obj.(*v1.CatalogService)
		if !ok {
			return nil, false, fault.Internalf(op, "object %s/%s is not a CatalogService", fn.Namespace, bnd.Catalog)
		}
		// Fail-closed on readiness: no published endpoint ⇒ the catalog proxy is not up yet. Requeue
		// rather than inject an empty URL.
		if cs.Status.Endpoint == "" {
			return nil, true, nil
		}
		alias := strings.ToUpper(bnd.Alias)
		out["FUNCD_CATALOG_"+alias+"_URL"] = cs.Status.Endpoint // the node-private catalog PEP proxy (ADR-0137)
		out["FUNCD_CATALOG_"+alias+"_TOKEN"] = token            // per-function MAC token (ADR-0137), not the shared QUACK_TOKEN
	}
	return out, false, nil
}

// addCatalogEnv writes the already-resolved catalog env pairs DIRECTLY into a worker's env (ADR-0091).
// It bypasses mergeSecretEnv on purpose: the keys are FUNCD_-prefixed, which mergeSecretEnv's reserved-
// key guard (secrets.go:64) would drop. catalogEnv is nil for a function with no spec.catalogs.
func (r *Reconciler) addCatalogEnv(env, catalogEnv map[string]string) {
	for k, v := range catalogEnv {
		env[k] = v
	}
}

// addCatalogExtensionDir injects DUCKDB_EXTENSION_DIRECTORY for a catalog-consumer function (one
// declaring spec.catalogs) when a dir is configured — the dev analogue of the prod bundle's
// duckdb-ext (ADR-0089), letting the handler's `LOAD quack`/`ducklake` resolve the curated DuckDB
// extensions locally. Empty dir, or a function with no catalog binding, gets nothing (prod path:
// the bundle carries duckdb-ext under FUNCD_BUNDLE_DIR, so catalogExtensionDir stays empty there).
func (r *Reconciler) addCatalogExtensionDir(env map[string]string, fn *v1.Function) {
	if r.catalogExtensionDir == "" || len(fn.Spec.Catalogs) == 0 {
		return
	}
	env["DUCKDB_EXTENSION_DIRECTORY"] = r.catalogExtensionDir
}
