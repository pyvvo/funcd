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
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/activator"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/gateway"
	"github.com/green-0-rabbit/funcd/internal/pooling"
	"github.com/green-0-rabbit/funcd/internal/runtime"
	"github.com/green-0-rabbit/funcd/internal/scheduler"
	"github.com/green-0-rabbit/funcd/internal/store"
)

const (
	condReady      v1.ConditionType = "Ready"
	condShapeValid v1.ConditionType = "ShapeValid"
	condPoolFull   v1.ConditionType = "PoolFull" // ADR-0046: over-cap pooled member held NotReady
	upstreamPort                    = "8080"
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
	httpClient   *http.Client

	// secrets (ADR-0057) resolves a function's bound Secret names → env vars; nil → injection
	// disabled (a function declaring spec.secrets fails closed). developerFor supplies the PDP
	// identity for the read (defaulted to the namespace-scoped developer when secrets are wired).
	secrets      SecretResolver
	developerFor func(ns v1.NamespaceName) auth.Identity

	// invokeSockets provisions the per-function worker-node local API (ADR-0064); nil ⇒ links off.
	invokeSockets InvokeSocketProvider

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
}

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
	return &Reconciler{
		store: d.Store, runtime: d.Runtime, scheduler: d.Scheduler,
		gateway: d.Gateway, validator: d.Validator, logger: logger.With("component", "function"),
		materializer: d.Materializer, shimCommand: d.ShimCommand, shimByFamily: d.ShimCommandsByFamily,
		endpointMode: d.EndpointMode, imageFor: d.ImageFor, resolver: d.Resolver,
		httpClient:        &http.Client{Timeout: 2 * time.Second},
		secrets:           d.Secrets,
		developerFor:      developerFor,
		invokeSockets:     d.InvokeSockets,
		assigner:          pooling.NewAssigner(),
		poolShimCommand:   d.PoolShimCommand,
		poolShimsByFamily: d.PoolShimsByFamily,
		poolLimit:         limit,
		poolSigs:          map[pooling.PoolKey]string{},
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
			return controller.Result{}, r.programAllRoutes(ctx)
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get function")
	}
	fn, ok := obj.(*v1.Function)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not a Function", req.Namespace, req.Name)
	}

	// 2. stamp an immutable Revision (ADR-0020) + resolve-and-pin the artifact digest if the
	// spec left it unset (ADR-0035). pinned is the Revision's authoritative digest, used to
	// materialize; an unresolvable ref → Failed (never a silent/wrong deploy).
	pinned, err := r.ensureRevision(ctx, fn)
	if err != nil {
		if errors.Is(err, errArtifactUnresolved) {
			fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "ArtifactUnresolved", Message: err.Error()})
			fn.Status.Phase = v1.PhaseFailed
			if _, uerr := r.store.Update(ctx, fn); uerr != nil {
				return controller.Result{}, retryOnConflict(uerr, op)
			}
			return controller.Result{}, r.programAllRoutes(ctx)
		}
		return controller.Result{}, err
	}

	// 3. shape gate (materialization): a failure blocks Ready + programs no route.
	if verr := r.validator.Validate(ctx, fn); verr != nil {
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionFalse, Reason: "ShapeInvalid", Message: verr.Error()})
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "ShapeInvalid"})
		fn.Status.Phase = v1.PhaseFailed
		if _, uerr := r.store.Update(ctx, fn); uerr != nil {
			return controller.Result{}, retryOnConflict(uerr, op)
		}
		return controller.Result{}, r.programAllRoutes(ctx)
	}

	// 3b. pooling placement (ADR-0046): decide whether this function is solo (status quo) or
	// joins a shared pool worker by (namespace, runtime, worker-id). A REJECTED (over-cap)
	// member is held NotReady with a PoolFull condition and gets no worker and no route.
	assign, err := r.assign(ctx, fn)
	if err != nil {
		return controller.Result{}, err
	}
	if assign.Rejected {
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionTrue})
		fn.Status.Conditions.Set(v1.Condition{Type: condPoolFull, Status: v1.ConditionTrue, Reason: "PoolFull", Message: assign.Reason})
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "PoolFull", Message: assign.Reason})
		fn.Status.Phase = v1.PhasePending
		fn.Status.Replicas = 0
		if _, uerr := r.store.Update(ctx, fn); uerr != nil {
			return controller.Result{}, retryOnConflict(uerr, op)
		}
		return controller.Result{}, r.programAllRoutes(ctx)
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
	secretEnv, serr := r.resolveSecretEnv(ctx, fn, assign.Pooled)
	if serr != nil {
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "SecretResolveFailed", Message: serr.Error()})
		fn.Status.Phase = v1.PhaseFailed
		fn.Status.Replicas = 0
		if _, uerr := r.store.Update(ctx, fn); uerr != nil {
			return controller.Result{}, retryOnConflict(uerr, op)
		}
		return controller.Result{}, r.programAllRoutes(ctx)
	}

	// 4. converge workeres to the EFFECTIVE desired count (honors the activator's wake Phase).
	// Solo: per-function workers (secretEnv merged into each, ADR-0057). Pooled: the one shared
	// pool worker for the key, driven to the max desired over the key's admitted members (ADR-0046
	// Decision 6) — pooled functions can't declare secrets (gated above), so secretEnv is nil there.
	running, err := r.convergeFor(ctx, fn, assign, pinned, secretEnv)
	if err != nil {
		return controller.Result{}, err
	}

	// 5. readiness: in shim mode (ADR-0030) a replica is Ready only when its shim reports
	// GET /health/readiness; a shim that exits (could not load the handler) is a shape
	// failure. In legacy mode ready == running (ADR-0020 behavior, unchanged). A pooled
	// member's readiness is its pool worker's readiness (ADR-0046 Decision 5).
	ready, shapeFailed := r.readyFor(ctx, fn, assign, running)
	fn.Status.Replicas = running
	switch {
	case shapeFailed:
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionFalse, Reason: "ShapeInvalid", Message: "the runtime shim could not load the handler"})
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "ShapeInvalid"})
		fn.Status.Phase = v1.PhaseFailed
	case ready >= 1:
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionTrue})
		fn.Status.Phase = v1.PhaseReady
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue})
	case running >= 1:
		// replicas started but the shim is not serving yet — keep polling.
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionTrue})
		fn.Status.Phase = v1.PhaseDeploying
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "ShimNotReady"})
	default:
		fn.Status.Conditions.Set(v1.Condition{Type: condShapeValid, Status: v1.ConditionTrue})
		fn.Status.Phase = v1.PhaseIdle
		fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "NoReplicas"})
	}
	if _, uerr := r.store.Update(ctx, fn); uerr != nil {
		return controller.Result{}, retryOnConflict(uerr, op)
	}
	if perr := r.programAllRoutes(ctx); perr != nil {
		return controller.Result{}, perr
	}
	if fn.Status.Phase == v1.PhaseDeploying { // shim booting — re-poll readiness soon
		return controller.Result{RequeueAfter: 200 * time.Millisecond}, nil
	}
	return controller.Result{}, nil
}

