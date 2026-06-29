// Package funcd is the platform-as-a-library facade (ADR-0014): the composition
// root that wires every port into a *Platform and owns its start/stop lifecycle.
// Run serves the control-plane API and runs the controller (ADR-0028); cmd/funcd is
// a thin shell over it; e2e tests embed it with the InMemory() preset — no daemon,
// no root, no network beyond the ephemeral control-plane listener.
package funcd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/activator"
	"github.com/green-0-rabbit/funcd/internal/activator/storescaler"
	"github.com/green-0-rabbit/funcd/internal/artifact"
	"github.com/green-0-rabbit/funcd/internal/auth"
	cedarauth "github.com/green-0-rabbit/funcd/internal/auth/cedar"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/blob/s3gateway"
	"github.com/green-0-rabbit/funcd/internal/bus"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/controlplane"
	"github.com/green-0-rabbit/funcd/internal/controlplane/admission"
	"github.com/green-0-rabbit/funcd/internal/controlplane/middleware"
	"github.com/green-0-rabbit/funcd/internal/dataplane"
	"github.com/green-0-rabbit/funcd/internal/eventing"
	"github.com/green-0-rabbit/funcd/internal/funclog"
	"github.com/green-0-rabbit/funcd/internal/funclog/compact"
	"github.com/green-0-rabbit/funcd/internal/funclog/logread"
	"github.com/green-0-rabbit/funcd/internal/function"
	"github.com/green-0-rabbit/funcd/internal/gateway"
	"github.com/green-0-rabbit/funcd/internal/kvstore"
	kvmemory "github.com/green-0-rabbit/funcd/internal/kvstore/memory"
	"github.com/green-0-rabbit/funcd/internal/platform/clock"
	"github.com/green-0-rabbit/funcd/internal/platform/observability"
	"github.com/green-0-rabbit/funcd/internal/provider"
	"github.com/green-0-rabbit/funcd/internal/runtime"
	"github.com/green-0-rabbit/funcd/internal/scheduler/singlenode"
	"github.com/green-0-rabbit/funcd/internal/secrets"
	"github.com/green-0-rabbit/funcd/internal/services"
	blobsvc "github.com/green-0-rabbit/funcd/internal/services/blob"
	kvsvc "github.com/green-0-rabbit/funcd/internal/services/kv"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/workernode/local"
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
	// defaultKVStoresPerNamespace is the per-namespace KVStore count cap when unset (ADR-0072).
	defaultKVStoresPerNamespace = 100
	// defaultBucketsPerNamespace is the per-namespace Bucket count cap when unset (ADR-0080).
	defaultBucketsPerNamespace = 100
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
	blob                  blob.Bucket
	// funclog structured function-log capture (ADR-0081): on by default when the runtime supports it.
	funclogDisabled bool
	funclogMaxAge   time.Duration // segment seal age; 0 ⇒ sink default (10s)
	funclogMaxBytes int           // segment seal size; 0 ⇒ sink default (8 MiB)
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
	gateway              gateway.Gateway
	logger               *slog.Logger
	telemetry            *observability.Telemetry

	// control plane (ADR-0028)
	listenAddr  string
	credentials middleware.CredentialStore
	authorizer  auth.Authorizer
	localNode   v1.ObjectName

	// data plane (ADR-0033): the function-invocation listener address.
	dataPlaneAddr string

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
	poolLimit         int

	// S3 gateway (ADR-0080/0085): opt-in S3-protocol frontend over c.blob. Disabled ⇒ no
	// listener, no IAM, no keypair injection. master is loaded/generated by buildControlPlane.
	s3gwEnabled        bool
	s3gwListenAddr     string
	s3gwEndpoint       string // sandbox-facing S3 URL (ADR-0085); empty ⇒ http://<listenAddr>
	s3gwMaxUploadBytes int64
	s3gwMasterFile     string // optional; empty ⇒ generate+persist under the data dir
	s3gwDataDir        string // where the master.key is persisted when no master file is set
}

