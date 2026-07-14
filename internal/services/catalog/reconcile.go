package catalog

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/edge/router"
	"github.com/green-0-rabbit/funcd/internal/provider"
	"github.com/green-0-rabbit/funcd/internal/secrets"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// quackTokenEnvKey is the engine-env key the resolved shared Quack/engine token lives under (ADR-0086,
// resolved from spec.secrets by engineEnv). On the Ready branch the catalog PEP proxy (ADR-0137) swaps
// a per-caller token for THIS value before forwarding, so it is never handed to a caller.
const quackTokenEnvKey = "QUACK_TOKEN"

// Reconcile converges one CatalogService (ADR-0086 as reworked by ADR-0087). A present
// CatalogService is deployed as an add-on provider: the reconciler assembles a provider.ProviderSpec
// from its bindings (spec.blob → the ADR-0085 per-fn S3 keypair derived over the provider identity;
// spec.catalog → FUNCD_DUCKLAKE_CATALOG; spec.secrets/spec.config → the Quack token + engine
// config) and calls the provider-runtime's Converge — NO backing Function is materialized. It
// reflects the provider's readiness/address into status. A deleted CatalogService (NotFound) tears
// the engine down via provider.Teardown.
//
// The reconciler writes NO data — DuckDB does, out-of-process, through the F47 S3 surface. In-process
// the engine never reaches Ready (no real image / no probe answerer), which is expected: the live
// DuckDB scenarios run on the deferred node-gated lane.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "services.catalog.Reconcile"
	obj, err := r.store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			// delete path: retract the external ingress edge entry (ADR-0138), stop the node-private
			// catalog PEP proxy (ADR-0137), then tear the engine down via the provider-runtime (all
			// idempotent).
			if r.routes != nil {
				if rerr := r.routes.Set(ctx, catalogRouteSource(req.Namespace, req.Name), nil); rerr != nil {
					return controller.Result{}, fault.Wrapf(rerr, fault.KindOf(rerr), op, "retract catalog ingress route %s/%s", req.Namespace, req.Name)
				}
			}
			if r.proxy != nil {
				r.proxy.Remove(req.Namespace, req.Name)
			}
			ref := provider.ProviderRef{Namespace: req.Namespace, Name: req.Name}
			if terr := r.prov.Teardown(ctx, ref); terr != nil {
				return controller.Result{}, fault.Wrapf(terr, fault.KindOf(terr), op, "teardown provider engine %s/%s", req.Namespace, req.Name)
			}
			return controller.Result{}, nil
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get catalogservice")
	}
	cs, ok := obj.(*v1.CatalogService)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not a CatalogService", req.Namespace, req.Name)
	}

	// ADR-0121: spec.blob + spec.catalog (bucket, prefix) EXISTENCE is reconcile-time — a CatalogService
	// naming a not-yet-applied Bucket/prefix is admitted and held not-Ready (BucketNotFound) until it
	// resolves, then converges (this is the check the removed catalog-blob-validity admission made). Fail-
	// closed: the engine's S3 reach is denied until the prefix (and its owner == cs.Name) exist.
	refRequeue, refMsg, rberr := r.resolveBucketRefs(ctx, cs)
	if rberr != nil {
		return controller.Result{}, rberr
	}
	if refRequeue {
		cs.Status.Function = v1.ObjectName(engineName(string(cs.Name)))
		cs.Status.Phase = v1.PhasePending
		cs.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "BucketNotFound", Message: refMsg})
		if _, uerr := r.store.Update(ctx, cs); uerr != nil {
			return controller.Result{}, retryOnConflict(uerr, op)
		}
		return controller.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Resolve the engine env (the bindings ADR-0087 injects) BEFORE converging. A secret/config
	// resolution failure holds the service not-Ready (BindingResolveFailed), no engine started —
	// the same fail-closed posture the Function secret gate uses (ADR-0057).
	env, berr := r.engineEnv(ctx, cs)
	if berr != nil {
		cs.Status.Function = v1.ObjectName(engineName(string(cs.Name)))
		cs.Status.Phase = v1.PhasePending
		cs.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "BindingResolveFailed", Message: berr.Error()})
		if _, uerr := r.store.Update(ctx, cs); uerr != nil {
			return controller.Result{}, retryOnConflict(uerr, op)
		}
		return controller.Result{}, nil
	}

	spec := provider.ProviderSpec{
		Ref:       provider.ProviderRef{Namespace: cs.Namespace, Name: cs.Name},
		Image:     r.imageFor(DuckDBRuntime),
		Port:      enginePort,
		Env:       env,
		Readiness: provider.ReadinessProbe{Path: "/", ExpectStatus: 200},
		Replicas:  1, // pinned single writer (no scale-to-zero), ADR-0087
		Resources: provider.ResourceSpec{CPU: cs.Spec.Resources.CPU, Memory: cs.Spec.Resources.Memory},
		// The PROVIDER route stays nil: the catalog's external edge is NOT the provider's engine route
		// (the provider gateway path is vestigial). On Ready the reconciler Ensures a node-private
		// catalog PEP proxy (r.proxy) fronting this engine (ADR-0137, internal path) and — when
		// spec.ingress opts in (ADR-0138) — programs an external edge entry to that PROXY through the
		// edge-router aggregator (syncIngressRoute, below), never to the engine. The reconciler owns
		// that entry because the proxy URL is known only after Ensure (post-Converge).
		Route: nil,
	}

	st, cerr := r.prov.Converge(ctx, spec)
	if cerr != nil {
		return controller.Result{}, fault.Wrapf(cerr, fault.KindOf(cerr), op, "converge provider engine %s/%s", cs.Namespace, cs.Name)
	}

	// Reflect the provider status. status.Function is KEPT (ADR-0086 API shape) — repointed at the
	// engine identity, not a backing Function. status.Endpoint is the ingress path when exposed,
	// else the netns Address (the daemon's handle).
	cs.Status.Function = v1.ObjectName(engineName(string(cs.Name)))
	if st.Ready {
		// INTERNAL enforcement (ADR-0137): front the ready engine with a node-private catalog PEP
		// proxy and publish the PROXY url as the endpoint — internal functions now inject the proxy
		// (which resolves a per-caller token → catalog::query PEP → swaps to the shared engine token),
		// not the engine directly. The engine address (st.Address, a netns "host:port") is the proxy's
		// upstream. When no proxy is wired (in-memory/dev) the engine address is published as before.
		endpoint := st.Endpoint
		if endpoint == "" {
			endpoint = st.Address
		}
		var proxyURL string
		if r.proxy != nil && st.Address != "" {
			purl, perr := r.proxy.Ensure(
				auth.EntityRef{Type: v1.KindCatalogService, Namespace: cs.Namespace, Name: cs.Name},
				"http://"+st.Address, env[quackTokenEnvKey])
			if perr != nil {
				return controller.Result{}, fault.Wrapf(perr, fault.KindOf(perr), op, "ensure catalog proxy %s/%s", cs.Namespace, cs.Name)
			}
			proxyURL = purl
			endpoint = purl
		}
		cs.Status.Endpoint = endpoint
		cs.Status.Phase = v1.PhaseReady
		cs.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue})
		// ADR-0138: OPT-IN external edge exposure — program an edge entry to the PEP PROXY (proxyURL,
		// never the raw engine) when spec.ingress is set; clear it otherwise. Requires the proxy (the
		// entry's upstream); with no proxy wired (in-memory/dev) there is nothing external to expose.
		if rerr := r.syncIngressRoute(ctx, cs, proxyURL); rerr != nil {
			return controller.Result{}, rerr
		}
	} else {
		// Not Ready: retract any external edge entry so the edge never points at a not-ready proxy.
		if rerr := r.syncIngressRoute(ctx, cs, ""); rerr != nil {
			return controller.Result{}, rerr
		}
		if st.Endpoint != "" {
			cs.Status.Endpoint = st.Endpoint
		} else {
			cs.Status.Endpoint = st.Address
		}
		// In-process the engine never reaches Ready (no real image) — Pending is expected; the live
		// engine comes up only on the node-gated lane.
		reason := st.Reason
		if reason == "" {
			reason = "EngineNotReady"
		}
		cs.Status.Phase = v1.PhasePending
		cs.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason})
	}
	if _, uerr := r.store.Update(ctx, cs); uerr != nil {
		return controller.Result{}, retryOnConflict(uerr, op)
	}
	if !st.Ready {
		// The engine is still starting (the container is up but its Quack endpoint isn't answering the
		// readiness probe yet) — re-converge + re-probe soon until Ready, the same re-poll the Function
		// reconciler does while a worker boots (ADR-0030). Without this the CatalogService would stay
		// Pending until an unrelated watch event, never auto-progressing to Ready.
		return controller.Result{RequeueAfter: 2 * time.Second}, nil
	}
	return controller.Result{}, nil
}

