package router_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge/router"
)

// recordingProgrammer records every snapshot it is Programmed with (a deep copy) and can be told to
// fail the next N calls. Safe for concurrent use — the aggregator serializes, but -race checks it.
type recordingProgrammer struct {
	mu        sync.Mutex
	snapshots [][]router.Entry
	failNext  int
	failErr   error
}

func (p *recordingProgrammer) Program(_ context.Context, entries []router.Entry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failNext > 0 {
		p.failNext--
		return p.failErr
	}
	cp := make([]router.Entry, len(entries))
	copy(cp, entries)
	p.snapshots = append(p.snapshots, cp)
	return nil
}

func (p *recordingProgrammer) last() []router.Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.snapshots) == 0 {
		return nil
	}
	return p.snapshots[len(p.snapshots)-1]
}

func (p *recordingProgrammer) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.snapshots)
}

func fnEntry(ns, fn, path string) router.Entry {
	return router.Entry{
		Namespace: v1.NamespaceName(ns),
		Rules:     []router.CompiledRule{{Path: path, Function: v1.ObjectName(fn)}},
	}
}

func upstreamEntry(ns, path, upstream string) router.Entry {
	return router.Entry{
		Namespace: v1.NamespaceName(ns),
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: path, Upstream: upstream}},
	}
}

func countRules(entries []router.Entry) int {
	n := 0
	for _, e := range entries {
		n += len(e.Rules)
	}
	return n
}

// TestAggregator_RoutesCoexist covers scenario: routes-coexist — the user-Route source and the
// catalog source are both present in the programmed union (the replace-all clobber is gone).
func TestAggregator_RoutesCoexist(t *testing.T) {
	t.Parallel()
	p := &recordingProgrammer{}
	a := router.NewAggregator(p, nil)

	require.NoError(t, a.Set(context.Background(), "routes", []router.Entry{fnEntry("default", "echo", "/echo")}))
	require.NoError(t, a.Set(context.Background(), "catalog/default/lake", []router.Entry{upstreamEntry("default", "/catalog/lake", "http://127.0.0.1:9999")}))

	require.Equal(t, 2, countRules(p.last()), "both sources coexist in the union — no clobber")
}

// TestAggregator_SourceIsolation covers scenario: aggregator-source-isolation — re-Setting the routes
// source leaves the catalog source's entry intact.
func TestAggregator_SourceIsolation(t *testing.T) {
	t.Parallel()
	p := &recordingProgrammer{}
	a := router.NewAggregator(p, nil)
	ctx := context.Background()

	require.NoError(t, a.Set(ctx, "routes", []router.Entry{fnEntry("default", "echo", "/echo")}))
	require.NoError(t, a.Set(ctx, "catalog/default/lake", []router.Entry{upstreamEntry("default", "/catalog/lake", "http://up")}))
	// the Route reconciler re-evaluates (a new Route) and re-Sets its whole slice.
	require.NoError(t, a.Set(ctx, "routes", []router.Entry{fnEntry("default", "echo", "/echo"), fnEntry("default", "api", "/api")}))

	require.Equal(t, 3, countRules(p.last()), "the catalog entry SURVIVES a routes re-Set — its partition is untouched")
}

// TestAggregator_Remove covers scenario: aggregator-remove — Set(nil) drops exactly that source.
func TestAggregator_Remove(t *testing.T) {
	t.Parallel()
	p := &recordingProgrammer{}
	a := router.NewAggregator(p, nil)
	ctx := context.Background()

	require.NoError(t, a.Set(ctx, "routes", []router.Entry{fnEntry("default", "echo", "/echo")}))
	require.NoError(t, a.Set(ctx, "catalog/default/lake", []router.Entry{upstreamEntry("default", "/catalog/lake", "http://up")}))
	require.NoError(t, a.Set(ctx, "catalog/default/lake", nil)) // teardown

	require.Equal(t, 1, countRules(p.last()), "only the catalog source's entry is removed")

	// removing an unknown source is a harmless no-op that re-programs the same table.
	require.NoError(t, a.Set(ctx, "catalog/never/existed", nil))
	require.Equal(t, 1, countRules(p.last()))
}

