// Package lake is the Lake reconciler (ADR-0086 as reworked by ADR-0087, F48/F57):
// the ADR-0019 controller half of the DuckLake/DuckDB/Quack add-on provider. Its engine is NO
// longer a backing Function — it is deployed by the add-on-provider runtime (internal/provider,
// ADR-0087): the reconciler resolves the service's bindings (spec.bindings.blob → the ADR-0085 per-fn S3
// keypair derived over the provider identity; spec.location → FUNCD_DUCKLAKE_CATALOG;
// spec.bindings.secret/spec.bindings.config → the Quack token + engine config) into a provider.ProviderSpec and
// calls provider.Runtime.Converge. It reflects the provider's readiness/address into status and
// tears the engine down on delete. cgo never touches the daemon — DuckDB runs out-of-process in
// the curated image.
package lake

import (
	"context"
	"log/slog"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/secrets"
	"github.com/pyvvo/funcd/internal/store"
)

// DuckDBRuntime is the curated runtime name whose image the reconciler deploys the engine from
// (ADR-0086/0087 / ADR-0054 embedded-image set). ImageFor(DuckDBRuntime) → the real DuckDB 2.0.0 +
// ducklake/httpfs/quack image ref (node-built out-of-band; in-process the engine never reaches
// Ready, which is expected — the live engine comes up only on the node-gated lane).
const DuckDBRuntime = "duckdb"

// enginePort is the port the DuckDB/Quack engine binds in its netns (ADR-0086/0087): the Quack
// HTTP server. FUNCD_QUACK_PORT carries it to the shim.
const enginePort = 8080

// condReady is the readiness condition the Lake reconciler raises (ADR-0086): True once
// the provider-runtime reports the engine Ready on its Quack HTTP probe. Every write stamps the generation it
// observed (ADR-0199 Decision 5).
const condReady = "Ready"

// condIngressReady reports whether the edge programs this lake's spec.ingress entry (ADR-0176): False names
// the aggregator's reason, or LakeNotReady while the lake has no proxy to expose.
const condIngressReady = v1.ConditionType("IngressReady")

// condCatalogSaved follows the engine's report of its catalog saves (ADR-0276): True with reasonSaved after a save,
// False with reasonSavesFailing while they fail. It never changes the phase, the proxy or the edge entry.
const (
	condCatalogSaved   = v1.ConditionType("CatalogSaved")
	reasonSaved        = "Saved"
	reasonSavesFailing = "SavesFailing"
)

