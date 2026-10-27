package dataplane_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/edge/static"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// heldOrders is a hold with orders held for its data, as restore run leaves it.
func heldOrders(t *testing.T) *hold.Hold {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, hold.MarkNamespaces(dir, []v1.NamespaceName{"orders"}, hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	return h
}

// heldDoor is a data-plane handler asking gate, with a warm Function api in orders and in web (both with the stance
// mode) whose upstream counts the calls that reach it.
func heldDoor(t *testing.T, gate hold.CallGate, entries []router.Entry, mode v1.AuthMode) (http.Handler, *spyScaler, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	up := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = w.Write([]byte("ok")) })
	h, st, scaler := authDoorUpstream(t, map[v1.ObjectName]string{"api": "u"}, entries, creds{}, up, dataplane.WithCallGate(gate))
	for _, ns := range []string{"orders", "web"} {
		seedNSAuth(t, st, ns, v1.ExposureImplicit, mode)
		seedFn(t, st, ns, "api")
	}
	return h, scaler, hits
}

// edgeDoor is a data-plane handler over entries and stat for the upstream and static paths, which reach no activator.
func edgeDoor(t *testing.T, entries []router.Entry, stat *static.Handler, opts ...dataplane.Option) http.Handler {
	t.Helper()
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), entries, nil))
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	return dataplane.Handler(st, act, rtr, "", nil, nil, stat, 0, nil, opts...)
}

// requireHeld asserts ADR-0290 Decision 2's refusal, a detail containing want, and that no worker was reached.
func requireHeld(t *testing.T, resp *http.Response, want string, scaler *spyScaler, hits *atomic.Int32) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, string(body))
	require.Equal(t, "30", resp.Header.Get("Retry-After"))
	var p struct{ Type, Detail string }
	require.NoError(t, json.Unmarshal(body, &p))
	require.Equal(t, "urn:funcd:problem:unavailable", p.Type)
	require.Contains(t, p.Detail, want)
	require.Zero(t, scaler.count(), "a refused call wakes nothing")
	require.Zero(t, hits.Load(), "a refused call reaches no worker")
}

// scenario: data-hold-refuses-calls — a call into a namespace that restore run held for its data, by its Route or by
// /function/<name>, gets 503 and Retry-After, and the detail says how the hold ends.
func TestScenarioDataHoldRefusesCalls(t *testing.T) {
	t.Parallel()
	entries := []router.Entry{{Namespace: "orders", Auth: v1.AuthOpen, Rules: []router.CompiledRule{{Path: "/orders", Function: "api"}}}}
	h, scaler, hits := heldDoor(t, heldOrders(t), entries, v1.AuthOpen)
	for _, path := range []string{"/function/api", "/orders"} {
		requireHeld(t, authReq(t, h, path, "orders", ""),
			"namespace orders is held for its data since ", scaler, hits)
	}
	requireHeld(t, authReq(t, h, "/function/api", "orders", ""),
		"calls resume after funcdctl hold release --namespace orders", scaler, hits)
}

// scenario: release-serves-calls — the owner's release lifts the refusal for the next call.
func TestScenarioReleaseServesCalls(t *testing.T) {
	t.Parallel()
	hd := heldOrders(t)
	h, scaler, hits := heldDoor(t, hd, nil, v1.AuthOpen)
	requireHeld(t, authReq(t, h, "/function/api", "orders", ""), "held for its data", scaler, hits)
	require.NoError(t, hd.ReleaseNamespace("orders", hold.ReasonData))
	require.Equal(t, http.StatusOK, authReq(t, h, "/function/api", "orders", "").StatusCode)
	require.Equal(t, int32(1), hits.Load())
}

// scenario: restore-load-refuses-calls — while an in-place Restore loads the namespace, the refusal names it.
func TestScenarioRestoreLoadRefusesCalls(t *testing.T) {
	t.Parallel()
	hd, err := hold.Open(t.TempDir())
	require.NoError(t, err)
	m := hold.Marker{Reason: hold.ReasonRestore, Since: v1.NewTimestamp(time.Now()), Restore: "nightly-1"}
	require.NoError(t, hd.HoldNamespace("orders", m))
	h, scaler, hits := heldDoor(t, hd, nil, v1.AuthOpen)
	requireHeld(t, authReq(t, h, "/function/api", "orders", ""),
		"namespace orders is held while Restore nightly-1 loads it since ", scaler, hits)
	require.NoError(t, hd.ReleaseNamespaceMarker("orders", m))
	require.Equal(t, http.StatusOK, authReq(t, h, "/function/api", "orders", "").StatusCode)
}

