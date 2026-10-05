package router_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
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
		Owner:     router.Owner{Kind: v1.KindRoute, Namespace: v1.NamespaceName(ns), Name: v1.ObjectName(fn)},
	}
}

func upstreamEntry(ns, name, path, upstream string) router.Entry {
	return router.Entry{
		Namespace: v1.NamespaceName(ns),
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: path, Upstream: upstream}},
		Owner:     router.Owner{Kind: v1.KindCatalogService, Namespace: v1.NamespaceName(ns), Name: v1.ObjectName(name)},
	}
}

func set(t *testing.T, a *router.Aggregator, source string, entries ...router.Entry) []router.Verdict {
	t.Helper()
	v, err := a.Set(context.Background(), source, entries)
	require.NoError(t, err)
	return v
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
	a := router.NewAggregator(p, nil, nil, nil)

	set(t, a, "routes", fnEntry("default", "echo", "/echo"))
	set(t, a, "catalog/default/lake", upstreamEntry("default", "lake", "/catalog/lake", "http://127.0.0.1:9999"))

	require.Equal(t, 2, countRules(p.last()), "both sources coexist in the union — no clobber")
}

// TestAggregator_SourceIsolation covers scenario: aggregator-source-isolation — re-Setting the routes
// source leaves the catalog source's entry intact.
func TestAggregator_SourceIsolation(t *testing.T) {
	t.Parallel()
	p := &recordingProgrammer{}
	a := router.NewAggregator(p, nil, nil, nil)

	set(t, a, "routes", fnEntry("default", "echo", "/echo"))
	set(t, a, "catalog/default/lake", upstreamEntry("default", "lake", "/catalog/lake", "http://up"))
	// the Route reconciler re-evaluates (a new Route) and re-Sets its whole slice.
	set(t, a, "routes", fnEntry("default", "echo", "/echo"), fnEntry("default", "api", "/api"))

	require.Equal(t, 3, countRules(p.last()), "the catalog entry SURVIVES a routes re-Set — its partition is untouched")
}

// TestAggregator_Remove covers scenario: aggregator-remove — Set(nil) drops exactly that source.
func TestAggregator_Remove(t *testing.T) {
	t.Parallel()
	p := &recordingProgrammer{}
	a := router.NewAggregator(p, nil, nil, nil)

	set(t, a, "routes", fnEntry("default", "echo", "/echo"))
	set(t, a, "catalog/default/lake", upstreamEntry("default", "lake", "/catalog/lake", "http://up"))
	set(t, a, "catalog/default/lake") // teardown

	require.Equal(t, 1, countRules(p.last()), "only the catalog source's entry is removed")

	// removing an unknown source is a harmless no-op that re-programs the same table.
	set(t, a, "catalog/never/existed")
	require.Equal(t, 1, countRules(p.last()))
}

// TestAggregator_ConcurrentSafe covers scenario: aggregator-concurrent-safe — many sources Setting
// concurrently never program a torn snapshot; the final table is the full union. Run with -race.
func TestAggregator_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	p := &recordingProgrammer{}
	a := router.NewAggregator(p, nil, nil, nil)

	set(t, a, "routes")
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := fmt.Sprintf("catalog/ns/c%02d", i)
			_, err := a.Set(context.Background(), src, []router.Entry{upstreamEntry("ns", fmt.Sprintf("c%02d", i), fmt.Sprintf("/c%02d", i), "http://up")})
			require.NoError(t, err)
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
	a := router.NewAggregator(p, nil, nil, nil)
	ctx := context.Background()

	_, err := a.Set(ctx, "routes", []router.Entry{fnEntry("default", "echo", "/echo")})
	require.Error(t, err, "the program error is surfaced so the reconciler retries")
	require.Equal(t, 0, p.calls(), "nothing was programmed on the failed call")

	// a subsequent Set from ANOTHER source re-programs the full union — the earlier source heals.
	set(t, a, "catalog/default/lake", upstreamEntry("default", "lake", "/catalog/lake", "http://up"))
	require.Equal(t, 2, countRules(p.last()), "the failed source's entry is re-programmed by the next Set (self-healing)")
}

