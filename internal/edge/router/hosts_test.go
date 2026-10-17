package router_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge/router"
)

func TestNamespaceHost(t *testing.T) {
	t.Parallel()
	require.Equal(t, "staging.localhost", router.NamespaceHost("staging", ""))
	require.Equal(t, "staging.apps.example.com", router.NamespaceHost("staging", "apps.example.com"))
	require.Empty(t, router.NamespaceHost("default", ""), "default has no namespace host")
}

func TestNamespaceOfHost(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]v1.NamespaceName{
		"staging.localhost":          "staging",
		"api.staging.localhost":      "staging",
		"STAGING.localhost:8081":     "staging",
		"staging.localhost.":         "staging",
		"localhost":                  "",
		"default.localhost":          "",
		"a_b.localhost":              "",
		"staging.example.com":        "",
		"xlocalhost":                 "",
		"staging.apps.example.com":   "",
		"127.0.0.1":                  "",
		"[::1]:8081":                 "",
		"fleet.staging.localhost:80": "staging",
	} {
		require.Equal(t, want, router.NamespaceOfHost(host, ""), host)
	}
	require.Equal(t, v1.NamespaceName("staging"), router.NamespaceOfHost("staging.apps.example.com", "apps.example.com"))
	require.Empty(t, router.NamespaceOfHost("staging.localhost", "apps.example.com"))
}

// Decision 3: a host-less entry of a non-default namespace claims its namespace host; default's stays host-less, and
// the verdict carries the claimed host.
func TestAggregator_HostlessEntryClaimsNamespaceHost(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	a := router.NewAggregator(rtr, nil, nil, "apps.example.com", nil)

	v := set(t, a, "routes", fnEntry("default", "d", "/api"), fnEntry("prod", "p", "/api"), fnEntry("staging", "s", "/api"),
		withHost(fnEntry("staging", "x", "/x"), "Fleet.Example.com"))
	require.Equal(t, []string{"", "prod.apps.example.com", "staging.apps.example.com", "fleet.example.com"},
		[]string{v[0].Host, v[1].Host, v[2].Host, v[3].Host})
	for _, x := range v {
		require.Empty(t, x.Reason, "%s/%s", x.Owner.Namespace, x.Owner.Name)
	}
	require.Equal(t, []string{"fleet.example.com", "prod.apps.example.com", "staging.apps.example.com"}, rtr.Hosts())

	for host, want := range map[string]v1.NamespaceName{
		"staging.apps.example.com": "staging",
		"prod.apps.example.com":    "prod",
		"apps.example.com":         "default",
		"other.apps.example.com":   "default",
		"anything.test":            "default",
	} {
		m, ok := rtr.Resolve(host, "/api", "GET")
		require.True(t, ok, host)
		require.Equal(t, want, m.Namespace, host)
	}
}

// Decision 4: while a namespace serves on its host, another namespace's claim on it or under it is HostReserved,
// whatever the (namespace, name) order, and default's host-less entries do not answer there. The reservation ends
// with the namespace's last entry there.
func TestAggregator_ReservationStartsAndEnds(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	n := &notified{}
	a := router.NewAggregator(rtr, nil, n.notify, "", nil)
	aaa := withHost(fnEntry("aaa", "r", "/"), "staging.localhost")
	under := withHost(fnEntry("aaa", "u", "/"), "api.staging.localhost")
	def := withHost(fnEntry("default", "d", "/"), "x.staging.localhost")

	v := set(t, a, "routes", aaa, under, def, fnEntry("default", "root", "/"), fnEntry("staging", "s", "/api"))
	for i := range 3 {
		require.Equal(t, "HostReserved", v[i].Reason, "%s/%s", v[i].Owner.Namespace, v[i].Owner.Name)
		require.Contains(t, v[i].Message, `namespace "staging"`)
	}
	require.Empty(t, v[3].Reason)
	require.Empty(t, v[4].Reason)

	_, ok := rtr.Resolve("staging.localhost", "/", "GET")
	require.False(t, ok, "default's host-less / does not answer on staging's reserved host")
	_, ok = rtr.Resolve("x.staging.localhost", "/", "GET")
	require.False(t, ok, "nor on a name under it")
	m, ok := rtr.Resolve("staging.localhost", "/api/v2", "GET")
	require.True(t, ok)
	require.Equal(t, v1.NamespaceName("staging"), m.Namespace)
	m, ok = rtr.Resolve("prod.localhost", "/", "GET")
	require.True(t, ok, "an unreserved namespace host is an ordinary host")
	require.Equal(t, v1.NamespaceName("default"), m.Namespace)

	v = set(t, a, "routes", aaa, under, def, fnEntry("default", "root", "/"))
	for i := range 4 {
		require.Empty(t, v[i].Reason, "%s/%s is programmed once staging stops serving", v[i].Owner.Namespace, v[i].Owner.Name)
	}
	m, ok = rtr.Resolve("staging.localhost", "/", "GET")
	require.True(t, ok)
	require.Equal(t, v1.NamespaceName("aaa"), m.Namespace)
}

