// Package funcd is the platform-as-a-library facade (ADR-0014): the composition
// root that wires every port into a *Platform and owns its start/stop lifecycle.
// Run serves the control-plane API and runs the controller (ADR-0028); cmd/funcd is
// a thin shell over it; e2e tests embed it with the InMemory() preset — no daemon,
// no root, no network beyond the ephemeral control-plane listener.
package funcd

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/activator/storescaler"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/auth"
	cedarauth "github.com/pyvvo/funcd/internal/auth/cedar"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/bus"
	cataloggw "github.com/pyvvo/funcd/internal/catalog/gateway"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/authn"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/edge/observ"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/edge/shape"
	"github.com/pyvvo/funcd/internal/edge/static"
	edgetls "github.com/pyvvo/funcd/internal/edge/tls"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/eventing/eventstore"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/funclog/compact"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/network"
	"github.com/pyvvo/funcd/internal/network/egress"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/route"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/secrets"
	"github.com/pyvvo/funcd/internal/sensor"
	"github.com/pyvvo/funcd/internal/services"
	blobsvc "github.com/pyvvo/funcd/internal/services/blob"
	catalogsvc "github.com/pyvvo/funcd/internal/services/catalog"
	identitysvc "github.com/pyvvo/funcd/internal/services/identity"
	kvsvc "github.com/pyvvo/funcd/internal/services/kv"
	rolessvc "github.com/pyvvo/funcd/internal/services/roles"
	"github.com/pyvvo/funcd/internal/site"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/internal/workflow"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	wbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// DevToken is the default control-plane credential token wired by InMemory(). It
// lets a test/tool authenticate against an embedded platform; it is NOT a
// production identity story (Production() wires no default token — ADR-0028).
const DevToken = "funcd-dev-token"

const (
	defaultProdAddr      = "0.0.0.0:8080"
	defaultDataPlaneAddr = "0.0.0.0:8081" // function-traffic ingress (ADR-0033); intentionally public (auth is V2)
	defaultLocalNode     = "local"
	shutdownTimeout      = 15 * time.Second
	// closeTimeout is Shutdown's own bound when Run stops, so an HTTP drain that used all of shutdownTimeout still
	// leaves the log Routes and telemetry time to flush (issue #453); 15 s + 5 s fits the unit's TimeoutStopSec=30.
	closeTimeout = 5 * time.Second
	// defaultKVStoresPerNamespace is the per-namespace KVStore count cap when unset (ADR-0072).
	defaultKVStoresPerNamespace = 100
	// defaultBucketsPerNamespace is the per-namespace Bucket count cap when unset (ADR-0080).
	defaultBucketsPerNamespace = 100
	// The workflow engine tunables when WithWorkflow is not given: the daemon config's workflow.* defaults
	// (ADR-0094, internal/platform/config), so a hung step fails the same way in dev and in production.
	defaultWorkflowStepTimeout  = 300 * time.Second
	defaultWorkflowRetention    = 720 * time.Hour
	defaultWorkflowRetry        = 1
	defaultWorkflowPayloadLimit = 256 << 10
	// defaultWorkflowMaxStepsInFlight is workflow.maxStepsInFlight's default (ADR-0146).
	defaultWorkflowMaxStepsInFlight = 64
	// The eventing DLQ bounds when WithDeadLetterQueue is not given: the daemon config's eventing.deadletter.*
	// defaults (ADR-0118), so a dead letter is evicted the same way in dev and in production.
	defaultDeadletterRetention  = 720 * time.Hour
	defaultDeadletterMaxEntries = 1000
	// defaultNestedInFlightCap bounds the nested fn-to-fn calls in flight to one Function (ADR-0147).
	defaultNestedInFlightCap = 10
)

// config holds the injected world — validated by validate() before New returns.
type config struct {
	store   store.Store
	kvStore kvstore.KV // the function-facing KV driver (ADR-0066/0069); nil ⇒ in-memory default
	// kvMaxStoresPerNamespace is the per-namespace KVStore count cap at admission (ADR-0072); 0 ⇒
	// the default (100); negative disables the quota.
	kvMaxStoresPerNamespace int
	// bucketMaxPerNamespace is the per-namespace Bucket count cap at admission (ADR-0080); 0 ⇒
	// the default (100); negative disables the quota.
	bucketMaxPerNamespace int
	// nestedInFlightCap is the per-target cap on nested fn-to-fn calls in flight (ADR-0147); 0 ⇒ the default (10).
	nestedInFlightCap int
	blob              blob.Bucket
	// funclog structured function-log capture (ADR-0081): on by default when the runtime supports it.
	funclogDisabled bool
	funclogMaxAge   time.Duration // segment seal age; 0 ⇒ sink default (10s)
	funclogMaxBytes int           // segment seal size; 0 ⇒ sink default (8 MiB)
	// funclogMaxRecordBytes bounds one shim record line (ADR-0168); 0 ⇒ funclog.DefaultMaxRecordBytes.
	funclogMaxRecordBytes int
	// funclog traces signal (ADR-0101): per-invocation spans on the same channel; on by default,
	// subordinate to the funclog channel (no channel ⇒ moot). WithoutFunclogTraces disables it.
	funclogTracesDisabled bool
	// funclog compacted compaction (ADR-0083): on by default when a blob substrate is present. When
	// logCompactConfigured is false the defaults apply (window 1h / interval 5m / retention 30d);
	// WithLogCompaction sets explicit values (retention <= 0 ⇒ keep forever); WithoutLogCompaction disables it.
	logCompactDisabled   bool
	logCompactConfigured bool
	logCompactWindow     time.Duration
	logCompactInterval   time.Duration
	logCompactRetention  time.Duration
	bus                  bus.Bus
	runtime              runtime.Runtime
	processRuntime       bool // InMemory: New builds a process runtime over the final logger, a later WithLogger's included
	gateway              gateway.Gateway
	logger               *slog.Logger
	normalizeLogFields   bool
	telemetry            *observability.Telemetry

	// egress network isolation (ADR-0115, FEAT-0007/F80). nil ⇒ not configured. Apply at Run start
	// (before workers serve), Remove at Shutdown. A no-op when disabled or non-Linux.
	netManager network.Manager
	netPolicy  network.Policy

	// egress gateway + DNS forwarder (ADR-0117, FEAT-0007/F81): the transparent egress PEP + the domain
	// trust anchor, wired when enabled (Linux/containerd only; a no-op elsewhere). Zero ⇒ not configured
	// (no gateway/forwarder started). Pairs with WithEgressIsolation (same server.network.egress flag).
	egressGatewayEnabled bool
	egressGatewayPort    uint16
	dnsForwarderPort     uint16
	dnsUpstream          netip.AddrPort

	// control plane (ADR-0028)
	listenAddr  string
	credentials middleware.CredentialStore
	authorizer  auth.Authorizer
	localNode   v1.ObjectName
	// nodePlatform is the platform this node runs (ADR-0145); "" ⇒ v1.HostPlatform().
	nodePlatform v1.OCIPlatform

	// data plane (ADR-0033): the function-invocation listener address.
	dataPlaneAddr string

	// TLS termination (ADR-0111, F74): when set, both listeners serve HTTPS via ServeTLS. nil ⇒
	// plaintext (the back-compat default). The zero Mode defaults to selfsigned (stdlib, offline).
	tlsSpec *edgetls.Spec

	// ingress protection (ADR-0112, F75): rate/size/concurrency limits on the data-plane chain. The
	// zero value is a pass-through (limits off by default).
	limits limit.Config

	// edge authn PEP (ADR-0113, F77): when true, the data-plane enforces the per-target auth stance
	// (reusing the control-plane credentials + authorizer). false ⇒ no PEP (open-only; an
	// authenticated stance then fails closed).
	edgeAuthEnabled bool

	// edge observability (ADR-0114, F76) + shaping (F78): RED metrics/trace/access-log; CORS/headers/
	// compression. Zero values are pass-throughs (off by default).
	observ  observ.Config
	shaping shape.Config

	// function execution (ADR-0030): the runtime shim launch prefix + the artifact
	// Materializer. When runtimeShim is set the reconciler runs functions via the shim
	// (defaulting to the local-file Materializer if none is supplied); when empty the
	// reconciler runs the legacy placeholder (no real execution).
	runtimeShim         []string
	runtimeShimByFamily map[string][]string // runtime-family prefix → shim cmd (ADR-0049)
	materializer        function.Materializer
	artifactDir         string // OCI artifact cache dir (ADR-0031); enables the oras Materializer
	invokeSocketDir     string // dir for per-function worker-node local API UDS (ADR-0064); empty → a temp dir

	// container execution (ADR-0032): when imageFor is set the reconciler runs functions
	// in the curated-image containerd worker (shim = image entrypoint, fixed netns port,
	// artifact bind-mounted) instead of the process-driver shim.
	imageFor func(runtime string) string

	// worker pooling (ADR-0046): the launch prefix for the pooled worker_threads host
	// (e.g. ["node", "/.../pool.mjs"]). When set, a function declaring spec.pooling.worker
	// co-locates with same-key peers in one pool worker. Empty ⇒ pooling off (all solo).
	poolShim          []string
	poolShimsByFamily map[string][]string // runtime-family prefix → pool-host cmd (ADR-0050)
	poolManifestDir   string              // the pool manifests' dir (WithPoolManifestDir); "" ⇒ a private temp dir
	poolLimit         int
	// bootBackoffInitial and bootBackoffMax bound a crash-looping worker's wait (ADR-0160); 0 ⇒ the defaults.
	bootBackoffInitial time.Duration
	bootBackoffMax     time.Duration
	// pacing holds the retry, requeue, supervision, wake, drain and probe times (ADR-0163); a zero field is the default.
	pacing Pacing

	// S3 gateway (ADR-0080/0085): opt-in S3-protocol frontend over c.blob. Disabled ⇒ no
	// listener, no IAM, no keypair injection. master is loaded/generated by buildControlPlane.
	s3gwEnabled        bool
	s3gwListenAddr     string
	s3gwEndpoint       string // sandbox-facing S3 URL (ADR-0085); empty ⇒ http://<listenAddr>
	s3gwMaxUploadBytes int64
	s3gwMasterFile     string // optional; empty ⇒ generate+persist under the data dir
	s3gwDataDir        string // where the master.key is persisted when no master file is set

	// catalogProxyHost is the netns-reachable host the per-CatalogService catalog PEP proxies publish
	// (ADR-0137) — the CNI bridge gateway IP (e.g. 10.63.0.1) under containerd, so a worker in its own
	// netns can reach the proxy. Empty ⇒ 127.0.0.1 (the process-runtime/dev default; loopback is shared).
	catalogProxyHost string

	// Workflow engine (ADR-0094): always wired. Durable run state is a Badger store at
	// workflowDataDir; empty ⇒ in-memory (the InMemory preset / tests). The tunables are the
	// workflow.* config keys — defaultStepTimeout + defaultRetry feed the engine core; retention drives
	// the terminal-run sweep (runWorkflowRetention) and payloadLimit bounds run inputs and step outputs
	// (the engine and the WorkflowRun admission).
	workflowDataDir      string
	workflowStepTimeout  time.Duration
	workflowRetention    time.Duration
	workflowDefaultRetry int
	workflowPayloadLimit int64
	// workflowMaxStepsInFlight bounds the function-step calls in flight across all runs (ADR-0146); 0 ⇒ no cap.
	workflowMaxStepsInFlight int
	// workflowContracts overrides the F65 typed-edge ContractResolver (ADR-0098). Empty ⇒ the production
	// OCI-metadata resolver. Set by `funcdctl dev` (ADR-0125) where a from-source step has no OCI artifact
	// to inspect, so the OCI resolver can never resolve a file:// bundle's contract.
	workflowContracts workflow.ContractResolver

	// catalogProvider overrides the CatalogService add-on-provider runtime (ADR-0087). Empty ⇒ the
	// production runtime that supervises the curated duckdb CONTAINER (internal/provider). Set by
	// `funcdctl dev` (ADR-0125) to a process-mode driver that runs the embedded DuckDB+Quack engine
	// as a host subprocess — there are no containers in process-dev.
	catalogProvider provider.Runtime

	// catalogExtensionDir is injected as DUCKDB_EXTENSION_DIRECTORY into catalog-consumer functions
	// (dev analogue of the prod bundle's duckdb-ext, ADR-0089). Set by `funcdctl dev`; empty in prod.
	catalogExtensionDir string

	// logObserver, when set, streams every captured function log line live (in addition to the normal
	// blob-persisted capture) — `funcdctl dev` uses it to print logs to the terminal in real time. nil ⇒
	// logs are only persisted, as in production.
	logObserver LogObserver

	// Eventing DLQ (ADR-0118): always wired. The dead-letter queue is a dedicated Badger store at
	// deadletterDataDir (empty ⇒ in-memory, mirroring the run store). deliveryAttempts caps the bounded
	// action-delivery retry; deadletterRetention (TTL) + deadletterMaxEntries (per-ns cap) drive the sweep.
	deadletterDataDir    string
	deliveryAttempts     int
	deadletterRetention  time.Duration
	deadletterMaxEntries int
	// The Sensor delivery queue sizes (ADR-0156, WithSensorDelivery); 0 ⇒ the sensor defaults (32, 4, 4096).
	sensorMaxDeliveriesInFlight int
	sensorMaxInFlightPerTarget  int
	sensorMaxQueuedPerSensor    int

	// Blob EventSource poll watcher (ADR-0119, F83): the platform-wide cadence a `blob:` source's prefixes
	// are List-polled for new objects. 0 ⇒ the 15s default.
	blobPollInterval time.Duration
	gcSweepInterval  time.Duration // ADR-0170: the owner garbage collector's sweep period (0 ⇒ gc.DefaultInterval)

	// Site reconciler (ADR-0139, F103): the index document served for "/" when a Site's spec.index is
	// empty. "" ⇒ "index.html".
	siteDefaultIndex string
	// appRevisionHistory is app.revisionHistory (ADR-0200, WithAppRevisionHistory); 0 ⇒ the App reconciler's 10. The
	// upgrade timeout is pacing.appUpgradeTimeout().
	appRevisionHistory int

	// invokeDefaultTimeout is invoke.defaultTimeout (ADR-0151): an external invoke's response deadline when its
	// Function sets no spec.timeout.
	invokeDefaultTimeout time.Duration
}

