package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	containerrt "github.com/pyvvo/funcd/internal/runtime"
)

// Runtime is the add-on-provider runtime. It REUSES the existing runtime.Runtime container port
// (internal/runtime) + the ingress gateway — it is NOT the Function controller and imposes no
// Function shape gate. Converge is re-entrant + idempotent: the per-provider reconciler calls it
// on EVERY reconcile; it (re)creates a missing/failed engine, probes readiness, programs the
// OPTIONAL ingress route once Ready, and returns status. This re-convergence IS the supervision
// (restart-on-crash) — no separate watchdog. Teardown stops and removes every engine replica (the
// driver owns netns cleanup) and removes any programmed route.
type Runtime interface {
	Converge(ctx context.Context, spec ProviderSpec) (ProviderStatus, error)
	Teardown(ctx context.Context, ref ProviderRef) error
}

// Deps wires the runtime over the EXISTING ports — no new execution/routing model, no new driver.
type Deps struct {
	// Runtime is the existing container port (internal/runtime, ADR-0032/0054): Create/Start/
	// Status/Stop/Remove/List. Required.
	Runtime containerrt.Runtime
	// OwnerKind is the kind whose engines this runtime runs (ADR-0152): every engine it creates carries it, and
	// Converge and Teardown act only on workers of this kind. Required.
	OwnerKind v1.Kind
	// Gateway is the ingress reverse-proxy (ADR-0013) — ProgramRoutes is replace-all; used only
	// when a Route is set. Optional: a nil gateway means an internal-only deployment (a provider
	// that declares a Route then is reported Ready but unexposed, with a logged warning).
	Gateway gateway.Gateway
	Logger  *slog.Logger
	// HTTPClient probes ReadinessProbe; nil ⇒ a node client bounded by ProbeTimeout.
	HTTPClient *http.Client
	// ProbeTimeout bounds one readiness probe (lake.engineProbeTimeout, ADR-0163); 0 ⇒ defaultProbeTimeout. Ignored
	// when HTTPClient is set.
	ProbeTimeout time.Duration
	// StorageRoot is the root of the engines' durable directories (ADR-0231); "" ⇒ no durable storage, so a spec with
	// Storage fails Invalid.
	StorageRoot string
	// ScratchRoot holds the engines' scratch images and ScratchMountRoot their mount points (ADR-0294); "" ⇒ no
	// scratch, so a spec's Scratch is ignored, as on the process runtime, which isolates nothing.
	ScratchRoot      string
	ScratchMountRoot string
	// Floor is asked for a scratch's size before it is allocated (ADR-0296); nil ⇒ no floor.
	Floor Floor
	// Now is the clock ProviderSpec.MaxAge is measured with (ADR-0270); nil ⇒ time.Now.
	Now func() time.Time
}

// Floor is the free-space floor of the data filesystem (quota.Floor, ADR-0296).
type Floor interface {
	Check(ctx context.Context, bytes int64) error
}

const defaultProbeTimeout = 2 * time.Second

// engineRuntime is the in-process driver of the provider Runtime port.
type engineRuntime struct {
	rt          containerrt.Runtime
	ownerKind   v1.Kind
	gateway     gateway.Gateway // may be nil (internal-only deployment)
	logger      *slog.Logger
	httpClient  *http.Client
	storageRoot string
	scratch     scratchFiles
	floor       Floor
	unenforced  containerrt.LimitWarner // logs once per engine its runtime ignores its limits (ADR-0291)
	// specs is the fingerprint of the spec each provider's engine was created from: a running engine of another spec
	// is replaced, since a provider re-created or changed between two reconciler passes reaches Converge with its new
	// spec while the old engine runs (#1195). The map starts empty, so a running engine this process did not create is
	// adopted; after a daemon restart there is none, since the boot sweep removed the earlier run's (ADR-0167, ADR-0186).
	specsMu sync.Mutex
	specs   map[ProviderRef]string
	now     func() time.Time
}

