package sensor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing"
)

// Retry-queue tuning (ADR-0118). These are the operational-retry defaults — short, since each Invoke
// attempt already blocks on a cold-start wake (ADR-0033); the backoff only absorbs a transient 5xx/503.
const (
	retryBaseDelay = 100 * time.Millisecond
	retryMaxDelay  = 10 * time.Second
)

// errDropped is what enqueue returns past the shutdown deadline once the drop count was logged (ADR-0156 §7).
var errDropped = errors.New("sensor: delivery dropped after shutdown")

// delivery is an action-delivery unit on the Sensor's bounded delivery queue. It carries the
// dependency's source/event tuple (so a DeadLetter is built from the unit, never by re-parsing the
// CloudEvent) and the firing CloudEvent to (re-)deliver.
type delivery struct {
	ns      v1.NamespaceName
	rg      v1.ResourceGroupName
	sensor  v1.ObjectName
	source  v1.ObjectName
	event   v1.ObjectName
	action  v1.Action
	ce      eventing.CloudEvent
	firedAt time.Time // the firing time, so the terminal Invocation's StartTime is the first attempt
}

// actionTarget is what the per-target cap counts (ADR-0156 §4), shared across Sensors. Its zero value names
// the withdrawn entry: the FIFO of deliveries a Sensor change released, parked without an attempt.
type actionTarget struct {
	ns   v1.NamespaceName
	kind v1.Kind // KindFunction or KindWorkflow
	name v1.ObjectName
}

func targetOf(d delivery) actionTarget {
	if d.action.Workflow != "" {
		return actionTarget{ns: d.ns, kind: v1.KindWorkflow, name: d.action.Workflow}
	}
	return actionTarget{ns: d.ns, kind: v1.KindFunction, name: d.action.Function}
}

// unitState is the one state of a delivery unit (ADR-0156 §3); it changes only under retryQueue.mu.
type unitState int

const (
	unitReady unitState = iota
	unitInFlight
	unitBackingOff
	unitWithdrawn
)

// unit is one live delivery. slot is the entry whose slot an in-flight unit holds (zero = the withdrawn
// entry); stale marks an in-flight unit its Sensor change withdrew.
type unit struct {
	d        delivery
	attempts int
	target   actionTarget
	slot     actionTarget
	state    unitState
	stale    bool
}

// pending is what get hands out and shutDown returns; withdrawn: park without an attempt, sensor-changed Reason.
type pending struct {
	id        string
	d         delivery
	attempts  int
	withdrawn bool
}

// lane is one ring entry: its FIFO of queued ids and its slots in use. An id that left the queued state stays
// in the FIFO until get skips it.
type lane struct {
	fifo     []string
	inFlight int
	inRing   bool
}

// retryQueue is the bounded delivery queue every attempt runs on (ADR-0156, superseding ADR-0118's inline
// attempt 1). Per-key exponential backoff (base·2^(attempts-1), capped) mirrors the ADR-0015 controller
// workqueue, which cannot be reused: it is controller.Request-keyed and informer-driven. A ring of lanes
// gives each target at most maxInFlightPerTarget attempts at once, and a Sensor holds at most
// maxQueuedPerSensor live units.
type retryQueue struct {
	mu   sync.Mutex
	cond *sync.Cond

	units   map[string]*unit
	lanes   map[actionTarget]*lane
	ring    []actionTarget // lanes with a queued id and a free slot
	sensors map[sensorKey]map[string]struct{}

	base, max            time.Duration
	maxInFlightPerTarget int
	maxQueuedPerSensor   int

	shuttingDown  bool
	deadline      time.Time
	dropped       int
	droppedLogged bool
}

