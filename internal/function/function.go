// Package function is the Function lifecycle reconciler (ADR-0020): the one
// controller.Reconciler for KindFunction. It stamps immutable Revisions, schedules +
// provisions workeres via the runtime port to the EFFECTIVE desired replica count
// (honoring the activator's wake Status.Phase, not blindly spec.replicas), validates
// the shape (materialization gate — no route to a broken function), programs the gateway
// with the FULL route table (gateway.ProgramRoutes is replace-all), and writes status.
// It also provides the production activator.Endpoints (the seam ADR-0016 deferred to P-M).
package function

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	"github.com/pyvvo/funcd/internal/pooling"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/scheduler"
	"github.com/pyvvo/funcd/internal/store"
)

const (
	condReady         v1.ConditionType = "Ready"
	condShapeValid    v1.ConditionType = "ShapeValid"
	condPoolFull      v1.ConditionType = "PoolFull"      // ADR-0046: over-cap pooled member held NotReady
	condRevisionReady v1.ConditionType = "RevisionReady" // ADR-0143: whether the latest generation serves
	upstreamPort                       = "8080"
)

// errArtifactUnresolved marks a Function whose artifact ref could not be resolved to a
// digest (ADR-0035) — the reconciler maps it to Phase=Failed + reason ArtifactUnresolved.
var errArtifactUnresolved = errors.New("artifact ref could not be resolved to a digest")

// ShapeValidator validates a Function's artifact conforms to its runtime shape.
type ShapeValidator interface {
	Validate(ctx context.Context, fn *v1.Function) error
}

// InvokeSocketProvider supplies the per-function worker-node local API socket path (ADR-0064): the
// reconciler sets it as FUNCD_INVOKE_SOCKET in the worker env so the shim can dial context.invoke.
// nil ⇒ fn-to-fn links are off (the env var is unset; invoke fails closed in the shim).
type InvokeSocketProvider interface {
	SocketFor(ns v1.NamespaceName, name v1.ObjectName) (string, error)
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

	// CatalogExtensionDir, when non-empty, is injected as DUCKDB_EXTENSION_DIRECTORY into a
	// catalog-consumer function's worker env (a function declaring spec.catalogs) so its handler's
	// `LOAD quack`/`ducklake` resolves the curated DuckDB extensions from this dir. It is the dev
	// analogue of the prod bundle's duckdb-ext (ADR-0089): `funcdctl dev` extracts the embedded
	// catalog engine's extensions once and points consumers at them; prod leaves it empty (the
	// bundle carries duckdb-ext under FUNCD_BUNDLE_DIR instead). Empty ⇒ no injection.
	CatalogExtensionDir string

	// SupervisionPeriod is the requeue of a Ready function (ADR-0142); 0 ⇒ controller.SupervisionPeriod.
	SupervisionPeriod time.Duration

	// Calls counts the calls made to each worker (ADR-0143): the resolver records each hand-out and the drain asks
	// whether a demoted revision's worker is idle. nil ⇒ every worker is idle.
	Calls *activator.CallTracker
	// DrainGrace bounds how long a demoted revision's workers drain; 0 ⇒ 30 s (ADR-0143).
	DrainGrace time.Duration
	// HandOutSettle is how long after a hand-out, or after the switch, a worker may still receive a call; 0 ⇒ 2 s.
	HandOutSettle time.Duration
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
	// Derive returns the deterministic per-(ns, fn) SigV4 keypair (s3gateway.DeriveKeypair,
	// bound to the node master secret). Required when Enabled.
	Derive func(ns, fn string) (access, secret string)
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
	store        store.Store
	runtime      runtime.Runtime
	scheduler    scheduler.Scheduler
	gateway      gateway.Gateway
	validator    ShapeValidator
	logger       *slog.Logger
	materializer Materializer // nil → legacy placeholder mode (no real execution)
	shimCommand  []string
	shimByFamily map[string][]string // runtime-family prefix → shim command (ADR-0049)
	endpointMode EndpointMode
	imageFor     func(string) string
	resolver     ArtifactResolver // nil → no digest resolution (ADR-0035)
	platformsOf  PlatformResolver // nil → no platform gate (ADR-0145)
	httpClient   *http.Client

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

	// catalogExtensionDir, when non-empty, is injected as DUCKDB_EXTENSION_DIRECTORY into a
	// catalog-consumer function's worker env (dev analogue of the prod bundle's duckdb-ext, ADR-0089).
	catalogExtensionDir string

	// pooling (ADR-0046/0050): the pure placement policy + per-family pool-host launch commands + cap.
	// A function pools iff a pool host exists for its runtime family (poolKeyFor); none ⇒ solo.
	assigner          pooling.Assigner
	poolShimCommand   []string
	poolShimsByFamily map[string][]string // runtime-family prefix → pool-host command (ADR-0050)
	poolLimit         int
	// poolSigs is the per-pool-key last-applied manifest signature, the state that makes pool
	// rebuilds idempotent (restart only on a manifest diff). Guarded by poolMu for concurrent
	// reconciles of sibling members of the same pool.
	poolMu   sync.Mutex
	poolSigs map[pooling.PoolKey]string

	// supervisionPeriod is the steady-state requeue and the replacement backoff (ADR-0142).
	supervisionPeriod time.Duration

	// calls, drainGrace and handOutSettle drive the drain of a demoted revision (ADR-0143).
	calls         *activator.CallTracker
	drainGrace    time.Duration
	handOutSettle time.Duration
}