// NewRuntime builds the add-on-provider runtime. Deps.Runtime is required; Gateway is optional
// (internal-only deployment); HTTPClient defaults to a short-timeout client.
func NewRuntime(d Deps) (Runtime, error) {
	const op = "provider.NewRuntime"
	if d.Runtime == nil {
		return nil, fault.Invalidf(op, "runtime is required")
	}
	if d.OwnerKind == "" {
		return nil, fault.Invalidf(op, "owner kind is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := d.HTTPClient
	if client == nil {
		timeout := d.ProbeTimeout
		if timeout <= 0 {
			timeout = defaultProbeTimeout
		}
		client = httpx.NodeClient(timeout)
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &engineRuntime{
		rt:          d.Runtime,
		ownerKind:   d.OwnerKind,
		gateway:     d.Gateway,
		logger:      logger.With("component", "provider.runtime"),
		httpClient:  client,
		storageRoot: d.StorageRoot,
		scratch:     scratchFiles{root: d.ScratchRoot, mountRoot: d.ScratchMountRoot, fs: loopScratch{}},
		floor:       d.Floor,
		specs:       map[ProviderRef]string{},
		now:         now,
	}, nil
}

// Converge idempotently brings the provider's engine to its desired state: for each pinned replica
// it adopts an existing instance or Creates+Starts a fresh one from the curated image (WorkerSpec
// .Command empty — the image entrypoint IS the engine, no artifact, no shape gate), reads the
// netns address, probes the engine's HTTP readiness, and — only once Ready AND a Route is declared
// — programs the gateway. A crashed/terminal/NotFound instance is recreated and a Created one is
// started on this pass (supervision = re-convergence). It returns the observed ProviderStatus.
func (r *engineRuntime) Converge(ctx context.Context, spec ProviderSpec) (ProviderStatus, error) {
	const op = "provider.Converge"
	if spec.Replicas <= 0 {
		return ProviderStatus{}, fault.Invalidf(op, "spec.Replicas must be >= 1 (a provider is pinned, no scale-to-zero); got %d", spec.Replicas)
	}
	if spec.Image == "" {
		return ProviderStatus{}, fault.Invalidf(op, "spec.Image is required")
	}
	if spec.Port <= 0 {
		return ProviderStatus{}, fault.Invalidf(op, "spec.Port must be > 0; got %d", spec.Port)
	}
	if spec.Storage != nil {
		if err := r.prepareStorage(spec); err != nil {
			return ProviderStatus{}, err
		}
	}
	if spec.Scratch != nil && spec.Replicas != 1 {
		return ProviderStatus{}, fault.Invalidf(op, "an engine with a scratch runs exactly one replica; got %d", spec.Replicas)
	}

	existing, err := r.namedInstances(ctx, spec.Ref)
	if err != nil {
		return ProviderStatus{}, err
	}
	fp := r.workerSpec(spec, 0)
	if spec.IssueEnv != nil {
		// which keys are issued counts, their values do not: a new key window replaces no engine, while gaining or
		// losing a pair does (ADR-0270 Decision 5)
		fp.Env = maps.Clone(fp.Env)
		if fp.Env == nil {
			fp.Env = map[string]string{}
		}
		for k := range spec.IssueEnv() {
			fp.Env[k] = ""
		}
	}
	want := specFingerprint(fp)
	r.specsMu.Lock()
	had, known := r.specs[spec.Ref]
	r.specsMu.Unlock()
	if known && had != want {
		for _, in := range existing {
			if rerr := r.retire(ctx, in.ID); rerr != nil {
				return ProviderStatus{}, fault.Wrapf(rerr, fault.KindOf(rerr), op, "retire the engine of an earlier spec")
			}
		}
		existing = nil
	}
	r.specsMu.Lock()
	r.specs[spec.Ref] = want
	r.specsMu.Unlock()
	byReplica := make(map[int]containerrt.Instance, len(existing))
	for _, in := range existing {
		byReplica[in.Replica] = in
	}

	running := 0
	var ready containerrt.Instance // the first running replica we can probe/route to
	haveReady := false
	for i := 0; i < spec.Replicas; i++ {
		inst, ok := byReplica[i]
		// An engine older than MaxAge carries a key pair about to expire (ADR-0270): replace it.
		aged := ok && spec.MaxAge > 0 && !inst.State.Terminal() && r.now().Sub(inst.CreatedAt) >= spec.MaxAge
		switch {
		// Recreate a missing / terminal (failed/stopped) or aged instance (supervision = re-convergence).
		case !ok || inst.State.Terminal() || aged:
			if aged {
				r.logger.Info("replacing an engine for a fresh key pair", "namespace", spec.Ref.Namespace, "name", spec.Ref.Name,
					"instance", inst.ID, "age", r.now().Sub(inst.CreatedAt))
			}
			if ok {
				if rerr := r.retire(ctx, inst.ID); rerr != nil {
					return ProviderStatus{}, fault.Wrapf(rerr, fault.KindOf(rerr), op, "retire terminal engine replica %d", i)
				}
			}
			ws := r.workerSpec(spec, i)
			if spec.IssueEnv != nil {
				ws.Env = maps.Clone(ws.Env)
				if ws.Env == nil {
					ws.Env = map[string]string{}
				}
				maps.Copy(ws.Env, spec.IssueEnv())
			}
			r.unenforced.Warn(ctx, r.logger, r.rt, string(spec.Ref.Namespace)+"/"+string(spec.Ref.Name), ws.Limits)
			if serr := r.makeScratch(ctx, spec); serr != nil {
				return ProviderStatus{}, serr
			}
			created, cerr := r.rt.Create(ctx, ws)
			if cerr != nil {
				return ProviderStatus{}, errors.Join(fault.Wrapf(cerr, fault.KindOf(cerr), op, "create engine replica %d", i),
					r.removeScratch(spec.Ref))
			}
			inst = created
			fallthrough
		// A failed Start leaves the instance Created (ADR-0142): start it.
		case inst.State == containerrt.StateCreated:
			if serr := r.rt.Start(ctx, inst.ID); serr != nil {
				return ProviderStatus{}, fault.Wrapf(serr, fault.KindOf(serr), op, "start engine replica %d", i)
			}
		}
		if inst.State == containerrt.StateRunning {
			running++
			if !haveReady {
				ready, haveReady = inst, true
			}
		}
	}

	status := ProviderStatus{Running: running, Report: ready.Report}
	if !haveReady {
		status.Reason = "EngineNotReady"
		return status, nil
	}

	// The engine binds the FIXED spec.Port in its netns (EndpointNetnsFixedPort, ADR-0032) — the
	// portfile-resolved Instance.Port is 0 for a curated image-entrypoint engine, so address the
	// configured port, not Instance.Port.
	addr := ready.IP + ":" + strconv.Itoa(spec.Port)
	status.Address = addr
	if ready.IP == "" || !r.probeReady(ctx, ready.IP, spec.Port, spec.Readiness) {
		status.Reason = "EngineNotReady"
		return status, nil
	}
	status.Ready = true

	// Ingress is OPTIONAL (ADR-0087): program a route only when one is declared AND the engine is
	// Ready. nil Route ⇒ internal-only: no route, the netns Address is the daemon's handle.
	if spec.Route != nil {
		if perr := r.programRoute(ctx, spec, ready); perr != nil {
			return ProviderStatus{}, perr
		}
		status.Endpoint = spec.Route.PathPrefix
	}
	return status, nil
}

// Teardown stops and removes every engine replica of the provider and removes any ingress route it programmed
// (ADR-0087). It is idempotent: a missing engine / route is fine.
func (r *engineRuntime) Teardown(ctx context.Context, ref ProviderRef) error {
	const op = "provider.Teardown"
	insts, err := r.namedInstances(ctx, ref)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if rerr := r.retire(ctx, in.ID); rerr != nil {
			return fault.Wrapf(rerr, fault.KindOf(rerr), op, "retire engine %s", in.ID)
		}
	}
	if serr := r.removeScratch(ref); serr != nil {
		return serr
	}
	r.specsMu.Lock()
	delete(r.specs, ref)
	r.specsMu.Unlock()
	if r.gateway != nil {
		if rerr := r.removeRoute(ctx, ref); rerr != nil {
			return rerr
		}
	}
	return nil
}

// specFingerprint is a digest of what a worker is created from, but its replica index.
func specFingerprint(w containerrt.WorkerSpec) string {
	w.Replica = 0
	b, _ := json.Marshal(w)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// retire stops an engine worker and removes it with its per-instance files (ADR-0143).
func (r *engineRuntime) retire(ctx context.Context, id containerrt.InstanceID) error {
	const op = "provider.retire"
	if err := r.rt.Stop(ctx, id); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "stop engine")
	}
	if err := r.rt.Remove(ctx, id); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "remove engine")
	}
	return nil
}

