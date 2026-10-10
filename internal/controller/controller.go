// Package controller is the one general reconcile engine (ADR-0015): a store-watch
// informer feeds a rate-limited, deduplicating workqueue that worker goroutines
// drain by calling each kind's registered Reconciler — with exponential-backoff
// retry, RequeueAfter, status write-back (the reconciler's), and clean shutdown.
// Every control-plane feature contributes only a Reconciler; the engine owns the
// watch/queue/retry/worker machinery. k8s-free (the workqueue is hand-written).
package controller

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// Request identifies the object to reconcile; it is also the workqueue key
// (comparable).
type Request struct {
	GVK       v1.GroupVersionKind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}

// Result tells the engine whether/when to re-reconcile.
type Result struct {
	Requeue      bool
	RequeueAfter time.Duration
}

// SupervisionPeriod is the steady-state requeue of a reconciler that owns running instances (ADR-0142):
// while they should be running it returns Result{RequeueAfter: SupervisionPeriod}, so the next pass
// checks them and replaces one that died without a store write.
const SupervisionPeriod = 10 * time.Second

// Reconciler is the per-kind logic a feature contributes. It must be idempotent
// (the queue is at-least-once). A delete surfaces as fault.NotFound from the store.
type Reconciler interface {
	Reconcile(ctx context.Context, req Request) (Result, error)
}

// MapFunc maps a changed object to the Requests of the objects whose reconcile reads it, such as a kind
// whose status is derived from it. A watch event carries no prior state, so the mapping must also cover
// what the object may have referenced before the change.
type MapFunc func(ctx context.Context, obj v1.Object) []Request

// Deps configures the engine (internal component, ADR-0002 §1).
type Deps struct {
	Store   store.Store
	Logger  *slog.Logger
	Workers int // number of worker goroutines; <1 defaults to 1
	// RetryBackoffMax caps the failed-reconcile and re-Watch retry (controller.retryBackoffMax, ADR-0163); 0 ⇒
	// maxBackoff.
	RetryBackoffMax time.Duration
}

// Controller is the reconcile engine.
type Controller struct {
	store       store.Store
	logger      *slog.Logger
	workers     int
	reconcilers map[v1.GroupVersionKind]Reconciler
	mappers     map[v1.GroupVersionKind][]MapFunc
	queue       *queue
}

const (
	baseBackoff = 5 * time.Millisecond
	maxBackoff  = 1 * time.Second
)

// New builds the engine. Store is required.
func New(d Deps) (*Controller, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("controller.New", "store is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	workers := d.Workers
	if workers < 1 {
		workers = 1
	}
	retryMax := d.RetryBackoffMax
	if retryMax <= 0 {
		retryMax = maxBackoff
	}
	return &Controller{
		store:       d.Store,
		logger:      logger.With("component", "controller"),
		workers:     workers,
		reconcilers: map[v1.GroupVersionKind]Reconciler{},
		mappers:     map[v1.GroupVersionKind][]MapFunc{},
		queue:       newQueue(baseBackoff, retryMax),
	}, nil
}

// Register binds a Reconciler to a gvk. Call before Run.
func (c *Controller) Register(gvk v1.GroupVersionKind, r Reconciler) {
	c.reconcilers[gvk] = r
}

// Watches enqueues mapFn's Requests on every change of a gvk, besides the gvk's own Reconciler if one is
// registered. Call before Run.
func (c *Controller) Watches(gvk v1.GroupVersionKind, mapFn MapFunc) {
	c.mappers[gvk] = append(c.mappers[gvk], mapFn)
}

// Enqueue adds req to the workqueue without blocking, as a store event would (deduplicated, re-queued on Done if
// in process), for a change no watch event carries: an edge verdict that another source's Set moved (ADR-0176), a
// run the workflow engine's goroutine advanced (ADR-0146). Safe from any goroutine, before Run, and a no-op after
// shutdown.
func (c *Controller) Enqueue(req Request) {
	c.queue.Add(req)
}

// Run opens a store Watch per registered or watched gvk (the informer), starts the workers,
// blocks until ctx is cancelled, then drains the watches and workers — no leak.
func (c *Controller) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	watchCtx, stopWatches := context.WithCancel(ctx)
	defer stopWatches()

	gvks := map[v1.GroupVersionKind]bool{}
	for gvk := range c.reconcilers {
		gvks[gvk] = true
	}
	for gvk := range c.mappers {
		gvks[gvk] = true
	}
	for gvk := range gvks {
		w, err := c.store.Watch(watchCtx, gvk, store.WatchOptions{})
		if err != nil {
			stopWatches()
			if ctx.Err() != nil {
				return nil // ctx cancelled during setup — graceful shutdown, not a failure
			}
			return fault.Wrapf(err, fault.Unavailable, "controller.Run", "watch %s", gvk.Kind)
		}
		wg.Add(1)
		go func(gvk v1.GroupVersionKind, w store.Watch) {
			defer wg.Done()
			c.watch(watchCtx, gvk, w)
		}(gvk, w)
	}

	for range c.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.worker(ctx)
		}()
	}

	<-ctx.Done()
	c.queue.ShutDown()
	wg.Wait()
	return nil
}