// A reservation by one source turns another source's claim HostReserved, and the loser is re-run when it ends.
func TestAggregator_ReservationAcrossSources(t *testing.T) {
	t.Parallel()
	n := &notified{}
	a := router.NewAggregator(router.New(), nil, n.notify, "", nil)
	cat := withHost(upstreamEntry("prod", "c", "/q", "http://c"), "staging.localhost")
	set(t, a, "routes")
	require.Empty(t, set(t, a, "catalog/prod/c", cat)[0].Reason)

	set(t, a, "routes", fnEntry("staging", "s", "/api"))
	require.Equal(t, []router.Owner{cat.Owner}, n.take())
	require.Equal(t, "HostReserved", set(t, a, "catalog/prod/c", cat)[0].Reason)

	set(t, a, "routes")
	require.Equal(t, []router.Owner{cat.Owner}, n.take())
	require.Empty(t, set(t, a, "catalog/prod/c", cat)[0].Reason)
}

// Decision 6: on a host that names a namespace, another namespace's rule under /function is HostReserved, reserved
// host or not; the namespace's own rule there is allowed.
func TestAggregator_FunctionPathOnNamespaceHost(t *testing.T) {
	t.Parallel()
	a := router.NewAggregator(router.New(), nil, nil, "", nil)
	v := set(t, a, "routes",
		withHost(fnEntry("prod", "root", "/"), "staging.localhost"),
		withHost(fnEntry("prod", "fn", "/function/fleet-api"), "staging.localhost"),
		withHost(fnEntry("default", "fn", "//function"), "api.staging.localhost"))
	require.Empty(t, v[0].Reason, "staging serves nothing there, so / is an ordinary claim")
	require.Equal(t, "HostReserved", v[1].Reason)
	require.Contains(t, v[1].Message, `belongs to namespace "staging"`)
	require.Equal(t, "HostReserved", v[2].Reason)
	require.Equal(t, "staging.localhost", v[1].Host, "a refused verdict carries its claimed host too")

	v = set(t, a, "routes", withHost(fnEntry("staging", "own", "/function/x"), "staging.localhost"))
	require.Empty(t, v[0].Reason, "a namespace may claim /function/ on its own host")
}

// Decision 5: an explicit namespace's host-less entry is served on its namespace host; an explicit default's is
// still HostRequired.
func TestAggregator_ExplicitNamespaceHostlessEntry(t *testing.T) {
	t.Parallel()
	a := router.NewAggregator(router.New(), modesOf(map[v1.NamespaceName]v1.ExposureMode{
		"staging": v1.ExposureExplicit, "default": v1.ExposureExplicit,
	}), nil, "", nil)
	v := set(t, a, "routes", fnEntry("default", "d", "/api"), fnEntry("staging", "s", "/api"))
	require.Equal(t, "HostRequired", v[0].Reason)
	require.Empty(t, v[1].Reason)
	require.Equal(t, "staging.localhost", v[1].Host)
}
