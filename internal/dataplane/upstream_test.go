package dataplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/edge/shape"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// TestScenarioDataPlaneServesUpstreamBackend covers ADR-0138: a matched Upstream (node-private
// reverse-proxy) edge entry is reverse-proxied to the in-daemon target — the catalog::query PEP proxy
// path — with the matched prefix stripped so the upstream is addressed at its own root, and NO
// activator hop / Function resolve.
func TestScenarioDataPlaneServesUpstreamBackend(t *testing.T) {
	t.Parallel()
	// the "PEP proxy" stub records the path it was addressed at and answers 403 (its fail-closed).
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "denied")
	}))
	t.Cleanup(up.Close)

	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default",
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: "/catalog/lake", Upstream: up.URL}},
	}}))

	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, rtr, nil, nil, nil, 0, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog/lake/db", nil))

	require.Equal(t, http.StatusForbidden, rec.Code, "the upstream's own response (fail-closed 403) is proxied back verbatim")
	require.Equal(t, "/db", gotPath, "the matched prefix /catalog/lake is stripped — the upstream is addressed at its root")
}

// TestScenarioDataPlaneUpstreamUnreachable covers the 502 path: a programmed Upstream that can't be
// dialed yields a fault (not a panic), so a rebinding proxy degrades gracefully.
func TestScenarioDataPlaneUpstreamUnreachable(t *testing.T) {
	t.Parallel()
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default",
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: "/catalog/lake", Upstream: "http://127.0.0.1:1"}}, // nothing listens
	}}))
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, rtr, nil, nil, nil, 0, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog/lake", nil))
	require.GreaterOrEqual(t, rec.Code, 500, "an unreachable upstream is a 5xx, not a panic")
}

// The Upstream proxy sits behind the same edge chain as the activator, so an upstream 1xx must not
// drop the edge's X-Request-Id and CORS headers on an Upstream route either.
func TestIssue417_UpstreamRouteKeepsEdgeHeadersAfter1xx(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/expect":
			_, _ = io.Copy(io.Discard, r.Body) // the first body read answers Expect with 100 Continue
		case "/drop":
			w.WriteHeader(http.StatusEarlyHints)
			panic(http.ErrAbortHandler)
		default:
			w.WriteHeader(http.StatusEarlyHints)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(up.Close)
	rtr := router.New()
	require.NoError(t, rtr.Program(t.Context(), []router.Entry{{
		Namespace: "default",
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: "/catalog/lake", Upstream: up.URL}},
	}}))
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	edge := httptest.NewServer(gateway.Chain(dataplane.Handler(st, act, rtr, nil, nil, nil, 0, nil),
		gateway.RequestID, shape.Chain(shape.Config{CORS: &shape.CORS{AllowOrigins: []string{"*"}}})))
	t.Cleanup(edge.Close)
	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)

	for _, tc := range []struct {
		name    string
		path    string
		body    io.Reader
		interim int
		status  int
	}{
		{name: "early-hints", path: "/hints", interim: http.StatusEarlyHints, status: http.StatusOK},
		{name: "expect-continue", path: "/expect", body: bytes.NewReader(make([]byte, 4096)), interim: http.StatusContinue, status: http.StatusOK},
		{name: "upstream-fails-after-1xx", path: "/drop", interim: http.StatusEarlyHints, status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var codes []int
			trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, _ textproto.MIMEHeader) error {
				codes = append(codes, code)
				return nil
			}}
			method := http.MethodGet
			if tc.body != nil {
				method = http.MethodPost
			}
			req, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), method, edge.URL+"/catalog/lake"+tc.path, tc.body)
			require.NoError(t, err)
			req.Header.Set("Origin", "https://app.example")
			if tc.body != nil {
				req.Header.Set("Expect", "100-continue")
			}
			resp, err := client.Do(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Contains(t, codes, tc.interim, "the upstream 1xx is relayed")
			require.Equal(t, tc.status, resp.StatusCode)
			require.Equal(t, "https://app.example", resp.Header.Get("Access-Control-Allow-Origin"))
			require.Contains(t, resp.Header.Values("Vary"), "Origin")
			require.NotEmpty(t, resp.Header.Get("X-Request-Id"))
		})
	}
}