// minRecordBytes is the smallest funclog.maxRecordBytes: a record's envelope and cut marker always fit (ADR-0168).
const minRecordBytes = 1024

// validate returns the first missing required dependency, or an invalid option combination, as a
// fault.Invalid.
func (c *config) validate() error {
	const op = "funcd.New"
	switch {
	case c.store == nil:
		return fault.Invalidf(op, "store is required")
	case c.blob == nil:
		return fault.Invalidf(op, "blob is required")
	case c.bus == nil:
		return fault.Invalidf(op, "bus is required")
	case c.runtime == nil && !c.processRuntime:
		return fault.Invalidf(op, "runtime is required")
	case c.gateway == nil:
		return fault.Invalidf(op, "gateway is required")
	case c.credentials == nil:
		return fault.Invalidf(op, "control-plane credentials are required (use WithCredentials, WithDevAuth or a preset)")
	}
	if n := c.funclogMaxRecordBytes; n != 0 && (n < minRecordBytes || n > funclog.MaxLineBytes) {
		return fault.Invalidf(op, "funclog.maxRecordBytes %d is outside [%d, %d]: the reader drops a line over %d bytes",
			n, minRecordBytes, funclog.MaxLineBytes, funclog.MaxLineBytes)
	}
	// Container execution runs every Function solo: no pool worker can run in a curated image (ADR-0173).
	if c.imageFor != nil && (len(c.poolShim) > 0 || len(c.poolShimsByFamily) > 0) {
		return fault.Invalidf(op, "worker pooling is not supported with container execution: WithPoolShim and WithPoolShimFor cannot be combined with WithContainerExecution")
	}
	return nil
}

// Platform is the assembled funcd runtime — the composition root built by New.
type Platform struct {
	cfg          *config
	fnReconciler *function.Reconciler // the Function reconciler; Shutdown removes its private pool manifest dir
	logger       *slog.Logger
	providers    *provider.Catalog // the platform provider catalog (ADR-0082)

	controller  *controller.Controller
	collector   *gc.Collector // ADR-0170: the owner garbage collector, run beside the controller
	eventing    *eventing.Source
	eventFanout *eventing.Fanout      // ADR-0108: the named-event publisher the F69 Sensor subscribes to
	blobWatcher *eventing.BlobWatcher // ADR-0119: the blob EventSource poll watcher (side Run loop)
	edgeRouter  router.Router         // ADR-0110 (F79): the Route matcher the data-plane front door consults
	tlsProvider edgetls.Provider      // ADR-0111 (F74): the TLS cert provider (nil ⇒ plaintext)
	activator   *activator.Activator
	httpServer  *http.Server
	listener    net.Listener
	addr        string

	routeReconciler *route.Reconciler // Sets the full Route table once before the controller runs (ADR-0176)
	kvReconciler    *kvsvc.Reconciler // reclaims deleted stores' data once before the controller runs (issue #708)

	dataPlaneServer   *http.Server // function-invocation listener (ADR-0033)
	dataPlaneListener net.Listener
	dataPlaneAddr     string

	invokeMgr         *local.Manager          // per-function worker-node local API broker (ADR-0064)
	invokeTmpDir      string                  // the temp socket dir New created (no WithInvokeSocketDir); removed by Shutdown
	workflowRuns      runstate.Store          // durable workflow run state (ADR-0094); closed on shutdown
	workflowSweeper   *workflow.RunReconciler // the run reconciler (ADR-0094); drives the retention sweep
	workflowEngine    *workflow.Engine        // owns the run goroutines; drained on shutdown (ADR-0146)
	workflowRetention time.Duration           // terminal-run retention horizon (0 ⇒ no sweep)

	eventStore           *eventstore.Store         // eventing's durable store (ADR-0201): dead letters + seen lists; closed on shutdown
	deadLetters          deadletter.Store          // the event store's dead-letter tenant (ADR-0118)
	sensorReconciler     *sensor.Reconciler        // owns the retry workers (drained on shutdown) + the DLQ replay seam
	deadletterRetention  time.Duration             // DLQ TTL horizon (0 ⇒ no TTL eviction)
	deadletterMaxEntries int                       // DLQ per-namespace count cap (0 ⇒ unbounded)
	logSink              *funclog.BlobSink         // structured function-log capture sink (ADR-0081); nil if unwired
	traceSink            *funclog.BlobTraceSink    // per-invocation trace sink (ADR-0101); nil if unwired/disabled
	logRoutes            logRoutes                 // the live capture Routes feeding logSink/traceSink, drained by Shutdown
	compactor            *compact.Compactor        // funclog compacted compaction pipeline (ADR-0083); nil if unwired
	s3gw                 *s3gateway.Server         // S3-protocol frontend (ADR-0080/0085); nil unless s3gwEnabled
	catalogProxy         *cataloggw.Manager        // per-CatalogService node-private catalog PEP proxies (ADR-0137); Shutdown-closed
	egressGateway        egress.Gateway            // transparent egress PEP (ADR-0117, F81); nil unless egress enabled
	egressForwarder      egress.Forwarder          // DNS forwarder / domain trust anchor (ADR-0117); nil unless enabled
	egressWorkers        *egress.MemoryWorkerIndex // src-IP → Ref, populated by the containerd runtime (ADR-0117 §5)

	drainTimeout time.Duration // server.shutdownTimeout: bounds the HTTP, workflow-run and Sensor drains and a failed New's cleanup
	shutdownOnce sync.Once
	shutdownErr  error
}

// New assembles the platform from opts, validates every required dependency, builds
// the control plane (controller + reconcilers + API server), and binds the
// control-plane listener (so Addr() is ready before Run). It returns a typed
// fault (and a nil *Platform) on a missing dep or a build/bind failure — never a
// partial platform, never a panic.
func New(opts ...Option) (_ *Platform, err error) {
	cfg := &config{
		workflowStepTimeout:  defaultWorkflowStepTimeout,
		workflowRetention:    defaultWorkflowRetention,
		workflowDefaultRetry: defaultWorkflowRetry,
		workflowPayloadLimit: defaultWorkflowPayloadLimit,
		deadletterRetention:  defaultDeadletterRetention,
		deadletterMaxEntries: defaultDeadletterMaxEntries,
		invokeDefaultTimeout: v1.DefaultInvokeTimeout,

		workflowMaxStepsInFlight: defaultWorkflowMaxStepsInFlight,
	}
	p := &Platform{cfg: cfg, drainTimeout: shutdownTimeout}
	// A failed New releases what the options and the build acquired, so the caller can retry (issue #94).
	defer func() {
		if err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), p.drainTimeout)
			defer cancel()
			_ = p.Shutdown(ctx)
		}
	}()
	for _, o := range opts {
		if err := o(cfg); err != nil {
			return nil, err
		}
	}
	p.drainTimeout = orDefault(cfg.pacing.ShutdownTimeout, shutdownTimeout)
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	if cfg.logger != nil && cfg.normalizeLogFields {
		cfg.logger = slog.New(observability.NewNormalizeHandler(cfg.logger.Handler()))
	}
	if cfg.logger == nil {
		lg, err := observability.NewLogger(observability.Config{Format: observability.FormatText}, os.Stdout)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, "funcd.New", "build default logger")
		}
		cfg.logger = lg.Root()
	}
	if cfg.runtime == nil && cfg.processRuntime {
		cfg.runtime = process.New(cfg.logger)
	}

	pc, err := providerCatalog()
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "funcd.New", "build provider catalog")
	}

	p.logger, p.providers = cfg.logger, pc
	if err := p.buildControlPlane(); err != nil {
		return nil, err
	}
	return p, nil
}

