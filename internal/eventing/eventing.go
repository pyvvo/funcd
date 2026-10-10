package eventing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/cron"
	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/store"
)

// runTick is the base resolution of the Run loop; a named event fires when at least its Interval has
// elapsed since its last fire. It is a quarter of the 100ms interval floor, so every interval gets a tick
// in each period and a firing is at most one tick late.
const runTick = 25 * time.Millisecond

// condReady is the EventSource readiness condition type (ADR-0119): a blob source with a missing Bucket is
// NotReady with a reason, mirroring the Route BackendNotFound pattern. A timer source is Ready once its events
// are registered; both kinds stamp the generation they observed (ADR-0199 Decision 5).
const condReady = v1.ConditionType("Ready")

// condSeenListSaved is False with reason SaveFailed while a blob event's record cannot be saved (ADR-0157).
const condSeenListSaved = v1.ConditionType("SeenListSaved")

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
	Clock     clock.Clock  // ADR-0182: the timer seed and the Run loop read it; default clock.System()
	// BucketRecheckInterval is eventing.bucketRecheckInterval (ADR-0163); 0 ⇒ bucketRecheckInterval.
	BucketRecheckInterval time.Duration
}

// Source is the EventSource reconciler + the registered named-event timer set. A firing PUBLISHES a named
// CloudEvent (ADR-0108); the action side (invoke a function / start a workflow, and the Invocation record)
// is the F69 Sensor (ADR-0109). The Source no longer invokes.
type Source struct {
	store     store.Store
	publisher Publisher
	blob      *BlobWatcher // ADR-0119: nil ⇒ blob sources cannot be registered
	logger    *slog.Logger
	clock     clock.Clock
	recheck   time.Duration // re-check of a blob source's Bucket

	mu     sync.Mutex
	timers map[eventKey]*timerEntry
}

// timerEntry is a registered named event's firing state: an interval event's lastFire on its creation grid
// (ADR-0182), or a cron event's timetable and next slot (ADR-0211). A cron entry has a non-nil table.
type timerEntry struct {
	interval time.Duration
	lastFire time.Time
	cron     string // a cron event: cron and zone are the unchanged-check key
	zone     string
	table    *cron.Timetable
	next     time.Time
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
	s := &Source{
		store:     d.Store,
		publisher: d.Publisher,
		blob:      d.Blob,
		logger:    logger.With("component", "eventing"),
		clock:     d.Clock,
		timers:    map[eventKey]*timerEntry{},
		recheck:   d.BucketRecheckInterval,
	}
	if s.clock == nil {
		s.clock = clock.System()
	}
	if s.recheck <= 0 {
		s.recheck = bucketRecheckInterval
	}
	if d.Blob != nil {
		d.Blob.SetHooks(WatchHooks{Exists: s.sourceExists, SaveFailing: s.setSaveFailing})
	}
	return s, nil
}

