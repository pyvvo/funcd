// Package catalog is the CatalogService reconciler (ADR-0086 as reworked by ADR-0087, F48/F57):
// the ADR-0019 controller half of the DuckLake/DuckDB/Quack add-on provider. Its engine is NO
// longer a backing Function — it is deployed by the add-on-provider runtime (internal/provider,
// ADR-0087): the reconciler resolves the service's bindings (spec.blob → the ADR-0085 per-fn S3
// keypair derived over the provider identity; spec.catalog → FUNCD_DUCKLAKE_CATALOG;
// spec.secrets/spec.config → the Quack token + engine config) into a provider.ProviderSpec and
// calls provider.Runtime.Converge. It reflects the provider's readiness/address into status and
// tears the engine down on delete. cgo never touches the daemon — DuckDB runs out-of-process in
// the curated image.
package catalog

import (
	"context"
	"log/slog"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	cataloggw "github.com/pyvvo/funcd/internal/catalog/gateway"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/store"
)

// DuckDBRuntime is the curated runtime name whose image the reconciler deploys the engine from
// (ADR-0086/0087 / ADR-0054 embedded-image set). ImageFor(DuckDBRuntime) → the real DuckDB 1.5.4 +
// ducklake/httpfs/quack image ref (node-built out-of-band; in-process the engine never reaches
// Ready, which is expected — the live engine comes up only on the node-gated lane).
const DuckDBRuntime = "duckdb"

// enginePort is the port the DuckDB/Quack engine binds in its netns (ADR-0086/0087): the Quack
// HTTP server. FUNCD_QUACK_PORT carries it to the shim.
const enginePort = 8080

// condReady is the readiness condition the CatalogService reconciler raises (ADR-0086): True once
// the provider-runtime reports the engine Ready on its Quack HTTP probe.
const condReady = "Ready"