// buildControlPlane constructs the controller + Function/Service/EventSource
// reconcilers + the authenticated control-plane server, and binds the listener.
// Every component constructor is error-returning; the first failure aborts New.
func (p *Platform) buildControlPlane() error {
	const op = "funcd.New"
	c := p.cfg
	if c.authorizer == nil {
		c.authorizer = rbac.New()
	}
	if c.localNode == "" {
		c.localNode = defaultLocalNode
	}
	if c.nodePlatform == "" {
		c.nodePlatform = v1.HostPlatform()
	}
	if c.listenAddr == "" {
		c.listenAddr = defaultProdAddr
	}
	if c.dataPlaneAddr == "" {
		c.dataPlaneAddr = defaultDataPlaneAddr
	}
	// KVStore per-namespace count quota (ADR-0072): 0 ⇒ the default (100); negative disables it.
	kvMaxStores := c.kvMaxStoresPerNamespace
	if kvMaxStores == 0 {
		kvMaxStores = defaultKVStoresPerNamespace
	}
	// Bucket per-namespace count quota (ADR-0080): 0 ⇒ the default (100); negative disables it.
	bucketMax := c.bucketMaxPerNamespace
	if bucketMax == 0 {
		bucketMax = defaultBucketsPerNamespace
	}

	sched, err := singlenode.New(c.localNode, c.nodePlatform)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build scheduler")
	}
	// Execution mode (ADR-0030/0032): container mode (imageFor set) runs the curated-image
	// containerd worker; process mode (runtimeShim set) runs the shim on the process driver.
	endpointMode := function.EndpointLoopback
	if c.imageFor != nil {
		endpointMode = function.EndpointNetnsFixedPort
	}
	materializer := c.materializer
	if (len(c.runtimeShim) > 0 || c.imageFor != nil) && materializer == nil {
		if c.artifactDir != "" {
			materializer = artifact.NewOrasMaterializer(c.artifactDir, c.nodePlatform) // P-V-A: OCI pull by digest
		} else {
			materializer = function.NewFileMaterializer() // P-V-1: no-dep file:// stand-in
		}
	}
	// The oras materializer also resolves a tag → digest (ADR-0035); the file driver does
	// not, so a type-assert auto-wires the resolver only in OCI mode (nil for file://).
	resolver, _ := materializer.(function.ArtifactResolver)
	// ...and lists an artifact's platforms for the placement gate (ADR-0145).
	platforms, _ := materializer.(function.PlatformResolver)
	// Secret injection (ADR-0057, F15 last mile): the reconciler resolves a function's bound
	// Secrets into worker env via the PDP-authorized resolver (ADR-0022) over the same store +
	// authorizer the control plane uses. DeveloperFor defaults to the namespace-scoped developer.
	secretResolver, err := secrets.NewResolver(secrets.Deps{Store: c.store, Authorizer: c.authorizer, Logger: p.logger})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build secrets resolver")
	}
	// Worker-node local API (ADR-0064): the per-function fn-to-fn invoke broker. Its Invoker
	// forwards through the data-plane handler built below, so wire that handler via a holder set
	// after it exists (the reconciler is constructed before the data plane, which wraps its activator).
	dpHolder := &local.HandlerHolder{}
	invokeSockDir := c.invokeSocketDir // config-derived (<dataDir>/invoke); empty ⇒ a temp dir (InMemory/tests)
	if invokeSockDir == "" {
		tmp, terr := os.MkdirTemp("", "funcd-invoke")
		if terr != nil {
			return fault.Wrapf(terr, fault.Internal, op, "create invoke socket dir")
		}
		invokeSockDir, p.invokeTmpDir = tmp, tmp
	}
	if err := local.CheckDir(invokeSockDir); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "invoke socket dir")
	}
	// KV service (ADR-0069/0072/0073): the durable driver (config-selected, ADR-0066) behind the
	// binding-gated Facade, reached by functions through the worker-node local API's /kv routes. Defaults
	// to in-memory. The BindingResolver resolves a caller's (function, alias) to its (store, table) via
	// the caller's Function.spec.kv over the metastore (default-deny); reads are coarse-allowed for any
	// bound caller, writes are owner-only (ADR-0073).
	if c.kvStore == nil {
		c.kvStore = kvmemory.New()
	}
	kvResolver, err := kvsvc.NewResolver(metaReader{c.store})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build KV binding resolver")
	}
	// Cedar PDP driver (ADR-0074): the per-object authorization engine the KV facade (PEP) calls for
	// kv::read/kv::write. Entities are materialized per call from the metastore (principal Function +
	// resource KVTable + parent KVStore); policies are the v1.Policy resources, compiled + cached
	// (recompiled on a store-revision change). DEFAULT-DENY — a read needs a permitting Policy; the
	// owner-write forbid is built in. rbac still decides control-plane CRUD (c.authorizer, unchanged).
	// The capability registry (ADR-0116): the three migrated capabilities (kv, invoke, s3) + the two
	// principal sources (Function, CatalogService; each resolves its own type) registered here at the composition
	// root. The schema vocabulary, the built-in PolicySet, and this composite EntityProvider are all
	// assembled from the registered set — a new capability (egress next) registers with no shared edit.
	// ADR-0136: a store-backed WriterLister feeds writer-role RolesAssignment grants into the s3/kv
	// single-writer `writers` set (so an external Identity can be granted write).
	writerLister := rolessvc.NewLister(c.store)
	s3Cap := cedarauth.S3CapabilityWithWriters(writerLister)
	cedarRegistry, err := cedarauth.NewRegistry(
		// ADR-0137 (F102): CatalogCapability makes the live PDP understand catalog::query; the per-CatalogService
		// PEP proxy that calls it is not yet wired here — see TODO(ADR-0137) in internal/services/catalog/reconcile.go.
		[]cedarauth.Capability{cedarauth.KVCapabilityWithWriters(writerLister), cedarauth.InvokeCapability(), s3Cap, cedarauth.EgressCapability(), cedarauth.CatalogCapability()},
		[]cedarauth.PrincipalSource{cedarauth.FunctionPrincipalSource(), cedarauth.CatalogServicePrincipalSource()},
	)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build cedar capability registry")
	}
	cedarEntities, err := cedarRegistry.EntityProvider(cedarMetaReader{c.store})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build cedar entity provider")
	}
	cedarPDP, err := cedarauth.New(cedarauth.Deps{
		Entities: cedarEntities,
		Policies: policySource{c.store},
		Builtins: cedarRegistry.Builtins(), // the ASSEMBLED built-ins (incl. any dev variant), not the package default
		Logger:   p.logger,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build cedar PDP driver")
	}
	kvFacade, err := kvsvc.NewFacade(kvsvc.FacadeDeps{KV: c.kvStore, Resolver: kvResolver, Authorizer: cedarPDP, Logger: p.logger})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build KV facade")
	}
	// The invoke Manager (ADR-0064) now also carries the cedar PDP (ADR-0075): the per-sandbox local
	// API asks link::invoke on the resolved target so a forbid Policy can revoke a declared link.
	// The function-facing blob facade (ADR-0127): context.blob's PEP. Built only when a blob substrate
	// is present (else the /blob routes stay off, exactly like a nil kv). It reuses the SAME cedar PDP
	// (the S3Capability already materializes a Function's spec.blob as its blobBindings), the SAME
	// s3BucketFor substrate view, and the SAME blobKey keyspace as the ADR-0080 S3 frontend — so objects
	// written via context.blob are the objects the S3 frontend serves. A nil interface (not a nil
	// *Facade) keeps the routes off; a typed nil pointer would slip past NewHandler's nil check.
	var blobPort local.Blob
	if c.blob != nil {
		blobResolver, rerr := blobsvc.NewResolver(metaReader{c.store})
		if rerr != nil {
			return fault.Wrapf(rerr, fault.KindOf(rerr), op, "build blob binding resolver")
		}
		blobFacade, berr := blobsvc.NewFacade(blobsvc.FacadeDeps{
			Resolver:   blobResolver,
			BucketFor:  s3BucketFor(c.blob, c.store),
			Authorizer: cedarPDP,
			Logger:     p.logger,
		})
		if berr != nil {
			return fault.Wrapf(berr, fault.KindOf(berr), op, "build blob facade")
		}
		blobPort = blobFacade
	}
	nestedCap := c.nestedInFlightCap
	if nestedCap == 0 {
		nestedCap = defaultNestedInFlightCap
	}
	invoker := local.NewNestedCapInvoker(local.NewInvoker(dpHolder), nestedCap, invokeMeter(c.telemetry))
	p.invokeMgr = local.NewManager(invokeSockDir, c.store, invoker, cedarPDP, kvFacade, blobPort, p.logger)

	// Egress gateway + DNS forwarder (ADR-0117, F81): the sole egress PEP + the domain trust anchor.
	// Wired only when enabled (Linux/containerd only; egress.New is a no-op elsewhere, mirroring F80).
	// The gateway authorizes every outbound worker connection via the cedar PDP (egress::connect over
	// the forwarder-attested NetDestination); the forwarder records (worker,domain)→IP so a domain rule
	// is trustworthy. The WorkerIndex is populated by the containerd runtime at worker provisioning (§5).
	if c.egressGatewayEnabled {
		p.egressWorkers = egress.NewMemoryWorkerIndex()
		fwd := egress.NewForwarder(
			netip.AddrPortFrom(netip.AddrFrom4([4]byte{0, 0, 0, 0}), c.dnsForwarderPort),
			c.dnsUpstream,
			p.egressWorkers,                // src-IP → namespace (ADR-0117 §5)
			storeWildcardPatterns{c.store}, // namespace → EgressPolicy wildcards (judge Major 2 dep)
		)
		p.egressForwarder = fwd
		p.egressGateway = egress.New(true, egress.Deps{
			GatewayPort: c.egressGatewayPort,
			Workers:     p.egressWorkers,
			DNS:         fwd,
			Authz:       cedarPDP,
			Audit:       egressAuditSink{logger: p.logger.With("component", "egress.audit")},
			Logger:      p.logger.With("component", "egress.gateway"),
		})
	}

	// S3 gateway (ADR-0080/0085): opt-in S3-protocol frontend over the blob substrate, reusing the
	// cedar PDP as the PEP. When enabled, load/generate the node master secret, build the server, and
	// expose the per-function keypair deriver to the reconciler for worker-env injection. Disabled ⇒
	// nothing is built (no listener, no IAM, no injection) — zero-config unchanged.
	// The node master secret (ADR-0085): the SAME 32-byte key the S3 gateway derives per-function
	// SigV4 keypairs from AND the catalog PEP proxy derives/verifies per-function catalog tokens with
	// (ADR-0137). Loaded ONCE here — before both the s3gw and the catalog wiring — so they share one
	// master (LoadOrCreateMaster is deterministic per file/dir, but loading twice risks generating two
	// different keys on a first run). Never logged.
	master, merr := s3gateway.LoadOrCreateMaster(c.s3gwMasterFile, c.s3gwDataDir)
	if merr != nil {
		return fault.Wrapf(merr, fault.KindOf(merr), op, "load node master secret")
	}

	var s3Injection function.S3GatewayInjection
	if c.s3gwEnabled {
		bucketFor := s3BucketFor(c.blob, c.store)
		srv, gerr := s3gateway.New(s3gateway.Deps{
			BucketFor:      bucketFor,
			Buckets:        s3Buckets(c.store),
			PDP:            cedarPDP,
			Master:         master,
			Listen:         c.s3gwListenAddr,
			MaxUploadBytes: c.s3gwMaxUploadBytes,
			// ADR-0135: resolve external Identity-issued keys to their stored secret (populates the
			// previously-nil ExternalKeys seam), so a managed Identity authenticates over SigV4.
			External: identitysvc.NewExternalKeys(c.store),
			Logger:   p.logger,
		})
		if gerr != nil {
			return fault.Wrapf(gerr, fault.KindOf(gerr), op, "build s3gateway")
		}
		p.s3gw = srv
		s3Injection = function.S3GatewayInjection{
			Enabled:    true,
			ListenAddr: c.s3gwListenAddr,
			Endpoint:   c.s3gwEndpoint,
			Derive: func(kind v1.Kind, ns, name string) (string, string) {
				kp := s3gateway.DeriveKeypair(master, kind, ns, name)
				return kp.AccessKey, kp.SecretKey
			},
		}
	}

	// Catalog PEP proxy manager (ADR-0137): the node-private per-CatalogService proxies that bring
	// internal function→catalog queries under the same per-caller, per-query Cedar PEP as blob/kv/S3.
	// It resolves a presented catalog token (a per-function MAC bearer or a minted per-Identity token)
	// to its principal via catalogKeys, PEPs catalog::query on the endpoint's CatalogService, and swaps
	// the caller token for the shared engine token only after an allow. The catalog reconciler binds a
	// listener per catalog on its recorded port and Ensures it fronts the Ready engine (publishing its url as
	// Status.Endpoint); a deleted catalog's listener closes once no Function binds it (ADR-0162).
	// bindHost/publishHost: a worker under containerd is in its OWN netns and cannot reach the daemon's
	// 127.0.0.1, so when c.catalogProxyHost is set (the CNI bridge gateway IP) the proxy binds 0.0.0.0
	// (netns-reachable) and publishes that host; empty ⇒ 127.0.0.1 both (the process-runtime/dev default).
	// This mirrors the s3gateway ListenAddr/Endpoint split (ADR-0085/0137).
	catBindHost, catPublishHost := "127.0.0.1", "127.0.0.1"
	if c.catalogProxyHost != "" {
		catBindHost, catPublishHost = "0.0.0.0", c.catalogProxyHost
	}
	catalogKeys := cataloggw.NewCatalogKeys(master, c.store)
	catalogMgr := cataloggw.NewManager(catBindHost, catPublishHost, catalogKeys, cedarPDP, p.logger)
	p.catalogProxy = catalogMgr

	// ADR-0143: one tracker counts every call to a worker — the activator's proxy, the Sensor invoker and the workflow
	// dispatcher send through it — so the reconciler can tell when a demoted revision's worker is drained.
	calls := activator.NewCallTracker(clock.System())
	fnReconciler, err := function.NewReconciler(function.Deps{
		Store:                c.store,
		LogMaxRecordBytes:    cmp.Or(c.funclogMaxRecordBytes, funclog.DefaultMaxRecordBytes),
		Calls:                calls,
		InvokeSockets:        p.invokeMgr,
		Runtime:              c.runtime,
		Scheduler:            sched,
		Gateway:              c.gateway,
		Validator:            function.NewBasicValidator(),
		Logger:               p.logger,
		Materializer:         materializer,
		ShimCommand:          c.runtimeShim,
		ShimCommandsByFamily: c.runtimeShimByFamily,
		EndpointMode:         endpointMode,
		ImageFor:             c.imageFor,
		Resolver:             resolver,
		Platforms:            platforms,
		BootBackoffInitial:   c.bootBackoffInitial,
		BootBackoffMax:       c.bootBackoffMax,
		SupervisionPeriod:    c.pacing.SupervisionPeriod,
		BootTimeout:          c.pacing.BootTimeout,
		DrainGrace:           c.pacing.DrainGrace,
		HandOutSettle:        c.pacing.HandOutSettle,
		DrainPollInterval:    c.pacing.DrainPollInterval,
		ReferentPollInterval: c.pacing.ReferentPollInterval,
		PoolShimCommand:      c.poolShim,
		PoolShimsByFamily:    c.poolShimsByFamily,
		PoolManifestDir:      c.poolManifestDir,
		PoolLimit:            c.poolLimit,
		S3Gateway:            s3Injection,
		CatalogMaster:        master, // ADR-0137: per-function catalog token derivation (same master as S3)
		CatalogExtensionDir:  c.catalogExtensionDir,
		CatalogProxies:       catalogMgr,
		Secrets:              secretResolver,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build function reconciler")
	}
	dispatcher, err := services.NewDispatcher(c.store, p.logger, kvsvc.NewHandler(), blobsvc.NewHandler())
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build service dispatcher")
	}
	// Activator (ADR-0016) built BEFORE eventing — it is eventing's Waker (ADR-0033).
	act, err := activator.New(activator.Deps{
		Store:     c.store,
		Endpoints: fnReconciler.Endpoints(),
		Scaler:    storescaler.New(c.store),
		Logger:    p.logger,
		Calls:     calls,

		ActivationTimeout: c.pacing.ActivationTimeout,
		ReclaimInterval:   c.pacing.ReclaimInterval,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build activator")
	}
	p.activator = act
	// ADR-0108: an EventSource firing PUBLISHES a named CloudEvent onto the in-process Fanout; the F69
	// Sensor subscribes to it (the action side — invoke/start-workflow — moved off the Source).
	fanout := eventing.NewFanout()
	p.eventFanout = fanout
	// ADR-0201: the event store holds the Sensor's dead letters and the BlobWatcher's seen lists, on disk when
	// deadletterDataDir is set, else in memory. On disk, the seen lists an earlier release kept in the KV move in
	// before the BlobWatcher is built; a failed move fails the start and keeps the KV records for the next one.
	es, err := eventstore.Open(eventstore.Config{InMemory: c.deadletterDataDir == "", Dir: c.deadletterDataDir})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "open event store")
	}
	p.eventStore = es
	if c.deadletterDataDir != "" {
		ctx := context.Background()
		moved, merr := eventstore.MigrateSeenLists(ctx, c.kvStore, es)
		if merr != nil {
			return fault.Wrapf(merr, fault.KindOf(merr), op, "move the blob seen lists out of the KV")
		}
		if moved > 0 {
			p.logger.InfoContext(ctx, "moved blob seen lists from the KV into the event store", "count", moved)
		}
	}
	// ADR-0119 (F83): the blob EventSource poll watcher. It lists the SAME s3BucketFor substrate view
	// external S3-frontend writes land in (writer-agnostic detection over blob.Bucket.List), keeps its per-event
	// seen lists in the event store, and publishes a named CloudEvent per new object onto the same Fanout the
	// Sensor subscribes to. Registered by the EventSource reconciler; Run in Run().
	blobWatcher, err := eventing.NewBlobWatcher(blobBucketLister{resolve: s3BucketFor(c.blob, c.store)}, fanout, es.SeenLists(), c.blobPollInterval, p.logger)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build blob watcher")
	}
	p.blobWatcher = blobWatcher
	source, err := eventing.NewSource(eventing.Deps{
		Store:     c.store,
		Publisher: fanout,
		Blob:      blobWatcher,
		Logger:    p.logger,

		BucketRecheckInterval: c.pacing.BucketRecheckInterval,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build eventing source")
	}
	p.eventing = source

	ctrl, err := controller.New(controller.Deps{Store: c.store, Logger: p.logger, RetryBackoffMax: c.pacing.RetryBackoffMax})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build controller")
	}
	if p.collector, err = gc.New(gc.Deps{Store: c.store, Purger: bucketPurger{shared: c.blob}, Interval: c.gcSweepInterval, Logger: p.logger}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build garbage collector")
	}
	p.edgeRouter = router.New() // ADR-0110 (F79): shared by the Route reconciler + the data-plane handler
	// Edge-route aggregator (ADR-0138): the SINGLE sole-writer of p.edgeRouter's replace-all table. The
	// Route reconciler (user Routes) and the CatalogService reconciler (its node-private catalog::query
	// PEP proxy entry) each Set only their own source partition; the aggregator unions all sources into
	// one Program call — so an exposed catalog coexists with user Routes instead of clobbering them. The
	// data-plane still reads p.edgeRouter directly (Resolve); only writes go through the aggregator.
	// ADR-0176: it arbitrates every source's claims against the namespace exposure modes and re-runs the
	// reconciler of each owner whose verdict another source's Set changed.
	edgeAgg := router.NewAggregator(p.edgeRouter,
		func(ctx context.Context, ns v1.NamespaceName) (v1.ExposureMode, error) {
			obj, err := c.store.Get(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
			if err != nil {
				return "", err
			}
			n, ok := obj.(*v1.Namespace)
			if !ok {
				return "", fault.Internalf(op, "namespace %q is a %T", ns, obj)
			}
			return n.Spec.DefaultExposure.Normalized(), nil
		},
		func(o router.Owner) {
			ctrl.Enqueue(controller.Request{GVK: o.Kind.GVK(), Namespace: o.Namespace, Name: o.Name})
		},
		p.logger)
	ctrl.Register(v1.KindFunction.GVK(), fnReconciler)
	p.fnReconciler = fnReconciler
	// a held revision's wake or reclaim is written to its Revision's status (ADR-0190 Decision 5)
	ctrl.Watches(v1.KindRevision.GVK(), fnReconciler.MapRevision)
	// a change to what a namespace grants its Functions can move a pooled one to another pool
	for _, k := range []v1.Kind{v1.KindKVStore, v1.KindBucket, v1.KindRolesAssignment, v1.KindEgressPolicy, v1.KindPolicy} {
		ctrl.Watches(k.GVK(), fnReconciler.MapAccess)
	}
	ctrl.Watches(v1.KindFunction.GVK(), fnReconciler.MapPoolDisplaced) // an asleep member a newcomer displaces (ADR-0193)
	ctrl.Register(v1.KindService.GVK(), dispatcher)
	ctrl.Register(v1.KindEventSource.GVK(), source)
	// ADR-0118 (F85): the eventing DLQ — the event store's dead-letter tenant, independent of the bus driver. It
	// backs the Sensor's bounded action-delivery retry + dead-lettering and the control-plane read/replay surface.
	dlq := es.DeadLetters()
	p.deadLetters = dlq
	p.deadletterRetention = c.deadletterRetention
	p.deadletterMaxEntries = c.deadletterMaxEntries
	// ADR-0109 (F69) + ADR-0118 (F85): the Sensor binds the named events published on the Fanout to actions
	// — start a WorkflowRun / invoke a Function (via the re-created invoke/wake logic), with bounded retry
	// before dead-lettering. It subscribes to fanout.
	sensorReconciler, err := sensor.NewReconciler(sensor.Deps{
		Store:                 c.store,
		Subscriber:            fanout,
		Invoker:               &sensor.HTTPInvoker{Endpoints: fnReconciler.Endpoints(), Waker: act, Client: workerClient(calls, 30*time.Second)},
		DeadLetters:           dlq,
		DeliveryAttempts:      c.deliveryAttempts,
		MaxDeliveriesInFlight: c.sensorMaxDeliveriesInFlight,
		MaxInFlightPerTarget:  c.sensorMaxInFlightPerTarget,
		MaxQueuedPerSensor:    c.sensorMaxQueuedPerSensor,
		Logger:                p.logger,

		DeliveryBackoffInitial: c.pacing.DeliveryBackoffInitial,
		DeliveryBackoffMax:     c.pacing.deliveryBackoffMax(),
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build sensor reconciler")
	}
	p.sensorReconciler = sensorReconciler
	ctrl.Register(v1.KindSensor.GVK(), sensorReconciler)
	// ADR-0110 (F79): the Route reconciler validates backends + multi-tenancy rules and programs the
	// edge router (replace-all) the data-plane front door consults. p.edgeRouter is created up front so
	// both the reconciler and the data-plane handler share the one live table.
	routeReconciler, err := route.NewReconciler(route.Deps{
		Store: c.store, Routes: edgeAgg, Logger: p.logger,
		ReferentPollInterval: c.pacing.ReferentPollInterval, ResyncInterval: c.pacing.RouteResyncInterval,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build route reconciler")
	}
	ctrl.Register(v1.KindRoute.GVK(), routeReconciler)
	p.routeReconciler = routeReconciler
	// ADR-0139 (F103): the Site reconciler materializes a site bundle under a digest-scoped prefix of the
	// SAME per-namespace Bucket view the S3 frontend and the static handler use (s3BucketFor), then owns
	// the Bucket + Route it declares inline; its status is derived from the owned Route.
	siteReconciler := site.New(site.Deps{Store: c.store, Buckets: s3BucketFor(c.blob, c.store), DefaultIndex: c.siteDefaultIndex, Logger: p.logger, ReferentPollInterval: c.pacing.ReferentPollInterval})
	ctrl.Register(v1.KindSite.GVK(), siteReconciler)
	ctrl.Watches(v1.KindRoute.GVK(), site.MapRoute)             // status is derived from the same-named owned Route
	ctrl.Watches(v1.KindBucket.GVK(), siteReconciler.MapBucket) // a raised maxObjectBytes lets a refused bundle deploy
	// KVStore reconciler (ADR-0072/0073): Ready + status.tables/bindings; on delete reclaim the store
	// prefix and on a table removed from spec.tables[] reclaim its sub-prefix, via the driver's
	// DropPrefix+List (type-asserted PrefixManager — a driver without it gets a no-op).
	prefixMgr, _ := c.kvStore.(kvsvc.PrefixManager)
	kvReconciler, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: c.store, KV: prefixMgr, Logger: p.logger})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build KVStore reconciler")
	}
	ctrl.Register(v1.KindKVStore.GVK(), kvReconciler)
	p.kvReconciler = kvReconciler
	ctrl.Watches(v1.KindFunction.GVK(), kvReconciler.MapFunction) // status.bindings counts Function.spec.kv
	// CatalogService reconciler (ADR-0086 as reworked by ADR-0087/F48/F57): the DuckDB/Quack engine
	// is deployed by the add-on-provider runtime (NOT a backing Function). The provider-runtime reuses
	// the EXISTING container port + ingress gateway; the reconciler derives the per-fn S3 keypair over
	// the provider identity (ADR-0085) and resolves spec.secrets/spec.config (the Quack token + engine
	// config) into the engine env. DuckDB runs out-of-process in the curated image — no cgo in daemon.
	// Prod: the add-on-provider runtime supervises the curated duckdb CONTAINER. Dev (ADR-0125)
	// injects a process-mode driver that runs the embedded engine as a subprocess (no containers).
	providerRuntime := c.catalogProvider
	if providerRuntime == nil {
		var perr error
		providerRuntime, perr = provider.NewRuntime(provider.Deps{
			Runtime: c.runtime, OwnerKind: v1.KindCatalogService, Gateway: c.gateway, Logger: p.logger,
			ProbeTimeout: c.pacing.EngineProbeTimeout,
		})
		if perr != nil {
			return fault.Wrapf(perr, fault.KindOf(perr), op, "build provider runtime")
		}
	}
	catalogDeps := catalogsvc.ReconcilerDeps{
		Store:      c.store,
		Provider:   providerRuntime,
		Secrets:    secretResolver,
		S3Endpoint: c.s3gwEndpoint,
		ImageFor:   c.imageFor,
		Logger:     p.logger,
		Proxy:      catalogMgr, // ADR-0137: Ensure a node-private catalog PEP proxy per Ready catalog
		Routes:     edgeAgg,    // ADR-0138: program the opt-in external edge entry to the proxy

		SupervisionPeriod:    c.pacing.SupervisionPeriod,
		ReferentPollInterval: c.pacing.ReferentPollInterval,
		EnginePollInterval:   c.pacing.EnginePollInterval,
	}
	// The engine's S3 keypair is derived over its CatalogService identity (ADR-0085, ADR-0175) by the same
	// deriver the Function reconciler uses — present only when the S3 gateway is enabled.
	if s3Injection.Enabled {
		catalogDeps.Derive = s3Injection.Derive
		if catalogDeps.S3Endpoint == "" {
			catalogDeps.S3Endpoint = "http://" + s3Injection.ListenAddr
		}
	}
	catalogReconciler, err := catalogsvc.NewReconciler(catalogDeps)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build CatalogService reconciler")
	}
	ctrl.Register(v1.KindCatalogService.GVK(), catalogReconciler)
	// ADR-0162: a consumer's status follows its catalog, and a released listener closes once no Function binds it.
	ctrl.Watches(v1.KindCatalogService.GVK(), fnReconciler.MapCatalogService)
	ctrl.Watches(v1.KindFunction.GVK(), catalogReconciler.MapFunction)

	// ADR-0135 (F100): the Identity credential-issuing reconciler — issues a keypair→owned Secret and
	// registers it (via storeExternalKeys, wired into s3gateway.Deps.External above).
	identityReconciler, err := identitysvc.NewReconciler(identitysvc.ReconcilerDeps{Store: c.store, Logger: p.logger, SupervisionPeriod: c.pacing.SupervisionPeriod})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build Identity reconciler")
	}
	ctrl.Register(v1.KindIdentity.GVK(), identityReconciler)

	// ADR-0101/0103: one shared funclog trace sink — the F51 per-invocation step spans AND the
	// engine's per-run root span (ADR-0103) both persist here, so a run's spans form one coherent
	// trace. Built whenever a blob substrate is present and traces are enabled (NOT gated on
	// LogCapturer: the run-root span is emitted in-process, not over the shim channel). traceSink is
	// a nil INTERFACE when disabled (never a typed-nil *BlobTraceSink), so the reconciler's nil-check
	// holds. pkg/funcd owns its lifecycle (closed once at shutdown); consumers only hold a reference.
	var traceSink funclog.TraceSink
	if c.blob != nil && !c.funclogDisabled && !c.funclogTracesDisabled {
		ts, terr := funclog.NewBlobTraceSink(funclog.Deps{
			Bucket: c.blob, Clock: clock.System(), Logger: p.logger,
			SegmentMaxAge: c.funclogMaxAge, SegmentMaxBytes: c.funclogMaxBytes,
		})
		if terr != nil {
			return fault.Wrapf(terr, fault.Internal, op, "build funclog trace sink")
		}
		p.traceSink = ts
		traceSink = ts
	}

	// Workflow engine (ADR-0094): durable run state (Badger at workflowDataDir; in-memory when
	// unset — the InMemory preset / tests), a step dispatcher over the activator's endpoints +
	// waker (fail-closed to targets that resolve to a real Function), and two reconcilers — the
	// Workflow reconciler materializes the owned step Function/KVStore fleet, the WorkflowRun
	// reconciler drives a run through the engine and mirrors its status + status.runs link.
	runs, rerr := wbadger.New(wbadger.Config{InMemory: c.workflowDataDir == "", Dir: c.workflowDataDir})
	if rerr != nil {
		return fault.Wrapf(rerr, fault.KindOf(rerr), op, "build workflow run store")
	}
	p.workflowRuns = runs
	wfDispatcher, derr := workflow.NewHTTPDispatcher(workflow.DispatchDeps{
		Endpoints: fnReconciler.Endpoints(),
		Waker:     act, // wake a scaled-to-zero step function (ADR-0033)
		Grant:     storeGranter{store: c.store},
		// No client Timeout: the engine bounds each attempt with the step's timeout on the request context
		// (ADR-0094), and a client-wide cap would cut a longer step short.
		Client: workerClient(calls, 0),
		Logger: p.logger,
	})
	if derr != nil {
		return fault.Wrapf(derr, fault.KindOf(derr), op, "build workflow dispatcher")
	}
	maxAttempts := c.workflowDefaultRetry
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	wfEngine, eerr := workflow.New(workflow.Deps{
		Runs:     runs,
		Dispatch: wfDispatcher,
		Config: workflow.Config{
			DefaultMaxAttempts: maxAttempts, DefaultStepTimeout: c.workflowStepTimeout, PayloadLimit: c.workflowPayloadLimit,
			MaxStepsInFlight: c.workflowMaxStepsInFlight, DefaultRetryBackoff: c.pacing.DefaultRetryBackoff,
		},
		Children: childResolver{c.store}, // ADR-0099: resolve a child workflow's spec for a `workflow:` step
		Traces:   traceSink,              // ADR-0104: the engine emits the run-root span for inline sub-workflow child runs
		Notify: func(ns v1.NamespaceName, name v1.ObjectName) { // ADR-0146: the run reconciler mirrors the run
			ctrl.Enqueue(controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: ns, Name: name})
		},
		Logger: p.logger,
	})
	if eerr != nil {
		return fault.Wrapf(eerr, fault.KindOf(eerr), op, "build workflow engine")
	}
	p.workflowRetention = c.workflowRetention
	p.workflowEngine = wfEngine
	wfMaterializer := workflow.NewMaterializer(c.store, runtimeResolver{}, p.logger, c.pacing.SupervisionPeriod)
	wfContracts := workflow.ContractResolver(contractResolver{})
	if c.workflowContracts != nil {
		wfContracts = c.workflowContracts
	}
	ctrl.Register(v1.KindWorkflow.GVK(), workflow.NewWorkflowReconciler(c.store, wfMaterializer, wfContracts, p.logger, c.pacing.ArtifactPollInterval))
	p.workflowSweeper = workflow.NewRunReconciler(c.store, wfEngine, traceSink, p.logger, c.pacing.ReferentPollInterval)
	ctrl.Register(v1.KindWorkflowRun.GVK(), p.workflowSweeper)
	// ADR-0199: the App reconciler writes an App's parts; a change to a part it controls or marks, or to an
	// object a ref entry names, requeues the App.
	appReconciler, err := app.NewReconciler(app.Deps{
		Store: c.store, Purger: bucketPurger{shared: c.blob}, Logger: p.logger, Clock: clock.System(),
		UpgradeTimeout: c.pacing.appUpgradeTimeout(), RevisionHistory: c.appRevisionHistory,
		SupervisionPeriod: c.pacing.SupervisionPeriod,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build App reconciler")
	}
	ctrl.Register(v1.KindApp.GVK(), appReconciler)
	for _, pair := range gc.Pairs() {
		if pair.Owner == v1.KindApp && pair.Child != v1.KindAppRevision { // a record, not a part (ADR-0200)
			ctrl.Watches(pair.Child.GVK(), appReconciler.MapPart)
		}
	}
	ctrl.Watches(v1.KindSecret.GVK(), appReconciler.MapSecret) // ADR-0213: a declared Secret created, changed or deleted
	p.controller = ctrl

	// partAdmissions are the admissions a direct write passes, over the reader r: the control plane runs them over
	// the store, and app-parts over a view that already holds an App's other parts (ADR-0199 Decision 3).
	partAdmissions := func(r admission.StoreReader) []admission.Admission {
		return []admission.Admission{
			// ADR-0064 fn-to-fn link rules on the write path.
			admission.NewLinkValidityAdmission(r),
			admission.NewLinkDeletionProtectionAdmission(r),
			// ADR-0072/0073 KV resource rules: store-count quota + KVStore deletion-protection (bound by
			// spec.kv or non-empty data on Delete; still-bound table removal and unreclaimed table re-add on
			// Update). Binding/owner EXISTENCE (Function.spec.kv → an existing store/table; KVStore
			// tables[].owner → a real Function) is RECONCILE-TIME (ADR-0121): the Function reconciler holds a
			// binding not-Ready until it resolves, and the owner UID is fail-closed at the PDP until the owner
			// exists — no write-time existence gate.
			admission.NewKVStoreQuotaAdmission(r, kvMaxStores),
			admission.NewKVStoreDeletionProtectionAdmission(r, kvProber{c.kvStore}),
			// ADR-0080 Bucket resource rules (the KVStore parallel): bucket-count quota + bucket-deletion-
			// protection (bound by spec.blob or non-empty data on Delete; still-bound prefix removal on Update).
			// Binding/owner EXISTENCE is reconcile-time (ADR-0121), as for KV. The data-emptiness prober Lists
			// the same substrate view the s3gateway writes (s3BucketFor).
			admission.NewBucketQuotaAdmission(r, bucketMax),
			admission.NewBucketDeletionProtectionAdmission(r, blobProber{blobBucketLister{resolve: s3BucketFor(c.blob, c.store)}}),
			// ADR-0086/0091 catalog blob + consumer-binding EXISTENCE were write-time gates; now reconcile-time
			// (ADR-0121): the CatalogService / Function reconcilers wait for the referent (Waiting condition).
			// ADR-0139 Site: spec.prefix is immutable on Update (the prefix permanently holds the site's bundles).
			admission.NewSitePrefixImmutableAdmission(),
			// ADR-0074 Policy validity: spec.cedar parses + references only the curated schema
			// (kv::read/kv::write; Function/KVStore/KVTable) — so every stored Policy compiles.
			admission.NewPolicyValidityAdmission(),
			// ADR-0094 WorkflowRun payload cap: spec.input ≤ payloadLimit (larger data by reference).
			admission.NewWorkflowRunPayloadAdmission(c.workflowPayloadLimit),
			// ADR-0098 F65: reject a WorkflowRun whose input violates the parent's cached contract (zero registry I/O).
			admission.NewWorkflowRunContractAdmission(storeReader{c.store}),
			// ADR-0094: a run's workflow/input/replay are fixed at creation — a second run under a taken name is a Conflict.
			admission.NewWorkflowRunSpecImmutableAdmission(),
			// ADR-0170: a ResourceGroup is deleted only when it has no member (or with force, members first).
			admission.NewResourceGroupDeletionProtectionAdmission(r),
		}
	}

	// ADR-0084: the function-log reader backing GET …/functions/{name}/logs (funcdctl logs). Present
	// whenever a blob substrate is — nil leaves the route unregistered. ADR-0106: the run-scoped querier
	// (GET …/workflowruns/{name}/logs) reuses the same reader + the metastore (to resolve status.traceId) + the
	// run records (--step resolves against the run's pinned spec).
	var logReader controlplane.LogQuerier
	var runLogQuerier controlplane.WorkflowRunLogQuerier
	if c.blob != nil {
		reader := logread.NewBlobReader(c.blob)
		logReader = reader
		runLogQuerier = controlplane.NewWorkflowRunLogQuerier(c.store, runs, reader)
	}
	handler, err := controlplane.NewServer(controlplane.Deps{
		Store:       c.store,
		Authorizer:  c.authorizer,
		Credentials: c.credentials,
		Logger:      p.logger,
		Logs:        logReader,
		RunLogs:     runLogQuerier,
		DeadLetters: dlq,              // ADR-0118: the DLQ read + replay/discard surface
		Replayer:    sensorReconciler, // ADR-0118: the imperative replay seam (one synchronous attempt)
		Collector:   p.collector,      // ADR-0170: a forced ResourceGroup delete collects the members' children
		Admissions:  append(partAdmissions(storeReader{c.store}), app.NewAdmission(partAdmissions, storeReader{c.store})),
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build control-plane server")
	}
	// ReadTimeout bounds the request read, as on the data plane (issue #90); with no IdleTimeout set,
	// net/http also uses it as the keep-alive idle bound, so a silent client cannot hold a connection (#300).
	// ErrorLog sends net/http's own errors (TLS handshake, accept, recovered panic) through the configured
	// logger; left nil they go to the stdlib log package, bypassing the log format (#454).
	httpErrorLog := slog.NewLogLogger(p.logger.Handler(), slog.LevelWarn)
	p.httpServer = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, ErrorLog: httpErrorLog}

	ln, err := net.Listen("tcp", c.listenAddr)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "bind control-plane listener on %s", c.listenAddr)
	}
	p.listener = ln
	p.addr = ln.Addr().String()

	// Data plane (ADR-0033): a SEPARATE listener serving function invocations through the
	// activator (path+store → activator), distinct from the authenticated control plane.
	// ADR-0112 (F75): the ingress-protection limiter is the INNERMOST middleware (last vararg) so
	// runtime order is Recover → RequestID → limit → dataplane.Handler — rejects (429/413/503) precede
	// the activator (zero wake) yet stay panic-guarded + X-Request-Id-correlated. A zero Config is a
	// pass-through (limits off by default). ADR-0164: under key: function the rate step runs inside
	// dataplane.Handler instead (limit.NewTargetLimiter), once the target Function is resolved.
	// ADR-0113 (F77): the edge authn PEP runs INSIDE dataplane.Handler (after the target resolves,
	// before store.Get + the activator). Built from the control-plane credentials + authorizer; nil
	// unless enabled (an authenticated stance then fails closed).
	var edgeEnforcer *authn.Enforcer
	if c.edgeAuthEnabled {
		edgeEnforcer, err = authn.New(authn.Deps{Creds: c.credentials, Authz: c.authorizer})
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "build edge authn PEP")
		}
	}
	// ADR-0120 (F82): the static-asset handler serves a static Route's Bucket prefix directly over the
	// SAME per-namespace Bucket view the S3 frontend uses (s3BucketFor → blob.Prefixed), with no
	// activator hop. Always wired; a static Route match without it would 404.
	staticHandler, err := static.New(static.Deps{Buckets: s3BucketFor(c.blob, c.store), Logger: p.logger})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build static asset handler")
	}
	// ADR-0114 (F76/F78): observability wraps outer-than-limit (times the whole hop incl. rejects) but
	// inner-than-RequestID (reads X-Request-Id); shaping is innermost (wraps the real response). The write stall
	// bound is outermost, so every byte of every listener response passes it. Runtime order:
	// WriteStall → Recover → RequestID → observ → limit → shape → dataplane.Handler.
	dpCore := dataplane.Handler(c.store, act, p.edgeRouter, edgeEnforcer, limit.NewTargetLimiter(c.limits), staticHandler,
		c.invokeDefaultTimeout, p.logger)
	edgeObserv, edgeShape := observ.Chain(c.observ, c.telemetry, p.logger), shape.Chain(c.shaping)
	dpHandler := gateway.Chain(dpCore, gateway.WriteStall(gateway.WriteStallTimeout), gateway.Recover(p.logger),
		gateway.RequestID, edgeObserv, limit.Chain(c.limits), edgeShape)
	// Late-bind the worker-node local API invoker (ADR-0064) to the same chain minus the ingress
	// limiter and edge observ: ADR-0112 guards the listener, so a nested fn-to-fn invoke never takes its
	// caller's in-flight slot or rate token (#87); edge signals describe only listener requests, so an
	// internal call records no edge span, metric or access-log line (ADR-0165).
	dpHolder.Set(gateway.Chain(dpCore, gateway.Recover(p.logger), gateway.RequestID, edgeShape))
	// ReadTimeout bounds the whole request read (headers + body), so a client that stops sending its
	// body cannot hold an ADR-0112 in-flight slot indefinitely (issue #90). net/http clears the
	// deadline once the body is read, so it does not cut a long-running handler.
	p.dataPlaneServer = &http.Server{Handler: dpHandler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, ErrorLog: httpErrorLog}
	dln, err := net.Listen("tcp", c.dataPlaneAddr)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "bind data-plane listener on %s", c.dataPlaneAddr)
	}
	p.dataPlaneListener = dln
	p.dataPlaneAddr = dln.Addr().String()

	// ADR-0081: structured function-log capture (Path B). If the runtime driver implements the
	// LogCapturer capability and a blob substrate is present, build the funclog sink and install the
	// per-instance capture hook — a Pump per channel that drains the shim's NDJSON into the sink. Path A, the raw
	// stdout/stderr, is installed beside it on the same gate (ADR-0168).
	if lc, ok := c.runtime.(runtime.LogCapturer); ok && c.blob != nil && !c.funclogDisabled {
		sink, serr := funclog.NewBlobSink(funclog.Deps{
			Bucket: c.blob, Clock: clock.System(), Logger: p.logger,
			SegmentMaxAge: c.funclogMaxAge, SegmentMaxBytes: c.funclogMaxBytes,
		})
		if serr != nil {
			return fault.Wrapf(serr, fault.Internal, op, "build funclog sink")
		}
		p.logSink = sink
		// ADR-0101: the traces signal rides the same channel. Reuse the shared trace sink built above
		// (ADR-0103) so step spans + the run-root span land in one trace store; the demux (Route) sends
		// span-tagged lines to it and untagged lines to the logs sink. nil ⇒ span lines are dropped.
		// Dev streams logs live by tee'ing an observer onto the logs sink (production leaves it nil, so
		// logs are only persisted).
		var logs funclog.Sink = sink
		if c.logObserver != nil {
			logs = &teeLogSink{inner: sink, observe: c.logObserver}
		}
		sinks := funclog.Sinks{Logs: logs, Traces: traceSink}
		lc.SetLogCapture(func(spec runtime.WorkerSpec, r io.ReadCloser) {
			res := funclog.Resource{
				Namespace: string(spec.Namespace),
				Function:  string(spec.Name),
				Replica:   strconv.Itoa(spec.Replica),
			}
			if !strings.HasPrefix(string(spec.Name), "__pool__") {
				p.logRoutes.start(r, func() { _ = funclog.Route(context.Background(), r, sinks, res, p.logger) })
				return
			}
			// A pool worker's records are stored under the member each names. The set is taken once per
			// process, here at its start, so a member removed later still has its drained records kept.
			_, isMember, _ := fnReconciler.PoolMembers(spec.Namespace, spec.Name)
			p.logRoutes.start(r, func() { _ = funclog.RoutePool(context.Background(), r, sinks, res, isMember, p.logger) })
		})
		// ADR-0168 Path A: a Function worker's raw stdout (INFO) and stderr (ERROR) go to the same logs sink, dev's tee
		// included. An engine's worker keeps only its tail.
		if oc, ok := c.runtime.(runtime.OutputCapturer); ok {
			oc.SetOutputCapture(func(spec runtime.WorkerSpec, out *workerpipe.Output) {
				if spec.OwnerKind != v1.KindFunction {
					return
				}
				res := funclog.Resource{Namespace: string(spec.Namespace), Function: string(spec.Name), Replica: strconv.Itoa(spec.Replica)}
				dst := logs
				if strings.HasPrefix(string(spec.Name), "__pool__") {
					// A raw line names no member, so it is stored under each member the pool runs now.
					members, _, _ := fnReconciler.PoolMembers(spec.Namespace, spec.Name)
					dst = newMemberSink(logs, res, members)
				}
				for _, src := range []struct {
					stream workerpipe.Stream
					source funclog.Source
				}{{workerpipe.Stdout, funclog.SourceStdout}, {workerpipe.Stderr, funclog.SourceStderr}} {
					r := out.Reader(src.stream)
					p.logRoutes.start(r, func() {
						_ = funclog.Pump(context.Background(), funclog.NewRawReader(r, src.source), dst, res, p.logger)
					})
				}
			})
		}
	}

	// ADR-0083: funclog compacted compaction. When a blob substrate is present and compaction is not disabled,
	// build the daemon-internal compactor that folds raw OTLP-JSONL into partitioned Parquet. Defaults
	// (window 1h / interval 5m / retention 30d) unless WithLogCompaction set explicit values.
	if c.blob != nil && !c.logCompactDisabled {
		retention := compact.DefaultRetention
		if c.logCompactConfigured {
			retention = c.logCompactRetention
		}
		comp, cerr := compact.New(compact.Deps{
			Bucket: c.blob, Clock: clock.System(), Logger: p.logger,
			Window: c.logCompactWindow, Interval: c.logCompactInterval, Retention: retention,
		})
		if cerr != nil {
			return fault.Wrapf(cerr, fault.Internal, op, "build funclog compactor")
		}
		p.compactor = comp
	}

	return nil
}