// validate returns the first missing required dependency as a fault.Invalid.
func (c *config) validate() error {
	const op = "funcd.New"
	switch {
	case c.store == nil:
		return fault.Invalidf(op, "store is required")
	case c.blob == nil:
		return fault.Invalidf(op, "blob is required")
	case c.bus == nil:
		return fault.Invalidf(op, "bus is required")
	case c.runtime == nil:
		return fault.Invalidf(op, "runtime is required")
	case c.gateway == nil:
		return fault.Invalidf(op, "gateway is required")
	case c.credentials == nil:
		return fault.Invalidf(op, "control-plane credentials are required (use WithDevAuth or a preset)")
	}
	return nil
}

// Platform is the assembled funcd runtime — the composition root built by New.
type Platform struct {
	cfg       *config
	logger    *slog.Logger
	providers *provider.Catalog // the platform provider catalog (ADR-0082)

	controller *controller.Controller
	eventing   *eventing.Source
	activator  *activator.Activator
	httpServer *http.Server
	listener   net.Listener
	addr       string

	dataPlaneServer   *http.Server // function-invocation listener (ADR-0033)
	dataPlaneListener net.Listener
	dataPlaneAddr     string

	invokeMgr *local.Manager     // per-function worker-node local API broker (ADR-0064)
	logSink   *funclog.BlobSink  // structured function-log capture sink (ADR-0081); nil if unwired
	compactor *compact.Compactor // funclog compacted compaction pipeline (ADR-0083); nil if unwired
	s3gw      *s3gateway.Server  // S3-protocol frontend (ADR-0080/0085); nil unless s3gwEnabled

	shutdownOnce sync.Once
	shutdownErr  error
}

