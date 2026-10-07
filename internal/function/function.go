// Package function is the Function lifecycle reconciler (ADR-0020): the one
// controller.Reconciler for KindFunction. It stamps immutable Revisions, schedules +
// provisions workers via the runtime port to the EFFECTIVE desired replica count
// (honoring the activator's wake Status.Phase, not blindly spec.replicas), validates
// the shape (materialization gate — no route to a broken function), programs the gateway
// with the FULL route table (gateway.ProgramRoutes is replace-all), and writes status.
// It also provides the production activator.Endpoints (the seam ADR-0016 deferred to P-M).
package function

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/envresolve"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/pooling"
	"github.com/pyvvo/funcd/internal/revhold"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/scheduler"
	catalogsvc "github.com/pyvvo/funcd/internal/services/catalog"
	"github.com/pyvvo/funcd/internal/store"
)

const (
	condReady         v1.ConditionType = "Ready"
	condShapeValid    v1.ConditionType = "ShapeValid"
	condPoolFull      v1.ConditionType = "PoolFull"      // ADR-0046: over-cap pooled member held NotReady
	condRevisionReady v1.ConditionType = "RevisionReady" // ADR-0143: whether the latest generation serves
	condAsleep        v1.ConditionType = "Asleep"        // True: a gate held this Function Pending or Failed while asleep; no worker runs (ADR-0192)
	upstreamPort                       = "8080"
)

// errArtifactUnresolved marks a Function whose artifact ref could not be resolved to a
// digest (ADR-0035) — the reconciler maps it to Phase=Failed + reason ArtifactUnresolved.
var errArtifactUnresolved = errors.New("artifact ref could not be resolved to a digest")

// errRevisionMissing marks a generation whose stamped Revision is gone, and errRevisionStampFailed one whose Revision
// cannot be stamped: the reconciler maps them to Phase=Failed + reasons RevisionMissing and RevisionStampFailed
// (ADR-0172).
var (
	errRevisionMissing     = errors.New("the stamped revision of this generation is missing")
	errRevisionStampFailed = errors.New("revision could not be stamped")
)

// ShapeValidator validates a Function's artifact conforms to its runtime shape.
type ShapeValidator interface {
	Validate(ctx context.Context, fn *v1.Function) error
}

// InvokeSocketProvider supplies the per-function worker-node local API socket path (ADR-0064): the
// reconciler sets it as FUNCD_INVOKE_SOCKET in the worker env so the shim can dial context.invoke.
// nil ⇒ fn-to-fn links are off (the env var is unset; invoke fails closed in the shim).
type InvokeSocketProvider interface {
	SocketFor(ns v1.NamespaceName, name v1.ObjectName) (string, error)
	// PoolSocketFor provisions the one local API socket of pool worker `pool`, on which each call names its member;
	// members is the set it serves, replaced on every call.
	PoolSocketFor(ns v1.NamespaceName, pool v1.ObjectName, members []v1.ObjectName) (string, error)
	// Remove stops + deletes the function's local API listener (called from teardown on delete, so
	// the socket lifecycle tracks the Function resource — controller-driven, not leaked).
	Remove(ns v1.NamespaceName, name v1.ObjectName)
}

// Deps configures the Function lifecycle reconciler (internal component, ADR-0002 §1).
type Deps struct {
	Store     store.Store
	Runtime   runtime.Runtime
	Scheduler scheduler.Scheduler
	Gateway   gateway.Gateway
	Validator ShapeValidator
	Logger    *slog.Logger
	// LogMaxRecordBytes is FUNCD_FUNCLOG_MAX_RECORD_BYTES for every shim and pool host (ADR-0168); 0 sets none.
	LogMaxRecordBytes int
	// Materializer + ShimCommand enable real function execution (ADR-0030): the
	// reconciler materializes the artifact and launches the runtime shim, and gates
	// readiness on the shim's /health/readiness. When Materializer is nil the reconciler
	// runs the legacy long-lived placeholder (ADR-0020) — used by the runtime-port tests.
	Materializer Materializer
	ShimCommand  []string // launch prefix for the DEFAULT shim, e.g. ["node", "/.../shim.mjs"]
	// ShimCommandsByFamily maps a runtime-family prefix → its shim launch prefix (ADR-0049): a
	// function whose spec.runtime starts with the key (e.g. "python") is launched with that
	// command instead of ShimCommand. Empty ⇒ every runtime uses the default ShimCommand (node).
	ShimCommandsByFamily map[string][]string

	// Resolver (ADR-0035) resolves an OCI artifact ref → digest at Revision-stamp time, so
	// the user need not pin the digest. nil → no resolution (file:// dev / legacy mode).
	Resolver ArtifactResolver
	// Platforms (ADR-0145) lists an artifact digest's platforms for the placement gate. nil → no gate.
	Platforms PlatformResolver

	// EndpointMode + ImageFor select container execution (ADR-0032). EndpointLoopback
	// (default) is the process driver: ShimCommand launches the shim, loopback+portfile.
	// EndpointNetnsFixedPort is the containerd driver: the shim is the curated IMAGE's
	// entrypoint (Command empty), the artifact is bind-mounted, and it binds a fixed port
	// on the netns IP. ImageFor maps fn.Spec.Runtime → the curated image ref.
	EndpointMode EndpointMode
	ImageFor     func(runtime string) string

	// PoolShimCommand enables worker pooling (ADR-0046): the launch prefix for the pooled
	// worker_threads host (e.g. ["node", "/.../pool.mjs"]). When set, a function declaring
	// spec.pooling.worker co-locates with same-key peers in one pool worker launched with
	// this command + FUNCD_POOL_MANIFEST. Empty ⇒ pooling is off (every function is solo).
	PoolShimCommand []string
	// PoolShimsByFamily maps a runtime-family prefix → its pool-host launch prefix (ADR-0050): a
	// python* function that opts in co-pools via this host (the subinterpreter pool.py) instead of
	// staying solo. Empty ⇒ only PoolShimCommand (node) pools; a family with no host runs solo.
	PoolShimsByFamily map[string][]string
	// PoolLimit caps handlers per pool worker (ADR-0046 Decision 3); 0 ⇒ the default (16).
	PoolLimit int
	// PoolManifestDir holds the pool workers' manifests, which carry member credentials: a 0700 dir funcd owns,
	// emptied by NewReconciler. Empty ⇒ a private temp dir NewReconciler creates and Close removes.
	PoolManifestDir string

	// Secrets resolves a function's bound Secret names → an env-var map injected into the
	// worker (ADR-0057, F15 last mile). nil ⇒ secret injection disabled: a function declaring
	// spec.secrets fails closed (SecretResolveFailed). Satisfied by *secrets.Resolver.
	Secrets SecretResolver
	// DeveloperFor returns the identity the PDP authorizes for a namespace's secret reads
	// (ADR-0057 Decision 4). Defaulted to the namespace-scoped developer identity when Secrets
	// is set and this is nil; ignored when Secrets is nil.
	DeveloperFor func(ns v1.NamespaceName) auth.Identity

	// InvokeSockets provisions the per-function worker-node local API (ADR-0064) and yields the
	// socket path the reconciler sets as FUNCD_INVOKE_SOCKET. nil ⇒ fn-to-fn links off.
	InvokeSockets InvokeSocketProvider

	// S3Gateway, when Enabled, injects a per-function SigV4 keypair + endpoint into the worker
	// env (ADR-0085) for a function that declares spec.blob — AWS_ACCESS_KEY_ID/
	// AWS_SECRET_ACCESS_KEY (DeriveKeypair over Master), AWS_REGION, AWS_ENDPOINT_URL_S3
	// (ListenAddr). A function without spec.blob, or a disabled gateway, gets nothing.
	S3Gateway S3GatewayInjection

	// CatalogMaster is the node master secret the per-function catalog token is derived from (ADR-0137):
	// resolveCatalogEnv injects FUNCD_CATALOG_<ALIAS>_TOKEN = DeriveCatalogToken(master, ns, fn), a
	// MAC-authenticated bearer the catalog PEP proxy verifies (was the shared QUACK_TOKEN). It is the
	// SAME master the S3 gateway derives keypairs from (loaded once at the compose root). nil/empty ⇒ a
	// still-derivable but unverifiable token (the proxy holds the real master); the master is never logged.
	CatalogMaster []byte

	// CatalogProxies reports the catalog listeners bound in this daemon run (ADR-0137, ADR-0162): a consumer is
	// injected its catalog's bound listener URL, and waits while none is bound. nil ⇒ the catalog's status.endpoint,
	// for harnesses without a Manager.
	CatalogProxies CatalogProxies

	// CatalogExtensionDir, when non-empty, is injected as DUCKDB_EXTENSION_DIRECTORY into a
	// catalog-consumer function's worker env (a function declaring spec.catalogs) so its handler's
	// `LOAD quack`/`ducklake` resolves the curated DuckDB extensions from this dir. It is the dev
	// analogue of the prod bundle's duckdb-ext (ADR-0089): `funcdctl dev` extracts the embedded
	// catalog engine's extensions once and points consumers at them; prod leaves it empty (the
	// bundle carries duckdb-ext under FUNCD_BUNDLE_DIR instead). Empty ⇒ no injection.
	CatalogExtensionDir string

	// SupervisionPeriod is the requeue of a Ready function (ADR-0142); 0 ⇒ controller.SupervisionPeriod.
	SupervisionPeriod time.Duration
	// BootBackoffInitial and BootBackoffMax bound the wait before a replica that crashed while booting is created
	// again (ADR-0160); 0 ⇒ 10 s and max(5 min, initial). Negative, or a set max below initial, is fault.Invalid.
	BootBackoffInitial time.Duration
	BootBackoffMax     time.Duration

	// Calls counts the calls made to each worker (ADR-0143): the resolver records each hand-out and the drain asks
	// whether a demoted revision's worker is idle. nil ⇒ every worker is idle.
	Calls *activator.CallTracker
	// DrainGrace bounds how long a demoted revision's workers drain; 0 ⇒ 30 s (ADR-0143).
	DrainGrace time.Duration
	// HandOutSettle is how long after a hand-out, or after the switch, a worker may still receive a call; 0 ⇒ 2 s.
	HandOutSettle time.Duration
	// DrainPollInterval is the longest gap between drain passes (runtime.drainPollInterval, ADR-0163); 0 ⇒ 1 s.
	DrainPollInterval time.Duration
	// BootTimeout stops a replica not ready this long after its last successful Start (runtime.bootTimeout, ADR-0163,
	// ADR-0183); 0 ⇒ 1 min.
	BootTimeout time.Duration
	// ReferentPollInterval re-checks a function waiting for a referent (controller.referentPollInterval, ADR-0163),
	// bounded by the supervision period (ADR-0142 Decision 9); 0 ⇒ 2 s.
	ReferentPollInterval time.Duration
	// Clock times the drain from drainingSince; nil ⇒ clock.System().
	Clock clock.Clock
}

// CatalogProxies reports the URL of a CatalogService's PEP proxy listener bound in this daemon run (ADR-0137,
// ADR-0162), false when none is bound. Satisfied by *cataloggw.Manager.
type CatalogProxies interface {
	ProxyURL(ns v1.NamespaceName, name v1.ObjectName) (string, bool)
}

// S3GatewayInjection configures the worker-env S3 keypair injection (ADR-0085). Derive computes
// the per-function (access, secret) from the node master over the Ref — supplied by the wiring
// (pkg/funcd) so this package stays free of the versitygw dependency the gateway carries; the
// master secret it closes over is never logged. The zero value (Enabled false) injects nothing.
type S3GatewayInjection struct {
	Enabled    bool
	ListenAddr string
	// Endpoint is the sandbox-facing S3 URL (ADR-0085): the node address a netns'd worker can reach —
	// the CNI bridge gateway IP under containerd. Empty ⇒ derived as http://<ListenAddr> (loopback dev).
	Endpoint string
	// Derive returns the deterministic per-(kind, ns, name) SigV4 keypair (s3gateway.DeriveKeypair,
	// bound to the node master secret; ADR-0175). Required when Enabled.
	Derive func(kind v1.Kind, ns, name string) (access, secret string)
}

// EndpointMode selects how a worker is ADDRESSED (ADR-0032); it is orthogonal to the
// Materializer-presence gate that selects the shim path.
type EndpointMode int

const (
	// EndpointLoopback addresses the shim on 127.0.0.1 via the FUNCD_PORTFILE handshake
	// (process driver, ADR-0030).
	EndpointLoopback EndpointMode = iota
	// EndpointNetnsFixedPort addresses the shim on the netns IP at a fixed port (containerd
	// driver, ADR-0032).
	EndpointNetnsFixedPort
)

const (
	// containerArtifactDir is where the artifact is bind-mounted in container mode (ADR-0032).
	containerArtifactDir = "/var/funcd/artifact"
	// containerShimPort is the fixed port the shim binds inside its netns (ADR-0032).
	containerShimPort = 8080
	// containerInvokeSocket is the in-sandbox path the worker-node local API socket is bind-mounted
	// to in container mode (ADR-0064); the shim dials it via FUNCD_INVOKE_SOCKET.
	containerInvokeSocket = "/run/funcd/invoke.sock"
)

// Reconciler is the one controller.Reconciler for KindFunction.
type Reconciler struct {
	logMaxRecordBytes int // FUNCD_FUNCLOG_MAX_RECORD_BYTES for every shim and pool host (ADR-0168); 0 sets none
	store             store.Store
	runtime           runtime.Runtime
	scheduler         scheduler.Scheduler
	gateway           gateway.Gateway
	validator         ShapeValidator
	logger            *slog.Logger
	materializer      Materializer // nil → legacy placeholder mode (no real execution)
	shimCommand       []string
	shimByFamily      map[string][]string // runtime-family prefix → shim command (ADR-0049)
	endpointMode      EndpointMode
	imageFor          func(string) string
	resolver          ArtifactResolver // nil → no digest resolution (ADR-0035)
	platformsOf       PlatformResolver // nil → no platform gate (ADR-0145)
	httpClient        *http.Client

	// secrets (ADR-0057) resolves a function's bound Secret names → env vars; nil → injection
	// disabled (a function declaring spec.secrets fails closed). developerFor supplies the PDP
	// identity for the read (defaulted to the namespace-scoped developer when secrets are wired).
	secrets      SecretResolver
	developerFor func(ns v1.NamespaceName) auth.Identity

	// invokeSockets provisions the per-function worker-node local API (ADR-0064); nil ⇒ links off.
	invokeSockets InvokeSocketProvider

	// s3Gateway injects the per-function S3 keypair env (ADR-0085) when enabled and the function
	// declares spec.blob. The master secret is never logged.
	s3Gateway S3GatewayInjection

	// catalogMaster is the node master the per-function catalog token is derived from (ADR-0137);
	// nil/empty ⇒ catalog injection derives a token the proxy cannot verify. Never logged.
	catalogMaster []byte

	// catalogProxies reports the catalog listeners bound in this daemon run (ADR-0137, ADR-0162); nil ⇒ status.endpoint.
	catalogProxies CatalogProxies

	// catalogExtensionDir, when non-empty, is injected as DUCKDB_EXTENSION_DIRECTORY into a
	// catalog-consumer function's worker env (dev analogue of the prod bundle's duckdb-ext, ADR-0089).
	catalogExtensionDir string

	// pooling (ADR-0046/0050): the pure placement policy + per-family pool-host launch commands + cap.
	// A function pools iff a pool host exists for its runtime family (poolKeyFor); none ⇒ solo.
	assigner          pooling.Assigner
	poolShimCommand   []string
	poolShimsByFamily map[string][]string // runtime-family prefix → pool-host command (ADR-0050)
	poolLimit         int
	// poolDrains is when the drain of each key's old pool workers started (ADR-0190 Decision 8); poolHolds is the
	// manifest each key's pool workers were last built to hold; poolSets is each pool worker's member set ("ns/worker" →
	// names), recorded before it is created; poolLive is when each key's pool worker last answered its liveness. All
	// guarded by poolMu, for concurrent reconciles of sibling members of the same pool.
	poolMu     sync.Mutex
	poolDrains map[pooling.PoolKey]poolDrain
	poolHolds  map[pooling.PoolKey]poolHold
	poolSets   map[string][]v1.ObjectName
	poolLive   map[pooling.PoolKey]poolLiveness
	// poolManifestDir holds the pool manifests; ownManifestDir is set when NewReconciler created it, for Close.
	poolManifestDir string
	ownManifestDir  bool

	// supervisionPeriod is the steady-state requeue and the replacement backoff (ADR-0142).
	supervisionPeriod time.Duration
	// boot counts the solo replicas' boot crashes (ADR-0160).
	boot *bootBackoff

	// calls, drainGrace, handOutSettle and clock drive the drain of a demoted revision (ADR-0143).
	calls         *activator.CallTracker
	drainGrace    time.Duration
	handOutSettle time.Duration
	drainPoll     time.Duration
	clock         clock.Clock

	// bootTimeout bounds a replica's boot; referentPoll is the referent-gate requeue (ADR-0163).
	bootTimeout  time.Duration
	referentPoll time.Duration

	// spared names, by Function, the UID of each Function whose last retire pass kept workers of a revision an open
	// workflow run holds (ADR-0190 Decision 7): its passes skip the steady state until a pass retires them.
	sparedMu sync.Mutex
	spared   map[fnKey]v1.UID
	// held names the Functions whose held revisions the next pass must look at (ADR-0190 Decisions 5 and 6): one a
	// Revision event moved to Deploying or Idle, or one with a held revision still active. It is a hint from the
	// Revision watch, guarded by sparedMu; the store's Revision.status stays the truth.
	held map[fnKey]bool
}