// Addr returns the bound control-plane address (host:port), available after New.
func (p *Platform) Addr() string { return p.addr }

// DataPlaneAddr returns the bound data-plane (function-invocation) address, available
// after New (ADR-0033).
func (p *Platform) DataPlaneAddr() string { return p.dataPlaneAddr }

// WaitS3Gateway blocks until the WithS3Gateway frontend is bound and serving (nil), it stopped first (its error),
// or ctx is done. The gateway binds its address only once Run starts it (ADR-0085), so a taken port surfaces here
// and from Run, not from New. Without the gateway it returns nil.
func (p *Platform) WaitS3Gateway(ctx context.Context) error {
	if p.s3gw == nil {
		return nil
	}
	return p.s3gw.Wait(ctx)
}

// Run starts the control loops + the control-plane server and blocks until ctx is
// cancelled, then shuts down gracefully and returns nil. It owns the crash-only
// lifecycle (ADR-0028): a setup error, such as an S3 gateway that cannot bind its address, also shuts the
// platform down before Run returns it.
func (p *Platform) Run(ctx context.Context) error {
	p.logger.InfoContext(ctx, "platform starting", "addr", p.addr, "dataPlaneAddr", p.dataPlaneAddr)
	p.logProviders(ctx)

	ctx, cancelLoops := context.WithCancel(ctx)
	defer cancelLoops()
	var wg sync.WaitGroup
	// abort stops the loops Run started and shuts the platform down, so a setup error leaves nothing open
	// (issue #489, ADR-0028).
	abort := func(err error) error {
		cancelLoops()
		wg.Wait()
		closeCtx, cancelClose := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancelClose()
		return errors.Join(err, p.Shutdown(closeCtx))
	}

	// Egress network isolation (ADR-0115, F80): program the default-deny + redirect substrate once, at
	// start, before any worker serves (fail-closed) — a no-op when disabled or non-Linux. A failure to
	// program is fatal: a half-applied egress fence must not run.
	if p.cfg.netManager != nil {
		if err := p.cfg.netManager.Apply(ctx, p.cfg.netPolicy); err != nil {
			return abort(fault.Wrapf(err, fault.KindOf(err), "funcd.Run", "apply worker egress isolation"))
		}
	}

	// ADR-0178 Decision 3: mark the KVStores the previous materializer made, before any controller, the
	// collector or the control plane reads them.
	if err := workflow.MarkKVStoresOnce(ctx, p.cfg.store, p.logger); err != nil && ctx.Err() == nil {
		return abort(fault.Wrapf(err, fault.KindOf(err), "funcd.Run", "mark workflow kv stores"))
	}

	if p.s3gw != nil { // ADR-0080/0085: the S3-protocol frontend listener (opt-in; stops on ctx cancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.s3gw.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.logger.ErrorContext(ctx, "s3 gateway stopped", "error", err)
			}
		}()
		// versitygw binds the address only once Run serves it, so a taken port fails Run here, before anything
		// else starts, as a taken control-plane port fails New (#497, ADR-0028).
		if err := p.s3gw.Wait(ctx); err != nil && ctx.Err() == nil {
			return abort(fault.Wrapf(err, fault.KindOf(err), "funcd.Run", "start the s3 gateway"))
		}
	}

	// Issue #708 (ADR-0170 "also across a crash"): reclaim the data of each KVStore deleted before its reconcile
	// ran, before the controller or the control plane can create a store of the same name.
	if err := p.kvReconciler.ReclaimDeleted(ctx); err != nil && ctx.Err() == nil {
		p.logger.WarnContext(ctx, "reclaim of deleted KV stores incomplete", "error", err)
	}
	// ADR-0199 Decision 7: the same for the objects of each Bucket deleted before its second purge ran.
	if err := reclaimDeletedBuckets(ctx, p.cfg.blob, p.cfg.store, p.logger); err != nil && ctx.Err() == nil {
		p.logger.WarnContext(ctx, "reclaim of deleted Buckets incomplete", "error", err)
	}

	// ADR-0176 Decision 6: the edge aggregator holds every catalog entry until "routes" has Set once, and no
	// Route event follows when no Route exists, so the full Route set is Set here, before the controller runs.
	if _, err := p.routeReconciler.Reconcile(ctx, controller.Request{GVK: v1.KindRoute.GVK()}); err != nil && ctx.Err() == nil {
		return abort(fault.Wrapf(err, fault.KindOf(err), "funcd.Run", "load the Route table"))
	}

	wg.Add(4)
	go func() {
		defer wg.Done()
		if err := p.controller.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.logger.ErrorContext(ctx, "controller stopped", "error", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := p.collector.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.logger.ErrorContext(ctx, "garbage collector stopped", "error", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := p.eventing.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.logger.ErrorContext(ctx, "eventing stopped", "error", err)
		}
	}()
	if p.blobWatcher != nil { // ADR-0119: the blob EventSource poll loop (drains on ctx cancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.blobWatcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.logger.ErrorContext(ctx, "blob watcher stopped", "error", err)
			}
		}()
	}
	go func() {
		defer wg.Done()
		if err := p.activator.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.logger.ErrorContext(ctx, "activator stopped", "error", err)
		}
	}()
	if p.compactor != nil { // ADR-0083: funclog compacted compaction loop (stops on ctx cancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.compactor.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.logger.ErrorContext(ctx, "funclog compactor stopped", "error", err)
			}
		}()
	}
	if p.workflowSweeper != nil && p.workflowRetention > 0 { // ADR-0094: periodic terminal-run retention sweep
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.runWorkflowRetention(ctx)
		}()
	}
	if p.workflowEngine != nil { // ADR-0146: the run goroutines, drained within the shutdown bound before Shutdown
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.workflowEngine.Run(ctx, p.drainTimeout)
		}()
	}
	if p.sensorReconciler != nil { // ADR-0118: the Sensor action-delivery retry workers (drained on ctx cancel, within the shutdown bound)
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.sensorReconciler.RunRetryWorkers(ctx, p.drainTimeout)
		}()
	}
	if p.deadLetters != nil && (p.deadletterRetention > 0 || p.deadletterMaxEntries > 0) { // ADR-0118: DLQ retention sweep
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.runDeadLetterRetention(ctx)
		}()
	}
	if p.egressForwarder != nil { // ADR-0117 (F81): the DNS forwarder / domain trust anchor (stops on ctx cancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.egressForwarder.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.logger.ErrorContext(ctx, "egress dns forwarder stopped", "error", err)
			}
		}()
	}
	if p.egressGateway != nil { // ADR-0117 (F81): the transparent egress PEP (no-op on non-Linux; stops on ctx cancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.egressGateway.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.logger.ErrorContext(ctx, "egress gateway stopped", "error", err)
			}
		}()
	}
	if p.egressWorkers != nil { // ADR-0117 §5: keep the src-IP→worker index synced from the running worker set
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.syncEgressWorkers(ctx)
		}()
	}
	// TLS termination (ADR-0111, F74): when configured, both listeners serve HTTPS. The provider is
	// given the F79 Route hosts (+ configured hosts) for its cert set; plaintext otherwise.
	serve := func(srv *http.Server, ln net.Listener) error { return srv.Serve(ln) }
	if p.cfg.tlsSpec != nil {
		const top = "funcd.Run.tls"
		spec := *p.cfg.tlsSpec
		if spec.StorageDir == "" && p.cfg.artifactDir != "" {
			spec.StorageDir = filepath.Join(p.cfg.artifactDir, "funcd-tls")
		}
		prov, terr := edgetls.New(spec, p.logger)
		if terr != nil {
			return abort(fault.Wrapf(terr, fault.KindOf(terr), top, "build tls provider"))
		}
		p.tlsProvider = prov
		hosts := append(append([]string{}, spec.Hosts...), p.edgeRouter.Hosts()...)
		if terr := prov.Manage(ctx, hosts); terr != nil {
			return abort(fault.Wrapf(terr, fault.KindOf(terr), top, "provision tls certs"))
		}
		cfg, terr := prov.TLSConfig()
		if terr != nil {
			return abort(fault.Wrapf(terr, fault.KindOf(terr), top, "build tls config"))
		}
		// ServeTLS writes its server's config (the HTTP/2 setup), so the two servers, served at once, never share one
		p.httpServer.TLSConfig = cfg
		p.dataPlaneServer.TLSConfig = cfg.Clone()
		serve = func(srv *http.Server, ln net.Listener) error { return srv.ServeTLS(ln, "", "") }
		p.logger.InfoContext(ctx, "TLS enabled", "mode", string(spec.Mode), "hosts", hosts)
	}
	go func() {
		if err := serve(p.httpServer, p.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.logger.ErrorContext(ctx, "control-plane server stopped", "error", err)
		}
	}()
	go func() {
		if err := serve(p.dataPlaneServer, p.dataPlaneListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.logger.ErrorContext(ctx, "data-plane server stopped", "error", err)
		}
	}()

	<-ctx.Done()

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.drainTimeout)
	defer cancel()
	p.logger.InfoContext(stopCtx, "platform stopping")
	// Drain both HTTP servers (stop accepting) before the ports close — an in-flight
	// invocation needs the runtime/store (ADR-0033).
	_ = p.dataPlaneServer.Shutdown(stopCtx)
	_ = p.httpServer.Shutdown(stopCtx)
	wg.Wait() // drain controller + eventing + activator before closing ports
	closeCtx, cancelClose := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancelClose()
	return p.Shutdown(closeCtx)
}