// syncIngressRoute reconciles this catalog's OPT-IN external edge entry (ADR-0138). When spec.ingress
// is set AND a proxy URL is known (Ready + proxy wired), it programs an edge-router entry whose
// Upstream is the PEP PROXY (http://<proxyURL>, never the engine) — served as an open reverse-proxy
// backend, since the proxy does its own catalog::query PEP. Otherwise it clears the catalog's edge
// source (not exposed, not Ready, or no proxy). The aggregator drops/keeps only this source's
// partition, so user Routes are untouched. No-op when no aggregator is wired (the in-memory/dev path).
func (r *Reconciler) syncIngressRoute(ctx context.Context, cs *v1.CatalogService, proxyURL string) error {
	const op = "services.catalog.syncIngressRoute"
	if r.routes == nil {
		return nil
	}
	src := catalogRouteSource(cs.Namespace, cs.Name)
	if cs.Spec.Ingress == nil || proxyURL == "" {
		if rerr := r.routes.Set(ctx, src, nil); rerr != nil {
			return fault.Wrapf(rerr, fault.KindOf(rerr), op, "clear catalog ingress route %s", src)
		}
		return nil
	}
	entry := router.Entry{
		Namespace: cs.Namespace,
		Host:      cs.Spec.Ingress.Host,
		Auth:      v1.AuthOpen, // the PEP proxy authenticates the caller (Quack token); no funcd bearer at the edge
		Rules: []router.CompiledRule{{
			Path:     cs.Spec.Ingress.PathPrefix,
			Upstream: "http://" + proxyURL, // the catalog::query PEP proxy — external query authorized like internal
		}},
	}
	if rerr := r.routes.Set(ctx, src, []router.Entry{entry}); rerr != nil {
		return fault.Wrapf(rerr, fault.KindOf(rerr), op, "program catalog ingress route %s", src)
	}
	return nil
}

