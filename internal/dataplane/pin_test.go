package dataplane_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/network/egress"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// pinBridge is funcd's address on the container bridge in these tests.
func pinBridge() netip.Addr { return netip.MustParseAddr("10.63.0.1") }

// pinnedDoor is the data plane with the pin on: team's site on team.localhost, its Route host api.example.com with no /
// rule, other's site on other.localhost and default's host-less /home, every Function warm. callers names the engine of
// team's Tunnel public at 10.63.0.3, team's worker api at 10.63.0.4, default's worker web at 10.63.0.5, and holds
// 10.63.0.6 in both indexes with different principals. served counts the requests a backend got.
func pinnedDoor(t *testing.T) (http.Handler, *spyScaler, *int) {
	t.Helper()
	served := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(up.Close)
	st := store.New(memory.New())
	for _, fn := range []struct{ ns, name string }{{"team", "site"}, {"team", "api"}, {"team", "app"}, {"team", "payroll"},
		{"other", "site"}, {"other", "payroll"}, {"default", "home"}, {"default", "payroll"}, {"default", "x"}} {
		seedFn(t, st, fn.ns, fn.name)
	}
	warm := map[v1.ObjectName]string{"site": "u", "api": "u", "app": "u", "payroll": "u", "home": "u", "x": "u"}
	scaler := &spyScaler{}
	act, err := activator.New(activator.Deps{Store: st, Endpoints: readyEndpoints{warm: warm, upstream: up.URL}, Scaler: scaler})
	require.NoError(t, err)
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{
		{Namespace: "team", Host: "team.localhost", Rules: []router.CompiledRule{{Path: "/", Function: "site"}}},
		{Namespace: "team", Host: "api.example.com", Rules: []router.CompiledRule{{Path: "/app", Function: "app"}}},
		{Namespace: "other", Host: "other.localhost", Rules: []router.CompiledRule{{Path: "/", Function: "site"}}},
		{Namespace: "default", Rules: []router.CompiledRule{{Path: "/home", Function: "home"}}},
	}, []string{"team.localhost", "other.localhost"}))
	workers, engines := egress.NewMemoryWorkerIndex(), egress.NewMemoryWorkerIndex()
	engines.Add(netip.MustParseAddr("10.63.0.3"), auth.EntityRef{Type: v1.KindAddonObject, Namespace: "team", Name: "public"})
	workers.Add(netip.MustParseAddr("10.63.0.4"), auth.EntityRef{Type: v1.KindFunction, Namespace: "team", Name: "api"})
	workers.Add(netip.MustParseAddr("10.63.0.5"), auth.EntityRef{Type: v1.KindFunction, Namespace: "default", Name: "web"})
	workers.Add(netip.MustParseAddr("10.63.0.6"), auth.EntityRef{Type: v1.KindFunction, Namespace: "team", Name: "api"})
	engines.Add(netip.MustParseAddr("10.63.0.6"), auth.EntityRef{Type: v1.KindAddonObject, Namespace: "other", Name: "public"})
	h := dataplane.Handler(st, act, rtr, "", nil, nil, nil, 0, nil,
		dataplane.WithBridgeCallers(pinBridge(), egress.Sources(workers, engines)))
	return h, scaler, &served
}

// onBridge sends a request from src that came in on local, as a listener on that address delivers it.
func onBridge(t *testing.T, h http.Handler, local netip.Addr, src, host, path, nsHeader string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	req.Host, req.RemoteAddr = host, src+":40000"
	if nsHeader != "" {
		req.Header.Set("X-Funcd-Namespace", nsHeader)
	}
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey,
		net.TCPAddrFromAddrPort(netip.AddrPortFrom(local, 8081))))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// ADR-0353 Decisions 3 and 4: a caller on the bridge address is served what the data plane resolves to its namespace,
