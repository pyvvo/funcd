package dataplane_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// forwarded is what the data plane sent a Function's worker.
type forwarded struct {
	path, query, body string
	header            http.Header
}

// httpFront serves Functions through the data plane with entries programmed, recording what reaches the worker.
func httpFront(t *testing.T, entries []router.Entry) (http.Handler, store.Store, *forwarded) {
	t.Helper()
	got := &forwarded{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.path, got.query, got.body, got.header = r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(up.Close)
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: up.URL}, Scaler: noScaler{}})
	require.NoError(t, err)
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), entries, nil))
	return dataplane.Handler(st, act, rtr, "", nil, nil, nil, 0, nil), st, got
}

func seedProtocol(t *testing.T, st store.Store, name string, protocol v1.FunctionProtocol) {
	t.Helper()
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image, fn.Spec.Protocol = "nodejs22", "handle", "file:///tmp/x", protocol
	_, err := st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func serve(h http.Handler, method, target, body string, header map[string]string) int {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// scenario: http-rest — for an http Function the edge wraps every JSON body, also one with a top-level data key that
// ADR-0134 passes through for an event Function, and names the prefix it stripped (ADR-0243 Decision 3).
func TestScenarioHTTPFunctionWrapsEveryJSONBody(t *testing.T) {
	t.Parallel()
	h, st, got := httpFront(t, nil)
	seedProtocol(t, st, "api", v1.ProtocolHTTP)
	seedProtocol(t, st, "events", v1.ProtocolCloudEvent)
	const body = `{"data":{"name":"truck-7"},"meta":{"page":2}}`

	require.Equal(t, http.StatusOK, serve(h, http.MethodPost, "/function/api/devices", body, nil))
	require.Equal(t, "/devices", got.path)
	require.Equal(t, "/function/api", got.header.Get("X-Forwarded-Prefix"))
	var ce struct {
		SpecVersion string          `json:"specversion"`
		Data        json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(got.body), &ce))
	require.Equal(t, "1.0", ce.SpecVersion)
	require.JSONEq(t, body, string(ce.Data), "the whole body is the envelope's data")

	require.Equal(t, http.StatusOK, serve(h, http.MethodPost, "/function/events", body, nil))
	require.Equal(t, body, got.body, "an event Function keeps ADR-0134's passthrough")
	require.Empty(t, got.header.Get("X-Forwarded-Prefix"), "only an http Function is told the prefix")
}

// scenario: http-limits — a body that is not JSON, or JSON that is not UTF-8, reaches an http Function byte for byte.
func TestHTTPFunctionNonJSONBodyIsRaw(t *testing.T) {
	t.Parallel()
	h, st, got := httpFront(t, nil)
	seedProtocol(t, st, "api", v1.ProtocolHTTP)
	for _, body := range []string{"name=truck-7&kind=van", "{\"name\":\"Caf\xe9\"}"} {
		require.Equal(t, http.StatusOK, serve(h, http.MethodPost, "/function/api/form", body, nil))
		require.Equal(t, body, got.body)
	}
}

// scenario: http-route — a Route's prefix is stripped and named in X-Forwarded-Prefix, replacing the client's.
func TestScenarioHTTPRouteNamesItsPrefix(t *testing.T) {
	t.Parallel()
	entries := []router.Entry{{Namespace: "default", Rules: []router.CompiledRule{{Path: "/api", Function: "api"}},
		Owner: router.Owner{Kind: v1.KindRoute, Namespace: "default", Name: "api"}}}
	h, st, got := httpFront(t, entries)
	seedProtocol(t, st, "api", v1.ProtocolHTTP)
	require.Equal(t, http.StatusOK, serve(h, http.MethodGet, "/api/devices/42?full=1", "", map[string]string{"X-Forwarded-Prefix": "/spoofed"}))
	require.Equal(t, "/devices/42", got.path)
	require.Equal(t, "full=1", got.query)
	require.Equal(t, "/api", got.header.Get("X-Forwarded-Prefix"))
}