// desiredReplicas computes the effective replica count: it honors the activator's wake
// signal (ADR-0016's partitioned Status.Phase) for scaled-to-zero functions, so a cold
// request's wake (Phase=Deploying) actually provisions a worker.
func (r *Reconciler) desiredReplicas(fn *v1.Function) int {
	sc := fn.Spec.Scaling
	if sc.MinReplicas == 0 { // scale-to-zero enabled
		switch fn.Status.Phase {
		case v1.PhaseDeploying, v1.PhaseReady: // woken (Deploying) or serving (Ready) — stay up
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

// converge creates/starts or stops workeres so the function's running count matches
// desired; it returns the resulting running count.
func (r *Reconciler) converge(ctx context.Context, fn *v1.Function, desired int, pinnedDigest string, secretEnv map[string]string) (int, error) {
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return 0, err
	}
	// stop extras
	for i := desired; i < len(insts); i++ {
		if serr := r.runtime.Stop(ctx, insts[i].ID); serr != nil {
			return 0, fault.Wrapf(serr, fault.KindOf(serr), "function.converge", "stop worker")
		}
	}
	// materialize the artifact once (shim mode) before launching any replica. The Revision's
	// pinned digest (ADR-0035) is applied to an in-memory copy — never written back to the
	// Function spec (store.Update persists only Status), so the spec keeps the user's input.
	artifactPath := ""
	if r.materializer != nil && desired > len(insts) {
		mfn := *fn
		mfn.Spec.Artifact.Digest = pinnedDigest
		artifactPath, err = r.materializer.Materialize(ctx, &mfn)
		if err != nil {
			return 0, fault.Wrapf(err, fault.KindOf(err), "function.converge", "materialize artifact")
		}
	}
	// create+start missing
	for i := len(insts); i < desired; i++ {
		if _, perr := r.scheduler.Schedule(ctx, scheduler.Request{Namespace: fn.Namespace, Name: fn.Name, Replica: i}); perr != nil {
			return 0, fault.Wrapf(perr, fault.KindOf(perr), "function.converge", "schedule")
		}
		inst, cerr := r.runtime.Create(ctx, r.workerSpec(fn, i, artifactPath, secretEnv))
		if cerr != nil {
			return 0, fault.Wrapf(cerr, fault.KindOf(cerr), "function.converge", "create worker")
		}
		if serr := r.runtime.Start(ctx, inst.ID); serr != nil {
			return 0, fault.Wrapf(serr, fault.KindOf(serr), "function.converge", "start worker")
		}
	}
	// recount running
	insts, err = r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return 0, err
	}
	running := 0
	for _, in := range insts {
		if in.State == runtime.StateRunning {
			running++
		}
	}
	return running, nil
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

// teardown stops every worker of a (deleted) function.
func (r *Reconciler) teardown(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	insts, err := r.namedInstances(ctx, ns, name)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if serr := r.runtime.Stop(ctx, in.ID); serr != nil {
			return fault.Wrapf(serr, fault.KindOf(serr), "function.teardown", "stop worker")
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
	revName := fmt.Sprintf("%s-%d", fn.Name, fn.Generation)
	existing, err := r.store.Get(ctx, v1.KindRevision.GVK(), fn.Namespace, v1.ObjectName(revName))
	if err == nil {
		fn.Status.CurrentRevision = revName
		if rev, ok := existing.(*v1.Revision); ok {
			return rev.Spec.Artifact.Digest, nil // never re-resolved
		}
		return fn.Spec.Artifact.Digest, nil
	}
	if fault.KindOf(err) != fault.NotFound {
		return "", fault.Wrapf(err, fault.KindOf(err), op, "get revision")
	}
	// create path: pin the digest — explicit if set, else resolve the ref (ADR-0035).
	pinned := fn.Spec.Artifact.Digest
	if pinned == "" {
		d, rerr := r.pinDigest(ctx, fn.Spec.Artifact.URI)
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
		Artifact: v1.ArtifactRef{URI: fn.Spec.Artifact.URI, Digest: pinned},
	}
	if _, cerr := r.store.Create(ctx, rev); cerr != nil {
		if fault.KindOf(cerr) != fault.Conflict {
			return "", fault.Wrapf(cerr, fault.KindOf(cerr), op, "create revision")
		}
		// concurrent create — adopt the stored Revision's pinned digest (idempotent).
		if cur, gerr := r.store.Get(ctx, v1.KindRevision.GVK(), fn.Namespace, v1.ObjectName(revName)); gerr == nil {
			if rev2, ok := cur.(*v1.Revision); ok {
				pinned = rev2.Spec.Artifact.Digest
			}
		}
	}
	fn.Status.CurrentRevision = revName
	return pinned, nil
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
// ADR-0046 Decision 5), or its own solo worker's address. "" if no running worker.
func (r *Reconciler) upstreamForFn(ctx context.Context, fn *v1.Function) string {
	// poolKeyFor already gates on a pool host existing for the runtime family (ADR-0050), so route
	// to the shared pool worker whenever it returns a key — NOT only for the node poolShimCommand
	// (a python* pool has only a python host configured, so a node-host check would mis-route it solo).
	if key, ok := r.poolKeyFor(fn); ok {
		return r.upstreamOf(ctx, fn.Namespace, poolInstanceName(key))
	}
	return r.upstreamOf(ctx, fn.Namespace, fn.Name)
}

// upstreamOf returns the upstream URL of the running worker named `name` in ns, or "".
// In shim mode (ADR-0030) the worker reports its resolved IP:Port (the shim's listening
// address); legacy mode falls back to a synthetic per-function host + the default port.
func (r *Reconciler) upstreamOf(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) string {
	insts, err := r.namedInstances(ctx, ns, name)
	if err != nil {
		return ""
	}
	for _, in := range insts {
		if in.State != runtime.StateRunning {
			continue
		}
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
	return ""
}

// readyReplicas reports how many replicas are serving and whether the shim reported a shape
// failure. In legacy mode (no Materializer) ready == running (ADR-0020, unchanged). In shim
// mode (ADR-0030) it polls each running replica's /health/readiness and treats a failed
// instance (the shim exited because it could not load the handler) as a shape failure.
func (r *Reconciler) readyReplicas(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, running int) (ready int, shapeFailed bool) {
	if r.materializer == nil {
		return running, false
	}
	insts, err := r.namedInstances(ctx, ns, name)
	if err != nil {
		return 0, false
	}
	for _, in := range insts {
		switch in.State {
		case runtime.StateFailed:
			shapeFailed = true
		case runtime.StateRunning:
			if in.Port > 0 && r.probeReady(ctx, in.IP, in.Port) {
				ready++
			}
		}
	}
	return ready, shapeFailed
}

// probeReady issues GET /health/readiness against a shim and reports a 200 (ADR-0030 §4b).
func (r *Reconciler) probeReady(ctx context.Context, ip string, port int) bool {
	host := ip
	if host == "" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s:%d/health/readiness", host, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
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
	// function the upstream is its pool worker's address, resolved by the pool key (ADR-0046).
	obj, err := e.r.store.Get(ctx, v1.KindFunction.GVK(), fn.Namespace, fn.Name)
	if err != nil {
		return "", false, nil
	}
	f, ok := obj.(*v1.Function)
	if !ok {
		return "", false, nil
	}
	up := e.r.upstreamForFn(ctx, f)
	ready := f.Status.Phase == v1.PhaseReady && up != ""
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
// addInvokeSocket sets FUNCD_INVOKE_SOCKET so the worker's shim can dial context.invoke (ADR-0064),
// provisioning the per-function worker-node local API on demand. Only a CALLER (a function that
// declares spec.links) gets a socket — a linkless function that invokes still fails closed in the
// shim. No-op when links are off entirely (r.invokeSockets nil).
func (r *Reconciler) addInvokeSocket(env map[string]string, fn *v1.Function) {
	if r.invokeSockets == nil || len(fn.Spec.Links) == 0 {
		return
	}
	sock, err := r.invokeSockets.SocketFor(fn.Namespace, fn.Name)
	if err != nil {
		r.logger.Warn("could not provision invoke socket", "function", fn.Name, "err", err)
		return
	}
	env["FUNCD_INVOKE_SOCKET"] = sock
}

func (r *Reconciler) workerSpec(fn *v1.Function, replica int, artifactPath string, secretEnv map[string]string) runtime.WorkerSpec {
	if r.materializer != nil && r.endpointMode == EndpointNetnsFixedPort {
		// Container mode (ADR-0032): the shim is the curated image's entrypoint (Command
		// empty), the artifact is bind-mounted read-only, and it binds a fixed netns port.
		env := map[string]string{
			"FUNCD_ARTIFACT": filepath.Join(containerArtifactDir, filepath.Base(artifactPath)),
			"FUNCD_HANDLER":  fn.Spec.Handler,
			"FUNCD_PORT":     strconv.Itoa(containerShimPort),
		}
		r.mergeSecretEnv(env, secretEnv)
		mounts := []runtime.Mount{{
			Source: filepath.Dir(artifactPath), Target: containerArtifactDir, ReadOnly: true,
		}}
		// Bind-mount the per-function local API socket into the sandbox so the shim can dial
		// context.invoke at the in-container path (ADR-0064); only a caller (declares links) gets one.
		if r.invokeSockets != nil && len(fn.Spec.Links) > 0 {
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
		r.addInvokeSocket(env, fn) // FUNCD_INVOKE_SOCKET for context.invoke (ADR-0064); reachable on the host
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
	case fn.Spec.Artifact.URI == "":
		return fault.Invalidf("function.shape", "function %q: spec.artifact.uri is required", fn.Name)
	}
	return nil
}