// fnKey names a Function in the spared set.
type fnKey struct {
	ns   v1.NamespaceName
	name v1.ObjectName
}

const (
	defaultDrainGrace        = 30 * time.Second
	defaultHandOutSettle     = 2 * time.Second
	defaultDrainPollInterval = time.Second
	defaultReferentPoll      = 2 * time.Second
)

// defaultPoolLimit is the per-pool handler cap when Deps.PoolLimit is unset (ADR-0046 Decision 3).
const defaultPoolLimit = 16

// NewReconciler builds the Function lifecycle reconciler. The ports are required;
// Materializer/ShimCommand are optional (set together to enable execution, ADR-0030).
func NewReconciler(d Deps) (*Reconciler, error) {
	const op = "function.NewReconciler"
	switch {
	case d.Store == nil:
		return nil, fault.Invalidf(op, "store is required")
	case d.Runtime == nil:
		return nil, fault.Invalidf(op, "runtime is required")
	case d.Scheduler == nil:
		return nil, fault.Invalidf(op, "scheduler is required")
	case d.Gateway == nil:
		return nil, fault.Invalidf(op, "gateway is required")
	case d.Validator == nil:
		return nil, fault.Invalidf(op, "validator is required")
	// In process mode (EndpointLoopback) the shim is launched via ShimCommand; in container
	// mode (EndpointNetnsFixedPort) the shim is the curated image's entrypoint, so no command.
	case d.Materializer != nil && d.EndpointMode == EndpointLoopback && len(d.ShimCommand) == 0:
		return nil, fault.Invalidf(op, "ShimCommand is required when a Materializer is set in process mode")
	case d.Materializer != nil && d.EndpointMode == EndpointNetnsFixedPort && d.ImageFor == nil:
		return nil, fault.Invalidf(op, "ImageFor is required when a Materializer is set in container mode")
	case d.BootBackoffInitial < 0 || d.BootBackoffMax < 0 || d.BootTimeout < 0:
		return nil, fault.Invalidf(op, "the boot backoff must not be negative")
	}
	bootInitial := d.BootBackoffInitial
	if bootInitial == 0 {
		bootInitial = defaultBootBackoffInitial
	}
	bootMax := d.BootBackoffMax
	switch {
	case bootMax == 0:
		bootMax = max(defaultBootBackoffMax, bootInitial)
	case bootMax < bootInitial:
		return nil, fault.Invalidf(op, "the boot backoff max %s is below its initial wait %s", bootMax, bootInitial)
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	manifestDir, ownManifestDir, err := preparePoolManifestDir(d.PoolManifestDir)
	if err != nil {
		return nil, err
	}
	limit := d.PoolLimit
	if limit <= 0 {
		limit = defaultPoolLimit
	}
	// Secret injection (ADR-0057): when a resolver is wired but no identity provider is given,
	// default to the namespace-scoped developer identity (the V1 single-tenant model).
	developerFor := d.DeveloperFor
	if d.Secrets != nil && developerFor == nil {
		developerFor = defaultDeveloperFor
	}
	period := d.SupervisionPeriod
	if period <= 0 {
		period = controller.SupervisionPeriod
	}
	drainGrace := d.DrainGrace
	if drainGrace <= 0 {
		drainGrace = defaultDrainGrace
	}
	settle := d.HandOutSettle
	if settle <= 0 {
		settle = defaultHandOutSettle
	}
	clk := d.Clock
	if clk == nil {
		clk = clock.System()
	}
	drainPoll := d.DrainPollInterval
	if drainPoll <= 0 {
		drainPoll = defaultDrainPollInterval
	}
	bootTO := d.BootTimeout
	if bootTO == 0 {
		bootTO = defaultBootTimeout
	}
	referentPoll := d.ReferentPollInterval
	if referentPoll <= 0 {
		referentPoll = defaultReferentPoll
	}
	return &Reconciler{
		store: d.Store, runtime: d.Runtime, scheduler: d.Scheduler,
		gateway: d.Gateway, validator: d.Validator, logger: logger.With("component", "function"),
		logMaxRecordBytes: d.LogMaxRecordBytes,
		materializer:      d.Materializer, shimCommand: d.ShimCommand, shimByFamily: d.ShimCommandsByFamily,
		endpointMode: d.EndpointMode, imageFor: d.ImageFor, resolver: d.Resolver, platformsOf: d.Platforms,
		httpClient:          httpx.NodeClient(probeTimeout),
		secrets:             d.Secrets,
		developerFor:        developerFor,
		invokeSockets:       d.InvokeSockets,
		s3Gateway:           d.S3Gateway,
		catalogMaster:       d.CatalogMaster,
		catalogExtensionDir: d.CatalogExtensionDir,
		catalogProxies:      d.CatalogProxies,
		assigner:            pooling.NewAssigner(),
		poolShimCommand:     d.PoolShimCommand,
		poolShimsByFamily:   d.PoolShimsByFamily,
		poolLimit:           limit,
		poolDrains:          map[pooling.PoolKey]poolDrain{},
		poolHolds:           map[pooling.PoolKey]poolHold{},
		poolSets:            map[string][]v1.ObjectName{},
		poolLive:            map[pooling.PoolKey]poolLiveness{},
		poolManifestDir:     manifestDir,
		ownManifestDir:      ownManifestDir,
		supervisionPeriod:   period,
		boot:                newBootBackoff(bootInitial, bootMax, bootTO, logger.With("component", "function")),
		calls:               d.Calls,
		drainGrace:          drainGrace,
		handOutSettle:       settle,
		drainPoll:           drainPoll,
		clock:               clk,
		bootTimeout:         bootTO,
		referentPoll:        min(referentPoll, period),
		spared:              map[fnKey]v1.UID{},
		held:                map[fnKey]bool{},
	}, nil
}

// Reconcile converges one Function to its desired state.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "function.Reconcile"
	obj, err := r.store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			// delete path: stop the function's workers, then re-program routes without it.
			if derr := r.teardown(ctx, req.Namespace, req.Name); derr != nil {
				return controller.Result{}, derr
			}
			gone := &v1.Function{ObjectMeta: v1.ObjectMeta{Namespace: req.Namespace, Name: req.Name}}
			r.setSpared(gone, false)
			r.takeHeld(gone)
			if derr := r.reclaimOrphanPools(ctx, req.Namespace); derr != nil {
				return controller.Result{}, derr
			}
			return controller.Result{}, r.programAllRoutes(ctx)
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get function")
	}
	fn, ok := obj.(*v1.Function)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not a Function", req.Namespace, req.Name)
	}
	// Conditions.Set replaces in place, so the snapshot owns its conditions (ADR-0161 Decision 1).
	read := fn.Status
	read.Conditions = slices.Clone(fn.Status.Conditions)
	res, err := r.reconcileFunction(ctx, fn)
	if err == nil || errors.As(err, new(routeError)) {
		return res, err
	}
	return r.failPass(ctx, fn, read, err)
}

// reconcileFunction is a pass over a stored Function. An error it returns before its own status write is written to the
// status by failPass; one after it is a routeError (ADR-0161 Decision 1).
func (r *Reconciler) reconcileFunction(ctx context.Context, fn *v1.Function) (controller.Result, error) {
	var err error
	// A spec change can move fn off its pool key (spec.pooling.worker or spec.runtime), leaving that pool without a member.
	if fn.Status.ObservedGeneration < fn.Generation {
		if err := r.reclaimOrphanPools(ctx, fn.Namespace); err != nil {
			return controller.Result{}, err
		}
	}
	// ADR-0142: a Ready solo Function whose spec is already processed only needs its replicas checked; if they
	// all run, the pass writes nothing and comes back after the supervision period.
	if r.steadyState(ctx, fn) {
		return controller.Result{RequeueAfter: r.supervisionPeriod}, nil
	}

	// status.pool names the pool fn's access puts it in, before any gate; a key that moved leaves its old pool to
	// reclaim once no Function has that key.
	var idx accessIndex
	if r.pooled(fn) {
		if idx, err = r.accessIn(ctx, fn.Namespace); err != nil {
			return controller.Result{}, err
		}
	}
	pool := ""
	if key, ok := r.poolKeyFor(fn, idx); ok {
		pool = key.String()
	}
	if pool != fn.Status.Pool {
		fn.Status.Pool = pool
		if err := r.reclaimOrphanPools(ctx, fn.Namespace); err != nil {
			return controller.Result{}, err
		}
	}

	// ADR-0143 Decision 4.1: drain a demoted revision and retire revisions that never served, before the gates, so a
	// failing gate never strands an old revision's workers. A status with no current revision tells no worker apart.
	var drainAfter time.Duration
	if fn.Status.CurrentRevision != "" {
		if drainAfter, err = r.drain(ctx, fn); err != nil {
			return controller.Result{}, err
		}
	}

	// 2. stamp an immutable Revision (ADR-0020) + resolve-and-pin the artifact digest if the
	// spec left it unset (ADR-0035). pinned is the Revision's authoritative digest, used to
	// materialize; an unresolvable ref → Failed (never a silent/wrong deploy).
	prevRevision := fn.Status.CurrentRevision
	pinned, err := r.ensureRevision(ctx, fn)
	if err != nil {
		switch {
		case errors.Is(err, errArtifactUnresolved):
			// a registry that comes back or a tag pushed later raises no event on the Function, so the gate is re-checked
			// every supervision period (issue #703)
			return r.heldThenGate(ctx, fn, gateFailure{reason: "ArtifactUnresolved", message: err.Error(), readyMessage: err.Error(), phase: v1.PhaseFailed, requeue: r.supervisionPeriod}, idx, drainAfter, nil)
		case errors.Is(err, errRevisionMissing):
			return r.heldThenGate(ctx, fn, gateFailure{reason: "RevisionMissing", message: err.Error(), readyMessage: err.Error(), phase: v1.PhaseFailed, requeue: r.supervisionPeriod}, idx, drainAfter, nil)
		case errors.Is(err, errRevisionStampFailed):
			// the status write first, then the error as a routeError, so failPass keeps the gate's status and controller
			// backoff retries the stamp (ADR-0015; ADR-0161 Decision 1; ADR-0172 Decision 6)
			if _, gerr := r.heldThenGate(ctx, fn, gateFailure{reason: "RevisionStampFailed", message: err.Error(), readyMessage: err.Error(), phase: v1.PhaseFailed, requeue: r.supervisionPeriod}, idx, drainAfter, nil); gerr != nil {
				return controller.Result{}, gerr
			}
			return controller.Result{}, routeError{err}
		}
		return controller.Result{}, err
	}
	if fn.Status.CurrentRevision != prevRevision {
		// a newer apply supersedes the revision that was coming up, so retire it now (ADR-0143 Decision 4.1)
		again, derr := r.drain(ctx, fn)
		if derr != nil {
			return controller.Result{}, derr
		}
		drainAfter = earliest(drainAfter, again)
	}

	// 2b. platform gate (ADR-0145): an artifact built for no platform the node runs fails here, before any
	// worker starts, rather than as a handler that cannot load. Before pooling's assign, so pooled members are
	// gated too; a resolver error (a registry outage) is retried, never a status.
	if perr := r.placeable(ctx, fn, fn.Spec.Image, pinned); perr != nil {
		if errors.Is(perr, scheduler.ErrNoMatchingPlatform) {
			msg := placementMessage(perr)
			return r.heldThenGate(ctx, fn, gateFailure{reason: "NoMatchingPlatform", message: msg, readyMessage: msg, phase: v1.PhaseFailed}, idx, drainAfter, nil)
		}
		return controller.Result{}, perr
	}

	// 3. shape gate (materialization): a failure blocks Ready + programs no route.
	if verr := r.validator.Validate(ctx, fn); verr != nil {
		return r.heldThenGate(ctx, fn, gateFailure{reason: "ShapeInvalid", message: verr.Error(), phase: v1.PhaseFailed, shapeInvalid: true}, idx, drainAfter, nil)
	}

	// 3a. runtime gate (issue #371, ADR-0149 Decision 2): a runtime that no shim on this node runs, or the CatalogService
	// engine image, fails here, before pooling's assign. An absent containerd image fails at Create instead (step 4).
	if msg, missing := r.runtimeUnavailable(fn); missing {
		return r.heldThenGate(ctx, fn, gateFailure{reason: reasonRuntimeUnavailable, message: msg, readyMessage: msg, phase: v1.PhaseFailed}, idx, drainAfter, nil)
	}

	// 3b. pooling placement (ADR-0046): decide whether this function is solo (status quo) or
	// joins a shared pool worker by (namespace, runtime, worker-id). A REJECTED (over-cap)
	// member is held NotReady with a PoolFull condition and gets no worker and no route; it
	// comes back on the supervision period, since a slot frees without a write to its object.
	assign, err := r.assign(ctx, fn, idx)
	if err != nil {
		return controller.Result{}, err
	}
	if assign.Rejected {
		return r.heldThenGate(ctx, fn, gateFailure{reason: "PoolFull", message: assign.Reason, readyMessage: assign.Reason, phase: v1.PhasePending, poolFull: true, requeue: r.supervisionPeriod}, idx, drainAfter, nil)
	}
	// Clear a stale PoolFull from a prior reconcile (e.g. the pool shrank and this member was
	// admitted): the condition reflects current placement, never a leftover.
	if _, ok := fn.Status.Conditions.Get(condPoolFull); ok {
		fn.Status.Conditions.Set(v1.Condition{Type: condPoolFull, Status: v1.ConditionFalse, Reason: "Admitted"})
	}

	// 3c-3d. binding gates: fn's current bindings resolve before any worker boots, a held revision's too (ADR-0190
	// Decision 9).
	env, err := r.bindings(ctx, fn)
	if err != nil {
		return controller.Result{}, err
	}
	if env.gate != nil {
		return r.heldThenGate(ctx, fn, *env.gate, idx, drainAfter, &env)
	}

	// 3e. held revisions (ADR-0190 Decision 6): a revision a pinned call woke boots solo beside the current one. Their
	// error is returned after the current revision's pass, which it neither stops nor writes (Decision 5).
	heldAfter, herr := r.convergeHeld(ctx, fn, &env)
	drainAfter = earliest(drainAfter, heldAfter)

	// 4. converge to the EFFECTIVE desired count (honors the activator's wake Phase). Pooled: the one shared pool
	// worker for the key, driven to the max desired over the key's admitted members (ADR-0046 Decision 6). Solo: per
	// revision, moving the calls from the serving revision to the current one once it is ready (ADR-0143).
	var v verdict
	if assign.Pooled {
		var poolAfter time.Duration
		v, poolAfter, err = r.convergePooled(ctx, fn, assign, env.secret, env.catalog, idx)
		drainAfter = earliest(drainAfter, poolAfter)
	} else {
		v, err = r.convergeSolo(ctx, fn, pinned, env.secret, env.catalog)
		if errors.Is(err, runtime.ErrImageUnavailable) {
			// ADR-0149 Decisions 4 and 5: an absent image fails the latest generation as a gate does, and a later
			// periodic pass tries the Create again, so an image that appears recovers the Function.
			msg := withoutOp(err)
			res, gerr := r.gateFailed(ctx, fn, gateFailure{reason: reasonRuntimeUnavailable, message: msg, readyMessage: msg, phase: v1.PhaseFailed, requeue: r.supervisionPeriod}, idx, drainAfter)
			return afterHeld(res, gerr, herr)
		}
	}
	if err != nil {
		return controller.Result{}, convergeError{err}
	}
	res, err := r.finish(ctx, fn, v, drainAfter)
	return afterHeld(res, err, herr)
}

// boundEnv is the env fn's current bindings give every revision it boots, its secret and config env and its catalog
// env, or the binding gate that stopped them.
type boundEnv struct {
	secret, catalog map[string]string
	gate            *gateFailure
}

// bindings resolves fn's current bindings, or returns the first binding gate that fails.
func (r *Reconciler) bindings(ctx context.Context, fn *v1.Function) (boundEnv, error) {
	// 3c. secret injection gate (ADR-0057, F15 last mile): resolve the function's bound secrets
	// into an env map BEFORE provisioning any worker. A PDP-deny / missing Secret / unconfigured
	// resolver fails the function CLOSED (not Ready, SecretResolveFailed, no worker) — never a worker
	// started with the secret absent. A pooled member resolves its own, like a solo one.
	secretEnv, serr := r.resolveBindingEnv(ctx, fn)
	if serr != nil {
		// Side-attributed reason (ADR-0093 §4): a config-side failure (missing ConfigMap) reports
		// ConfigResolveFailed; every other case (secret PDP-deny / missing / unconfigured) reports
		// SecretResolveFailed. One envresolve.ResolveEnv call, two reasons.
		reason := "SecretResolveFailed"
		if errors.Is(serr, envresolve.ErrConfig) {
			reason = "ConfigResolveFailed"
		}
		// No ConfigMap or Secret event reconciles a Function, so a binding applied later is found only by a requeue.
		var requeue time.Duration
		if fault.KindOf(serr) == fault.NotFound {
			requeue = r.referentPoll
		}
		return boundEnv{gate: &gateFailure{reason: reason, message: serr.Error(), readyMessage: serr.Error(), phase: v1.PhaseFailed, requeue: requeue}}, nil
	}

	// 3c-bis. data-reference gate (ADR-0121): a spec.blob/spec.kv binding naming a not-yet-applied
	// Bucket/prefix or KVStore/table holds the function not-Ready and requeues (accept-and-requeue) —
	// fail-closed, since the PDP denies the bound access until the referent exists, so waiting just avoids
	// booting a worker whose grants can't resolve. This is the existence check the removed blob/kv
	// binding-validity admissions used to make synchronously at apply time.
	refRequeue, refReason, refMsg, rferr := r.resolveDataReferences(ctx, fn)
	if rferr != nil {
		return boundEnv{}, rferr
	}
	if refRequeue {
		return boundEnv{gate: &gateFailure{reason: refReason, message: refMsg, readyMessage: refMsg, phase: v1.PhasePending, requeue: r.referentPoll}}, nil
	}

	// 3d. catalog consumer-binding gate (ADR-0091, F61): resolve each spec.catalogs binding into the
	// FUNCD_CATALOG_<ALIAS>_URL/_TOKEN env pair BEFORE provisioning any worker. A bound catalog that
	// is not Ready yet (its endpoint is not the PEP proxy yet) requeues fail-closed —
	// the function is held Ready=False/CatalogNotReady and re-reconciled soon, never booted with an
	// empty URL/token (mirrors the ADR-0088 catalog-wait). A hard resolution error fails it closed.
	catalogEnv, requeue, cerr := r.resolveCatalogEnv(ctx, fn)
	if cerr != nil {
		return boundEnv{gate: &gateFailure{reason: "CatalogResolveFailed", message: cerr.Error(), readyMessage: cerr.Error(), phase: v1.PhaseFailed}}, nil
	}
	if requeue {
		const msg = "a bound CatalogService is not Ready yet (no endpoint, token or live proxy); waiting"
		return boundEnv{gate: &gateFailure{reason: "CatalogNotReady", message: msg, readyMessage: msg, phase: v1.PhasePending, requeue: r.referentPoll}}, nil
	}
	return boundEnv{secret: secretEnv, catalog: catalogEnv}, nil
}

