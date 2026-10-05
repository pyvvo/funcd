package funcd

import (
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/bus"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/edge/observ"
	"github.com/pyvvo/funcd/internal/edge/shape"
	edgetls "github.com/pyvvo/funcd/internal/edge/tls"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/network"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workflow"
)

// Option configures the platform. Each With* function returns an Option that
// injects one dependency. Options are validated by New before the platform is
// returned (ADR-0002 functional-options facade).
type Option func(*config) error

// WithStore injects the metastore (database layer) port.
func WithStore(s store.Store) Option {
	return func(c *config) error { c.store = s; return nil }
}

// WithKVStore injects the function-facing KV driver (ADR-0066/0069). Absent ⇒ the in-memory driver.
func WithKVStore(kv kvstore.KV) Option {
	return func(c *config) error { c.kvStore = kv; return nil }
}

// WithKVStoreQuota sets the per-namespace KVStore count cap enforced at admission (ADR-0072,
// kvstore.maxStoresPerNamespace). 0 ⇒ the default (100); a negative value disables the quota.
func WithKVStoreQuota(maxPerNamespace int) Option {
	return func(c *config) error { c.kvMaxStoresPerNamespace = maxPerNamespace; return nil }
}

// WithBlob injects the blob (storage layer) port.
func WithBlob(b blob.Bucket) Option {
	return func(c *config) error { c.blob = b; return nil }
}

// WithBus injects the messaging port.
func WithBus(b bus.Bus) Option {
	return func(c *config) error { c.bus = b; return nil }
}

// WithFunclog tunes structured function-log capture (ADR-0081): the segment seal age and size
// (either 0 keeps the sink default — 10s / 8 MiB). Capture is on by default when the runtime
// supports it; use WithoutFunclog to disable.
func WithFunclog(segmentMaxAge time.Duration, segmentMaxBytes int) Option {
	return func(c *config) error {
		c.funclogMaxAge, c.funclogMaxBytes = segmentMaxAge, segmentMaxBytes
		return nil
	}
}

// WithoutFunclog disables structured function-log capture (Path B); raw stdout/stderr still flows.
func WithoutFunclog() Option {
	return func(c *config) error { c.funclogDisabled = true; return nil }
}

// WithoutFunclogTraces disables the traces signal (ADR-0101, per-invocation spans) while keeping the
// logs signal. Subordinate to WithoutFunclog: with the whole funclog channel off, there is no channel
// and this is moot. With the channel on but traces off, the host's trace sink is nil and span lines
// are read off the channel and dropped. Traces are on by default when the runtime supports capture.
func WithoutFunclogTraces() Option {
	return func(c *config) error { c.funclogTracesDisabled = true; return nil }
}

// WithLogCompaction tunes function-log compacted compaction (ADR-0083): the window (bucket size + close
// threshold), the pass interval, and the compacted retention. A zero window/interval keeps the default
// (1h / 5m); retention <= 0 keeps compacted forever. Compaction is on by default when a blob substrate is
// present; use WithoutLogCompaction to disable.
func WithLogCompaction(window, interval, retention time.Duration) Option {
	return func(c *config) error {
		c.logCompactWindow, c.logCompactInterval, c.logCompactRetention = window, interval, retention
		c.logCompactConfigured = true
		return nil
	}
}

// WithoutLogCompaction disables compacted compaction: no compactor goroutine runs and raw OTLP-JSONL is left
// untouched (capture itself still runs).
func WithoutLogCompaction() Option {
	return func(c *config) error { c.logCompactDisabled = true; return nil }
}

// WithS3Gateway enables the opt-in S3-protocol frontend over the blob substrate
// (ADR-0080/0085): a node-private TCP listener serving GET(+range)/PUT(+multipart)/
// HEAD/DELETE/ListObjectsV2 governed by the cedar spec.blob binding-as-grant PEP, with
// per-function SigV4 keypairs derived from a node master secret and injected into a
// spec.blob function's worker env. listenAddr is node-private (e.g. 127.0.0.1:9000);
// maxUploadBytes (0 ⇒ 1 GiB) caps a single buffered object; masterSecretFile (empty ⇒
// generate+persist 0600 under dataDir/s3gateway/master.key) supplies the node master.
// Without this option no listener, IAM, or keypair injection exists.
func WithS3Gateway(listenAddr, endpoint string, maxUploadBytes int64, masterSecretFile, dataDir string) Option {
	return func(c *config) error {
		c.s3gwEnabled = true
		c.s3gwListenAddr = listenAddr
		c.s3gwEndpoint = endpoint
		c.s3gwMaxUploadBytes = maxUploadBytes
		c.s3gwMasterFile = masterSecretFile
		c.s3gwDataDir = dataDir
		return nil
	}
}

