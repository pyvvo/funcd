package eventing

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/activator"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// runTick is the base resolution of the Run loop; a timer fires when at least its
// Interval has elapsed since its last fire.
const runTick = 250 * time.Millisecond

// FunctionRef aliases the activator's ref — the same {Namespace, Name}.
type FunctionRef = activator.FunctionRef

// Invoker delivers a CloudEvent to a function's upstream.
type Invoker interface {
	Invoke(ctx context.Context, fn FunctionRef, ev CloudEvent) error
}

// Waker wakes a scaled-to-zero function and returns its ready upstream (ADR-0033). The
// activator provides it; when set, a timer at a cold function wakes it (closing ADR-0023's
// deferred C3 wake) instead of recording Unavailable.
type Waker interface {
	Wake(ctx context.Context, fn FunctionRef) (upstream string, err error)
}

// Deps configures the eventing Source (internal component, ADR-0002 §1).
type Deps struct {
	Store      store.Store
	Endpoints  activator.Endpoints // resolves the function upstream (P-M provides)
	Waker      Waker               // optional (ADR-0033): wake a cold target instead of failing
	Logger     *slog.Logger        // default slog.Default()
	HTTPClient *http.Client        // default http.DefaultClient
}

// Source is the EventSource reconciler, the registered timer set, and the invoker
// that POSTs CloudEvents at function upstreams.
type Source struct {
	store     store.Store
	endpoints activator.Endpoints
	invoker   Invoker
	logger    *slog.Logger

	mu     sync.Mutex
	timers map[FunctionRef]*timerEntry // keyed by the EventSource's ref
}

// timerEntry is a registered timer source's firing state.
type timerEntry struct {
	interval time.Duration
	function v1.ObjectName
	rg       v1.ResourceGroupName
	lastFire time.Time
}

// NewSource builds the Source. Store and Endpoints are required.
func NewSource(d Deps) (*Source, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("eventing.NewSource", "store is required")
	}
	if d.Endpoints == nil {
		return nil, fault.Invalidf("eventing.NewSource", "endpoints is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "eventing")
	client := d.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Source{
		store:     d.Store,
		endpoints: d.Endpoints,
		invoker:   &httpInvoker{endpoints: d.Endpoints, waker: d.Waker, client: client},
		logger:    logger,
		timers:    map[FunctionRef]*timerEntry{},
	}, nil
}

// Reconcile owns KindEventSource: it registers a type:timer source (→ Ready),
// deregisters on delete, and ignores type:http (the gateway+shim path, P-S).
func (s *Source) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	ref := FunctionRef{Namespace: req.Namespace, Name: req.Name}
	obj, err := s.store.Get(ctx, v1.KindEventSource.GVK(), req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			s.deregister(ref) // delete → stop ticking
			return controller.Result{}, nil
		}
		return controller.Result{}, err
	}
	es, ok := obj.(*v1.EventSource)
	if !ok {
		return controller.Result{}, fault.Internalf("eventing.Reconcile", "unexpected type %T", obj)
	}
	if es.Spec.Type != v1.EventSourceTypeTimer {
		s.deregister(ref) // http (or unset) sources are not timer-driven
		return controller.Result{}, nil
	}
	if es.Spec.Timer == nil || es.Spec.Timer.Interval <= 0 {
		return controller.Result{}, fault.Invalidf("eventing.Reconcile", "timer eventsource %q needs a positive interval", req.Name)
	}
	s.register(ref, es)
	if es.Status.Phase != v1.PhaseReady {
		es.Status.Phase = v1.PhaseReady
		if _, err := s.store.Update(ctx, es); err != nil {
			return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), "eventing.Reconcile", "set eventsource ready")
		}
	}
	return controller.Result{}, nil
}

// Fire performs one deterministic tick of the named EventSource: build the
// CloudEvent, invoke the bound function, and record the Invocation.
func (s *Source) Fire(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	obj, err := s.store.Get(ctx, v1.KindEventSource.GVK(), ns, name)
	if err != nil {
		return err
	}
	es, ok := obj.(*v1.EventSource)
	if !ok {
		return fault.Internalf("eventing.Fire", "unexpected type %T", obj)
	}
	if es.Spec.Type != v1.EventSourceTypeTimer {
		return fault.Invalidf("eventing.Fire", "eventsource %q is not a timer", name)
	}
	return s.dispatch(ctx, ns, name, es.Spec.Function, es.ResourceGroup)
}

// Run ticks the registered timer set until ctx is cancelled, firing each source
// whose Interval has elapsed. Started by P-I; not part of the reconciler.
func (s *Source) Run(ctx context.Context) error {
	ticker := time.NewTicker(runTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			for _, d := range s.dueTimers(time.Now()) {
				if err := s.dispatch(ctx, d.ref.Namespace, d.ref.Name, d.fn, d.rg); err != nil {
					s.logger.WarnContext(ctx, "timer dispatch failed", "eventsource", d.ref.Name, "error", err)
				}
			}
		}
	}
}