// Shutdown closes the platform's components (the control-plane listener, the five
// ports, telemetry). It is concurrency-safe and idempotent (sync.Once so Run's
// shutdown-on-ctx and a caller's explicit Shutdown cannot race or double-close) and
// best-effort (joins errors, closing all even if one fails).
func (p *Platform) Shutdown(ctx context.Context) error {
	p.shutdownOnce.Do(func() {
		if p.listener != nil {
			_ = p.listener.Close() // best-effort; may already be closed by httpServer.Shutdown
		}
		if p.dataPlaneListener != nil {
			_ = p.dataPlaneListener.Close() // best-effort; may already be closed by dataPlaneServer.Shutdown
		}
		if p.invokeMgr != nil {
			p.invokeMgr.Close() // stop all per-function local API listeners (ADR-0064)
		}
		if p.tlsProvider != nil {
			_ = p.tlsProvider.Close(ctx) // stop certmagic's renewal goroutine (ADR-0111)
		}
		if cl, ok := p.cfg.kvStore.(io.Closer); ok { // the durable KV driver (ADR-0066/0069)
			_ = cl.Close()
		}
		// Stop the per-CatalogService catalog PEP proxies (ADR-0137) — node-private http.Servers that
		// front the engines; closed before the runtime tears the engines down.
		if p.catalogProxy != nil {
			p.catalogProxy.Shutdown()
		}
		// Stop the S3 gateway (ADR-0080/0085) before blob.Close — it serves from the blob substrate.
		var s3gwErr error
		if p.s3gw != nil {
			s3gwErr = p.s3gw.Close()
		}
		// Close the runtime first (stops instances → log channels EOF), let every capture Route read its
		// channel to the end and flush, then seal any remaining funclog segments, all before blob.Close()
		// (the sink writes to blob) — ADR-0081.
		runtimeErr := closeDriver(p.cfg.runtime)
		// The egress fence comes down only once the runtime has stopped the workers it confines. When the runtime's Close
		// failed a worker may still run, so the nftables tables stay for the next start's Apply to replace.
		if p.cfg.netManager != nil && runtimeErr == nil {
			_ = p.cfg.netManager.Remove(ctx) // tear down the egress nftables tables (ADR-0115, F80)
		}
		if p.egressGateway != nil {
			_ = p.egressGateway.Close() // stop the transparent egress PEP (ADR-0117, F81)
		}
		if p.fnReconciler != nil {
			_ = p.fnReconciler.Close() // the pool workers are stopped, so their manifests can go
		}
		p.logRoutes.drain(ctx)
		var logSinkErr, traceSinkErr error
		if p.logSink != nil {
			logSinkErr = p.logSink.Close()
		}
		if p.traceSink != nil { // ADR-0101: seal remaining trace segments before blob.Close()
			traceSinkErr = p.traceSink.Close()
		}
		errs := []error{
			s3gwErr,
			closeDriver(p.cfg.bus),
			closeDriver(p.cfg.gateway),
			runtimeErr,
			logSinkErr,
			traceSinkErr,
			closeDriver(p.cfg.blob),
			closeDriver(p.cfg.store),
		}
		if p.workflowRuns != nil {
			errs = append(errs, p.workflowRuns.Close())
		}
		if p.eventStore != nil { // ADR-0201: after Run stopped the BlobWatcher and the Sensor
			errs = append(errs, p.eventStore.Close())
		}
		if p.invokeTmpDir != "" { // after the runtime stopped the workers that dial its sockets (issue #330)
			errs = append(errs, os.RemoveAll(p.invokeTmpDir))
		}
		if p.cfg.telemetry != nil {
			errs = append(errs, p.cfg.telemetry.Shutdown(ctx))
		}
		p.shutdownErr = errors.Join(errs...)
	})
	return p.shutdownErr
}