// TestAggregator_Deterministic covers the no-flap property — identical desired state programs an
// identical table (entries in (namespace, name, kind rank) order), so re-reconciling doesn't churn.
func TestAggregator_Deterministic(t *testing.T) {
	t.Parallel()
	p := &recordingProgrammer{}
	a := router.NewAggregator(p, nil, nil, nil)

	set(t, a, "catalog/default/lake", upstreamEntry("default", "lake", "/catalog/lake", "http://up"))
	set(t, a, "routes", fnEntry("default", "echo", "/echo"))
	first := p.last()
	// re-Set both with identical content; the union must be byte-identical order (sorted keys).
	set(t, a, "routes", fnEntry("default", "echo", "/echo"))
	second := p.last()
	require.Equal(t, first, second, "identical desired state ⇒ identical union (no flap)")
	require.Equal(t, v1.ObjectName("echo"), first[0].Owner.Name, "entries are ordered by owner (namespace, name), not by source key")
}

// TestAggregator_EmptySourceRejected confirms an empty source key is rejected (a wiring-bug guard).
func TestAggregator_EmptySourceRejected(t *testing.T) {
	t.Parallel()
	a := router.NewAggregator(&recordingProgrammer{}, nil, nil, nil)
	_, err := a.Set(context.Background(), "", []router.Entry{fnEntry("default", "echo", "/echo")})
	require.Error(t, err)
}

// TestRouter_ResolvesUpstream covers the Upstream backend threading through compile → Resolve: an
// Internal (Upstream) entry resolves to a Match carrying the upstream + stripped prefix.
func TestRouter_ResolvesUpstream(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{
		upstreamEntry("default", "lake", "/catalog/lake", "http://10.63.0.1:34517"),
	}))
	m, ok := rtr.Resolve("any", "/catalog/lake/db", "POST")
	require.True(t, ok, "the upstream entry matches its prefix subtree")
	require.Equal(t, "http://10.63.0.1:34517", m.Upstream, "the match carries the node-private upstream")
	require.Empty(t, m.Function, "an upstream backend has no Function")
	require.Equal(t, "/catalog/lake", m.StripPrefix, "the matched prefix is stripped to address the upstream at its root")
}

func withHost(e router.Entry, host string) router.Entry {
	e.Host = host
	return e
}

// modesOf is a ModeFunc over a fixed table; a namespace outside it is NotFound (implicit).
func modesOf(m map[v1.NamespaceName]v1.ExposureMode) router.ModeFunc {
	return func(_ context.Context, ns v1.NamespaceName) (v1.ExposureMode, error) {
		if mode, ok := m[ns]; ok {
			return mode, nil
		}
		return "", fault.NotFoundf("test", "namespace %q", ns)
	}
}

// notified records the owners the aggregator asks to re-run.
type notified struct {
	mu     sync.Mutex
	owners []router.Owner
}

func (n *notified) notify(o router.Owner) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.owners = append(n.owners, o)
}

func (n *notified) take() []router.Owner {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := n.owners
	n.owners = nil
	return out
}