// engineEnv assembles the engine's environment (ADR-0087): the ADR-0085 per-fn S3 keypair (derived
// over the PROVIDER identity ns/cs.Name), AWS_REGION/AWS_ENDPOINT_URL, FUNCD_DUCKLAKE_CATALOG (the
// catalog's s3:// key), FUNCD_QUACK_PORT, and the resolved spec.secrets (the Quack token) +
// spec.config (DUCKDB_*) Data keys — merged with reserved-FUNCD_-key precedence. A secret/config
// resolution failure is returned (the caller fails the service closed).
func (r *Reconciler) engineEnv(ctx context.Context, cs *v1.CatalogService) (map[string]string, error) {
	const op = "services.catalog.engineEnv"
	env := map[string]string{
		"AWS_REGION":             "us-east-1",
		"FUNCD_QUACK_PORT":       strconv.Itoa(enginePort),
		"FUNCD_DUCKLAKE_CATALOG": catalogURI(cs.Spec.Catalog),
	}
	// The ADR-0085 per-fn S3 keypair, derived over the PROVIDER identity (ns, cs.Name) — so the
	// engine's S3 reach is scoped to its own bindings (the Bucket prefix owner == cs.Name).
	if r.derive != nil && len(cs.Spec.Blob) > 0 {
		access, secret := r.derive(string(cs.Namespace), string(cs.Name))
		env["AWS_ACCESS_KEY_ID"] = access
		env["AWS_SECRET_ACCESS_KEY"] = secret
	}
	if r.s3Endpoint != "" {
		env["AWS_ENDPOINT_URL_S3"] = r.s3Endpoint
		env["AWS_ENDPOINT_URL"] = r.s3Endpoint
	}

	// spec.config (ConfigMaps, non-sensitive) + spec.secrets (Secrets, sensitive) → guarded engine
	// env, resolved through the shared provider helper (ADR-0092): config first, then secrets, each
	// dropping any reserved FUNCD_ key. The provider-specific env above (S3 keypair, endpoints,
	// FUNCD_*) is composed BEFORE and preserved — the helper's result is merged in with the same
	// reserved-key guard.
	resolved, rerr := provider.ResolveEnv(ctx, provider.EnvDeps{
		Secrets:  r.secrets,
		Store:    r.store,
		Identity: r.developerFor,
		Logger:   r.logger,
	}, cs.Namespace, cs.Spec.Config, cs.Spec.Secrets)
	if rerr != nil {
		return nil, fault.Wrapf(rerr, fault.KindOf(rerr), op, "resolve provider env")
	}
	secrets.MergeEnvGuarded(env, resolved, r.logger)
	return env, nil
}