// closeDriver closes a required driver, which is nil when New failed before an option set it.
func closeDriver(c io.Closer) error {
	if c == nil {
		return nil
	}
	return c.Close()
}

// runtimeResolver is the production workflow.RuntimeResolver: it reads a step image's runtime
// class from the OCI manifest annotation (dev.funcd.runtime.v1, ADR-0094) without pulling the
// bundle. The image string is the OCI ref; no digest is pinned here (the manifest is the truth).
type runtimeResolver struct{}

func (runtimeResolver) Runtime(ctx context.Context, image string) (v1.RuntimeName, error) {
	rt, err := artifact.InspectRuntime(ctx, image, "")
	if err != nil {
		return "", err
	}
	return v1.RuntimeName(rt), nil
}

// contractResolver is the production workflow.ContractResolver (ADR-0098): it reads a step image's I/O
// contract from OCI metadata (never the bundle) and the resolved manifest digest, for the typed-edge gate.
type contractResolver struct{}

func (contractResolver) Contract(ctx context.Context, image string) (v1.WorkflowContract, string, error) {
	blob, digest, err := artifact.InspectContract(ctx, image, "")
	if err != nil {
		return v1.WorkflowContract{}, "", err
	}
	var c v1.WorkflowContract
	if uerr := json.Unmarshal(blob, &c); uerr != nil {
		return v1.WorkflowContract{}, "", uerr
	}
	return c, digest, nil
}