// heldThenGate converges fn's held revisions, then records the gate g that stopped its current revision, so that gate
// never strands a woken held revision (ADR-0190 Decision 6). env is the bindings the pass resolved, nil when g stopped
// it before them. A held revision's error is returned after the gate's status write.
func (r *Reconciler) heldThenGate(ctx context.Context, fn *v1.Function, g gateFailure, idx accessIndex, drainAfter time.Duration, env *boundEnv) (controller.Result, error) {
	heldAfter, herr := r.convergeHeld(ctx, fn, env)
	res, err := r.gateFailed(ctx, fn, g, idx, earliest(drainAfter, heldAfter))
	return afterHeld(res, err, herr)
}

// afterHeld returns the held revisions' error herr once the pass's own status write succeeded, so failPass never writes
// it to the Function's status.
func afterHeld(res controller.Result, err, herr error) (controller.Result, error) {
	if err == nil && herr != nil {
		return controller.Result{}, routeError{herr}
	}
	return res, err
}

// convergeError is an error of the converge step — materialize, schedule, create, stop, list, the serving Revision's
// read — which failPass reports as StartFailed (ADR-0161 Decision 1).
type convergeError struct{ err error }

func (e convergeError) Error() string { return e.err.Error() }
func (e convergeError) Unwrap() error { return e.err }

// routeError is an error returned as is after the pass's status write succeeded: programming the routes, or the
// RevisionStampFailed gate's stamp error (ADR-0172 Decision 6), which failPass must not overwrite.
type routeError struct{ err error }

func (e routeError) Error() string { return e.err.Error() }
func (e routeError) Unwrap() error { return e.err }

// reasonReconcileFailed is the reason of a failed pass outside the converge step (ADR-0161 Decision 1).
const reasonReconcileFailed = "ReconcileFailed"

// failPass writes the status of a pass that failed with err before its own status write, from read, the status the pass
// started from, and returns err for the engine's backoff (ADR-0161 Decision 1). Ready and status.replicas follow the
// listening workers; a List error, or a pool worker that does not answer, keeps them as read (issues #353, #838). It
// never writes Failed and never wakes the Function; only finish makes a Degraded Function Ready.
func (r *Reconciler) failPass(ctx context.Context, fn *v1.Function, read v1.FunctionStatus, err error) (controller.Result, error) {
	reason := reasonReconcileFailed
	if errors.As(err, new(convergeError)) {
		reason = "StartFailed"
	}
	msg := err.Error()
	started := read.Phase
	fn.Status.ObservedGeneration = read.ObservedGeneration
	fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: reason, Message: msg, ObservedGeneration: fn.Generation})
	readReady, hasReady := read.Conditions.Get(condReady)
	n, lerr := r.listeningCount(ctx, fn)
	switch {
	case lerr != nil:
		fn.Status.Phase, fn.Status.Replicas = started, read.Replicas
		if hasReady {
			restoreCondition(&fn.Status.Conditions, readReady)
		}
	case started == v1.PhaseReady && n >= 1:
		fn.Status.Phase, fn.Status.Replicas = v1.PhaseReady, n
		if hasReady {
			restoreCondition(&fn.Status.Conditions, readReady)
		}
	default:
		fn.Status.Replicas = 0
		fn.Status.Phase = started
		if servingPhase(started) {
			fn.Status.Phase = v1.PhaseDegraded
		}
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: msg})
	}
	if !sameIgnoringMessages(read, fn.Status) && ctx.Err() == nil {
		if _, uerr := r.store.Update(ctx, fn); uerr != nil {
			r.logger.Warn("could not write the status of a failed pass", "namespace", fn.Namespace, "name", fn.Name, "err", uerr)
		}
	}
	if ctx.Err() == nil {
		if perr := r.programAllRoutes(ctx); perr != nil {
			r.logger.Warn("could not program the routes after a failed pass", "namespace", fn.Namespace, "name", fn.Name, "err", perr)
		}
	}
	return controller.Result{}, err
}

// sameIgnoringMessages reports whether two statuses differ at most in their conditions' messages, so a run of failures
// with one reason writes once and keeps the engine's backoff (ADR-0047: a write re-queues at once).
func sameIgnoringMessages(a, b v1.FunctionStatus) bool {
	a.Conditions, b.Conditions = withoutMessages(a.Conditions), withoutMessages(b.Conditions)
	return reflect.DeepEqual(a, b)
}

// restoreCondition puts c back as it was read, its transition time included.
func restoreCondition(cs *v1.Conditions, c v1.Condition) {
	if i := slices.IndexFunc(*cs, func(x v1.Condition) bool { return x.Type == c.Type }); i >= 0 {
		(*cs)[i] = c
		return
	}
	cs.Set(c)
}

func withoutMessages(cs v1.Conditions) v1.Conditions {
	out := slices.Clone(cs)
	for i := range out {
		out[i].Message = ""
	}
	return out
}

// withoutOp is err's text without the op of its outermost fault.Error.
func withoutOp(err error) string {
	var fe *fault.Error
	if errors.As(err, &fe) {
		return strings.TrimPrefix(err.Error(), fe.Op+": ")
	}
	return err.Error()
}

// gateFailure is a gate that stopped the latest generation before converge, or at a worker create inside it (ADR-0143
// Decision 4.6, ADR-0149 Decision 4).
type gateFailure struct {
	reason, message string        // RevisionReady's reason and message
	readyMessage    string        // the message the gate writes on Ready when nothing serves
	phase           v1.Phase      // the phase the gate writes when nothing serves
	shapeInvalid    bool          // the shape gate: ShapeValid turns False too
	poolFull        bool          // the pool gate: PoolFull is set, ShapeValid stays as served leaves it (ADR-0174)
	requeue         time.Duration // the gate's requeue when nothing serves (0 = none)
}

// gateFailed records a gate failure. On an asleep Function, whatever its placement (ADR-0192, ADR-0193), it first
// releases its workers: the serving and the current revision's workers stop, and a pooled member's pool worker stops
// once no admitted member of its key wants one (idx is the pass's access index); it marks it Asleep, then the gate's own
// writes apply. Otherwise it judges by the serving revision's workers, a pooled member's pool worker (ADR-0161 Decision
// 2): while one listens and the Function was Ready it stays Ready with their count; while one runs it is Degraded;
// otherwise the gate's own writes apply. While a worker of the serving revision runs, a solo Function's current
// revision's workers stop if it is not the serving one (ADR-0143 Decision 4.6) and the pass returns after the period.
func (r *Reconciler) gateFailed(ctx context.Context, fn *v1.Function, g gateFailure, idx accessIndex, drainAfter time.Duration) (controller.Result, error) {
	const op = "function.Reconcile"
	gen := fn.Generation
	fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: g.reason, Message: g.message, ObservedGeneration: gen})
	switch {
	case g.shapeInvalid:
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionFalse, Reason: "ShapeInvalid", Message: g.message, ObservedGeneration: gen})
	case g.poolFull:
		// the pool gate loads nothing, so only a generation that has served keeps ShapeValid True (ADR-0174)
		if served(fn) {
			fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionTrue, ObservedGeneration: gen})
		} else {
			fn.Status.Conditions.Set(notStarted(fn, condShapeValid))
		}
		fn.Status.Conditions.Set(v1.Condition{Type: condPoolFull, Status: v1.ConditionTrue, Reason: "PoolFull", Message: g.message})
	}
	requeue := g.requeue
	var running, listening int
	if r.asleep(fn) {
		if err := r.stopAsleep(ctx, fn); err != nil {
			return controller.Result{}, err
		}
		if err := r.releasePool(ctx, fn, idx); err != nil {
			return controller.Result{}, err
		}
		fn.Status.Conditions.Set(v1.Condition{Type: condAsleep, Status: v1.ConditionTrue, Reason: "ScaledToZero", Message: "no worker runs; a call wakes the Function"})
	} else {
		clearAsleep(fn)
		var err error
		if running, listening, err = r.servingWorkers(ctx, fn); err != nil {
			return controller.Result{}, err
		}
	}
	if running >= 1 {
		if c := fn.Status.CurrentRevision; c != fn.Status.ServingRevision && !r.pooled(fn) {
			if err := r.stopRevision(ctx, fn, v1.ObjectName(c)); err != nil {
				return controller.Result{}, err
			}
		}
		requeue = r.supervisionPeriod
	}
	switch {
	case listening >= 1 && fn.Status.Phase == v1.PhaseReady:
		fn.Status.Replicas = listening
	case running >= 1:
		fn.Status.Phase, fn.Status.Replicas = v1.PhaseDegraded, 0
		if rc, ok := fn.Status.Conditions.Get(condReady); !ok || rc.Status != v1.ConditionFalse {
			fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "Restarting", Message: "no worker of the serving revision listens"})
		}
	default:
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: g.reason, Message: g.readyMessage})
		fn.Status.Phase, fn.Status.Replicas = g.phase, 0
	}
	if _, uerr := r.store.Update(ctx, fn); uerr != nil {
		return controller.Result{}, retryOnConflict(uerr, op)
	}
	if perr := r.programAllRoutes(ctx); perr != nil {
		return controller.Result{}, routeError{perr}
	}
	return controller.Result{RequeueAfter: earliest(requeue, drainAfter)}, nil
}

// asleep reports whether fn is a scale-to-zero Function that is asleep, whatever its placement: its read phase is
// Idle, or a gate held it Pending or Failed while asleep (ADR-0192, ADR-0193). Its pass wants 0 workers, and only
// the activator's wake ends it.
func (r *Reconciler) asleep(fn *v1.Function) bool {
	if fn.Spec.Scaling.MinReplicas != 0 {
		return false
	}
	switch fn.Status.Phase {
	case v1.PhaseIdle:
		return true
	case v1.PhasePending, v1.PhaseFailed:
		c, ok := fn.Status.Conditions.Get(condAsleep)
		return ok && c.Status == v1.ConditionTrue
	}
	return false
}

// stopAsleep stops an asleep Function's serving and current revisions' workers and clears what served, as
// convergeSolo's desired-0 branch does (ADR-0192 Decision 1).
func (r *Reconciler) stopAsleep(ctx context.Context, fn *v1.Function) error {
	s, c := fn.Status.ServingRevision, fn.Status.CurrentRevision
	if s != "" {
		if err := r.stopRevision(ctx, fn, v1.ObjectName(s)); err != nil {
			return err
		}
	}
	if c != "" && c != s {
		if err := r.stopRevision(ctx, fn, v1.ObjectName(c)); err != nil {
			return err
		}
	}
	fn.Status.ServingRevision, fn.Status.DrainingRevision, fn.Status.DrainingSince = "", "", nil
	return nil
}

// clearAsleep sets Asleep False when it is True, so a Function that never slept carries no Asleep condition.
func clearAsleep(fn *v1.Function) {
	if c, ok := fn.Status.Conditions.Get(condAsleep); ok && c.Status == v1.ConditionTrue {
		fn.Status.Conditions.Set(v1.Condition{Type: condAsleep, Status: v1.ConditionFalse})
	}
}

// verdict is a pass's outcome: what serves (ADR-0142's phase rules) and how the current revision fares (ADR-0143).
type verdict struct {
	running, ready int       // the serving side's running and ready replicas
	shapeFailed    bool      // nothing serves and the revision being brought up cannot load its handler
	serving        bool      // the replicas judged have served since the last deploy
	retryAt        time.Time // the earliest backoff deadline (zero if none)
	booting        bool      // a replica of the current revision runs but is not ready yet
	switching      bool      // the serving revision serves while the current one, which differs, comes up
	currentFailed  bool      // while switching: the current revision cannot load its handler
	loadErr        string    // the shim's load error, when shapeFailed or currentFailed
	switched       bool      // this pass moved the calls to the current revision
	startErr       error     // the first error starting a current-revision replica (nil if every replica started)
	repairErr      string    // a serving pass: why it stopped a replica that never became ready (issue #309)
	crashLoop      string    // the serving side's boot-crash message (ADR-0160), "" if none
	// currentCrashLoop is the current revision's boot-crash message while switching; desired is the serving side's
	// replica count M.
	currentCrashLoop string
	desired          int
	pooled           bool      // a pooled member, judged on its pool host's /health/members (ADR-0046)
	pollAt           time.Time // when every booting replica has a boot-crash count: the earliest boot timeout (ADR-0161)
}

// holdsFailed reports whether a pass that started Failed leaves the status as read (ADR-0169): no worker of the judged
// revisions runs, boots or is ready, and the pass found no shape failure, Start error or boot crash.
func holdsFailed(started v1.Phase, v verdict) bool {
	return started == v1.PhaseFailed && v.running == 0 && v.ready == 0 && !v.booting &&
		!v.shapeFailed && !v.currentFailed && v.startErr == nil && v.crashLoop == "" && v.currentCrashLoop == ""
}

// finish writes the pass's status from v and returns its requeue. Ready and the phase describe the serving side;
// ShapeValid and RevisionReady, the current revision (ADR-0143 Decision 5), which neither reports True before a replica
// of it has been ready (ADR-0174).
func (r *Reconciler) finish(ctx context.Context, fn *v1.Function, v verdict, drainAfter time.Duration) (controller.Result, error) {
	sleeping := r.asleep(fn)
	clearAsleep(fn)
	// ADR-0192 Decision 3: an asleep Failed Function whose gates pass goes Idle, not held Failed
	if holdsFailed(fn.Status.Phase, v) && !sleeping {
		// ADR-0169 Decision 2: phase, Ready, ShapeValid and RevisionReady stay as read
		fn.Status.Replicas = 0
		fn.Status.ObservedGeneration = fn.Generation
		return r.record(ctx, fn, v, drainAfter)
	}
	loaded := (!v.switching && v.ready >= 1) || served(fn)
	gen := fn.Generation
	fn.Status.Replicas = v.ready
	fn.Status.ObservedGeneration = gen
	// ShapeValid is set once, from its final value: setting it True and then False in one pass would move its
	// LastTransitionTime on every pass, a write that retriggers the pass through the watch (issue #24).
	switch {
	case v.shapeFailed || (v.switching && v.currentFailed):
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionFalse, Reason: "ShapeInvalid", Message: v.loadErr, ObservedGeneration: gen})
	case loaded:
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionTrue, ObservedGeneration: gen})
	default:
		fn.Status.Conditions.Set(notStarted(fn, condShapeValid))
	}
	switch {
	case v.shapeFailed:
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "ShapeInvalid"})
		fn.Status.Phase = v1.PhaseFailed
	case v.ready >= 1:
		fn.Status.Phase = v1.PhaseReady
		ready := v1.Condition{Type: condReady, Status: v1.ConditionTrue}
		if v.crashLoop != "" {
			ready.Reason, ready.Message = reasonCrashLoop, fmt.Sprintf("replicas %d ready of %d: %s", v.ready, v.desired, v.crashLoop)
		}
		fn.Status.Conditions.Set(ready)
	case v.serving:
		// ADR-0142: no replica is ready while a dead one is replaced (the blueprint's Ready → Degraded → Ready).
		fn.Status.Phase = v1.PhaseDegraded
		reason, msg := "Restarting", "a replica exited and is being replaced"
		switch {
		case v.startErr != nil:
			msg = "a replica exited and its replacement could not start: " + v.startErr.Error()
		case v.crashLoop != "":
			reason, msg = reasonCrashLoop, v.crashLoop
		case v.repairErr != "":
			msg = "a replica exited and its replacement was stopped: " + v.repairErr
		}
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: msg})
	case v.startErr != nil && v.running == 0:
		// the blueprint's Deploying → Failed on a worker error; requeueFor retries it after its growing wait (ADR-0169)
		fn.Status.Phase = v1.PhaseFailed
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "StartFailed", Message: v.startErr.Error()})
	case v.crashLoop != "":
		// a replica crashed while booting and waits out its backoff (ADR-0160): never Idle, never Failed
		fn.Status.Phase = v1.PhaseDeploying
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reasonCrashLoop, Message: v.crashLoop})
	case v.running >= 1 || !v.retryAt.IsZero():
		// replicas started but the shim is not serving yet, or a replica waits out its backoff (ADR-0142) — keep
		// polling.
		fn.Status.Phase = v1.PhaseDeploying
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "ShimNotReady"})
	default:
		fn.Status.Phase = v1.PhaseIdle
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "NoReplicas"})
	}
	switch {
	case v.switching && v.currentFailed:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "ShapeInvalid", Message: "the current revision could not load its handler; the serving revision keeps the calls", ObservedGeneration: gen})
	case v.switching && v.startErr != nil:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "StartFailed", Message: "a worker of the current revision could not start: " + v.startErr.Error(), ObservedGeneration: gen})
	case v.switching && v.currentCrashLoop != "":
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: reasonCrashLoop, Message: v.currentCrashLoop, ObservedGeneration: gen})
	case v.switching:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "Progressing", Message: "the current revision is booting beside the serving one", ObservedGeneration: gen})
	case v.shapeFailed:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "ShapeInvalid", ObservedGeneration: gen})
	case v.startErr != nil && fn.Status.Phase == v1.PhaseFailed:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "StartFailed", Message: v.startErr.Error(), ObservedGeneration: gen})
	case v.crashLoop != "" && fn.Status.Phase == v1.PhaseDeploying:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: reasonCrashLoop, Message: v.crashLoop, ObservedGeneration: gen})
	case fn.Status.Phase == v1.PhaseDeploying:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "Progressing", ObservedGeneration: gen})
	case loaded:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionTrue, ObservedGeneration: gen})
	default:
		fn.Status.Conditions.Set(notStarted(fn, condRevisionReady))
	}
	return r.record(ctx, fn, v, drainAfter)
}