// TestIssue440_UpstreamErrorsLogThroughSlog: a failed edge upstream call (ADR-0138) is logged once
// through the data plane's slog logger, naming the upstream and the error, and ReverseProxy's own
// errors (a failed body copy) go through the same handler, never the stdlib log package. Not parallel:
// it swaps the stdlib logger's output.
func TestIssue440_UpstreamErrorsLogThroughSlog(t *testing.T) {
	var stdlog bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&stdlog)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	stopped := httptest.NewServer(http.NotFoundHandler())
	stopped.Close()
	truncating := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(truncating.Close)

	cases := []struct {
		name     string
		upstream string
		logged   string
		warns    int
	}{
		{name: "an unreachable upstream", upstream: stopped.URL, logged: "connection refused", warns: 1},
		{name: "an upstream that fails mid-body", upstream: truncating.URL, logged: "read error during body copy", warns: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rtr := router.New()
			require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
				Namespace: "default",
				Auth:      v1.AuthOpen,
				Rules:     []router.CompiledRule{{Path: "/catalog/lake", Upstream: tc.upstream}},
			}}))
			st := store.New(memory.New())
			act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
			require.NoError(t, err)
			var logs bytes.Buffer
			h := dataplane.Handler(st, act, rtr, nil, nil, nil, 0, slog.New(slog.NewTextHandler(&logs, nil)))

			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/catalog/lake/db", nil))

			require.Contains(t, logs.String(), tc.logged)
			require.Equal(t, tc.warns, strings.Count(logs.String(), "level=WARN"), logs.String())
			require.Equal(t, tc.warns, strings.Count(logs.String(), "upstream="+tc.upstream), "every line names the upstream")
		})
	}
	require.Empty(t, stdlog.String(), "nothing is logged through the stdlib log package")
}

// The Upstream proxy keeps its connections out of http.DefaultTransport: every httptest.Server.Close in the process
// closes that transport's idle connections, and one landing while an Upstream call has just picked a parked connection
// fails the call. Not parallel: it closes the default transport's idle connections, which would break the other tests'
// parked connections.
func TestIssue564_DataPlaneUpstreamSurvivesDefaultTransportCloseIdle(t *testing.T) {
	conns := new(atomic.Int32)
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	up.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	up.Start()
	t.Cleanup(up.Close)

	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default",
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: "/catalog/lake", Upstream: up.URL}},
	}}))
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, rtr, nil, nil, nil, 0, nil)
	get := func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog/lake/db", nil))
		require.Equal(t, http.StatusOK, rec.Code)
	}

	get()
	get()
	require.EqualValues(t, 1, conns.Load(), "two Upstream calls share one keep-alive connection")
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	get()
	require.EqualValues(t, 1, conns.Load(), "closing the default transport's idle connections must not touch the data plane's")
}

// A failed or misconfigured edge upstream answers the client with a fixed 503 detail: the dial error
// and the upstream URL name an in-daemon listener, so they go to the log only, never to the client.
func TestUpstreamFailureProblemHidesUpstreamAddress(t *testing.T) {
	t.Parallel()
	stopped := httptest.NewServer(http.NotFoundHandler())
	stopped.Close()
	stoppedHost := strings.TrimPrefix(stopped.URL, "http://")

	for _, tc := range []struct {
		name     string
		upstream string
		hidden   string
	}{
		{name: "an unreachable upstream", upstream: stopped.URL, hidden: stoppedHost},
		{name: "a malformed upstream", upstream: "10.63.0.7:8080", hidden: "10.63.0.7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rtr := router.New()
			require.NoError(t, rtr.Program(t.Context(), []router.Entry{{
				Namespace: "default",
				Auth:      v1.AuthOpen,
				Rules:     []router.CompiledRule{{Path: "/catalog/lake", Upstream: tc.upstream}},
			}}))
			st := store.New(memory.New())
			act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
			require.NoError(t, err)
			var logs bytes.Buffer
			h := dataplane.Handler(st, act, rtr, nil, nil, nil, 0, slog.New(slog.NewTextHandler(&logs, nil)))

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog/lake/db", nil))

			require.Equal(t, http.StatusServiceUnavailable, rec.Code)
			require.Contains(t, rec.Body.String(), "urn:funcd:problem:unavailable")
			require.NotContains(t, rec.Body.String(), tc.hidden, "the problem detail must not name the upstream address")
			require.Contains(t, logs.String(), tc.hidden, "the cause stays in the log")
		})
	}
}