// ActiveTimers reports the number of registered timer sources (observability; a
// status/metrics surface and the reconcile scenario both read it).
func (s *Source) ActiveTimers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.timers)
}

// dueTimer is a snapshot of a timer that is due to fire this tick.
type dueTimer struct {
	ref FunctionRef
	fn  v1.ObjectName
	rg  v1.ResourceGroupName
}

// dueTimers marks and returns the timers whose interval has elapsed, advancing
// their lastFire under the lock so a fire is never double-counted across ticks.
func (s *Source) dueTimers(now time.Time) []dueTimer {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []dueTimer
	for ref, e := range s.timers {
		if now.Sub(e.lastFire) >= e.interval {
			e.lastFire = now
			due = append(due, dueTimer{ref: ref, fn: e.function, rg: e.rg})
		}
	}
	return due
}

// register adds or updates a timer entry, preserving lastFire when nothing changed
// so a frequent reconcile cannot starve firing.
func (s *Source) register(ref FunctionRef, es *v1.EventSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.timers[ref]; ok && e.interval == es.Spec.Timer.Interval && e.function == es.Spec.Function {
		return
	}
	s.timers[ref] = &timerEntry{
		interval: es.Spec.Timer.Interval,
		function: es.Spec.Function,
		rg:       es.ResourceGroup,
		lastFire: time.Now(),
	}
}

func (s *Source) deregister(ref FunctionRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.timers, ref)
}

// dispatch builds the CloudEvent, invokes the bound function, and records the
// Invocation. A not-ready / failed invoke is recorded (never silently dropped)
// and the invoke error is returned.
func (s *Source) dispatch(ctx context.Context, ns v1.NamespaceName, source, fn v1.ObjectName, rg v1.ResourceGroupName) error {
	ev, err := NewTimerEvent(ns, source)
	if err != nil {
		return err
	}
	start := time.Now().UTC()
	invErr := s.invoker.Invoke(ctx, FunctionRef{Namespace: ns, Name: fn}, ev)
	if recErr := s.record(ctx, ns, rg, ev, start, invErr); recErr != nil {
		s.logger.WarnContext(ctx, "record invocation failed", "eventsource", source, "error", recErr)
	}
	return invErr
}

// record persists an Invocation for one dispatch (Ready on success, Failed +
// Error on a not-ready/failed invoke).
func (s *Source) record(ctx context.Context, ns v1.NamespaceName, rg v1.ResourceGroupName, ev CloudEvent, start time.Time, invErr error) error {
	obj, ok := v1.NewObject(v1.KindInvocation)
	if !ok {
		return fault.Internalf("eventing.record", "unknown kind %s", v1.KindInvocation)
	}
	inv := obj.(*v1.Invocation)
	inv.Name = v1.ObjectName("inv-" + ev.ID)
	inv.Namespace = ns
	inv.ResourceGroup = rg
	inv.Status.StartTime = start
	inv.Status.EndTime = time.Now().UTC()
	if invErr != nil {
		inv.Status.Error = invErr.Error()
		inv.Status.Phase = v1.PhaseFailed
	} else {
		inv.Status.Phase = v1.PhaseReady
	}
	_, err := s.store.Create(ctx, inv)
	return err
}

// httpInvoker resolves the function's ready upstream via activator.Endpoints and
// POSTs the CloudEvent. It imports Endpoints (read) — not the gateway; waking a
// scaled-to-zero function via a trigger is deferred to the activator-route wiring (P-I).
type httpInvoker struct {
	endpoints activator.Endpoints
	waker     Waker // optional (ADR-0033): wake a cold target before invoking
	client    *http.Client
}

func (i *httpInvoker) Invoke(ctx context.Context, fn FunctionRef, ev CloudEvent) error {
	upstream, ready, err := i.endpoints.Upstream(ctx, fn)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "eventing.Invoke", "resolve upstream for %s/%s", fn.Namespace, fn.Name)
	}
	if !ready || upstream == "" {
		// Trigger-driven wake (ADR-0033): a timer at a scaled-to-zero function wakes it
		// rather than dropping the trigger. Without a Waker, the legacy Unavailable stands.
		if i.waker == nil {
			return fault.Unavailablef("eventing.Invoke", "function %s/%s has no ready upstream", fn.Namespace, fn.Name)
		}
		upstream, err = i.waker.Wake(ctx, fn)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), "eventing.Invoke", "wake %s/%s", fn.Namespace, fn.Name)
		}
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return fault.Internalf("eventing.Invoke", "marshal cloudevent: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(body))
	if err != nil {
		return fault.Internalf("eventing.Invoke", "build request: %v", err)
	}
	req.Header.Set("Content-Type", contentTypeCE)
	resp, err := i.client.Do(req)
	if err != nil {
		return fault.Unavailablef("eventing.Invoke", "POST to %s/%s upstream: %v", fn.Namespace, fn.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		return fault.Unavailablef("eventing.Invoke", "function %s/%s returned status %d", fn.Namespace, fn.Name, resp.StatusCode)
	}
	return nil
}