// Reconcile owns KindEventSource (ADR-0108): it registers every named event of a `timer:` source
// (→ Ready), deregisters on delete, and registers nothing for a source with no timer kind. A deleted or
// non-blob source has its blob records purged (ADR-0157).
func (s *Source) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	obj, err := s.store.Get(ctx, v1.KindEventSource.GVK(), req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			s.deregisterTimers(req.Namespace, req.Name)
			return controller.Result{}, s.purgeBlob(ctx, req.Namespace, req.Name)
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
		return controller.Result{}, s.purgeBlob(ctx, req.Namespace, req.Name)
	}
	if err := s.registerTimer(req.Namespace, req.Name, time.Time(es.CreationTime), es.Spec.Timer); err != nil {
		return controller.Result{}, err
	}
	cur, hasReady := es.Status.Conditions.Get(condReady)
	_, seenCond := es.Status.Conditions.Get(condSeenListSaved)
	if es.Status.Phase != v1.PhaseReady || !hasReady || cur.Status != v1.ConditionTrue || cur.ObservedGeneration != es.Generation || seenCond {
		es.Status.Phase = v1.PhaseReady
		es.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, ObservedGeneration: es.Generation})
		es.Status.Conditions = slices.DeleteFunc(es.Status.Conditions, func(c v1.Condition) bool { return c.Type == condSeenListSaved })
		if _, err := s.store.Update(ctx, es); err != nil {
			return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), "eventing.Reconcile", "set eventsource ready")
		}
	}
	return controller.Result{}, s.purgeBlob(ctx, req.Namespace, req.Name)
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
			return controller.Result{RequeueAfter: s.recheck}, nrErr
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), "eventing.reconcileBlob", "resolve bucket %q", es.Spec.Blob.Bucket)
	}
	s.blob.Register(ns, name, es.UID, normalizedBlob(es))
	if cur, ok := es.Status.Conditions.Get(condReady); !ok || cur.Status != v1.ConditionTrue || cur.ObservedGeneration != es.Generation || es.Status.Phase != v1.PhaseReady {
		es.Status.Phase = v1.PhaseReady
		es.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, Reason: "Watching", ObservedGeneration: es.Generation})
		if _, uerr := s.store.Update(ctx, es); uerr != nil {
			return controller.Result{}, fault.Wrapf(uerr, fault.KindOf(uerr), "eventing.reconcileBlob", "set eventsource ready")
		}
	}
	return controller.Result{RequeueAfter: s.recheck}, nil
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
	es.Status.Conditions = slices.DeleteFunc(es.Status.Conditions, func(c v1.Condition) bool { return c.Type == condSeenListSaved })
	if _, err := s.store.Update(ctx, es); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "eventing.reconcileBlob", "set eventsource not-ready")
	}
	return nil
}

// registerTimer (re)registers every named event of a timer source, keeping an entry whose schedule is unchanged
// (a frequent reconcile can't starve firing), and prunes events removed from the spec. A new or re-intervaled
// interval entry is seeded on the creation grid created + k×interval (ADR-0182), so its schedule survives a daemon
// restart; a grid point passed while the daemon was down is skipped. A new cron entry, or one whose expression,
// zone or kind changed, is seeded with the first slot strictly after now (ADR-0211 Decision 6). An event whose
// cron does not parse keeps its previous entry and its error is returned after the other events are registered.
// An event with neither cron nor a positive interval is skipped with a warning and left idle (#872).
func (s *Source) registerTimer(ns v1.NamespaceName, source v1.ObjectName, created time.Time, t *v1.TimerSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[eventKey]bool, len(t.Events))
	var errs []error
	for i := range t.Events {
		ev := &t.Events[i]
		k := eventKey{ns: ns, source: source, event: ev.Name}
		if ev.Cron != "" {
			want[k] = true
			if e, ok := s.timers[k]; ok && e.table != nil && e.cron == ev.Cron && e.zone == ev.TimeZone {
				continue // unchanged — keep its next
			}
			table, err := parseCron(ev)
			if err != nil {
				errs = append(errs, fault.Wrapf(err, fault.Invalid, "eventing.registerTimer", "event %s/%s/%s", ns, source, ev.Name))
				continue
			}
			s.timers[k] = &timerEntry{cron: ev.Cron, zone: ev.TimeZone, table: table, next: table.Next(s.clock.Now())}
			continue
		}
		interval := time.Duration(ev.Interval)
		if interval <= 0 {
			s.logger.Warn("timer event has no positive interval; skipped", "namespace", ns, "eventsource", source, "event", ev.Name, "interval", interval)
			continue
		}
		want[k] = true
		if e, ok := s.timers[k]; ok && e.table == nil && e.interval == interval {
			continue // unchanged — keep its lastFire
		}
		s.timers[k] = &timerEntry{interval: interval, lastFire: gridFloor(created, s.clock.Now(), interval)}
	}
	for k := range s.timers { // prune events dropped from the spec
		if k.ns == ns && k.source == source && !want[k] {
			delete(s.timers, k)
		}
	}
	return errors.Join(errs...)
}

// parseCron parses a cron event's expression in its zone; admission (v1alpha1.CheckCron) refuses what fails here.
func parseCron(ev *v1.TimerEvent) (*cron.Timetable, error) {
	loc, err := cron.LoadZone(ev.TimeZone)
	if err != nil {
		return nil, err
	}
	return cron.Parse(ev.Cron, loc)
}