const (
	defaultDrainGrace    = 30 * time.Second
	defaultHandOutSettle = 2 * time.Second
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
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
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
	return &Reconciler{
		store: d.Store, runtime: d.Runtime, scheduler: d.Scheduler,
		gateway: d.Gateway, validator: d.Validator, logger: logger.With("component", "function"),
		materializer: d.Materializer, shimCommand: d.ShimCommand, shimByFamily: d.ShimCommandsByFamily,
		endpointMode: d.EndpointMode, imageFor: d.ImageFor, resolver: d.Resolver, platformsOf: d.Platforms,
		httpClient:          &http.Client{Timeout: 2 * time.Second},
		secrets:             d.Secrets,
		developerFor:        developerFor,
		invokeSockets:       d.InvokeSockets,
		s3Gateway:           d.S3Gateway,
		catalogMaster:       d.CatalogMaster,
		catalogExtensionDir: d.CatalogExtensionDir,
		assigner:            pooling.NewAssigner(),
		poolShimCommand:     d.PoolShimCommand,
		poolShimsByFamily:   d.PoolShimsByFamily,
		poolLimit:           limit,
		poolSigs:            map[pooling.PoolKey]string{},
		supervisionPeriod:   period,
		calls:               d.Calls,
		drainGrace:          drainGrace,
		handOutSettle:       settle,
	}, nil
}

// Reconcile converges one Function to its desired state.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "function.Reconcile"
	obj, err := r.store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			// delete path: stop the function's workeres, then re-program routes without it.
			if derr := r.teardown(ctx, req.Namespace, req.Name); derr != nil {
				return controller.Result{}, derr
			}
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
		if errors.Is(err, errArtifactUnresolved) {
			return r.gateFailed(ctx, fn, gateFailure{reason: "ArtifactUnresolved", message: err.Error(), readyMessage: err.Error(), phase: v1.PhaseFailed}, drainAfter)
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
			return r.gateFailed(ctx, fn, gateFailure{reason: "NoMatchingPlatform", message: msg, readyMessage: msg, phase: v1.PhaseFailed}, drainAfter)
		}
		return controller.Result{}, perr
	}

	// 3. shape gate (materialization): a failure blocks Ready + programs no route.
	if verr := r.validator.Validate(ctx, fn); verr != nil {
		return r.gateFailed(ctx, fn, gateFailure{reason: "ShapeInvalid", message: verr.Error(), phase: v1.PhaseFailed, shapeInvalid: true}, drainAfter)
	}

	// 3b. pooling placement (ADR-0046): decide whether this function is solo (status quo) or
	// joins a shared pool worker by (namespace, runtime, worker-id). A REJECTED (over-cap)
	// member is held NotReady with a PoolFull condition and gets no worker and no route; it
	// comes back on the supervision period, since a slot frees without a write to its object.
	assign, err := r.assign(ctx, fn)
	if err != nil {
		return controller.Result{}, err
	}
	if assign.Rejected {
		return r.gateFailed(ctx, fn, gateFailure{reason: "PoolFull", message: assign.Reason, readyMessage: assign.Reason, phase: v1.PhasePending, poolFull: true, zeroReplicas: true, requeue: r.supervisionPeriod}, drainAfter)
	}
	// Clear a stale PoolFull from a prior reconcile (e.g. the pool shrank and this member was
	// admitted): the condition reflects current placement, never a leftover.
	if _, ok := fn.Status.Conditions.Get(condPoolFull); ok {
		fn.Status.Conditions.Set(v1.Condition{Type: condPoolFull, Status: v1.ConditionFalse, Reason: "Admitted"})
	}

	// 3c. secret injection gate (ADR-0057, F15 last mile): resolve the function's bound secrets
	// into an env map BEFORE provisioning any worker. A PDP-deny / missing Secret / unconfigured
	// resolver / pooled function fails the function CLOSED (not Ready, SecretResolveFailed, no
	// worker) — never a worker started with the secret absent. Mirrors the artifact-unresolved gate.
	secretEnv, serr := r.resolveBindingEnv(ctx, fn, assign.Pooled)
	if serr != nil {
		// Side-attributed reason (ADR-0093 §4): a config-side failure (missing ConfigMap) reports
		// ConfigResolveFailed; every other case (secret PDP-deny / missing / unconfigured, or the
		// pooled gate) reports SecretResolveFailed. One envresolve.ResolveEnv call, two reasons.
		reason := "SecretResolveFailed"
		if errors.Is(serr, envresolve.ErrConfig) {
			reason = "ConfigResolveFailed"
		}
		// No ConfigMap or Secret event reconciles a Function, so a binding applied later is found only by a requeue.
		var requeue time.Duration
		if fault.KindOf(serr) == fault.NotFound {
			requeue = 2 * time.Second
		}
		return r.gateFailed(ctx, fn, gateFailure{reason: reason, message: serr.Error(), readyMessage: serr.Error(), phase: v1.PhaseFailed, zeroReplicas: true, requeue: requeue}, drainAfter)
	}

	// 3c-bis. data-reference gate (ADR-0121): a spec.blob/spec.kv binding naming a not-yet-applied
	// Bucket/prefix or KVStore/table holds the function not-Ready and requeues (accept-and-requeue) —
	// fail-closed, since the PDP denies the bound access until the referent exists, so waiting just avoids
	// booting a worker whose grants can't resolve. This is the existence check the removed blob/kv
	// binding-validity admissions used to make synchronously at apply time.
	refRequeue, refReason, refMsg, rferr := r.resolveDataReferences(ctx, fn)
	if rferr != nil {
		return controller.Result{}, rferr
	}
	if refRequeue {
		return r.gateFailed(ctx, fn, gateFailure{reason: refReason, message: refMsg, readyMessage: refMsg, phase: v1.PhasePending, zeroReplicas: true, requeue: 2 * time.Second}, drainAfter)
	}

	// 3d. catalog consumer-binding gate (ADR-0091, F61): resolve each spec.catalogs binding into the
	// FUNCD_CATALOG_<ALIAS>_URL/_TOKEN env pair BEFORE provisioning any worker. A bound catalog that
	// is not Ready yet (its endpoint is not the PEP proxy yet) requeues fail-closed —
	// the function is held Ready=False/CatalogNotReady and re-reconciled soon, never booted with an
	// empty URL/token (mirrors the ADR-0088 catalog-wait). A hard resolution error fails it closed.
	catalogEnv, requeue, cerr := r.resolveCatalogEnv(ctx, fn)
	if cerr != nil {
		return r.gateFailed(ctx, fn, gateFailure{reason: "CatalogResolveFailed", message: cerr.Error(), readyMessage: cerr.Error(), phase: v1.PhaseFailed, zeroReplicas: true}, drainAfter)
	}
	if requeue {
		const msg = "a bound CatalogService is not Ready yet (no endpoint or token); waiting"
		return r.gateFailed(ctx, fn, gateFailure{reason: "CatalogNotReady", message: msg, readyMessage: msg, phase: v1.PhasePending, zeroReplicas: true, requeue: 2 * time.Second}, drainAfter)
	}

	// 4. converge to the EFFECTIVE desired count (honors the activator's wake Phase). Pooled: the one shared pool
	// worker for the key, driven to the max desired over the key's admitted members (ADR-0046 Decision 6). Solo: per
	// revision, moving the calls from the serving revision to the current one once it is ready (ADR-0143).
	var v verdict
	if assign.Pooled {
		v, err = r.convergePooled(ctx, fn, assign)
	} else {
		v, err = r.convergeSolo(ctx, fn, pinned, secretEnv, catalogEnv)
	}
	if err != nil {
		return controller.Result{}, err
	}
	return r.finish(ctx, fn, v, drainAfter)
}