// childResolver is the production workflow.ChildResolver (ADR-0099): it reads a child Workflow's pinned
// spec from the store for a `workflow:` sub-workflow step's inline execution.
type childResolver struct{ s store.Store }

var _ workflow.ChildWorkflowResolver = childResolver{} // implements the optional seam: the inline child run pins its step contracts

func (r childResolver) Child(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.WorkflowSpec, map[v1.ObjectName]string, error) {
	wf, err := r.ChildWorkflow(ctx, ns, name)
	if err != nil {
		return v1.WorkflowSpec{}, nil, err
	}
	// ADR-0107: the child's resolved step images (its ADR-0098 status cache) digest-pin the inline child run.
	var images map[v1.ObjectName]string
	if len(wf.Status.Steps) > 0 {
		images = make(map[v1.ObjectName]string, len(wf.Status.Steps))
		for _, s := range wf.Status.Steps {
			if s.Image != "" {
				images[s.Name] = s.Image
			}
		}
	}
	return wf.Spec, images, nil
}

func (r childResolver) ChildWorkflow(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.Workflow, error) {
	obj, err := r.s.Get(ctx, v1.KindWorkflow.GVK(), ns, name) // V1: same-namespace children (ADR-0099 scope)
	if err != nil {
		return nil, err
	}
	return obj.(*v1.Workflow), nil
}

// storeGranter is the production workflow.Granter: fail-closed defense-in-depth for step dispatch.
// The engine only ever dispatches steps of a run's pinned spec to their declared/materialized
// targets; this gate additionally requires the target to resolve to a real Function, so an
// unknown target is denied. (Per-run spec-as-grant is enforced structurally by the engine.) A pinned
// dispatch (ADR-0190) is granted only when the pin names the target and its Revision is still the one
// pinned: controlled by the pinned Function UID and, except for a file:// artifact, of the pinned digest.
type storeGranter struct{ store store.Store }

func (g storeGranter) Allow(ns v1.NamespaceName, target v1.ObjectName, pin *v1.RevisionPin) bool {
	ctx := context.Background()
	if pin == nil {
		_, err := g.store.Get(ctx, v1.KindFunction.GVK(), ns, target)
		return err == nil
	}
	if pin.Function != target {
		return false
	}
	obj, err := g.store.Get(ctx, v1.KindRevision.GVK(), ns, pin.Revision)
	if err != nil {
		return false
	}
	rev := obj.(*v1.Revision)
	owner, ok := v1.ControllerOf(rev.OwnerReferences)
	return ok && owner.Name == target && owner.UID == pin.FunctionUID && rev.Spec.ImageDigest == pin.ImageDigest
}

// runWorkflowRetention periodically reclaims terminal workflow runs older than the retention horizon,
// engine records and WorkflowRun objects alike (ADR-0094). It sweeps at most hourly (sooner when the horizon is short), and stops on ctx
// cancel. A sweep failure is logged, not fatal — the next tick retries.
func (p *Platform) runWorkflowRetention(ctx context.Context) {
	interval := p.workflowRetention
	if interval > time.Hour {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := p.workflowSweeper.SweepExpired(ctx, p.workflowRetention)
			if err != nil {
				p.logger.WarnContext(ctx, "workflow retention sweep failed", "error", err)
				continue
			}
			if n > 0 {
				p.logger.InfoContext(ctx, "workflow retention sweep reclaimed runs", "count", n)
			}
		}
	}
}

// runDeadLetterRetention periodically evicts dead letters past the TTL and over the per-namespace count cap
// (ADR-0118 §5), mirroring the run-retention sweep. It sweeps at most hourly (sooner when the TTL is short),
// stops on ctx cancel, and logs (never fatal) a sweep failure — the next tick retries.
func (p *Platform) runDeadLetterRetention(ctx context.Context) {
	interval := time.Hour
	if p.deadletterRetention > 0 && p.deadletterRetention < interval {
		interval = p.deadletterRetention
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := p.deadLetters.SweepExpired(ctx, p.deadletterRetention, p.deadletterMaxEntries)
			if err != nil {
				p.logger.WarnContext(ctx, "dead-letter retention sweep failed", "error", err)
				continue
			}
			if n > 0 {
				p.logger.InfoContext(ctx, "dead-letter retention sweep evicted entries", "count", n)
			}
		}
	}
}

// storeReader adapts store.Store to admission.StoreReader for the ADR-0064 link admissions and the
// ADR-0072 KV admissions (the admission package stays a near-leaf and does not import store).
// invokeMeter is the meter the nested-call cap counts its refusals on (ADR-0147); a no-op one without telemetry.
func invokeMeter(t *observability.Telemetry) metric.Meter {
	if t == nil {
		return metricnoop.NewMeterProvider().Meter("funcd.invoke")
	}
	return t.MeterProvider().Meter("funcd.invoke")
}

type storeReader struct{ s store.Store }

func (r storeReader) List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	res, err := r.s.List(ctx, gvk, store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// Get adapts store.Store.Get for the ADR-0098 WorkflowRun contract admission (reads the parent Workflow).
func (r storeReader) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	return r.s.Get(ctx, gvk, ns, name)
}

// metaReader adapts store.Store to kvsvc.MetaReader for the ADR-0073 KV BindingResolver (the kv package
// stays near-leaf and does not import store). The resolver reads the caller Function + target KVStore.
type metaReader struct{ s store.Store }

func (r metaReader) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	return r.s.Get(ctx, gvk, ns, name)
}

// cedarMetaReader adapts store.Store to cedarauth.MetaReader (ADR-0074): the cedar EntityProvider
// reads the caller Function (principal attrs) + the target KVStore (the table's owner + attrs) per
// Authorize call — only the request-relevant entities, never a full-store rebuild.
type cedarMetaReader struct{ s store.Store }

func (r cedarMetaReader) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	return r.s.Get(ctx, gvk, ns, name)
}

// policySource adapts store.Store to cedarauth.PolicySource (ADR-0074/0117): it lists every user
// v1.Policy AND compiles every v1.EgressPolicy into synthetic v1.Policy Cedar text (ADR-0117, M1 — the
// egress grant reaches the PDP through the SAME PolicySource path, listed alongside KindPolicy). The
// cache revision joins the lists' collection resourceVersions, each the store-wide one (changed by ANY
// write — a Policy, EgressPolicy, RolesAssignment, OR namespace Function add/remove), so an
// appliesTo-affecting Function change recompiles; the cache compares it for equality only (ADR-0202).
type policySource struct{ s store.Store }

func (p policySource) Policies(ctx context.Context) ([]v1.Policy, string, error) {
	const op = "funcd.policySource.Policies"
	polRes, err := p.s.List(ctx, v1.KindPolicy.GVK(), store.ListOptions{})
	if err != nil {
		return nil, "", fault.Wrapf(err, fault.KindOf(err), op, "list policies")
	}
	out := make([]v1.Policy, 0, len(polRes.Items))
	for _, o := range polRes.Items {
		if pol, ok := o.(*v1.Policy); ok {
			out = append(out, *pol)
		}
	}

	// ADR-0117 (F81): compile each namespace's EgressPolicy(ies) into synthetic v1.Policy Cedar text,
	// scoped to the namespace Function-set (appliesTo expansion). Listed alongside the user Policies.
	epRes, err := p.s.List(ctx, v1.KindEgressPolicy.GVK(), store.ListOptions{})
	if err != nil {
		return nil, "", fault.Wrapf(err, fault.KindOf(err), op, "list egress policies")
	}
	fnRes, err := p.s.List(ctx, v1.KindFunction.GVK(), store.ListOptions{})
	if err != nil {
		return nil, "", fault.Wrapf(err, fault.KindOf(err), op, "list functions")
	}
	fnByNS := map[v1.NamespaceName][]v1.ObjectName{}
	for _, o := range fnRes.Items {
		if fn, ok := o.(*v1.Function); ok {
			fnByNS[fn.Namespace] = append(fnByNS[fn.Namespace], fn.Name)
		}
	}
	for _, o := range epRes.Items {
		ep, ok := o.(*v1.EgressPolicy)
		if !ok {
			continue
		}
		syn, cerr := cedarauth.CompileEgressPolicy(ep.Namespace, ep, fnByNS[ep.Namespace])
		if cerr != nil {
			return nil, "", fault.Wrapf(cerr, fault.KindOf(cerr), op, "compile egress policy %q/%q", ep.Namespace, ep.Name)
		}
		out = append(out, syn...)
	}

	// ADR-0136 (F101): compile each RolesAssignment's read/query/invoke grants into synthetic Cedar
	// permits (write grants gate the single-writer forbid via the WriterLister, not a permit here).
	raPols, raRV, raErr := rolessvc.CompilePolicies(ctx, p.s)
	if raErr != nil {
		return nil, "", fault.Wrapf(raErr, fault.KindOf(raErr), op, "compile roles assignments")
	}
	out = append(out, raPols...)

	rev := strings.Join([]string{polRes.ResourceVersion, epRes.ResourceVersion, fnRes.ResourceVersion, raRV}, ",")
	return out, rev, nil
}