// record writes the status finish set, programs the routes and returns the pass's requeue.
func (r *Reconciler) record(ctx context.Context, fn *v1.Function, v verdict, drainAfter time.Duration) (controller.Result, error) {
	if _, uerr := r.store.Update(ctx, fn); uerr != nil {
		return controller.Result{}, retryOnConflict(uerr, "function.Reconcile")
	}
	if perr := r.programAllRoutes(ctx); perr != nil {
		return controller.Result{}, routeError{perr}
	}
	requeue := r.requeueFor(fn.Status.Phase, v)
	if v.switched {
		requeue = earliest(requeue, r.handOutSettle) // come back for the drain
	}
	return controller.Result{RequeueAfter: earliest(requeue, drainAfter)}, nil
}

// served reports whether a replica of fn's latest generation has been ready (ADR-0174 Decision 1).
func served(fn *v1.Function) bool {
	c, ok := fn.Status.Conditions.Get(condShapeValid)
	return ok && c.Status == v1.ConditionTrue && c.ObservedGeneration == fn.Generation
}

// notStarted is condition t of fn while no replica of fn's latest generation has been ready (ADR-0174).
func notStarted(fn *v1.Function, t v1.ConditionType) v1.Condition {
	return v1.Condition{Type: t, Status: v1.ConditionUnknown, Reason: "NotStarted", Message: "no replica of this generation has been ready yet", ObservedGeneration: fn.Generation}
}

// requeueFor is how soon a pass comes back (ADR-0142; ADR-0143 Decision 4.7). While the current revision comes up
// beside the serving one, it polls a booting replica and otherwise — a failed current revision included, as one whose
// replica timed out booting (issue #354) — checks back after the supervision period. A booting replica is polled at
// readinessPoll only on its first boot attempt; once every booting replica has a boot-crash count, the pass comes back
// at its boot timeout or re-create time, at most the period (ADR-0161 Decision 3).
func (r *Reconciler) requeueFor(phase v1.Phase, v verdict) time.Duration {
	now := r.clock.Now()
	poll := readinessPoll
	if !v.pollAt.IsZero() {
		poll = min(r.supervisionPeriod, max(earlier(v.pollAt, v.retryAt).Sub(now), time.Millisecond))
	}
	if v.switching {
		if v.booting && !v.currentFailed {
			return poll
		}
		if !v.retryAt.IsZero() {
			return min(r.supervisionPeriod, max(v.retryAt.Sub(now), time.Millisecond))
		}
		return r.supervisionPeriod
	}
	if v.pooled && v.crashLoop != "" { // a member whose load timed out stays failed until its pool's next start
		return max(v.retryAt.Sub(now), time.Millisecond)
	}
	switch phase {
	case v1.PhaseDeploying: // shim booting — re-poll readiness soon; a replica in its backoff — at its deadline
		if v.running == 0 && !v.retryAt.IsZero() {
			return max(v.retryAt.Sub(now), time.Millisecond)
		}
		return poll
	case v1.PhaseReady: // ADR-0142: come back to check the replicas
		return r.supervisionPeriod
	case v1.PhaseDegraded: // ADR-0142: poll a booting replacement, else wait out the backoff
		if v.running > v.ready {
			return poll
		}
		if !v.retryAt.IsZero() { // at least 1ms: a zero RequeueAfter would mean no requeue
			return max(v.retryAt.Sub(now), time.Millisecond)
		}
		return r.supervisionPeriod
	case v1.PhaseFailed:
		// ADR-0169 Decision 4: a replica that could not start is started again at the end of its growing wait; the pool
		// worker, which has no counter, after the period, as a pooled member's shape failure (ADR-0158). A solo shape
		// failure is not retried.
		switch {
		case !v.retryAt.IsZero():
			return max(v.retryAt.Sub(now), time.Millisecond)
		case v.startErr != nil || (v.pooled && v.shapeFailed):
			return r.supervisionPeriod
		}
	}
	return 0
}

// earliest is the sooner of two requeues, where 0 means none.
func earliest(a, b time.Duration) time.Duration {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	}
	return min(a, b)
}

// readinessPoll is how soon a pass re-checks a booting shim.
const readinessPoll = 200 * time.Millisecond

// defaultBootTimeout bounds how long a solo replica may run before it listens, or listen without becoming ready (ADR-0030 §4b's
// timeout): one that never listens is a boot crash (ADR-0161 Decision 3), one that listens but is never ready a shape
// failure. It exceeds the activator's 30 s activation hold, so it never cuts short a boot that a cold call still waits
// for. runtime.bootTimeout replaces it (ADR-0163).
const defaultBootTimeout = time.Minute

// probeTimeout bounds one readiness probe. The probe runs inside the pass, on the engine's shared worker, so a replica
// that never answers must cost less than the poll it is repeated at (issue #75); a local shim answers in microseconds.
const probeTimeout = readinessPoll / 2

// servingPhase reports whether a Function in this phase has served since its last deploy (ADR-0142).
func servingPhase(p v1.Phase) bool { return p == v1.PhaseReady || p == v1.PhaseDegraded }

// steadyState reports whether fn is a solo Function at desired state: Ready with no reason (a crash loop beside a ready
// replica runs the full pass, ADR-0160), its generation processed, its current revision serving with nothing draining
// (ADR-0143), status.replicas at desired, every bound catalog Ready (ADR-0162) and every replica listening (ADR-0161).
// It calls runtime.Status once per replica (ADR-0142) and, for a catalog consumer, one store Get per binding; it writes
// nothing.
func (r *Reconciler) steadyState(ctx context.Context, fn *v1.Function) bool {
	if fn.Status.Phase != v1.PhaseReady || fn.Status.ObservedGeneration != fn.Generation {
		return false
	}
	if rc, ok := fn.Status.Conditions.Get(condReady); ok && rc.Reason != "" {
		return false
	}
	if r.pooled(fn) {
		return false
	}
	c := fn.Status.CurrentRevision
	if c == "" || fn.Status.ServingRevision != c || fn.Status.DrainingRevision != "" || r.isSpared(fn) || r.heldPending(fn) {
		return false
	}
	if rr, ok := fn.Status.Conditions.Get(condRevisionReady); !ok || rr.Status != v1.ConditionTrue {
		return false
	}
	desired := r.desiredReplicas(fn)
	if fn.Status.Replicas != desired {
		return false
	}
	if !r.catalogsReady(ctx, fn) {
		return false
	}
	for i := range desired {
		in, err := r.runtime.Status(ctx, runtime.NewInstanceID(fn.Namespace, fn.Name, v1.ObjectName(c), i))
		if err != nil || !r.listening(in) {
			return false
		}
	}
	return true
}

// listening reports whether worker in gets calls (ADR-0161 Decision 2): it runs and wrote its port (ADR-0160), at an
// address. The legacy placeholder writes no port, so there running suffices.
func (r *Reconciler) listening(in runtime.Instance) bool {
	if in.State != runtime.StateRunning {
		return false
	}
	return r.materializer == nil || (in.Listened && in.IP != "" && in.Port > 0)
}

// listeningCount is the number of listening workers of fn's serving revision (else its current one), or of its pool
// worker while fn's own /health/members entry reads ready (ADR-0158).
func (r *Reconciler) listeningCount(ctx context.Context, fn *v1.Function) (int, error) {
	_, n, err := r.countWorkers(ctx, fn, servingRevision(fn))
	return n, err
}

// servingWorkers counts the running and the listening workers of status.servingRevision, with no fallback, as
// countWorkers does.
func (r *Reconciler) servingWorkers(ctx context.Context, fn *v1.Function) (running, listening int, err error) {
	s := v1.ObjectName(fn.Status.ServingRevision)
	if s == "" {
		return 0, 0, nil
	}
	return r.countWorkers(ctx, fn, s)
}

// countWorkers counts the running and the listening workers of fn's revision rev or, for a pooled member, of the pool
// worker the resolver hands out (servingPool, else the newest running one), which runs for fn only while its
// /health/members lists fn and listens for it only while that entry reads ready (ADR-0158). A pool worker that does not
// answer is an error, as a List error is, so no pass judges a member it could not read (issue #838).
func (r *Reconciler) countWorkers(ctx context.Context, fn *v1.Function, rev v1.ObjectName) (running, listening int, err error) {
	name := fn.Name
	key, pooled := pooling.PoolKey{}, r.pooled(fn)
	if pooled {
		k, ok := pooling.ParsePool(fn.Namespace, fn.Status.Pool)
		if !ok {
			return 0, 0, nil
		}
		key, name, rev = k, poolInstanceName(k), ""
	}
	insts, err := r.namedInstances(ctx, fn.Namespace, name)
	if err != nil {
		return 0, 0, err
	}
	if pooled {
		w, ok := r.servingPool(insts)
		if !ok {
			w, ok = newestPool(insts, func(in runtime.Instance) bool { return in.State == runtime.StateRunning })
		}
		insts = nil
		if ok {
			insts, rev = []runtime.Instance{w}, w.Revision
		}
	}
	for _, in := range insts {
		if in.Revision != rev || in.State != runtime.StateRunning {
			continue
		}
		running++
		if r.listening(in) {
			listening++
		}
	}
	if pooled && running > 0 && r.materializer != nil {
		_, m, ok, perr := r.memberIn(ctx, key, insts, fn.Name)
		if perr != nil {
			return 0, 0, perr
		}
		if !ok {
			return 0, 0, nil
		}
		if m.State != memberReady {
			listening = 0
		}
	}
	return running, listening, nil
}

// desiredReplicas computes the effective replica count: it honors the activator's wake
// signal (ADR-0016's partitioned Status.Phase) for scaled-to-zero functions, so a cold
// request's wake (Phase=Deploying) actually provisions a worker. An asleep Function wants
// none until a call wakes it (ADR-0192).
func (r *Reconciler) desiredReplicas(fn *v1.Function) int {
	sc := fn.Spec.Scaling
	if sc.MinReplicas == 0 { // scale-to-zero enabled
		if r.asleep(fn) {
			return 0
		}
		switch fn.Status.Phase {
		case v1.PhaseDeploying, v1.PhaseReady, v1.PhaseDegraded, v1.PhaseFailed:
			// woken, serving or repairing (ADR-0142) — stay up until the activator's idle-reclaim writes Idle. Without
			// keeping Ready up, the reconcile right after a wake would tear the function down before it can serve
			// (ADR-0033: a woken function stays up until idle, not torn down per request). A Failed function that is
			// not asleep keeps its replicas, which idle reclaim never takes: a new spec, a gate that passes or a Start
			// retried after its growing wait brings a worker up (ADR-0169).
			return maxInt(1, fn.Spec.Replicas)
		case v1.PhaseIdle: // the activator reclaimed it
			return 0
		default: // initial (Pending/empty): honor the static replica floor (0 for scale-to-zero)
			return fn.Spec.Replicas
		}
	}
	return maxInt(fn.Spec.Replicas, sc.MinReplicas)
}

// convergeSolo converges a solo Function's revisions and judges them (ADR-0143 Decision 4). With nothing serving, or
// the current revision serving, it is ADR-0142's per-replica converge of the current revision; otherwise the current
// revision comes up beside the serving one (switchSolo).
func (r *Reconciler) convergeSolo(ctx context.Context, fn *v1.Function, pinned string, secretEnv, catalogEnv map[string]string) (verdict, error) {
	c := v1.ObjectName(fn.Status.CurrentRevision)
	s := v1.ObjectName(fn.Status.ServingRevision)
	desired := r.desiredReplicas(fn)
	untried := fn.Status.ObservedGeneration < fn.Generation
	if desired == 0 {
		if err := r.stopAll(ctx, fn, c); err != nil {
			return verdict{}, err
		}
		fn.Status.ServingRevision, fn.Status.DrainingRevision, fn.Status.DrainingSince = "", "", nil
		s = ""
	}
	if s != "" && s != c {
		return r.switchSolo(ctx, fn, s, c, pinned, desired, untried, secretEnv, catalogEnv)
	}
	serving := servingPhase(fn.Status.Phase)
	pass, err := r.convergeRevision(ctx, fn, c, pinned, replicaRange(desired), convergeOpts{serving: serving, untried: untried, scaleDown: true}, secretEnv, catalogEnv)
	if err != nil {
		return verdict{}, err
	}
	running := pass.running
	ready, failed, readyRetry, crashLoop, err := r.readyReplicas(ctx, fn.Namespace, fn.Name, c, running, desired, readinessPath, r.bootTimeout, serving, r.boot)
	if err != nil {
		return verdict{}, err
	}
	ul, err := r.stopUnlistened(ctx, fn, c, desired)
	if err != nil {
		return verdict{}, err
	}
	running -= len(ul.stopped)
	if ul.crashLoop != "" {
		crashLoop = ul.crashLoop
	}
	var repairErr string
	if serving {
		// ADR-0142: in a pass that started serving, a Failed replica is a crash under repair, and so is one that never
		// became ready (issue #309)
		stopped, serr := r.stopNeverReady(ctx, fn, failed)
		if serr != nil {
			return verdict{}, serr
		}
		if stopped {
			running--
			repairErr = r.notReadyError()
		}
		failed = ""
	}
	if ready >= 1 && s == "" {
		fn.Status.ServingRevision = string(c)
	}
	return verdict{
		running: running, ready: ready, shapeFailed: failed != "", loadErr: r.loadError(ctx, failed),
		serving: serving, retryAt: earlier(earlier(pass.retryAt, readyRetry), ul.retryAt), booting: running > ready, startErr: pass.startErr,
		repairErr: repairErr, crashLoop: crashLoop, desired: desired, pollAt: ul.pollAt,
	}, nil
}

// stopNeverReady stops failed, the replica readiness judged failed, if it still runs — it ran for bootTimeout without
// becoming ready (ADR-0030 §4b) — and fn has been Degraded as long, so convergeRevision (ensurePool, for a pool worker)
// replaces it after the backoff like a crash under repair (ADR-0142). A replica that served before fn lost its last ready one keeps bootTimeout from
// then, so one failed probe of a busy worker does not stop it. It reports whether it stopped the replica.
func (r *Reconciler) stopNeverReady(ctx context.Context, fn *v1.Function, failed runtime.InstanceID) (bool, error) {
	rc, _ := fn.Status.Conditions.Get(condReady)
	if failed == "" || fn.Status.Phase != v1.PhaseDegraded || time.Since(time.Time(rc.LastTransitionTime)) < r.bootTimeout {
		return false, nil
	}
	if in, err := r.runtime.Status(ctx, failed); err != nil || in.State != runtime.StateRunning {
		return false, nil
	}
	if err := r.runtime.Stop(ctx, failed); err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), "function.converge", "stop a worker that never became ready")
	}
	return true, nil
}

// unlistened is what stopUnlistened did to a revision: the replicas it stopped, the earliest time a replica may be
// re-created, the earliest boot timeout of the booting replicas when every one has a boot-crash count (zero otherwise),
// and the boot-crash message of the lowest replica with a count.
type unlistened struct {
	stopped   []runtime.InstanceID
	retryAt   time.Time
	pollAt    time.Time
	crashLoop string
}

// stopUnlistened stops each running replica of solo revision rev below `below` that has not listened within bootTimeout
// of its last start and counts it as a boot crash, so convergeRevision re-creates it after the growing wait (ADR-0161
// Decision 3). A booting replica is one that runs and has not listened: the pinned shims write their port file only
// once ready. The legacy placeholder never listens, so it is never stopped here.
func (r *Reconciler) stopUnlistened(ctx context.Context, fn *v1.Function, rev v1.ObjectName, below int) (unlistened, error) {
	var u unlistened
	if r.materializer == nil {
		return u, nil
	}
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return u, err
	}
	now := r.clock.Now()
	counted, lowest := true, -1
	for _, in := range insts {
		if in.Revision != rev || in.Replica >= below {
			continue
		}
		if in.State == runtime.StateRunning && !r.listening(in) {
			deadline := lastStart(in).Add(r.bootTimeout)
			if now.Before(deadline) {
				if c, ok := r.boot.crash(in.ID); ok && c.count > 0 {
					u.pollAt = earlier(u.pollAt, deadline)
				} else {
					counted = false
				}
			} else {
				if err := r.runtime.Stop(ctx, in.ID); err != nil {
					return unlistened{}, fault.Wrapf(err, fault.KindOf(err), "function.converge", "stop a worker that did not listen")
				}
				r.boot.timedOut(in)
				in.Exit = runtime.Exit{Cause: runtime.ExitByStop}
				_, due := r.boot.observe(in, exitStopped)
				u.stopped = append(u.stopped, in.ID)
				u.retryAt = earlier(u.retryAt, due)
			}
		}
		if c, ok := r.boot.crash(in.ID); ok && c.count > 0 && c.message != "" && (lowest < 0 || in.Replica < lowest) {
			lowest, u.crashLoop = in.Replica, c.message
		}
	}
	if !counted {
		u.pollAt = time.Time{}
	}
	return u, nil
}