// gridFloor returns the largest created + k×interval (k any integer) that is not after now. A zero created
// returns now: store.Create always sets CreationTime, so only a direct test call passes zero.
func gridFloor(created, now time.Time, interval time.Duration) time.Time {
	if created.IsZero() {
		return now
	}
	since := now.Sub(created)
	k := since / interval
	if since%interval < 0 {
		k-- // round toward −∞ so the seed never lies after now, also when now is before created
	}
	return created.Add(k * interval)
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

// purgeBlob deregisters a source's blob events and deletes their records (ADR-0157 Decision 5; nil watcher ⇒
// no-op). Its error is returned by Reconcile so the request is retried.
func (s *Source) purgeBlob(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) error {
	if s.blob == nil {
		return nil
	}
	return s.blob.Purge(ctx, ns, source)
}

// sourceExists is the start sweep's store lookup (ADR-0157 Decision 8).
func (s *Source) sourceExists(ctx context.Context, src SourceRef) (bool, error) {
	_, err := s.store.Get(ctx, v1.KindEventSource.GVK(), src.Namespace, src.Name)
	if err == nil {
		return true, nil
	}
	if fault.KindOf(err) == fault.NotFound {
		return false, nil
	}
	return false, err
}

// setSaveFailing sets SeenListSaved False/SaveFailed naming each failing event, or removes it when none fails
// (ADR-0157 Decision 9).
func (s *Source) setSaveFailing(ctx context.Context, src SourceRef, failing map[v1.ObjectName]error) error {
	obj, err := s.store.Get(ctx, v1.KindEventSource.GVK(), src.Namespace, src.Name)
	if err != nil {
		return err
	}
	es, ok := obj.(*v1.EventSource)
	if !ok {
		return fault.Internalf("eventing.setSaveFailing", "unexpected type %T", obj)
	}
	if len(failing) == 0 {
		if _, ok := es.Status.Conditions.Get(condSeenListSaved); !ok {
			return nil
		}
		es.Status.Conditions = slices.DeleteFunc(es.Status.Conditions, func(c v1.Condition) bool { return c.Type == condSeenListSaved })
	} else {
		msgs := make([]string, 0, len(failing))
		for _, ev := range slices.Sorted(maps.Keys(failing)) {
			msgs = append(msgs, fmt.Sprintf("%s: %v", ev, failing[ev]))
		}
		es.Status.Conditions.Set(v1.Condition{Type: condSeenListSaved, Status: v1.ConditionFalse, Reason: "SaveFailed", Message: strings.Join(msgs, "; "), ObservedGeneration: es.Generation})
	}
	if _, err := s.store.Update(ctx, es); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "eventing.setSaveFailing", "set eventsource %s condition", condSeenListSaved)
	}
	return nil
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

// Run ticks the registered named-event set until ctx is cancelled, publishing each event that is due. Started
// by the pkg/funcd lifecycle (ADR-0033); not part of the reconciler.
func (s *Source) Run(ctx context.Context) error {
	ticker := time.NewTicker(runTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			for _, k := range s.dueTimers(s.clock.Now()) {
				if err := s.Fire(ctx, k.ns, k.source, k.event); err != nil {
					s.logger.WarnContext(ctx, "timer publish failed", "eventsource", k.source, "event", k.event, "error", err)
				}
			}
		}
	}
}

// dueTimers marks and returns the named events that are due, advancing them under the lock so a fire is never
// double-counted across ticks. An interval event's lastFire advances to the latest period boundary, not to the tick
// time, so tick lateness never stretches the period; a cron event's next advances to the first slot after now.
// Either way, periods or slots missed while a publish was in flight are skipped rather than fired in a burst.
func (s *Source) dueTimers(now time.Time) []eventKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []eventKey
	for k, e := range s.timers {
		if e.table != nil {
			if !now.Before(e.next) {
				e.next = e.table.Next(now)
				due = append(due, k)
			}
			continue
		}
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
