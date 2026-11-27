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
	"github.com/pyvvo/funcd/internal/auth/apitoken"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/bus"
	"github.com/pyvvo/funcd/internal/contractcheck"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge"
	"github.com/pyvvo/funcd/internal/edge/authn"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/edge/observ"
	"github.com/pyvvo/funcd/internal/edge/oidc"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/edge/shape"
	"github.com/pyvvo/funcd/internal/edge/static"
	edgetls "github.com/pyvvo/funcd/internal/edge/tls"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/eventstore"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/health"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/lifecycle"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/multinode/agent"
	"github.com/pyvvo/funcd/internal/network"
	"github.com/pyvvo/funcd/internal/network/egress"
	"github.com/pyvvo/funcd/internal/platform/clock"
	platformconfig "github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/nsmaster"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/quota"
	"github.com/pyvvo/funcd/internal/restore"
	"github.com/pyvvo/funcd/internal/route"
	"github.com/pyvvo/funcd/internal/route/owned"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/sensor"
	"github.com/pyvvo/funcd/internal/services"
	addonsvc "github.com/pyvvo/funcd/internal/services/addon"
	backupsvc "github.com/pyvvo/funcd/internal/services/backup"
	blobsvc "github.com/pyvvo/funcd/internal/services/blob"
	identitysvc "github.com/pyvvo/funcd/internal/services/identity"
	kvsvc "github.com/pyvvo/funcd/internal/services/kv"
	rolessvc "github.com/pyvvo/funcd/internal/services/roles"
	"github.com/pyvvo/funcd/internal/site"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/volume"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/internal/workflow"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	wbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// DevToken is the default control-plane credential token wired by InMemory(). It
// lets a test/tool authenticate against an embedded platform; it is NOT a
// production identity story (Production() wires no default token — ADR-0028),
// and New refuses it on an address off loopback (ADR-0314).
const DevToken = "funcd-dev-token"

// DevAdminToken is funcdctl dev's admin token, fixed and documented as DevToken is (ADR-0334 Decision 5). Only a
// platform built WithDevAdminToken answers it, New refuses it off loopback, and no credential list holds it.
const DevAdminToken = "funcd-dev-admin-token"

const (
	defaultProdAddr      = "0.0.0.0:8080"
	defaultDataPlaneAddr = "0.0.0.0:8081" // function-traffic ingress (ADR-0033); intentionally public (auth is V2)
	defaultLocalNode     = "local"
	shutdownTimeout      = 15 * time.Second
	// closeMargin is what Shutdown's bound leaves after the runtime's stop grace: the log Routes and telemetry flush
	// (issue #453), the stop's output wait and the private containerd's sweep (ADR-0277).
	closeMargin = 2 * time.Second
	// defaultCloseGrace stands in for the stop grace of a runtime that reports none (runtime.StopGracer).
	defaultCloseGrace = 3 * time.Second
	// defaultKVStoresPerNamespace is the per-namespace KVStore count cap when unset (ADR-0072).
	defaultKVStoresPerNamespace = 100
	// defaultBucketsPerNamespace is the per-namespace Bucket count cap when unset (ADR-0080).
	defaultBucketsPerNamespace = 100
	// defaultSQLDatabasesPerNamespace is the per-namespace SQLDatabase count cap when unset (ADR-0232).
	defaultSQLDatabasesPerNamespace = 20
	// defaultAddonObjectsPerNamespace is the per-namespace cap on the add-on objects of engine and engineless kinds when
	// unset (ADR-0332 Decision 2); defaultAddonSettingsPerNamespace the cap on settings objects (ADR-0340 Decision 6).
	defaultAddonObjectsPerNamespace  = 20
	defaultAddonSettingsPerNamespace = 200
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
	// How long a client of the platform's two HTTP servers has to send a request.
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 10 * time.Second
	// maxRetentionSweep is the longest wait between two sweeps of terminal runs or dead letters.
	maxRetentionSweep = time.Hour
)

// config holds the injected world — validated by validate() before New returns.
type config struct {
	store   store.Store
	kvStore kvstore.KV // the function-facing KV driver (ADR-0066/0069); nil ⇒ in-memory default
	// kvMaxStoresPerNamespace is the per-namespace KVStore count cap at admission (ADR-0072); 0 ⇒
	// the default (100); negative disables the quota.
	kvMaxStoresPerNamespace int
	// kvSweepInterval is how often the KV service deletes the keys that expired (ADR-0320); 0 ⇒ the default.
	kvSweepInterval time.Duration
	// bucketMaxPerNamespace is the per-namespace Bucket count cap at admission (ADR-0080); 0 ⇒
	// the default (100); negative disables the quota.
	bucketMaxPerNamespace int
	// functionProbeTimeout bounds one readiness, liveness or /health/members probe of a Function's workers
	// (function.Config.ProbeTimeout); 0 ⇒ its default. It has no public option: tests whose probes a local server
	// answers raise it, so a loaded host does not read a ready worker as unready (#1126).
	functionProbeTimeout time.Duration
	// sqlMaxDatabasesPerNamespace is the per-namespace SQLDatabase count cap at admission (ADR-0232); 0 ⇒ the default
	// (20); negative disables the quota.
	sqlMaxDatabasesPerNamespace int
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
	// devAdminToken makes the credential store also answer DevAdminToken as an admin (WithDevAdminToken, ADR-0334).
	devAdminToken bool
	authorizer    auth.Authorizer
	localNode     v1.ObjectName
	// nodePlatform is the platform this node runs (ADR-0145); "" ⇒ v1.HostPlatform().
	nodePlatform v1.OCIPlatform

	// data plane (ADR-0033): the function-invocation listener address.
	dataPlaneAddr string
	// gatewayReadTimeout replaces serverReadTimeout on the data-plane listener; only a test sets it (#90).
	gatewayReadTimeout time.Duration
	// baseDomain is server.baseDomain (ADR-0240); "" ⇒ "localhost".
	baseDomain string

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

	// the edge sign-in (ADR-0335): server.edgePublicScheme and server.edgePublicPort build its callbacks (Decision 9);
	// oidcBackChannel replaces Decision 7's transport to a provider outside funcd, which only a test sets.
	edgePublicScheme string
	edgePublicPort   int
	oidcBackChannel  http.RoundTripper
	// proxies are server.trustedProxies, which name each request's client address (ADR-0354).
	proxies edge.Proxies

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
	s3gwMasterFile     string        // optional; empty ⇒ generate+persist under the data dir
	s3gwDataDir        string        // where the master.key is persisted when no master file is set; empty ⇒ in memory
	s3gwKeyPeriod      time.Duration // set by WithS3KeyRotation (ADR-0270); 0 ⇒ fixed pairs
	masterSecret       []byte        // set by WithMasterSecret: the loaded master, used instead of loading one
	hold               *hold.Hold    // set by WithHold (ADR-0206); nil ⇒ never held
	// blobStoreURL is the blob store's URL (WithBlobStoreURL): a BackupSchedule's target is compared with it
	// (ADR-0222 Decision 6); "" compares with nothing.
	blobStoreURL string
	// workloadBackupLimits bound one listing of a target and one copy (WithWorkloadBackupLimits); a zero field is its default.
	workloadBackupLimits workload.Limits
	// backupClock reads the workload backups' times and backupMeter takes their metrics (ADR-0222); nil ⇒
	// clock.System() and the telemetry's meter.
	backupClock clock.Clock
	backupMeter metric.Meter

	// bridgeAddress is funcd's address on its container bridge (ADR-0238), bound and published by the lake proxies and
	// the SQL change feed, and bound by the DNS forwarder (ADR-0315); "" ⇒ 127.0.0.1.
	bridgeAddress string

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
	// The job.* keys (ADR-0347, WithJobs): the runs holding a worker at once on this node, a run's input and result
	// bytes, and how long an ended run is kept.
	jobMaxRunning   int
	jobPayloadLimit int64
	jobRetention    time.Duration
	// workflowMaxStepsInFlight bounds the function-step calls in flight across all runs (ADR-0146); 0 ⇒ no cap.
	workflowMaxStepsInFlight int
	// workflowContracts overrides the F65 typed-edge ContractResolver (ADR-0098). Empty ⇒ the production
	// OCI-metadata resolver. Set by `funcdctl dev` (ADR-0125) where a from-source step has no OCI artifact
	// to inspect, so the OCI resolver can never resolve a file:// bundle's contract.
	workflowContracts workflow.ContractResolver
	// workflowRuntimes overrides the step-image RuntimeResolver (ADR-0247 Decision 7); nil ⇒ the OCI annotation.
	workflowRuntimes workflow.RuntimeResolver

	// lakeProvider overrides the Lake add-on-provider runtime (ADR-0087). Empty ⇒ the
	// production runtime that supervises the curated duckdb CONTAINER (internal/provider). Set by
	// `funcdctl dev` (ADR-0125) to a process-mode driver that runs the embedded DuckDB+Quack engine
	// as a host subprocess — there are no containers in process-dev.
	lakeProvider provider.Runtime
	// sqlProvider overrides the SQLDatabase engine runtime (ADR-0232); nil ⇒ the production runtime.
	sqlProvider provider.Runtime
	// addonRuntime overrides the add-on engines' runtime, which runs images (ADR-0334, ADR-0344 Decision 9); nil ⇒ the
	// production runtime, which runs them on a containerd server and none on a process server.
	addonRuntime provider.Runtime
	// imageOverrides are the runtime names runtime.containerd.imageOverride maps, which no Addon may ship (ADR-0344
	// Decision 4).
	imageOverrides []string
	// addonRegistries are addons.registries, the registries an Addon's image may come from (ADR-0332 Decision 6);
	// addonMaxObjects is addons.maxObjectsPerNamespace, 0 ⇒ the default (20), negative disables the cap;
	// addonMaxSettings is addons.maxSettingsPerNamespace, 0 ⇒ the default (200) (ADR-0340 Decision 6).
	addonRegistries  []string
	addonMaxObjects  int
	addonMaxSettings int
	// addonAnyRegistry lets an Addon's image come from any registry (WithAnyAddonRegistry, ADR-0334 Decision 5).
	addonAnyRegistry bool
	// addonFollowCurrent makes every add-on object follow its Addon's current revision (WithAddonsFollowingCurrent,
	// ADR-0339 Decision 7).
	addonFollowCurrent bool
	// addonCallTimeout and addonCallPayload bound a Function's call of an engine (WithAddonCallLimits, ADR-0338).
	addonCallTimeout time.Duration
	addonCallPayload int64
	// addonSettingsTimeout bounds a settings Function's call (WithAddonSettingsTimeout, ADR-0341); 0 ⇒ 30 s.
	addonSettingsTimeout time.Duration
	// addonImages reads the images of the add-ons' kinds (WithAddonImages, ADR-0358 Decision 4); nil ⇒ the registries.
	addonImages addonsvc.ImageReader
	// ownRuntimeImages are the images of funcd's own runtimes under dev's opt-in (WithOwnRuntimeImages, ADR-0359).
	ownRuntimeImages map[v1.RuntimeName]string
	// sqlCallTimeout bounds one context.sql call (ADR-0233, sql.callTimeout); 0 ⇒ 30 s.
	sqlCallTimeout time.Duration
	// sqlChangeFeedPort is the port the engines post their changes to (ADR-0235, sql.changeFeedPort); 0 ⇒ 7075 with a
	// bridge address, else a free loopback port bound by New (ADR-0260).
	sqlChangeFeedPort int

	// functionVolumes turns the Functions' volumes on (ADR-0262); nil ⇒ off. functionVolumesEphemeral marks a root that
	// does not outlive the daemon (storage.mode memory), where DataMissing is never set.
	functionVolumes          *volume.Options
	functionVolumesEphemeral bool
	// providerStorage is the root of the add-on engines' durable directories (ADR-0231); "" ⇒ none.
	providerStorage string
	// providerStorageEphemeral is a root that does not outlive the daemon (storage.mode memory), so a missing engine
	// directory is no lost data (ADR-0234 Decision 6).
	providerStorageEphemeral bool

	// lakeExtensionDir is injected as DUCKDB_EXTENSION_DIRECTORY into lake-consumer functions
	// (dev analogue of the prod bundle's duckdb-ext, ADR-0089). Set by `funcdctl dev`; empty in prod.
	lakeExtensionDir string
	// quarantinePeriod is how long the start-up reclaims keep the data no object names (storage.quarantineRetention,
	// ADR-0288); 0 ⇒ platformconfig.DefaultQuarantineRetention. quarantineClock dates the entries and the sweep's
	// cutoff; nil ⇒ the system clock.
	quarantinePeriod time.Duration
	// functionDefaultResources fills what a solo Function's spec.resources leaves empty (ADR-0291); zero sets none.
	functionDefaultResources v1.ResourceSpec
	quarantineClock          clock.Clock
	// lakeCatalogMaxBytes caps one Lake's catalog in the catalog store (lake.catalogMaxSize, ADR-0293); 0 ⇒ the
	// gateway's upload cap alone.
	lakeCatalogMaxBytes int64
	// lakeScratchBytes sizes each Lake engine's scratch, kept as images under scratchRoot and mounted under
	// scratchMountRoot (lake.scratchSize, ADR-0294); scratchRoot "" ⇒ no scratch, as on the process runtime.
	lakeScratchBytes              int64
	scratchRoot, scratchMountRoot string
	// floorFree and floorMinFree are the free-space floor of the data filesystem (storage.minFreeSize, ADR-0296);
	// floorMinFree 0 ⇒ no floor.
	floorFree    quota.FreeFunc
	floorMinFree int64
	// lakeSaveInterval is every Lake engine's catalog save interval (lake.saveInterval, ADR-0274); 0 ⇒ none set: every
	// Lake saves before it answers (ADR-0279).
	lakeSaveInterval time.Duration

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

	// backupRunner is the platform backup runner (ADR-0205, WithPlatformBackup); nil ⇒ none.
	backupRunner *runner.Runner
	// backupParent is the generation a restore loaded (WithBackupParent); nil ⇒ none.
	backupParent *backup.GenRef

	// cluster makes the platform a control plane that worker nodes join (WithCluster, FEAT-0002); nil ⇒ none.
	cluster *ClusterOptions
	// nodeAgent makes the platform a worker node (WithNodeAgent, FEAT-0002); nil ⇒ none.
	nodeAgent *NodeAgentOptions

	// presetBlob and presetBus are what InMemory opened. WithBlob and WithBus close the one they replace, as nothing else
	// would: the platform closes only the drivers it ends up with.
	presetBlob blob.Bucket
	presetBus  bus.Bus
}