// gateFailure is a gate that stopped the latest generation before converge (ADR-0143 Decision 4.6).
type gateFailure struct {
	reason, message string        // RevisionReady's reason and message
	readyMessage    string        // the message the gate writes on Ready when nothing serves
	phase           v1.Phase      // the phase the gate writes when nothing serves
	shapeInvalid    bool          // the shape gate: ShapeValid turns False too
	poolFull        bool          // the pool gate: PoolFull is set, ShapeValid stays True
	zeroReplicas    bool          // the gate writes status.replicas 0 when nothing serves
	requeue         time.Duration // the gate's requeue when nothing serves (0 = none)
}

// gateFailed records a gate failure. While a worker of the serving revision runs, it keeps serving: phase, Ready,
// replicas, servingRevision and the route stay, and the current revision's workers stop if it is not the serving one
// (ADR-0143 Decision 4.6). Otherwise the gate's own writes apply, as before ADR-0143.
func (r *Reconciler) gateFailed(ctx context.Context, fn *v1.Function, g gateFailure, drainAfter time.Duration) (controller.Result, error) {
	const op = "function.Reconcile"
	fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: g.reason, Message: g.message})
	switch {
	case g.shapeInvalid:
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionFalse, Reason: "ShapeInvalid", Message: g.message})
	case g.poolFull:
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionTrue})
		fn.Status.Conditions.Set(v1.Condition{Type: condPoolFull, Status: v1.ConditionTrue, Reason: "PoolFull", Message: g.message})
	}
	requeue := g.requeue
	serves, err := r.servingWorkerRuns(ctx, fn)
	if err != nil {
		return controller.Result{}, err
	}
	if serves {
		if c := fn.Status.CurrentRevision; c != fn.Status.ServingRevision {
			if err := r.stopRevision(ctx, fn, v1.ObjectName(c)); err != nil {
				return controller.Result{}, err
			}
		}
		requeue = r.supervisionPeriod
	} else {
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: g.reason, Message: g.readyMessage})
		fn.Status.Phase = g.phase
		if g.zeroReplicas {
			fn.Status.Replicas = 0
		}
	}
	if _, uerr := r.store.Update(ctx, fn); uerr != nil {
		return controller.Result{}, retryOnConflict(uerr, op)
	}
	if perr := r.programAllRoutes(ctx); perr != nil {
		return controller.Result{}, perr
	}
	return controller.Result{RequeueAfter: earliest(requeue, drainAfter)}, nil
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
	switched       bool      // this pass moved the calls to the current revision
	startErr       error     // the first Start error of a current-revision replica (nil if every Start succeeded)
}

// finish writes the pass's status from v and returns its requeue. Ready and the phase describe the serving side;
// ShapeValid and RevisionReady, the current revision (ADR-0143 Decision 5).
func (r *Reconciler) finish(ctx context.Context, fn *v1.Function, v verdict, drainAfter time.Duration) (controller.Result, error) {
	const op = "function.Reconcile"
	fn.Status.Replicas = v.running
	fn.Status.ObservedGeneration = fn.Generation
	// ShapeValid is set once, from its final value: setting it True and then False in one pass would move its
	// LastTransitionTime on every pass, a write that retriggers the pass through the watch (issue #24).
	if v.shapeFailed || (v.switching && v.currentFailed) {
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionFalse, Reason: "ShapeInvalid", Message: "the runtime shim could not load the handler"})
	} else {
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionTrue})
	}
	switch {
	case v.shapeFailed:
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "ShapeInvalid"})
		fn.Status.Phase = v1.PhaseFailed
	case v.ready >= 1:
		fn.Status.Phase = v1.PhaseReady
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue})
	case v.serving:
		// ADR-0142: no replica is ready while a dead one is replaced (the blueprint's Ready → Degraded → Ready).
		fn.Status.Phase = v1.PhaseDegraded
		msg := "a replica exited and is being replaced"
		if v.startErr != nil {
			msg = "a replica exited and its replacement could not start: " + v.startErr.Error()
		}
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "Restarting", Message: msg})
	case v.startErr != nil && v.running == 0:
		// the blueprint's Deploying → Failed on a worker error; requeueFor retries it once per period
		fn.Status.Phase = v1.PhaseFailed
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "StartFailed", Message: v.startErr.Error()})
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
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "ShapeInvalid", Message: "the current revision could not load its handler; the serving revision keeps the calls"})
	case v.switching && v.startErr != nil:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "StartFailed", Message: "a worker of the current revision could not start: " + v.startErr.Error()})
	case v.switching:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "Progressing", Message: "the current revision is booting beside the serving one"})
	case v.shapeFailed:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "ShapeInvalid"})
	case v.startErr != nil && fn.Status.Phase == v1.PhaseFailed:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "StartFailed", Message: v.startErr.Error()})
	case fn.Status.Phase == v1.PhaseDeploying:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionFalse, Reason: "Progressing"})
	default:
		fn.Status.Conditions.Set(v1.Condition{Type: condRevisionReady, Status: v1.ConditionTrue})
	}
	if _, uerr := r.store.Update(ctx, fn); uerr != nil {
		return controller.Result{}, retryOnConflict(uerr, op)
	}
	if perr := r.programAllRoutes(ctx); perr != nil {
		return controller.Result{}, perr
	}
	requeue := r.requeueFor(fn.Status.Phase, v)
	if v.switched {
		requeue = earliest(requeue, r.handOutSettle) // come back for the drain
	}
	return controller.Result{RequeueAfter: earliest(requeue, drainAfter)}, nil
}