// switchSolo brings the current revision c up beside the serving revision s and moves the calls to it once every
// replica of c is ready and no revision drains (ADR-0143 Decisions 4.3–4.5). s keeps its replica indexes, and a dead s
// worker is replaced from s's Revision; once that Revision is deleted, s's listening workers serve until the switch.
func (r *Reconciler) switchSolo(ctx context.Context, fn *v1.Function, s, c v1.ObjectName, pinned string, desired int, untried bool, secretEnv, catalogEnv map[string]string) (verdict, error) {
	sIdx, err := r.servingIndexes(ctx, fn, s)
	if err != nil {
		return verdict{}, err
	}
	var sPass revisionPass
	sfn, spinned, err := r.revisionTemplate(ctx, fn, s)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		sPass.running, err = r.runningReplicas(ctx, fn.Namespace, fn.Name, s, sIdx)
	case err == nil:
		sPass, err = r.convergeRevision(ctx, sfn, s, spinned, sIdx, convergeOpts{serving: true}, secretEnv, catalogEnv)
	}
	if err != nil {
		return verdict{}, err
	}
	cPass, err := r.convergeRevision(ctx, fn, c, pinned, replicaRange(desired), convergeOpts{untried: untried, scaleDown: true}, secretEnv, catalogEnv)
	if err != nil {
		return verdict{}, err
	}
	readyC, failedC, cRetry, cCrash, err := r.readyReplicas(ctx, fn.Namespace, fn.Name, c, cPass.running, desired, readinessPath, r.bootTimeout, false, r.boot)
	if err != nil {
		return verdict{}, err
	}
	cUl, err := r.stopUnlistened(ctx, fn, c, desired)
	if err != nil {
		return verdict{}, err
	}
	cPass.running -= len(cUl.stopped)
	if cUl.crashLoop != "" {
		cCrash = cUl.crashLoop
	}
	if readyC == desired && fn.Status.DrainingRevision == "" {
		now := v1.NewTimestamp(r.clock.Now())
		fn.Status.ServingRevision, fn.Status.DrainingRevision, fn.Status.DrainingSince = string(c), string(s), &now
		return verdict{running: cPass.running, ready: readyC, serving: true, switched: true, desired: desired}, nil
	}
	readyS, _, sRetry, sCrash, err := r.readyReplicas(ctx, fn.Namespace, fn.Name, s, sPass.running, maxIndex(sIdx)+1, readinessPath, r.bootTimeout, true, r.boot)
	if err != nil {
		return verdict{}, err
	}
	sUl, err := r.stopUnlistened(ctx, fn, s, maxIndex(sIdx)+1)
	if err != nil {
		return verdict{}, err
	}
	sPass.running -= len(sUl.stopped)
	if sUl.crashLoop != "" {
		sCrash = sUl.crashLoop
	}
	retryAt := earlier(earlier(earlier(sPass.retryAt, cPass.retryAt), earlier(cRetry, sRetry)), earlier(cUl.retryAt, sUl.retryAt))
	return verdict{
		running: sPass.running, ready: readyS, serving: true, retryAt: retryAt,
		booting: cPass.running > readyC, switching: true, currentFailed: failedC != "", loadErr: r.loadError(ctx, failedC), startErr: cPass.startErr,
		crashLoop: sCrash, currentCrashLoop: cCrash, desired: len(sIdx), pollAt: cUl.pollAt,
	}, nil
}

// revisionTemplate is fn as revision rev runs it — the runtime, handler and image of rev's snapshot (ADR-0020) with the
// Function's current bindings — and rev's pinned digest.
func (r *Reconciler) revisionTemplate(ctx context.Context, fn *v1.Function, rev v1.ObjectName) (*v1.Function, string, error) {
	const op = "function.revisionTemplate"
	snap, err := r.getRevision(ctx, fn.Namespace, rev)
	if err != nil {
		return nil, "", fault.Wrapf(err, fault.KindOf(err), op, "get revision %q", rev)
	}
	switch revisionOf(snap, fn) {
	case revOther:
		return nil, "", fault.Wrapf(stampTaken(snap), fault.Conflict, op, "read revision")
	case revNamesake:
		return nil, "", fault.NotFoundf(op, "revision %q is a deleted namesake's", rev)
	}
	tmpl := *fn
	tmpl.Spec.Runtime, tmpl.Spec.Handler, tmpl.Spec.Image = snap.Spec.Runtime, snap.Spec.Handler, snap.Spec.Image
	return &tmpl, snap.Spec.ImageDigest, nil
}

// servingIndexes are the replica indexes the serving revision s keeps during a switch: those the runtime lists, or
// 0 … max(status.replicas, 1)−1 when it lists none, as after a daemon restart (ADR-0143 Decision 4.3, ADR-0161).
func (r *Reconciler) servingIndexes(ctx context.Context, fn *v1.Function, s v1.ObjectName) ([]int, error) {
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return nil, err
	}
	var idx []int
	for _, in := range insts {
		if in.Revision == s {
			idx = append(idx, in.Replica)
		}
	}
	if len(idx) == 0 {
		return replicaRange(max(fn.Status.Replicas, 1)), nil
	}
	return idx, nil
}

func replicaRange(n int) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	return idx
}

func maxIndex(idx []int) int {
	m := -1
	for _, i := range idx {
		m = max(m, i)
	}
	return m
}

// convergeOpts says how convergeRevision treats a revision's replicas: serving replaces a crash after the backoff,
// untried replaces a terminal replica at once, and scaleDown stops the revision's replicas outside the indexes.
type convergeOpts struct {
	serving, untried, scaleDown bool
}

// revisionPass is what convergeRevision left of a revision, or ensurePool of a pool worker: its running count among the
// indexes, the earliest time a replica waiting out its backoff may be replaced (zero if none), and the first error
// starting a replica — a Start error or a local API socket that could not be provisioned — which the pass writes to the
// status instead of failing before it (issues #73, #358).
type revisionPass struct {
	running  int
	retryAt  time.Time
	startErr error
}

// convergeRevision drives replicas `indexes` of revision rev toward running with ADR-0142's per-replica table
// (planReplicas). With opts.scaleDown it stops rev's replicas outside indexes. tmpl is fn as rev runs it.
func (r *Reconciler) convergeRevision(ctx context.Context, tmpl *v1.Function, rev v1.ObjectName, pinnedDigest string, indexes []int, opts convergeOpts, secretEnv, catalogEnv map[string]string) (revisionPass, error) {
	const op = "function.converge"
	insts, err := r.namedInstances(ctx, tmpl.Namespace, tmpl.Name)
	if err != nil {
		return revisionPass{}, err
	}
	want := make(map[int]bool, len(indexes))
	for _, i := range indexes {
		want[i] = true
	}
	byReplica := make(map[int]runtime.Instance, len(indexes))
	for _, in := range insts {
		if in.Revision != rev {
			continue
		}
		if want[in.Replica] {
			byReplica[in.Replica] = in
			continue
		}
		if !opts.scaleDown {
			continue
		}
		r.boot.reset(in.ID)
		if in.State != runtime.StateStopped { // scale down by replica index; a Failed one is stopped too
			if serr := r.runtime.Stop(ctx, in.ID); serr != nil {
				return revisionPass{}, fault.Wrapf(serr, fault.KindOf(serr), op, "stop worker")
			}
		}
	}

	now := r.clock.Now()
	launch, replace, start, retryAt := planReplicas(byReplica, indexes, opts, now, r.supervisionPeriod, r.boot, r.materializer == nil)
	// ADR-0169 Decision 4: a replica whose last Start or worker spec failed is neither created nor started before its
	// growing wait ends; the pass keeps that error as its start error and comes back then.
	var startErr error
	held := func(id runtime.InstanceID) bool {
		at, herr := r.boot.held(id, now)
		if herr == nil {
			return false
		}
		retryAt = earlier(retryAt, at)
		if startErr == nil {
			startErr = herr
		}
		return true
	}
	replace = slices.DeleteFunc(replace, func(in runtime.Instance) bool { return held(in.ID) })
	launch = slices.DeleteFunc(launch, func(i int) bool { return held(runtime.NewInstanceID(tmpl.Namespace, tmpl.Name, rev, i)) })
	start = slices.DeleteFunc(start, held)

	// materialize the artifact once (shim mode) before launching any replica. The Revision's
	// pinned digest (ADR-0035) is applied to an in-memory copy — never written back to the
	// Function spec (store.Update persists only Status), so the spec keeps the user's input.
	artifactPath := ""
	if r.materializer != nil && len(launch) > 0 {
		mfn := *tmpl
		mfn.Spec.ImageDigest = pinnedDigest
		artifactPath, err = r.materializer.Materialize(ctx, &mfn)
		if err != nil {
			return revisionPass{}, fault.Wrapf(err, fault.KindOf(err), op, "materialize artifact")
		}
	}
	for _, in := range replace {
		if serr := r.runtime.Stop(ctx, in.ID); serr != nil {
			return revisionPass{}, fault.Wrapf(serr, fault.KindOf(serr), op, "stop exited worker")
		}
	}
	platforms, err := r.artifactPlatforms(ctx, tmpl.Spec.Image, pinnedDigest)
	if err != nil {
		return revisionPass{}, err
	}
	for _, i := range launch {
		if _, perr := r.scheduler.Schedule(ctx, scheduler.Request{Namespace: tmpl.Namespace, Name: tmpl.Name, Replica: i, Platforms: platforms}); perr != nil {
			return revisionPass{}, fault.Wrapf(perr, fault.KindOf(perr), op, "schedule")
		}
		spec, serr := r.workerSpec(tmpl, i, artifactPath, secretEnv, catalogEnv)
		if serr != nil {
			r.logger.Warn("could not start worker", "function", tmpl.Name, "replica", i, "err", serr)
			retryAt = earlier(retryAt, r.boot.startResult(runtime.NewInstanceID(tmpl.Namespace, tmpl.Name, rev, i), now, serr))
			if startErr == nil {
				startErr = serr
			}
			continue
		}
		spec.Revision = rev
		inst, cerr := r.runtime.Create(ctx, spec)
		if errors.Is(cerr, runtime.ErrImageUnavailable) {
			return revisionPass{}, fault.Wrapf(cerr, fault.KindOf(cerr), op, "%s", unavailablePrefix(tmpl.Spec.Runtime))
		}
		if cerr != nil {
			return revisionPass{}, fault.Wrapf(cerr, fault.KindOf(cerr), op, "create worker")
		}
		start = append(start, inst.ID)
	}
	for _, id := range start {
		serr := r.runtime.Start(ctx, id)
		retryAt = earlier(retryAt, r.boot.startResult(id, now, serr))
		if serr != nil {
			r.logger.Warn("could not start worker", "instance", id, "err", serr)
			if startErr == nil {
				startErr = serr
			}
		}
	}

	running, err := r.runningReplicas(ctx, tmpl.Namespace, tmpl.Name, rev, indexes)
	if err != nil {
		return revisionPass{}, err
	}
	return revisionPass{running: running, retryAt: retryAt, startErr: startErr}, nil
}

// planReplicas triages replicas `indexes` of a revision, byReplica holding their instances: a missing replica is
// launched, a Created one started, and a terminal one of an untried generation replaced at once. For a tried generation
// it reads each terminal replica by how it ended (ADR-0160 Decision 3): an exit 3 before listening in a pass not
// serving is kept, so readiness reports the shape failure; a boot crash is replaced after boot's growing wait, as is a
// counted one Stop ran after; any other end after period. With a nil boot it neither classifies nor counts and keeps
// ADR-0142's rule: a Stopped replica or a crash in a serving revision after period, any other Failed one kept. retryAt
// is the earliest time a waiting replica may be replaced (zero if none).
func planReplicas(byReplica map[int]runtime.Instance, indexes []int, opts convergeOpts, now time.Time, period time.Duration,
	boot *bootBackoff, legacy bool) (launch []int, replace []runtime.Instance, start []runtime.InstanceID, retryAt time.Time) {
	for _, i := range indexes {
		in, ok := byReplica[i]
		var due time.Time
		switch {
		case !ok:
			launch = append(launch, i)
			continue
		case in.State == runtime.StateCreated:
			start = append(start, in.ID)
			continue
		case !in.State.Terminal():
			continue
		case opts.untried:
			replace, launch = append(replace, in), append(launch, i)
			continue
		case boot == nil:
			if in.State != runtime.StateStopped && !opts.serving {
				continue
			}
		default:
			class := classifyExit(in, opts.serving, legacy)
			if class == exitShapeError {
				continue
			}
			_, due = boot.observe(in, class)
		}
		if due.IsZero() {
			due = lastStart(in).Add(period)
		}
		if now.Before(due) {
			retryAt = earlier(retryAt, due)
			continue
		}
		replace, launch = append(replace, in), append(launch, i)
	}
	return launch, replace, start, retryAt
}

// runningReplicas counts the running workers of revision rev among replicas `indexes`.
func (r *Reconciler) runningReplicas(ctx context.Context, ns v1.NamespaceName, name, rev v1.ObjectName, indexes []int) (int, error) {
	insts, err := r.namedInstances(ctx, ns, name)
	if err != nil {
		return 0, err
	}
	running := 0
	for _, in := range insts {
		if in.Revision == rev && slices.Contains(indexes, in.Replica) && in.State == runtime.StateRunning {
			running++
		}
	}
	return running, nil
}

// drain stops and removes the workers of revisions that no longer serve (ADR-0143 Decision 4.1): those of the drained
// revision once idle — none before HandOutSettle after drainingSince, all once DrainGrace has passed — and those of any
// revision other than the serving, current and drained ones at once, since they never served. It clears
// drainingRevision when none of its workers remain, forgets the boot-crash entries of every other revision (ADR-0169),
// and returns how soon the pass must come back (0 if nothing drains).
func (r *Reconciler) drain(ctx context.Context, fn *v1.Function) (time.Duration, error) {
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return 0, err
	}
	s, c, d := fn.Status.ServingRevision, fn.Status.CurrentRevision, fn.Status.DrainingRevision
	var elapsed time.Duration
	if fn.Status.DrainingSince != nil {
		elapsed = r.clock.Now().Sub(time.Time(*fn.Status.DrainingSince))
	}
	draining, kept := 0, 0
	var held revhold.Holds
	for _, in := range insts {
		rev := string(in.Revision)
		if rev == s || rev == c {
			continue
		}
		if held == nil {
			if held, err = revhold.Held(ctx, r.store, fn.Namespace); err != nil {
				return 0, err
			}
		}
		if held.Revision(fn.Name, fn.UID, in.Revision) {
			kept++
			continue
		}
		if d != "" && rev == d && in.State == runtime.StateRunning &&
			(elapsed < r.handOutSettle || (elapsed < r.drainGrace && !r.idle(fn, in))) {
			draining++
			continue
		}
		if err := r.retire(ctx, in); err != nil {
			return 0, err
		}
	}
	r.setSpared(fn, kept > 0)
	if draining == 0 && d != "" {
		fn.Status.DrainingRevision, fn.Status.DrainingSince = "", nil
	}
	r.boot.forgetStale(backoffPrefix(fn.Namespace, fn.Name), s, c, fn.Status.DrainingRevision)
	if draining == 0 {
		return 0, nil
	}
	wait := r.drainGrace - elapsed
	if elapsed < r.handOutSettle {
		wait = r.handOutSettle - elapsed
	}
	return min(r.drainPoll, max(wait, time.Millisecond)), nil
}

// idle reports whether no call to worker in is in flight and the resolver has not handed it out recently (ADR-0143).
func (r *Reconciler) idle(fn *v1.Function, in runtime.Instance) bool {
	return r.calls == nil || r.calls.Idle(instanceURL(fn.Namespace, fn.Name, in), r.handOutSettle)
}

// retire stops a worker and forgets it (ADR-0143), with its boot-crash count (ADR-0160).
func (r *Reconciler) retire(ctx context.Context, in runtime.Instance) error {
	const op = "function.retire"
	r.boot.reset(in.ID)
	if err := r.runtime.Stop(ctx, in.ID); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "stop worker")
	}
	if err := r.runtime.Remove(ctx, in.ID); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "remove worker")
	}
	return nil
}

// stopAll stops every worker of a Function scaled to zero and retires those of revisions other than c, with their
// boot-crash entries (ADR-0169); c's stopped replicas stay listed for ADR-0142's wake backoff (ADR-0143 Decision 5).
// A revision an open workflow run holds is spared (ADR-0190 Decision 7).
func (r *Reconciler) stopAll(ctx context.Context, fn *v1.Function, c v1.ObjectName) error {
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return err
	}
	var held revhold.Holds
	kept := false
	for _, in := range insts {
		if in.Revision != c {
			if held == nil {
				if held, err = revhold.Held(ctx, r.store, fn.Namespace); err != nil {
					return err
				}
			}
			if held.Revision(fn.Name, fn.UID, in.Revision) {
				kept = true
				continue
			}
			if err := r.retire(ctx, in); err != nil {
				return err
			}
			continue
		}
		if in.State != runtime.StateStopped {
			if err := r.runtime.Stop(ctx, in.ID); err != nil {
				return fault.Wrapf(err, fault.KindOf(err), "function.stopAll", "stop worker")
			}
		}
	}
	r.setSpared(fn, kept)
	r.boot.forgetStale(backoffPrefix(fn.Namespace, fn.Name), string(c))
	return nil
}

