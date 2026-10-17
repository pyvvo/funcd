package local_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// fetchInvoker records the FetchRequest it is given and answers reply.
type fetchInvoker struct {
	reply local.FetchReply
	got   *local.FetchRequest
}

func (f fetchInvoker) Invoke(context.Context, local.Ref, []byte, time.Duration) ([]byte, error) {
	return nil, nil
}

func (f fetchInvoker) Fetch(_ context.Context, _ local.Ref, req local.FetchRequest, _ time.Duration) (local.FetchReply, error) {
	*f.got = req
	return f.reply, nil
}

func fetchHandler(protocol v1.FunctionProtocol, inv local.Invoker, authz *fakeAuthz) http.Handler {
	res := &fakeResolver{target: local.Ref{Namespace: "team-a", Function: "devices"}, protocol: protocol}
	if authz == nil {
		return local.NewHandler(local.Ref{Namespace: "team-a", Function: "bridge"}, res, inv, nil, nil, nil, nil, nil)
	}
	return local.NewHandler(local.Ref{Namespace: "team-a", Function: "bridge"}, res, inv, authz, nil, nil, nil, nil)
}

// scenario: http-fetch — context.fetch reaches a linked http Function with the caller's method, path, query, headers
// and body, and gets its whole reply back with its status and headers (ADR-0243 Decision 6).
func TestScenarioFetchForwardsTheRequestAndTheReply(t *testing.T) {
	var got local.FetchRequest
	inv := fetchInvoker{got: &got, reply: local.FetchReply{
		Status: http.StatusCreated, Header: http.Header{"Location": {"/devices/7"}}, Body: []byte(`{"id":"7"}`),
	}}
	r := httptest.NewRequest(http.MethodPost, "/fetch/devices/devices/purge?dry=1", strings.NewReader(`{"older":"30d"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(local.MemberHeader, "bridge")
	rec := httptest.NewRecorder()
	fetchHandler(v1.ProtocolHTTP, inv, nil).ServeHTTP(rec, r)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, "/devices/7", rec.Header().Get("Location"))
	require.Equal(t, `{"id":"7"}`, rec.Body.String())
	require.Equal(t, "1", rec.Header().Get(local.FetchReplyHeader), "the shim tells a reply from a refusal")
	require.Equal(t, http.MethodPost, got.Method)
	require.Equal(t, "/devices/purge", got.Path)
	require.Equal(t, "dry=1", got.RawQuery)
	require.Equal(t, `{"older":"30d"}`, string(got.Body))
	require.Equal(t, "application/json", got.Header.Get("Content-Type"))
	require.Empty(t, got.Header.Get(local.MemberHeader), "the pool member header stays on the caller's side")
}

// context.fetch(alias, '/') reaches the target's root.
func TestFetchToTheRootKeepsTheSlash(t *testing.T) {
	var got local.FetchRequest
	rec := httptest.NewRecorder()
	fetchHandler(v1.ProtocolHTTP, fetchInvoker{got: &got, reply: local.FetchReply{Status: http.StatusOK}}, nil).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fetch/devices/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "/", got.Path)
}

// scenario: http-fetch — fetch to an event Function and invoke to an http one each fail closed, naming the other call.
func TestFetchAndInvokeRefuseTheOtherKind(t *testing.T) {
	var got local.FetchRequest
	rec := httptest.NewRecorder()
	fetchHandler(v1.ProtocolCloudEvent, fetchInvoker{got: &got}, nil).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fetch/devices/devices", nil))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "context.invoke")
	require.Empty(t, rec.Header().Get(local.FetchReplyHeader), "a refusal is no reply")
	require.Empty(t, got.Method, "nothing is called")

	rec = post(t, fetchHandler(v1.ProtocolHTTP, fakeInvoker{out: []byte(`{}`)}, nil), "devices", `{}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "context.fetch")
}

// context.fetch passes the PDP as invoke does: a forbid Policy refuses it before any call.
func TestFetchChecksThePDP(t *testing.T) {
	var got local.FetchRequest
	authz := &fakeAuthz{allowed: false}
	rec := httptest.NewRecorder()
	fetchHandler(v1.ProtocolHTTP, fetchInvoker{got: &got}, authz).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fetch/devices/devices", nil))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Empty(t, got.Method)
}

// The invoker sends a fetch through the data plane as the edge would: /function/<target><path> with the query, the
// target's namespace and the JSON body wrapped in a CloudEvent envelope.
func TestInvokerFetchesThroughTheDataPlane(t *testing.T) {
	var path, query, ns, body string
	dp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		path, query, ns, body = r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Funcd-Namespace"), string(b)
		w.Header().Set("Location", "/devices/7")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"7"}`)
	})
	reply, err := local.NewInvoker(dp).Fetch(context.Background(), local.Ref{Namespace: "team-a", Function: "devices"},
		local.FetchRequest{Method: http.MethodPost, Path: "/devices", RawQuery: "dry=1", Header: http.Header{}, Body: []byte(`{"data":1}`)}, time.Second)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, reply.Status)
	require.Equal(t, "/devices/7", reply.Header.Get("Location"))
	require.Equal(t, `{"id":"7"}`, string(reply.Body))
	require.Equal(t, "/function/devices/devices", path)
	require.Equal(t, "dry=1", query)
	require.Equal(t, "team-a", ns)
	var ce struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &ce))
	require.JSONEq(t, `{"data":1}`, string(ce.Data), "wrapped even with a top-level data key")
}

// context.fetch keeps the path percent-encoded from the caller to the data plane, so an escaped space, % or ? neither
// breaks the request nor cuts the path (ADR-0243 Decision 6).
func TestFetchKeepsAnEscapedPath(t *testing.T) {
	var path, query string
	dp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(fetchHandler(v1.ProtocolHTTP, local.NewInvoker(dp), nil))
	t.Cleanup(srv.Close)
	for _, c := range []struct{ sent, path, query string }{
		{"/files/My%20Truck", "/function/devices/files/My Truck", ""},
		{"/files/100%25", "/function/devices/files/100%", ""},
		{"/files/a%3Fb?x=1", "/function/devices/files/a?b", "x=1"},
	} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, srv.URL+"/fetch/devices"+c.sent, nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err, c.sent)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusNoContent, resp.StatusCode, c.sent)
		require.Equal(t, c.path, path, c.sent)
		require.Equal(t, c.query, query, c.sent)
	}
}
