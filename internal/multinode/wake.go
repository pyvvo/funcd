package multinode

import (
	"context"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// routeRetry paces the router after a failed read of the change log, which first wakes every node so none misses a change.
const routeRetry = 100 * time.Millisecond

// wakes holds the signal of each node whose feed waits. A write closes only the signals of the nodes it concerns (#1586),
// so it costs one read of the change log, not one for each node that waits.
type wakes struct {
	mu    sync.Mutex
	nodes map[v1.ObjectName]chan struct{}
}

// of is closed at the next change that concerns node.
func (w *wakes) of(node v1.ObjectName) <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch, ok := w.nodes[node]
	if !ok {
		ch = make(chan struct{})
		w.nodes[node] = ch
	}
	return ch
}

func (w *wakes) wake(node v1.ObjectName) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ch, ok := w.nodes[node]; ok {
		close(ch)
		delete(w.nodes, node)
	}
}

func (w *wakes) wakeAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for node, ch := range w.nodes {
		close(ch)
		delete(w.nodes, node)
	}
}

// startRouting reads the change log's head, then routes each later record to the nodes it concerns until ctx ends. Run
// reads the head before the node API serves, so no record that a feed has not read goes unrouted.
func (s *Handler) startRouting(ctx context.Context) error {
	head, err := s.cl.Head(ctx)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "multinode.route", "read the change log's head")
	}
	go s.route(ctx, head)
	return nil
}

// route wakes the nodes that each record after cursor concerns, as Changes keeps them for a node: a record of a namespace
// wakes the node it is placed on unless that node wrote it, a Namespace or a kind that every node follows wakes them all,
// and any other cluster-scoped record wakes none. A failed read, and a change log that compacted past cursor or started
// again below it after a restore, wake every node.
func (s *Handler) route(ctx context.Context, cursor uint64) {
	all := func(store.Change) bool { return true }
	for ctx.Err() == nil {
		signal := s.cl.ChangeSignal()
		out, next, err := s.cl.Changes(ctx, cursor, feedLimit, all)
		switch {
		case fault.KindOf(err) == fault.PreconditionFailed:
			s.wakes.wakeAll()
			if head, herr := s.cl.Head(ctx); herr == nil {
				cursor = head
			}
			continue
		case err != nil:
			s.wakes.wakeAll()
			if ctx.Err() == nil {
				s.cfg.Logger.WarnContext(ctx, "node API: read the change log to wake the nodes", "error", err)
			}
			wait(ctx, routeRetry)
			continue
		}
		for _, c := range out {
			s.wakeFor(ctx, c)
		}
		cursor = max(cursor, next)
		if len(out) > 0 {
			continue
		}
		if head, herr := s.cl.Head(ctx); herr == nil && head < cursor {
			s.wakes.wakeAll()
			cursor = head
			continue
		}
		select {
		case <-signal:
		case <-ctx.Done():
		}
	}
}

func (s *Handler) wakeFor(ctx context.Context, c store.Change) {
	switch {
	case c.Kind == v1.KindNamespace || nodeKind(c.Kind):
		s.wakes.wakeAll()
	case c.Namespace == "":
	default:
		node, err := s.cfg.Placement.NodeOf(ctx, c.Namespace)
		switch {
		case err != nil:
			s.wakes.wakeAll()
		case node != "" && node != c.Origin:
			s.wakes.wake(node)
		}
	}
}

func wait(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