// resolveBucketRefs enforces ADR-0121's reconcile-time existence for a CatalogService's data references:
// every spec.blob (bucket, prefix) AND spec.catalog (bucket, prefix) must name a Bucket prefix that exists
// in the namespace — the check the removed catalog-blob-validity admission made synchronously. A miss
// returns requeue=true with a message; the caller holds the service not-Ready and re-reconciles.
func (r *Reconciler) resolveBucketRefs(ctx context.Context, cs *v1.CatalogService) (requeue bool, message string, err error) {
	const op = "services.catalog.resolveBucketRefs"
	bl, lerr := r.store.List(ctx, v1.KindBucket.GVK(), store.ListOptions{Namespace: cs.Namespace})
	if lerr != nil {
		return false, "", fault.Wrapf(lerr, fault.KindOf(lerr), op, "list buckets in %q", cs.Namespace)
	}
	prefixes := map[v1.ObjectName]map[string]bool{} // bucket name → set of prefix names
	for _, o := range bl.Items {
		b, ok := o.(*v1.Bucket)
		if !ok {
			continue
		}
		set := make(map[string]bool, len(b.Spec.Prefixes))
		for _, p := range b.Spec.Prefixes {
			set[p.Name] = true
		}
		prefixes[b.Name] = set
	}
	check := func(what string, bucket v1.ObjectName, prefix string) (bool, string) {
		set, ok := prefixes[bucket]
		if !ok {
			return true, fmt.Sprintf("%s → bucket %q not found in namespace %q; waiting", what, bucket, cs.Namespace)
		}
		if !set[prefix] {
			return true, fmt.Sprintf("%s → prefix %q not found in bucket %q; waiting", what, prefix, bucket)
		}
		return false, ""
	}
	for _, bnd := range cs.Spec.Blob {
		if rq, msg := check(fmt.Sprintf("spec.blob[%s]", bnd.Alias), bnd.Bucket, bnd.Prefix); rq {
			return true, msg, nil
		}
	}
	if rq, msg := check("spec.catalog", cs.Spec.Catalog.Bucket, cs.Spec.Catalog.Prefix); rq {
		return true, msg, nil
	}
	return false, "", nil
}

// catalogURI is the s3:// key the DuckLake SQLite catalog lives at (ADR-0086): the bound
// (bucket, prefix) under the reserved _ducklake/catalog.db key.
func catalogURI(c v1.CatalogRef) string {
	return fmt.Sprintf("s3://%s/%s/_ducklake/catalog.db", c.Bucket, c.Prefix)
}

// retryOnConflict swallows a store Conflict (re-reconciled on the next watch event), else wraps.
func retryOnConflict(err error, op string) error {
	if fault.KindOf(err) == fault.Conflict {
		return nil
	}
	return fault.Wrapf(err, fault.KindOf(err), op, "status write-back")
}
