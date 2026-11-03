package funcd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/funclog/compact"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/lifecycle"
	"github.com/pyvvo/funcd/internal/network/egress"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/quota"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
	"github.com/pyvvo/funcd/internal/volume"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// executor is the platform's Executor provider (ADR-0305): the runtime that runs every workload, and what it gives a
// workload around it: the egress fence, the egress gateway and the DNS forwarder, the volumes, the local API sockets
// that serve the binding providers' facades (ADR-0309 Decision 4), and the capture of its logs. The runtime and the
// fence are the config's, so its hooks release them even when New failed before build.
type executor struct {
	cfg *config

	egressWorkers   *egress.MemoryWorkerIndex // src-IP → Ref, filled by the containerd runtime (ADR-0117 §5); nil unless egress is on
	egressForwarder egress.Forwarder
	egressGateway   egress.Gateway
	volumes         volume.Manager         // the Functions' volumes (ADR-0262); nil ⇒ off
	sockets         *local.Manager         // one local API listener per worker (ADR-0064)
	socketTmpDir    string                 // the socket dir build created when WithInvokeSocketDir set none
	logSink         *funclog.BlobSink      // structured function-log capture (ADR-0081); nil if unwired
	traceSink       *funclog.BlobTraceSink // per-invocation and run-root spans (ADR-0101, ADR-0103); nil if off
	logs            funclog.Sink           // logSink, tee'd to funcdctl dev's observer; nil without funclog
	logRoutes       logRoutes              // the live capture Routes feeding the sinks, drained by Stop
	compactor       *compact.Compactor     // funclog compaction (ADR-0083); nil if unwired

	services workerServices // the egress gateway and the DNS forwarder, on a context Run cannot end (#1232)

	closeOnce sync.Once
	closeErr  error
}

var (
	_ lifecycle.Starter       = (*executor)(nil)
	_ lifecycle.Runner        = (*executor)(nil)
	_ lifecycle.BeforeStopper = (*executor)(nil)
	_ lifecycle.Stopper       = (*executor)(nil)
	_ describer               = (*executor)(nil)
)

// execDeps are the parts the Executor is built over: the Platform's, and the facades of the binding providers and of
// secrets, which a worker's local API serves.
type execDeps struct {
	authz   auth.Authorizer
	floor   *quota.Floor
	invoker local.Invoker           // a worker's fn-to-fn calls, through the data plane once it is built
	checks  local.DependencyChecker // a worker's binding check (ADR-0215 Decision 3)
	kv      local.KV
	blob    local.Blob
	sql     local.SQL
	secrets local.Secrets
}

func (e *executor) Name() string { return "executor" }

func (e *executor) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{
		{Name: "egress", Kind: provider.Builtin, Summary: "egress gateway (default-deny + audit)"},
		{Name: "log-ingest", Kind: provider.Builtin, Summary: "function-telemetry side channel + funclog read path (ADR-0081/0084)"},
	}
}

// build builds the egress gateway and the DNS forwarder when egress is on (ADR-0117), the volumes (ADR-0262), the
// local API sockets over the binding providers' facades, and the log sinks and compactor. It runs after the binding
// providers are built, since a worker's local API serves their facades.
func (e *executor) build(d execDeps) error {
	const op = "funcd.New"
	c := e.cfg
	if c.egressGatewayEnabled {
		e.buildEgress(d.authz)
	}
	if err := e.openVolumes(); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "open the Function volumes")
	}
	dir := c.invokeSocketDir // config-derived (<dataDir>/invoke); empty ⇒ a temp dir (InMemory/tests)
	if dir == "" {
		tmp, err := os.MkdirTemp("", "funcd-invoke")
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "create invoke socket dir")
		}
		dir, e.socketTmpDir = tmp, tmp
	}
	if err := local.CheckDir(dir); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "invoke socket dir")
	}
	e.sockets = local.NewManager(dir, c.store, d.invoker, d.authz, d.checks, c.logger, local.WithFacets(local.KVFacet(d.kv),
		local.BlobFacet(d.blob), local.SQLFacet(d.sql), local.SecretFacet(d.secrets)))
	return e.buildLogs(d.floor)
}