// WithCatalogProxyHost sets the netns-reachable host the per-CatalogService catalog PEP proxies publish
// (ADR-0137) — the query-path analog of the S3 gateway's sandbox-facing Endpoint host. Under containerd a
// worker runs in its OWN netns and cannot reach the daemon's 127.0.0.1, so set this to the CNI bridge
// gateway IP (e.g. "10.63.0.1"): the proxy then binds 0.0.0.0 and publishes that host in
// FUNCD_CATALOG_<ALIAS>_URL. Empty (the default / process-runtime / funcdctl dev) ⇒ 127.0.0.1, where the
// daemon and the worker share the loopback.
func WithCatalogProxyHost(host string) Option {
	return func(c *config) error {
		c.catalogProxyHost = host
		return nil
	}
}

// WithWorkflow tunes the workflow engine (ADR-0094). The engine is always wired; without this option it
// runs in memory with the daemon config's defaults (300s step timeout, 720h retention, one attempt,
// 256 KiB payload cap). This option sets its persistence + tunables: dataDir is the Badger run-state directory
// (empty ⇒ in-memory), defaultStepTimeout bounds a single step invocation (0 ⇒ none), retention is how
// long terminal runs survive before the periodic sweep reclaims them (0 ⇒ never), defaultRetry is the
// per-step attempt cap when a step declares no retry (< 1 ⇒ 1), and payloadLimit caps a run's input (at
// admission) and a step's output in bytes (0 ⇒ unbounded). cmd/funcd derives dataDir as
// <dataDir>/workflow from config.
func WithWorkflow(dataDir string, defaultStepTimeout, retention time.Duration, defaultRetry int, payloadLimit int64) Option {
	return func(c *config) error {
		c.workflowDataDir = dataDir
		c.workflowStepTimeout = defaultStepTimeout
		c.workflowRetention = retention
		c.workflowDefaultRetry = defaultRetry
		c.workflowPayloadLimit = payloadLimit
		return nil
	}
}

// WithDeadLetterQueue tunes the eventing DLQ + bounded action-delivery retry (ADR-0118, F85). The DLQ is
// always wired; without this option it runs in memory with the daemon config's defaults (720h retention, a
// per-namespace cap of 1000, three delivery attempts). This option sets its persistence + tunables: dataDir
// is the dedicated Badger directory (empty ⇒ in-memory, the InMemory-preset / memory-storage path),
// deliveryAttempts is the bounded-retry cap before a failed workflow:/function: delivery is dead-lettered
// (< 1 ⇒ 3), retention is how long parked entries survive the periodic sweep (0 ⇒ never), and maxEntries is
// the per-namespace count cap (0 ⇒ unbounded). cmd/funcd derives dataDir as <dataDir>/deadletter from config.
func WithDeadLetterQueue(dataDir string, deliveryAttempts int, retention time.Duration, maxEntries int) Option {
	return func(c *config) error {
		c.deadletterDataDir = dataDir
		c.deliveryAttempts = deliveryAttempts
		c.deadletterRetention = retention
		c.deadletterMaxEntries = maxEntries
		return nil
	}
}

// WithBlobPollInterval sets the platform-wide cadence at which a `blob:` EventSource's prefixes are
// List-polled for new objects (ADR-0119, F83). A duration ≤ 0 ⇒ the 15s default. One cadence for all blob
// sources in V1 (a per-source override is an open question).
func WithBlobPollInterval(d time.Duration) Option {
	return func(c *config) error { c.blobPollInterval = d; return nil }
}

// WithSiteDefaultIndex sets the document a Site serves for "/" (and asserts present before Ready) when
// its spec.index is empty (ADR-0139, F103). "" ⇒ "index.html". A relative path, never a leading '/'.
func WithSiteDefaultIndex(index string) Option {
	return func(c *config) error {
		if strings.HasPrefix(index, "/") {
			return fault.Invalidf("funcd.WithSiteDefaultIndex", "site default index %q must be a relative path (no leading '/')", index)
		}
		c.siteDefaultIndex = index
		return nil
	}
}