// the edge entry it matches, else its /function/<name> path, and a header naming another namespace is refused on any
// host; another namespace's, an unknown or a conflicting caller gets 403 before any wake or backend. The public listener
// serves as before.
func TestPinServesTheCallersNamespaceOnly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		what                string
		local               netip.Addr
		src, host, path, ns string
		want                int
	}{
		{"the engine, its namespace's host", pinBridge(), "10.63.0.3", "team.localhost", "/", "", http.StatusOK},
		{"the engine, another namespace's host", pinBridge(), "10.63.0.3", "other.localhost", "/", "", http.StatusForbidden},
		{"the engine, by name on its Route host, the header naming another", pinBridge(), "10.63.0.3", "api.example.com", "/function/payroll", "other", http.StatusForbidden},
		{"the engine, by name on its Route host, no header: default", pinBridge(), "10.63.0.3", "api.example.com", "/function/payroll", "", http.StatusForbidden},
		{"the engine, by name, the header naming its own", pinBridge(), "10.63.0.3", "api.example.com", "/function/payroll", "team", http.StatusOK},
		{"the engine, by name on its namespace host", pinBridge(), "10.63.0.3", "team.localhost", "/function/payroll", "", http.StatusOK},
		{"the engine, its host, the header naming another", pinBridge(), "10.63.0.3", "team.localhost", "/", "other", http.StatusForbidden},
		{"the engine, another namespace's host by name", pinBridge(), "10.63.0.3", "other.localhost", "/function/payroll", "", http.StatusForbidden},
		{"a worker of default, its host-less entry", pinBridge(), "10.63.0.5", "example.com", "/home", "", http.StatusOK},
		{"a worker of default, its Function by name", pinBridge(), "10.63.0.5", "example.com", "/function/x", "", http.StatusOK},
		{"a worker of default, a host team reserves", pinBridge(), "10.63.0.5", "team.localhost", "/", "", http.StatusForbidden},
		{"a worker of team, its host", pinBridge(), "10.63.0.4", "team.localhost", "/", "", http.StatusOK},
		{"a worker of team, another namespace's host", pinBridge(), "10.63.0.4", "other.localhost", "/", "", http.StatusForbidden},
		{"an unknown caller", pinBridge(), "10.63.0.9", "team.localhost", "/", "", http.StatusForbidden},
		{"a conflicting caller", pinBridge(), "10.63.0.6", "team.localhost", "/", "", http.StatusForbidden},
		{"an unknown caller on a path of no route", pinBridge(), "10.63.0.9", "team.localhost", "/nothing/here", "", http.StatusForbidden},
		{"the public listener, any namespace's host", netip.MustParseAddr("127.0.0.1"), "10.63.0.9", "other.localhost", "/", "", http.StatusOK},
		{"the public listener, the header naming another", netip.MustParseAddr("127.0.0.1"), "10.63.0.3", "api.example.com", "/function/payroll", "other", http.StatusOK},
	} {
		h, scaler, served := pinnedDoor(t)
		res := onBridge(t, h, tc.local, tc.src, tc.host, tc.path, tc.ns)
		require.Equal(t, tc.want, res.StatusCode, tc.what)
		if tc.want == http.StatusForbidden {
			require.Zero(t, *served, "%s: no backend runs", tc.what)
			require.Zero(t, scaler.count(), "%s: nothing wakes", tc.what)
			var p fault.Problem
			require.NoError(t, json.NewDecoder(res.Body).Decode(&p))
			require.NotContains(t, p.Detail, "other", "%s: the refusal names no namespace", tc.what)
			require.NotContains(t, p.Detail, "team", "%s: the refusal names no namespace", tc.what)
		}
	}
}

// An internal call is never pinned, whatever its context holds.
func TestPinLeavesAnInternalCallAlone(t *testing.T) {
	t.Parallel()
	h, _, served := pinnedDoor(t)
	req := httptest.NewRequest(http.MethodPost, "http://other.localhost/function/payroll", nil)
	req.Header.Set("X-Funcd-Namespace", "other")
	req.RemoteAddr = "10.63.0.3:40000"
	ctx := context.WithValue(req.Context(), http.LocalAddrContextKey, net.TCPAddrFromAddrPort(netip.AddrPortFrom(pinBridge(), 8081)))
	req = req.WithContext(dataplane.WithInternal(ctx))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, *served)
}