// gate is the hold every side-effect runner asks (ADR-0206 Decision 6): hold.Never without WithHold.
func (c *config) gate() hold.Gate {
	if c.hold == nil {
		return hold.Never
	}
	return c.hold
}

// nsGate is the per-namespace hold the per-namespace runners ask (ADR-0222 Decision 8): hold.NeverNamespace without
// WithHold.
func (c *config) nsGate() hold.NamespaceGate {
	if c.hold == nil {
		return hold.NeverNamespace
	}
	return c.hold
}

// callGate is the namespace hold the data plane asks before it serves a call (ADR-0290): hold.NeverCall without
// WithHold.
func (c *config) callGate() hold.CallGate {
	if c.hold == nil {
		return hold.NeverCall
	}
	return c.hold
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
	case c.cluster != nil && c.nodeAgent != nil:
		return fault.Invalidf(op, "a platform is a control plane that worker nodes join or a worker node, not both: "+
			"WithCluster and WithNodeAgent cannot be combined (ADR-0363)")
	}
	if err := c.checkDevTokenBind(); err != nil {
		return err
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

// checkDevTokenBind refuses a store that accepts the built-in DevToken or DevAdminToken on an address off loopback: the
// control plane's, and the data plane's when the edge reads the same credentials (ADR-0314 Decisions 5 and 6, ADR-0334
// Decision 5). An empty address is judged as what it binds.
func (c *config) checkDevTokenBind() error {
	const op = "funcd.New"
	for _, b := range []struct{ token, name, wayOut string }{
		{DevToken, "DevToken", "pass WithCredentials, WithDevAuth with a token of your own, or a loopback address"},
		{DevAdminToken, "DevAdminToken", "leave out WithDevAdminToken, or use a loopback address"},
	} {
		if _, err := c.credentials.Lookup(context.Background(), b.token); err != nil {
			continue
		}
		if addr := cmp.Or(c.listenAddr, defaultProdAddr); !platformconfig.IsLoopbackAddr(addr) {
			return fault.Invalidf(op, "WithListenAddr %q is off loopback, and the built-in %s works only on loopback: %s", addr, b.name, b.wayOut)
		}
		if addr := cmp.Or(c.dataPlaneAddr, defaultDataPlaneAddr); c.edgeAuthEnabled && !platformconfig.IsLoopbackAddr(addr) {
			return fault.Invalidf(op, "WithDataPlaneAddr %q is off loopback, WithEdgeAuth is on, and the built-in %s works only on loopback: %s",
				addr, b.name, b.wayOut)
		}
	}
	return nil
}

// Platform is the assembled funcd runtime — the composition root built by New.
type Platform struct {
	cfg          *config
	quotas       *quota.Book          // the store quotas every capped write asks (ADR-0292)
	checker      quota.Checker        // what every storage write asks: the quotas, behind the free-space floor (ADR-0296)
	floor        *quota.Floor         // the free-space floor; nil ⇒ none
	fnReconciler *function.Reconciler // the Function reconciler; Shutdown removes its private pool manifest dir
	logger       *slog.Logger
	providers    *provider.Catalog // the platform provider catalog (ADR-0082)

	controller  *controller.Controller
	collector   *gc.Collector      // ADR-0170: the owner garbage collector, run beside the controller
	cluster     *multinode.Cluster // FEAT-0002: the node API of a control plane that worker nodes join; nil ⇒ none
	agent       *agent.Agent       // FEAT-0002: the agent of a worker node; nil ⇒ none
	eventFanout *eventing.Fanout   // ADR-0108: the named-event publisher the F69 Sensor subscribes to
	edgeRouter  router.Router      // ADR-0110 (F79): the Route matcher the data-plane front door consults
	tlsProvider edgetls.Provider   // ADR-0111 (F74): the TLS cert provider (nil ⇒ plaintext)
	activator   *activator.Activator
	httpServer  *http.Server
	listener    net.Listener
	addr        string

	routeReconciler *route.Reconciler      // Sets the full Route table once before the controller runs (ADR-0176)
	backups         *backupsvc.Reconcilers // ADR-0222: the workload backups, whose runs Run waits for
	prober          *health.Prober         // the storage probes (ADR-0215), started before the controller runs

	dataPlaneServer   *http.Server // function-invocation listener (ADR-0033)
	dataPlaneListener net.Listener
	bridgeListen      netip.AddrPort // the edge's own listener on the bridge address (ADR-0353 Decision 1); invalid ⇒ none
	dataPlaneAddr     string

	workflowRuns      runstate.Store          // durable workflow run state (ADR-0094); closed on shutdown
	workflowSweeper   *workflow.RunReconciler // the run reconciler (ADR-0094); drives the retention sweep
	workflowEngine    *workflow.Engine        // owns the run goroutines; drained on shutdown (ADR-0146)
	workflowRetention time.Duration           // terminal-run retention horizon (0 ⇒ no sweep)

	jobRuns *function.JobRunReconciler // the JobRun reconciler (ADR-0347): Boot, the retention sweep, the executors
	// jobStarts and jobGrants are the Job facet's start and its binding and Cedar check (ADR-0349), set before the
	// Executor is built.
	jobStarts *jobRunStarter
	jobGrants jobGrants

	eventStore *eventstore.Store // eventing's durable store (ADR-0201): dead letters + seen lists; closed on shutdown
	phases     *lifecycle.Phases // the providers in boot order, the Executor first
	exec       *executor         // the Executor provider
	kv         *kvProvider
	blob       *blobProvider
	sql        *sqlProvider
	lake       *lakeProvider
	addon      *addonProvider
	eventing   *eventingProvider
	secrets    *secretsProvider
	invoke     *invokeProvider

	drainTimeout time.Duration // server.shutdownTimeout: bounds the HTTP, workflow-run and Sensor drains and a failed New's cleanup
	shutdownOnce sync.Once
	shutdownErr  error
}

// frugalStores is the production side of SetFrugalStoresForTest: set, New opens the in-memory event and run stores
// with Config.Frugal. A server never sets it.
var frugalStores atomic.Bool //nolint:gochecknoglobals // set only by SetFrugalStoresForTest

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
		jobMaxRunning:        platformconfig.DefaultJobMaxRunning,
		jobPayloadLimit:      platformconfig.DefaultJobPayloadLimit,
		jobRetention:         platformconfig.DefaultJobRetention,
		deadletterRetention:  defaultDeadletterRetention,
		deadletterMaxEntries: defaultDeadletterMaxEntries,
		invokeDefaultTimeout: v1.DefaultInvokeTimeout,

		workflowMaxStepsInFlight: defaultWorkflowMaxStepsInFlight,
	}
	p := &Platform{cfg: cfg, drainTimeout: shutdownTimeout}
	// The Executor is listed before anything is built, so a New that fails still closes the runtime its options opened
	// (#94).
	p.exec, p.kv, p.blob, p.sql, p.lake = &executor{cfg: cfg}, &kvProvider{cfg: cfg}, &blobProvider{cfg: cfg},
		&sqlProvider{cfg: cfg}, &lakeProvider{cfg: cfg}
	p.addon = &addonProvider{cfg: cfg}
	p.eventing, p.secrets, p.invoke = &eventingProvider{cfg: cfg}, &secretsProvider{cfg: cfg}, &invokeProvider{cfg: cfg}
	p.phases = lifecycle.New(append(append([]lifecycle.Provider{p.exec, p.kv, p.blob, p.sql, p.lake, p.addon, p.eventing,
		p.secrets}, hookless()...), p.invoke)...)
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
	if cfg.devAdminToken && cfg.credentials != nil {
		cfg.credentials = devAdminCredentials{next: cfg.credentials}
	}
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

	pc, err := providerCatalog(p.phases)
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
	p.quotas = quota.NewBook(clock.System(), p.logger)
	p.checker = p.quotas
	if c.floorMinFree > 0 {
		p.floor = quota.NewFloor(c.floorFree, c.floorMinFree, clock.System(), p.logger)
		p.checker = p.floor.Guard(p.quotas)
	}
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

	// FEAT-0002: a worker node schedules under its own name, the one it joined under.
	if c.nodeAgent != nil {
		a, aerr := agent.New(agent.Config{Dir: c.nodeAgent.Dir, Store: c.store, Logger: p.logger,
			Connector: nodeConnector(), Version: c.nodeAgent.Version, Platform: string(c.nodePlatform)})
		if aerr != nil {
			return fault.Wrapf(aerr, fault.KindOf(aerr), op, "load the worker node's identity")
		}
		p.agent, c.localNode = a, a.Name()
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
	// A worker's fn-to-fn invoke (ADR-0064) forwards through the data-plane handler built below, so the local API's
	// Invoker reaches it through a holder set once it exists.
	dpHolder := &local.HandlerHolder{}
	// The KV driver (config-selected, ADR-0066) defaults to in-memory.
	if c.kvStore == nil {
		c.kvStore = kvmemory.New()
	}
	// Cedar PDP driver (ADR-0074): the per-object authorization engine the KV facade (PEP) calls for
	// kv::read/kv::write. Entities are materialized per call from the metastore (principal Function +
	// resource KVTable + parent KVStore); policies are the v1.Policy resources, compiled + cached
	// (recompiled on a store-revision change). DEFAULT-DENY — a read needs a permitting Policy; the
	// owner-write forbid is built in. rbac still decides control-plane CRUD (c.authorizer, unchanged).
	// The capability registry (ADR-0116): the three migrated capabilities (kv, invoke, s3) + the two
	// principal sources (Function, Lake; each resolves its own type) registered here at the composition
	// root. The schema vocabulary, the built-in PolicySet, and this composite EntityProvider are all
	// assembled from the registered set — a new capability (egress next) registers with no shared edit.
	// ADR-0136: a store-backed WriterLister feeds writer-role RolesAssignment grants into the s3/kv
	// single-writer `writers` set (so an external Identity can be granted write).
	writerLister := rolessvc.NewLister(c.store)
	s3Cap := cedarauth.S3CapabilityWithWriters(writerLister)
	cedarRegistry, err := cedarauth.NewRegistry(
		// ADR-0137 (F102): LakeCapability makes the live PDP understand lake::query; the per-Lake
		// PEP proxy that calls it is not yet wired here — see TODO(ADR-0137) in internal/services/lake/reconcile.go.
		[]cedarauth.Capability{cedarauth.KVCapabilityWithWriters(writerLister), cedarauth.InvokeCapability(), s3Cap, cedarauth.EgressCapability(), cedarauth.LakeCapability(),
			cedarauth.SQLCapabilityWithWriters(writerLister), cedarauth.SecretCapabilityWithWriters(writerLister),
			cedarauth.AddonCapability(), cedarauth.JobCapability()},
		[]cedarauth.PrincipalSource{cedarauth.FunctionPrincipalSource(), cedarauth.JobPrincipalSource(),
			cedarauth.LakePrincipalSource(), cedarauth.AddonObjectPrincipalSource()},
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
	// ADR-0321 Decision 3 (superseding ADR-0310 Decision 6's "over the config alone"): secrets is built once the PDP
	// exists, still before every other provider, since lake, the Function reconciler and the workload backups read its
	// resolver. A workload's read (a principal on its identity) asks the PDP secret::read; a coarse read (the start
	// check, the backups) still goes to the control plane's authorizer.
	secretsAuthz, err := auth.NewRoutingAuthorizer(c.authorizer, cedarPDP)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build secrets authorizer")
	}
	if err := p.secrets.build(secretsAuthz, p.agent); err != nil {
		return err
	}
	// ADR-0215: the storage probes read a sentinel key of the KV engine and the blob store; the dependency check the
	// local API answers reads their results, the Facades' resolve and read authorization, the link resolver and the PDP.
	probes := map[health.Target]health.Probe{health.TargetKV: health.KVProbe(c.kvStore)}
	if c.blob != nil {
		probes[health.TargetBlob] = health.BlobProbe(c.blob)
	}
	p.prober, err = health.NewProber(clock.System(), orDefault(c.pacing.StorageProbeInterval, defaultStorageProbeInterval),
		orDefault(c.pacing.StorageProbeTimeout, defaultStorageProbeTimeout), probes, p.logger)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build storage prober")
	}
	// The node master secret (ADR-0085): the SAME 32-byte key the S3 gateway derives per-function
	// SigV4 keypairs from, the lake PEP proxy derives/verifies per-function lake tokens with
	// (ADR-0137) and the SQL engines' passwords come from (ADR-0232). Loaded ONCE here — before the SQL
	// facade, the s3gw and the lake wiring — so they share one master (LoadOrCreateMaster is
	// deterministic per file/dir, but loading twice risks generating two different keys on a first run).
	// Never logged.
	master := c.masterSecret
	if master == nil {
		if merr := s3gateway.MigrateMaster(c.s3gwMasterFile, c.s3gwDataDir, c.s3gwEnabled, p.logger); merr != nil {
			return fault.Wrapf(merr, fault.KindOf(merr), op, "migrate node master secret")
		}
		var merr error
		if master, merr = s3gateway.LoadOrCreateMaster(c.s3gwMasterFile, c.s3gwDataDir); merr != nil {
			return fault.Wrapf(merr, fault.KindOf(merr), op, "load node master secret")
		}
	}
	// FEAT-0002 F248: the API and the edge take the API tokens kept in the metastore too.
	c.credentials = middleware.ChainCredentials(c.credentials, apitoken.NewCredentials(c.store, nil))
	// FEAT-0002 decision 8: a worker node derives each namespace's credentials from that namespace's own master.
	var masters nsmaster.Source
	if p.agent != nil {
		masters = p.agent.NamespaceMaster
	}
	if c.cluster != nil {
		cl, cerr := p.newCluster(c.cluster, c.store, c.localNode, master)
		if cerr != nil {
			return fault.Wrapf(cerr, fault.KindOf(cerr), op, "build the node API")
		}
		p.cluster = cl
	}
	ctrl, err := controller.New(controller.Deps{Store: c.store, Logger: p.logger, RetryBackoffMax: c.pacing.RetryBackoffMax,
		Skip: p.elsewhere})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build controller")
	}
	p.edgeRouter = router.New() // ADR-0110 (F79): shared by the Route reconciler + the data-plane handler
	// Edge-route aggregator (ADR-0138): the SINGLE sole-writer of p.edgeRouter's replace-all table. The
	// Route reconciler (user Routes) and the Lake reconciler (its node-private lake::query
	// PEP proxy entry) each Set only their own source partition; the aggregator unions all sources into
	// one Program call — so an exposed lake coexists with user Routes instead of clobbering them. The
	// data-plane still reads p.edgeRouter directly (Resolve); only writes go through the aggregator.
	// ADR-0176: it arbitrates every source's claims against the namespace exposure modes and re-runs the
	// reconciler of each owner whose verdict another source's Set changed.
	edgeAgg := router.NewAggregator(p.edgeRouter,
		func(ctx context.Context, ns v1.NamespaceName) (router.NamespaceDefaults, error) {
			obj, err := c.store.Get(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
			if err != nil {
				return router.NamespaceDefaults{}, err
			}
			n, ok := obj.(*v1.Namespace)
			if !ok {
				return router.NamespaceDefaults{}, fault.Internalf(op, "namespace %q is a %T", ns, obj)
			}
			d := router.NamespaceDefaults{Mode: n.Spec.DefaultExposure.Normalized()}
			if e := n.Spec.EdgeDefaults; e != nil && e.Auth != nil {
				d.OIDC = router.OIDCOf(e.Auth.RouteAuth())
			}
			return d, nil
		},
		func(o router.Owner) {
			ctrl.Enqueue(controller.Request{GVK: o.Kind.GVK(), Namespace: o.Namespace, Name: o.Name})
		},
		c.baseDomain, p.logger)
	// ADR-0108: an EventSource firing PUBLISHES a named CloudEvent onto the in-process Fanout; the F69
	// Sensor subscribes to it (the action side — invoke/start-workflow — moved off the Source).
	fanout := eventing.NewFanout()
	p.eventFanout = fanout
	// ADR-0201: the event store holds the Sensor's dead letters and the BlobWatcher's seen lists, on disk when
	// deadletterDataDir is set, else in memory. On disk, the seen lists an earlier release kept in the KV move in
	// before the BlobWatcher is built; a failed move fails the start and keeps the KV records for the next one.
	es, err := eventstore.Open(eventstore.Config{InMemory: c.deadletterDataDir == "", Frugal: frugalStores.Load(), Dir: c.deadletterDataDir})
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
	// ADR-0309 Decision 3: the other providers build their parts over the Platform's, kv, blob, sql and lake in list
	// order, then invoke, whose links check reads the PDP; the Executor comes after them and the dependency check.
	if err := p.kv.build(kvDeps{authz: cedarPDP, quota: p.checker, prober: p.prober, ctrl: ctrl}); err != nil {
		return err
	}
	if err := p.blob.build(blobDeps{authz: cedarPDP, quota: p.checker, master: master, masters: masters,
		prober: p.prober, ctrl: ctrl}); err != nil {
		return err
	}
	if err := p.sql.build(sqlDeps{authz: cedarPDP, quota: p.checker, master: master, masters: masters, ctrl: ctrl, fanout: fanout,
		events: es, engineLine: p.exec.keepDevEngineLine}); err != nil {
		return err
	}
	if err := p.lake.build(lakeDeps{authz: cedarPDP, master: master, masters: masters, floor: p.floor, secrets: p.secrets.resolver,
		routes: edgeAgg, s3: p.blob.injection, ctrl: ctrl, engineLine: p.exec.keepDevEngineLine}); err != nil {
		return err
	}
	// the collector is built below, before any pass runs: an App's last engineless part asks it for a namespace pass
	collect := func(ctx context.Context, ns v1.NamespaceName) error {
		if p.collector == nil {
			return nil
		}
		return p.collector.CollectNamespace(ctx, ns)
	}
	if err := p.addon.build(addonDeps{secrets: p.secrets.resolver, routes: edgeAgg, ctrl: ctrl,
		indexEngines: p.exec.indexAddonEngines, authz: cedarPDP, master: master, masters: masters,
		follows: p.agent != nil, engineLine: p.exec.keepDevEngineLine, collect: collect}); err != nil {
		return err
	}
	p.exec.addonNamespaces = p.addon.engineNamespaces
	if err := p.invoke.build(invokeDeps{authz: cedarPDP}); err != nil {
		return err
	}
	// ADR-0308 Decision 3: every store's facade exists and the default KV driver is set (#1370), so the providers'
	// quotas go in the Book and their bindings into the dependency check, in list order.
	p.registerQuotas()
	deps := health.NewChecker(health.CheckerDeps{Store: c.store, Bindings: p.dependencyChecks()})
	nestedCap := c.nestedInFlightCap
	if nestedCap == 0 {
		nestedCap = defaultNestedInFlightCap
	}
	invoker := local.NewNestedCapInvoker(local.NewInvoker(dpHolder), nestedCap, invokeMeter(c.telemetry))
	// The Executor is built once the providers are: a worker's local API serves their facets, in list order (ADR-0310),
	// the Job facet last, whose starter gets the control plane's admissions once they are built, below (ADR-0349).
	p.jobStarts, p.jobGrants = &jobRunStarter{store: c.store}, jobGrants{store: c.store, authz: cedarPDP}
	if err := p.exec.build(execDeps{authz: cedarPDP, floor: p.floor, invoker: invoker, checks: deps,
		facets: p.facets()}); err != nil {
		return err
	}

	// ADR-0143: one tracker counts every call to a worker — the activator's proxy, the Sensor invoker and the workflow
	// dispatcher send through it — so the reconciler can tell when a demoted revision's worker is drained.
	calls := activator.NewCallTracker(clock.System())
	fnReconciler, err := function.NewReconciler(function.Deps{
		Store:                c.store,
		QuarantinePeriod:     c.quarantinePeriodOrDefault(),
		DefaultResources:     c.functionDefaultResources,
		LogMaxRecordBytes:    cmp.Or(c.funclogMaxRecordBytes, funclog.DefaultMaxRecordBytes),
		Calls:                calls,
		InvokeSockets:        p.exec.sockets,
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
		OwnImages:            c.ownRuntimeImages,
		Inspector:            jobContracts(c.workflowContracts),
		BootBackoffInitial:   c.bootBackoffInitial,
		BootBackoffMax:       c.bootBackoffMax,
		SupervisionPeriod:    c.pacing.SupervisionPeriod,
		BootTimeout:          c.pacing.BootTimeout,
		LivenessTimeout:      c.pacing.livenessTimeout(),
		ReadinessTimeout:     c.pacing.ReadinessTimeout,
		DrainGrace:           c.pacing.DrainGrace,
		HandOutSettle:        c.pacing.HandOutSettle,
		DrainPollInterval:    c.pacing.DrainPollInterval,
		ReferentPollInterval: c.pacing.ReferentPollInterval,
		ProbeTimeout:         c.functionProbeTimeout,
		PoolShimCommand:      c.poolShim,
		PoolShimsByFamily:    c.poolShimsByFamily,
		PoolManifestDir:      c.poolManifestDir,
		PoolLimit:            c.poolLimit,
		S3Gateway:            p.blob.injection,
		LakeMaster:           master, // ADR-0137: per-function lake token derivation (same master as S3)
		AddonProxy:           p.addon.proxyFor(),
		NamespaceMasters:     masters,
		LakeExtensionDir:     c.lakeExtensionDir,
		LakeProxies:          p.lake.proxy,
		Secrets:              p.secrets.resolver,
		Volumes:              p.exec.volumes,
		VolumesDurable:       p.exec.volumes != nil && !c.functionVolumesEphemeral,
		Hold:                 c.gate(),
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build function reconciler")
	}
	p.exec.captureLogs(fnReconciler) // a pool worker's lines go to the members the reconciler names
	p.exec.useFunctions(fnReconciler)
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

	if p.collector, err = gc.New(gc.Deps{Store: c.store, Purger: bucketPurger{shared: c.blob}, Interval: c.gcSweepInterval,
		Logger: p.logger, Skip: p.elsewhere}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build garbage collector")
	}
	ctrl.Register(v1.KindFunction.GVK(), fnReconciler)
	p.fnReconciler = fnReconciler
	// ADR-0347 Decision 2: a Job is reconciled on the Function's deps; a run that ends lets its Job collect the revision
	// it pinned, and a change to what a Job defines at its root re-runs the Job, which writes it back
	jobs := fnReconciler.Jobs()
	ctrl.Register(v1.KindJob.GVK(), jobs)
	ctrl.Watches(v1.KindJobRun.GVK(), jobs.MapRun)
	// ADR-0344 Decisions 7 and 11: an Addon's change requeues the Functions and Jobs whose runtime an Addon may ship
	ctrl.Watches(v1.KindAddon.GVK(), fnReconciler.MapAddon)
	ctrl.Watches(v1.KindAddon.GVK(), jobs.MapAddon)
	// a held revision's wake or reclaim is written to its Revision's status (ADR-0190 Decision 5)
	ctrl.Watches(v1.KindRevision.GVK(), fnReconciler.MapRevision)
	ctrl.Watches(v1.KindRestore.GVK(), fnReconciler.MapRestore) // a Restore into a volume clears DataMissing (ADR-0262)
	// a change to what a namespace grants its Functions can move a pooled one to another pool
	for _, k := range function.AccessKinds() {
		ctrl.Watches(k.GVK(), fnReconciler.MapAccess)
	}
	ctrl.Watches(v1.KindFunction.GVK(), fnReconciler.MapPoolDisplaced) // an asleep member a newcomer displaces (ADR-0193)
	// ADR-0248 Decision 3: a change to what a Function defines at its root re-runs that Function, which writes it back
	for _, k := range []v1.Kind{v1.KindKVStore, v1.KindBucket, v1.KindSQLDatabase, v1.KindLake, v1.KindConfigMap} {
		ctrl.Watches(k.GVK(), ownedstore.MapOwner(c.store, v1.KindFunction))
		ctrl.Watches(k.GVK(), ownedstore.MapOwner(c.store, v1.KindJob))
	}
	ctrl.Register(v1.KindService.GVK(), dispatcher)
	// ADR-0302: one invokePlugin chain, which every call to a worker runs: the data plane's, the workflow
	// dispatcher's, the Sensors' and the App hooks'. ADR-0113 (F77): its authn plugin is the edge PEP, built from the
	// control-plane credentials + authorizer; nil unless enabled (an authenticated stance then fails closed).
	var edgeEnforcer *authn.Enforcer
	if c.edgeAuthEnabled {
		edgeEnforcer, err = authn.New(authn.Deps{Creds: c.credentials, Authz: c.authorizer})
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "build edge authn PEP")
		}
	}
	targetLimiter := limit.NewTargetLimiter(c.limits)
	// ADR-0303: the contract plugins come last, so they check the envelope the data plane built.
	callChain := invoke.NewChain(append(dataplane.Plugins(edgeEnforcer, targetLimiter, c.callGate(), c.invokeDefaultTimeout),
		contractcheck.Plugins(fnReconciler)...)...)
	// ADR-0347 Decisions 4 to 8: the runs' one status writer, still under the platform's and the namespaces' holds, whose
	// executors call through the chain; a Job's change reaches its runs, and a run that ends frees a slot for those that
	// wait
	runsCfg := function.RunsConfig{MaxRunning: c.jobMaxRunning, PayloadLimit: c.jobPayloadLimit,
		Retention: c.jobRetention, Enqueue: ctrl.Enqueue, Hold: c.gate(), NamespaceHold: c.nsGate(), Chain: callChain,
		Client: workerClient(calls, 0), Skip: p.elsewhere}
	if p.exec.egressWorkers != nil {
		runsCfg.Egress = p.exec.egressWorkers // a run's worker is indexed as its Job before its one call (ADR-0351 Decision 6)
	}
	p.jobRuns = fnReconciler.Runs(runsCfg)
	ctrl.Register(v1.KindJobRun.GVK(), p.jobRuns)
	ctrl.Watches(v1.KindJob.GVK(), p.jobRuns.MapJob)
	ctrl.Watches(v1.KindJobRun.GVK(), p.jobRuns.MapRun)
	// ADR-0309: eventing builds its parts once the workload core exists, since its Sensors call through the Function
	// reconciler's endpoints, the activator and the call chain.
	if err := p.eventing.build(eventingDeps{fanout: fanout, events: es, quota: p.checker, endpoints: fnReconciler.Endpoints(),
		waker: act, calls: calls, chain: callChain, drainTimeout: p.drainTimeout, ctrl: ctrl}); err != nil {
		return err
	}
	// ADR-0110 (F79): the Route reconciler validates backends + multi-tenancy rules and programs the
	// edge router (replace-all) the data-plane front door consults. p.edgeRouter is created up front so
	// both the reconciler and the data-plane handler share the one live table.
	routeReconciler, err := route.NewReconciler(route.Deps{
		Store: c.store, Routes: edgeAgg, Logger: p.logger,
		ReferentPollInterval: c.pacing.ReferentPollInterval, ResyncInterval: c.pacing.RouteResyncInterval, Skip: p.elsewhere,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build route reconciler")
	}
	ctrl.Register(v1.KindRoute.GVK(), routeReconciler)
	p.routeReconciler = routeReconciler
	// ADR-0139 (F103): the Site reconciler materializes a site bundle under a digest-scoped prefix of the
	// SAME per-namespace Bucket view the S3 frontend and the static handler use (s3BucketFor), then owns
	// the Bucket + Route it declares inline; its status is derived from the owned Route.
	siteReconciler := site.New(site.Deps{Store: c.store, Buckets: s3BucketFor(c.blob, c.store, p.checker), DefaultIndex: c.siteDefaultIndex, Logger: p.logger, ReferentPollInterval: c.pacing.ReferentPollInterval})
	ctrl.Register(v1.KindSite.GVK(), siteReconciler)
	ctrl.Watches(v1.KindRoute.GVK(), owned.MapOwner(c.store, v1.KindSite)) // status is derived from the owned Route
	// ADR-0241 Decision 5: a Function's IngressReady follows the Route of its own address
	ctrl.Watches(v1.KindRoute.GVK(), owned.MapOwner(c.store, v1.KindFunction))
	ctrl.Watches(v1.KindBucket.GVK(), siteReconciler.MapBucket)       // a raised maxObjectBytes lets a refused bundle deploy
	ctrl.Watches(v1.KindConfigMap.GVK(), siteReconciler.MapConfigMap) // a changed ConfigMap re-renders config.json (ADR-0239)
	// ADR-0215 Decision 7: a probe flip re-runs every KVStore or Bucket.
	p.prober.OnChange(func(t health.Target, _ health.Result) {
		kind := v1.KindKVStore
		if t == health.TargetBlob {
			kind = v1.KindBucket
		}
		enqueueAll(context.Background(), c.store, ctrl, kind, p.logger)
	})
	ctrl.Watches(v1.KindLake.GVK(), fnReconciler.MapLake) // ADR-0162: a consumer's status follows its lake

	// ADR-0135 (F100): the Identity credential-issuing reconciler — issues a keypair→owned Secret and
	// registers it (via storeExternalKeys, wired into s3gateway.Deps.External above).
	identityReconciler, err := identitysvc.NewReconciler(identitysvc.ReconcilerDeps{Store: c.store, Logger: p.logger, SupervisionPeriod: c.pacing.SupervisionPeriod})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build Identity reconciler")
	}
	ctrl.Register(v1.KindIdentity.GVK(), identityReconciler)
	p.registerSignIn(ctrl, fnReconciler)

	// Workflow engine (ADR-0094): durable run state (Badger at workflowDataDir; in-memory when
	// unset — the InMemory preset / tests), a step dispatcher over the activator's endpoints +
	// waker (fail-closed to targets that resolve to a real Function), and two reconcilers — the
	// Workflow reconciler materializes the owned step Function/KVStore fleet, the WorkflowRun
	// reconciler drives a run through the engine and mirrors its status + status.runs link.
	runs, rerr := wbadger.New(wbadger.Config{InMemory: c.workflowDataDir == "", Frugal: frugalStores.Load(), Dir: c.workflowDataDir})
	if rerr != nil {
		return fault.Wrapf(rerr, fault.KindOf(rerr), op, "build workflow run store")
	}
	p.workflowRuns = runs
	wfDispatcher, derr := workflow.NewHTTPDispatcher(workflow.DispatchDeps{
		Endpoints: fnReconciler.Endpoints(),
		Waker:     act, // wake a scaled-to-zero step function (ADR-0033)
		Protocol:  protocolOf(c.store),
		Chain:     callChain,
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
		Traces:   p.exec.traces(),        // ADR-0104: the engine emits the run-root span for inline sub-workflow child runs
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
	var wfRuntimes workflow.RuntimeResolver = runtimeResolver{}
	if c.workflowRuntimes != nil {
		wfRuntimes = c.workflowRuntimes
	}
	wfMaterializer := workflow.NewMaterializer(c.store, wfRuntimes, p.logger, c.pacing.SupervisionPeriod)
	wfContracts := workflow.ContractResolver(contractResolver{})
	if c.workflowContracts != nil {
		wfContracts = c.workflowContracts
	}
	ctrl.Register(v1.KindWorkflow.GVK(), workflow.NewWorkflowReconciler(c.store, wfMaterializer, wfContracts, p.logger, c.pacing.ArtifactPollInterval))
	p.workflowSweeper = workflow.NewRunReconciler(c.store, wfEngine, p.exec.traces(), p.logger, c.pacing.ReferentPollInterval)
	p.workflowSweeper.SetHold(c.gate())
	p.workflowSweeper.SetNamespaceHold(c.nsGate())
	p.workflowSweeper.SetSkip(p.elsewhere)
	ctrl.Register(v1.KindWorkflowRun.GVK(), p.workflowSweeper)
	// ADR-0199: the App reconciler writes an App's parts; a change to a part it controls or marks, or to an
	// object a ref entry names, requeues the App.
	appReconciler, err := app.NewReconciler(app.Deps{
		Store: c.store, Purger: bucketPurger{shared: c.blob}, Logger: p.logger, Clock: clock.System(),
		UpgradeTimeout: c.pacing.appUpgradeTimeout(), RevisionHistory: c.appRevisionHistory,
		SupervisionPeriod: c.pacing.SupervisionPeriod, Hold: c.gate(), NamespaceHold: c.nsGate(),
		// ADR-0214: a second invoker for the hook calls, with no client timeout: the context carries the hook
		// Function's spec.timeout or invoke.defaultTimeout, as the workflow dispatcher bounds a step.
		Invoker: &sensor.HTTPInvoker{Endpoints: fnReconciler.Endpoints(), Waker: act, Protocol: protocolOf(c.store),
			Client: workerClient(calls, 0), Chain: callChain, Caller: invoke.AppHook},
		InvokeTimeout: c.invokeDefaultTimeout,
		Enqueue:       ctrl.Enqueue,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build App reconciler")
	}
	ctrl.Register(v1.KindApp.GVK(), appReconciler)
	ctrl.Watches(v1.KindFunction.GVK(), appReconciler.MapStepFunction) // a Workflow part waits for its step Functions (ADR-0215)
	// ADR-0341: a settings kind's Function is called from its target's pass through an invoker of the hooks' kind,
	// bounded by addons.settingsTimeout; a Function's change requeues the settings objects it applies and their targets.
	if rec := p.addon.reconciler; rec != nil {
		rec.SetSettingsCaller(addonSettingsCaller{inv: &sensor.HTTPInvoker{Endpoints: fnReconciler.Endpoints(), Waker: act,
			Protocol: protocolOf(c.store), Client: workerClient(calls, 0), Chain: callChain, Caller: invoke.AddonSettings}},
			ctrl.Enqueue, c.addonSettingsTimeout)
		ctrl.Watches(v1.KindFunction.GVK(), rec.MapFunction)
		// ADR-0345: an engineless kind's Function gets each group's list the same way; a Function's change requeues the
		// objects it reconciles, and a JobRun's end makes its namespace's groups due
		rec.SetObjectsCaller(addonObjectsCaller{inv: &sensor.HTTPInvoker{Endpoints: fnReconciler.Endpoints(), Waker: act,
			Protocol: protocolOf(c.store), Client: workerClient(calls, 0), Chain: callChain, Caller: invoke.AddonObjects}})
		ctrl.Watches(v1.KindFunction.GVK(), rec.MapEnginelessFunction)
		ctrl.Watches(v1.KindJobRun.GVK(), rec.MapJobRun)
	}
	for _, pair := range gc.Pairs() {
		if pair.Owner == v1.KindApp && pair.Child != v1.KindAppRevision { // a record, not a part (ADR-0200)
			ctrl.Watches(pair.Child.GVK(), appReconciler.MapPart)
		}
	}
	ctrl.Watches(v1.KindSecret.GVK(), appReconciler.MapSecret) // ADR-0213: a declared Secret created, changed or deleted
	ctrl.Watches(v1.KindApp.GVK(), appReconciler.MapRequires)  // ADR-0219: an App's dependents and the Apps it requires
	// ADR-0222: an app owner's backups, run with the platform's rights on the shared stores.
	if err := p.registerWorkloadBackups(ctrl, p.secrets.resolver); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build the workload backup reconcilers")
	}
	p.controller = ctrl

	// partAdmissions are the admissions a direct write passes, over the reader r: the control plane runs them over
	// the store, and app-parts over a view that already holds an App's other parts (ADR-0199 Decision 3).
	partAdmissions := func(r admission.StoreReader) []admission.Admission {
		// ADR-0064 fn-to-fn link rules on the write path, then each store provider's resource rules.
		links := []admission.Admission{admission.NewLinkValidityAdmission(r), admission.NewLinkDeletionProtectionAdmission(r)}
		all := append(append(links, p.providerAdmissions(r)...),
			// ADR-0086/0091 lake blob + consumer-binding EXISTENCE were write-time gates; now reconcile-time
			// (ADR-0121): the Lake / Function reconcilers wait for the referent (Waiting condition).
			// ADR-0139 Site: spec.bindings.blob[0].prefix is immutable on Update (the prefix permanently holds the site's bundles).
			admission.NewSitePrefixImmutableAdmission(),
			// ADR-0074 Policy validity: spec.cedar parses + references only the actions and entity types of the
			// registered capabilities (ADR-0116) — so every stored Policy compiles.
			admission.NewPolicyValidityAdmission(),
			// ADR-0352 Decision 1: an EgressPolicy item of an add-on kind names a kind an Addon registers for engines.
			admission.NewEgressAppliesToAdmission(r),
			// ADR-0248 Decision 2: a binding names what its root declares; a Function's own stores default their
			// owner to it and its config bindings name the stored ConfigMaps.
			admission.NewBindingDeclaredAdmission(r),
			admission.NewFunctionStoreOwnerAdmission(),
			// ADR-0262: volumes off refuse a Function with one; on, its local volumes are booked at apply.
			admission.NewFunctionVolumesAdmission(p.volumeBooker()),
			admission.NewLakeStoreOwnerAdmission(),
			// ADR-0094 WorkflowRun payload cap: spec.input ≤ payloadLimit (larger data by reference).
			admission.NewWorkflowRunPayloadAdmission(c.workflowPayloadLimit),
			// ADR-0098 F65: reject a WorkflowRun whose input violates the parent's cached contract (zero registry I/O).
			admission.NewWorkflowRunContractAdmission(storeReader{c.store}),
			// ADR-0094: a run's workflow/input/replay are fixed at creation — a second run under a taken name is a Conflict.
			admission.NewWorkflowRunSpecImmutableAdmission(),
			// ADR-0347 Decision 3: a JobRun's input within job.payloadLimit and its Job's contract, its spec fixed but a
			// one-way cancel; ADR-0348: an approval and a rejection, by a caller who may update the Job.
			admission.NewJobRunPayloadAdmission(c.jobPayloadLimit),
			admission.NewJobRunContractAdmission(storeReader{c.store}),
			admission.NewJobRunSpecAdmission(),
			admission.NewJobRunApprovalAdmission(c.authorizer),
			// ADR-0170: a ResourceGroup is deleted only when it has no member (or with force, members first).
			admission.NewResourceGroupDeletionProtectionAdmission(r),
			// ADR-0222: a schedule's recipients and target, a Backup's ttl, immutable records, and update on the
			// Namespace, one active at a time and none while a restore marker stands for an in-place Restore.
			admission.NewBackupScheduleAdmission(c.blobStoreURL),
			admission.NewBackupSpecImmutableAdmission(),
			admission.NewBackupTTLAdmission(r),
			admission.NewRestoreInPlaceAdmission(c.authorizer, r, c.hold),
		)
		if p.cluster != nil { // FEAT-0002 F250: a node runs the checks that read its own state
			all = p.cluster.Forward(all)
		}
		return all
	}

	cpAdmissions := append(partAdmissions(storeReader{c.store}), app.NewAdmission(partAdmissions, storeReader{c.store}),
		app.NewRequiresAdmission(storeReader{c.store}))
	if p.cluster != nil {
		cpAdmissions = append(cpAdmissions, p.cluster.Admissions()...)
	}
	// ADR-0349 Decision 2: a run that a Function starts passes the admission of a person's create, the control plane's.
	p.jobStarts.admit = admission.NewPipeline(append([]admission.Admission{admission.NewValidateAdmission()}, cpAdmissions...)...)

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
	// FEAT-0002 F250: the parts that act on a node's state answer a worker node's calls, and on a control plane each
	// reaches the node that runs the namespace.
	holdSvc := controlplane.NewHoldService(controlplane.HoldDeps{ // ADR-0206: the hold's evidence and release
		Hold: c.hold, Store: c.store, DeadLetters: p.eventing.deadLetters, Platform: holdPlatform{p},
	})
	// ADR-0205: the platform backup cuts the event store, the metastore and the run state.
	var backupStatus controlplane.BackupStatuser
	if c.backupRunner != nil {
		var meta snapshot.Source = c.store
		if p.agent != nil {
			meta = store.WithoutSecrets(c.store)
		}
		if err := c.backupRunner.Bind(runner.Inputs{Events: p.eventStore, Meta: meta, Runs: runs, Parent: c.backupParent}); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "bind the platform backup")
		}
		backupStatus = c.backupRunner
	}
	parts := p.routeNodeParts(nodeParts{
		logs: logReader, runLogs: runLogQuerier, deadLetters: p.eventing.deadLetters, replayer: p.eventing.sensorReconciler,
		apps: appReconciler, volumes: p.volumeDeleter(), hold: holdSvc, backup: backupStatus,
	}, holdSvc, multinode.NodeState(partAdmissions(storeReader{c.store})))
	handler, err := controlplane.NewServer(controlplane.Deps{
		Backup:      backupStatus,
		Nodes:       parts.nodes, // FEAT-0002 F250: a worker node's own hold and platform backup
		Store:       c.store,
		Authorizer:  c.authorizer,
		Credentials: c.credentials,
		Logger:      p.logger,
		Logs:        parts.logs,
		RunLogs:     parts.runLogs,
		DeadLetters: parts.deadLetters,       // ADR-0118: the DLQ read + replay/discard surface
		Replayer:    parts.replayer,          // ADR-0118: the imperative replay seam (one synchronous attempt)
		Collector:   p.collector,             // ADR-0170: a forced ResourceGroup delete collects the members' children
		AppRetrier:  parts.apps,              // ADR-0214: retry an App's failed hook
		Planner:     app.NewPlanner(c.store), // ADR-0220: a dry-run App write's plan
		Volumes:     parts.volumes,           // ADR-0262: delete a retained Function volume
		Hold:        parts.hold,              // ADR-0206: the hold's evidence and release
		// ADR-0219: app-requires runs after app-parts.
		Admissions: cpAdmissions,
		JoinTokens: parts.joinTokens,
		APITokens:  apitoken.NewMinter(c.store, nil),
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build control-plane server")
	}
	// ReadTimeout bounds the request read, as on the data plane (issue #90); with no IdleTimeout set,
	// net/http also uses it as the keep-alive idle bound, so a silent client cannot hold a connection (#300).
	// ErrorLog sends net/http's own errors (TLS handshake, accept, recovered panic) through the configured
	// logger; left nil they go to the stdlib log package, bypassing the log format (#454).
	httpErrorLog := slog.NewLogLogger(p.logger.Handler(), slog.LevelWarn)
	if p.agent != nil {
		handler = readOnly(handler)
	}
	p.httpServer = &http.Server{Handler: recordControlPlane(handler), ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout: serverReadTimeout, ErrorLog: httpErrorLog}

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
	// ADR-0120 (F82): the static-asset handler serves a static Route's Bucket prefix directly over the
	// SAME per-namespace Bucket view the S3 frontend uses (s3BucketFor → blob.Prefixed), with no
	// activator hop. Always wired; a static Route match without it would 404.
	staticHandler, err := static.New(static.Deps{Buckets: s3BucketFor(c.blob, c.store, p.checker), Logger: p.logger})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build static asset handler")
	}
	// ADR-0114 (F76/F78): observability wraps outer-than-limit (times the whole hop incl. rejects) but
	// inner-than-RequestID (reads X-Request-Id); shaping is innermost (wraps the real response). The write stall
	// bound is outermost, so every byte of every listener response passes it. Runtime order:
	// WriteStall → Recover → RequestID → client address (ADR-0354) → observ → limit → shape → dataplane.Handler.
	dpOpts := []dataplane.Option{dataplane.WithCallGate(c.callGate()), dataplane.WithChain(callChain),
		dataplane.WithOIDC(oidc.New(oidc.Deps{Store: c.store, CallbackURL: p.callbacks().URL, HTTPS: p.callbacks().HTTPS(),
			BackChannel: c.oidcBackChannel, Addons: storedAddons{c.store}, Logger: p.logger}))}
	if bridge, ok := p.bridgeEdge(); ok { // ADR-0353: a caller on the bridge address gets only its namespace
		dpOpts = append(dpOpts, dataplane.WithBridgeCallers(bridge, p.exec.egressSources))
	}
	dpCore := dataplane.Handler(c.store, act, p.edgeRouter, c.baseDomain, edgeEnforcer, targetLimiter, staticHandler,
		c.invokeDefaultTimeout, p.logger, dpOpts...)
	edgeObserv, edgeShape := observ.Chain(c.observ, c.telemetry, p.logger), shape.Chain(c.shaping)
	dpHandler := gateway.Chain(dpCore, gateway.WriteStall(gateway.WriteStallTimeout), gateway.Recover(p.logger),
		gateway.RequestID, p.clientAddress, edgeObserv, limit.Chain(c.limits), edgeShape)
	// Late-bind the worker-node local API invoker (ADR-0064) to the same chain minus the ingress
	// limiter and edge observ: ADR-0112 guards the listener, so a nested fn-to-fn invoke never takes its
	// caller's in-flight slot or rate token (#87); edge signals describe only listener requests, so an
	// internal call records no edge span, metric or access-log line (ADR-0165).
	dpHolder.Set(gateway.Chain(dpCore, gateway.Recover(p.logger), gateway.RequestID, edgeShape))
	// ReadTimeout bounds the whole request read (headers + body), so a client that stops sending its
	// body cannot hold an ADR-0112 in-flight slot indefinitely (issue #90). net/http clears the
	// deadline once the body is read, so it does not cut a long-running handler.
	p.dataPlaneServer = &http.Server{Handler: recordGateway(dpHandler), ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout: cmp.Or(c.gatewayReadTimeout, serverReadTimeout), ErrorLog: httpErrorLog}
	if p.controlPlaneAlone() {
		return nil // each node's edge serves its own namespaces
	}
	dln, err := net.Listen("tcp", c.dataPlaneAddr)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "bind data-plane listener on %s", c.dataPlaneAddr)
	}
	p.dataPlaneListener = dln
	p.dataPlaneAddr = dln.Addr().String()
	if tcp, ok := dln.Addr().(*net.TCPAddr); ok {
		p.edgeOnTheBridge(tcp.AddrPort())
	}

	return nil
}