// TestAggregator_ValidatesBeforeCommit: an entry without a ranked owner, or with an owner in another namespace,
// and a ModeFunc error each fail the Set with nothing committed or programmed.
func TestAggregator_ValidatesBeforeCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := &recordingProgrammer{}
	var modeErr error
	a := router.NewAggregator(p, func(context.Context, v1.NamespaceName) (v1.ExposureMode, error) {
		return v1.ExposureImplicit, modeErr
	}, nil, nil)
	set(t, a, "routes", fnEntry("default", "echo", "/echo"))
	calls := p.calls()

	unranked := fnEntry("default", "x", "/x")
	unranked.Owner.Kind = v1.KindFunction
	otherNS := fnEntry("default", "y", "/y")
	otherNS.Owner.Namespace = "other"
	noOwner := fnEntry("default", "z", "/z")
	noOwner.Owner = router.Owner{}
	for _, bad := range []router.Entry{unranked, otherNS, noOwner} {
		_, err := a.Set(ctx, "routes", []router.Entry{bad})
		require.Error(t, err)
		require.Equal(t, fault.Invalid, fault.KindOf(err))
	}
	require.Equal(t, calls, p.calls(), "a refused Set programs nothing")

	modeErr = fault.Unavailablef("test", "store down")
	_, err := a.Set(ctx, "routes", []router.Entry{fnEntry("default", "hostless", "/h")})
	require.Error(t, err)
	require.Equal(t, calls, p.calls(), "a ModeFunc error programs nothing")

	modeErr = nil
	set(t, a, "catalog/default/lake", withHost(upstreamEntry("default", "lake", "/catalog/lake", "http://up"), "lake.example"))
	require.Equal(t, 2, countRules(p.last()), "the refused Sets left the routes source as it was")
}

// TestScenario_same_name_tie_route_first covers scenario: same-name-tie-route-first — Route a/x and
// CatalogService a/x on one claim: the Route wins whatever the Set order, and the catalog reports RouteConflict.
func TestScenario_same_name_tie_route_first(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	n := &notified{}
	a := router.NewAggregator(rtr, nil, n.notify, nil)
	cat := withHost(upstreamEntry("a", "x", "/q", "http://cat"), "h")
	rt := withHost(fnEntry("a", "x", "/q"), "h")

	require.Equal(t, "EdgeStarting", set(t, a, "catalog/a/x", cat)[0].Reason, "no catalog entry before the Route table loads")
	require.Empty(t, set(t, a, "routes", rt)[0].Reason, "the Route is programmed")
	require.Equal(t, []router.Owner{cat.Owner}, n.take(), "the catalog's verdict changed, so it is re-run")

	v := set(t, a, "catalog/a/x", cat)[0]
	require.Equal(t, "RouteConflict", v.Reason)
	require.Contains(t, v.Message, "Route a/x")
	m, ok := rtr.Resolve("h", "/q", "GET")
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("x"), m.Function, "the Route serves the claim")
	require.Empty(t, m.Upstream)
}

// TestScenario_reserved_prefix_refused covers scenario: reserved-prefix-refused — a host-less claim under
// /function/ (any source, any mode, any spelling of the path) and a host-less catalog at / are refused; with no
// host-less / Route, nothing in the edge table matches /function/f, so the data plane serves it by name.
func TestScenario_reserved_prefix_refused(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	a := router.NewAggregator(rtr, modesOf(map[v1.NamespaceName]v1.ExposureMode{"ex": v1.ExposureExplicit}), nil, nil)

	v := set(t, a, "routes", fnEntry("im", "rf", "/function/f"), withHost(fnEntry("im", "rh", "/function/g"), "h"))
	require.Equal(t, "ReservedPath", v[0].Reason, "a host-less Route under /function/ is refused")
	require.Empty(t, v[1].Reason, "with a host it is allowed")

	for i, c := range []struct{ ns, path string }{
		{"im", "/function/f"},
		{"im", "/function"},
		{"im", "/function/"},
		{"im", "//function/f"},
		{"im", "/"},
		{"ex", "/function/f"},
		{"ex", "/"},
	} {
		name := fmt.Sprintf("c%d", i)
		v := set(t, a, "catalog/"+c.ns+"/"+name, upstreamEntry(c.ns, name, c.path, "http://up"))
		require.Equal(t, "ReservedPath", v[0].Reason, "host-less catalog at %q in %q", c.path, c.ns)
	}
	require.Empty(t, set(t, a, "catalog/im/root", withHost(upstreamEntry("im", "root", "/", "http://up"), "h"))[0].Reason,
		"a catalog at / with a host is allowed")

	_, ok := rtr.Resolve("any", "/function/f", "POST")
	require.False(t, ok, "no edge row matches, so POST /function/f reaches Function f by name")
}