// scenario: internal-call-refused — a fn-to-fn call by name skips the edge PEP, not the hold.
func TestScenarioInternalCallRefused(t *testing.T) {
	t.Parallel()
	h, scaler, hits := heldDoor(t, heldOrders(t), nil, v1.AuthOpen)
	req := httptest.NewRequest(http.MethodPost, "http://any/function/api", nil)
	req.Header.Set("X-Funcd-Namespace", "orders")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(dataplane.WithInternal(req.Context())))
	requireHeld(t, rec.Result(), "held for its data", scaler, hits)
}

// scenario: lake-upstream-refused — the upstream path runs no edge PEP, so its refusal is the upstream's usual 503 and
// names no hold; the release lets the next request through.
func TestScenarioLakeUpstreamRefused(t *testing.T) {
	t.Parallel()
	hits := &atomic.Int32{}
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(up.Close)
	hd := heldOrders(t)
	h := edgeDoor(t, []router.Entry{{Namespace: "orders", Auth: v1.AuthOpen,
		Rules: []router.CompiledRule{{Path: "/lake/sales", Upstream: up.URL}}}}, nil, dataplane.WithCallGate(hd))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lake/sales/db", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "30", rec.Header().Get("Retry-After"))
	require.Contains(t, rec.Body.String(), "edge upstream unavailable")
	require.NotContains(t, rec.Body.String(), "held")
	require.Zero(t, hits.Load())

	require.NoError(t, hd.ReleaseNamespace("orders", hold.ReasonData))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lake/sales/db", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int32(1), hits.Load())
}

// scenario: site-serves-while-held — a held namespace's Site keeps serving its static files.
func TestScenarioSiteServesWhileHeld(t *testing.T) {
	t.Parallel()
	b, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	require.NoError(t, b.Put(context.Background(), "web/index.html", []byte("INDEX"), blob.PutOptions{}))
	sh, err := static.New(static.Deps{Buckets: func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool) {
		return b, ns == "orders" && bucket == "site"
	}})
	require.NoError(t, err)
	h := edgeDoor(t, []router.Entry{{Namespace: "orders", Auth: v1.AuthOpen,
		Rules: []router.CompiledRule{{Path: "/", Static: &v1.StaticBackend{Bucket: "site", Prefix: "web/", Index: "index.html"}}}}},
		sh, dataplane.WithCallGate(heldOrders(t)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "INDEX", rec.Body.String())
}

// scenario: auth-before-hold — a caller without credentials gets today's 401, which says nothing of the hold.
func TestScenarioAuthBeforeHold(t *testing.T) {
	t.Parallel()
	h, scaler, hits := heldDoor(t, heldOrders(t), nil, v1.AuthAuthenticated)
	resp := authReq(t, h, "/function/api", "orders", "")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Empty(t, resp.Header.Get("Retry-After"))
	require.NotContains(t, string(body), "held")
	require.Zero(t, scaler.count())
	require.Zero(t, hits.Load())
}

// scenario: platform-hold-unchanged — the platform's own hold is no namespace marker, so a call is served as today.
func TestScenarioPlatformHoldUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	hd, err := hold.Open(dir)
	require.NoError(t, err)
	require.True(t, hd.Held())
	h, _, hits := heldDoor(t, hd, nil, v1.AuthOpen)
	require.Equal(t, http.StatusOK, authReq(t, h, "/function/api", "orders", "").StatusCode)
	require.Equal(t, int32(1), hits.Load())
}

// scenario: unheld-namespace-unaffected — a hold on orders leaves web's calls alone.
func TestScenarioUnheldNamespaceUnaffected(t *testing.T) {
	t.Parallel()
	h, _, hits := heldDoor(t, heldOrders(t), nil, v1.AuthOpen)
	require.Equal(t, http.StatusOK, authReq(t, h, "/function/api", "web", "").StatusCode)
	require.Equal(t, int32(1), hits.Load())
}
