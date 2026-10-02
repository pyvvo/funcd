package provider

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/gateway"
	containerrt "github.com/pyvvo/funcd/internal/runtime"
)

// Runtime is the add-on-provider runtime. It REUSES the existing runtime.Runtime container port
// (internal/runtime) + the ingress gateway — it is NOT the Function controller and imposes no
// Function shape gate. Converge is re-entrant + idempotent: the per-provider reconciler calls it
// on EVERY reconcile; it (re)creates a missing/failed engine, probes readiness, programs the
// OPTIONAL ingress route once Ready, and returns status. This re-convergence IS the supervision
// (restart-on-crash) — no separate watchdog. Teardown stops the engine (the driver owns netns
// cleanup) and removes any programmed route.
type Runtime interface {
	Converge(ctx context.Context, spec ProviderSpec) (ProviderStatus, error)
	Teardown(ctx context.Context, ref ProviderRef) error
}

// Deps wires the runtime over the EXISTING ports — no new execution/routing model, no new driver.
type Deps struct {
	// Runtime is the existing container port (internal/runtime, ADR-0032/0054): Create/Start/
	// Status/Stop/List. Required.
	Runtime containerrt.Runtime
	// Gateway is the ingress reverse-proxy (ADR-0013) — ProgramRoutes is replace-all; used only
	// when a Route is set. Optional: a nil gateway means an internal-only deployment (a provider
	// that declares a Route then is reported Ready but unexposed, with a logged warning).
	Gateway gateway.Gateway
	Logger  *slog.Logger
	// HTTPClient probes ReadinessProbe; nil ⇒ a default short-timeout client.
	HTTPClient *http.Client
}

// engineRuntime is the in-process driver of the provider Runtime port.
type engineRuntime struct {
	rt         containerrt.Runtime
	gateway    gateway.Gateway // may be nil (internal-only deployment)
	logger     *slog.Logger
	httpClient *http.Client
}

// NewRuntime builds the add-on-provider runtime. Deps.Runtime is required; Gateway is optional
// (internal-only deployment); HTTPClient defaults to a short-timeout client.
func NewRuntime(d Deps) (Runtime, error) {
	const op = "provider.NewRuntime"
	if d.Runtime == nil {
		return nil, fault.Invalidf(op, "runtime is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := d.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	return &engineRuntime{
		rt:         d.Runtime,
		gateway:    d.Gateway,
		logger:     logger.With("component", "provider.runtime"),
		httpClient: client,
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

	existing, err := r.namedInstances(ctx, spec.Ref)
	if err != nil {
		return ProviderStatus{}, err
	}
	byReplica := make(map[int]containerrt.Instance, len(existing))
	for _, in := range existing {
		byReplica[in.Replica] = in
	}

	running := 0
	var ready containerrt.Instance // the first running replica we can probe/route to
	haveReady := false
	for i := 0; i < spec.Replicas; i++ {
		inst, ok := byReplica[i]
		switch {
		// Recreate a missing / terminal (failed/stopped) instance (supervision = re-convergence).
		case !ok || inst.State.Terminal():
			if ok {
				// best-effort stop the terminal instance before recreating it (idempotent).
				if serr := r.rt.Stop(ctx, inst.ID); serr != nil {
					return ProviderStatus{}, fault.Wrapf(serr, fault.KindOf(serr), op, "stop terminal engine replica %d", i)
				}
			}
			created, cerr := r.rt.Create(ctx, r.workerSpec(spec, i))
			if cerr != nil {
				return ProviderStatus{}, fault.Wrapf(cerr, fault.KindOf(cerr), op, "create engine replica %d", i)
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

	status := ProviderStatus{Running: running}
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

// Teardown stops every engine replica of the provider and removes any ingress route it programmed
// (ADR-0087). It is idempotent: a missing engine / route is fine.
func (r *engineRuntime) Teardown(ctx context.Context, ref ProviderRef) error {
	const op = "provider.Teardown"
	insts, err := r.namedInstances(ctx, ref)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if serr := r.rt.Stop(ctx, in.ID); serr != nil {
			return fault.Wrapf(serr, fault.KindOf(serr), op, "stop engine %s", in.ID)
		}
	}
	if r.gateway != nil {
		if rerr := r.removeRoute(ctx, ref); rerr != nil {
			return rerr
		}
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
		Replica:   replica,
		Image:     spec.Image,
		Command:   nil, // the curated image entrypoint is the engine — no artifact, no shape gate
		Env:       spec.Env,
		Limits:    limitsFrom(spec.Resources),
	}
}

// namedInstances returns the runtime instances belonging to the provider (matched by Name in its
// namespace), the same lookup pattern the Function reconciler uses (internal/function).
func (r *engineRuntime) namedInstances(ctx context.Context, ref ProviderRef) ([]containerrt.Instance, error) {
	all, err := r.rt.List(ctx, ref.Namespace)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "provider.instances", "list engine workers")
	}
	out := make([]containerrt.Instance, 0, len(all))
	for _, in := range all {
		if in.Name == ref.Name {
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
// runs exactly one provider (the F48 CatalogService, which today programs no Route at all — it is
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

// limitsFrom maps the recorded ResourceSpec onto runtime.Limits when the values are parseable
// (forward-compat, ADR-0086 sizing). Unparseable / empty values map to 0 (unlimited) — the runtime
// driver applies what it can; this never fails a deploy on a cosmetic sizing string.
func limitsFrom(rs ResourceSpec) containerrt.Limits {
	var l containerrt.Limits
	if cpu, err := strconv.ParseFloat(strings.TrimSpace(rs.CPU), 64); err == nil && cpu > 0 {
		l.CPUs = cpu
	}
	if b, ok := parseMemoryBytes(rs.Memory); ok {
		l.MemoryBytes = b
	}
	return l
}

// parseMemoryBytes parses a memory hint like "4Gi"/"512Mi"/"1024" into bytes (binary suffixes).
// Returns (0, false) on an empty or unparseable value.
func parseMemoryBytes(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	mult := int64(1)
	for suffix, m := range map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40} {
		if strings.HasSuffix(s, suffix) {
			mult = m
			s = strings.TrimSuffix(s, suffix)
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n * mult, true
}