// TestScenario_catalog_vs_catalog_handover covers scenario: catalog-vs-catalog-handover — when the winning
// catalog is deleted, the loser's entry is programmed and its reconciler is re-run with no change to it.
func TestScenario_catalog_vs_catalog_handover(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	n := &notified{}
	a := router.NewAggregator(rtr, nil, n.notify, nil)
	set(t, a, "routes")
	ca := withHost(upstreamEntry("a", "c", "/q", "http://a"), "h")
	cb := withHost(upstreamEntry("b", "c", "/q", "http://b"), "h")

	require.Empty(t, set(t, a, "catalog/a/c", ca)[0].Reason)
	v := set(t, a, "catalog/b/c", cb)[0]
	require.Equal(t, "RouteConflict", v.Reason)
	require.Contains(t, v.Message, "CatalogService a/c")
	n.take()

	set(t, a, "catalog/a/c")
	require.Equal(t, []router.Owner{cb.Owner}, n.take(), "b/c is re-run without another change to it")
	m, ok := rtr.Resolve("h", "/q", "GET")
	require.True(t, ok)
	require.Equal(t, "http://b", m.Upstream, "b/c's entry is programmed")
	require.Empty(t, set(t, a, "catalog/b/c", cb)[0].Reason, "the re-run reports IngressReady=True")
}

// restartClaims are the claims of the ADR-0176 scenarios, one namespace pair per scenario.
func restartClaims() map[string][]router.Entry {
	src := map[string][]router.Entry{"routes": {
		withHost(fnEntry("a1", "r", "/q"), "h1"),
		withHost(fnEntry("b2", "r", "/q"), "h2"),
		withHost(fnEntry("a3", "x", "/q"), "h3"),
		fnEntry("im", "rf", "/function/f"),
	}}
	for _, e := range []router.Entry{
		withHost(upstreamEntry("b1", "c", "/q", "http://b1"), "h1"),
		withHost(upstreamEntry("a2", "c", "/q", "http://a2"), "h2"),
		withHost(upstreamEntry("a3", "x", "/q", "http://a3"), "h3"),
		withHost(upstreamEntry("a4", "c", "/q", "http://a4"), "h4"),
		withHost(upstreamEntry("b4", "c", "/q", "http://b4"), "h4"),
		upstreamEntry("ex", "c", "/q", "http://ex"),
		upstreamEntry("im", "cf", "/function/f", "http://cf"),
		upstreamEntry("im", "root", "/", "http://root"),
	} {
		src["catalog/"+string(e.Owner.Namespace)+"/"+string(e.Owner.Name)] = []router.Entry{e}
	}
	return src
}

// boot Sets every source in order, checking that no catalog entry is programmed before "routes" has Set, then
// re-runs every source once, as NotifyFunc would, and returns the settled verdicts and the programmed table.
func boot(t *testing.T, claims map[string][]router.Entry, order []string) (map[router.Owner]router.Verdict, []router.Entry) {
	t.Helper()
	p := &recordingProgrammer{}
	n := &notified{}
	a := router.NewAggregator(p, modesOf(map[v1.NamespaceName]v1.ExposureMode{"ex": v1.ExposureExplicit}), n.notify, nil)
	routesSet, held := false, false
	for _, src := range order {
		v := set(t, a, src, claims[src]...)
		if src == "routes" {
			routesSet = true
			if held {
				require.NotEmpty(t, n.take(), "the held catalogs are re-run once the Route table loads")
			}
			continue
		}
		held = held || !routesSet
		if !routesSet {
			require.Contains(t, []string{"EdgeStarting", "HostRequired", "ReservedPath"}, v[0].Reason)
			for _, e := range p.last() {
				require.NotEqual(t, v1.KindCatalogService, e.Owner.Kind, "no catalog entry is programmed before the Route table loads")
			}
		}
	}
	got := map[router.Owner]router.Verdict{}
	for _, src := range order {
		for _, v := range set(t, a, src, claims[src]...) {
			got[v.Owner] = v
		}
	}
	return got, p.last()
}

