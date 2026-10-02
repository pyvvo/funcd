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
	"strconv"
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
	return &Controller{
		store:       d.Store,
		logger:      logger.With("component", "controller"),
		workers:     workers,
		reconcilers: map[v1.GroupVersionKind]Reconciler{},
		mappers:     map[v1.GroupVersionKind][]MapFunc{},
		queue:       newQueue(baseBackoff, maxBackoff),
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
// the highest resourceVersion seen, or re-lists when the store no longer retains it.
func (c *Controller) watch(ctx context.Context, gvk v1.GroupVersionKind, w store.Watch) {
	var seen uint64
	for {
		seen = c.forward(ctx, gvk, w, seen)
		w.Stop()
		if ctx.Err() != nil {
			return
		}
		c.logger.WarnContext(ctx, "store closed the watch, re-watching",
			"kind", gvk.Kind, "resourceVersion", seen)
		if w = c.rewatch(ctx, gvk, seen); w == nil {
			return
		}
	}
}

// forward enqueues every event of w until its stream closes or ctx ends, and
// returns the highest object resourceVersion seen.
func (c *Controller) forward(ctx context.Context, gvk v1.GroupVersionKind, w store.Watch, seen uint64) uint64 {
	for {
		select {
		case <-ctx.Done():
			return seen
		case ev, ok := <-w.ResultChan():
			if !ok {
				return seen
			}
			meta := ev.Object.GetObjectMeta()
			if rv, err := strconv.ParseUint(meta.ResourceVersion, 10, 64); err == nil && rv > seen {
				seen = rv
			}
			if _, ok := c.reconcilers[gvk]; ok {
				c.queue.Add(Request{GVK: gvk, Namespace: meta.Namespace, Name: meta.Name})
			}
			for _, mapFn := range c.mappers[gvk] {
				for _, req := range mapFn(ctx, ev.Object) {
					c.queue.Add(req)
				}
			}
		}
	}
}

// rewatch opens a Watch that replays the changes after resourceVersion seen,
// falling back to a full re-list when that revision is too old (fault.Unavailable),
// and retries other failures with backoff. It returns nil once ctx ends.
func (c *Controller) rewatch(ctx context.Context, gvk v1.GroupVersionKind, seen uint64) store.Watch {
	opts := store.WatchOptions{SinceResourceVersion: strconv.FormatUint(seen, 10)}
	for failures := 1; ; failures++ {
		w, err := c.store.Watch(ctx, gvk, opts)
		if err == nil {
			return w
		}
		if opts.SinceResourceVersion != "" && fault.KindOf(err) == fault.Unavailable {
			opts.SinceResourceVersion = ""
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		c.logger.WarnContext(ctx, "re-watch failed, retrying", "kind", gvk.Kind, "error", err)
		select {
		case <-ctx.Done():
			return nil
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
