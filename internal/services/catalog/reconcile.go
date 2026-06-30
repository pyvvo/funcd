package catalog

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/provider"
)

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
			// delete path: tear the engine down via the provider-runtime (idempotent).
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
		// Route is nil: the CatalogService is INTERNAL-ONLY in V1 (ADR-0087) — in-platform clients
		// reach the engine via status.Address; external ingress is a follow-up consumer-binding ADR.
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
	if st.Endpoint != "" {
		cs.Status.Endpoint = st.Endpoint
	} else {
		cs.Status.Endpoint = st.Address
	}
	if st.Ready {
		cs.Status.Phase = v1.PhaseReady
		cs.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue})
	} else {
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
	return controller.Result{}, nil
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

	// spec.config (ConfigMaps, non-sensitive): merge Data keys (DUCKDB_*) into env.
	for _, name := range cs.Spec.Config {
		cm, gerr := r.getConfigMap(ctx, cs.Namespace, name)
		if gerr != nil {
			return nil, fault.Wrapf(gerr, fault.KindOf(gerr), op, "resolve config %q", name)
		}
		for k, v := range cm.Spec.Data {
			if isReservedFuncdKey(k) {
				r.logger.Warn("dropping config env key that collides with a reserved FUNCD_ key", "key", k)
				continue
			}
			env[k] = v
		}
	}

	// spec.secrets (Secrets, sensitive): resolve PDP-authorized → merge Data keys (QUACK_TOKEN).
	if len(cs.Spec.Secrets) > 0 {
		if r.secrets == nil {
			return nil, fault.Invalidf(op, "secret injection is not configured but %s/%s declares %d secret(s)",
				cs.Namespace, cs.Name, len(cs.Spec.Secrets))
		}
		resolved, serr := r.secrets.ResolveEnv(ctx, r.developerFor(cs.Namespace), cs.Namespace, objectNames(cs.Spec.Secrets))
		if serr != nil {
			return nil, fault.Wrapf(serr, fault.KindOf(serr), op, "resolve secrets")
		}
		for k, v := range resolved {
			if isReservedFuncdKey(k) {
				r.logger.Warn("dropping secret env key that collides with a reserved FUNCD_ key", "key", k)
				continue
			}
			env[k] = v
		}
	}
	return env, nil
}

// getConfigMap reads a ConfigMap by name from the store (cross-resource resolution of spec.config).
func (r *Reconciler) getConfigMap(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.ConfigMap, error) {
	const op = "services.catalog.getConfigMap"
	obj, err := r.store.Get(ctx, v1.KindConfigMap.GVK(), ns, name)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "get configmap %s/%s", ns, name)
	}
	cm, ok := obj.(*v1.ConfigMap)
	if !ok {
		return nil, fault.Internalf(op, "object %s/%s is not a ConfigMap", ns, name)
	}
	return cm, nil
}

// catalogURI is the s3:// key the DuckLake SQLite catalog lives at (ADR-0086): the bound
// (bucket, prefix) under the reserved _ducklake/catalog.db key.
func catalogURI(c v1.CatalogRef) string {
	return fmt.Sprintf("s3://%s/%s/_ducklake/catalog.db", c.Bucket, c.Prefix)
}

// isReservedFuncdKey reports whether an env key is reserved by the runtime shim contract (FUNCD_*);
// a resolved secret/config value can never shadow it (mirrors ADR-0057, reserved-env-not-overridable).
func isReservedFuncdKey(k string) bool { return strings.HasPrefix(k, "FUNCD_") }

// objectNames converts the typed spec field to the []string the SecretResolver takes.
func objectNames(names []v1.ObjectName) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = string(n)
	}
	return out
}

// retryOnConflict swallows a store Conflict (re-reconciled on the next watch event), else wraps.
func retryOnConflict(err error, op string) error {
	if fault.KindOf(err) == fault.Conflict {
		return nil
	}
	return fault.Wrapf(err, fault.KindOf(err), op, "status write-back")
}