// setSpared records whether fn's last retire pass kept a held revision's workers.
func (r *Reconciler) setSpared(fn *v1.Function, kept bool) {
	r.sparedMu.Lock()
	defer r.sparedMu.Unlock()
	if kept {
		r.spared[fnKey{fn.Namespace, fn.Name}] = fn.UID
	} else {
		delete(r.spared, fnKey{fn.Namespace, fn.Name})
	}
}

// isSpared reports whether fn's last retire pass kept a held revision's workers, which a later pass retires once
// no open run holds them.
func (r *Reconciler) isSpared(fn *v1.Function) bool {
	r.sparedMu.Lock()
	defer r.sparedMu.Unlock()
	uid, ok := r.spared[fnKey{fn.Namespace, fn.Name}]
	return ok && uid == fn.UID
}

// markHeld asks the next pass of the Function ns/name to look at its held revisions.
func (r *Reconciler) markHeld(ns v1.NamespaceName, name v1.ObjectName) {
	r.sparedMu.Lock()
	defer r.sparedMu.Unlock()
	r.held[fnKey{ns, name}] = true
}

// takeHeld clears and reports fn's held-revision hint. A pass takes it before it reads the Revisions, so a Revision
// event after the read marks it again.
func (r *Reconciler) takeHeld(fn *v1.Function) bool {
	r.sparedMu.Lock()
	defer r.sparedMu.Unlock()
	k := fnKey{fn.Namespace, fn.Name}
	marked := r.held[k]
	delete(r.held, k)
	return marked
}

// heldPending reports whether fn's held revisions wait for a pass.
func (r *Reconciler) heldPending(fn *v1.Function) bool {
	r.sparedMu.Lock()
	defer r.sparedMu.Unlock()
	return r.held[fnKey{fn.Namespace, fn.Name}]
}

// MapRevision maps a changed Revision to the Function that controls it (ADR-0190 Decision 5): the scaler's wake
// (Deploying) or reclaim (Idle) of a held revision is then acted on by that Function's next pass.
func (r *Reconciler) MapRevision(_ context.Context, obj v1.Object) []controller.Request {
	rev, ok := obj.(*v1.Revision)
	if !ok {
		return nil
	}
	owner, ok := v1.ControllerOf(rev.OwnerReferences)
	if !ok || owner.Kind != v1.KindFunction {
		return nil
	}
	if p := rev.Status.Phase; p == v1.PhaseDeploying || p == v1.PhaseIdle {
		r.markHeld(rev.Namespace, owner.Name)
	}
	return []controller.Request{{GVK: v1.KindFunction.GVK(), Namespace: rev.Namespace, Name: owner.Name}}
}

// convergeHeld drives fn's held revisions, those other than its current and serving ones whose Revision.status a
// wake or a pass has set (ADR-0190 Decisions 5 and 6). It reads them only when the Revision watch marked fn, when a
// held revision is still active or when a held revision's workers were spared this pass. It returns how soon the
// pass must come back for them (0 if none is active). env is fn's resolved bindings, nil to resolve them here.
func (r *Reconciler) convergeHeld(ctx context.Context, fn *v1.Function, env *boundEnv) (requeue time.Duration, err error) {
	if !r.takeHeld(fn) && !r.isSpared(fn) {
		return 0, nil
	}
	// a held revision with no worker is not spared, so only the hint brings a failed or active one back
	defer func() {
		if err != nil || requeue > 0 {
			r.markHeld(fn.Namespace, fn.Name)
		}
	}()
	res, err := r.store.List(ctx, v1.KindRevision.GVK(), store.ListOptions{Namespace: fn.Namespace})
	if err != nil {
		return 0, fault.Wrapf(err, fault.KindOf(err), "function.convergeHeld", "list revisions")
	}
	var revs []*v1.Revision
	for _, obj := range res.Items {
		rev, ok := obj.(*v1.Revision)
		if !ok || rev.Status.Phase == "" || !v1.ControlledBy(rev.OwnerReferences, v1.KindFunction, fn.UID) ||
			!activator.HeldRevision(fn, activator.FunctionRef{Revision: rev.Name, UID: fn.UID}) {
			continue
		}
		revs = append(revs, rev)
	}
	if len(revs) == 0 {
		return 0, nil
	}
	held, err := revhold.Held(ctx, r.store, fn.Namespace)
	if err != nil {
		return 0, err
	}
	if env == nil {
		e, err := r.bindings(ctx, fn)
		if err != nil {
			return 0, err
		}
		env = &e
	}
	for _, rev := range revs {
		after, err := r.convergeHeldRevision(ctx, fn, rev, held.Revision(fn.Name, fn.UID, rev.Name), env)
		if err != nil {
			return 0, err
		}
		requeue = earliest(requeue, after)
	}
	return requeue, nil
}

// heldActive reports whether a held revision in phase p has, or is getting, a worker.
func heldActive(p v1.Phase) bool {
	return p == v1.PhaseDeploying || p == v1.PhaseReady || p == v1.PhaseDegraded
}

// convergeHeldRevision drives one held revision rev of fn by its phase: one a wake moved to Deploying, or one Ready or
// Degraded, runs one solo worker through revisionTemplate and convergeRevision, and its readiness or failure is written
// to rev's status (Decision 5); one idle reclaim moved to Idle has its workers retired; one no open run holds anymore
// is written Idle, its workers being drain's. A Failed one stays Failed while held. Under a binding gate (Decision 9)
// a woken one does not boot: a failed binding fails it with the gate's reason and a waiting one keeps it Deploying,
// while a serving one keeps its worker as gateFailed keeps the serving revision's. It returns the pass's requeue for
// rev (0 when rev is not active).
func (r *Reconciler) convergeHeldRevision(ctx context.Context, fn *v1.Function, rev *v1.Revision, held bool, env *boundEnv) (time.Duration, error) {
	phase := rev.Status.Phase
	switch {
	case !held:
		if !heldActive(phase) {
			return 0, nil
		}
		return r.writeHeld(ctx, rev, v1.PhaseIdle, v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "NoReplicas"}, 0)
	case phase == v1.PhaseIdle:
		return 0, r.retireRevision(ctx, fn, rev.Name)
	case !heldActive(phase):
		return 0, nil
	}
	if g := env.gate; g != nil {
		ready := v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: g.reason, Message: g.readyMessage}
		switch {
		case phase != v1.PhaseDeploying:
			return r.supervisionPeriod, nil
		case g.phase == v1.PhaseFailed:
			if err := r.retireRevision(ctx, fn, rev.Name); err != nil {
				return 0, err
			}
			return r.writeHeld(ctx, rev, v1.PhaseFailed, ready, 0)
		}
		return r.writeHeld(ctx, rev, phase, ready, g.requeue)
	}
	tmpl, digest, err := r.revisionTemplate(ctx, fn, rev.Name)
	if err != nil {
		return r.heldBootFailed(ctx, rev, err)
	}
	serving := phase != v1.PhaseDeploying
	pass, err := r.convergeRevision(ctx, tmpl, rev.Name, digest, replicaRange(1), convergeOpts{serving: serving, scaleDown: true}, env.secret, env.catalog)
	if err != nil {
		return r.heldBootFailed(ctx, rev, err)
	}
	ready, failed, readyRetry, crashLoop, err := r.readyReplicas(ctx, fn.Namespace, fn.Name, rev.Name, pass.running, 1, readinessPath, r.bootTimeout, serving, r.boot)
	if err != nil {
		return 0, err
	}
	ul, err := r.stopUnlistened(ctx, fn, rev.Name, 1)
	if err != nil {
		return 0, err
	}
	if ul.crashLoop != "" {
		crashLoop = ul.crashLoop
	}
	running := pass.running - len(ul.stopped)
	retryAt := earlier(earlier(pass.retryAt, readyRetry), ul.retryAt)
	poll := readinessPoll
	if !retryAt.IsZero() && running == 0 {
		poll = max(retryAt.Sub(r.clock.Now()), time.Millisecond)
	}
	switch {
	case failed != "" && !serving:
		return r.writeHeld(ctx, rev, v1.PhaseFailed, v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "ShapeInvalid", Message: r.loadError(ctx, failed)}, 0)
	case ready >= 1:
		return r.writeHeld(ctx, rev, v1.PhaseReady, v1.Condition{Type: condReady, Status: v1.ConditionTrue}, r.supervisionPeriod)
	case pass.startErr != nil && running == 0:
		return r.writeHeld(ctx, rev, phase, v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "StartFailed", Message: pass.startErr.Error()}, poll)
	case crashLoop != "":
		return r.writeHeld(ctx, rev, phase, v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reasonCrashLoop, Message: crashLoop}, poll)
	case serving:
		return r.writeHeld(ctx, rev, v1.PhaseDegraded, v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "Restarting", Message: "a replica exited and is being replaced"}, poll)
	}
	return r.writeHeld(ctx, rev, v1.PhaseDeploying, v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "ShimNotReady"}, poll)
}

// heldBootFailed writes the error that kept the held revision rev from booting to its status, and retries it on the
// supervision period (Decision 5): an absent image is RuntimeUnavailable, as for the current revision (ADR-0149
// Decision 5), any other error StartFailed.
func (r *Reconciler) heldBootFailed(ctx context.Context, rev *v1.Revision, err error) (time.Duration, error) {
	reason, msg := "StartFailed", err.Error()
	if errors.Is(err, runtime.ErrImageUnavailable) {
		reason, msg = reasonRuntimeUnavailable, withoutOp(err)
	}
	return r.writeHeld(ctx, rev, rev.Status.Phase, v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: msg}, r.supervisionPeriod)
}

// writeHeld writes phase and the Ready condition to the held revision rev's status when they differ from it, and
// returns requeue. A write that conflicts with the scaler's comes back at readinessPoll instead of failing the pass.
func (r *Reconciler) writeHeld(ctx context.Context, rev *v1.Revision, phase v1.Phase, ready v1.Condition, requeue time.Duration) (time.Duration, error) {
	if cur, ok := rev.Status.Conditions.Get(condReady); ok && rev.Status.Phase == phase &&
		cur.Status == ready.Status && cur.Reason == ready.Reason && cur.Message == ready.Message {
		return requeue, nil
	}
	rev.Status.Phase = phase
	rev.Status.Conditions.Set(ready)
	if _, err := r.store.Update(ctx, rev); err != nil {
		if fault.KindOf(err) == fault.Conflict {
			return readinessPoll, nil
		}
		return 0, fault.Wrapf(err, fault.KindOf(err), "function.convergeHeld", "update revision %q", rev.Name)
	}
	return requeue, nil
}

// retireRevision stops and removes every worker of revision rev, so a later wake boots it afresh.
func (r *Reconciler) retireRevision(ctx context.Context, fn *v1.Function, rev v1.ObjectName) error {
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if in.Revision == rev {
			if err := r.retire(ctx, in); err != nil {
				return err
			}
		}
	}
	return nil
}

// stopRevision stops every worker of revision rev.
func (r *Reconciler) stopRevision(ctx context.Context, fn *v1.Function, rev v1.ObjectName) error {
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if in.Revision == rev && in.State != runtime.StateStopped {
			if err := r.runtime.Stop(ctx, in.ID); err != nil {
				return fault.Wrapf(err, fault.KindOf(err), "function.stopRevision", "stop worker")
			}
		}
	}
	return nil
}

// namedInstances returns the Function workers whose Name matches `name` in ns. For a solo
// function this is its replicas; for a pool worker `name` is the synthetic pool name
// (poolInstanceName) — the shared worker has its own instance identity, distinct from any
// member's, so a member's solo lookups never collide with the pool (ADR-0046). Another kind's
// worker of the same name, a CatalogService engine, is never one of them (ADR-0152).
func (r *Reconciler) namedInstances(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) ([]runtime.Instance, error) {
	all, err := r.runtime.List(ctx, ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "function.instances", "list workers")
	}
	out := make([]runtime.Instance, 0, len(all))
	for _, in := range all {
		if in.OwnerKind == v1.KindFunction && in.Name == name {
			out = append(out, in)
		}
	}
	return out, nil
}

// teardown stops and removes every worker of a (deleted) function.
func (r *Reconciler) teardown(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	insts, err := r.namedInstances(ctx, ns, name)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if err := r.retire(ctx, in); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), "function.teardown", "retire worker")
		}
	}
	r.boot.forget(backoffPrefix(ns, name)) // replicas with no instance too: a namesake starts at zero (ADR-0169)
	if r.invokeSockets != nil {
		r.invokeSockets.Remove(ns, name) // the local API socket dies with the Function (ADR-0064)
	}
	return nil
}

// ensureRevision stamps an immutable Revision for the Function's current generation if one
// does not already exist (the store bumps generation only on spec change; the activator's
// status-only Phase writes do not, so a wake never stamps a spurious Revision).
// It returns the Revision's authoritative artifact digest (ADR-0035): on the create path the
// digest is the explicit spec digest, else the resolved-and-pinned one; otherwise it is the
// stored Revision's digest — a stamped Revision is NEVER re-resolved, so a moved tag cannot
// drift it. It follows ADR-0172's table: a deleted namesake's Revision is dropped and the generation stamped afresh,
// another Function's is never touched (errRevisionStampFailed), and a stamped Revision that is gone fails closed
// (errRevisionMissing). The create path first retires every worker labelled with the Revision's name: the owner
// garbage collector may already have deleted a deleted namesake's Revision (ADR-0170).
func (r *Reconciler) ensureRevision(ctx context.Context, fn *v1.Function) (string, error) {
	const op = "function.ensureRevision"
	revName := revisionName(fn)
	existing, err := r.getRevision(ctx, fn.Namespace, v1.ObjectName(revName))
	switch {
	case err == nil:
		switch revisionOf(existing, fn) {
		case revSelf:
			return r.adoptRevision(ctx, fn, existing)
		case revOther:
			return "", stampTaken(existing)
		}
		if derr := r.dropRevision(ctx, existing); derr != nil {
			return "", derr
		}
	case fault.KindOf(err) != fault.NotFound:
		return "", fault.Wrapf(err, fault.KindOf(err), op, "get revision")
	case fn.Status.CurrentRevision == revName:
		return "", fmt.Errorf("%w: %s", errRevisionMissing, revName)
	}
	// create path: pin the digest — explicit if set, else resolve the ref (ADR-0035).
	pinned := fn.Spec.ImageDigest
	if pinned == "" {
		d, rerr := r.pinDigest(ctx, fn.Spec.Image)
		if rerr != nil {
			return "", rerr
		}
		pinned = d
	}
	obj, _ := v1.NewObject(v1.KindRevision)
	rev := obj.(*v1.Revision)
	rev.Name = v1.ObjectName(revName)
	rev.Namespace = fn.Namespace
	rev.ResourceGroup = fn.ResourceGroup
	owner := controllerRef(fn)
	rev.OwnerReferences = []v1.OwnerReference{owner}
	rev.Spec = v1.RevisionSpec{
		Function: owner.ObjectRef,
		Number:   fn.Generation,
		Runtime:  fn.Spec.Runtime,
		Handler:  fn.Spec.Handler,
		Image:    fn.Spec.Image, ImageDigest: pinned,
	}
	if err := r.retireStale(ctx, fn, rev.Name); err != nil {
		return "", err
	}
	if _, cerr := r.store.Create(ctx, rev); cerr != nil {
		switch fault.KindOf(cerr) {
		case fault.Conflict:
			return r.settleCreateConflict(ctx, fn, cerr)
		case fault.Invalid:
			return "", fmt.Errorf("%w: %w", errRevisionStampFailed, cerr)
		}
		return "", fault.Wrapf(cerr, fault.KindOf(cerr), op, "create revision")
	}
	fn.Status.CurrentRevision = revName
	return pinned, nil
}

// settleCreateConflict reads the Revision a concurrent create stored under fn's name: fn's own is adopted, another
// Function's fails the stamp, and a deleted namesake's returns the Conflict, so the next pass drops it (ADR-0172).
func (r *Reconciler) settleCreateConflict(ctx context.Context, fn *v1.Function, conflict error) (string, error) {
	const op = "function.ensureRevision"
	cur, err := r.getRevision(ctx, fn.Namespace, v1.ObjectName(revisionName(fn)))
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), op, "get revision after a create conflict")
	}
	switch revisionOf(cur, fn) {
	case revSelf:
		return r.adoptRevision(ctx, fn, cur)
	case revOther:
		return "", stampTaken(cur)
	}
	return "", fault.Wrapf(conflict, fault.Conflict, op, "create revision")
}

// adoptRevision makes rev, fn's own, the current revision. A ref-less one (stamped before 2dc9d26) first gets fn as
// its controller: the reconciler's one metadata write to a Revision (ADR-0172 Decision 5).
func (r *Reconciler) adoptRevision(ctx context.Context, fn *v1.Function, rev *v1.Revision) (string, error) {
	if _, owned := v1.ControllerOf(rev.OwnerReferences); !owned {
		rev.OwnerReferences = append(rev.OwnerReferences, controllerRef(fn))
		if _, err := r.store.Update(ctx, rev); err != nil {
			return "", fault.Wrapf(err, fault.KindOf(err), "function.adoptRevision", "adopt revision %q", rev.Name)
		}
	}
	fn.Status.CurrentRevision = string(rev.Name)
	return rev.Spec.ImageDigest, nil
}