// buildEgress builds the egress gateway, the sole egress PEP, which authorizes each worker connection through the PDP
// (egress::connect), and the DNS forwarder, whose recorded (worker, domain) → IP makes a domain rule trustworthy.
func (e *executor) buildEgress(authz auth.Authorizer) {
	c := e.cfg
	e.egressWorkers = egress.NewMemoryWorkerIndex()
	fwd := egress.NewForwarder(
		netip.AddrPortFrom(netip.AddrFrom4([4]byte{0, 0, 0, 0}), c.dnsForwarderPort),
		c.dnsUpstream,
		e.egressWorkers,                // src-IP → namespace (ADR-0117 §5)
		storeWildcardPatterns{c.store}, // namespace → EgressPolicy wildcards
	)
	e.egressForwarder = fwd
	e.egressGateway = egress.New(true, egress.Deps{
		GatewayPort: c.egressGatewayPort,
		Workers:     e.egressWorkers,
		DNS:         fwd,
		Authz:       authz,
		Audit:       egressAuditSink{logger: c.logger.With("component", "egress.audit")},
		Logger:      c.logger.With("component", "egress.gateway"),
	})
}

// buildLogs builds, over the blob substrate, the trace sink the workflow engine shares (ADR-0103), the logs sink when the
// runtime captures a worker's channel (ADR-0081), and the compactor (ADR-0083: window 1h, interval 5m, retention 30d
// unless WithLogCompaction set them).
func (e *executor) buildLogs(floor *quota.Floor) error {
	const op = "funcd.New"
	c := e.cfg
	if c.blob == nil {
		return nil
	}
	if !c.funclogDisabled && !c.funclogTracesDisabled {
		ts, err := funclog.NewBlobTraceSink(funclog.Deps{
			Bucket: c.blob, Clock: clock.System(), Logger: c.logger,
			SegmentMaxAge: c.funclogMaxAge, SegmentMaxBytes: c.funclogMaxBytes,
		})
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "build funclog trace sink")
		}
		e.traceSink = ts
	}
	if _, ok := c.runtime.(runtime.LogCapturer); ok && !c.funclogDisabled {
		deps := funclog.Deps{
			Bucket: c.blob, Clock: clock.System(), Logger: c.logger,
			SegmentMaxAge: c.funclogMaxAge, SegmentMaxBytes: c.funclogMaxBytes,
		}
		if floor != nil {
			deps.Floor = floor
		}
		sink, err := funclog.NewBlobSink(deps)
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "build funclog sink")
		}
		e.logSink, e.logs = sink, sink
		if c.logObserver != nil { // funcdctl dev streams the logs live
			e.logs = &teeLogSink{inner: sink, observe: c.logObserver}
		}
	}
	if !c.logCompactDisabled {
		retention := compact.DefaultRetention
		if c.logCompactConfigured {
			retention = c.logCompactRetention
		}
		comp, err := compact.New(compact.Deps{
			Bucket: c.blob, Clock: clock.System(), Logger: c.logger,
			Window: c.logCompactWindow, Interval: c.logCompactInterval, Retention: retention,
		})
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "build funclog compactor")
		}
		e.compactor = comp
	}
	return nil
}

// traces is the trace sink as its consumers take it: a nil interface, never a typed nil, when tracing is off.
func (e *executor) traces() funclog.TraceSink {
	if e.traceSink == nil {
		return nil
	}
	return e.traceSink
}

// captureLogs installs the runtime's capture hooks once the Function reconciler, which names a pool's members, exists.
// A worker's channel goes to the logs sink, its span lines to the trace sink (ADR-0101); its raw stdout and stderr go to
// the logs sink too, or only its tail stays without funclog (ADR-0168 Path A); an engine's lines go to the daemon's log
// (ADR-0276). A pool worker's records go to the member each names, its raw lines to each member it runs.
func (e *executor) captureLogs(fn *function.Reconciler) {
	c := e.cfg
	logger := c.logger
	if lc, ok := c.runtime.(runtime.LogCapturer); ok && e.logSink != nil {
		sinks := funclog.Sinks{Logs: e.logs, Traces: e.traces()}
		lc.SetLogCapture(func(spec runtime.WorkerSpec, r io.ReadCloser) {
			res := funclog.Resource{Namespace: string(spec.Namespace), Function: string(spec.Name), Replica: strconv.Itoa(spec.Replica)}
			if !strings.HasPrefix(string(spec.Name), "__pool__") {
				e.logRoutes.start(r, func() { _ = funclog.Route(context.Background(), r, sinks, res, logger) })
				return
			}
			// The member set is taken once per process, at its start, so a member removed later keeps its drained records.
			_, isMember, _ := fn.PoolMembers(spec.Namespace, spec.Name)
			e.logRoutes.start(r, func() { _ = funclog.RoutePool(context.Background(), r, sinks, res, isMember, logger) })
		})
	}
	if oc, ok := c.runtime.(runtime.OutputCapturer); ok {
		oc.SetOutputCapture(func(spec runtime.WorkerSpec, out *workerpipe.Output) {
			if spec.OwnerKind != v1.KindFunction {
				e.logEngineOutput(spec, out)
				return
			}
			if e.logs == nil {
				return
			}
			res := funclog.Resource{Namespace: string(spec.Namespace), Function: string(spec.Name), Replica: strconv.Itoa(spec.Replica)}
			dst := e.logs
			if strings.HasPrefix(string(spec.Name), "__pool__") {
				members, _, _ := fn.PoolMembers(spec.Namespace, spec.Name)
				dst = newMemberSink(e.logs, res, members)
			}
			for _, src := range []struct {
				stream workerpipe.Stream
				source funclog.Source
			}{{workerpipe.Stdout, funclog.SourceStdout}, {workerpipe.Stderr, funclog.SourceStderr}} {
				r := out.Reader(src.stream)
				e.logRoutes.start(r, func() {
					_ = funclog.Pump(context.Background(), funclog.NewRawReader(r, src.source), dst, res, logger)
				})
			}
		})
	}
}

