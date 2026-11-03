package funcd

import (
	"context"
	"errors"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/lifecycle"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/volume"
)

// executor is the platform's Executor provider (ADR-0305): the runtime that runs every workload, and what it gives a
// workload around it: the egress fence, the egress gateway and the DNS forwarder, and the capture of its logs. It reads
// its parts from the Platform when a hook runs, not when it is built, so a part set after New counts.
type executor struct {
	p *Platform

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

func (e *executor) Name() string { return "executor" }

func (e *executor) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{
		{Name: "egress", Kind: provider.Builtin, Summary: "egress gateway (default-deny + audit)"},
		{Name: "log-ingest", Kind: provider.Builtin, Summary: "function-telemetry side channel + funclog read path (ADR-0081/0084)"},
	}
}

// Start applies the egress fence before any worker can run (ADR-0115), then serves the egress gateway and the DNS
// forwarder (ADR-0117).
func (e *executor) Start(ctx context.Context) error {
	p := e.p
	if p.cfg.netManager != nil {
		if err := p.cfg.netManager.Apply(ctx, p.cfg.netPolicy); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), "funcd.Run", "apply worker egress isolation")
		}
	}
	if fwd := p.egressForwarder; fwd != nil {
		e.services.start(ctx, p.loop("egress dns forwarder stopped", fwd.Serve))
	}
	if gw := p.egressGateway; gw != nil {
		e.services.start(ctx, p.loop("egress gateway stopped", gw.Serve))
	}
	return nil
}

// Run keeps the egress worker index synced from the running workers (ADR-0117), compacts the function logs (ADR-0083)
// and measures the volumes funcdctl dev cannot limit (ADR-0262).
func (e *executor) Run(ctx context.Context) {
	p := e.p
	var loops []func(context.Context)
	if p.egressWorkers != nil {
		loops = append(loops, p.syncEgressWorkers)
	}
	if c := p.compactor; c != nil {
		loops = append(loops, p.loop("funclog compactor stopped", c.Run))
	}
	if vm := p.volumes; vm != nil {
		loops = append(loops, func(ctx context.Context) { volume.Run(ctx, vm) })
	}
	runAll(ctx, loops...)
}

// BeforeStop closes the runtime, which stops every worker. The egress gateway and the DNS forwarder serve on until
// Stop: a stopping worker still uses them (#1232).
func (e *executor) BeforeStop(context.Context) error {
	if closed, err := e.closeRuntime(); closed {
		return err
	}
	return nil
}

// Stop takes the egress fence down only once the runtime closed, since a worker may run otherwise (ADR-0115). It then
// stops the egress gateway and the DNS forwarder, and lets every log Route drain and the sinks seal their segments
// before the blob closes (ADR-0081, ADR-0101).
func (e *executor) Stop(ctx context.Context) error {
	p := e.p
	closed, runtimeErr := e.closeRuntime()
	if p.cfg.netManager != nil && runtimeErr == nil {
		_ = p.cfg.netManager.Remove(ctx)
	}
	e.services.stop(ctx)
	if p.egressGateway != nil {
		_ = p.egressGateway.Close()
	}
	p.logRoutes.drain(ctx)
	var errs []error
	if closed {
		errs = append(errs, runtimeErr)
	}
	if p.logSink != nil {
		errs = append(errs, p.logSink.Close())
	}
	if p.traceSink != nil {
		errs = append(errs, p.traceSink.Close())
	}
	return errors.Join(errs...)
}

// closeRuntime closes the runtime once, stopping every worker; closed reports whether this call did.
func (e *executor) closeRuntime() (closed bool, err error) {
	e.closeOnce.Do(func() {
		e.closeErr, closed = closeDriver(e.p.cfg.runtime), true
	})
	return closed, e.closeErr
}

// loop returns run as a loop that logs msg when run ends with an error other than its context's cancellation.
func (p *Platform) loop(msg string, run func(context.Context) error) func(context.Context) {
	return func(ctx context.Context) {
		if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.logger.ErrorContext(ctx, msg, "error", err)
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