// New assembles the platform from opts, validates every required dependency, builds
// the control plane (controller + reconcilers + API server), and binds the
// control-plane listener (so Addr() is ready before Run). It returns a typed
// fault (and a nil *Platform) on a missing dep or a build/bind failure — never a
// partial platform, never a panic.
func New(opts ...Option) (*Platform, error) {
	cfg := &config{}
	for _, o := range opts {
		if err := o(cfg); err != nil {
			return nil, err
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	if cfg.logger == nil {
		lg, err := observability.NewLogger(observability.Config{Format: observability.FormatText}, os.Stdout)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, "funcd.New", "build default logger")
		}
		cfg.logger = lg.Root()
	}

	pc, err := providerCatalog()
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "funcd.New", "build provider catalog")
	}

	p := &Platform{cfg: cfg, logger: cfg.logger, providers: pc}
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

	sched, err := singlenode.New(c.localNode)
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
			materializer = artifact.NewOrasMaterializer(c.artifactDir) // P-V-A: OCI pull by digest
		} else {
			materializer = function.NewFileMaterializer() // P-V-1: no-dep file:// stand-in
		}
	}
	// The oras materializer also resolves a tag → digest (ADR-0035); the file driver does
	// not, so a type-assert auto-wires the resolver only in OCI mode (nil for file://).
	resolver, _ := materializer.(function.ArtifactResolver)
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
		invokeSockDir = tmp
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
	cedarEntities, err := cedarauth.NewEntityProvider(cedarMetaReader{c.store})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build cedar entity provider")
	}
	cedarPDP, err := cedarauth.New(cedarauth.Deps{
		Entities: cedarEntities,
		Policies: policySource{c.store},
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
	p.invokeMgr = local.NewManager(invokeSockDir, c.store, local.NewInvoker(dpHolder), cedarPDP, kvFacade, p.logger)

	// S3 gateway (ADR-0080/0085): opt-in S3-protocol frontend over the blob substrate, reusing the
	// cedar PDP as the PEP. When enabled, load/generate the node master secret, build the server, and
	// expose the per-function keypair deriver to the reconciler for worker-env injection. Disabled ⇒
	// nothing is built (no listener, no IAM, no injection) — zero-config unchanged.
	var s3Injection function.S3GatewayInjection
	if c.s3gwEnabled {
		master, merr := s3gateway.LoadOrCreateMaster(c.s3gwMasterFile, c.s3gwDataDir)
		if merr != nil {
			return fault.Wrapf(merr, fault.KindOf(merr), op, "load s3gateway master secret")
		}
		bucketFor := s3BucketFor(c.blob, c.store)
		srv, gerr := s3gateway.New(s3gateway.Deps{
			BucketFor:      bucketFor,
			PDP:            cedarPDP,
			Master:         master,
			Listen:         c.s3gwListenAddr,
			MaxUploadBytes: c.s3gwMaxUploadBytes,
			Logger:         p.logger,
		})
		if gerr != nil {
			return fault.Wrapf(gerr, fault.KindOf(gerr), op, "build s3gateway")
		}
		p.s3gw = srv
		s3Injection = function.S3GatewayInjection{
			Enabled:    true,
			ListenAddr: c.s3gwListenAddr,
			Endpoint:   c.s3gwEndpoint,
			Derive: func(ns, fn string) (string, string) {
				kp := s3gateway.DeriveKeypair(master, ns, fn)
				return kp.AccessKey, kp.SecretKey
			},
		}
	}

	fnReconciler, err := function.NewReconciler(function.Deps{
		Store:                c.store,
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
		PoolShimCommand:      c.poolShim,
		PoolShimsByFamily:    c.poolShimsByFamily,
		PoolLimit:            c.poolLimit,
		S3Gateway:            s3Injection,
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
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build activator")
	}
	p.activator = act
	source, err := eventing.NewSource(eventing.Deps{
		Store:      c.store,
		Endpoints:  fnReconciler.Endpoints(),
		Waker:      act, // trigger-driven wake of a scaled-to-zero function (ADR-0033)
		Logger:     p.logger,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build eventing source")
	}
	p.eventing = source

	ctrl, err := controller.New(controller.Deps{Store: c.store, Logger: p.logger})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build controller")
	}
	ctrl.Register(v1.KindFunction.GVK(), fnReconciler)
	ctrl.Register(v1.KindService.GVK(), dispatcher)
	ctrl.Register(v1.KindEventSource.GVK(), source)
	// KVStore reconciler (ADR-0072/0073): Ready + status.tables/bindings; on delete reclaim the store
	// prefix and on a table removed from spec.tables[] reclaim its sub-prefix, via the driver's
	// DropPrefix+List (type-asserted PrefixManager — a driver without it gets a no-op).
	prefixMgr, _ := c.kvStore.(kvsvc.PrefixManager)
	kvReconciler, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: c.store, KV: prefixMgr, Logger: p.logger})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build KVStore reconciler")
	}
	ctrl.Register(v1.KindKVStore.GVK(), kvReconciler)
	p.controller = ctrl

	// ADR-0084: the function-log reader backing GET …/functions/{name}/logs (funcdctl logs). Present
	// whenever a blob substrate is — nil leaves the route unregistered.
	var logReader controlplane.LogQuerier
	if c.blob != nil {
		logReader = logread.NewBlobReader(c.blob)
	}
	handler, err := controlplane.NewServer(controlplane.Deps{
		Store:       c.store,
		Authorizer:  c.authorizer,
		Credentials: c.credentials,
		Logger:      p.logger,
		Logs:        logReader,
		Admissions: []admission.Admission{
			// ADR-0064 fn-to-fn link rules on the write path.
			admission.NewLinkValidityAdmission(storeReader{c.store}),
			admission.NewLinkDeletionProtectionAdmission(storeReader{c.store}),
			// ADR-0072/0073 KV resource rules: store-count quota; kv-binding-validity (Function.spec.kv
			// names an existing store/table); kv-owner-exists (KVStore tables[].owner is a real Function);
			// KVStore deletion-protection (bound by spec.kv or non-empty data on Delete; still-bound table
			// removal on Update).
			admission.NewKVStoreQuotaAdmission(storeReader{c.store}, kvMaxStores),
			admission.NewKVBindingValidityAdmission(storeReader{c.store}),
			admission.NewKVOwnerExistsAdmission(storeReader{c.store}),
			admission.NewKVStoreDeletionProtectionAdmission(storeReader{c.store}, kvProber{c.kvStore}),
			// ADR-0080 Bucket resource rules (the KVStore parallel): bucket-count quota; blob-binding-validity
			// (Function.spec.blob names an existing bucket/prefix); bucket-prefix-owner-exists (Bucket
			// prefixes[].owner is a real Function); bucket-deletion-protection (bound by spec.blob or non-empty
			// data on Delete; still-bound prefix removal on Update). The data-emptiness prober is nil until the
			// s3gateway data plane lands (a later slice) — binding-protection still applies (nil ⇒ skip the
			// data check, the optional-prober pattern KVStore uses).
			admission.NewBucketQuotaAdmission(storeReader{c.store}, bucketMax),
			admission.NewBlobBindingValidityAdmission(storeReader{c.store}),
			admission.NewBucketPrefixOwnerExistsAdmission(storeReader{c.store}),
			admission.NewBucketDeletionProtectionAdmission(storeReader{c.store}, nil),
			// ADR-0074 Policy validity: spec.cedar parses + references only the curated schema
			// (kv::read/kv::write; Function/KVStore/KVTable) — so every stored Policy compiles.
			admission.NewPolicyValidityAdmission(),
		},
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build control-plane server")
	}
	p.httpServer = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	ln, err := net.Listen("tcp", c.listenAddr)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "bind control-plane listener on %s", c.listenAddr)
	}
	p.listener = ln
	p.addr = ln.Addr().String()

	// Data plane (ADR-0033): a SEPARATE listener serving function invocations through the
	// activator (path+store → activator), distinct from the authenticated control plane.
	dpHandler := gateway.Chain(dataplane.Handler(c.store, act, p.logger), gateway.Recover, gateway.RequestID)
	dpHolder.Set(dpHandler) // late-bind the data-plane handler into the worker-node local API invoker (ADR-0064)
	p.dataPlaneServer = &http.Server{Handler: dpHandler, ReadHeaderTimeout: 10 * time.Second}
	dln, err := net.Listen("tcp", c.dataPlaneAddr)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "bind data-plane listener on %s", c.dataPlaneAddr)
	}
	p.dataPlaneListener = dln
	p.dataPlaneAddr = dln.Addr().String()

	// ADR-0081: structured function-log capture (Path B). If the runtime driver implements the
	// LogCapturer capability and a blob substrate is present, build the funclog sink and install the
	// per-instance capture hook — a Pump per channel that drains the shim's NDJSON into the sink.
	// (Path A, raw stdout/stderr, stays the runtime's own log file.)
	if lc, ok := c.runtime.(runtime.LogCapturer); ok && c.blob != nil && !c.funclogDisabled {
		sink, serr := funclog.NewBlobSink(funclog.Deps{
			Bucket: c.blob, Clock: clock.System(), Logger: p.logger,
			SegmentMaxAge: c.funclogMaxAge, SegmentMaxBytes: c.funclogMaxBytes,
		})
		if serr != nil {
			return fault.Wrapf(serr, fault.Internal, op, "build funclog sink")
		}
		p.logSink = sink
		lc.SetLogCapture(func(spec runtime.WorkerSpec, r io.ReadCloser) {
			res := funclog.Resource{
				Namespace: string(spec.Namespace),
				Function:  string(spec.Name),
				Replica:   strconv.Itoa(spec.Replica),
			}
			go func() {
				defer func() { _ = r.Close() }()
				_ = funclog.Pump(context.Background(), funclog.NewNDJSONReader(r), sink, res, p.logger)
			}()
		})
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

// Run starts the control loops + the control-plane server and blocks until ctx is
// cancelled, then shuts down gracefully and returns nil. It owns the crash-only
// lifecycle (ADR-0028).
func (p *Platform) Run(ctx context.Context) error {
	p.logger.InfoContext(ctx, "platform starting", "addr", p.addr, "dataPlaneAddr", p.dataPlaneAddr)
	p.logProviders(ctx)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		if err := p.controller.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.logger.ErrorContext(ctx, "controller stopped", "error", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := p.eventing.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.logger.ErrorContext(ctx, "eventing stopped", "error", err)
		}
	}()
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
	if p.s3gw != nil { // ADR-0080/0085: the S3-protocol frontend listener (opt-in; stops on ctx cancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.s3gw.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.logger.ErrorContext(ctx, "s3 gateway stopped", "error", err)
			}
		}()
	}
	go func() {
		if err := p.httpServer.Serve(p.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.logger.ErrorContext(ctx, "control-plane server stopped", "error", err)
		}
	}()
	go func() {
		if err := p.dataPlaneServer.Serve(p.dataPlaneListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.logger.ErrorContext(ctx, "data-plane server stopped", "error", err)
		}
	}()

	<-ctx.Done()

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	p.logger.InfoContext(stopCtx, "platform stopping")
	// Drain both HTTP servers (stop accepting) before the ports close — an in-flight
	// invocation needs the runtime/store (ADR-0033).
	_ = p.dataPlaneServer.Shutdown(stopCtx)
	_ = p.httpServer.Shutdown(stopCtx)
	wg.Wait() // drain controller + eventing + activator before closing ports
	return p.Shutdown(stopCtx)
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
		if cl, ok := p.cfg.kvStore.(io.Closer); ok { // the durable KV driver (ADR-0066/0069)
			_ = cl.Close()
		}
		// Stop the S3 gateway (ADR-0080/0085) before blob.Close — it serves from the blob substrate.
		var s3gwErr error
		if p.s3gw != nil {
			s3gwErr = p.s3gw.Close()
		}
		// Close the runtime first (stops instances → log channels EOF → pumps flush), then seal any
		// remaining funclog segments, all before blob.Close() (the sink writes to blob) — ADR-0081.
		runtimeErr := p.cfg.runtime.Close()
		var logSinkErr error
		if p.logSink != nil {
			logSinkErr = p.logSink.Close()
		}
		errs := []error{
			s3gwErr,
			p.cfg.bus.Close(),
			p.cfg.gateway.Close(),
			runtimeErr,
			logSinkErr,
			p.cfg.blob.Close(),
			p.cfg.store.Close(),
		}
		if p.cfg.telemetry != nil {
			errs = append(errs, p.cfg.telemetry.Shutdown(ctx))
		}
		p.shutdownErr = errors.Join(errs...)
	})
	return p.shutdownErr
}

