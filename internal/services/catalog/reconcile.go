package catalog

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/secrets"
	"github.com/pyvvo/funcd/internal/store"
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
			return controller.Result{}, r.deleted(ctx, req.Namespace, req.Name)
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get catalogservice")
	}
	cs, ok := obj.(*v1.CatalogService)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not a CatalogService", req.Namespace, req.Name)
	}

	// ADR-0162: bind the listener on the recorded port before any branch writes the status, so every write records it.
	if r.proxy != nil {
		bound, moved, lerr := r.proxy.Listen(cs.Namespace, cs.Name, cs.Status.ProxyPort)
		if lerr != nil {
			return controller.Result{}, fault.Wrapf(lerr, fault.KindOf(lerr), op, "bind catalog proxy listener %s/%s", cs.Namespace, cs.Name)
		}
		if moved {
			r.logger.WarnContext(ctx, "catalog proxy port is taken; bound a new one, so its consumers' URL changes",
				"catalog", string(cs.Namespace)+"/"+string(cs.Name), "recorded", cs.Status.ProxyPort, "bound", bound)
		}
		cs.Status.ProxyPort = bound
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
		return r.holdNotReady(ctx, cs, "BucketNotFound", refMsg, 2*time.Second)
	}

	// Resolve the engine env (the bindings ADR-0087 injects) BEFORE converging. A secret/config
	// resolution failure holds the service not-Ready (BindingResolveFailed), no engine running —
	// the same fail-closed posture the Function secret gate uses (ADR-0057).
	env, berr := r.engineEnv(ctx, cs)
	if berr != nil {
		// No ConfigMap or Secret event reconciles a CatalogService, so a binding applied later is found only by a requeue.
		var requeue time.Duration
		if fault.KindOf(berr) == fault.NotFound {
			requeue = 2 * time.Second
		}
		return r.holdNotReady(ctx, cs, "BindingResolveFailed", berr.Error(), requeue)
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
		// An engine that cannot be (re)created is not Ready: write that before failing the pass, else the last Ready
		// status and the dead engine's endpoint outlive it (issue #104).
		st = provider.ProviderStatus{Reason: "EngineConvergeFailed"}
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
	if cerr != nil {
		return controller.Result{}, fault.Wrapf(cerr, fault.KindOf(cerr), op, "converge provider engine %s/%s", cs.Namespace, cs.Name)
	}
	if !st.Ready {
		// The engine is still starting (the container is up but its Quack endpoint isn't answering the
		// readiness probe yet) — re-converge + re-probe soon until Ready, the same re-poll the Function
		// reconciler does while a worker boots (ADR-0030). Without this the CatalogService would stay
		// Pending until an unrelated watch event, never auto-progressing to Ready.
		return controller.Result{RequeueAfter: 2 * time.Second}, nil
	}
	// ADR-0142: come back after the supervision period, so Converge recreates an engine that died with no write.
	return controller.Result{RequeueAfter: r.period}, nil
}

// deleted is the delete path: it retracts the external ingress edge entry (ADR-0138), releases the catalog's PEP proxy
// listener (503, URL kept), tears the engine down, and closes the listener only once no Function binds the catalog
// (ADR-0162 Decision 4). Every step is idempotent; a failed check keeps the listener and fails the pass.
func (r *Reconciler) deleted(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	const op = "services.catalog.Reconcile"
	if r.routes != nil {
		if _, rerr := r.routes.Set(ctx, catalogRouteSource(ns, name), nil); rerr != nil {
			return fault.Wrapf(rerr, fault.KindOf(rerr), op, "retract catalog ingress route %s/%s", ns, name)
		}
	}
	bound := false
	if r.proxy != nil {
		if _, bound = r.proxy.ProxyURL(ns, name); bound {
			r.proxy.Release(ns, name)
		}
	}
	if terr := r.prov.Teardown(ctx, provider.ProviderRef{Namespace: ns, Name: name}); terr != nil {
		return fault.Wrapf(terr, fault.KindOf(terr), op, "teardown provider engine %s/%s", ns, name)
	}
	if !bound {
		return nil
	}
	binds, berr := anyFunctionBinds(ctx, r.store, ns, name)
	if berr != nil {
		return berr
	}
	if !binds {
		r.proxy.Remove(ns, name)
	}
	return nil
}

// anyFunctionBinds reports whether a stored Function of ns binds catalog name (ADR-0162 Decision 4): its spec.catalogs
// names it, or a worker of an earlier spec may still run, because its spec is unprocessed or it switches revisions.
// It reads the store on every call, uncached.
func anyFunctionBinds(ctx context.Context, st store.Store, ns v1.NamespaceName, name v1.ObjectName) (bool, error) {
	const op = "services.catalog.anyFunctionBinds"
	list, err := st.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), op, "list functions in %q", ns)
	}
	for _, o := range list.Items {
		fn, ok := o.(*v1.Function)
		if !ok {
			continue
		}
		if fn.Status.ObservedGeneration != fn.Generation || fn.Status.DrainingRevision != "" ||
			(fn.Status.ServingRevision != "" && fn.Status.ServingRevision != fn.Status.CurrentRevision) {
			return true, nil
		}
		for _, b := range fn.Spec.Catalogs {
			if b.Catalog == name {
				return true, nil
			}
		}
	}
	return false, nil
}

