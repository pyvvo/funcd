package function

import (
	"context"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// quackTokenKey is the Secret Data key the CatalogService's Quack token lives under (ADR-0091/0087).
// resolveCatalogEnv selects exactly this key out of the resolved Secret map and re-keys it to
// FUNCD_CATALOG_<ALIAS>_TOKEN — it never merges the whole map.
const quackTokenKey = "QUACK_TOKEN"

// resolveCatalogEnv resolves each spec.catalogs binding into the FUNCD_CATALOG_<ALIAS>_URL/_TOKEN env
// pair for worker injection (ADR-0091). For every binding it Gets the bound CatalogService in the
// function's namespace, reads its status.endpoint (the published Quack URL, injected VERBATIM), and
// resolves the Quack token by PDP-authorized secret resolution over cs.spec.secrets — selecting only
// the "QUACK_TOKEN" key out of the returned map (never merging the map).
//
// It is fail-closed on readiness: if a bound catalog has no status.endpoint yet (still deploying) OR
// its Secret carries no QUACK_TOKEN, it returns (requeue=true) with no env populated for that binding
// — the caller holds the function Ready=False/CatalogNotReady and requeues, rather than injecting an
// empty URL/token. Returns (nil, false, nil) when the function declares no catalogs.
//
// The returned keys are written DIRECTLY into the worker env by the caller — NEVER through
// mergeSecretEnv, whose FUNCD_ reserved-key guard (secrets.go:64) would silently drop them. (Mirrors
// the catalog reconciler, which sets its own FUNCD_QUACK_PORT/FUNCD_DUCKLAKE_CATALOG directly,
// reconcile.go:120-123.)
func (r *Reconciler) resolveCatalogEnv(ctx context.Context, fn *v1.Function) (env map[string]string, requeue bool, err error) {
	const op = "function.resolveCatalogEnv"
	if len(fn.Spec.Catalogs) == 0 {
		return nil, false, nil
	}
	if r.secrets == nil {
		return nil, false, fault.Invalidf(op, "catalog injection needs secret resolution but it is not configured (%s/%s declares %d catalog binding(s))",
			fn.Namespace, fn.Name, len(fn.Spec.Catalogs))
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
		// Fail-closed on readiness: no published endpoint ⇒ the catalog is not Ready yet. Requeue
		// rather than inject an empty URL.
		if cs.Status.Endpoint == "" {
			return nil, true, nil
		}
		// Resolve the Quack token PDP-authorized (same secret-injector identity as the function's own
		// secrets, ADR-0057) and select ONLY the QUACK_TOKEN key — do not merge the whole map.
		resolved, rerr := r.secrets.ResolveEnv(ctx, r.developerFor(fn.Namespace), fn.Namespace, secretNames(cs.Spec.Secrets))
		if rerr != nil {
			return nil, false, fault.Wrapf(rerr, fault.KindOf(rerr), op, "resolve catalog %q token", bnd.Catalog)
		}
		token, ok := resolved[quackTokenKey]
		if !ok || token == "" {
			// The catalog's Secret carries no QUACK_TOKEN yet (or it is empty) — the provider is still
			// coming up. Fail closed: requeue rather than inject an empty token.
			return nil, true, nil
		}
		alias := strings.ToUpper(bnd.Alias)
		out["FUNCD_CATALOG_"+alias+"_URL"] = cs.Status.Endpoint // verbatim (ADR-0091)
		out["FUNCD_CATALOG_"+alias+"_TOKEN"] = token
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