// scenario: edge-chunked-body-over-cap — behind the edge's limit.Chain (MaxBodyBytes 1024), a 64 KiB chunked
// body to an edge upstream answers 413, not 503, and a 2 KiB chunked body to /function/<name> answers 413
// naming the 1024-byte cap that tripped, not maxNormalizeBytes (ADR-0148).
func TestScenarioEdgeChunkedBodyOverCap(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(up.Close)
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default",
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: "/catalog/lake", Upstream: up.URL}},
	}}))
	st := store.New(memory.New())
	seedFunction(t, st, "echo")
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: up.URL}, Scaler: noScaler{}})
	require.NoError(t, err)
	h := limit.Chain(limit.Config{MaxBodyBytes: 1024})(dataplane.Handler(st, act, rtr, nil, nil, nil, 0, nil))

	for _, tc := range []struct {
		name, path string
		size       int
	}{
		{"upstream", "/catalog/lake/q", 64 << 10},
		{"function", "/function/echo", 2 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A MultiReader has no known length, so the request is chunked (ContentLength -1).
			req := httptest.NewRequest(http.MethodPost, tc.path, io.MultiReader(strings.NewReader(strings.Repeat("x", tc.size))))
			require.Equal(t, int64(-1), req.ContentLength)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
			var p fault.Problem
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
			require.Equal(t, "urn:funcd:problem:payload-too-large", p.Type)
			require.Contains(t, p.Detail, "exceeds 1024 bytes")
		})
	}
}

// ADR-0151: an Upstream Route (ADR-0138) is served before serveFunction, so invoke.defaultTimeout does not bound it.
func TestUpstreamRouteNotBoundByInvokeDeadline(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = io.WriteString(w, "late")
	}))
	t.Cleanup(up.Close)
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default",
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: "/catalog/lake", Upstream: up.URL}},
	}}))
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, rtr, nil, nil, nil, time.Second, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog/lake", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "late", rec.Body.String())
}

// scenario: edge-upstream-burst-reuses-connections — 20 bursts of 32 concurrent external requests to an Upstream edge
// route (ADR-0138) open 32 connections to the upstream, all in the first burst (ADR-0155). The upstream holds each
// burst until all of its requests have arrived: without the barrier a call that ends while another's dial is pending
// hands that call its connection, and the dialed one stays idle.
func TestScenarioEdgeUpstreamBurstReusesConnections(t *testing.T) {
	t.Parallel()
	const bursts, wide = 20, 32
	conns := new(atomic.Int32)
	var mu sync.Mutex
	arrived, release := 0, make(chan struct{})
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		burst := release
		if arrived++; arrived == wide {
			close(release)
			arrived, release = 0, make(chan struct{})
		}
		mu.Unlock()
		select {
		case <-burst:
		case <-time.After(10 * time.Second):
			t.Errorf("a burst never reached %d requests in flight", wide)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	up.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	up.Start()
	t.Cleanup(up.Close)

	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default",
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: "/catalog/lake", Upstream: up.URL}},
	}}))
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, rtr, nil, nil, nil, 0, nil)

	for burst := range bursts {
		var wg sync.WaitGroup
		for range wide {
			wg.Go(func() {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog/lake/db", nil))
				if rec.Code != http.StatusOK {
					t.Errorf("status %d, want 200", rec.Code)
				}
			})
		}
		wg.Wait()
		require.EqualValues(t, wide, conns.Load(), "connections accepted after burst %d", burst+1)
	}
}
