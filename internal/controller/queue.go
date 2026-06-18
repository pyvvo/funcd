package controller

import (
	"sync"
	"time"
)

// queue is the rate-limited, deduplicating, delaying workqueue keyed by Request
// (ADR-0015 §3). It guarantees no two workers process the same key concurrently:
// a key added while it is being processed is re-queued on Done. AddRateLimited
// applies per-key exponential backoff; Forget resets a key's failure count.
type queue struct {
	mu   sync.Mutex
	cond *sync.Cond

	dirty      map[Request]struct{} // queued (waiting to be processed)
	processing map[Request]struct{} // currently being processed
	order      []Request            // FIFO of keys ready for Get
	failures   map[Request]int      // per-key backoff failure count

	base, max    time.Duration
	shuttingDown bool
}

func newQueue(base, maxDelay time.Duration) *queue {
	q := &queue{
		dirty:      map[Request]struct{}{},
		processing: map[Request]struct{}{},
		failures:   map[Request]int{},
		base:       base,
		max:        maxDelay,
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Add enqueues key (deduplicated).
func (q *queue) Add(key Request) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.addLocked(key)
}

func (q *queue) addLocked(key Request) {
	if q.shuttingDown {
		return
	}
	if _, ok := q.dirty[key]; ok {
		return // already queued
	}
	q.dirty[key] = struct{}{}
	if _, ok := q.processing[key]; ok {
		return // being processed; it will be re-queued on Done
	}
	q.order = append(q.order, key)
	q.cond.Signal()
}

// AddAfter enqueues key after delay (a real timer; ADR-0015 §3).
func (q *queue) AddAfter(key Request, delay time.Duration) {
	if delay <= 0 {
		q.Add(key)
		return
	}
	time.AfterFunc(delay, func() { q.Add(key) })
}

// AddRateLimited enqueues key after an exponential backoff (base·2^(failures-1),
// capped at max) computed from its accumulated failure count.
func (q *queue) AddRateLimited(key Request) {
	q.mu.Lock()
	q.failures[key]++
	n := q.failures[key]
	q.mu.Unlock()
	q.AddAfter(key, q.backoff(n))
}

func (q *queue) backoff(failures int) time.Duration {
	d := q.base
	for i := 1; i < failures; i++ {
		d *= 2
		if d >= q.max {
			return q.max
		}
	}
	return d
}

// Get blocks until a key is available (returns it, marking it processing) or the
// queue is shut down (returns shutdown=true).
func (q *queue) Get() (key Request, shutdown bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.order) == 0 && !q.shuttingDown {
		q.cond.Wait()
	}
	if len(q.order) == 0 {
		return Request{}, true
	}
	key = q.order[0]
	q.order = q.order[1:]
	q.processing[key] = struct{}{}
	delete(q.dirty, key)
	return key, false
}

// Done marks key processed; if it was re-added while processing, it is re-queued.
func (q *queue) Done(key Request) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.processing, key)
	if _, ok := q.dirty[key]; ok {
		q.order = append(q.order, key)
		q.cond.Signal()
	}
}

// Forget resets key's backoff failure count (call on success/requeue).
func (q *queue) Forget(key Request) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.failures, key)
}

// Len reports the number of keys ready for Get.
func (q *queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.order)
}

// ShutDown stops the queue; Get then returns shutdown for all callers.
func (q *queue) ShutDown() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shuttingDown = true
	q.cond.Broadcast()
}
