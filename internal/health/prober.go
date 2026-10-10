// Package health is the platform's built-in health (ADR-0215): the storage prober, which reads a sentinel key of the
// KV engine and the blob store on a period and keeps each result in memory, and the dependency checker the
// worker-node local API answers GET /health/dependencies with.
package health

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// Target names a storage the prober reads.
type Target string

const (
	TargetKV   Target = "kv"
	TargetBlob Target = "blob"
	// SentinelKey is the key a probe reads. A namespace is a DNS label, so no data key starts with ".".
	SentinelKey = ".funcd-health"
)

// Probe reads one storage once; an error is the storage being unreachable.
type Probe func(ctx context.Context) error

// Result is a target's latest probe outcome: Message holds the probe error while it fails, and Since is when Healthy
// last changed (the first probe for a result that never changed).
type Result struct {
	Healthy bool
	Message string
	Since   time.Time
}

// KVProbe reads SentinelKey from kv: found or missing is healthy.
func KVProbe(kv kvstore.KV) Probe {
	return func(ctx context.Context) error {
		_, _, err := kv.Get(ctx, SentinelKey)
		return err
	}
}

// BlobProbe asks b whether SentinelKey exists: either answer is healthy.
func BlobProbe(b blob.Bucket) Probe {
	return func(ctx context.Context) error {
		_, err := b.Exists(ctx, SentinelKey)
		return err
	}
}

// Prober probes each target once per interval, each probe bounded by timeout, and keeps the results in memory
// (ADR-0215 Decision 6). It writes nothing.
type Prober struct {
	clock    clock.Clock
	interval time.Duration
	timeout  time.Duration
	probes   map[Target]Probe
	logger   *slog.Logger

	mu        sync.Mutex
	results   map[Target]Result
	listeners []func(Target, Result)
}

// NewProber builds a prober over probes. interval and timeout are positive and timeout is less than interval.
func NewProber(c clock.Clock, interval, timeout time.Duration, probes map[Target]Probe, logger *slog.Logger) (*Prober, error) {
	const op = "health.NewProber"
	switch {
	case interval <= 0 || timeout <= 0:
		return nil, fault.Invalidf(op, "the probe interval %s and timeout %s must be positive", interval, timeout)
	case timeout >= interval:
		return nil, fault.Invalidf(op, "the probe timeout %s must be less than the interval %s", timeout, interval)
	}
	if c == nil {
		c = clock.System()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Prober{
		clock: c, interval: interval, timeout: timeout, probes: maps.Clone(probes),
		logger: logger.With("component", "health"), results: map[Target]Result{},
	}, nil
}

// Start probes each target once before it returns, then once per interval until ctx is done.
func (p *Prober) Start(ctx context.Context) {
	p.probeAll(ctx)
	go func() {
		t := time.NewTicker(p.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.probeAll(ctx)
			}
		}
	}()
}

// Result is target t's latest result; a target with no probe, or not probed yet, is healthy.
func (p *Prober) Result(t Target) Result {
	if p == nil {
		return Result{Healthy: true}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if r, ok := p.results[t]; ok {
		return r
	}
	return Result{Healthy: true}
}

// OnChange registers fn, called after a probe whose result's Healthy differs from the target's previous one.
func (p *Prober) OnChange(fn func(Target, Result)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listeners = append(p.listeners, fn)
}

func (p *Prober) probeAll(ctx context.Context) {
	targets := slices.Sorted(maps.Keys(p.probes))
	for _, t := range targets {
		if ctx.Err() != nil {
			return
		}
		p.record(t, p.probe(ctx, p.probes[t]))
	}
}

// probe runs pr bounded by the timeout; a probe that ignores its context is left behind once the timeout passes.
func (p *Prober) probe(ctx context.Context, pr Probe) error {
	pctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pr(pctx) }()
	select {
	case err := <-done:
		return err
	case <-pctx.Done():
		if errors.Is(pctx.Err(), context.DeadlineExceeded) {
			return fault.Unavailablef("health.probe", "the probe did not answer within %s", p.timeout)
		}
		return pctx.Err()
	}
}

func (p *Prober) record(t Target, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	now := p.clock.Now()
	next := Result{Healthy: err == nil, Since: now}
	if err != nil {
		next.Message = err.Error()
	}
	p.mu.Lock()
	prev, seen := p.results[t]
	if !seen {
		prev = Result{Healthy: true}
	} else if prev.Healthy == next.Healthy {
		next.Since = prev.Since
	}
	p.results[t] = next
	flipped := prev.Healthy != next.Healthy
	listeners := slices.Clone(p.listeners)
	p.mu.Unlock()
	if !flipped {
		return
	}
	if next.Healthy {
		p.logger.Info("storage probe recovered", "target", string(t))
	} else {
		p.logger.Warn("storage probe failing", "target", string(t), "error", next.Message)
	}
	for _, fn := range listeners {
		fn(t, next)
	}
}

// ReasonStorageUnreachable is the Ready reason of a KVStore or Bucket whose storage probe fails (ADR-0215 Decision 7).
const ReasonStorageUnreachable = "StorageUnreachable"

// StoreStatus is cur, a KVStore's or Bucket's status, at generation gen after its storage probe gave r: phase Ready
// with Ready True while the probe passes, else phase Degraded with Ready False StorageUnreachable and the probe error.
func StoreStatus(cur v1.Status, r Result, gen int64) v1.Status {
	next := cur
	next.Conditions = slices.Clone(cur.Conditions)
	next.ObservedGeneration = gen
	ready := v1.Condition{Type: condReady, Status: v1.ConditionTrue, ObservedGeneration: gen}
	next.Phase = v1.PhaseReady
	if !r.Healthy {
		ready.Status, ready.Reason, ready.Message = v1.ConditionFalse, ReasonStorageUnreachable, r.Message
		next.Phase = v1.PhaseDegraded
	}
	next.Conditions.Set(ready)
	return next
}

const condReady v1.ConditionType = "Ready"
