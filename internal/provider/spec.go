// Package provider holds the platform provider catalog (ADR-0082) and the add-on-provider
// management runtime (ADR-0087, FEAT-0003/F57).
//
// The catalog is the blueprint provider model in code. A provider is a shared platform capability
// endpoint — funcd's wasmCloud-style capability provider (a binding is the link, a port + ≥2
// drivers the contract) — in two tiers: built-in (in-daemon, pure-Go, trusted core) and add-on (an
// out-of-daemon deployed service function). A Descriptor names a provider's existing ADR-0019
// four-part shape (CRD + facade + controller + port + drivers); the catalog only classifies and
// replaces no facade, port or driver.
//
// The runtime is the mechanism funcd uses to deploy and supervise a curated engine image
// (DuckDB/Quack today; a vector DB, an inference server tomorrow) as a governed, gateway-exposed,
// health-probed service. It is PURE-GO orchestration over the EXISTING ports — the
// container-execution port (internal/runtime, ADR-0032/0054) and the ingress gateway
// (internal/gateway, ADR-0013). It is NOT the Function controller and imposes NO Function shape
// gate (ADR-0020): a provider has no user artifact/handler — the curated image's entrypoint IS the
// engine — and serves its own protocol with its own HTTP readiness probe, not the funcd shim's
// /health/readiness.
//
// A per-provider reconciler (the F48 CatalogService first) assembles a ProviderSpec and calls
// Converge on every reconcile; Converge is re-entrant + idempotent and a crashed engine is
// recreated on the next pass (supervision = re-convergence, no separate watchdog). Teardown stops
// and removes every engine replica and removes any programmed route.
package provider

import (
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ReadinessProbe is the engine's own HTTP health check (a provider serves its protocol, not the
// funcd shim). The probe is GET <Path>; the engine is Ready only once it answers ExpectStatus.
type ReadinessProbe struct {
	Path         string // e.g. "/" (Quack's RPC endpoint answers 200 there); GET only
	ExpectStatus int    // e.g. 200
}

// ProviderRef is the (namespace, name) identity of one provider instance (ADR-0087). It is the
// one identity the provider has: the ADR-0085 keypair is derived from it, and it is the value a
// Bucket prefix names as `owner` to grant the engine write.
type ProviderRef struct {
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}

// RouteSpec is the OPTIONAL ingress exposure of a provider (ADR-0087/0013). When a ProviderSpec
// carries one, the gateway reverse-proxies a matched request to the engine once it is Ready.
type RouteSpec struct {
	ID         string // stable route id (e.g. "<ns>/<name>")
	PathPrefix string // the ingress path the engine is exposed at
	Host       string // optional host match ("" = any host)
}

// ResourceSpec sizes the engine (cpu/mem). Forward-compat, mirrors ADR-0086's recorded sizing;
// the runtime maps it onto runtime.Limits only when the values are numerically parseable.
type ResourceSpec struct {
	CPU    string // e.g. "2" (cores); "" ⇒ unset
	Memory string // e.g. "4Gi"; "" ⇒ unset
}

// ProviderSpec is the desired state of one add-on-provider engine, assembled by a per-provider
// reconciler. The Env is caller-assembled (the ADR-0085 keypair, the engine token, config) — the
// runtime puts it verbatim on the worker; it owns no engine-specific logic.
type ProviderSpec struct {
	Ref       ProviderRef       // (namespace, name) — identity of the provider instance
	Image     string            // the curated engine image ref (ADR-0054 embedded set), e.g. funcd/runtime-duckdb
	Port      int               // the serving port the engine binds in its netns (e.g. 8080)
	Env       map[string]string // injected env (the ADR-0085 keypair, the engine token, config) — caller-assembled
	Readiness ReadinessProbe    // the engine's HTTP readiness probe
	Replicas  int               // pinned count (1 for a stateful single-writer engine); 0 is invalid here
	Resources ResourceSpec      // cpu/mem sizing (forward-compat, mirrors ADR-0086)
	// Route is OPTIONAL ingress exposure. Non-nil ⇒ program a gateway route once Ready (behind the
	// gateway's auth; eventually externally reachable). nil ⇒ INTERNAL-ONLY: no route is programmed;
	// the engine is reachable by in-platform clients via the published netns endpoint
	// (ProviderStatus.Address).
	Route *RouteSpec
}

// ProviderStatus is what Converge observes back to the caller (which writes it to its CRD status).
type ProviderStatus struct {
	Running  int    // engine replicas running
	Ready    bool   // the readiness probe passed
	Address  string // the engine's in-platform netns endpoint "host:port" — set when running (in-platform clients dial this)
	Endpoint string // the ingress path clients reach when a Route is programmed; "" if internal-only
	Reason   string // when not Ready, why (e.g. "EngineNotReady")
}