// WithRuntime injects the function runtime (worker) port.
func WithRuntime(r runtime.Runtime) Option {
	return func(c *config) error { c.runtime = r; return nil }
}

// WithGateway injects the ingress / API gateway port.
func WithGateway(g gateway.Gateway) Option {
	return func(c *config) error { c.gateway = g; return nil }
}

// WithRuntimeShim enables real function execution (ADR-0030): cmd is the launch prefix
// for the runtime shim (e.g. "node", "/opt/funcd/shim.mjs"), to which the reconciler
// appends the artifact + handler via the environment. With no shim configured the
// reconciler runs the legacy placeholder (no execution).
func WithRuntimeShim(cmd ...string) Option {
	return func(c *config) error { c.runtimeShim = cmd; return nil }
}

// WithRuntimeShimFor registers a shim launch prefix for one runtime family (ADR-0049): a
// function whose spec.runtime starts with runtimeFamily (e.g. "python") is launched with cmd
// instead of the default WithRuntimeShim. Lets one daemon run several curated languages — e.g.
// node by default plus WithRuntimeShimFor("python", "python3", "/opt/funcd/shim.py"). The rest
// of execution (artifact + handler env, portfile handshake, readiness) is identical per language.
func WithRuntimeShimFor(runtimeFamily string, cmd ...string) Option {
	return func(c *config) error {
		if c.runtimeShimByFamily == nil {
			c.runtimeShimByFamily = map[string][]string{}
		}
		c.runtimeShimByFamily[runtimeFamily] = cmd
		return nil
	}
}

// WithPoolShim enables worker pooling (ADR-0046): cmd is the launch prefix for the pooled
// worker_threads host (e.g. "node", "/opt/funcd/pool.mjs"). A function declaring
// spec.pooling.worker then co-locates with same-(namespace, runtime, worker-id) peers in one
// pool worker, launched with this command + the FUNCD_POOL_MANIFEST of its admitted members.
// With no pool shim configured pooling is off and every function runs solo (the default).
func WithPoolShim(cmd ...string) Option {
	return func(c *config) error { c.poolShim = cmd; return nil }
}

// WithPoolShimFor registers a pooled-host launch prefix for one runtime family (ADR-0050): a
// python* function that opts into spec.pooling.worker co-pools via this host (the subinterpreter
// pool.py) instead of staying solo. WithPoolShim remains the node default. A runtime family with no
// pool host runs solo — so this is additive and host-gated. Used as
// WithPoolShimFor("python", "python3.14", "/opt/funcd/pool.py").
func WithPoolShimFor(runtimeFamily string, cmd ...string) Option {
	return func(c *config) error {
		if c.poolShimsByFamily == nil {
			c.poolShimsByFamily = map[string][]string{}
		}
		c.poolShimsByFamily[runtimeFamily] = cmd
		return nil
	}
}

// WithPoolLimit caps handlers per pool worker (ADR-0046 Decision 3): over-cap members
// declaring one worker id are held NotReady with a PoolFull condition. 0 ⇒ the default (16).
func WithPoolLimit(limit int) Option {
	return func(c *config) error { c.poolLimit = limit; return nil }
}

// WithMaterializer overrides the artifact Materializer (ADR-0030). When a shim is
// configured but no Materializer is supplied, the local-file driver is used (the OCI
// driver is P-V-A). Has no effect unless a runtime shim is also configured.
func WithMaterializer(m function.Materializer) Option {
	return func(c *config) error { c.materializer = m; return nil }
}

// WithWorkflowContractResolver overrides the workflow F65 typed-edge ContractResolver (ADR-0098). The
// production resolver reads a step image's I/O contract from OCI metadata; `funcdctl dev` (ADR-0125)
// injects a resolver for from-source steps, whose file:// bundles carry no OCI artifact to inspect.
func WithWorkflowContractResolver(r workflow.ContractResolver) Option {
	return func(c *config) error { c.workflowContracts = r; return nil }
}

// WithLogObserver streams every captured function log line to obs as it happens, in addition to the
// normal blob-persisted capture — `funcdctl dev` uses it to print logs to the terminal in real time.
// obs runs on the log-routing goroutine and must not block.
func WithLogObserver(obs LogObserver) Option {
	return func(c *config) error { c.logObserver = obs; return nil }
}