func newRetryQueue(base, maxDelay time.Duration, maxInFlightPerTarget, maxQueuedPerSensor int) *retryQueue {
	if base <= 0 {
		base = retryBaseDelay
	}
	if maxDelay <= 0 {
		maxDelay = retryMaxDelay
	}
	if maxInFlightPerTarget < 1 {
		maxInFlightPerTarget = defaultMaxInFlightPerTarget
	}
	if maxQueuedPerSensor < 1 {
		maxQueuedPerSensor = defaultMaxQueuedPerSensor
	}
	q := &retryQueue{
		units:                map[string]*unit{},
		lanes:                map[actionTarget]*lane{},
		sensors:              map[sensorKey]map[string]struct{}{},
		base:                 base,
		max:                  maxDelay,
		maxInFlightPerTarget: maxInFlightPerTarget,
		maxQueuedPerSensor:   maxQueuedPerSensor,
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func fullReason(d delivery, bound int) error {
	return fault.ResourceExhaustedf(op, "delivery queue of sensor %s/%s is full (%d deliveries pending, the bound set by eventing.maxQueuedPerSensor); not attempted", d.ns, d.sensor, bound)
}

func changedReason(d delivery) error {
	return fault.Conflictf(op, "sensor %s/%s changed with the delivery still queued; replay sends it to the current spec", d.ns, d.sensor)
}

func shutdownReason() error {
	return fault.Unavailablef(op, "daemon shut down with the delivery still queued")
}

// enqueue makes d ready for attempt 1, or keeps nothing and returns the park reason (Sensor full, or shut down
// before the deadline). Past the deadline it counts d dropped and returns nil, or errDropped once takeDropped ran.
func (q *retryQueue) enqueue(id string, d delivery) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shuttingDown {
		switch {
		case time.Now().Before(q.deadline):
			return shutdownReason()
		case q.droppedLogged:
			return errDropped
		}
		q.dropped++
		return nil
	}
	k := sensorKey{d.ns, d.sensor}
	ids := q.sensors[k]
	if len(ids) >= q.maxQueuedPerSensor {
		return fullReason(d, q.maxQueuedPerSensor)
	}
	if ids == nil {
		ids = map[string]struct{}{}
		q.sensors[k] = ids
	}
	ids[id] = struct{}{}
	u := &unit{d: d, target: targetOf(d), state: unitReady}
	q.units[id] = u
	q.push(u.target, id)
	return nil
}

// takeDropped returns the deliveries counted dropped past the deadline and marks the count logged.
func (q *retryQueue) takeDropped() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.droppedLogged = true
	return q.dropped
}

// get blocks until a lane in the ring has a queued id, takes that id's slot and hands it out, or returns
// shutdown once the queue is shut down: only the attempts already in flight finish (ADR-0118 §6).
func (q *retryQueue) get() (p pending, shutdown bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if q.shuttingDown {
			return pending{}, true
		}
		for len(q.ring) > 0 {
			t := q.ring[0]
			q.ring = q.ring[1:]
			l := q.lanes[t]
			l.inRing = false
			if p, ok := q.take(t, l); ok {
				return p, false
			}
			q.offer(t)
		}
		q.ring = nil
		q.cond.Wait()
	}
}

// take pops t's FIFO up to its first id still queued there and moves that unit in flight on t's slot.
func (q *retryQueue) take(t actionTarget, l *lane) (pending, bool) {
	want := unitReady
	if t == (actionTarget{}) {
		want = unitWithdrawn
	}
	for len(l.fifo) > 0 {
		id := l.fifo[0]
		l.fifo = l.fifo[1:]
		u := q.units[id]
		if u == nil || u.state != want {
			continue
		}
		u.state = unitInFlight
		u.slot = t
		l.inFlight++
		q.offer(t)
		return pending{id: id, d: u.d, attempts: u.attempts, withdrawn: want == unitWithdrawn}, true
	}
	l.fifo = nil
	return pending{}, false
}

func (q *retryQueue) push(t actionTarget, id string) {
	l := q.lanes[t]
	if l == nil {
		l = &lane{}
		q.lanes[t] = l
	}
	l.fifo = append(l.fifo, id)
	q.offer(t)
}

// offer adds t to the ring when it has a queued id and a free slot, and drops a lane left with nothing.
func (q *retryQueue) offer(t actionTarget) {
	l := q.lanes[t]
	if l == nil || l.inRing {
		return
	}
	if len(l.fifo) > 0 && l.inFlight < q.maxInFlightPerTarget {
		l.inRing = true
		q.ring = append(q.ring, t)
		q.cond.Signal()
		return
	}
	if len(l.fifo) == 0 && l.inFlight == 0 {
		delete(q.lanes, t)
	}
}

func (q *retryQueue) release(u *unit) {
	if l := q.lanes[u.slot]; l != nil {
		l.inFlight--
		q.offer(u.slot)
	}
}

func (q *retryQueue) leave(k sensorKey, id string) {
	ids := q.sensors[k]
	delete(ids, id)
	if len(ids) == 0 {
		delete(q.sensors, k)
	}
}

// reschedule frees an in-flight unit's slot and arms its next attempt after the per-key backoff
// (base·2^(attempts-1), capped), on a timer off the worker. It reports ok = false, leaving the unit in flight
// for the caller to park and forget, when shut down or stale.
func (q *retryQueue) reschedule(id string, attempts int) (ok, stale bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	u := q.units[id]
	switch {
	case u == nil || u.state != unitInFlight:
		return false, false
	case u.stale:
		return false, true
	case q.shuttingDown:
		return false, false
	}
	q.release(u)
	u.attempts = attempts
	u.state = unitBackingOff
	time.AfterFunc(q.backoff(attempts), func() { q.ready(id) })
	return true, false
}

// ready makes a backing-off unit ready (its backoff elapsed); on any other state it does nothing.
func (q *retryQueue) ready(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	u := q.units[id]
	if u == nil || u.state != unitBackingOff {
		return
	}
	u.state = unitReady
	q.push(u.target, id)
}

