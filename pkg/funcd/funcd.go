// Package funcd is the platform-as-a-library facade (ADR-0014): the composition
// root that wires every port into a *Platform and owns its start/stop lifecycle.
// Run serves the control-plane API and runs the controller (ADR-0028); cmd/funcd is
// a thin shell over it; e2e tests embed it with the InMemory() preset — no daemon,
// no root, no network beyond the ephemeral control-plane listener.
package funcd

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/activator"
	"github.com/green-0-rabbit/funcd/internal/activator/storescaler"
	"github.com/green-0-rabbit/funcd/internal/artifact"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/bus"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/controlplane"
	"github.com/green-0-rabbit/funcd/internal/controlplane/admission"
	"github.com/green-0-rabbit/funcd/internal/controlplane/middleware"
	"github.com/green-0-rabbit/funcd/internal/dataplane"
	"github.com/green-0-rabbit/funcd/internal/eventing"
	"github.com/green-0-rabbit/funcd/internal/function"
	"github.com/green-0-rabbit/funcd/internal/gateway"
	"github.com/green-0-rabbit/funcd/internal/observability"
	"github.com/green-0-rabbit/funcd/internal/runtime"
	"github.com/green-0-rabbit/funcd/internal/scheduler/singlenode"
	"github.com/green-0-rabbit/funcd/internal/secrets"
	"github.com/green-0-rabbit/funcd/internal/services"
	blobsvc "github.com/green-0-rabbit/funcd/internal/services/blob"
	kvsvc "github.com/green-0-rabbit/funcd/internal/services/kv"
	"github.com/green-0-rabbit/funcd/internal/store"
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
)

// config holds the injected world — validated by validate() before New returns.
type config struct {
	store     store.Store
	blob      blob.Bucket
	bus       bus.Bus
	runtime   runtime.Runtime
	gateway   gateway.Gateway
	logger    *slog.Logger
	telemetry *observability.Telemetry

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
	cfg    *config
	logger *slog.Logger

	controller *controller.Controller
	eventing   *eventing.Source
	activator  *activator.Activator
	httpServer *http.Server
	listener   net.Listener
	addr       string

	dataPlaneServer   *http.Server // function-invocation listener (ADR-0033)
	dataPlaneListener net.Listener
	dataPlaneAddr     string

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

	p := &Platform{cfg: cfg, logger: cfg.logger}
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
	fnReconciler, err := function.NewReconciler(function.Deps{
		Store:                c.store,
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
	p.controller = ctrl

	handler, err := controlplane.NewServer(controlplane.Deps{
		Store:       c.store,
		Authorizer:  c.authorizer,
		Credentials: c.credentials,
		Logger:      p.logger,
		Admissions: []admission.Admission{ // ADR-0064 fn-to-fn link rules on the write path
			admission.NewLinkValidityAdmission(storeReader{c.store}),
			admission.NewLinkDeletionProtectionAdmission(storeReader{c.store}),
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
	p.dataPlaneServer = &http.Server{Handler: dpHandler, ReadHeaderTimeout: 10 * time.Second}
	dln, err := net.Listen("tcp", c.dataPlaneAddr)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "bind data-plane listener on %s", c.dataPlaneAddr)
	}
	p.dataPlaneListener = dln
	p.dataPlaneAddr = dln.Addr().String()
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
		errs := []error{
			p.cfg.bus.Close(),
			p.cfg.gateway.Close(),
			p.cfg.runtime.Close(),
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

// storeReader adapts store.Store to admission.StoreReader for the ADR-0064 link admissions
// (the admission package stays a near-leaf and does not import store).
type storeReader struct{ s store.Store }

func (r storeReader) List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	res, err := r.s.List(ctx, gvk, store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}