// TestScenario_verdict_restart_stable covers scenario: verdict-restart-stable — the sources Set in the reverse
// order after a restart reach the same verdicts and the same programmed table.
func TestScenario_verdict_restart_stable(t *testing.T) {
	t.Parallel()
	claims := restartClaims()
	order := make([]string, 0, len(claims))
	order = append(order, "routes")
	for src := range claims {
		if src != "routes" {
			order = append(order, src)
		}
	}
	sort.Strings(order[1:])
	before, beforeTable := boot(t, claims, order)

	reversed := make([]string, len(order))
	for i, src := range order {
		reversed[len(order)-1-i] = src
	}
	after, afterTable := boot(t, claims, reversed)

	require.Equal(t, before, after, "every verdict is the same after the restart")
	require.Equal(t, beforeTable, afterTable, "the programmed table is the same after the restart")

	reason := func(kind v1.Kind, ns, name string) string {
		return before[router.Owner{Kind: kind, Namespace: v1.NamespaceName(ns), Name: v1.ObjectName(name)}].Reason
	}
	require.Empty(t, reason(v1.KindRoute, "a1", "r"))
	require.Equal(t, "RouteConflict", reason(v1.KindCatalogService, "b1", "c"))
	require.Empty(t, reason(v1.KindCatalogService, "a2", "c"))
	require.Equal(t, "RouteConflict", reason(v1.KindRoute, "b2", "r"))
	require.Empty(t, reason(v1.KindRoute, "a3", "x"))
	require.Equal(t, "RouteConflict", reason(v1.KindCatalogService, "a3", "x"))
	require.Empty(t, reason(v1.KindCatalogService, "a4", "c"))
	require.Equal(t, "RouteConflict", reason(v1.KindCatalogService, "b4", "c"))
	require.Equal(t, "HostRequired", reason(v1.KindCatalogService, "ex", "c"))
	require.Equal(t, "ReservedPath", reason(v1.KindRoute, "im", "rf"))
	require.Equal(t, "ReservedPath", reason(v1.KindCatalogService, "im", "cf"))
	require.Equal(t, "ReservedPath", reason(v1.KindCatalogService, "im", "root"))
}

// "/api" and "/api/" Prefix claims serve the same subtree, so two namespaces claiming them conflict.
func TestIssue701_TrailingSlashPrefixClaimsSamePath(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	a := router.NewAggregator(rtr, nil, nil, nil)

	v := set(t, a, "routes", fnEntry("a", "x", "/api"), fnEntry("b", "y", "/api/"))
	require.Empty(t, v[0].Reason, "a/x wins the lexical tie-break")
	require.Equal(t, "RouteConflict", v[1].Reason, "/api/ claims the same subtree as /api")
	m, ok := rtr.Resolve("any", "/api/users", "GET")
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("x"), m.Function)
}

// TestAggregator_HostSpellingsAreOneClaim: a host written with a port, in another letter case or with a trailing
// dot is the same claim as its plain spelling, so another namespace cannot take the host by respelling it.
func TestAggregator_HostSpellingsAreOneClaim(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{"shop.test:8081", "SHOP.TEST", "shop.test."} {
		t.Run(spelling, func(t *testing.T) {
			t.Parallel()
			rtr := router.New()
			a := router.NewAggregator(rtr, modesOf(map[v1.NamespaceName]v1.ExposureMode{"a": v1.ExposureExplicit, "b": v1.ExposureExplicit}), nil, nil)

			v := set(t, a, "routes", withHost(fnEntry("a", "shop", "/"), "shop.test"), withHost(fnEntry("b", "evil", "/"), spelling))
			require.Empty(t, v[0].Reason, "the owner keeps its claim")
			require.Equal(t, "RouteConflict", v[1].Reason, "%q is the owner's host in another spelling", spelling)
			require.Equal(t, []string{"shop.test"}, rtr.Hosts())
		})
	}
}
