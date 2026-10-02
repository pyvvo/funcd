package sensor

import (
	"context"
	"sync"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing"
)

// Retry-queue tuning (ADR-0118). These are the operational-retry defaults — short, since each Invoke
// attempt already blocks on a cold-start wake (ADR-0033); the backoff only absorbs a transient 5xx/503.
const (
	retryBaseDelay = 100 * time.Millisecond
	retryMaxDelay  = 10 * time.Second
	retryWorkers   = 2
)

// delivery is a retryable action-delivery unit on the Sensor's rate-limited retry queue. It carries the
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

// retryQueue is a small rate-limited, worker-drained queue over in-memory delivery units. It MIRRORS THE
// SHAPE of the ADR-0015 controller workqueue (internal/controller/queue.go) — per-key exponential backoff
// (base·2^(attempts-1), capped), the per-key count IS the attempt counter — but is a distinct
// re-implementation, NOT reuse: ADR-0015's queue is unexported, controller.Request-keyed and informer-driven
// off store.Watch, so it cannot be instantiated for delivery units that aren't stored resources (there is
// nothing to watch). Each firing is an independent unit keyed by a unique id — no cross-firing dedup.
type retryQueue struct {
	mu   sync.Mutex
	cond *sync.Cond

	units    map[string]delivery // id → the retryable unit (payload)
	attempts map[string]int      // id → attempts already made (the per-key backoff/attempt counter)
	order    []string            // FIFO of ids ready for get (a backoff timer has elapsed)

	base, max    time.Duration
	shuttingDown bool
}

func newRetryQueue(base, maxDelay time.Duration) *retryQueue {
	if base <= 0 {
		base = retryBaseDelay
	}
	if maxDelay <= 0 {
		maxDelay = retryMaxDelay
	}
	q := &retryQueue{
		units:    map[string]delivery{},
		attempts: map[string]int{},
		base:     base,
		max:      maxDelay,
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// reschedule records that `attempts` deliveries have been made for id and schedules the next attempt after
// the per-key backoff (base·2^(attempts-1), capped). A real timer, off the caller's goroutine (never an
// in-callback sleep that would block the Fanout).
func (q *retryQueue) reschedule(id string, d delivery, attempts int) {
	q.mu.Lock()
	if q.shuttingDown {
		q.mu.Unlock()
		return
	}
	q.units[id] = d
	q.attempts[id] = attempts
	delay := q.backoff(attempts)
	q.mu.Unlock()
	time.AfterFunc(delay, func() { q.ready(id) })
}

// ready marks id available for a worker (its backoff has elapsed), unless it was forgotten or the queue is
// shutting down.
func (q *retryQueue) ready(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shuttingDown {
		return
	}
	if _, ok := q.units[id]; !ok {
		return // forgotten (delivered/dead-lettered) before the timer fired
	}
	q.order = append(q.order, id)
	q.cond.Signal()
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

// get blocks until a unit's backoff has elapsed (returns it with the attempts already made) or the queue is
// shut down (shutdown=true). A unit still queued at shutdown is not handed out: only the attempts already in
// flight finish (ADR-0118 §6).
func (q *retryQueue) get() (id string, d delivery, attempts int, shutdown bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.order) == 0 && !q.shuttingDown {
		q.cond.Wait()
	}
	if q.shuttingDown {
		return "", delivery{}, 0, true
	}
	id = q.order[0]
	q.order = q.order[1:]
	return id, q.units[id], q.attempts[id], false
}

// forget drops a unit's state (called on a terminal outcome — delivered or dead-lettered).
func (q *retryQueue) forget(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.units, id)
	delete(q.attempts, id)
}

// shutDown stops the queue; get then returns shutdown for all workers (pending backoff timers no-op).
func (q *retryQueue) shutDown() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shuttingDown = true
	q.cond.Broadcast()
}

// RunRetryWorkers starts the delivery retry workers and blocks until ctx is cancelled, then shuts down the
// queue and waits for in-flight attempts to drain (a graceful drain narrows — does not close — the
// in-memory-retry crash window; ADR-0118 Temporary workarounds). The drain lasts at most drain: an attempt
// still in flight then is cancelled, so shutdown keeps its bound (ADR-0028). Wire it as a pkg/funcd background
// goroutine. A nil DeadLetters store means dead-lettering is off, so there is no retry loop to run.
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
	for i := 0; i < retryWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.retryWorker(attemptCtx)
		}()
	}
	<-ctx.Done()
	r.retry.shutDown()
	bound := time.AfterFunc(drain, func() {
		r.logger.WarnContext(ctx, "sensor retry drain reached its bound, cancelling the attempts in flight", "bound", drain)
		cut()
	})
	defer bound.Stop()
	wg.Wait()
}

// retryWorker pulls due delivery units and performs the next attempt until the queue is shut down.
func (r *Reconciler) retryWorker(ctx context.Context) {
	for {
		id, d, made, shutdown := r.retry.get()
		if shutdown {
			return
		}
		r.attemptDelivery(ctx, id, d, made+1) // made+1 = the attempt this worker performs
	}
}