// callbacks are the edge sign-in's callbacks (ADR-0335 Decision 9), on the data plane's bound port.
func (p *Platform) callbacks() oidc.Callbacks {
	c := p.cfg
	return oidc.Callbacks{TLS: c.tlsSpec != nil, Scheme: c.edgePublicScheme, Port: c.edgePublicPort, Bound: func() int {
		_, port, err := net.SplitHostPort(p.dataPlaneAddr)
		if err != nil {
			return 0
		}
		n, _ := strconv.Atoi(port)
		return n
	}}
}

// registerSignIn registers the OIDCProvider kind's one reconciler, which re-runs on a change of a Route, a workload,
// a Namespace or a Secret of its namespace (ADR-0336 Decision 3), the Namespace check of edgeDefaults.auth, and the
// watches that move a Route's or a Function's ProviderNotFound (ADR-0335 Decision 10).
func (p *Platform) registerSignIn(ctrl *controller.Controller, fns *function.Reconciler) {
	c := p.cfg
	ctrl.Register(v1.KindOIDCProvider.GVK(), oidc.NewReconciler(oidc.ReconcilerDeps{Store: c.store, CallbackURL: p.callbacks().URL,
		Client: oidc.BackChannelClient(c.oidcBackChannel), Held: c.nsGate(), SupervisionPeriod: c.pacing.SupervisionPeriod,
		BaseDomain: c.baseDomain, Addons: storedAddons{c.store}, Logger: p.logger}))
	for _, k := range []v1.Kind{v1.KindRoute, v1.KindFunction, v1.KindSite, v1.KindNamespace, v1.KindSecret, v1.KindAddonObject} {
		ctrl.Watches(k.GVK(), oidc.MapProviders(c.store))
	}
	ctrl.Register(v1.KindNamespace.GVK(), oidc.NewNamespaceCheck(c.store, p.logger))
	ctrl.Watches(v1.KindOIDCProvider.GVK(), oidc.MapProviderNamespace)
	ctrl.Watches(v1.KindOIDCProvider.GVK(), route.MapOIDCProvider(c.store))
	ctrl.Watches(v1.KindOIDCProvider.GVK(), fns.MapOIDCProvider)
}

