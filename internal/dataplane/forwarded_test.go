package dataplane_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// forwardedDoor serves a warm Function api by name in default and an upstream entry at login.example.com, both backed
// by one server whose last request's headers seen returns.
func forwardedDoor(t *testing.T) (http.Handler, func() http.Header) {
	t.Helper()
	var mu sync.Mutex
	var last http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		last = r.Header.Clone()
		mu.Unlock()
	}))
	t.Cleanup(backend.Close)
	st := store.New(memory.New())
	seedFn(t, st, "default", "api")
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: backend.URL}, Scaler: noScaler{}})
	require.NoError(t, err)
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{Namespace: "default", Host: "login.example.com", Auth: v1.AuthOpen,
		Rules: []router.CompiledRule{{Path: "/", Upstream: backend.URL}}}}, nil))
	return dataplane.Handler(st, act, rtr, "", nil, nil, nil, 0, nil), func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

// sendForwarded sends a request from peer 203.0.113.9 with the caller's own forwarding headers; client, when valid, is the
// address the edge named.
func sendForwarded(t *testing.T, h http.Handler, host, path string, client netip.Addr, internal bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://"+host+path, nil)
	req.Host, req.RemoteAddr = host, "203.0.113.9:4242"
	for name, v := range map[string]string{"X-Forwarded-For": "192.0.2.66", "Forwarded": "for=192.0.2.66", "X-Real-IP": "192.0.2.67",
		"X-Forwarded-Proto": "http", "X-Forwarded-Host": "caller.example"} {
		req.Header.Set(name, v)
	}
	ctx := req.Context()
	if client.IsValid() {
		ctx = edge.WithClient(ctx, client)
	}
	if internal {
		ctx = dataplane.WithInternal(ctx)
	}
	h.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))
}

// ADR-0354 Decision 4: a Function worker that a call through the listener reaches gets X-Forwarded-For holding the client
// address alone and none of the caller's X-Forwarded-For, Forwarded or X-Real-IP, with X-Forwarded-Proto and
// X-Forwarded-Host as the caller sent them; with no proxy trusted the client is the peer. An upstream entry gets the
// client address in place of the peer. A fn-to-fn call is unchanged.
func TestBackendsGetTheClientAddress(t *testing.T) {
	t.Parallel()
	h, seen := forwardedDoor(t)
	client := netip.MustParseAddr("198.51.100.7")

	sendForwarded(t, h, "localhost", "/function/api", client, false)
	got := seen()
	require.Equal(t, []string{"198.51.100.7"}, got.Values("X-Forwarded-For"))
	require.Empty(t, got.Get("Forwarded"))
	require.Empty(t, got.Get("X-Real-Ip"))
	require.Equal(t, "http", got.Get("X-Forwarded-Proto"))
	require.Equal(t, "caller.example", got.Get("X-Forwarded-Host"))

	sendForwarded(t, h, "localhost", "/function/api", netip.Addr{}, false)
	require.Equal(t, []string{"203.0.113.9"}, seen().Values("X-Forwarded-For"), "no proxy trusted: the peer alone")

	sendForwarded(t, h, "login.example.com", "/ping", client, false)
	got = seen()
	require.Equal(t, []string{"198.51.100.7"}, got.Values("X-Forwarded-For"), "an upstream entry gets the client address")
	for _, name := range []string{"Forwarded", "X-Real-Ip", "X-Forwarded-Proto", "X-Forwarded-Host"} {
		require.Empty(t, got.Get(name), name)
	}

	sendForwarded(t, h, "localhost", "/function/api", client, true)
	got = seen()
	require.Equal(t, []string{"192.0.2.66, 203.0.113.9"}, got.Values("X-Forwarded-For"), "a fn-to-fn call is unchanged")
	require.Equal(t, "192.0.2.67", got.Get("X-Real-Ip"))
}