// getRevision reads Revision name of namespace ns.
func (r *Reconciler) getRevision(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.Revision, error) {
	obj, err := r.store.Get(ctx, v1.KindRevision.GVK(), ns, name)
	if err != nil {
		return nil, err
	}
	rev, ok := obj.(*v1.Revision)
	if !ok {
		return nil, fault.Internalf("function.getRevision", "object %q is not a Revision", name)
	}
	return rev, nil
}

func controllerRef(fn *v1.Function) v1.OwnerReference {
	ref := v1.ObjectRef{Kind: v1.KindFunction, Namespace: fn.Namespace, Name: fn.Name}
	return v1.OwnerReference{ObjectRef: ref, UID: fn.UID, Controller: true}
}

const (
	maxRevisionName = 63 // a DNS-1123 label, validated by store.Create
	revisionHashLen = 8  // hex digits of SHA-256(Function name) in a shortened name
)

// revisionName is the Revision a Function's generation stamps (ADR-0020, ADR-0172): <name>-<generation> while that
// fits a label, else the name cut to fit, the first hex digits of its SHA-256 and the generation.
func revisionName(fn *v1.Function) string {
	gen := strconv.FormatInt(fn.Generation, 10)
	name := string(fn.Name)
	if len(name)+1+len(gen) <= maxRevisionName {
		return name + "-" + gen
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:])[:revisionHashLen] + "-" + gen
	return name[:maxRevisionName-len(suffix)] + suffix
}

// revisionOwner is whose a stored Revision is, judged against the Function that would stamp it (ADR-0172 Decision 5).
type revisionOwner int

const (
	revSelf     revisionOwner = iota // this Function's
	revNamesake                      // a deleted Function's of the same name
	revOther                         // another Function's
)

// revisionOf follows ADR-0172 Decision 5: a controller ref decides first (this UID revSelf, another UID of this name
// revNamesake, another Function revOther); ref-less, spec.function naming another Function is revOther, then
// revSelf if the status names it, else revNamesake.
func revisionOf(rev *v1.Revision, fn *v1.Function) revisionOwner {
	if o, ok := v1.ControllerOf(rev.OwnerReferences); ok {
		switch {
		case o.Kind != v1.KindFunction || o.Name != fn.Name:
			return revOther
		case o.UID == fn.UID:
			return revSelf
		}
		return revNamesake
	}
	if f := rev.Spec.Function.Name; f != "" && f != fn.Name {
		return revOther
	}
	if name := string(rev.Name); fn.Status.CurrentRevision == name || fn.Status.ServingRevision == name {
		return revSelf
	}
	return revNamesake
}

// stampTaken is the error of a Revision name another Function's Revision holds.
func stampTaken(rev *v1.Revision) error {
	ref := rev.Spec.Function
	if o, ok := v1.ControllerOf(rev.OwnerReferences); ok {
		ref = o.ObjectRef
	}
	return fmt.Errorf("%w: %s is %s %q's", errRevisionStampFailed, rev.Name, ref.Kind, ref.Name)
}

// retireStale retires every worker of fn's name labelled with Revision rev before that Revision is created: no worker
// of a previous Revision of that name survives (issue #55, ADR-0170 Decision 6). A crash between the retire and the
// create retires again on the next pass.
func (r *Reconciler) retireStale(ctx context.Context, fn *v1.Function, rev v1.ObjectName) error {
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if in.Revision == rev {
			if err := r.retire(ctx, in); err != nil {
				return err
			}
		}
	}
	r.boot.forget(backoffPrefix(fn.Namespace, fn.Name) + string(rev) + "/")
	return nil
}

// dropRevision deletes rev, a deleted Function's Revision, so the create path stamps it afresh and retires its
// workers (issue #55).
func (r *Reconciler) dropRevision(ctx context.Context, rev *v1.Revision) error {
	if err := r.store.Delete(ctx, v1.KindRevision.GVK(), rev.Namespace, rev.Name, rev.ResourceVersion); err != nil && fault.KindOf(err) != fault.NotFound {
		return fault.Wrapf(err, fault.KindOf(err), "function.dropRevision", "delete revision %q", rev.Name)
	}
	return nil
}

// pinDigest resolves an OCI artifact ref → digest at Revision stamp (ADR-0035). With no
// resolver configured (file:// dev / legacy mode) there is no digest to pin — the
// FileMaterializer reads the path. With a resolver, an unresolvable / empty result fails
// closed (errArtifactUnresolved) so a Function never deploys an unverified ref.
func (r *Reconciler) pinDigest(ctx context.Context, uri string) (string, error) {
	if r.resolver == nil {
		return "", nil
	}
	d, err := r.resolver.Resolve(ctx, uri)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errArtifactUnresolved, err)
	}
	if d == "" {
		return "", fmt.Errorf("%w: %q resolved to an empty digest", errArtifactUnresolved, uri)
	}
	return d, nil
}

// programAllRoutes programs the gateway with the FULL desired route table — every Ready
// function's route — because gateway.ProgramRoutes is replace-all (ADR-0013): programming
// only one function's route would clobber the rest.
func (r *Reconciler) programAllRoutes(ctx context.Context) error {
	list, err := r.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "function.programAllRoutes", "list functions")
	}
	routes := make([]gateway.Route, 0, len(list.Items))
	for _, obj := range list.Items {
		fn, ok := obj.(*v1.Function)
		if !ok || fn.Status.Phase != v1.PhaseReady {
			continue
		}
		// A List error programs nothing, so no pass drops a route because the runtime cannot list (ADR-0161).
		upstream, err := r.upstreamForFn(ctx, fn)
		if err != nil {
			return err
		}
		if upstream == "" {
			// A Ready function with no listening worker right now. Omit it rather than passing an empty upstream to the
			// gateway, which rejects the WHOLE batch (one stale function would drop every route). It is reprogrammed on
			// its own next reconcile, once its upstream resolves.
			continue
		}
		routes = append(routes, gateway.Route{
			ID:         gateway.RouteID(string(fn.Namespace) + "/" + string(fn.Name)),
			PathPrefix: "/function/" + string(fn.Name),
			Upstream:   upstream,
		})
	}
	return r.gateway.ProgramRoutes(ctx, routes)
}

// upstreamForFn returns a function's upstream URL: the address of the pool worker servingPool picks for a pooled
// function (resolved by its pool key, not by Instance.Name, since the worker is shared — ADR-0046 Decision 5; ADR-0190
// Decision 8), or a listening worker of its serving revision (ADR-0143, ADR-0161). "" if none listens.
func (r *Reconciler) upstreamForFn(ctx context.Context, fn *v1.Function) (string, error) {
	// poolKeyFor already gates on a pool host existing for the runtime family (ADR-0050), so route
	// to the shared pool worker whenever it returns a key — NOT only for the node poolShimCommand
	// (a python* pool has only a python host configured, so a node-host check would mis-route it solo).
	if r.pooled(fn) {
		key, ok := pooling.ParsePool(fn.Namespace, fn.Status.Pool)
		if !ok {
			return "", nil
		}
		insts, err := r.namedInstances(ctx, fn.Namespace, poolInstanceName(key))
		if err != nil {
			return "", err
		}
		if w, ok := r.servingPool(insts); ok {
			return instanceURL(fn.Namespace, w.Name, w), nil
		}
		return "", nil
	}
	return r.upstreamOf(ctx, fn.Namespace, fn.Name, servingRevision(fn))
}

// pinnedPoolUpstream returns the address of the newest listening pool worker of fn's key that holds the manifest last
// built for the key, while that manifest holds fn at rev's code; "" otherwise. An old pool worker, which may hold fn's
// previous code, is never handed out for a pinned call (ADR-0190 Decisions 4 and 8).
func (r *Reconciler) pinnedPoolUpstream(ctx context.Context, fn *v1.Function, rev *v1.Revision) (string, error) {
	key, ok := pooling.ParsePool(fn.Namespace, fn.Status.Pool)
	if !ok {
		return "", nil
	}
	r.poolMu.Lock()
	hold, held := r.poolHolds[key]
	r.poolMu.Unlock()
	if !held || hold.codes[fn.Name] != (memberCode{image: rev.Spec.Image, digest: rev.Spec.ImageDigest, handler: rev.Spec.Handler}) {
		return "", nil
	}
	insts, err := r.namedInstances(ctx, fn.Namespace, poolInstanceName(key))
	if err != nil {
		return "", err
	}
	cur, _ := splitPool(insts, hold.sig)
	if w, ok := r.servingPool(cur); ok {
		return instanceURL(fn.Namespace, w.Name, w), nil
	}
	return "", nil
}

// servingRevision is the Revision whose workers receive a solo Function's calls: servingRevision, or the current
// revision while nothing serves yet (ADR-0143).
func servingRevision(fn *v1.Function) v1.ObjectName {
	if fn.Status.ServingRevision != "" {
		return v1.ObjectName(fn.Status.ServingRevision)
	}
	return v1.ObjectName(fn.Status.CurrentRevision)
}

// upstreamOf returns the upstream URL of the lowest listening replica of revision rev named `name` in ns, or "" when
// none listens (ADR-0161 Decision 2).
func (r *Reconciler) upstreamOf(ctx context.Context, ns v1.NamespaceName, name, rev v1.ObjectName) (string, error) {
	insts, err := r.namedInstances(ctx, ns, name)
	if err != nil {
		return "", err
	}
	best := -1
	for i, in := range insts {
		if in.Revision == rev && r.listening(in) && (best < 0 || in.Replica < insts[best].Replica) {
			best = i
		}
	}
	if best < 0 {
		return "", nil
	}
	return instanceURL(ns, name, insts[best]), nil
}

// instanceURL is a worker's upstream URL. In shim mode (ADR-0030) the worker reports its resolved IP:Port (the shim's
// listening address); legacy mode falls back to a synthetic per-function host + the default port.
func instanceURL(ns v1.NamespaceName, name v1.ObjectName, in runtime.Instance) string {
	host := in.IP
	if host == "" {
		host = string(name) + "." + string(ns)
	}
	port := upstreamPort
	if in.Port > 0 {
		port = strconv.Itoa(in.Port)
	}
	return "http://" + host + ":" + port
}

// readyReplicas reports how many replicas of revision rev are serving and, for a shape failure, a failed instance
// ("" if none). In legacy mode (no Materializer) ready == running (ADR-0020, unchanged). In shim mode (ADR-0030) it polls
// each listening replica's health endpoint at path (ADR-0161), and a listening solo replica, or a running pool worker,
// that has not become ready within bootLimit of its last start is a shape failure; a zero bootLimit sets no limit. A solo
// replica that never listens is stopUnlistened's. A terminal replica is read by how it ended (ADR-0160 Decision 4): only an exit 3 before listening in a pass not serving is failed, and the boot crashes it judges are
// counted on boot, giving the earliest retry and, from the lowest replica with a count, the crash-loop message. With a
// nil boot every Failed replica is failed, as before (the pool worker). Only replicas below `below` count (ADR-0142): a
// replica being scaled away is not judged. A List error is returned, so the pass writes no status from a failed read
// (issue #353).
func (r *Reconciler) readyReplicas(ctx context.Context, ns v1.NamespaceName, name, rev v1.ObjectName, running, below int,
	path string, bootLimit time.Duration, serving bool, boot *bootBackoff) (ready int, failed runtime.InstanceID,
	retryAt time.Time, crashLoop string, err error) {
	if r.materializer == nil {
		return running, "", time.Time{}, "", nil
	}
	insts, err := r.namedInstances(ctx, ns, name)
	if err != nil {
		return 0, "", time.Time{}, "", err
	}
	now := r.clock.Now()
	lowest := -1
	for _, in := range insts {
		if in.Revision != rev || in.Replica >= below {
			continue
		}
		var crash bootCrash
		switch {
		case in.State == runtime.StateRunning:
			if boot != nil && in.Listened {
				boot.reset(in.ID)
			} else if boot != nil {
				crash, _ = boot.crash(in.ID)
			}
			listening := r.listening(in)
			switch {
			case listening && r.probeReady(ctx, in.IP, in.Port, path):
				ready++
			case bootLimit > 0 && (listening || boot == nil) && now.Sub(lastStart(in)) >= bootLimit:
				failed = lowerID(failed, in.ID)
			}
		case !in.State.Terminal():
		case boot == nil:
			if in.State == runtime.StateFailed {
				failed = lowerID(failed, in.ID)
			}
		default:
			class := classifyExit(in, serving, false)
			if class == exitShapeError {
				failed = lowerID(failed, in.ID)
				continue
			}
			var due time.Time
			crash, due = boot.observe(in, class)
			retryAt = earlier(retryAt, due)
		}
		if crash.count > 0 && crash.message != "" && (lowest < 0 || in.Replica < lowest) {
			lowest, crashLoop = in.Replica, crash.message
		}
	}
	return ready, failed, retryAt, crashLoop, nil
}

// lowerID is the lower of two instance IDs, "" counting as none, so the error reported for a shape failure does not
// vary with List's order.
func lowerID(a, b runtime.InstanceID) runtime.InstanceID {
	if a == "" || b < a {
		return b
	}
	return a
}

// loadError is the error failed instance id's shim wrote before it exited — the last line of its captured output —
// which ShapeValid carries (ADR-0030 §4b); "" for no instance, and a generic message when the output is unreadable.
// An instance still running was failed for never becoming ready (readyReplicas' boot limit), so it has no load error.
func (r *Reconciler) loadError(ctx context.Context, id runtime.InstanceID) string {
	if id == "" {
		return ""
	}
	if in, err := r.runtime.Status(ctx, id); err == nil && in.State == runtime.StateRunning {
		return r.notReadyError()
	}
	last := "the runtime shim could not load the handler"
	rc, err := r.runtime.Logs(ctx, id)
	if err != nil {
		return last
	}
	defer func() { _ = rc.Close() }()
	sc := bufio.NewScanner(rc)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			last = line
		}
	}
	return last
}

// notReadyError is why a replica that ran for the boot timeout without becoming ready failed.
func (r *Reconciler) notReadyError() string {
	return "the handler did not become ready within " + r.bootTimeout.String()
}

// The health endpoints a shim and a pool host serve (ADR-0030 §4b, ADR-0044); a pool host also reports each
// member's state.
const (
	readinessPath = "/health/readiness"
	livenessPath  = "/health/liveness"
	membersPath   = "/health/members"
)

// probeReady issues GET path against a shim and reports a 200 (ADR-0030 §4b).
func (r *Reconciler) probeReady(ctx context.Context, ip string, port int, path string) bool {
	host := ip
	if host == "" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s:%d%s", host, port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer httpx.CloseBody(resp.Body)
	return resp.StatusCode == http.StatusOK
}

// probeMembers issues GET /health/members against a pool host and decodes its 200 answer.
func (r *Reconciler) probeMembers(ctx context.Context, ip string, port int) ([]memberHealth, bool) {
	host := ip
	if host == "" {
		host = "127.0.0.1"
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, fmt.Sprintf("http://%s:%d%s", host, port, membersPath), nil)
	if err != nil {
		return nil, false
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer httpx.CloseBody(resp.Body)
	var members []memberHealth
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&members) != nil {
		return nil, false
	}
	return members, true
}

// Endpoints returns the production activator.Endpoints (the seam ADR-0016 deferred to P-M):
// a function's ready worker upstream.
func (r *Reconciler) Endpoints() activator.Endpoints { return endpoints{r: r} }

type endpoints struct{ r *Reconciler }

func (e endpoints) Upstream(ctx context.Context, fn activator.FunctionRef) (string, bool, error) {
	// "ready" must mean the shim is actually serving — gate on the reconciler's readiness
	// verdict (Status.Phase==Ready, set only after /health/readiness passes), not merely on
	// a running process. Otherwise the activator forwards before the shim binds (502). The
	// upstream is still returned so a caller can see the address while it boots. For a pooled
	// function the upstream is its pool worker's address, resolved by the pool key, plus the
	// /function/<name> path the pool routes the member by (ADR-0046 Decision 5): every caller (the
	// data plane, a workflow step, a Sensor action) gets it from here, where pooling is decided.
	obj, err := e.r.store.Get(ctx, v1.KindFunction.GVK(), fn.Namespace, fn.Name)
	if fn.Revision != "" {
		return e.pinned(ctx, fn, obj, err)
	}
	if err != nil {
		return "", false, nil
	}
	f, ok := obj.(*v1.Function)
	if !ok {
		return "", false, nil
	}
	up, _ := e.r.upstreamForFn(ctx, f) // a runtime that cannot list hands out no worker: "", not ready (ADR-0161)
	if e.r.pooled(f) && up != "" {
		up += "/function/" + string(f.Name)
	}
	ready := f.Status.Phase == v1.PhaseReady && up != ""
	if ready && e.r.calls != nil {
		e.r.calls.HandedOut(up) // the drain waits out a call that resolved this upstream (ADR-0143)
	}
	return up, ready, nil
}