// Addr returns the bound control-plane address (host:port), available after New.
func (p *Platform) Addr() string { return p.addr }

// DataPlaneAddr returns the bound data-plane (function-invocation) address, available
// after New (ADR-0033); "" on a control plane alone, which has no data plane (FEAT-0002).
func (p *Platform) DataPlaneAddr() string { return p.dataPlaneAddr }

// WaitS3Gateway blocks until the WithS3Gateway frontend is bound and serving (nil), it stopped first (its error),
// or ctx is done. The gateway binds its address only once Run starts it (ADR-0085), so a taken port surfaces here
// and from Run, not from New. Without the gateway it returns nil.
func (p *Platform) WaitS3Gateway(ctx context.Context) error {
	if p.blob.gateway == nil {
		return nil
	}
	return p.blob.gateway.Wait(ctx)
}

// Run starts the control loops + the control-plane server and blocks until ctx is
// cancelled, then shuts down gracefully and returns nil. It owns the crash-only
// lifecycle (ADR-0028): a setup error, such as an S3 gateway that cannot bind its address, also shuts the
// platform down before Run returns it.
func (p *Platform) Run(ctx context.Context) error {
	p.logger.InfoContext(ctx, "platform starting", "addr", p.addr, "dataPlaneAddr", p.dataPlaneAddr)
	p.logProviders(ctx)
	p.warnUnmatchedKinds()

	ctx, cancelLoops := context.WithCancel(ctx)
	defer cancelLoops()
	var wg sync.WaitGroup
	// abort stops the loops Run started and shuts the platform down, so a setup error leaves nothing open
	// (issue #489, ADR-0028).
	abort := func(err error) error {
		cancelLoops()
		wg.Wait()
		closeCtx, cancelClose := context.WithTimeout(context.WithoutCancel(ctx), p.closeTimeout())
		defer cancelClose()
		return errors.Join(err, p.Shutdown(closeCtx))
	}
	if err := validBaseDomain(p.cfg.baseDomain, p.cfg.tlsSpec); err != nil {
		return abort(err)
	}

	// FEAT-0002 F251: a worker node whose store is new or restored takes its namespaces from the control plane first, so no
	// step below, such as the reclaim of what a deleted store left, reads a store that lacks them.
	if p.agent != nil {
		if err := p.agent.Prepare(ctx); err != nil && ctx.Err() == nil {
			return abort(err)
		}
	}

	// The providers start first, the Executor at their head: it applies the egress fence before anything can run a
	// worker (ADR-0115). A failed Start shuts the platform down (#489); a Start a stop signal cut is a clean stop.
	p.logger.InfoContext(ctx, "platform provider hooks", "providers", p.phases.Hooks(sharedHooks()...))
	if err := p.phases.Start(ctx); err != nil && ctx.Err() == nil {
		return abort(err)
	}
	// ADR-0347 Decision 7: the drivers swept every worker an earlier run left when they were built, and the controller
	// has not started, so a run stored Running was cut by the stop; held or not, it ends Interrupted and never restarts.
	if p.jobRuns != nil {
		if err := p.jobRuns.Boot(ctx); err != nil && ctx.Err() == nil {
			return abort(err)
		}
	}

	if m, held := p.heldMarker(); held {
		// ADR-0206 Decision 6: KV or blob data newer than a restored metastore may have no object yet, so the boot
		// reclaims wait for the first start after the release; `funcdctl hold status` lists that data.
		p.logger.WarnContext(ctx, "platform held: no timer, blob source, Sensor, run, App rollout, retention sweep or "+
			"reclaim acts until `funcdctl hold release`", "reason", m.Reason, "since", m.Since)
		// held, it reclaims nothing: it books the stored volumes and keeps the last run's emptyDirs, which go once
		// the hold lifts (#1204)
		if err := p.fnReconciler.ReclaimVolumes(ctx); err != nil && ctx.Err() == nil {
			p.logger.WarnContext(ctx, "booking of Function volumes incomplete", "error", err)
		}
	} else {
		// ADR-0307: only a boot that is not held reclaims what deleted KVStores, Buckets and SQLDatabases left (#708,
		// ADR-0199 Decision 7, ADR-0232 Decision 7), once every provider started and before any controller can create
		// a resource of the same name.
		if err := p.phases.Reclaim(ctx); err != nil && ctx.Err() == nil {
			p.logger.WarnContext(ctx, "reclaim of deleted resources incomplete", "error", err)
		}
		// ADR-0262 Decision 8: no worker survives a restart, so emptyDirs go; volumes of deleted Functions follow their
		// deletion policy.
		if err := p.fnReconciler.ReclaimVolumes(ctx); err != nil && ctx.Err() == nil {
			p.logger.WarnContext(ctx, "reclaim of Function volumes incomplete", "error", err)
		}
	}

	// ADR-0176 Decision 6: the edge aggregator holds every lake entry until "routes" has Set once, and no
	// Route event follows when no Route exists, so the full Route set is Set here, before the controller runs.
	if _, err := p.routeReconciler.Reconcile(ctx, controller.Request{GVK: v1.KindRoute.GVK()}); err != nil && ctx.Err() == nil {
		return abort(fault.Wrapf(err, fault.KindOf(err), "funcd.Run", "load the Route table"))
	}

	// ADR-0215 Decision 6: the first storage probe runs before the reconcilers read its result.
	p.prober.Start(ctx)

	// The providers' loops and the Platform's own run on Run's context, so they all drain together before the providers
	// stop.
	p.phases.Run(ctx, &wg)
	loops := []func(context.Context){
		func(ctx context.Context) {
			loop(p.logger, "controller stopped", p.controller.Run)(ctx)
			p.backups.Wait() // a run's goroutine outlives its reconcile, never the controller (ADR-0222)
		},
		loop(p.logger, "garbage collector stopped", p.collector.Run),
		loop(p.logger, "activator stopped", p.activator.Run),
		p.quotas.Run,         // ADR-0292 Decision 3: the store quotas' refresh, off the write path
		p.runQuarantineSweep, // ADR-0288 Decision 5: the quarantine's sweep, never inside the start
	}
	if p.workflowSweeper != nil && p.workflowRetention > 0 { // ADR-0094: periodic terminal-run retention sweep
		loops = append(loops, p.runWorkflowRetention)
	}
	if p.jobRuns != nil { // ADR-0347 Decision 8: the ended JobRuns' retention sweep
		loops = append(loops, p.runJobRetention)
	}
	if p.workflowEngine != nil { // ADR-0146: the run goroutines, drained within the shutdown bound before Shutdown
		loops = append(loops, func(ctx context.Context) { p.workflowEngine.Run(ctx, p.drainTimeout) })
	}
	if p.cfg.backupRunner != nil { // ADR-0205: the platform backup runs (stops on ctx cancel)
		loops = append(loops, p.cfg.backupRunner.Run)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		runAll(ctx, loops...)
	}()
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
	if p.dataPlaneListener != nil {
		go func() {
			if err := serve(p.dataPlaneServer, p.dataPlaneListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				p.logger.ErrorContext(ctx, "data-plane server stopped", "error", err)
			}
		}()
	}
	if p.bridgeListen.IsValid() {
		go p.serveBridge(ctx, serve)
	}
	if p.cluster != nil {
		go func() {
			if err := p.cluster.Run(ctx); err != nil {
				p.logger.ErrorContext(ctx, "node API stopped", "error", err)
			}
		}()
	}
	var restored atomic.Bool
	if p.agent != nil {
		go func() {
			err := p.agent.Run(ctx)
			switch {
			case errors.Is(err, agent.ErrRestored):
				restored.Store(true)
				cancelLoops() // the platform stops, and starts again on the restored state
			case err != nil:
				p.logger.ErrorContext(ctx, "node agent stopped", "error", err)
			}
		}()
	}

	<-ctx.Done()

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.drainTimeout)
	defer cancel()
	p.logger.InfoContext(stopCtx, "platform stopping")
	// Drain both HTTP servers (stop accepting) before the ports close — an in-flight
	// invocation needs the runtime/store (ADR-0033).
	_ = p.dataPlaneServer.Shutdown(stopCtx)
	_ = p.httpServer.Shutdown(stopCtx)
	wg.Wait() // drain controller + eventing + activator before closing ports
	closeCtx, cancelClose := context.WithTimeout(context.WithoutCancel(ctx), p.closeTimeout())
	defer cancelClose()
	err := p.Shutdown(closeCtx)
	if restored.Load() {
		return errors.Join(ErrControlPlaneRestored, err)
	}
	return err
}