// SecretResolver resolves a service's bound Secret names → an env-var map for engine injection,
// PDP-authorized for id (ADR-0022/0057). The reconciler depends on this local seam (ADR-0002
// import discipline) — satisfied by *secrets.Resolver, wired in pkg/funcd. A nil resolver disables
// secret injection: a Lake that declares spec.bindings.secret then fails closed (not Ready).
type SecretResolver interface {
	ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

// ReconcilerDeps configures the KindLake reconciler (ADR-0086/0087). Store + Provider are
// required; the rest tune the engine env (the bindings the ADR injects). When Derive/Secrets are
// nil the corresponding env is simply absent (in-memory/dev) — the engine still deploys but can't
// reach S3 / authenticate (it never reaches Ready in-process anyway).
type ReconcilerDeps struct {
	Store    store.Store
	Provider provider.Runtime
	Logger   *slog.Logger
	// Derive returns the deterministic per-(kind, ns, name) SigV4 keypair (ADR-0085, ADR-0175), derived
	// over the Lake identity (KindLake, ns, cs.Name). nil ⇒ no S3 keypair injected.
	// Same shape the Function reconciler's S3GatewayInjection.Derive carries.
	Derive func(kind v1.Kind, ns, name string) (access, secret string)
	// RenewalAge is the engine age at which its worker is replaced for a fresh key pair (ADR-0270); nil or 0 ⇒ never.
	RenewalAge func(kind v1.Kind, ns, name string) time.Duration
	// Secrets resolves spec.bindings.secret → env (the Quack token, ADR-0057). nil ⇒ a service declaring
	// spec.bindings.secret fails closed.
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
	// Proxy runs the per-Lake node-private lake PEP proxy (ADR-0137, ADR-0162): every pass Listens on
	// the recorded status.proxyPort, the Ready branch Ensures the proxy fronts the engine and publishes ITS url as
	// Status.Endpoint (internal functions inject the proxy, not the engine), a not-Ready pass Suspends it, and a
	// delete Releases it, closing it only once no Function binds the lake. nil ⇒ no internal PEP proxy (the
	// engine address is published directly, the pre-ADR-0137 posture) — kept nil-safe for the in-memory/unit path.
	Proxy *lakegw.Manager
	// Routes contributes this lake's OPT-IN external ingress entry (source "lake/<ns>/<name>")
	// to the shared edge-router aggregator (ADR-0138). When spec.ingress is set and the lake is
	// Ready, the reconciler programs an edge entry whose Upstream is the PEP PROXY (never the engine);
	// cleared on not-Ready / delete. nil ⇒ no external exposure (the in-memory/dev path).
	Routes router.EntrySetter

	// SupervisionPeriod is the requeue of a Ready Lake (ADR-0142); 0 ⇒ controller.SupervisionPeriod.
	SupervisionPeriod time.Duration
	// ReferentPollInterval re-checks a service waiting for a Bucket or a binding (controller.referentPollInterval,
	// ADR-0163); EnginePollInterval re-checks an engine not answering its probe yet (lake.enginePollInterval),
	// bounded by SupervisionPeriod. 0 ⇒ 2s.
	ReferentPollInterval time.Duration
	EnginePollInterval   time.Duration
	// SaveInterval is how often every engine saves its catalog after a commit (lake.saveInterval, ADR-0274), set as
	// the engine's FUNCD_LAKE_SAVE_INTERVAL; 0 ⇒ none set, and the engine saves every second.
	SaveInterval time.Duration
}

const defaultPollInterval = 2 * time.Second

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Reconciler is the controller.Reconciler for KindLake (ADR-0086/0087): present ⇒
// assemble a provider.ProviderSpec + Converge the engine via the provider-runtime + reflect status;
// absent (deleted) ⇒ Teardown the engine.
type Reconciler struct {
	store        store.Store
	prov         provider.Runtime
	logger       *slog.Logger
	derive       func(kind v1.Kind, ns, name string) (access, secret string)
	renewalAge   func(kind v1.Kind, ns, name string) time.Duration
	secrets      SecretResolver
	developerFor func(ns v1.NamespaceName) auth.Identity
	s3Endpoint   string
	imageFor     func(string) string
	proxy        *lakegw.Manager
	routes       router.EntrySetter
	period       time.Duration // steady-state requeue (ADR-0142)
	referentPoll time.Duration
	enginePoll   time.Duration
	saveInterval time.Duration
}

// NewReconciler builds the Lake reconciler. Store + Provider are required.
func NewReconciler(d ReconcilerDeps) (*Reconciler, error) {
	const op = "services.lake.NewReconciler"
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
		developerFor = secrets.InjectorIdentity
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
		logger:       logger.With("component", "services.lake.reconciler"),
		derive:       d.Derive,
		renewalAge:   d.RenewalAge,
		secrets:      d.Secrets,
		developerFor: developerFor,
		s3Endpoint:   d.S3Endpoint,
		imageFor:     imageFor,
		proxy:        d.Proxy,
		routes:       d.Routes,
		period:       period,
		referentPoll: orDefault(d.ReferentPollInterval, defaultPollInterval),
		enginePoll:   min(orDefault(d.EnginePollInterval, defaultPollInterval), period),
		saveInterval: d.SaveInterval,
	}, nil
}

// lakeRouteSource is the lake's edge-aggregator source key (ADR-0138): its external ingress
// entry is one partition of the shared edge table, keyed "lake/<ns>/<name>" — disjoint from the
// Route reconciler's "routes" partition.
func lakeRouteSource(ns v1.NamespaceName, name v1.ObjectName) string {
	return "lake/" + string(ns) + "/" + string(name)
}

// engineName is the engine identity published in status.Function (kept from ADR-0086, repointed at
// the engine, not a backing Function): "<cs-name>-duckdb". Deterministic; the provider worker
// itself is named for the Lake (cs.Name), the provider identity.
func engineName(csName string) string { return csName + "-duckdb" }
