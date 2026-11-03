package funcd

import (
	"context"
	"io"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/lifecycle"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/workflow"
)

// The binding providers (ADR-0305): the high-level providers a Function binds. Each reads its parts from the Platform
// when a hook runs, and each hook is safe on a platform that never booted.

// kvProvider is the KV provider (ADR-0069/0072).
type kvProvider struct{ p *Platform }

var (
	_ lifecycle.Starter   = (*kvProvider)(nil)
	_ lifecycle.Reclaimer = (*kvProvider)(nil)
	_ lifecycle.Stopper   = (*kvProvider)(nil)
	_ describer           = (*kvProvider)(nil)
)

func (s *kvProvider) Name() string { return "kv" }

func (s *kvProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "kv", Kind: provider.Builtin, Port: "kvstore.KV", Bindings: []string{"spec.bindings.kv"},
		Summary: "namespaced key-value store"}}
}

// Start marks the KVStores the previous materializer made before any controller, the collector or the control plane
// reads them (ADR-0178 Decision 3).
func (s *kvProvider) Start(ctx context.Context) error {
	if err := workflow.MarkKVStoresOnce(ctx, s.p.cfg.store, s.p.logger); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "funcd.Run", "mark workflow kv stores")
	}
	return nil
}

// Reclaim reclaims the data of each KVStore deleted before its reconcile ran, before a store of the same name can be
// created (issue #708).
func (s *kvProvider) Reclaim(ctx context.Context) error {
	return s.p.kvReconciler.ReclaimDeleted(ctx, s.p.cfg.quarantineNow())
}

// Stop closes the durable KV driver (ADR-0066), once the workers that call it stopped.
func (s *kvProvider) Stop(context.Context) error {
	if cl, ok := s.p.cfg.kvStore.(io.Closer); ok {
		_ = cl.Close()
	}
	return nil
}

// blobProvider is the blob provider, with the S3 gateway it serves from its Start to its Stop (ADR-0080/0085).
type blobProvider struct{ p *Platform }

var (
	_ lifecycle.Starter   = (*blobProvider)(nil)
	_ lifecycle.Reclaimer = (*blobProvider)(nil)
	_ lifecycle.Stopper   = (*blobProvider)(nil)
	_ describer           = (*blobProvider)(nil)
)

func (s *blobProvider) Name() string { return "blob" }

func (s *blobProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{
		{Name: "blob", Kind: provider.Builtin, Port: "blob.Bucket", Bindings: []string{"spec.bindings.blob"}, Summary: "opaque-byte object substrate"},
		{Name: "s3", Kind: provider.Builtin, Port: "blob.Bucket", Bindings: []string{"spec.bindings.blob"}, Summary: "S3-protocol frontend over blob (ADR-0080)"},
	}
}

// Start serves the S3 gateway, and a taken port fails the start: versitygw binds the address only once Run serves it
// (#497, ADR-0028).
func (s *blobProvider) Start(ctx context.Context) error {
	p := s.p
	if gw := p.s3gw; gw != nil {
		p.workerServices.start(ctx, p.loop("s3 gateway stopped", gw.Run))
		if err := gw.Wait(ctx); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), "funcd.Run", "start the s3 gateway")
		}
	}
	return nil
}

// Reclaim reclaims the objects of each Bucket deleted before its second purge ran (ADR-0199 Decision 7).
func (s *blobProvider) Reclaim(ctx context.Context) error {
	p := s.p
	return reclaimDeletedBuckets(ctx, p.cfg.blob, p.cfg.store, p.logger, p.cfg.quarantineNow(), p.cfg.quarantinePeriodOrDefault())
}

// Stop stops the S3 gateway after the runtime stopped the workers that use it (#1232). Its Serve ends first, so Close
// cannot fail a Serve that still runs, and it closes before blob.Close, since it serves from the blob substrate.
func (s *blobProvider) Stop(ctx context.Context) error {
	s.p.workerServices.stop(ctx)
	if s.p.s3gw != nil {
		return s.p.s3gw.Close()
	}
	return nil
}

// sqlProvider is the SQL provider (ADR-0232).
type sqlProvider struct{ p *Platform }

var (
	_ lifecycle.Reclaimer = (*sqlProvider)(nil)
	_ lifecycle.Runner    = (*sqlProvider)(nil)
	_ lifecycle.Stopper   = (*sqlProvider)(nil)
	_ describer           = (*sqlProvider)(nil)
)

func (s *sqlProvider) Name() string { return "sql" }

func (s *sqlProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "sql", Kind: provider.Addon, Bindings: []string{"spec.bindings.sql"},
		Summary: "one rqlite SQL engine per SQLDatabase (ADR-0232)"}}
}

