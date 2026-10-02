package eventing

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
)

// runTick is the base resolution of the Run loop; a named event fires when at least its Interval has
// elapsed since its last fire. It is a quarter of the 100ms interval floor, so every interval gets a tick
// in each period and a firing is at most one tick late.
const runTick = 25 * time.Millisecond

// condReady is the EventSource readiness condition type (ADR-0119): a blob source with a missing Bucket is
// NotReady with a reason, mirroring the Route BackendNotFound pattern.
const condReady = v1.ConditionType("Ready")

// bucketRecheckInterval requeues every blob source so a Bucket created or deleted after the EventSource is
// picked up without an external trigger: no Bucket event reaches this reconciler (ADR-0119: missing bucket ⇒
// NotReady, not Ready-but-silently-not-polling).
const bucketRecheckInterval = 15 * time.Second

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

// Deps configures the eventing Source (internal component, ADR-0002 §1; reshaped by ADR-0108, extended by
// ADR-0119).
type Deps struct {
	Store     store.Store  // required
	Publisher Publisher    // where a firing emits its named CloudEvent (required)
	Blob      *BlobWatcher // ADR-0119: the poll watcher a `blob:` source registers on; nil ⇒ no blob support
	Logger    *slog.Logger // default slog.Default()
}

// Source is the EventSource reconciler + the registered named-event timer set. A firing PUBLISHES a named
// CloudEvent (ADR-0108); the action side (invoke a function / start a workflow, and the Invocation record)
// is the F69 Sensor (ADR-0109). The Source no longer invokes.
type Source struct {
	store     store.Store
	publisher Publisher
	blob      *BlobWatcher // ADR-0119: nil ⇒ blob sources cannot be registered
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
		blob:      d.Blob,
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
	if es.Spec.Blob != nil {
		return s.reconcileBlob(ctx, es)
	}
	s.deregisterBlob(req.Namespace, req.Name) // not (any longer) a blob source: stop watching
	if es.Spec.Timer == nil {
		s.deregisterTimers(req.Namespace, req.Name) // no timer kind (a future webhook source): not tick-driven
		return controller.Result{}, nil
	}
	s.registerTimer(req.Namespace, req.Name, es.Spec.Timer)
	_, blobCond := es.Status.Conditions.Get(condReady) // left by an earlier blob kind; a timer source has none
	if es.Status.Phase != v1.PhaseReady || blobCond {
		es.Status.Phase = v1.PhaseReady
		es.Status.Conditions = slices.DeleteFunc(es.Status.Conditions, func(c v1.Condition) bool { return c.Type == condReady })
		if _, err := s.store.Update(ctx, es); err != nil {
			return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), "eventing.Reconcile", "set eventsource ready")
		}
	}
	return controller.Result{}, nil
}

// reconcileBlob owns the `blob:` source branch (ADR-0119): it resolves the watched Bucket, registers the
// source's named events on the BlobWatcher and sets Ready — or, when the Bucket does not exist in the
// namespace, deregisters and sets NotReady with a BucketNotFound condition (never Ready-but-not-polling).
// Either way it requeues, so a Bucket created or deleted later is picked up. `on` is defaulted on the
// watcher's copy (decode/normalize), not in Validate and not in the stored spec.
func (s *Source) reconcileBlob(ctx context.Context, es *v1.EventSource) (controller.Result, error) {
	ns, name := es.Namespace, es.Name
	s.deregisterTimers(ns, name) // a source that became a blob kind must stop any prior timers
	if s.blob == nil {
		return controller.Result{}, s.setBlobNotReady(ctx, es, "BlobWatcherUnavailable", "blob event watching is not enabled")
	}
	_, err := s.store.Get(ctx, v1.KindBucket.GVK(), ns, es.Spec.Blob.Bucket)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			s.deregisterBlob(ns, name)
			nrErr := s.setBlobNotReady(ctx, es, "BucketNotFound", fmt.Sprintf("bucket %q not found in namespace %q", es.Spec.Blob.Bucket, ns))
			return controller.Result{RequeueAfter: bucketRecheckInterval}, nrErr
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), "eventing.reconcileBlob", "resolve bucket %q", es.Spec.Blob.Bucket)
	}
	s.blob.Register(ns, name, normalizedBlob(es))
	if cur, ok := es.Status.Conditions.Get(condReady); !ok || cur.Status != v1.ConditionTrue || cur.ObservedGeneration != es.Generation || es.Status.Phase != v1.PhaseReady {
		es.Status.Phase = v1.PhaseReady
		es.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, Reason: "Watching", ObservedGeneration: es.Generation})
		if _, uerr := s.store.Update(ctx, es); uerr != nil {
			return controller.Result{}, fault.Wrapf(uerr, fault.KindOf(uerr), "eventing.reconcileBlob", "set eventsource ready")
		}
	}
	return controller.Result{RequeueAfter: bucketRecheckInterval}, nil
}

// normalizedBlob returns es's blob source with the ADR-0119 defaults applied to a copy. es is written back
// for its status, so defaulting es itself would rewrite the user's spec and bump its generation past the
// condition stamped with it.
func normalizedBlob(es *v1.EventSource) *v1.BlobSource {
	view := v1.EventSource{Spec: v1.EventSourceSpec{Blob: &v1.BlobSource{Bucket: es.Spec.Blob.Bucket, Events: slices.Clone(es.Spec.Blob.Events)}}}
	view.Normalize()
	return view.Spec.Blob
}

// setBlobNotReady marks a blob source NotReady with a reason/message (ADR-0119, mirroring Route BackendNotFound).
func (s *Source) setBlobNotReady(ctx context.Context, es *v1.EventSource, reason, msg string) error {
	es.Status.Phase = v1.PhasePending
	es.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: msg, ObservedGeneration: es.Generation})
	if _, err := s.store.Update(ctx, es); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "eventing.reconcileBlob", "set eventsource not-ready")
	}
	return nil
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

// deregisterSource removes every registration (timer + blob) of one EventSource — used on delete.
func (s *Source) deregisterSource(ns v1.NamespaceName, source v1.ObjectName) {
	s.deregisterTimers(ns, source)
	s.deregisterBlob(ns, source)
}

// deregisterTimers removes every named timer event of one EventSource (delete / loses its timer kind).
func (s *Source) deregisterTimers(ns v1.NamespaceName, source v1.ObjectName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.timers {
		if k.ns == ns && k.source == source {
			delete(s.timers, k)
		}
	}
}

// deregisterBlob removes every named blob event of one EventSource from the watcher (nil watcher ⇒ no-op).
func (s *Source) deregisterBlob(ns v1.NamespaceName, source v1.ObjectName) {
	if s.blob != nil {
		s.blob.Deregister(ns, source)
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
// under the lock so a fire is never double-counted across ticks. lastFire advances to the latest period
// boundary, not to the tick time, so tick lateness never stretches the period, and periods missed while a
// publish was in flight are skipped rather than fired in a burst.
func (s *Source) dueTimers(now time.Time) []eventKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []eventKey
	for k, e := range s.timers {
		if elapsed := now.Sub(e.lastFire); elapsed >= e.interval {
			e.lastFire = now.Add(-(elapsed % e.interval))
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