// workerSpec maps a ProviderSpec replica onto the existing runtime.WorkerSpec (no new abstraction).
// Command is EMPTY: the curated image's entrypoint IS the engine — there is no shim command, no
// artifact, and no Function shape gate (ADR-0087). The caller-assembled Env (ADR-0085 keypair,
// engine token, config) is passed verbatim.
func (r *engineRuntime) workerSpec(spec ProviderSpec, replica int) containerrt.WorkerSpec {
	return containerrt.WorkerSpec{
		Namespace: spec.Ref.Namespace,
		Name:      spec.Ref.Name,
		OwnerKind: r.ownerKind,
		Replica:   replica,
		Image:     spec.Image,
		Command:   nil, // the curated image entrypoint is the engine — no artifact, no shape gate
		Env:       spec.Env,
		Limits:    containerrt.LimitsFrom(spec.Resources.CPU, spec.Resources.Memory),
		Mounts:    append(r.storageMounts(spec), r.scratch.mounts(spec)...),

		EgressOnly: spec.EgressOnly,
	}
}

// namedInstances returns the runtime instances belonging to the provider (matched by its owner kind and Name in its
// namespace), the same lookup pattern the Function reconciler uses (internal/function); a Function's worker of the
// same name is never one of them (ADR-0152).
func (r *engineRuntime) namedInstances(ctx context.Context, ref ProviderRef) ([]containerrt.Instance, error) {
	all, err := r.rt.List(ctx, ref.Namespace)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "provider.instances", "list engine workers")
	}
	out := make([]containerrt.Instance, 0, len(all))
	for _, in := range all {
		if in.OwnerKind == r.ownerKind && in.Name == ref.Name {
			out = append(out, in)
		}
	}
	return out, nil
}