// pinned resolves a ref pinned to one revision (ADR-0190 Decision 4): a listening worker of exactly that revision
// through upstreamOf, or, while the pinned revision is a pooled member's current one, a pool worker that holds it
// (pinnedPoolUpstream). A Function that is
// gone or has another UID, or a Revision that is gone or another Function's, is fault.NotFound naming the pin; nothing
// falls back to the serving revision. A pinned revision that is not the serving one is ready once a worker listens.
func (e endpoints) pinned(ctx context.Context, ref activator.FunctionRef, obj v1.Object, err error) (string, bool, error) {
	const op = "function.Upstream"
	gone := func(why string) error {
		return fault.NotFoundf(op, "pinned revision %q of function %s/%s (uid %s) %s", ref.Revision, ref.Namespace, ref.Name, ref.UID, why)
	}
	if fault.KindOf(err) == fault.NotFound {
		return "", false, gone("is gone: the function is deleted")
	}
	f, ok := obj.(*v1.Function)
	if err != nil || !ok {
		return "", false, nil
	}
	if f.UID != ref.UID {
		return "", false, gone(fmt.Sprintf("is gone: the function has uid %s", f.UID))
	}
	rev, err := e.r.getRevision(ctx, ref.Namespace, ref.Revision)
	if fault.KindOf(err) == fault.NotFound {
		return "", false, gone("is not found")
	}
	if err != nil {
		return "", false, nil
	}
	if revisionOf(rev, f) != revSelf {
		return "", false, gone("belongs to another function")
	}
	var up string
	if e.r.pooled(f) && string(ref.Revision) == f.Status.CurrentRevision {
		if up, _ = e.r.pinnedPoolUpstream(ctx, f, rev); up != "" {
			up += "/function/" + string(f.Name)
		}
	} else {
		up, _ = e.r.upstreamOf(ctx, ref.Namespace, ref.Name, ref.Revision)
	}
	ready := up != "" && f.Status.Phase == v1.PhaseReady
	if activator.HeldRevision(f, ref) { // judged by its own status (ADR-0190 Decision 5); "" is a worker kept from before
		ready = up != "" && rev.Status.Phase != v1.PhaseIdle && rev.Status.Phase != v1.PhaseFailed
	} else if ref.Revision != servingRevision(f) {
		ready = up != ""
	}
	if ready && e.r.calls != nil {
		e.r.calls.HandedOut(up)
	}
	return up, ready, nil
}

// shimFor selects the shim launch prefix for a function's runtime (ADR-0049): the longest
// registered family prefix that matches fn.Spec.Runtime (e.g. "python" → the python shim), else
// the default ShimCommand for a node-family runtime; nil for any other runtime, which no shim on
// this node runs (ADR-0149 Decision 1). The rest of the worker spec (env, portfile, readiness) is
// identical regardless of which shim is chosen.
func (r *Reconciler) shimFor(rt v1.RuntimeName) []string {
	if cmd := r.familyShim(rt); cmd != nil {
		return cmd
	}
	if isNodeFamily(rt) {
		return r.shimCommand
	}
	return nil
}

// familyShim is the shim of the longest registered family prefix that matches rt; nil when none does.
func (r *Reconciler) familyShim(rt v1.RuntimeName) []string {
	best, cmd := "", []string(nil)
	for family, c := range r.shimByFamily {
		if len(family) > len(best) && strings.HasPrefix(string(rt), family) {
			best, cmd = family, c
		}
	}
	return cmd
}

// reasonRuntimeUnavailable is the reason of a Function whose runtime this node cannot serve (ADR-0149 Decision 4).
const reasonRuntimeUnavailable = "RuntimeUnavailable"

// runtimeUnavailable is the runtime gate (ADR-0149 Decision 2). In process mode a runtime is unavailable when no shim
// runs it and the Function does not pool on a host of its own (issue #371); in containerd mode the CatalogService
// engine image is not a function runtime. The legacy mode without a Materializer is never gated.
func (r *Reconciler) runtimeUnavailable(fn *v1.Function) (string, bool) {
	rt := fn.Spec.Runtime
	var cause string
	switch {
	case r.materializer == nil:
		return "", false
	case r.endpointMode == EndpointLoopback:
		if r.shimFor(rt) != nil {
			return "", false
		}
		if r.pooled(fn) {
			return "", false
		}
		cause = "no shim is registered for it"
	case r.endpointMode == EndpointNetnsFixedPort && rt == catalogsvc.DuckDBRuntime:
		cause = "it is the CatalogService engine image, not a function runtime"
	default:
		return "", false
	}
	return unavailablePrefix(rt) + ": " + cause, true
}

// unavailablePrefix is the message prefix of every RuntimeUnavailable outcome (ADR-0149 Decision 4).
func unavailablePrefix(rt v1.RuntimeName) string {
	return fmt.Sprintf("runtime %q is not available on this node", rt)
}

// addInvokeSocket sets FUNCD_INVOKE_SOCKET so the worker's shim can dial the per-sandbox worker-node
// local API — context.invoke (ADR-0064) AND context.kv (ADR-0069). EVERY function gets the socket (KV is
// available to all; the link-as-grant check for invoke stays at RESOLVE time, so a linkless function's
// invoke still fails closed). No-op when the local API is off entirely (r.invokeSockets nil); a socket that cannot be
// provisioned is an error, so no worker starts without its local API (issue #358).
func (r *Reconciler) addInvokeSocket(env map[string]string, fn *v1.Function) error {
	sock, err := r.invokeSocket(fn)
	if sock != "" {
		env["FUNCD_INVOKE_SOCKET"] = sock
	}
	return err
}

// invokeSocket provisions fn's local API socket and returns its host path; "" when the local API is off.
func (r *Reconciler) invokeSocket(fn *v1.Function) (string, error) {
	if r.invokeSockets == nil {
		return "", nil
	}
	sock, err := r.invokeSockets.SocketFor(fn.Namespace, fn.Name)
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), "function.invokeSocket", "provision the local API socket")
	}
	return sock, nil
}

// addS3Env injects the per-function S3 SigV4 keypair + endpoint (ADR-0085) when the s3gateway
// is enabled AND the function declares spec.blob — AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY
// (derived from the node master secret over the Ref), AWS_REGION, AWS_ENDPOINT_URL_S3. The
// function never chooses its secret; it cannot derive a peer's. A function without spec.blob,
// or a disabled gateway, gets nothing. The secret is set into env but never logged.
func (r *Reconciler) addS3Env(env map[string]string, fn *v1.Function) {
	if !r.s3Gateway.Enabled || r.s3Gateway.Derive == nil || len(fn.Spec.Blob) == 0 {
		return
	}
	access, secret := r.s3Gateway.Derive(v1.KindFunction, string(fn.Namespace), string(fn.Name))
	env["AWS_ACCESS_KEY_ID"] = access
	env["AWS_SECRET_ACCESS_KEY"] = secret
	env["AWS_REGION"] = "us-east-1"
	// The sandbox-facing endpoint: Endpoint when set (the node address a netns'd worker can reach —
	// the CNI bridge gateway IP under containerd), else derive from the bind ListenAddr (loopback dev).
	endpoint := r.s3Gateway.Endpoint
	if endpoint == "" {
		endpoint = "http://" + r.s3Gateway.ListenAddr
	}
	env["AWS_ENDPOINT_URL_S3"] = endpoint
}

// isPythonFamily reports whether rt is a python-family runtime (e.g. "python314") — the family
// whose vendored deps import via PYTHONPATH (ADR-0089). It mirrors shimFor's family-prefix match.
func isPythonFamily(rt v1.RuntimeName) bool {
	return strings.HasPrefix(string(rt), "python")
}

// isNodeFamily reports whether rt is a node-family runtime (e.g. "nodejs22"), the only family the default shim and pool
// host serve (ADR-0149 Decision 1).
func isNodeFamily(rt v1.RuntimeName) bool {
	return strings.HasPrefix(string(rt), "node")
}

// addBundleEnv sets the generic bundle env (ADR-0089) into a worker's env: FUNCD_BUNDLE_DIR names
// the bundle root (any runtime — a handler resolves its own asset dirs from it, e.g. duckdb-ext),
// and for the python family PYTHONPATH names the same root so every vendored dependency imports.
// bundleRoot is the container artifact dir in container mode and Dir(artifactPath) in process mode.
// A single-file function gets the same env harmlessly (the dir holds only the one file), so bundle
// vs single-file is transparent to the runtime; the ADR-0049 shim is unchanged.
func addBundleEnv(env map[string]string, rt v1.RuntimeName, bundleRoot string) {
	env["FUNCD_BUNDLE_DIR"] = bundleRoot
	if isPythonFamily(rt) {
		env["PYTHONPATH"] = bundleRoot
	}
}

// contractDeliveryNames are the delivered contract file names the materializer writes into the
// bundle root (ADR-0123): the in-bundle blob for a bundle, the dotfile sidecar for a single-file
// function. The reconciler probes the host bundle root for one of them to set FUNCD_CONTRACT_PATH.
//
//nolint:gochecknoglobals // an immutable lookup table (a slice can't be const)
var contractDeliveryNames = []string{"__funcd_contract.json", ".funcd-contract.json"}

// contractFileIn reports the delivered contract file name present in the host bundle root dir, if
// any (ADR-0123). "" when none — a contract-less legacy artifact, or a dev FileMaterializer path
// with no delivered schema (the shim then falls back to a module-baked validator).
func contractFileIn(dir string) (name string, ok bool) {
	for _, n := range contractDeliveryNames {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return n, true
		}
	}
	return "", false
}

// addContractEnv sets FUNCD_CONTRACT_PATH (ADR-0123) so the shim compiles the delivered I/O schema
// at worker warm-up. hostRoot is the bundle root on the host (where the materializer wrote the
// contract file); workerRoot is the path the same dir is visible at inside the worker — equal to
// hostRoot in process mode, the container mount target in container mode. No-op when no contract
// file was delivered (legacy/dev), so the shim's fail-closed path only triggers for a contracted
// function whose schema genuinely failed to reach the worker.
func addContractEnv(env map[string]string, hostRoot, workerRoot string) {
	if name, ok := contractFileIn(hostRoot); ok {
		env["FUNCD_CONTRACT_PATH"] = filepath.Join(workerRoot, name)
	}
}

// shimEnv is the env a solo shim worker starts from: the artifact path as the worker sees it, the
// handler export (ADR-0030), and FUNCD_FUNCTION, which the shim names its invocation spans after (ADR-0101).
func shimEnv(fn *v1.Function, artifact string) map[string]string {
	return map[string]string{
		"FUNCD_ARTIFACT": artifact,
		"FUNCD_HANDLER":  fn.Spec.Handler,
		"FUNCD_FUNCTION": string(fn.Name),
	}
}

// addRecordBound passes funclog.maxRecordBytes to a shim or a pool host, which bounds each record line by it (ADR-0168).
func (r *Reconciler) addRecordBound(env map[string]string) {
	if r.logMaxRecordBytes > 0 {
		env["FUNCD_FUNCLOG_MAX_RECORD_BYTES"] = strconv.Itoa(r.logMaxRecordBytes)
	}
}

// workerSpec builds the runtime spec for one replica. In shim mode (a Materializer is
// configured, ADR-0030) it launches the runtime shim with the materialized artifact +
// handler in the env; otherwise it runs the legacy long-lived placeholder (ADR-0020).
// secretEnv is the already-resolved secret env map (ADR-0057), merged into Env with
// reserved-FUNCD_-key precedence; nil ⇒ none.
func (r *Reconciler) workerSpec(fn *v1.Function, replica int, artifactPath string, secretEnv, catalogEnv map[string]string) (runtime.WorkerSpec, error) {
	if r.materializer != nil && r.endpointMode == EndpointNetnsFixedPort {
		// Container mode (ADR-0032): the shim is the curated image's entrypoint (Command
		// empty), the artifact is bind-mounted read-only, and it binds a fixed netns port.
		env := shimEnv(fn, filepath.Join(containerArtifactDir, filepath.Base(artifactPath)))
		r.addRecordBound(env)
		env["FUNCD_PORT"] = strconv.Itoa(containerShimPort)
		addBundleEnv(env, fn.Spec.Runtime, containerArtifactDir) // FUNCD_BUNDLE_DIR (+ PYTHONPATH, python family), ADR-0089
		// FUNCD_CONTRACT_PATH (ADR-0123): the schema is delivered into the host bundle root
		// (Dir(artifactPath)) which is bind-mounted at containerArtifactDir, so the in-worker path
		// is under the mount target.
		addContractEnv(env, filepath.Dir(artifactPath), containerArtifactDir)
		r.addS3Env(env, fn)
		r.addCatalogEnv(env, catalogEnv)  // FUNCD_CATALOG_<ALIAS>_URL/_TOKEN written DIRECTLY (ADR-0091) — never via mergeSecretEnv
		r.addCatalogExtensionDir(env, fn) // DUCKDB_EXTENSION_DIRECTORY for a catalog consumer (dev; prod uses the bundle's duckdb-ext)
		r.mergeSecretEnv(env, secretEnv)
		mounts := []runtime.Mount{{
			Source: filepath.Dir(artifactPath), Target: containerArtifactDir, ReadOnly: true,
		}}
		// Bind-mount the per-function local API socket into the sandbox so the shim can dial
		// context.invoke (ADR-0064) + context.kv (ADR-0069) at the in-container path. Every function gets one.
		sock, err := r.invokeSocket(fn)
		if err != nil {
			return runtime.WorkerSpec{}, err
		}
		if sock != "" {
			env["FUNCD_INVOKE_SOCKET"] = containerInvokeSocket
			mounts = append(mounts, runtime.Mount{Source: sock, Target: containerInvokeSocket})
		}
		return runtime.WorkerSpec{
			Namespace: fn.Namespace,
			Name:      fn.Name,
			OwnerKind: v1.KindFunction,
			Replica:   replica,
			Image:     r.imageFor(string(fn.Spec.Runtime)),
			Mounts:    mounts,
			Env:       env,
		}, nil
	}
	if r.materializer != nil {
		// Process mode (ADR-0030): ShimCommand launches the shim; loopback + portfile.
		env := shimEnv(fn, artifactPath)
		r.addRecordBound(env)
		addBundleEnv(env, fn.Spec.Runtime, filepath.Dir(artifactPath)) // FUNCD_BUNDLE_DIR (+ PYTHONPATH, python family), ADR-0089
		// FUNCD_CONTRACT_PATH (ADR-0123): the delivered schema sits in the same host dir the shim
		// reads directly in process mode, so host root == worker root.
		addContractEnv(env, filepath.Dir(artifactPath), filepath.Dir(artifactPath))
		// FUNCD_INVOKE_SOCKET for context.invoke (ADR-0064); reachable on the host
		if err := r.addInvokeSocket(env, fn); err != nil {
			return runtime.WorkerSpec{}, err
		}
		r.addS3Env(env, fn)               // AWS_* S3 keypair + endpoint for a spec.blob function (ADR-0085)
		r.addCatalogEnv(env, catalogEnv)  // FUNCD_CATALOG_<ALIAS>_URL/_TOKEN written DIRECTLY (ADR-0091) — never via mergeSecretEnv
		r.addCatalogExtensionDir(env, fn) // DUCKDB_EXTENSION_DIRECTORY for a catalog consumer (dev; prod uses the bundle's duckdb-ext)
		r.mergeSecretEnv(env, secretEnv)
		return runtime.WorkerSpec{
			Namespace: fn.Namespace,
			Name:      fn.Name,
			OwnerKind: v1.KindFunction,
			Replica:   replica,
			Image:     string(fn.Spec.Runtime),
			Command:   r.shimFor(fn.Spec.Runtime), // node by default; python* → the python shim (ADR-0049)
			Env:       env,
		}, nil
	}
	return runtime.WorkerSpec{
		Namespace: fn.Namespace,
		Name:      fn.Name,
		OwnerKind: v1.KindFunction,
		Replica:   replica,
		Image:     string(fn.Spec.Runtime),
		// Legacy placeholder (no Materializer): a long-lived process stands in for the
		// worker; real execution is the shim path (ADR-0030).
		Command: []string{"sleep", "86400"},
	}, nil
}

func retryOnConflict(err error, op string) error {
	if fault.KindOf(err) == fault.Conflict {
		return nil // re-reconciled on the next watch event with the fresh RV
	}
	return fault.Wrapf(err, fault.KindOf(err), op, "status write-back")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// NewBasicValidator returns the V1 spec-well-formedness ShapeValidator (the authoritative
// load-time materialization is the shim's, on the Linux lane, P-S).
func NewBasicValidator() ShapeValidator { return basicValidator{} }

type basicValidator struct{}

func (basicValidator) Validate(_ context.Context, fn *v1.Function) error {
	switch {
	case fn.Spec.Runtime == "":
		return fault.Invalidf("function.shape", "function %q: spec.runtime is required", fn.Name)
	case fn.Spec.Handler == "":
		return fault.Invalidf("function.shape", "function %q: spec.handler is required", fn.Name)
	case fn.Spec.Image == "":
		return fault.Invalidf("function.shape", "function %q: spec.image is required", fn.Name)
	}
	return nil
}

// artifactPlatforms lists the platforms the artifact at digest provides (ADR-0145): nil when no platform resolver is
// wired, no digest is pinned, or the artifact runs anywhere.
func (r *Reconciler) artifactPlatforms(ctx context.Context, uri, digest string) ([]v1.OCIPlatform, error) {
	if r.platformsOf == nil || digest == "" {
		return nil, nil
	}
	ps, err := r.platformsOf.Platforms(ctx, uri, digest)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "function.artifactPlatforms", "list the platforms of %s@%s", uri, digest)
	}
	return ps, nil
}

// placeable asks the scheduler whether fn's artifact at digest can run on a node (ADR-0145): an error matching
// scheduler.ErrNoMatchingPlatform when none of the artifact's platforms is a node's.
func (r *Reconciler) placeable(ctx context.Context, fn *v1.Function, uri, digest string) error {
	platforms, err := r.artifactPlatforms(ctx, uri, digest)
	if err != nil || len(platforms) == 0 {
		return err
	}
	_, err = r.scheduler.Schedule(ctx, scheduler.Request{Namespace: fn.Namespace, Name: fn.Name, Replica: 0, Platforms: platforms})
	return err
}

// placementMessage is the innermost message of a refused placement, without the operation prefixes: the
// scheduler's ("artifact provides [...]; node ... runs ...") or the resolver's (an index that names no platform).
func placementMessage(err error) string {
	msg := err.Error()
	var fe *fault.Error
	for errors.As(err, &fe) {
		if fe.Msg != "" {
			msg = fe.Msg
		}
		err = fe.Err
	}
	return msg
}