// watch enqueues the Requests for every change on the gvk's watch stream (Added /
// Modified / Deleted, uniformly). The store closes the stream of a watcher that
// falls behind (ADR-0006), so a close before ctx ends re-watches: it resumes after
// the resourceVersion seen last (store.ResumePoint), or re-lists when the store no
// longer retains it or it is of another timeline (ADR-0202).
// A re-list reports only the objects that still exist, so it also enqueues the
// Requests each object seen before it last drove: a delete lost in the gap still
// reaches its reconcile as NotFound.
func (c *Controller) watch(ctx context.Context, gvk v1.GroupVersionKind, w store.Watch) {
	var seen store.Version
	known := map[Request][]Request{}
	for {
		seen = c.forward(ctx, gvk, w, seen, known)
		w.Stop()
		if ctx.Err() != nil {
			return
		}
		c.logger.WarnContext(ctx, "store closed the watch, re-watching",
			"kind", gvk.Kind, "resourceVersion", seen.String())
		var relisted bool
		if w, relisted = c.rewatch(ctx, gvk, seen); w == nil {
			return
		}
		if relisted {
			for _, reqs := range known {
				for _, req := range reqs {
					c.queue.Add(req)
				}
			}
			clear(known)
		}
	}
}

// forward enqueues the Requests of every event of w until its stream closes or ctx
// ends, records in known the Requests each existing object drove, and returns the
// resume point after the object resourceVersions seen.
func (c *Controller) forward(ctx context.Context, gvk v1.GroupVersionKind, w store.Watch, seen store.Version, known map[Request][]Request) store.Version {
	_, reconciled := c.reconcilers[gvk]
	for {
		select {
		case <-ctx.Done():
			return seen
		case ev, ok := <-w.ResultChan():
			if !ok {
				return seen
			}
			meta := ev.Object.GetObjectMeta()
			if v, err := store.ParseVersion(meta.ResourceVersion); err == nil {
				seen = store.ResumePoint(seen, v)
			}
			key := Request{GVK: gvk, Namespace: meta.Namespace, Name: meta.Name}
			var reqs []Request
			if reconciled {
				reqs = append(reqs, key)
			}
			for _, mapFn := range c.mappers[gvk] {
				reqs = append(reqs, mapFn(ctx, ev.Object)...)
			}
			for _, req := range reqs {
				c.queue.Add(req)
			}
			if ev.Type == store.Deleted {
				delete(known, key)
			} else {
				known[key] = reqs
			}
		}
	}
}

// rewatch opens a Watch that replays the changes after resourceVersion seen,
// falling back to a full re-list when none was seen or the store cannot replay
// from it (fault.Unavailable), and retries other failures with backoff. It reports
// whether the Watch is a re-list, and returns a nil Watch once ctx ends.
func (c *Controller) rewatch(ctx context.Context, gvk v1.GroupVersionKind, seen store.Version) (store.Watch, bool) {
	var opts store.WatchOptions
	if seen != (store.Version{}) {
		opts.SinceResourceVersion = seen.String()
	}
	for failures := 1; ; failures++ {
		w, err := c.store.Watch(ctx, gvk, opts)
		if err == nil {
			return w, opts.SinceResourceVersion == ""
		}
		if opts.SinceResourceVersion != "" && fault.KindOf(err) == fault.Unavailable {
			opts.SinceResourceVersion = ""
			continue
		}
		if ctx.Err() != nil {
			return nil, false
		}
		c.logger.WarnContext(ctx, "re-watch failed, retrying", "kind", gvk.Kind, "error", err)
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(c.queue.backoff(failures)):
		}
	}
}

func (c *Controller) worker(ctx context.Context) {
	for {
		key, shutdown := c.queue.Get()
		if shutdown {
			return
		}
		c.reconcile(ctx, key)
		c.queue.Done(key)
	}
}

func (c *Controller) reconcile(ctx context.Context, key Request) {
	r, ok := c.reconcilers[key.GVK]
	if !ok {
		c.queue.Forget(key)
		return
	}
	res, err := r.Reconcile(ctx, key)
	switch {
	case err != nil:
		c.logger.WarnContext(ctx, "reconcile failed, requeueing",
			"kind", key.GVK.Kind, "namespace", string(key.Namespace), "name", string(key.Name), "error", err)
		c.queue.AddRateLimited(key)
	case res.RequeueAfter > 0:
		c.queue.Forget(key)
		c.queue.AddAfter(key, res.RequeueAfter)
	case res.Requeue:
		c.queue.Forget(key)
		c.queue.Add(key)
	default:
		c.queue.Forget(key)
	}
}