// SecretResolver resolves a service's bound Secret names → an env-var map for engine injection,
// PDP-authorized for id (ADR-0022/0057). The reconciler depends on this local seam (ADR-0002
// import discipline) — satisfied by *secrets.Resolver, wired in pkg/funcd. A nil resolver disables
// secret injection: a CatalogService that declares spec.secrets then fails closed (not Ready).
type SecretResolver interface {
	ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

// ReconcilerDeps configures the KindCatalogService reconciler (ADR-0086/0087). Store + Provider are
// required; the rest tune the engine env (the bindings the ADR injects). When Derive/Secrets are
// nil the corresponding env is simply absent (in-memory/dev) — the engine still deploys but can't
// reach S3 / authenticate (it never reaches Ready in-process anyway).
type ReconcilerDeps struct {
	Store    store.Store
	Provider provider.Runtime
	Logger   *slog.Logger
	// Derive returns the deterministic per-(ns, name) SigV4 keypair (ADR-0085), derived over the
	// PROVIDER identity (ns, cs.Name). nil ⇒ no S3 keypair injected. Same shape the Function
	// reconciler's S3GatewayInjection.Derive carries.
	Derive func(ns, name string) (access, secret string)
	// Secrets resolves spec.secrets → env (the Quack token, ADR-0057). nil ⇒ a service declaring
	// spec.secrets fails closed.
	Secrets SecretResolver
	// DeveloperFor returns the PDP identity the secret reads are authorized as (ADR-0057). Defaulted
	// to the namespace-scoped developer identity when Secrets is set and this is nil.
	DeveloperFor func(ns v1.NamespaceName) auth.Identity
	// S3Endpoint is the sandbox-facing S3 URL injected as AWS_ENDPOINT_URL_S3 (ADR-0085). Empty ⇒
	// the var is unset.
	S3Endpoint string
	// ImageFor maps a curated runtime name → the engine image ref (ADR-0054). nil ⇒ a deterministic
	// fallback "funcd/runtime-<runtime>" is used.
	ImageFor func(runtime string) string
	// Proxy runs the per-CatalogService node-private catalog PEP proxy (ADR-0137): on the Ready
	// branch the reconciler Ensures a proxy fronting the engine and publishes ITS url as
	// Status.Endpoint (internal functions inject the proxy, not the engine), and Removes it on
	// teardown. nil ⇒ no internal PEP proxy (the engine address is published directly, the
	// pre-ADR-0137 posture) — kept nil-safe for the in-memory/unit path.
	Proxy *cataloggw.Manager
	// Routes contributes this catalog's OPT-IN external ingress entry (source "catalog/<ns>/<name>")
	// to the shared edge-router aggregator (ADR-0138). When spec.ingress is set and the catalog is
	// Ready, the reconciler programs an edge entry whose Upstream is the PEP PROXY (never the engine);
	// cleared on not-Ready / delete. nil ⇒ no external exposure (the in-memory/dev path).
	Routes router.EntrySetter

	// SupervisionPeriod is the requeue of a Ready CatalogService (ADR-0142); 0 ⇒ controller.SupervisionPeriod.
	SupervisionPeriod time.Duration
}

// Reconciler is the controller.Reconciler for KindCatalogService (ADR-0086/0087): present ⇒
// assemble a provider.ProviderSpec + Converge the engine via the provider-runtime + reflect status;
// absent (deleted) ⇒ Teardown the engine.
type Reconciler struct {
	store        store.Store
	prov         provider.Runtime
	logger       *slog.Logger
	derive       func(ns, name string) (access, secret string)
	secrets      SecretResolver
	developerFor func(ns v1.NamespaceName) auth.Identity
	s3Endpoint   string
	imageFor     func(string) string
	proxy        *cataloggw.Manager
	routes       router.EntrySetter
	period       time.Duration // steady-state requeue (ADR-0142)
}

// NewReconciler builds the CatalogService reconciler. Store + Provider are required.
func NewReconciler(d ReconcilerDeps) (*Reconciler, error) {
	const op = "services.catalog.NewReconciler"
	switch {
	case d.Store == nil:
		return nil, fault.Invalidf(op, "store is required")
	case d.Provider == nil:
		return nil, fault.Invalidf(op, "provider runtime is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	developerFor := d.DeveloperFor
	if d.Secrets != nil && developerFor == nil {
		developerFor = defaultDeveloperFor
	}
	imageFor := d.ImageFor
	if imageFor == nil {
		imageFor = func(rt string) string { return "funcd/runtime-" + rt }
	}
	period := d.SupervisionPeriod
	if period <= 0 {
		period = controller.SupervisionPeriod
	}
	return &Reconciler{
		store:        d.Store,
		prov:         d.Provider,
		logger:       logger.With("component", "services.catalog.reconciler"),
		derive:       d.Derive,
		secrets:      d.Secrets,
		developerFor: developerFor,
		s3Endpoint:   d.S3Endpoint,
		imageFor:     imageFor,
		proxy:        d.Proxy,
		routes:       d.Routes,
		period:       period,
	}, nil
}

// catalogRouteSource is the catalog's edge-aggregator source key (ADR-0138): its external ingress
// entry is one partition of the shared edge table, keyed "catalog/<ns>/<name>" — disjoint from the
// Route reconciler's "routes" partition.
func catalogRouteSource(ns v1.NamespaceName, name v1.ObjectName) string {
	return "catalog/" + string(ns) + "/" + string(name)
}

// engineName is the engine identity published in status.Function (kept from ADR-0086, repointed at
// the engine, not a backing Function): "<cs-name>-duckdb". Deterministic; the provider worker
// itself is named for the CatalogService (cs.Name), the provider identity.
func engineName(csName string) string { return csName + "-duckdb" }

// defaultDeveloperFor synthesizes the namespace-scoped developer identity the PDP secret reads use
// when Deps.DeveloperFor is unset (ADR-0057 Decision 4) — the same posture the Function reconciler
// uses. V1 is single-tenant per namespace, so the developer may read its own namespace's secrets.
func defaultDeveloperFor(ns v1.NamespaceName) auth.Identity {
	return auth.Identity{
		Subject:    "system:secret-injector:" + string(ns),
		Role:       auth.RoleDeveloper,
		Namespaces: []v1.NamespaceName{ns},
	}
}
