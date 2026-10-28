// Package provider holds the platform provider catalog (ADR-0082) and the add-on-provider
// management runtime (ADR-0087, FEAT-0003/F57).
//
// The provider catalog is the blueprint provider model in code. A provider is a shared platform capability
// endpoint — funcd's wasmCloud-style capability provider (a binding is the link, a port + ≥2
// drivers the contract) — in two tiers: built-in (in-daemon, pure-Go, trusted core) and add-on (an
// out-of-daemon deployed service function). A Descriptor names a provider's existing ADR-0019
// four-part shape (CRD + facade + controller + port + drivers); the provider catalog only classifies and
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
// A per-provider reconciler (the F48 Lake first) assembles a ProviderSpec and calls
// Converge on every reconcile; Converge is re-entrant + idempotent and a crashed engine is
// recreated on the next pass (supervision = re-convergence, no separate watchdog). Teardown stops
// and removes every engine replica and removes any programmed route.
package provider

import (
	"net/netip"
	"net/url"
	"strconv"
	"time"

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

// StorageSpec asks for a durable host directory mounted into the engine (ADR-0231): kept across re-creations,
// teardowns and daemon restarts, removed only by Volumes.Reclaim. UID and GID are the user the engine's image runs as;
// the provider's ADR pins them equal to that image's user.
type StorageSpec struct {
	Path string // the mount path inside the engine, absolute
	UID  int
	GID  int
}

// ScratchSpec asks for a filesystem of fixed size mounted into the engine (ADR-0294): a preallocated image made new
// at each engine start and removed with the engine, so the engine's writes there stay within SizeBytes.
type ScratchSpec struct {
	Path      string // the mount path inside the engine, absolute
	SizeBytes int64
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
	// Storage is OPTIONAL durable storage (ADR-0231); nil ⇒ the engine keeps nothing across re-creations.
	Storage *StorageSpec
	// Scratch is an OPTIONAL filesystem of fixed size (ADR-0294); nil, or a runtime with no scratch root, ⇒ none.
	Scratch *ScratchSpec
	// EgressOnly is copied to the engine's WorkerSpec: nil means no fence (ADR-0269).
	EgressOnly []netip.AddrPort
	// MaxAge, when positive, makes Converge replace a running engine older than it, so a new one carries a fresh key
	// pair (ADR-0270). 0 ⇒ never replaced for age.
	MaxAge time.Duration
	// IssueEnv, when set, returns env issued with each engine as Converge creates it, laid over Env: a key pair valid
	// for that engine's life (ADR-0270 Decision 5). Only its keys are part of the spec an engine is compared by (#1195),
	// so a new key window replaces no engine, while gaining or losing a pair does.
	IssueEnv func() map[string]string
}

// ProviderStatus is what Converge observes back to the caller (which writes it to its CRD status).
type ProviderStatus struct {
	Running  int    // engine replicas running
	Ready    bool   // the readiness probe passed
	Address  string // the engine's in-platform netns endpoint "host:port" — set when running (in-platform clients dial this)
	Endpoint string // the ingress path clients reach when a Route is programmed; "" if internal-only
	Reason   string // when not Ready, why (e.g. "EngineNotReady")
	Report   string // the ready replica's report (runtime.Instance.Report, ADR-0276); only the Lake reads it
}

// EgressTo is the fence of an engine that dials only rawURL (ADR-0269): its address and port, with the scheme's port
// when it names none. An empty rawURL is an empty fence. ok is false when the host is not an IP literal, which a
// fence cannot hold.
func EgressTo(rawURL string) (fence []netip.AddrPort, ok bool) {
	if rawURL == "" {
		return []netip.AddrPort{}, true
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return []netip.AddrPort{}, false
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil {
		return []netip.AddrPort{}, false
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return []netip.AddrPort{}, false
	}
	return []netip.AddrPort{netip.AddrPortFrom(addr.Unmap(), uint16(n))}, true
}
