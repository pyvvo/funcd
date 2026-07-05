package funcd

import (
	"log/slog"
	"time"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/bus"
	"github.com/green-0-rabbit/funcd/internal/controlplane/middleware"
	"github.com/green-0-rabbit/funcd/internal/function"
	"github.com/green-0-rabbit/funcd/internal/gateway"
	"github.com/green-0-rabbit/funcd/internal/kvstore"
	"github.com/green-0-rabbit/funcd/internal/platform/observability"
	"github.com/green-0-rabbit/funcd/internal/runtime"
	"github.com/green-0-rabbit/funcd/internal/store"
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

// WithWorkflow tunes the workflow engine (ADR-0094). The engine is always wired; this option
// sets its persistence + tunables: dataDir is the Badger run-state directory (empty ⇒ in-memory,
// the default / InMemory-preset path), defaultStepTimeout bounds a single step invocation
// (0 ⇒ none), retention is how long terminal runs survive before the periodic sweep reclaims them
// (0 ⇒ never), defaultRetry is the per-step attempt cap when a step declares no retry (< 1 ⇒ 1),
// and payloadLimit caps a run's input (at admission) and a step's output in bytes (0 ⇒ unbounded).
// cmd/funcd derives dataDir as <dataDir>/workflow from config.
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

// WithArtifactStore enables the OCI artifact Materializer (ADR-0031): functions are
// pulled by digest from their `artifact.uri` into a per-digest cache under dir. When a
// runtime shim is configured and no explicit Materializer is set, this selects the oras
// driver over the local-file stand-in.
func WithArtifactStore(dir string) Option {
	return func(c *config) error { c.artifactDir = dir; return nil }
}

// WithInvokeSocketDir sets the directory for the per-function worker-node local API sockets
// (ADR-0064, fn-to-fn invoke). cmd/funcd sets it to <dataDir>/invoke from config; unset
// (InMemory()/tests) ⇒ a temp dir.
func WithInvokeSocketDir(dir string) Option {
	return func(c *config) error { c.invokeSocketDir = dir; return nil }
}

// WithDataPlaneAddr sets the data-plane (function-invocation) listen address (ADR-0033).
// Default is 0.0.0.0:8081 (Production); InMemory() uses an ephemeral 127.0.0.1:0.
func WithDataPlaneAddr(addr string) Option {
	return func(c *config) error { c.dataPlaneAddr = addr; return nil }
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
