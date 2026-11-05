package dataplane_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/dataplane"
)

// echoUpstream captures the body the data plane forwards to the function upstream.
func echoUpstream(t *testing.T, got *string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = string(b)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(s.Close)
	return s
}

// scenario: internal-invoke-untouched — an EXTERNAL invoke is wrapped into a CloudEvent envelope by
// the edge; an INTERNAL (fn-to-fn/workflow/sensor) invoke is forwarded byte-for-byte (those producers
// already build a v1.0 envelope). Uses a plain `{"x":1}` body that would be wrapped if external, so
// the two paths are distinguishable.
func TestScenarioInternalInvokeUntouched(t *testing.T) {
	t.Parallel()
	var got string
	up := echoUpstream(t, &got)
	h, st := newHandler(t, up.URL)
	seedFunction(t, st, "echo")

	// External: plain body → wrapped as event data.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/function/echo", strings.NewReader(`{"x":1}`))
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var ce struct {
		SpecVersion string          `json:"specversion"`
		Data        json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(got), &ce))
	require.Equal(t, "1.0", ce.SpecVersion)
	require.JSONEq(t, `{"x":1}`, string(ce.Data))

	// Internal: same plain body → forwarded verbatim (NOT normalized).
	got = ""
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/function/echo", strings.NewReader(`{"x":1}`))
	req2 = req2.WithContext(dataplane.WithInternal(req2.Context()))
	h.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, `{"x":1}`, got, "internal traffic is forwarded byte-for-byte (not normalized)")
}

// scenario: no-input-empty-body-invoke — POST with no body → the function receives an envelope whose
// data is null (no {"data":null} required from the caller).
func TestScenarioEmptyBodyForwardsNullData(t *testing.T) {
	t.Parallel()
	var got string
	up := echoUpstream(t, &got)
	h, st := newHandler(t, up.URL)
	seedFunction(t, st, "echo")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/function/echo", nil)
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var ce struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(got), &ce))
	require.Equal(t, "null", string(ce.Data))
}

// The edge buffers a call's body to build its envelope, so the worker gets that body with its length, however the caller
// framed it (ADR-0134 Contracts).
func TestChunkedCallReachesTheWorkerWithItsLength(t *testing.T) {
	t.Parallel()
	type received struct {
		codings []string
		length  int64
		body    string
	}
	got := make(chan received, 1)
	h, st, _ := authDoorUpstream(t, warmAPI(), nil, creds{}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- received{codings: r.TransferEncoding, length: r.ContentLength, body: string(b)}
	}))
	seedFunction(t, st, "api")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/function/api", "application/json", io.MultiReader(strings.NewReader(`{"data":1}`)))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	worker := <-got
	require.Empty(t, worker.codings, "the worker's request is not chunked")
	require.Equal(t, int64(len(worker.body)), worker.length)
	require.Contains(t, worker.body, `"data":1`)
}