// storeReader adapts store.Store to admission.StoreReader for the ADR-0064 link admissions and the
// ADR-0072 KV admissions (the admission package stays a near-leaf and does not import store).
type storeReader struct{ s store.Store }

func (r storeReader) List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	res, err := r.s.List(ctx, gvk, store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
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

// policySource adapts store.Store to cedarauth.PolicySource (ADR-0074): it lists every v1.Policy
// (cluster-wide — namespace is encoded in the Cedar entity IDs) and returns the store-wide
// resourceVersion as the cache revision, so the cedar driver recompiles only on a Policy change.
type policySource struct{ s store.Store }

func (p policySource) Policies(ctx context.Context) ([]v1.Policy, string, error) {
	const op = "funcd.policySource.Policies"
	res, err := p.s.List(ctx, v1.KindPolicy.GVK(), store.ListOptions{})
	if err != nil {
		return nil, "", fault.Wrapf(err, fault.KindOf(err), op, "list policies")
	}
	out := make([]v1.Policy, 0, len(res.Items))
	for _, o := range res.Items {
		if pol, ok := o.(*v1.Policy); ok {
			out = append(out, *pol)
		}
	}
	return out, res.ResourceVersion, nil
}

// s3BucketFor builds the s3gateway BucketFor resolver (ADR-0080): it maps an S3
// (namespace, bucket-name) to a prefixed view of the single shared blob substrate,
// resolving ok=true only when a Bucket of that name exists in that namespace. The
// namespacing scheme is a key-prefix view `s3/<ns>/<bucket>/` over the shared bucket —
// one substrate bucket, many logical S3 buckets — so distinct namespaces and buckets
// never collide. Existence-by-namespace here gives tenancy a second guard (a missing /
// cross-namespace bucket is NoSuchBucket); the binding-as-grant Cedar PEP is the
// authorization gate on every object op.
func s3BucketFor(shared blob.Bucket, st store.Store) func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool) {
	return func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool) {
		if bucket == "" {
			return nil, false
		}
		if _, err := st.Get(context.Background(), v1.KindBucket.GVK(), ns, v1.ObjectName(bucket)); err != nil {
			return nil, false
		}
		return blob.Prefixed(shared, "s3/"+string(ns)+"/"+bucket+"/"), true
	}
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