// WithCatalogProviderRuntime overrides the CatalogService add-on-provider runtime (ADR-0087). The
// production runtime supervises the curated duckdb CONTAINER image; `funcdctl dev` (ADR-0125) injects
// a process-mode driver that runs the embedded DuckDB+Quack engine as a host subprocess, since
// process-dev runs no containers.
func WithCatalogProviderRuntime(r provider.Runtime) Option {
	return func(c *config) error { c.catalogProvider = r; return nil }
}

// WithCatalogExtensionDir points catalog-consumer functions at a local DuckDB extension directory,
// injected into their worker env as DUCKDB_EXTENSION_DIRECTORY so a handler's `LOAD quack`/`ducklake`
// resolves the curated extensions. It is the `funcdctl dev` (ADR-0125) analogue of the prod bundle's
// duckdb-ext (ADR-0089): dev extracts the embedded catalog engine's extensions once and points
// consumers at them, since a dev handler runs from source with no bundle. Empty ⇒ no injection (prod).
func WithCatalogExtensionDir(dir string) Option {
	return func(c *config) error { c.catalogExtensionDir = dir; return nil }
}

// WithArtifactStore enables the OCI artifact Materializer (ADR-0031): functions are
// pulled by digest from their `spec.image` into a per-digest cache under dir. When a
// runtime shim is configured and no explicit Materializer is set, this selects the oras
// driver over the local-file stand-in.
func WithArtifactStore(dir string) Option {
	return func(c *config) error { c.artifactDir = dir; return nil }
}

// WithNodePlatform sets the platform this node runs (ADR-0145): the scheduler refuses an artifact built for
// other platforms, and the materializer pulls an image index's manifest for this one. Default: the daemon's
// own GOOS/GOARCH (v1.HostPlatform), which is right whenever funcd runs on the node it schedules.
func WithNodePlatform(p v1.OCIPlatform) Option {
	return func(c *config) error {
		if err := p.Validate(); err != nil {
			return fault.Wrapf(err, fault.Invalid, "funcd.WithNodePlatform", "node platform")
		}
		c.nodePlatform = p
		return nil
	}
}

// WithInvokeSocketDir sets the directory for the per-function worker-node local API sockets
// (ADR-0064, fn-to-fn invoke). cmd/funcd sets it to <dataDir>/invoke from config; unset
// (InMemory()/tests) ⇒ a temp dir. New fails with fault.Invalid when the dir is too long for a Unix
// socket path.
func WithInvokeSocketDir(dir string) Option {
	return func(c *config) error { c.invokeSocketDir = dir; return nil }
}

// WithDataPlaneAddr sets the data-plane (function-invocation) listen address (ADR-0033).
// Default is 0.0.0.0:8081 (Production); InMemory() uses an ephemeral 127.0.0.1:0.
func WithDataPlaneAddr(addr string) Option {
	return func(c *config) error { c.dataPlaneAddr = addr; return nil }
}

// WithTLS enables TLS termination (ADR-0111, F74) on BOTH listeners (control-plane + data-plane) via
// ServeTLS — funcd keeps its own http.Servers (no handover). The zero Mode defaults to `selfsigned`
// (stdlib, offline, generated+persisted). Omit it entirely for plaintext (the back-compat default).
// selfsigned and acme persist key material under spec.StorageDir, which defaults to
// <artifact store>/funcd-tls; with neither set, Run fails with fault.Invalid.
func WithTLS(spec edgetls.Spec) Option {
	return func(c *config) error { s := spec; c.tlsSpec = &s; return nil }
}

// WithLimits enables the ingress-protection middleware (ADR-0112, F75) on the data-plane chain: a
// token-bucket rate limit (429), a Content-Length body-size cap (413), and an in-flight concurrency
// ceiling (503), all rejecting BEFORE the activator (zero wake). A zero Config is a pass-through.
func WithLimits(cfg limit.Config) Option {
	return func(c *config) error { c.limits = cfg; return nil }
}

// WithEdgeAuth enables the edge authn PEP (ADR-0113, F77) on the data plane: for a target whose
// stance is `authenticated`, the caller's bearer is authenticated (reusing the control-plane
// credentials) and the decision delegated to the authorizer, rejecting 401/403 before the activator.
// Without it, an `authenticated` stance fails closed (401); `open` targets are unaffected.
func WithEdgeAuth() Option {
	return func(c *config) error { c.edgeAuthEnabled = true; return nil }
}