// MapFunction re-runs the reconcile of every released catalog listener in a changed Function's namespace, so the
// delete path closes it once the last Function that binds it goes (ADR-0162). It reads only the Manager; with no
// proxy wired it maps nothing.
func (r *Reconciler) MapFunction(_ context.Context, obj v1.Object) []controller.Request {
	if r.proxy == nil {
		return nil
	}
	ns := obj.GetObjectMeta().Namespace
	var reqs []controller.Request
	for _, name := range r.proxy.Released(ns) {
		reqs = append(reqs, controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: ns, Name: name})
	}
	return reqs
}

// holdNotReady fails a gated CatalogService closed, as the Function gate stops its worker (ADR-0057): it retracts the
// external edge entry (ADR-0138), suspends the proxy consumers were injected with (its URL kept for the recovery),
// writes Pending with no endpoint, and stops the engine (issue #372).
func (r *Reconciler) holdNotReady(ctx context.Context, cs *v1.CatalogService, reason, message string, requeue time.Duration) (controller.Result, error) {
	const op = "services.catalog.holdNotReady"
	if rerr := r.syncIngressRoute(ctx, cs, ""); rerr != nil {
		return controller.Result{}, rerr
	}
	if r.proxy != nil {
		r.proxy.Suspend(cs.Namespace, cs.Name)
	}
	cs.Status.Function = v1.ObjectName(engineName(string(cs.Name)))
	cs.Status.Endpoint = ""
	cs.Status.Phase = v1.PhasePending
	cs.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: message})
	if _, uerr := r.store.Update(ctx, cs); uerr != nil {
		return controller.Result{}, retryOnConflict(uerr, op)
	}
	if terr := r.prov.Teardown(ctx, provider.ProviderRef{Namespace: cs.Namespace, Name: cs.Name}); terr != nil {
		return controller.Result{}, fault.Wrapf(terr, fault.KindOf(terr), op, "stop provider engine %s/%s", cs.Namespace, cs.Name)
	}
	return controller.Result{RequeueAfter: requeue}, nil
}

// syncIngressRoute reconciles this catalog's OPT-IN external edge entry (ADR-0138). When spec.ingress
// is set AND a proxy URL is known (Ready + proxy wired), it Sets an edge-router entry whose Upstream is
// the PEP PROXY (http://<proxyURL>, never the engine) — served as an open reverse-proxy backend, since
// the proxy does its own catalog::query PEP — and reports the aggregator's verdict as IngressReady
// (ADR-0176); a refused entry is not programmed while Ready and the engine stay as they are. Otherwise
// it clears the catalog's edge source: IngressReady=False (CatalogNotReady) when spec.ingress is set,
// no IngressReady when it is not. No-op when no aggregator is wired (the in-memory/dev path).
func (r *Reconciler) syncIngressRoute(ctx context.Context, cs *v1.CatalogService, proxyURL string) error {
	const op = "services.catalog.syncIngressRoute"
	if r.routes == nil {
		return nil
	}
	src := catalogRouteSource(cs.Namespace, cs.Name)
	if cs.Spec.Ingress == nil || proxyURL == "" {
		if _, rerr := r.routes.Set(ctx, src, nil); rerr != nil {
			return fault.Wrapf(rerr, fault.KindOf(rerr), op, "clear catalog ingress route %s", src)
		}
		if cs.Spec.Ingress == nil {
			cs.Status.Conditions = slices.DeleteFunc(cs.Status.Conditions, func(c v1.Condition) bool { return c.Type == condIngressReady })
		} else {
			cs.Status.Conditions.Set(v1.Condition{Type: condIngressReady, Status: v1.ConditionFalse, Reason: "CatalogNotReady",
				Message: "the catalog is not Ready or has no proxy URL"})
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
		Owner: router.Owner{Kind: v1.KindCatalogService, Namespace: cs.Namespace, Name: cs.Name},
	}
	verdicts, rerr := r.routes.Set(ctx, src, []router.Entry{entry})
	if rerr != nil {
		return fault.Wrapf(rerr, fault.KindOf(rerr), op, "program catalog ingress route %s", src)
	}
	if len(verdicts) != 1 {
		return fault.Internalf(op, "edge aggregator returned %d verdicts for 1 entry of %s", len(verdicts), src)
	}
	if v := verdicts[0]; v.Reason != "" {
		cs.Status.Conditions.Set(v1.Condition{Type: condIngressReady, Status: v1.ConditionFalse, Reason: v.Reason, Message: v.Message})
	} else {
		cs.Status.Conditions.Set(v1.Condition{Type: condIngressReady, Status: v1.ConditionTrue, Reason: "Programmed"})
	}
	return nil
}

// engineEnv assembles the engine's environment (ADR-0087): the ADR-0085 S3 keypair (derived
// over the CatalogService identity, ADR-0175), AWS_REGION/AWS_ENDPOINT_URL, FUNCD_DUCKLAKE_CATALOG (the
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
	// The engine signs as CatalogService::"<ns>/<cs.Name>" (ADR-0175): its reads are its own spec.blob
	// bindings, and it writes a prefix only where the S3 PEP resolves it as that prefix's writer.
	if r.derive != nil && len(cs.Spec.Blob) > 0 {
		access, secret := r.derive(v1.KindCatalogService, string(cs.Namespace), string(cs.Name))
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