// Reclaim reclaims the directory of each SQLDatabase deleted before its reconcile ran (ADR-0232 Decision 7).
func (s *sqlProvider) Reclaim(ctx context.Context) error {
	return s.p.sqlReconciler.ReclaimOrphans(ctx, s.p.cfg.quarantineNow())
}

// Run opens and serves the engines' change feed (ADR-0235) until the run context ends.
func (s *sqlProvider) Run(ctx context.Context) {
	p := s.p
	if p.sqlFeed == nil {
		return
	}
	serve := func(ctx context.Context) error {
		if p.sqlFeedListener != nil {
			return p.sqlFeed.ServeListener(ctx, p.sqlFeedListener)
		}
		return p.sqlFeed.Serve(ctx, p.sqlFeedHost, p.sqlFeedPort)
	}
	runAll(ctx, p.openSQLFeed, p.loop("sql change feed stopped", serve))
}

// Stop releases the change feed's loopback port, which New holds whether or not Run served it (ADR-0260).
func (s *sqlProvider) Stop(context.Context) error {
	if s.p.sqlFeedListener != nil {
		_ = s.p.sqlFeedListener.Close()
	}
	return nil
}

// lakeProvider is the Lake provider (ADR-0086).
type lakeProvider struct{ p *Platform }

var (
	_ lifecycle.BeforeStopper = (*lakeProvider)(nil)
	_ describer               = (*lakeProvider)(nil)
)

func (s *lakeProvider) Name() string { return "lake" }

func (s *lakeProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "lake", Kind: provider.Addon, Bindings: []string{"spec.bindings.lake"},
		Summary: "the lake service: DuckLake over DuckDB and Quack (F48, ADR-0086)"}}
}

// BeforeStop closes the node-private PEP proxies before the runtime stops the engines behind them (ADR-0137).
func (s *lakeProvider) BeforeStop(context.Context) error {
	if s.p.lakeProxy != nil {
		s.p.lakeProxy.Shutdown()
	}
	return nil
}

// eventingProvider is the eventing provider (ADR-0108).
type eventingProvider struct{ p *Platform }

var (
	_ lifecycle.Runner = (*eventingProvider)(nil)
	_ describer        = (*eventingProvider)(nil)
)

func (s *eventingProvider) Name() string { return "eventing" }

func (s *eventingProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "eventing", Kind: provider.Builtin, Summary: "CloudEvents trigger delivery (EventSource)"}}
}

// Run delivers events, polls the blob EventSources (ADR-0119), retries the Sensor's actions and sweeps the dead letters
// (ADR-0118). The retries drain within the platform's drain bound once the run context ends.
func (s *eventingProvider) Run(ctx context.Context) {
	p := s.p
	loops := []func(context.Context){p.loop("eventing stopped", p.eventing.Run)}
	if p.blobWatcher != nil {
		loops = append(loops, p.loop("blob watcher stopped", p.blobWatcher.Run))
	}
	if p.sensorReconciler != nil {
		loops = append(loops, func(ctx context.Context) { p.sensorReconciler.RunRetryWorkers(ctx, p.drainTimeout) })
	}
	if p.deadLetters != nil && (p.deadletterRetention > 0 || p.deadletterMaxEntries > 0) {
		loops = append(loops, p.runDeadLetterRetention)
	}
	runAll(ctx, loops...)
}

// catalogEntry is a provider with no hook: its Function-facing service is served from the socket or the env, and it
// only names its catalog entry.
type catalogEntry struct{ d provider.Descriptor }

var _ describer = catalogEntry{}

func (c catalogEntry) Name() string                       { return c.d.Name }
func (c catalogEntry) Descriptors() []provider.Descriptor { return []provider.Descriptor{c.d} }

// hookless are the providers with no hook, after the ones that have hooks.
func hookless() []lifecycle.Provider {
	return []lifecycle.Provider{
		catalogEntry{provider.Descriptor{Name: "secrets", Kind: provider.Builtin, Port: "secrets.Resolver",
			Bindings: []string{"spec.bindings.secret"}, Summary: "secret resolution + at-rest encryption"}},
		catalogEntry{provider.Descriptor{Name: "config", Kind: provider.Builtin, Bindings: []string{"spec.bindings.config"},
			Summary: "ConfigMap values a Function reads from its env (ADR-0245)"}},
		catalogEntry{provider.Descriptor{Name: "invoke", Kind: provider.Builtin, Bindings: []string{"spec.bindings.links"},
			Summary: "synchronous fn-to-fn RPC (context.invoke)"}},
	}
}