// WithEdgeObservability enables edge observability (ADR-0114, F76): RED metrics + an edge trace span
// (traceparent) + an access log. Uses the injected Telemetry (no-op by default). Zero Config ⇒ off.
func WithEdgeObservability(cfg observ.Config) Option {
	return func(c *config) error { c.observ = cfg; return nil }
}

// WithEdgeShaping enables edge shaping (ADR-0114, F78): CORS, response header inject/strip, and gzip
// compression (skipping streaming/upgrades). Zero Config ⇒ off (pass-through).
func WithEdgeShaping(cfg shape.Config) Option {
	return func(c *config) error { c.shaping = cfg; return nil }
}

// WithEgressIsolation enables the worker network-isolation substrate (ADR-0115, FEAT-0007/F80): funcd
// programs an nftables policy (over the funcd0 CNI bridge) that default-denies worker egress and
// redirects remaining external TCP into the egress gateway (F81). mgr is the platform-specific Manager
// (network.New — a no-op when disabled or non-Linux); p is the ruleset frame. Applied at Run start
// (before workers serve), removed at Shutdown.
func WithEgressIsolation(mgr network.Manager, p network.Policy) Option {
	return func(c *config) error { c.netManager, c.netPolicy = mgr, p; return nil }
}

// WithEgressGateway wires the ADR-0117 (F81) transparent egress PEP + DNS forwarder: when enabled on
// Linux, funcd starts a gateway on gatewayPort (F80's REDIRECT target) that authorizes every outbound
// worker connection against its namespace's EgressPolicy via the cedar PDP (egress::connect over a
// forwarder-attested NetDestination), and a DNS forwarder on dnsForwarderPort (the domain trust anchor +
// the only reachable resolver). dnsUpstream is the node resolver the forwarder forwards to. Disabled or
// non-Linux ⇒ no-op (egress open). Pairs with WithEgressIsolation (same server.network.egress flag).
func WithEgressGateway(gatewayPort, dnsForwarderPort uint16, dnsUpstream netip.AddrPort) Option {
	return func(c *config) error {
		c.egressGatewayEnabled = true
		c.egressGatewayPort = gatewayPort
		c.dnsForwarderPort = dnsForwarderPort
		c.dnsUpstream = dnsUpstream
		return nil
	}
}

// WithContainerExecution enables curated-image container execution (ADR-0032): functions
// run in the containerd worker from the image imageFor(fn.Spec.Runtime) returns, with the
// shim as the image entrypoint, the artifact bind-mounted, and a fixed netns port. Used
// with the containerd runtime driver (prod); WithRuntimeShim is the process-driver path.
func WithContainerExecution(imageFor func(runtime string) string) Option {
	return func(c *config) error { c.imageFor = imageFor; return nil }
}

// WithLogger injects the root logger. If not set, a stdout text logger is built.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) error { c.logger = l; return nil }
}

// WithTelemetry injects the OTel telemetry pipeline. If not set, the no-op
// pipeline is used (no collector required).
func WithTelemetry(t *observability.Telemetry) Option {
	return func(c *config) error { c.telemetry = t; return nil }
}

// WithListenAddr sets the control-plane bind address (host:port). Default is the
// preset's (InMemory: an ephemeral "127.0.0.1:0"; Production: "0.0.0.0:8080").
func WithListenAddr(addr string) Option {
	return func(c *config) error { c.listenAddr = addr; return nil }
}

// WithAuthorizer injects the authorization PDP. Defaults to rbac.New().
func WithAuthorizer(a auth.Authorizer) Option {
	return func(c *config) error { c.authorizer = a; return nil }
}

// WithDevAuth wires a single developer-role credential for token, scoped to the
// given namespaces (ADR-0028). It is the V1 dev/test credential mechanism — not a
// production identity story (multi-role/issuance is V2) — and lets a public-surface
// caller authenticate without importing internal/auth. InMemory() applies a default
// (DevToken in "default"); Production() sets none (the operator supplies one).
func WithDevAuth(token string, namespaces ...string) Option {
	return func(c *config) error {
		nss := make([]v1.NamespaceName, len(namespaces))
		for i, n := range namespaces {
			nss[i] = v1.NamespaceName(n)
		}
		c.credentials = middleware.NewStaticCredentials(map[string]auth.Identity{
			token: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: nss},
		})
		return nil
	}
}
