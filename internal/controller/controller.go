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

// Reconciler is the per-kind logic a feature contributes. It must be idempotent
// (the queue is at-least-once). A delete surfaces as fault.NotFound from the store.
type Reconciler interface {
	Reconcile(ctx context.Context, req Request) (Result, error)
}

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
		queue:       newQueue(baseBackoff, maxBackoff),
	}, nil
}

// Register binds a Reconciler to a gvk. Call before Run.
func (c *Controller) Register(gvk v1.GroupVersionKind, r Reconciler) {
	c.reconcilers[gvk] = r
}

// Run opens a store Watch per registered gvk (the informer), starts the workers,
// blocks until ctx is cancelled, then drains the watches and workers — no leak.
func (c *Controller) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	watches := make([]store.Watch, 0, len(c.reconcilers))

	for gvk := range c.reconcilers {
		w, err := c.store.Watch(ctx, gvk, store.WatchOptions{})
		if err != nil {
			for _, sw := range watches {
				sw.Stop()
			}
			if ctx.Err() != nil {
				return nil // ctx cancelled during setup — graceful shutdown, not a failure
			}
			return fault.Wrapf(err, fault.Unavailable, "controller.Run", "watch %s", gvk.Kind)
		}
		watches = append(watches, w)
		wg.Add(1)
		go func(gvk v1.GroupVersionKind, w store.Watch) {
			defer wg.Done()
			c.watch(ctx, gvk, w)
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
	for _, w := range watches {
		w.Stop()
	}
	wg.Wait()
	return nil
}

// watch enqueues a Request for every change on the gvk's watch stream (Added /
// Modified / Deleted, uniformly).
func (c *Controller) watch(ctx context.Context, gvk v1.GroupVersionKind, w store.Watch) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.ResultChan():
			if !ok {
				return
			}
			meta := ev.Object.GetObjectMeta()
			c.queue.Add(Request{GVK: gvk, Namespace: meta.Namespace, Name: meta.Name})
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