// requeueFor is how soon a pass comes back (ADR-0142; ADR-0143 Decision 4.7). While the current revision comes up
// beside the serving one, it polls a booting replica and otherwise checks back after the supervision period.
func (r *Reconciler) requeueFor(phase v1.Phase, v verdict) time.Duration {
	if v.switching {
		if v.booting {
			return readinessPoll
		}
		if !v.retryAt.IsZero() {
			return min(r.supervisionPeriod, max(time.Until(v.retryAt), time.Millisecond))
		}
		return r.supervisionPeriod
	}
	switch phase {
	case v1.PhaseDeploying: // shim booting — re-poll readiness soon; a replica in its backoff — at its deadline
		if v.running == 0 && !v.retryAt.IsZero() {
			return max(time.Until(v.retryAt), time.Millisecond)
		}
		return readinessPoll
	case v1.PhaseReady: // ADR-0142: come back to check the replicas
		return r.supervisionPeriod
	case v1.PhaseDegraded: // ADR-0142: poll a booting replacement, else wait out the backoff
		if v.running > v.ready {
			return readinessPoll
		}
		if !v.retryAt.IsZero() { // at least 1ms: a zero RequeueAfter would mean no requeue
			return max(time.Until(v.retryAt), time.Millisecond)
		}
		return r.supervisionPeriod
	case v1.PhaseFailed: // a worker that could not start is started again after the period; a shape failure is not
		if v.startErr != nil {
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

// bootTimeout bounds how long a solo replica may run without becoming ready before readiness judges it a shape failure
// (ADR-0030 §4b's timeout), as when its handler blocks while it loads. It exceeds the activator's 30 s activation hold,
// so it never cuts short a boot that a cold call still waits for.
const bootTimeout = time.Minute

// servingPhase reports whether a Function in this phase has served since its last deploy (ADR-0142).
func servingPhase(p v1.Phase) bool { return p == v1.PhaseReady || p == v1.PhaseDegraded }

// steadyState reports whether fn is a solo Function at desired state: Ready, its generation processed, its current
// revision serving with nothing draining (ADR-0143), and every replica's instance running. It calls only
// runtime.Status, once per replica (ADR-0142).
func (r *Reconciler) steadyState(ctx context.Context, fn *v1.Function) bool {
	if fn.Status.Phase != v1.PhaseReady || fn.Status.ObservedGeneration != fn.Generation {
		return false
	}
	if _, pooled := r.poolKeyFor(fn); pooled {
		return false
	}
	c := fn.Status.CurrentRevision
	if c == "" || fn.Status.ServingRevision != c || fn.Status.DrainingRevision != "" {
		return false
	}
	if rr, ok := fn.Status.Conditions.Get(condRevisionReady); !ok || rr.Status != v1.ConditionTrue {
		return false
	}
	for i := range r.desiredReplicas(fn) {
		in, err := r.runtime.Status(ctx, runtime.NewInstanceID(fn.Namespace, fn.Name, v1.ObjectName(c), i))
		if err != nil || in.State != runtime.StateRunning {
			return false
		}
	}
	return true
}

// desiredReplicas computes the effective replica count: it honors the activator's wake
// signal (ADR-0016's partitioned Status.Phase) for scaled-to-zero functions, so a cold
// request's wake (Phase=Deploying) actually provisions a worker.
func (r *Reconciler) desiredReplicas(fn *v1.Function) int {
	sc := fn.Spec.Scaling
	if sc.MinReplicas == 0 { // scale-to-zero enabled
		switch fn.Status.Phase {
		case v1.PhaseDeploying, v1.PhaseReady, v1.PhaseDegraded: // woken, serving, or repairing (ADR-0142) — stay up
			// until the activator's idle-reclaim writes Idle. Without keeping Ready up, the
			// reconcile right after a wake would tear the function down before it can serve
			// (ADR-0033: a woken function stays up until idle, not torn down per request).
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
	running, retryAt, startErr, err := r.convergeRevision(ctx, fn, c, pinned, replicaRange(desired), serving, untried, true, secretEnv, catalogEnv)
	if err != nil {
		return verdict{}, err
	}
	ready, shapeFailed := r.readyReplicas(ctx, fn.Namespace, fn.Name, c, running, desired, readinessPath, bootTimeout)
	if serving {
		shapeFailed = false // ADR-0142: in a pass that started serving, a Failed replica is a crash under repair
	}
	if ready >= 1 && s == "" {
		fn.Status.ServingRevision = string(c)
	}
	return verdict{running: running, ready: ready, shapeFailed: shapeFailed, serving: serving, retryAt: retryAt, booting: running > ready, startErr: startErr}, nil
}

// switchSolo brings the current revision c up beside the serving revision s and moves the calls to it once every
// replica of c is ready and no revision drains (ADR-0143 Decisions 4.3–4.5). s keeps its replica indexes, and a dead s
// worker is replaced from s's Revision; once that Revision is deleted, s's running workers serve until the switch.
func (r *Reconciler) switchSolo(ctx context.Context, fn *v1.Function, s, c v1.ObjectName, pinned string, desired int, untried bool, secretEnv, catalogEnv map[string]string) (verdict, error) {
	sIdx, err := r.servingIndexes(ctx, fn, s)
	if err != nil {
		return verdict{}, err
	}
	var runningS int
	var retryS time.Time
	sfn, spinned, err := r.revisionTemplate(ctx, fn, s)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		runningS, err = r.runningReplicas(ctx, fn.Namespace, fn.Name, s, sIdx)
	case err == nil:
		runningS, retryS, _, err = r.convergeRevision(ctx, sfn, s, spinned, sIdx, true, false, false, secretEnv, catalogEnv)
	}
	if err != nil {
		return verdict{}, err
	}
	runningC, retryC, startC, err := r.convergeRevision(ctx, fn, c, pinned, replicaRange(desired), false, untried, true, secretEnv, catalogEnv)
	if err != nil {
		return verdict{}, err
	}
	readyC, failedC := r.readyReplicas(ctx, fn.Namespace, fn.Name, c, runningC, desired, readinessPath, bootTimeout)
	if readyC == desired && fn.Status.DrainingRevision == "" {
		now := time.Now()
		fn.Status.ServingRevision, fn.Status.DrainingRevision, fn.Status.DrainingSince = string(c), string(s), &now
		return verdict{running: runningC, ready: readyC, serving: true, switched: true}, nil
	}
	readyS, _ := r.readyReplicas(ctx, fn.Namespace, fn.Name, s, runningS, maxIndex(sIdx)+1, readinessPath, bootTimeout)
	retryAt := retryS
	if retryAt.IsZero() || (!retryC.IsZero() && retryC.Before(retryAt)) {
		retryAt = retryC
	}
	return verdict{
		running: runningS, ready: readyS, serving: true, retryAt: retryAt,
		booting: runningC > readyC, switching: true, currentFailed: failedC, startErr: startC,
	}, nil
}

// revisionTemplate is fn as revision rev runs it — the runtime, handler and image of rev's snapshot (ADR-0020) with the
// Function's current bindings — and rev's pinned digest.
func (r *Reconciler) revisionTemplate(ctx context.Context, fn *v1.Function, rev v1.ObjectName) (*v1.Function, string, error) {
	obj, err := r.store.Get(ctx, v1.KindRevision.GVK(), fn.Namespace, rev)
	if err != nil {
		return nil, "", fault.Wrapf(err, fault.KindOf(err), "function.revisionTemplate", "get revision %q", rev)
	}
	snap, ok := obj.(*v1.Revision)
	if !ok {
		return nil, "", fault.Internalf("function.revisionTemplate", "object %q is not a Revision", rev)
	}
	tmpl := *fn
	tmpl.Spec.Runtime, tmpl.Spec.Handler, tmpl.Spec.Image = snap.Spec.Runtime, snap.Spec.Handler, snap.Spec.Image
	return &tmpl, snap.Spec.ImageDigest, nil
}

// servingIndexes are the replica indexes the serving revision s keeps during a switch: those the runtime lists, or
// 0 … status.replicas−1 when it lists none, as after a daemon restart (ADR-0143 Decision 4.3).
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
		return replicaRange(fn.Status.Replicas), nil
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

// convergeRevision drives replicas `indexes` of revision rev toward running with ADR-0142's per-replica table: it
// creates a missing replica, starts a Created one, and replaces a terminal one — at once for an untried generation,
// after the backoff for a Stopped replica or a crash in a serving revision — while a Failed replica of a tried
// generation that does not serve is kept, so readiness reports the shape failure. With scaleDown it stops rev's
// replicas outside indexes. tmpl is fn as rev runs it. It returns rev's running count among indexes, the earliest
// time a replica waiting out its backoff may be replaced (zero if none), and the first Start error, which the pass
// writes to the status instead of failing before it (issue #73).
func (r *Reconciler) convergeRevision(ctx context.Context, tmpl *v1.Function, rev v1.ObjectName, pinnedDigest string, indexes []int, serving, untried, scaleDown bool, secretEnv, catalogEnv map[string]string) (int, time.Time, error, error) {
	const op = "function.converge"
	insts, err := r.namedInstances(ctx, tmpl.Namespace, tmpl.Name)
	if err != nil {
		return 0, time.Time{}, nil, err
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
		if scaleDown && in.State != runtime.StateStopped { // scale down by replica index; a Failed one is stopped too
			if serr := r.runtime.Stop(ctx, in.ID); serr != nil {
				return 0, time.Time{}, nil, fault.Wrapf(serr, fault.KindOf(serr), op, "stop worker")
			}
		}
	}

	now := time.Now()
	var retryAt time.Time
	var launch []int // replicas to create (missing) or replace (terminal)
	var replace []runtime.Instance
	var start []runtime.InstanceID
	for _, i := range indexes {
		in, ok := byReplica[i]
		switch {
		case !ok:
			launch = append(launch, i)
		case in.State == runtime.StateCreated:
			start = append(start, in.ID)
		case !in.State.Terminal():
			// running: keep
		case untried:
			replace, launch = append(replace, in), append(launch, i)
		case in.State == runtime.StateStopped || serving:
			if due := in.CreatedAt.Add(r.supervisionPeriod); now.Before(due) {
				if retryAt.IsZero() || due.Before(retryAt) {
					retryAt = due
				}
				continue
			}
			replace, launch = append(replace, in), append(launch, i)
		default:
			// Failed, generation tried, not serving: keep, so readiness marks the shape failure
		}
	}

	// materialize the artifact once (shim mode) before launching any replica. The Revision's
	// pinned digest (ADR-0035) is applied to an in-memory copy — never written back to the
	// Function spec (store.Update persists only Status), so the spec keeps the user's input.
	artifactPath := ""
	if r.materializer != nil && len(launch) > 0 {
		mfn := *tmpl
		mfn.Spec.ImageDigest = pinnedDigest
		artifactPath, err = r.materializer.Materialize(ctx, &mfn)
		if err != nil {
			return 0, time.Time{}, nil, fault.Wrapf(err, fault.KindOf(err), op, "materialize artifact")
		}
	}
	for _, in := range replace {
		if serr := r.runtime.Stop(ctx, in.ID); serr != nil {
			return 0, time.Time{}, nil, fault.Wrapf(serr, fault.KindOf(serr), op, "stop exited worker")
		}
	}
	platforms, err := r.artifactPlatforms(ctx, tmpl.Spec.Image, pinnedDigest)
	if err != nil {
		return 0, time.Time{}, nil, err
	}
	for _, i := range launch {
		if _, perr := r.scheduler.Schedule(ctx, scheduler.Request{Namespace: tmpl.Namespace, Name: tmpl.Name, Replica: i, Platforms: platforms}); perr != nil {
			return 0, time.Time{}, nil, fault.Wrapf(perr, fault.KindOf(perr), op, "schedule")
		}
		spec := r.workerSpec(tmpl, i, artifactPath, secretEnv, catalogEnv)
		spec.Revision = rev
		inst, cerr := r.runtime.Create(ctx, spec)
		if cerr != nil {
			return 0, time.Time{}, nil, fault.Wrapf(cerr, fault.KindOf(cerr), op, "create worker")
		}
		start = append(start, inst.ID)
	}
	var startErr error
	for _, id := range start {
		if serr := r.runtime.Start(ctx, id); serr != nil {
			r.logger.Warn("could not start worker", "instance", id, "err", serr)
			if startErr == nil {
				startErr = serr
			}
		}
	}

	running, err := r.runningReplicas(ctx, tmpl.Namespace, tmpl.Name, rev, indexes)
	if err != nil {
		return 0, time.Time{}, nil, err
	}
	return running, retryAt, startErr, nil
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
// drainingRevision when none of its workers remain, and returns how soon the pass must come back (0 if nothing drains).
func (r *Reconciler) drain(ctx context.Context, fn *v1.Function) (time.Duration, error) {
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return 0, err
	}
	s, c, d := fn.Status.ServingRevision, fn.Status.CurrentRevision, fn.Status.DrainingRevision
	var elapsed time.Duration
	if fn.Status.DrainingSince != nil {
		elapsed = time.Since(*fn.Status.DrainingSince)
	}
	draining := 0
	for _, in := range insts {
		rev := string(in.Revision)
		if rev == s || rev == c {
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
	if draining == 0 {
		if d != "" {
			fn.Status.DrainingRevision, fn.Status.DrainingSince = "", nil
		}
		return 0, nil
	}
	wait := r.drainGrace - elapsed
	if elapsed < r.handOutSettle {
		wait = r.handOutSettle - elapsed
	}
	return min(time.Second, max(wait, time.Millisecond)), nil
}

// idle reports whether no call to worker in is in flight and the resolver has not handed it out recently (ADR-0143).
func (r *Reconciler) idle(fn *v1.Function, in runtime.Instance) bool {
	return r.calls == nil || r.calls.Idle(instanceURL(fn.Namespace, fn.Name, in), r.handOutSettle)
}

// retire stops a worker and forgets it (ADR-0143).
func (r *Reconciler) retire(ctx context.Context, in runtime.Instance) error {
	const op = "function.retire"
	if err := r.runtime.Stop(ctx, in.ID); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "stop worker")
	}
	if err := r.runtime.Remove(ctx, in.ID); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "remove worker")
	}
	return nil
}

// stopAll stops every worker of a Function scaled to zero and retires those of revisions other than c; c's stopped
// replicas stay listed for ADR-0142's wake backoff (ADR-0143 Decision 5).
func (r *Reconciler) stopAll(ctx context.Context, fn *v1.Function, c v1.ObjectName) error {
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if in.Revision != c {
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

// servingWorkerRuns reports whether a worker of the serving revision is running (ADR-0143 Decision 4.6).
func (r *Reconciler) servingWorkerRuns(ctx context.Context, fn *v1.Function) (bool, error) {
	s := v1.ObjectName(fn.Status.ServingRevision)
	if s == "" {
		return false, nil
	}
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return false, err
	}
	for _, in := range insts {
		if in.Revision == s && in.State == runtime.StateRunning {
			return true, nil
		}
	}
	return false, nil
}

// namedInstances returns the runtime instances whose Name matches `name` in ns. For a solo
// function this is its replicas; for a pool worker `name` is the synthetic pool name
// (poolInstanceName) — the shared worker has its own instance identity, distinct from any
// member's, so a member's solo lookups never collide with the pool (ADR-0046).
func (r *Reconciler) namedInstances(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) ([]runtime.Instance, error) {
	all, err := r.runtime.List(ctx, ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "function.instances", "list workeres")
	}
	out := make([]runtime.Instance, 0, len(all))
	for _, in := range all {
		if in.Name == name {
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
	if r.invokeSockets != nil {
		r.invokeSockets.Remove(ns, name) // the local API socket dies with the Function (ADR-0064)
	}
	return nil
}

// ensureRevision stamps an immutable Revision for the Function's current generation if one
// does not already exist (the store bumps generation only on spec change; the activator's
// status-only Phase writes do not, so a wake never stamps a spurious Revision).
// It returns the Revision's authoritative artifact digest (ADR-0035): on the create path the
// digest is the explicit spec digest, else the resolved-and-pinned one; on the early-return
// path it is the EXISTING Revision's digest — a stamped Revision is NEVER re-resolved, so a
// moved tag cannot drift it (the immutability guarantee).
func (r *Reconciler) ensureRevision(ctx context.Context, fn *v1.Function) (string, error) {
	const op = "function.ensureRevision"
	revName := revisionName(fn)
	existing, err := r.store.Get(ctx, v1.KindRevision.GVK(), fn.Namespace, v1.ObjectName(revName))
	if err == nil {
		fn.Status.CurrentRevision = revName
		if rev, ok := existing.(*v1.Revision); ok {
			return rev.Spec.ImageDigest, nil // never re-resolved
		}
		return fn.Spec.ImageDigest, nil
	}
	if fault.KindOf(err) != fault.NotFound {
		return "", fault.Wrapf(err, fault.KindOf(err), op, "get revision")
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
	rev.Spec = v1.RevisionSpec{
		Function: v1.ObjectRef{Kind: v1.KindFunction, Namespace: fn.Namespace, Name: fn.Name},
		Number:   fn.Generation,
		Runtime:  fn.Spec.Runtime,
		Handler:  fn.Spec.Handler,
		Image:    fn.Spec.Image, ImageDigest: pinned,
	}
	if _, cerr := r.store.Create(ctx, rev); cerr != nil {
		if fault.KindOf(cerr) != fault.Conflict {
			return "", fault.Wrapf(cerr, fault.KindOf(cerr), op, "create revision")
		}
		// concurrent create — adopt the stored Revision's pinned digest (idempotent).
		if cur, gerr := r.store.Get(ctx, v1.KindRevision.GVK(), fn.Namespace, v1.ObjectName(revName)); gerr == nil {
			if rev2, ok := cur.(*v1.Revision); ok {
				pinned = rev2.Spec.ImageDigest
			}
		}
	}
	fn.Status.CurrentRevision = revName
	return pinned, nil
}

// revisionName is the Revision a Function's generation stamps (ADR-0020): <name>-<generation>.
func revisionName(fn *v1.Function) string { return fmt.Sprintf("%s-%d", fn.Name, fn.Generation) }

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
		upstream := r.upstreamForFn(ctx, fn)
		if upstream == "" {
			// A Ready function whose worker isn't resolvable right now (its instance briefly
			// unlisted mid-transition). Omit it rather than passing an empty upstream to the
			// gateway, which rejects the WHOLE batch (one stale function would drop every route).
			// It is reprogrammed on its own next reconcile, once its upstream resolves.
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

// upstreamForFn returns a function's upstream URL: the pool worker's address for a pooled
// function (resolved by its pool key, not by Instance.Name, since the worker is shared —
// ADR-0046 Decision 5), or a running worker of its serving revision (ADR-0143). "" if none runs.
func (r *Reconciler) upstreamForFn(ctx context.Context, fn *v1.Function) string {
	// poolKeyFor already gates on a pool host existing for the runtime family (ADR-0050), so route
	// to the shared pool worker whenever it returns a key — NOT only for the node poolShimCommand
	// (a python* pool has only a python host configured, so a node-host check would mis-route it solo).
	if key, ok := r.poolKeyFor(fn); ok {
		return r.upstreamOf(ctx, fn.Namespace, poolInstanceName(key), "")
	}
	return r.upstreamOf(ctx, fn.Namespace, fn.Name, servingRevision(fn))
}

// servingRevision is the Revision whose workers receive a solo Function's calls: servingRevision, or the current
// revision while nothing serves yet (ADR-0143).
func servingRevision(fn *v1.Function) v1.ObjectName {
	if fn.Status.ServingRevision != "" {
		return v1.ObjectName(fn.Status.ServingRevision)
	}
	return v1.ObjectName(fn.Status.CurrentRevision)
}

// upstreamOf returns the upstream URL of a running worker of revision rev named `name` in ns, or "".
func (r *Reconciler) upstreamOf(ctx context.Context, ns v1.NamespaceName, name, rev v1.ObjectName) string {
	insts, err := r.namedInstances(ctx, ns, name)
	if err != nil {
		return ""
	}
	for _, in := range insts {
		if in.Revision == rev && in.State == runtime.StateRunning {
			return instanceURL(ns, name, in)
		}
	}
	return ""
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

// readyReplicas reports how many replicas of revision rev are serving and whether the shim reported a shape failure.
// In legacy mode (no Materializer) ready == running (ADR-0020, unchanged). In shim mode (ADR-0030) it polls each
// running replica's health endpoint at path and treats a failed instance (the shim exited because it could not load the
// handler), or a running one that has not become ready within bootLimit of its creation, as a shape failure; a zero
// bootLimit sets no limit. Only replicas below `below` count (ADR-0142): a replica being scaled away is not judged.
func (r *Reconciler) readyReplicas(ctx context.Context, ns v1.NamespaceName, name, rev v1.ObjectName, running, below int, path string, bootLimit time.Duration) (ready int, shapeFailed bool) {
	if r.materializer == nil {
		return running, false
	}
	insts, err := r.namedInstances(ctx, ns, name)
	if err != nil {
		return 0, false
	}
	for _, in := range insts {
		if in.Revision != rev || in.Replica >= below {
			continue
		}
		switch in.State {
		case runtime.StateFailed:
			shapeFailed = true
		case runtime.StateRunning:
			switch {
			case in.Port > 0 && r.probeReady(ctx, in.IP, in.Port, path):
				ready++
			case bootLimit > 0 && time.Since(in.CreatedAt) >= bootLimit:
				shapeFailed = true
			}
		}
	}
	return ready, shapeFailed
}

// The health endpoints a shim and a pool host serve (ADR-0030 §4b, ADR-0044).
const (
	readinessPath = "/health/readiness"
	livenessPath  = "/health/liveness"
)

// probeBodyMax bounds the body drained before close: a body read to EOF returns the connection to
// the keep-alive pool, so repeated probes do not churn ephemeral ports (ADR-0041).
const probeBodyMax = 4 << 10

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
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, probeBodyMax))
		_ = resp.Body.Close()
	}()
	return resp.StatusCode == http.StatusOK
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
	if err != nil {
		return "", false, nil
	}
	f, ok := obj.(*v1.Function)
	if !ok {
		return "", false, nil
	}
	up := e.r.upstreamForFn(ctx, f)
	if _, pooled := e.r.poolKeyFor(f); pooled && up != "" {
		up += "/function/" + string(f.Name)
	}
	ready := f.Status.Phase == v1.PhaseReady && up != ""
	if ready && e.r.calls != nil {
		e.r.calls.HandedOut(up) // the drain waits out a call that resolved this upstream (ADR-0143)
	}
	return up, ready, nil
}

// workerSpec builds the runtime spec for one replica. In shim mode (a Materializer is
// configured, ADR-0030) it launches the runtime shim with the materialized artifact +
// handler in the env; otherwise it runs the legacy long-lived placeholder (ADR-0020).
// shimFor selects the shim launch prefix for a function's runtime (ADR-0049): the longest
// registered family prefix that matches fn.Spec.Runtime (e.g. "python" → the python shim), else
// the default ShimCommand (node). One daemon can thus run several curated languages; the rest of
// the worker spec (env, portfile, readiness) is identical regardless of which shim is chosen.
func (r *Reconciler) shimFor(rt v1.RuntimeName) []string {
	best, cmd := "", []string(nil)
	for family, c := range r.shimByFamily {
		if len(family) > len(best) && strings.HasPrefix(string(rt), family) {
			best, cmd = family, c
		}
	}
	if cmd != nil {
		return cmd
	}
	return r.shimCommand
}

// workerSpec builds one replica's runtime spec. It is pure: secretEnv is the already-resolved
// secret env map (ADR-0057), merged into Env with reserved-FUNCD_-key precedence; nil ⇒ none.
// addInvokeSocket sets FUNCD_INVOKE_SOCKET so the worker's shim can dial the per-sandbox worker-node
// local API — context.invoke (ADR-0064) AND context.kv (ADR-0069). EVERY function gets the socket (KV is
// available to all; the link-as-grant check for invoke stays at RESOLVE time, so a linkless function's
// invoke still fails closed). No-op when the local API is off entirely (r.invokeSockets nil).
func (r *Reconciler) addInvokeSocket(env map[string]string, fn *v1.Function) {
	if r.invokeSockets == nil {
		return
	}
	sock, err := r.invokeSockets.SocketFor(fn.Namespace, fn.Name)
	if err != nil {
		r.logger.Warn("could not provision invoke socket", "function", fn.Name, "err", err)
		return
	}
	env["FUNCD_INVOKE_SOCKET"] = sock
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
	access, secret := r.s3Gateway.Derive(string(fn.Namespace), string(fn.Name))
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

func (r *Reconciler) workerSpec(fn *v1.Function, replica int, artifactPath string, secretEnv, catalogEnv map[string]string) runtime.WorkerSpec {
	if r.materializer != nil && r.endpointMode == EndpointNetnsFixedPort {
		// Container mode (ADR-0032): the shim is the curated image's entrypoint (Command
		// empty), the artifact is bind-mounted read-only, and it binds a fixed netns port.
		env := map[string]string{
			"FUNCD_ARTIFACT": filepath.Join(containerArtifactDir, filepath.Base(artifactPath)),
			"FUNCD_HANDLER":  fn.Spec.Handler,
			"FUNCD_PORT":     strconv.Itoa(containerShimPort),
		}
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
		if r.invokeSockets != nil {
			if sock, err := r.invokeSockets.SocketFor(fn.Namespace, fn.Name); err == nil {
				env["FUNCD_INVOKE_SOCKET"] = containerInvokeSocket
				mounts = append(mounts, runtime.Mount{Source: sock, Target: containerInvokeSocket})
			} else {
				r.logger.Warn("could not provision invoke socket", "function", fn.Name, "err", err)
			}
		}
		return runtime.WorkerSpec{
			Namespace: fn.Namespace,
			Name:      fn.Name,
			Replica:   replica,
			Image:     r.imageFor(string(fn.Spec.Runtime)),
			Mounts:    mounts,
			Env:       env,
		}
	}
	if r.materializer != nil {
		// Process mode (ADR-0030): ShimCommand launches the shim; loopback + portfile.
		env := map[string]string{
			"FUNCD_ARTIFACT": artifactPath,
			"FUNCD_HANDLER":  fn.Spec.Handler,
		}
		addBundleEnv(env, fn.Spec.Runtime, filepath.Dir(artifactPath)) // FUNCD_BUNDLE_DIR (+ PYTHONPATH, python family), ADR-0089
		// FUNCD_CONTRACT_PATH (ADR-0123): the delivered schema sits in the same host dir the shim
		// reads directly in process mode, so host root == worker root.
		addContractEnv(env, filepath.Dir(artifactPath), filepath.Dir(artifactPath))
		r.addInvokeSocket(env, fn)        // FUNCD_INVOKE_SOCKET for context.invoke (ADR-0064); reachable on the host
		r.addS3Env(env, fn)               // AWS_* S3 keypair + endpoint for a spec.blob function (ADR-0085)
		r.addCatalogEnv(env, catalogEnv)  // FUNCD_CATALOG_<ALIAS>_URL/_TOKEN written DIRECTLY (ADR-0091) — never via mergeSecretEnv
		r.addCatalogExtensionDir(env, fn) // DUCKDB_EXTENSION_DIRECTORY for a catalog consumer (dev; prod uses the bundle's duckdb-ext)
		r.mergeSecretEnv(env, secretEnv)
		return runtime.WorkerSpec{
			Namespace: fn.Namespace,
			Name:      fn.Name,
			Replica:   replica,
			Image:     string(fn.Spec.Runtime),
			Command:   r.shimFor(fn.Spec.Runtime), // node by default; python* → the python shim (ADR-0049)
			Env:       env,
		}
	}
	return runtime.WorkerSpec{
		Namespace: fn.Namespace,
		Name:      fn.Name,
		Replica:   replica,
		Image:     string(fn.Spec.Runtime),
		// Legacy placeholder (no Materializer): a long-lived process stands in for the
		// worker; real execution is the shim path (ADR-0030).
		Command: []string{"sleep", "86400"},
	}
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