// storeWildcardPatterns adapts store.Store to egress.WildcardPatterns (ADR-0117, judge Major 2): it
// returns a namespace's EgressPolicy wildcard domain patterns ("*.x.com") so the DNS forwarder injects a
// matched pattern token, keeping wildcard authorization an exact set-membership check over a
// forwarder-derived token. Derived from the same EgressPolicy set the PDP compiles (no duplicate state).
type storeWildcardPatterns struct{ s store.Store }

func (w storeWildcardPatterns) Wildcards(ns v1.NamespaceName) []string {
	res, err := w.s.List(context.Background(), v1.KindEgressPolicy.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return nil
	}
	var out []string
	for _, o := range res.Items {
		ep, ok := o.(*v1.EgressPolicy)
		if !ok {
			continue
		}
		for i := range ep.Spec.Rules {
			for _, d := range ep.Spec.Rules[i].To.Domains {
				if len(d) > 2 && d[0] == '*' && d[1] == '.' {
					out = append(out, d)
				}
			}
		}
	}
	return out
}

// egressAuditSink records each egress decision (ADR-0117): every connection (allowed + blocked) is
// logged. Funclog-envelope convergence is an ADR-0117 open question resolved at funclog wiring; V1 logs
// through slog so the audit trail exists.
type egressAuditSink struct{ logger *slog.Logger }

func (s egressAuditSink) Egress(ctx context.Context, rec egress.AuditRecord) {
	s.logger.InfoContext(ctx, "egress decision",
		"namespace", rec.Namespace, "function", rec.Function, "domain", rec.Domain,
		"ip", rec.IP.String(), "port", rec.Port, "allowed", rec.Allowed, "reason", rec.Reason)
}

// EgressWorkerIndex returns the egress WorkerIndex the containerd runtime populates at worker
// provisioning (ADR-0117 §5): on worker-up it records the funcd0 IP → the worker's principal
// Ref (its owner kind) so the gateway can authenticate the caller by source IP. nil ⇒ egress is not enabled.
func (p *Platform) EgressWorkerIndex() *egress.MemoryWorkerIndex { return p.egressWorkers }

// syncEgressWorkers keeps the ADR-0117 §5 WorkerIndex (src funcd0 IP → principal Ref) reconciled with the
// runtime's running worker set, so the egress gateway can authenticate a redirected connection by source
// IP (and the DNS forwarder can resolve a worker's namespace for wildcard-token injection). It polls the
// runtime — the same observed view the Function reconciler drives — every interval, diffing against the
// last snapshot: a newly-running worker's IP is Added, a vanished one Removed. Runs only when the egress
// gateway is wired; stops on ctx cancel.
func (p *Platform) syncEgressWorkers(ctx context.Context) {
	t := time.NewTicker(orDefault(p.cfg.pacing.WorkerSyncInterval, defaultWorkerSyncInterval))
	defer t.Stop()
	prev := map[netip.Addr]auth.EntityRef{}
	prev = p.reconcileEgressWorkers(ctx, prev) // seed immediately (don't wait a full tick)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prev = p.reconcileEgressWorkers(ctx, prev)
		}
	}
}

// reconcileEgressWorkers computes the current running-worker IP→Ref set from the runtime (over the
// namespaces that have a Function or a CatalogService) and applies the diff against prev to the WorkerIndex,
// returning the new snapshot. Each worker is indexed as the principal of its owner kind (ADR-0175): a Function
// worker as its Function, an engine as its CatalogService; a worker of any other kind is not indexed (an
// unknown source, denied). A worker's funcd0 IP is the source IP the F80 REDIRECT preserves, so it keys both
// the gateway lookup and the DNS-forwarder correlation. A transient store error keeps the current index
// (returns prev).
func (p *Platform) reconcileEgressWorkers(ctx context.Context, prev map[netip.Addr]auth.EntityRef) map[netip.Addr]auth.EntityRef {
	namespaces := map[v1.NamespaceName]struct{}{}
	for _, kind := range []v1.Kind{v1.KindFunction, v1.KindCatalogService} {
		res, err := p.cfg.store.List(ctx, kind.GVK(), store.ListOptions{})
		if err != nil {
			return prev
		}
		for _, o := range res.Items {
			namespaces[o.GetNamespace()] = struct{}{}
		}
	}
	live := map[netip.Addr]auth.EntityRef{}
	for ns := range namespaces {
		insts, lerr := p.cfg.runtime.List(ctx, ns)
		if lerr != nil {
			continue
		}
		for _, in := range insts {
			if in.State != runtime.StateRunning || in.IP == "" {
				continue
			}
			if in.OwnerKind != v1.KindFunction && in.OwnerKind != v1.KindCatalogService {
				continue
			}
			ip, perr := netip.ParseAddr(in.IP)
			if perr != nil {
				continue
			}
			live[ip] = auth.EntityRef{Type: in.OwnerKind, Namespace: in.Namespace, Name: in.Name}
		}
	}
	for ip, ref := range live {
		if pr, ok := prev[ip]; !ok || pr != ref {
			p.egressWorkers.Add(ip, ref)
		}
	}
	for ip := range prev {
		if _, ok := live[ip]; !ok {
			p.egressWorkers.Remove(ip)
		}
	}
	return live
}

// s3BucketFor builds the s3gateway BucketFor resolver (ADR-0080): it maps an S3
// (namespace, bucket-name) to a prefixed view of the single shared blob substrate,
// resolving ok=true only when a Bucket of that name exists in that namespace. The
// namespacing scheme is a key-prefix view `s3/<ns>/<bucket>/` over the shared bucket —
// one substrate bucket, many logical S3 buckets — so distinct namespaces and buckets
// never collide. Existence-by-namespace here gives tenancy a second guard (a missing /
// cross-namespace bucket is NoSuchBucket); the binding-as-grant Cedar PEP is the
// authorization gate on every object op. The view carries the Bucket's spec.maxObjectBytes,
// so every write path through it (S3 frontend, context.blob, site) enforces that policy.
func s3BucketFor(shared blob.Bucket, st store.Store) func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool) {
	return func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool) {
		if bucket == "" {
			return nil, false
		}
		obj, err := st.Get(context.Background(), v1.KindBucket.GVK(), ns, v1.ObjectName(bucket))
		if err != nil {
			return nil, false
		}
		var maxObjectBytes int64
		if b, ok := obj.(*v1.Bucket); ok {
			maxObjectBytes = b.Spec.MaxObjectBytes
		}
		return blob.Capped(blob.Prefixed(shared, bucketPrefix(ns, v1.ObjectName(bucket))), maxObjectBytes), true
	}
}

// s3Root is the substrate key prefix under which every Bucket's objects live (ADR-0080).
const s3Root = "s3/"

// bucketPrefix is one Bucket's substrate key prefix, s3/<ns>/<bucket>/ (ADR-0080).
func bucketPrefix(ns v1.NamespaceName, bucket v1.ObjectName) string {
	return s3Root + string(ns) + "/" + string(bucket) + "/"
}

// purgePage is the most objects one listing of a Purge returns.
const purgePage = 1000

// bucketPurger is the gc.BucketPurger over a Bucket's raw substrate prefix (ADR-0199 Decision 7). Unlike
// s3BucketFor it resolves no Bucket resource, so it also purges the prefix of a deleted one.
type bucketPurger struct{ shared blob.Bucket }

func (p bucketPurger) Purge(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName) error {
	const op = "funcd.bucketPurger.Purge"
	view := blob.Prefixed(p.shared, bucketPrefix(ns, bucket))
	after := ""
	for {
		items, more, err := view.ListAfter(ctx, "", after, purgePage)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "list bucket %s/%s", ns, bucket)
		}
		for _, it := range items {
			if err := view.Delete(ctx, it.Key); err != nil && fault.KindOf(err) != fault.NotFound {
				return fault.Wrapf(err, fault.KindOf(err), op, "delete %q of bucket %s/%s", it.Key, ns, bucket)
			}
		}
		if !more || len(items) == 0 {
			return nil
		}
		after = items[len(items)-1].Key
	}
}

// reclaimDeletedBuckets purges the prefix of every Bucket that no longer exists, which a crash between a Bucket's
// delete and its second purge leaves behind (ADR-0199 Decision 7). It reads one key per prefix and seeks past the
// rest, as the S3 frontend rolls up a common prefix.
func reclaimDeletedBuckets(ctx context.Context, shared blob.Bucket, st store.Store, logger *slog.Logger) error {
	const op = "funcd.reclaimDeletedBuckets"
	purger := bucketPurger{shared: shared}
	var errs []error
	after := ""
	for {
		items, _, err := shared.ListAfter(ctx, s3Root, after, 1)
		if err != nil {
			return errors.Join(append(errs, fault.Wrapf(err, fault.KindOf(err), op, "list bucket prefixes"))...)
		}
		if len(items) == 0 {
			return errors.Join(errs...)
		}
		key := items[0].Key
		parts := strings.SplitN(strings.TrimPrefix(key, s3Root), "/", 3)
		if len(parts) < 3 {
			after = key
			continue
		}
		ns, name := v1.NamespaceName(parts[0]), v1.ObjectName(parts[1])
		after = max(bucketPrefix(ns, name)+string(utf8.MaxRune), key)
		if ns.Validate() != nil || name.Validate() != nil {
			continue
		}
		_, gerr := st.Get(ctx, v1.KindBucket.GVK(), ns, name)
		if fault.KindOf(gerr) != fault.NotFound {
			if gerr != nil {
				errs = append(errs, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get bucket %s/%s", ns, name))
			}
			continue
		}
		if perr := purger.Purge(ctx, ns, name); perr != nil {
			errs = append(errs, perr)
			continue
		}
		logger.InfoContext(ctx, "reclaimed the objects of a deleted Bucket", "namespace", ns, "bucket", name)
	}
}

// s3Buckets lists a namespace's Bucket resources for the S3 gateway's HeadBucket and
// ListBuckets (ADR-0080).
func s3Buckets(st store.Store) func(ctx context.Context, ns v1.NamespaceName) ([]v1.Bucket, error) {
	return func(ctx context.Context, ns v1.NamespaceName) ([]v1.Bucket, error) {
		l, err := st.List(ctx, v1.KindBucket.GVK(), store.ListOptions{Namespace: ns})
		if err != nil {
			return nil, err
		}
		out := make([]v1.Bucket, 0, len(l.Items))
		for _, o := range l.Items {
			if b, ok := o.(*v1.Bucket); ok {
				out = append(out, *b)
			}
		}
		return out, nil
	}
}

// blobBucketLister adapts the s3BucketFor resolver to the eventing.BucketLister the BlobWatcher polls
// (ADR-0119): it resolves (ns, Bucket) to the SAME prefixed substrate view external S3-frontend writes land
// in, then Lists the prefix over it. A missing Bucket is fault.NotFound (the watcher logs + skips that poll;
// the reconciler independently marks the source NotReady).
type blobBucketLister struct {
	resolve func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool)
}

func (l blobBucketLister) List(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName, prefix string) ([]blob.Attributes, error) {
	b, ok := l.resolve(ns, string(bucket))
	if !ok {
		return nil, fault.NotFoundf("funcd.blobBucketLister", "bucket %q not found in namespace %q", bucket, ns)
	}
	return b.List(ctx, prefix)
}

// blobProber adapts blobBucketLister to admission.BlobProber (ADR-0080 deletion-protection): HasAny reports
// whether any object exists under "<prefix>/" of the Bucket's substrate view.
type blobProber struct{ lister blobBucketLister }

func (p blobProber) HasAny(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName, prefix string) (bool, error) {
	items, err := p.lister.List(ctx, ns, bucket, prefix+"/")
	if err != nil {
		return false, err
	}
	return len(items) > 0, nil
}

// kvProber adapts the kvstore.KV driver's List to admission.KVProber (ADR-0072 deletion-protection):
// HasAny reports whether any key exists under the store's prefix.
type kvProber struct{ kv kvstore.KV }

func (p kvProber) HasAny(ctx context.Context, prefix string) (bool, error) {
	keys, err := p.kv.List(ctx, prefix)
	if err != nil {
		return false, err
	}
	return len(keys) > 0, nil
}

// workerClient is the client the workflow dispatcher (timeout 0: each attempt is bounded by its step's timeout) and
// the Sensor invoker (30 s) call workers with: counted for drain (ADR-0143), and sending the call's deadline in
// X-Funcd-Timeout-Ms so a pool's timer follows it (ADR-0151).
func workerClient(calls *activator.CallTracker, timeout time.Duration) *http.Client {
	return &http.Client{Transport: activator.DeadlineTransport(calls.Wrap(nil)), Timeout: timeout}
}