// TestAggregator_ConcurrentSafe covers scenario: aggregator-concurrent-safe — many sources Setting
// concurrently never program a torn snapshot; the final table is the full union. Run with -race.
func TestAggregator_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	p := &recordingProgrammer{}
	a := router.NewAggregator(p, nil)

	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := fmt.Sprintf("catalog/ns/c%02d", i)
			require.NoError(t, a.Set(context.Background(), src, []router.Entry{upstreamEntry("ns", fmt.Sprintf("/c%02d", i), "http://up")}))
		}(i)
	}
	wg.Wait()

	require.Equal(t, n, countRules(p.last()), "the final union has every source's entry")
}

// TestAggregator_ProgramFailureAtomic covers scenario: aggregator-program-failure-atomic — a failed
// Program leaves the source map committed, so the next Set re-programs the FULL union.
func TestAggregator_ProgramFailureAtomic(t *testing.T) {
	t.Parallel()
	p := &recordingProgrammer{failNext: 1, failErr: fmt.Errorf("router down")}
	a := router.NewAggregator(p, nil)
	ctx := context.Background()

	err := a.Set(ctx, "routes", []router.Entry{fnEntry("default", "echo", "/echo")})
	require.Error(t, err, "the program error is surfaced so the reconciler retries")
	require.Equal(t, 0, p.calls(), "nothing was programmed on the failed call")

	// a subsequent Set from ANOTHER source re-programs the full union — the earlier source heals.
	require.NoError(t, a.Set(ctx, "catalog/default/lake", []router.Entry{upstreamEntry("default", "/catalog/lake", "http://up")}))
	require.Equal(t, 2, countRules(p.last()), "the failed source's entry is re-programmed by the next Set (self-healing)")
}

// TestAggregator_Deterministic covers the no-flap property — identical desired state programs an
// identical union (sources concatenated in sorted key order), so re-reconciling doesn't churn.
func TestAggregator_Deterministic(t *testing.T) {
	t.Parallel()
	p := &recordingProgrammer{}
	a := router.NewAggregator(p, nil)
	ctx := context.Background()

	require.NoError(t, a.Set(ctx, "catalog/default/lake", []router.Entry{upstreamEntry("default", "/catalog/lake", "http://up")}))
	require.NoError(t, a.Set(ctx, "routes", []router.Entry{fnEntry("default", "echo", "/echo")}))
	first := p.last()
	// re-Set both with identical content; the union must be byte-identical order (sorted keys).
	require.NoError(t, a.Set(ctx, "routes", []router.Entry{fnEntry("default", "echo", "/echo")}))
	second := p.last()
	require.Equal(t, first, second, "identical desired state ⇒ identical union (no flap)")
	// "catalog/..." sorts before "routes", so the catalog entry is first.
	require.Equal(t, "http://up", first[0].Rules[0].Upstream, "sources are ordered by sorted key")
}

// TestAggregator_EmptySourceRejected confirms an empty source key is rejected (a wiring-bug guard).
func TestAggregator_EmptySourceRejected(t *testing.T) {
	t.Parallel()
	a := router.NewAggregator(&recordingProgrammer{}, nil)
	require.Error(t, a.Set(context.Background(), "", []router.Entry{fnEntry("default", "echo", "/echo")}))
}

// TestRouter_ResolvesUpstream covers the Upstream backend threading through compile → Resolve: an
// Internal (Upstream) entry resolves to a Match carrying the upstream + stripped prefix.
func TestRouter_ResolvesUpstream(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{
		upstreamEntry("default", "/catalog/lake", "http://10.63.0.1:34517"),
	}))
	m, ok := rtr.Resolve("any", "/catalog/lake/db", "POST")
	require.True(t, ok, "the upstream entry matches its prefix subtree")
	require.Equal(t, "http://10.63.0.1:34517", m.Upstream, "the match carries the node-private upstream")
	require.Empty(t, m.Function, "an upstream backend has no Function")
	require.Equal(t, "/catalog/lake", m.StripPrefix, "the matched prefix is stripped to address the upstream at its root")
}
