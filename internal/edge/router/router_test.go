package router_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge/router"
)

func prog(t *testing.T, entries ...router.Entry) router.Router {
	t.Helper()
	r := router.New()
	require.NoError(t, r.Program(context.Background(), entries))
	return r
}

func rule(path string, exact bool, fn string, methods ...string) router.CompiledRule {
	var m map[string]bool
	if len(methods) > 0 {
		m = map[string]bool{}
		for _, x := range methods {
			m[x] = true
		}
	}
	return router.CompiledRule{Path: path, Exact: exact, Methods: m, Function: v1.ObjectName(fn)}
}

// scenario: match-exact-vs-prefix
func TestScenarioMatchExactVsPrefix(t *testing.T) {
	r := prog(t, router.Entry{Namespace: "default", Rules: []router.CompiledRule{
		rule("/orders", false, "orders-fn"),
	}})
	for _, p := range []string{"/orders", "/orders/x", "/orders/x/y"} {
		m, ok := r.Resolve("any", p, "GET")
		require.True(t, ok, p)
		require.Equal(t, v1.ObjectName("orders-fn"), m.Function)
	}
	_, ok := r.Resolve("any", "/orders-2", "GET")
	require.False(t, ok, "prefix must be segment-aware, not string-prefix")

	ex := prog(t, router.Entry{Namespace: "default", Rules: []router.CompiledRule{rule("/orders", true, "orders-fn")}})
	_, ok = ex.Resolve("any", "/orders", "GET")
	require.True(t, ok)
	_, ok = ex.Resolve("any", "/orders/x", "GET")
	require.False(t, ok, "exact must not match sub-paths")
}

// ADR-0120 (F82): a "/" Prefix rule is a catch-all subtree, and a static backend is carried through
// Program → Resolve into the Match (nil for a function backend).
func TestStaticRootCatchAllAndBackendCarried(t *testing.T) {
	back := &v1.StaticBackend{Bucket: "reports", Prefix: "bi/", Index: "index.html", SPA: true}
	r := prog(t, router.Entry{Namespace: "analytics", Host: "bi.example.com", Rules: []router.CompiledRule{
		{Path: "/", Exact: false, Static: back},
	}})
	for _, p := range []string{"/", "/index.html", "/img/logo.png", "/dashboard/orders"} {
		m, ok := r.Resolve("bi.example.com", p, "GET")
		require.True(t, ok, "the root static route catches %q", p)
		require.NotNil(t, m.Static, "the static backend is carried into the Match for %q", p)
		require.Equal(t, v1.ObjectName("reports"), m.Static.Bucket)
	}
	// A function match carries a nil Static.
	rf := prog(t, router.Entry{Namespace: "default", Rules: []router.CompiledRule{rule("/orders", false, "orders-fn")}})
	m, ok := rf.Resolve("any", "/orders", "GET")
	require.True(t, ok)
	require.Nil(t, m.Static)
}

// scenario: match-host
func TestScenarioMatchHost(t *testing.T) {
	r := prog(t,
		router.Entry{Namespace: "a", Host: "a.com", Rules: []router.CompiledRule{rule("/", false, "a-fn")}},
		router.Entry{Namespace: "b", Host: "b.com", Rules: []router.CompiledRule{rule("/", false, "b-fn")}},
	)
	m, ok := r.Resolve("a.com", "/", "GET")
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("a-fn"), m.Function)
	require.Equal(t, v1.NamespaceName("a"), m.Namespace)
	m, ok = r.Resolve("b.com", "/", "GET")
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("b-fn"), m.Function)
	_, ok = r.Resolve("c.com", "/", "GET")
	require.False(t, ok, "unknown host must not match a host-qualified route")
}

// scenario: match-method
func TestScenarioMatchMethod(t *testing.T) {
	r := prog(t, router.Entry{Namespace: "default", Rules: []router.CompiledRule{rule("/x", false, "x-fn", "GET")}})
	_, ok := r.Resolve("any", "/x", "GET")
	require.True(t, ok)
	_, ok = r.Resolve("any", "/x", "POST")
	require.False(t, ok, "a method not in the rule set must not match")
}

// scenario: match-longest-prefix
func TestScenarioMatchLongestPrefix(t *testing.T) {
	r := prog(t, router.Entry{Namespace: "default", Rules: []router.CompiledRule{
		rule("/a", false, "a-fn"),
		rule("/a/b", false, "ab-fn"),
	}})
	m, ok := r.Resolve("any", "/a/b/x", "GET")
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("ab-fn"), m.Function, "longest prefix wins")
	m, ok = r.Resolve("any", "/a/c", "GET")
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("a-fn"), m.Function)
}

// scenario: solo-route-strips-prefix (router half — StripPrefix carries the matched prefix)
func TestScenarioStripPrefix(t *testing.T) {
	r := prog(t, router.Entry{Namespace: "default", Rules: []router.CompiledRule{
		rule("/orders", false, "orders-fn"),
		rule("/exact", true, "exact-fn"),
	}})
	m, _ := r.Resolve("any", "/orders/x", "GET")
	require.Equal(t, "/orders", m.StripPrefix)
	m, _ = r.Resolve("any", "/exact", "GET")
	require.Equal(t, "", m.StripPrefix, "an exact rule strips nothing")
}

// scenario: route-delete-unroutes (Program replace-all drops removed routes)
func TestScenarioResolveMissAfterReplaceAll(t *testing.T) {
	r := prog(t, router.Entry{Namespace: "default", Rules: []router.CompiledRule{rule("/x", false, "x-fn")}})
	_, ok := r.Resolve("any", "/x", "GET")
	require.True(t, ok)
	require.NoError(t, r.Program(context.Background(), nil)) // replace-all with empty
	_, ok = r.Resolve("any", "/x", "GET")
	require.False(t, ok, "a replace-all with the route removed must stop resolving it")
}

func TestHostsAccessor(t *testing.T) {
	r := prog(t,
		router.Entry{Namespace: "a", Host: "a.com", Rules: []router.CompiledRule{rule("/", false, "a")}},
		router.Entry{Namespace: "b", Host: "", Rules: []router.CompiledRule{rule("/", false, "b")}},
	)
	require.Equal(t, []string{"a.com"}, r.Hosts(), "only distinct non-empty hosts")
}