func (q *retryQueue) backoff(attempts int) time.Duration {
	d := q.base
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= q.max {
			return q.max
		}
	}
	return d
}

// forget frees the slot named by the unit's slot and removes the unit (every terminal path of an in-flight unit).
func (q *retryQueue) forget(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	u := q.units[id]
	if u == nil {
		return
	}
	if u.state == unitInFlight {
		q.release(u)
	}
	delete(q.units, id)
	q.leave(sensorKey{u.d.ns, u.d.sensor}, id)
}

// withdraw selects k's units whose action keep rejects (keep nil ⇒ all): it moves the selected ready and
// backing-off units to withdrawn, marks the selected in-flight units stale, and drops every selected unit from
// k's id set (ADR-0156 §8). The caller holds Reconciler.mu.
func (q *retryQueue) withdraw(k sensorKey, keep func(v1.Action) bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for id := range q.sensors[k] {
		u := q.units[id]
		if keep != nil && keep(u.d.action) {
			continue
		}
		switch u.state {
		case unitReady, unitBackingOff:
			u.state = unitWithdrawn
			q.push(actionTarget{}, id)
		case unitInFlight:
			u.stale = true
		case unitWithdrawn:
		}
		q.leave(k, id)
	}
}

// shutDown stops handing out units and returns every unit not in flight, removed from the queue; an enqueue
// before deadline is then refused with the shutdown Reason and one after it counted dropped (ADR-0156 §7).
func (q *retryQueue) shutDown(deadline time.Time) []pending {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shuttingDown = true
	q.deadline = deadline
	var out []pending
	for id, u := range q.units {
		if u.state == unitInFlight {
			continue
		}
		out = append(out, pending{id: id, d: u.d, attempts: u.attempts, withdrawn: u.state == unitWithdrawn})
		delete(q.units, id)
		q.leave(sensorKey{u.d.ns, u.d.sensor}, id)
	}
	for t, l := range q.lanes {
		l.fifo, l.inRing = nil, false
		if l.inFlight == 0 {
			delete(q.lanes, t)
		}
	}
	q.ring = nil
	q.cond.Broadcast()
	return out
}

// RunRetryWorkers starts the delivery workers and blocks until ctx is cancelled. It then shuts the queue down,
// parks the deliveries still queued until the drain bound passes and drops the rest with one count log, and
// waits for the attempts in flight (ADR-0156 §7). An attempt still in flight at the bound is cancelled, so
// shutdown keeps its bound (ADR-0028). Wire it as a pkg/funcd background goroutine. A nil DeadLetters store
// means dead-lettering is off: delivery stays inline and there is no queue to run.
func (r *Reconciler) RunRetryWorkers(ctx context.Context, drain time.Duration) {
	if r.deadletters == nil {
		<-ctx.Done()
		return
	}
	// The workers run on a context shutdown does not cancel: an attempt in flight must finish, and so must its
	// dead-letter and Invocation writes (ADR-0118 §6), unless the drain bound passes first.
	attemptCtx, cut := context.WithCancel(context.WithoutCancel(ctx))
	defer cut()
	var wg sync.WaitGroup
	for range r.maxDeliveriesInFlight {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.retryWorker(attemptCtx)
		}()
	}
	<-ctx.Done()
	deadline := time.Now().Add(drain)
	queued := r.retry.shutDown(deadline)
	bound := time.AfterFunc(drain, func() {
		r.logger.WarnContext(ctx, "sensor retry drain reached its bound, cancelling the attempts in flight", "bound", drain)
		cut()
	})
	defer bound.Stop()
	dropped := r.parkQueued(ctx, queued, deadline)
	wg.Wait()
	if n := dropped + r.retry.takeDropped(); n > 0 {
		r.logger.WarnContext(ctx, "sensor deliveries dropped at shutdown, the drain bound passed with them queued", "count", n)
	}
}

// parkQueued parks the deliveries shutDown returned until the deadline and returns how many it left unparked.
func (r *Reconciler) parkQueued(ctx context.Context, queued []pending, deadline time.Time) int {
	for i, p := range queued {
		if !time.Now().Before(deadline) {
			return len(queued) - i
		}
		reason := shutdownReason()
		if p.withdrawn {
			reason = changedReason(p.d)
		}
		r.park(ctx, p.d, p.attempts, reason)
	}
	return 0
}

// retryWorker runs the next attempt of each unit it gets, or parks a withdrawn one, until the queue shuts down.
func (r *Reconciler) retryWorker(ctx context.Context) {
	for {
		p, shutdown := r.retry.get()
		if shutdown {
			return
		}
		if p.withdrawn {
			r.park(ctx, p.d, p.attempts, changedReason(p.d))
			r.retry.forget(p.id)
			continue
		}
		r.attemptDelivery(ctx, p.id, p.d, p.attempts+1)
	}
}
