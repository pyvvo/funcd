package eventing

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// runTick is the base resolution of the Run loop; a named event fires when at least its Interval has
// elapsed since its last fire.
const runTick = 250 * time.Millisecond

// Publisher is the delivery seam a Source emits named CloudEvents to (ADR-0108). The V1 driver is the
// in-process Fanout (fanout.go) the F69 Sensor subscribes to; a bus-backed driver is a V2 swap. Emitting
// to nobody is a no-op by design (an EventSource with no Sensor yet is valid).
type Publisher interface {
	Publish(ctx context.Context, ev CloudEvent) error
}

// eventKey identifies one named event of one EventSource: (namespace, source, event). It is the timer-set
// key and the Fanout subscription key (ADR-0108) — the fanout derives it from a CloudEvent's URI + type.
type eventKey struct {
	ns     v1.NamespaceName
	source v1.ObjectName
	event  v1.ObjectName
}

// Deps configures the eventing Source (internal component, ADR-0002 §1; reshaped by ADR-0108).
type Deps struct {
	Store     store.Store  // required
	Publisher Publisher    // where a firing emits its named CloudEvent (required)
	Logger    *slog.Logger // default slog.Default()
}

// Source is the EventSource reconciler + the registered named-event timer set. A firing PUBLISHES a named
// CloudEvent (ADR-0108); the action side (invoke a function / start a workflow, and the Invocation record)
// is the F69 Sensor (ADR-0109). The Source no longer invokes.
type Source struct {
	store     store.Store
	publisher Publisher
	logger    *slog.Logger

	mu     sync.Mutex
	timers map[eventKey]*timerEntry
}

// timerEntry is a registered named event's firing state.
type timerEntry struct {
	interval time.Duration
	lastFire time.Time
}

// NewSource builds the Source. Store and Publisher are required.
func NewSource(d Deps) (*Source, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("eventing.NewSource", "store is required")
	}
	if d.Publisher == nil {
		return nil, fault.Invalidf("eventing.NewSource", "publisher is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Source{
		store:     d.Store,
		publisher: d.Publisher,
		logger:    logger.With("component", "eventing"),
		timers:    map[eventKey]*timerEntry{},
	}, nil
}

// Reconcile owns KindEventSource (ADR-0108): it registers every named event of a `timer:` source
// (→ Ready), deregisters on delete, and registers nothing for a source with no timer kind.
func (s *Source) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	obj, err := s.store.Get(ctx, v1.KindEventSource.GVK(), req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			s.deregisterSource(req.Namespace, req.Name) // delete → stop ticking
			return controller.Result{}, nil
		}
		return controller.Result{}, err
	}
	es, ok := obj.(*v1.EventSource)
	if !ok {
		return controller.Result{}, fault.Internalf("eventing.Reconcile", "unexpected type %T", obj)
	}
	if es.Spec.Timer == nil {
		s.deregisterSource(req.Namespace, req.Name) // no timer kind (a future webhook source): not tick-driven
		return controller.Result{}, nil
	}
	s.registerTimer(req.Namespace, req.Name, es.Spec.Timer)
	if es.Status.Phase != v1.PhaseReady {
		es.Status.Phase = v1.PhaseReady
		if _, err := s.store.Update(ctx, es); err != nil {
			return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), "eventing.Reconcile", "set eventsource ready")
		}
	}
	return controller.Result{}, nil
}

// registerTimer (re)registers every named event of a timer source, preserving lastFire when the interval
// is unchanged (a frequent reconcile can't starve firing), and prunes events removed from the spec.
func (s *Source) registerTimer(ns v1.NamespaceName, source v1.ObjectName, t *v1.TimerSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[eventKey]bool, len(t.Events))
	for i := range t.Events {
		ev := &t.Events[i]
		k := eventKey{ns: ns, source: source, event: ev.Name}
		want[k] = true
		if e, ok := s.timers[k]; ok && e.interval == ev.Interval {
			continue // unchanged — keep its lastFire
		}
		s.timers[k] = &timerEntry{interval: ev.Interval, lastFire: time.Now()}
	}
	for k := range s.timers { // prune events dropped from the spec
		if k.ns == ns && k.source == source && !want[k] {
			delete(s.timers, k)
		}
	}
}

// deregisterSource removes every named event of one EventSource (delete / loses its timer kind).
func (s *Source) deregisterSource(ns v1.NamespaceName, source v1.ObjectName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.timers {
		if k.ns == ns && k.source == source {
			delete(s.timers, k)
		}
	}
}

// Fire performs one deterministic tick of a named event: build its CloudEvent and publish it. A
// published-to-nobody event is a no-op (no Sensor subscribed yet) — never an error.
func (s *Source) Fire(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName) error {
	ev, err := NewNamedEvent(ns, source, event)
	if err != nil {
		return err
	}
	return s.publisher.Publish(ctx, ev)
}

// Run ticks the registered named-event set until ctx is cancelled, publishing each event whose Interval
// has elapsed. Started by the pkg/funcd lifecycle (ADR-0033); not part of the reconciler.
func (s *Source) Run(ctx context.Context) error {
	ticker := time.NewTicker(runTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			for _, k := range s.dueTimers(time.Now()) {
				if err := s.Fire(ctx, k.ns, k.source, k.event); err != nil {
					s.logger.WarnContext(ctx, "timer publish failed", "eventsource", k.source, "event", k.event, "error", err)
				}
			}
		}
	}
}

// dueTimers marks and returns the named events whose interval has elapsed, advancing their lastFire
// under the lock so a fire is never double-counted across ticks.
func (s *Source) dueTimers(now time.Time) []eventKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []eventKey
	for k, e := range s.timers {
		if now.Sub(e.lastFire) >= e.interval {
			e.lastFire = now
			due = append(due, k)
		}
	}
	return due
}

// ActiveTimers reports the number of registered named-event timers (observability; a status/metrics
// surface and the reconcile scenario both read it).
func (s *Source) ActiveTimers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.timers)
}