// programRoute programs the gateway with the provider's ingress route, upstream
// http://<netnsIP>:<port> (ADR-0087/0013).
//
// REPLACE-ALL CAVEAT (known limitation, ADR-0087): gateway.ProgramRoutes is replace-all — it
// replaces the WHOLE live route table. A per-provider Converge that programs only ITS own route
// would clobber every other provider's (and every function's) route. The clean fix is that the
// CALLER aggregates the full provider-route set and programs it once; for ADR-0087 the live lane
// runs exactly one provider (the F48 Lake, which today programs no Route at all — it is
// internal-only), so this is not yet exercised. TODO(ADR-0087 follow-up): an aggregation seam (or a
// non-replace-all gateway op) so multiple ingress-exposed providers + functions coexist. This is
// NOT solved here — it is documented and deferred.
func (r *engineRuntime) programRoute(ctx context.Context, spec ProviderSpec, inst containerrt.Instance) error {
	const op = "provider.programRoute"
	if r.gateway == nil {
		// Internal-only deployment (no gateway wired): a declared Route can't be honored. Log and
		// continue — the engine is still Ready and reachable via its netns Address.
		r.logger.Warn("provider declares an ingress Route but no gateway is wired; serving internal-only",
			"namespace", spec.Ref.Namespace, "name", spec.Ref.Name)
		return nil
	}
	upstream := "http://" + inst.IP + ":" + strconv.Itoa(spec.Port)
	route := gateway.Route{
		ID:         gateway.RouteID(spec.Route.ID),
		Host:       spec.Route.Host,
		PathPrefix: spec.Route.PathPrefix,
		Upstream:   upstream,
	}
	if perr := r.gateway.ProgramRoutes(ctx, []gateway.Route{route}); perr != nil {
		return fault.Wrapf(perr, fault.KindOf(perr), op, "program ingress route %s", spec.Route.ID)
	}
	return nil
}

// removeRoute re-programs the gateway WITHOUT the provider's route (ADR-0087 Teardown). It reads
// the live table, drops any route the provider owns (id "<ns>/<name>"), and re-programs the
// remainder. (Same replace-all caveat as programRoute: with one provider this is the full set.)
func (r *engineRuntime) removeRoute(ctx context.Context, ref ProviderRef) error {
	const op = "provider.removeRoute"
	live, err := r.gateway.Routes(ctx)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "read live routes")
	}
	ownID := string(ref.Namespace) + "/" + string(ref.Name)
	kept := make([]gateway.Route, 0, len(live))
	for _, rt := range live {
		if string(rt.ID) == ownID {
			continue
		}
		kept = append(kept, rt)
	}
	if len(kept) == len(live) {
		return nil // nothing of ours was programmed
	}
	if perr := r.gateway.ProgramRoutes(ctx, kept); perr != nil {
		return fault.Wrapf(perr, fault.KindOf(perr), op, "re-program routes without %s", ownID)
	}
	return nil
}