// Start applies the egress fence before any worker can run (ADR-0115), then serves the egress gateway and the DNS
// forwarder (ADR-0117).
func (e *executor) Start(ctx context.Context) error {
	c := e.cfg
	if c.netManager != nil {
		if err := c.netManager.Apply(ctx, c.netPolicy); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), "funcd.Run", "apply worker egress isolation")
		}
	}
	if fwd := e.egressForwarder; fwd != nil {
		e.services.start(ctx, loop(c.logger, "egress dns forwarder stopped", fwd.Serve))
	}
	if gw := e.egressGateway; gw != nil {
		e.services.start(ctx, loop(c.logger, "egress gateway stopped", gw.Serve))
	}
	return nil
}

// Run keeps the egress worker index synced from the running workers (ADR-0117), compacts the function logs (ADR-0083)
// and measures the volumes funcdctl dev cannot limit (ADR-0262).
func (e *executor) Run(ctx context.Context) {
	var loops []func(context.Context)
	if e.egressWorkers != nil {
		loops = append(loops, e.syncEgressWorkers)
	}
	if c := e.compactor; c != nil {
		loops = append(loops, loop(e.cfg.logger, "funclog compactor stopped", c.Run))
	}
	if vm := e.volumes; vm != nil {
		loops = append(loops, func(ctx context.Context) { volume.Run(ctx, vm) })
	}
	runAll(ctx, loops...)
}

// BeforeStop closes the runtime, which stops every worker, then the workers' local API listeners: it is the last
// BeforeStop, so a stopping worker keeps its local API until then (ADR-0305 Decision 5). The egress gateway and the DNS
// forwarder serve on until Stop: a stopping worker still uses them (#1232).
func (e *executor) BeforeStop(context.Context) error {
	closed, err := e.closeRuntime()
	if e.sockets != nil {
		e.sockets.Close()
	}
	if closed {
		return err
	}
	return nil
}

// Stop takes the egress fence down only once the runtime closed, since a worker may run otherwise (ADR-0115). It then
// stops the egress gateway and the DNS forwarder, lets every log Route drain and the sinks seal their segments before
// the blob closes (ADR-0081, ADR-0101), and removes the socket dir it made, which no worker dials now (#330).
func (e *executor) Stop(ctx context.Context) error {
	c := e.cfg
	closed, runtimeErr := e.closeRuntime()
	if c.netManager != nil && runtimeErr == nil {
		_ = c.netManager.Remove(ctx)
	}
	e.services.stop(ctx)
	if e.egressGateway != nil {
		_ = e.egressGateway.Close()
	}
	e.logRoutes.drain(ctx)
	var errs []error
	if closed {
		errs = append(errs, runtimeErr)
	}
	if e.logSink != nil {
		errs = append(errs, e.logSink.Close())
	}
	if e.traceSink != nil {
		errs = append(errs, e.traceSink.Close())
	}
	if e.socketTmpDir != "" {
		errs = append(errs, os.RemoveAll(e.socketTmpDir))
	}
	return errors.Join(errs...)
}

// closeRuntime closes the runtime once, stopping every worker; closed reports whether this call did.
func (e *executor) closeRuntime() (closed bool, err error) {
	e.closeOnce.Do(func() {
		e.closeErr, closed = closeDriver(e.cfg.runtime), true
	})
	return closed, e.closeErr
}

// loop returns run as a loop that logs msg through logger when run ends with an error other than its context's
// cancellation.
func loop(logger *slog.Logger, msg string, run func(context.Context) error) func(context.Context) {
	return func(ctx context.Context) {
		if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.ErrorContext(ctx, msg, "error", err)
		}
	}
}

// runAll runs each loop on ctx in its own goroutine and returns once every one has returned.
func runAll(ctx context.Context, loops ...func(context.Context)) {
	var wg sync.WaitGroup
	for _, loop := range loops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loop(ctx)
		}()
	}
	wg.Wait()
}