// closeTimeout is Shutdown's own bound when Run stops or aborts (ADR-0277): the runtime's stop grace plus closeMargin, so
// an HTTP drain that used all of shutdownTimeout still leaves the workers their grace and the log Routes and telemetry
// time to flush after it (issue #453).
func (p *Platform) closeTimeout() time.Duration {
	grace := defaultCloseGrace
	if g, ok := p.cfg.runtime.(runtime.StopGracer); ok {
		grace = g.StopGrace()
	}
	return grace + closeMargin
}

// Shutdown closes the platform's components (the control-plane listener, the five
// ports, telemetry). It is concurrency-safe and idempotent (sync.Once so Run's
// shutdown-on-ctx and a caller's explicit Shutdown cannot race or double-close) and
// best-effort (joins errors, closing all even if one fails).
func (p *Platform) Shutdown(ctx context.Context) error {
	p.shutdownOnce.Do(func() {
		if p.cluster != nil {
			_ = p.cluster.Close()
		}
		if p.listener != nil {
			_ = p.listener.Close() // best-effort; may already be closed by httpServer.Shutdown
		}
		if p.dataPlaneListener != nil {
			_ = p.dataPlaneListener.Close() // best-effort; may already be closed by dataPlaneServer.Shutdown
		}
		if p.tlsProvider != nil {
			_ = p.tlsProvider.Close(ctx) // stop certmagic's renewal goroutine (ADR-0111)
		}
		if p.jobRuns != nil {
			// the controller has stopped, so no pass writes a stopped executor's answer: the next boot ends its run
			// Interrupted (ADR-0347 Decision 7); each executor stops its worker while the runtime is still open
			p.jobRuns.Close()
		}
		// Every provider gets ready to close, in reverse order: the Lake closes its PEP proxies, then the Executor closes
		// the runtime, which stops every worker, and the workers' local API listeners.
		beforeStopErr := p.phases.BeforeStop(ctx)
		// Every provider stops, in reverse order: the binding providers release what the workers used (the SQL feed's
		// port, the S3 gateway, the KV driver), then the Executor takes the egress fence down once the runtime
		// closed, stops the egress gateway and the DNS forwarder, drains the log Routes into the sinks before
		// blob.Close (ADR-0081) and removes the socket dir it made (#330).
		stopErr := p.phases.Stop(ctx)
		if p.fnReconciler != nil {
			// Only after Stop is the runtime closed on every path, booted or not: the pool workers are stopped, so their
			// manifests can go.
			_ = p.fnReconciler.Close()
		}
		errs := []error{
			closeDriver(p.cfg.bus),
			closeDriver(p.cfg.gateway),
			beforeStopErr,
			stopErr,
			closeDriver(p.cfg.blob),
			closeDriver(p.cfg.store),
		}
		if p.workflowRuns != nil {
			errs = append(errs, p.workflowRuns.Close())
		}
		// ADR-0201: after Run stopped the BlobWatcher and the Sensor. Eventing, the SQL feed's watermarks and the platform
		// backup share the event store, so it closes with the Platform's own parts (ADR-0305 Decision 5).
		if p.eventStore != nil {
			errs = append(errs, p.eventStore.Close())
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

// contractInspector is the production function.ContractInspector (ADR-0347 Decision 2): a Job's image contract read
// by its digest from registry metadata, never the bundle. An image pinned by no digest, a file artifact, has none to
// read that way.
type contractInspector struct{}

func (contractInspector) Inspect(ctx context.Context, image, digest string) ([]byte, error) {
	if digest == "" {
		return nil, fault.NotFoundf("funcd.contractInspector", "%s is pinned by no digest", image)
	}
	return artifact.Inspect(ctx, image, digest)
}

// jobContracts is the Jobs' ContractInspector: dev's contract resolver when it sets one, else the registry metadata.
func jobContracts(dev workflow.ContractResolver) function.ContractInspector {
	if dev != nil {
		return resolverInspector{dev}
	}
	return contractInspector{}
}

// resolverInspector reads a Job's contract through the workflow contract resolver that funcdctl dev sets
// (WithWorkflowContractResolver), which knows an image built from source by its reference, so a JobRun's admission under
// dev checks its input as a server's does.
type resolverInspector struct{ r workflow.ContractResolver }

func (i resolverInspector) Inspect(ctx context.Context, image, _ string) ([]byte, error) {
	c, _, err := i.r.Contract(ctx, image)
	if err != nil {
		return nil, err
	}
	if len(c.Input) == 0 && len(c.Output) == 0 {
		return nil, fault.NotFoundf("funcd.resolverInspector", "%s carries no contract", image)
	}
	return json.Marshal(c)
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

// protocolOf reads how a Function is called from the store, so a Sensor, an App hook or a workflow step never reaches
// one that serves HTTP (ADR-0243 Decision 5).
func protocolOf(st store.Store) activator.ProtocolFunc {
	return func(ctx context.Context, fn activator.FunctionRef) (v1.FunctionProtocol, error) {
		obj, err := st.Get(ctx, v1.KindFunction.GVK(), fn.Namespace, fn.Name)
		if err != nil {
			return "", err
		}
		f, ok := obj.(*v1.Function)
		if !ok {
			return "", fault.Internalf("funcd.protocolOf", "%s/%s is not a Function", fn.Namespace, fn.Name)
		}
		return f.Spec.Protocol.Normalized(), nil
	}
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
	if interval > maxRetentionSweep {
		interval = maxRetentionSweep
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if p.cfg.gate().Held() { // held runs are evidence (ADR-0206 Decision 6)
				continue
			}
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

// runJobRetention sweeps the ended JobRuns past job.retention (ADR-0347 Decision 8) at most hourly, sooner when the
// retention is short, until ctx is cancelled. The sweep itself skips held runs; a failure is logged and the next tick
// retries.
func (p *Platform) runJobRetention(ctx context.Context) {
	ticker := time.NewTicker(min(p.cfg.jobRetention, maxRetentionSweep))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := p.jobRuns.SweepExpired(ctx)
			if err != nil {
				p.logger.WarnContext(ctx, "job run retention sweep failed", "error", err)
				continue
			}
			if n > 0 {
				p.logger.InfoContext(ctx, "job run retention sweep deleted runs", "count", n)
			}
		}
	}
}

// runDeadLetterRetention periodically evicts dead letters past the TTL and over the per-namespace count cap
// (ADR-0118 §5), mirroring the run-retention sweep. It sweeps at most hourly (sooner when the TTL is short),
// stops on ctx cancel, and logs (never fatal) a sweep failure — the next tick retries.
func (s *eventingProvider) runDeadLetterRetention(ctx context.Context) {
	c := s.cfg
	interval := maxRetentionSweep
	if c.deadletterRetention > 0 && c.deadletterRetention < interval {
		interval = c.deadletterRetention
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.gate().Held() { // held dead letters are evidence (ADR-0206 Decision 6)
				continue
			}
			n, err := s.deadLetters.SweepExpired(ctx, c.deadletterRetention, c.deadletterMaxEntries)
			if err != nil {
				c.logger.WarnContext(ctx, "dead-letter retention sweep failed", "error", err)
				continue
			}
			if n > 0 {
				c.logger.InfoContext(ctx, "dead-letter retention sweep evicted entries", "count", n)
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

// storeDomainRules adapts store.Store to egress.DomainRules (ADR-0315): the domain rules of the EgressPolicies that apply
// to a worker's principal, read on each lookup from the same EgressPolicy set the PDP compiles (no duplicate state). A
// failed list is an error, so the lookup is refused.
type storeDomainRules struct{ s store.Store }

func (r storeDomainRules) For(ctx context.Context, principal auth.EntityRef) ([]string, error) {
	const op = "funcd.storeDomainRules.For"
	kind, err := addonKindOf(ctx, r.s, principal)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "read the add-on object")
	}
	res, err := r.s.List(ctx, v1.KindEgressPolicy.GVK(), store.ListOptions{Namespace: principal.Namespace})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list egress policies")
	}
	var out []string
	for _, o := range res.Items {
		if ep, ok := o.(*v1.EgressPolicy); ok {
			out = append(out, cedarauth.EgressDomains(ep, principal, kind)...)
		}
	}
	return out, nil
}

// addonKindOf is the kind of the stored add-on object that principal names, as an EgressPolicy item names it (ADR-0352
// Decision 4): "" for another principal, or for an object no longer stored.
func addonKindOf(ctx context.Context, st store.Store, principal auth.EntityRef) (string, error) {
	if principal.Type != v1.KindAddonObject {
		return "", nil
	}
	obj, err := st.Get(ctx, v1.KindAddonObject.GVK(), principal.Namespace, principal.Name)
	if fault.KindOf(err) == fault.NotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	o, _ := obj.(*v1.AddonObject)
	if o == nil {
		return "", nil
	}
	return o.Spec.Kind, nil
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
func (p *Platform) EgressWorkerIndex() *egress.MemoryWorkerIndex { return p.exec.egressWorkers }

// S3Keys is the deriver of the platform's in-platform S3 key pairs (ADR-0270); its zero value when the gateway is off.
func (p *Platform) S3Keys() s3gateway.Deriver { return p.blob.keys }

// syncEgressWorkers keeps the ADR-0117 §5 WorkerIndex (src funcd0 IP → principal Ref) reconciled with the
// runtime's running worker set, so the egress gateway can authenticate a redirected connection by source
// IP (and the DNS forwarder can resolve a worker's namespace for wildcard-token injection). It polls the
// runtime — the same observed view the Function reconciler drives — every interval, diffing against the
// last snapshot: a newly-running worker's IP is Added, a vanished one Removed. Runs only when the egress
// gateway is wired; stops on ctx cancel.
func (e *executor) syncEgressWorkers(ctx context.Context) {
	t := time.NewTicker(orDefault(e.cfg.pacing.WorkerSyncInterval, defaultWorkerSyncInterval))
	defer t.Stop()
	prev := map[netip.Addr]auth.EntityRef{}
	prev = e.reconcileEgressWorkers(ctx, prev) // seed immediately (don't wait a full tick)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prev = e.reconcileEgressWorkers(ctx, prev)
		}
	}
}

// reconcileEgressWorkers computes the current running-worker IP→Ref set from the runtime (over the
// namespaces that have a Function, a Lake or a Job) and applies the diff against prev to the WorkerIndex,
// returning the new snapshot. Each worker is indexed as the principal of its owner kind (ADR-0175): a Function
// worker as its Function, an engine as its Lake, a run's worker as its run's Job (ADR-0351 Decision 6); a worker of
// any other kind, or of a run no longer stored, is not indexed (an unknown source, denied). A worker's funcd0 IP is
// the source IP the F80 REDIRECT preserves, so it keys both the gateway lookup and the DNS-forwarder correlation. A
// transient store error keeps the current index (returns prev).
func (e *executor) reconcileEgressWorkers(ctx context.Context, prev map[netip.Addr]auth.EntityRef) map[netip.Addr]auth.EntityRef {
	namespaces := map[v1.NamespaceName]struct{}{}
	for _, kind := range []v1.Kind{v1.KindFunction, v1.KindLake, v1.KindJob} {
		res, err := e.cfg.store.List(ctx, kind.GVK(), store.ListOptions{})
		if err != nil {
			return prev
		}
		for _, o := range res.Items {
			namespaces[o.GetNamespace()] = struct{}{}
		}
	}
	live := map[netip.Addr]auth.EntityRef{}
	for ns := range namespaces {
		insts, lerr := e.cfg.runtime.List(ctx, ns)
		if lerr != nil {
			continue
		}
		for _, in := range insts {
			if in.State != runtime.StateRunning || in.IP == "" {
				continue
			}
			ip, perr := netip.ParseAddr(in.IP)
			if perr != nil {
				continue
			}
			ref, ok, err := e.egressPrincipal(ctx, in)
			if err != nil {
				return prev
			}
			if ok {
				live[ip] = ref
			}
		}
	}
	for ip, ref := range live {
		if pr, ok := prev[ip]; !ok || pr != ref {
			e.egressWorkers.Add(ip, ref)
		}
	}
	for ip := range prev {
		if _, ok := live[ip]; !ok {
			e.egressWorkers.Remove(ip)
		}
	}
	_ = e.indexAddonEngines(ctx) // a failed run keeps the index; the next sync runs again
	return live
}

// egressPrincipal is the principal worker in acts as at the egress gateway: its Function or its Lake, or the Job its run
// names in spec.job, read by the worker's name (ADR-0351 Decision 6). ok is false for a worker of any other kind and
// for a run no longer stored.
func (e *executor) egressPrincipal(ctx context.Context, in runtime.Instance) (auth.EntityRef, bool, error) {
	switch in.OwnerKind {
	case v1.KindFunction, v1.KindLake:
		return auth.EntityRef{Type: in.OwnerKind, Namespace: in.Namespace, Name: in.Name}, true, nil
	case v1.KindJobRun:
		obj, err := e.cfg.store.Get(ctx, v1.KindJobRun.GVK(), in.Namespace, in.Name)
		if fault.KindOf(err) == fault.NotFound {
			return auth.EntityRef{}, false, nil
		}
		if err != nil {
			return auth.EntityRef{}, false, err
		}
		run, ok := obj.(*v1.JobRun)
		if !ok {
			return auth.EntityRef{}, false, nil
		}
		return auth.EntityRef{Type: v1.KindJob, Namespace: in.Namespace, Name: run.Spec.Job}, true, nil
	}
	return auth.EntityRef{}, false, nil
}

// indexAddonEngines puts the address of every add-on engine, from its worker's creation on, in the gateway's engine
// index, which no rule reaches (ADR-0332 Decision 3). It runs at each worker sync and, through the add-on runtime's
// BeforeStart, before each engine starts. Runs take turns, so a run's list never replaces a later run's. A failed read
// keeps the index as it was and is the error.
func (e *executor) indexAddonEngines(ctx context.Context) error {
	if e.egressEngines == nil {
		return nil
	}
	e.engineIndexMu.Lock()
	defer e.engineIndexMu.Unlock()
	engines, err := addonEngineAddrs(ctx, e.cfg.store, e.cfg.runtime, e.addonNamespaces)
	if err != nil {
		return err
	}
	e.egressEngines.Replace(engines)
	return nil
}

// addonEngineAddrs are the addresses of the add-on engines' workers, from each worker's creation on, whatever its state
// (ADR-0332 Decision 3): in each namespace that holds an add-on object or, as running reports, an engine of the add-on
// runtime, a stopping engine's included.
func addonEngineAddrs(ctx context.Context, st store.Store, rt runtime.Runtime, running func() []v1.NamespaceName) (map[netip.Addr]auth.EntityRef, error) {
	const op = "funcd.addonEngineAddrs"
	res, err := st.List(ctx, v1.KindAddonObject.GVK(), store.ListOptions{})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list the add-on objects")
	}
	namespaces := map[v1.NamespaceName]bool{}
	for _, o := range res.Items {
		namespaces[o.GetNamespace()] = true
	}
	if running != nil {
		for _, ns := range running() {
			namespaces[ns] = true
		}
	}
	engines := map[netip.Addr]auth.EntityRef{}
	for ns := range namespaces {
		insts, lerr := rt.List(ctx, ns)
		if lerr != nil {
			return nil, fault.Wrapf(lerr, fault.KindOf(lerr), op, "list the workers of %s", ns)
		}
		for _, in := range insts {
			if in.OwnerKind != v1.KindAddonObject {
				continue
			}
			if ip, perr := netip.ParseAddr(in.IP); perr == nil {
				engines[ip] = auth.EntityRef{Type: v1.KindAddonObject, Namespace: in.Namespace, Name: in.Name}
			}
		}
	}
	return engines, nil
}

// backupEngines tells a backup target's address from an add-on engine's: an object's status.address, and the address
// of each engine's worker from its creation on (ADR-0332 Decision 3).
func backupEngines(st store.Store, rt runtime.Runtime, running func() []v1.NamespaceName) addonsvc.Engines {
	return addonsvc.Engines{Store: st, Workers: func(ctx context.Context, addr netip.Addr) (bool, error) {
		engines, err := addonEngineAddrs(ctx, st, rt, running)
		if err != nil {
			return false, err
		}
		_, ok := engines[addr]
		return ok, nil
	}}
}

// enqueueAll enqueues every object of kind in the store.
func enqueueAll(ctx context.Context, st store.Store, ctrl *controller.Controller, kind v1.Kind, logger *slog.Logger) {
	list, err := st.List(ctx, kind.GVK(), store.ListOptions{})
	if err != nil {
		logger.WarnContext(ctx, "could not list the objects a storage probe change affects", "kind", string(kind), "error", err)
		return
	}
	for _, o := range list.Items {
		m := o.GetObjectMeta()
		ctrl.Enqueue(controller.Request{GVK: kind.GVK(), Namespace: m.Namespace, Name: m.Name})
	}
}

// s3BucketFor builds the s3gateway BucketFor resolver (ADR-0080): it maps an S3
// (namespace, bucket-name) to a prefixed view of the single shared blob substrate,
// resolving ok=true only when a Bucket of that name exists in that namespace. The
// namespacing scheme is a key-prefix view `s3/<ns>/<bucket>/` over the shared bucket —
// one substrate bucket, many logical S3 buckets — so distinct namespaces and buckets
// never collide. Existence-by-namespace here gives tenancy a second guard (a missing /
// cross-namespace bucket is fault.NotFound, NoSuchBucket on the S3 frontend); a failed read of the Bucket is returned
// as that error, so no caller takes it for a missing Bucket (#1411). The binding-as-grant Cedar PEP is the
// authorization gate on every object op. The view carries the Bucket's spec.quota: its per-object cap
// (blob.Capped, a 413 first) over its total sizes (blob.Quoted, ADR-0292), so every write path through it (S3
// frontend, context.blob, site) enforces them.
func s3BucketFor(shared blob.Bucket, st store.Store, chk quota.Checker) func(ns v1.NamespaceName, bucket string) (blob.Bucket, error) {
	const op = "funcd.s3BucketFor"
	return func(ns v1.NamespaceName, bucket string) (blob.Bucket, error) {
		if bucket == "" {
			return nil, fault.NotFoundf(op, "a bucket needs a name")
		}
		obj, err := st.Get(context.Background(), v1.KindBucket.GVK(), ns, v1.ObjectName(bucket))
		if err != nil && fault.KindOf(err) != fault.NotFound {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "read bucket %q in namespace %q", bucket, ns)
		}
		b, _ := obj.(*v1.Bucket)
		if b == nil {
			return nil, fault.NotFoundf(op, "bucket %q not found in namespace %q", bucket, ns)
		}
		capped := func(prefix string) bool {
			_, ok := bucketQuota(b, prefix).Limit()
			return ok
		}
		subject := quota.Subject{Kind: v1.KindBucket, Namespace: ns, Name: v1.ObjectName(bucket)}
		view := blob.Quoted(blob.Prefixed(shared, bucketPrefix(ns, v1.ObjectName(bucket))), chk, subject, capped)
		return blob.Capped(view, b.Spec.Quota.MaxObjectBytes), nil
	}
}

// s3Root is the substrate key prefix under which every Bucket's objects live (ADR-0080).
const s3Root = "s3/"

// bucketPrefix is one Bucket's substrate key prefix, s3/<ns>/<bucket>/ (ADR-0080).
func bucketPrefix(ns v1.NamespaceName, bucket v1.ObjectName) string {
	return s3Root + string(ns) + "/" + string(bucket) + "/"
}

// lakeCatalogRoot is the substrate key prefix of the Lake catalog store, beside s3Root (ADR-0293): a Lake's catalog is
// lake-catalogs/<ns>/<bucket>/<prefix>/_ducklake/catalog.db, under the location of its files.
const lakeCatalogRoot = "lake-catalogs/"

// lakeCatalogPrefix is a namespace's substrate prefix in the catalog store.
func lakeCatalogPrefix(ns v1.NamespaceName) string { return lakeCatalogRoot + string(ns) + "/" }

// bucketCatalogPrefix is the substrate prefix of the catalogs of the Lakes on one Bucket's prefixes, which go with the
// Bucket's objects wherever a purge or the quarantine takes them (ADR-0293 Decision 6).
func bucketCatalogPrefix(ns v1.NamespaceName, bucket v1.ObjectName) string {
	return lakeCatalogPrefix(ns) + string(bucket) + "/"
}

// LakeData maps a catalog's substrate key to the substrate prefix of its Lake's files, ok=false for any other key: the
// platform mirror freezes a catalog before them (ADR-0293 Decision 5).
func LakeData(key string) (prefix string, ok bool) {
	rest, ok := strings.CutPrefix(key, lakeCatalogRoot)
	if !ok {
		return "", false
	}
	loc, ok := strings.CutSuffix(rest, "/_ducklake/catalog.db")
	if !ok || strings.Count(loc, "/") != 2 {
		return "", false
	}
	return s3Root + loc + "/", true
}

// registerWorkloadBackups builds the BackupSchedule, Backup and Restore reconcilers (ADR-0222) over the member kinds the
// providers serve (ADR-0311), in list order, and registers them: a Backup's change requeues its schedule and the
// Restores that read or wait for it. A target is never an add-on engine's address (ADR-0332 Decision 3).
func (p *Platform) registerWorkloadBackups(ctrl *controller.Controller, secrets backupsvc.SecretReader) error {
	c := p.cfg
	d := backupsvc.Deps{
		Store: c.store, Secrets: secrets, StoreURL: c.blobStoreURL, Limits: c.workloadBackupLimits,
		Period: c.pacing.SupervisionPeriod, Clock: c.backupClock, Meter: c.backupMeter, Logger: p.logger,
		Engines: backupEngines(c.store, c.runtime, p.addon.engineNamespaces),
	}
	for _, h := range lifecycle.Each[backupHook](p.phases) {
		d.Kinds = append(d.Kinds, h.BackupKinds()...)
	}
	if d.Meter == nil {
		d.Meter = backupMeter(c.telemetry)
	}
	if c.hold != nil {
		d.Hold = c.hold
	}
	rs, err := backupsvc.New(d)
	if err != nil {
		return err
	}
	p.backups = rs
	ctrl.Register(v1.KindBackupSchedule.GVK(), rs.Schedules)
	ctrl.Register(v1.KindBackup.GVK(), rs.Backups)
	ctrl.Register(v1.KindRestore.GVK(), rs.Restores)
	ctrl.Watches(v1.KindBackup.GVK(), rs.Schedules.MapBackup)
	ctrl.Watches(v1.KindBackup.GVK(), rs.Restores.MapBackup)
	return nil
}

// backupMeter is the meter of the funcd.backup metrics; a no-op one without telemetry.
func backupMeter(t *observability.Telemetry) metric.Meter {
	if t == nil {
		return metricnoop.NewMeterProvider().Meter("funcd.backup")
	}
	return t.MeterProvider().Meter("funcd.backup")
}

// purgePage is the most objects one listing of a Purge returns.
const purgePage = 1000

// bucketPurger is the gc.BucketPurger over a Bucket's raw substrate prefix (ADR-0199 Decision 7). Unlike
// s3BucketFor it resolves no Bucket resource, so it also purges the prefix of a deleted one.
type bucketPurger struct{ shared blob.Bucket }

// Purge deletes the Bucket's objects, then the catalogs of the Lakes on its prefixes (ADR-0293 Decision 5).
func (p bucketPurger) Purge(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName) error {
	if err := p.purge(ctx, ns, bucket, bucketPrefix(ns, bucket)); err != nil {
		return err
	}
	return p.purge(ctx, ns, bucket, bucketCatalogPrefix(ns, bucket))
}

func (p bucketPurger) purge(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName, prefix string) error {
	const op = "funcd.bucketPurger.Purge"
	view := blob.Prefixed(p.shared, prefix)
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

// bucketOrphans lists each <ns>/<bucket> with objects under bucketPrefix or catalogs under bucketCatalogPrefix and no
// Bucket (ADR-0206 Decision 6, ADR-0293 Decision 6), with the errors of the lookups it could not make. It reads one key
// per prefix and seeks past the rest, as the S3 frontend rolls up a common prefix.
func bucketOrphans(ctx context.Context, shared blob.Bucket, st store.Store) ([]v1.ObjectRef, error) {
	const op = "funcd.bucketOrphans"
	var out []v1.ObjectRef
	var errs []error
	seen := map[v1.ObjectRef]bool{}
	for _, root := range []string{s3Root, lakeCatalogRoot} {
		after := ""
		for {
			items, _, err := shared.ListAfter(ctx, root, after, 1)
			if err != nil {
				errs = append(errs, fault.Wrapf(err, fault.KindOf(err), op, "list bucket prefixes under %s", root))
				break
			}
			if len(items) == 0 {
				break
			}
			key := items[0].Key
			parts := strings.SplitN(strings.TrimPrefix(key, root), "/", 3)
			if len(parts) < 3 {
				after = key
				continue
			}
			ns, name := v1.NamespaceName(parts[0]), v1.ObjectName(parts[1])
			after = max(root+string(ns)+"/"+string(name)+"/"+string(utf8.MaxRune), key)
			ref := v1.ObjectRef{Kind: v1.KindBucket, Namespace: ns, Name: name}
			if ns.Validate() != nil || name.Validate() != nil || seen[ref] {
				continue
			}
			seen[ref] = true
			_, gerr := st.Get(ctx, v1.KindBucket.GVK(), ns, name)
			switch {
			case fault.KindOf(gerr) == fault.NotFound:
				out = append(out, ref)
			case gerr != nil:
				errs = append(errs, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get bucket %s/%s", ns, name))
			}
		}
	}
	return out, errors.Join(errs...)
}

// heldMarker reports the hold's marker while the platform is held (ADR-0206).
func (p *Platform) heldMarker() (hold.Marker, bool) {
	if p.cfg.hold == nil {
		return hold.Marker{}, false
	}
	return p.cfg.hold.Marker()
}

// holdPlatform is the controlplane.HoldPlatform over the assembled platform (ADR-0206 Decisions 1, 8).
type holdPlatform struct{ p *Platform }

func (h holdPlatform) Counts(ctx context.Context) (map[v1.Kind]int, error) {
	return restore.Counts(ctx, h.p.cfg.store)
}

func (h holdPlatform) Orphans(ctx context.Context) ([]string, error) {
	return h.p.kv.reconciler.Orphans(ctx)
}

func (h holdPlatform) BucketOrphans(ctx context.Context) ([]v1.ObjectRef, error) {
	return bucketOrphans(ctx, h.p.cfg.blob, h.p.cfg.store)
}

func (h holdPlatform) SQLOrphans(ctx context.Context) ([]v1.ObjectRef, error) {
	return h.p.sql.reconciler.Orphans(ctx)
}

// AddonOrphans are the add-on objects whose storage has no object, which a start not held quarantines (ADR-0333
// Decision 5, ADR-0231 Decision 4).
func (h holdPlatform) AddonOrphans(ctx context.Context) ([]v1.ObjectRef, error) {
	return h.p.addon.reconciler.Orphans(ctx)
}

func (h holdPlatform) VolumeOrphans(ctx context.Context) ([]v1.ObjectRef, error) {
	return h.p.fnReconciler.VolumeOrphans(ctx)
}

func (h holdPlatform) Advance(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) error {
	return h.p.eventing.blobWatcher.Advance(ctx, ns, source)
}

func (h holdPlatform) Pending(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) (map[string]int, error) {
	return h.p.eventing.blobWatcher.Pending(ctx, ns, source)
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

// lakeAt names the Lake of a namespace located on bucket/prefix, for the S3 gateway's catalog delete (ADR-0293).
func lakeAt(st store.Store) func(ctx context.Context, ns v1.NamespaceName, bucket, prefix string) (v1.ObjectName, bool, error) {
	return func(ctx context.Context, ns v1.NamespaceName, bucket, prefix string) (v1.ObjectName, bool, error) {
		l, err := st.List(ctx, v1.KindLake.GVK(), store.ListOptions{Namespace: ns})
		if err != nil {
			return "", false, err
		}
		for _, o := range l.Items {
			if lk, ok := o.(*v1.Lake); ok && string(lk.Spec.Location.Bucket) == bucket && lk.Spec.Location.Prefix == prefix {
				return lk.Name, true, nil
			}
		}
		return "", false, nil
	}
}

// blobBucketLister adapts the s3BucketFor resolver to the eventing.BucketLister the BlobWatcher polls
// (ADR-0119): it resolves (ns, Bucket) to the SAME prefixed substrate view external S3-frontend writes land
// in, then Lists the prefix over it. A missing Bucket is fault.NotFound and a failed read of it is that error (the
// watcher logs + skips that poll; the reconciler independently marks the source NotReady).
type blobBucketLister struct {
	resolve func(ns v1.NamespaceName, bucket string) (blob.Bucket, error)
}

func (l blobBucketLister) List(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName, prefix string) ([]blob.Attributes, error) {
	b, err := l.resolve(ns, string(bucket))
	if err != nil {
		return nil, err
	}
	return b.List(ctx, prefix)
}

// blobProber adapts blobBucketLister to admission.BlobProber (ADR-0080 deletion-protection): Remaining reports the
// objects under "<prefix>/" of the Bucket's substrate view, else a Lake's catalog for that prefix in the catalog store,
// whose inlined rows are the prefix's data too (ADR-0293), by the S3 key its writer deletes it with.
type blobProber struct {
	lister blobBucketLister
	shared blob.Bucket
}

func (p blobProber) Remaining(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName, prefix string) (string, error) {
	items, err := p.lister.List(ctx, ns, bucket, prefix+"/")
	switch {
	case err != nil:
		return "", err
	case len(items) > 0:
		return "objects", nil
	}
	key := v1.LakeCatalogKey(v1.LakeLocation{Bucket: bucket, Prefix: prefix})
	found, err := p.shared.Exists(ctx, lakeCatalogPrefix(ns)+key)
	if err != nil || !found {
		return "", err
	}
	return "its Lake catalog " + v1.LakeCatalogBucket + "/" + key, nil
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

// openVolumes opens the Functions' volume manager when WithFunctionVolumes turned volumes on (ADR-0262), with the
// namespace budget read from each Namespace and the holder read from each Function.
func (e *executor) openVolumes() error {
	c := e.cfg
	if c.functionVolumes == nil {
		return nil
	}
	o := *c.functionVolumes
	o.Logger = c.logger.With("component", "volume")
	o.Budget = func(ctx context.Context, ns v1.NamespaceName) (int64, error) {
		obj, err := c.store.Get(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
		if fault.KindOf(err) == fault.NotFound {
			return 0, nil
		}
		if err != nil {
			return 0, fault.Wrapf(err, fault.KindOf(err), "funcd.volumeBudget", "get namespace %q", ns)
		}
		if n, ok := obj.(*v1.Namespace); ok {
			return n.Spec.VolumeBudgetOf(), nil
		}
		return 0, nil
	}
	o.Holder = func(ctx context.Context, ns v1.NamespaceName, kind v1.Kind, name v1.ObjectName) (v1.UID, bool, error) {
		obj, err := c.store.Get(ctx, kind.GVK(), ns, name)
		if fault.KindOf(err) == fault.NotFound {
			return "", false, nil
		}
		if err != nil {
			return "", false, fault.Wrapf(err, fault.KindOf(err), "funcd.volumeHolder", "get %s %s/%s", kind, ns, name)
		}
		return obj.GetObjectMeta().UID, true, nil
	}
	// a stored Function or Job declares the volume, or a stored App's part the App has not written that way yet (#1208)
	o.Declared = func(ctx context.Context, id volume.ID) (bool, error) {
		const op = "funcd.volumeDeclared"
		declares := func(obj v1.Object) bool {
			var vols []v1.VolumeEntry
			switch w := obj.(type) {
			case *v1.Function:
				vols = w.Spec.Volumes
			case *v1.Job:
				vols = w.Spec.Volumes
			}
			return obj.GroupVersionKind().Kind == id.Kind && obj.GetName() == id.Workload &&
				slices.ContainsFunc(vols, func(v v1.VolumeEntry) bool { return v.Type == v1.VolumeLocal && v.VolumeName() == id.Name })
		}
		obj, err := c.store.Get(ctx, id.Kind.GVK(), id.Namespace, id.Workload)
		switch {
		case err == nil && declares(obj):
			return true, nil
		case err != nil && fault.KindOf(err) != fault.NotFound:
			return false, fault.Wrapf(err, fault.KindOf(err), op, "get %s %s/%s", id.Kind, id.Namespace, id.Workload)
		}
		apps, err := c.store.List(ctx, v1.KindApp.GVK(), store.ListOptions{Namespace: id.Namespace})
		if err != nil {
			return false, fault.Wrapf(err, fault.KindOf(err), op, "list apps in %q", id.Namespace)
		}
		for _, item := range apps.Items {
			if a, ok := item.(*v1.App); ok && slices.ContainsFunc(a.Parts(), declares) {
				return true, nil
			}
		}
		return false, nil
	}
	m, err := volume.New(o)
	if err != nil {
		return err
	}
	e.volumes = m
	return nil
}

// volumeBooker is the function-volumes admission's booker: nil (volumes off) without a manager.
func (p *Platform) volumeBooker() admission.VolumeBooker {
	if p.exec.volumes == nil {
		return nil
	}
	return volumeBooker{p.exec.volumes}
}

type volumeBooker struct{ m volume.Manager }

func (b volumeBooker) Book(ctx context.Context, ns v1.NamespaceName, kind v1.Kind, workload, name v1.ObjectName, sizeBytes int64) error {
	return b.m.Book(ctx, volume.ID{Namespace: ns, Kind: kind, Workload: workload, Name: name}, sizeBytes)
}

func (b volumeBooker) Fits(ctx context.Context, ns v1.NamespaceName, kind v1.Kind, workload, name v1.ObjectName, sizeBytes int64) error {
	return b.m.Fits(ctx, volume.ID{Namespace: ns, Kind: kind, Workload: workload, Name: name}, sizeBytes)
}

// volumeLoader adapts the volume manager to the workload backups (ADR-0262 Decision 11), and the Function reconciler's
// DataMissing and worker stop to a Restore's load (#1206).
type volumeLoader struct {
	m    volume.Manager
	lost func(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) (bool, error)
	stop func(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName) (func(), error)
}

func vid(ns v1.NamespaceName, fn, name v1.ObjectName) volume.ID {
	return volume.ID{Namespace: ns, Kind: v1.KindFunction, Workload: fn, Name: name}
}

func (l volumeLoader) Source(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) (io.ReadCloser, error) {
	return l.m.Tar(ctx, vid(ns, fn, name))
}

func (l volumeLoader) Load(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName, archive io.Reader) error {
	return l.m.Untar(ctx, vid(ns, fn, name), archive)
}

func (l volumeLoader) IsEmpty(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) (bool, error) {
	return l.m.Empty(ctx, vid(ns, fn, name))
}

func (l volumeLoader) Clear(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) error {
	return l.m.Clear(ctx, vid(ns, fn, name))
}

func (l volumeLoader) Lost(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) (bool, error) {
	return l.lost(ctx, ns, fn, name)
}

func (l volumeLoader) StopWorkers(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName) (func(), error) {
	return l.stop(ctx, ns, fn)
}

// volumeDeleter is the volume route's deleter: nil (no route) while volumes are off.
func (p *Platform) volumeDeleter() controlplane.VolumeDeleter {
	if p.exec.volumes == nil {
		return nil
	}
	return p.fnReconciler
}
