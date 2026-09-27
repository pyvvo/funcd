package router

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
)

// Programmer is the replace-all sink the Aggregator drives — the edge Router's Program. Router
// satisfies it; the aggregator depends only on this narrow method.
type Programmer interface {
	Program(ctx context.Context, entries []Entry) error
}

// EntrySetter contributes ONE named source's edge entries to a shared edge table (ADR-0138). Router's
// Program is replace-all, so multiple independent route sources — the Route reconciler (user Routes)
// and the CatalogService reconciler (its node-private catalog::query PEP proxy) — would clobber each
// other if each called Program directly. They call Set instead. Set replaces THIS source's slice
// (nil/empty clears it); the implementation unions all sources and programs the table exactly once.
type EntrySetter interface {
	Set(ctx context.Context, source string, entries []Entry) error
}

// Aggregator is the single sole-writer of a Router's replace-all edge table (ADR-0138): it merges
// per-source Entry slices into one union and programs it atomically. It implements EntrySetter. Safe
// for concurrent use. This is the "aggregation seam" that lets a second edge source (an exposed
// catalog) coexist with user Routes instead of clobbering them.
type Aggregator struct {
	p   Programmer
	log *slog.Logger

	mu      sync.Mutex
	sources map[string][]Entry // source key → that source's entries
}

// NewAggregator builds an Aggregator over the edge Router's replace-all Program. log nil ⇒ Default.
func NewAggregator(p Programmer, log *slog.Logger) *Aggregator {
	if log == nil {
		log = slog.Default()
	}
	return &Aggregator{
		p:       p,
		log:     log.With("component", "edge.router.aggregator"),
		sources: make(map[string][]Entry),
	}
}

// Set replaces source's entries (nil/empty clears it), recomputes the union across all sources, and
// programs it in a single Router.Program call — all under the lock, so the router only ever sees a
// self-consistent full snapshot. Sources are concatenated in sorted key order (deterministic; the
// Router itself re-sorts rows longest-path-first, so cross-source order does not affect matching). On
// a Program error the source map is left updated (committed) so the next Set — from this or any other
// source — re-programs the full union: idempotent, self-healing recovery with no permanent route
// loss. The error is still returned so the caller retries its own reconcile.
func (a *Aggregator) Set(ctx context.Context, source string, entries []Entry) error {
	const op = "edge.router.aggregator.Set"
	if source == "" {
		return fault.Invalidf(op, "route source key must not be empty")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if len(entries) == 0 {
		delete(a.sources, source)
	} else {
		cp := make([]Entry, len(entries)) // defensive copy — the caller must not mutate our state
		copy(cp, entries)
		a.sources[source] = cp
	}

	union := a.unionLocked()
	if err := a.p.Program(ctx, union); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "program aggregated edge table (%d entries, source %q)", len(union), source)
	}
	return nil
}

// unionLocked concatenates every source's entries in sorted key order. The caller must hold a.mu.
func (a *Aggregator) unionLocked() []Entry {
	keys := make([]string, 0, len(a.sources))
	for k := range a.sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	union := make([]Entry, 0)
	for _, k := range keys {
		union = append(union, a.sources[k]...)
	}
	return union
}
