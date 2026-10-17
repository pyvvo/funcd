package router_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge/router"
)

func ownedEntry(e router.Entry) router.Entry {
	e.Owned = true
	return e
}

func enabled(e router.Entry, on bool) router.Entry {
	e.Enabled = &on
	return e
}

// ADR-0241 Decision 6: Disabled is ruled first, claims nothing, still reserves its namespace host and reports its host.
func TestAggregator_DisabledClaimsNothingAndReserves(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	a := router.NewAggregator(rtr, nil, nil, "", nil)
	v := set(t, a, "routes",
		enabled(fnEntry("staging", "off", "/function/x"), false),
		withHost(fnEntry("prod", "take", "/x"), "staging.localhost"),
		withHost(fnEntry("prod", "same", "/function/x"), "other.test"))
	require.Equal(t, "Disabled", v[0].Reason, "ruled before every other rule")
	require.Equal(t, "staging.localhost", v[0].Host)
	require.Equal(t, "HostReserved", v[1].Reason, "a Disabled entry still reserves its namespace host")
	require.Empty(t, v[2].Reason)
	_, ok := rtr.Resolve("staging.localhost", "/function/x", "POST")
	require.False(t, ok, "a Disabled entry is not programmed")
}

// An owned Route with enabled unset is Disabled in an explicit namespace, whatever its host; a set enabled wins, and a
// non-Route source is never Disabled by the mode.
func TestAggregator_OwnedRouteFollowsTheMode(t *testing.T) {
	t.Parallel()
	a := router.NewAggregator(router.New(), modesOf(map[v1.NamespaceName]v1.ExposureMode{"ex": v1.ExposureExplicit}), nil, "", nil)
	v := set(t, a, "routes",
		ownedEntry(withHost(fnEntry("ex", "a", "/a"), "a.test")),
		enabled(ownedEntry(fnEntry("ex", "b", "/b")), true),
		withHost(fnEntry("ex", "c", "/c"), "c.test"))
	require.Equal(t, "Disabled", v[0].Reason)
	require.Empty(t, v[1].Reason)
	require.Empty(t, v[2].Reason, "a Route that is not owned is not Disabled by the mode")
	set(t, a, "routes")
	require.Empty(t, set(t, a, "catalog/ex/c", upstreamEntry("ex", "c", "/q", "http://up"))[0].Reason)
}

// ADR-0241 Decision 7: within a namespace every entry that is not owned wins an equal claim over an owned one and a
// non-Route source, whatever the names; across namespaces the namespace order stands.
func TestAggregator_YourRoutesWin(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	a := router.NewAggregator(rtr, nil, nil, "", nil)
	site := ownedEntry(withHost(fnEntry("staging", "aaa", "/"), "fleet.test"))
	yours := withHost(fnEntry("staging", "zz-app", "/"), "fleet.test")
	other := withHost(fnEntry("zzz", "a", "/"), "fleet.test")
	v := set(t, a, "routes", site, yours, other)
	require.Equal(t, "RouteConflict", v[0].Reason)
	require.Contains(t, v[0].Message, "Route staging/zz-app")
	require.Empty(t, v[1].Reason)
	require.Equal(t, "RouteConflict", v[2].Reason, "a later namespace still loses")

	cat := withHost(upstreamEntry("staging", "aaa", "/q", "http://up"), "fleet.test")
	set(t, a, "routes", withHost(fnEntry("staging", "zzz", "/q"), "fleet.test"))
	require.Equal(t, "RouteConflict", set(t, a, "catalog/staging/aaa", cat)[0].Reason, "a non-Route source is ruled as owned")
}

// ADR-0241 Decision 8: a rule's stance beats its Route's.
func TestRouter_RuleStanceBeatsRoute(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	e := fnEntry("staging", "app", "/")
	e.Auth = v1.AuthOpen
	e.Rules = append(e.Rules, router.CompiledRule{Path: "/api", Function: "api", Auth: v1.AuthAuthenticated})
	require.NoError(t, rtr.Program(t.Context(), []router.Entry{e}, nil))
	m, ok := rtr.Resolve("any", "/api/x", "GET")
	require.True(t, ok)
	require.Equal(t, v1.AuthAuthenticated, m.Auth)
	require.Equal(t, "/api", m.Path)
	m, ok = rtr.Resolve("any", "/page", "GET")
	require.True(t, ok)
	require.Equal(t, v1.AuthOpen, m.Auth, "a rule without a stance takes its Route's")
}
