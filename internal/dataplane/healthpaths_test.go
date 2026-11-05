package dataplane_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// workerPaths is a worker that records the path of each call it receives.
type workerPaths struct {
	mu    sync.Mutex
	paths []string
}

func (w *workerPaths) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.paths = append(w.paths, r.URL.Path)
	_, _ = io.WriteString(rw, "from the worker")
}

func (w *workerPaths) received() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.paths)
}

// healthRoutes are a Prefix rule /api and a Prefix rule / to Function api, each on a host of its own.
func healthRoutes() []router.Entry {
	return []router.Entry{
		{Namespace: "default", Host: "prefix.test", Rules: []router.CompiledRule{{Path: "/api", Function: "api"}}},
		{Namespace: "default", Host: "root.test", Rules: []router.CompiledRule{{Path: "/", Function: "api"}}},
	}
}

// requireNoRoute requires the health-paths answer to a call sent to target, which names the path as the listener decodes it.
func requireNoRoute(t *testing.T, resp *http.Response, target string) {
	t.Helper()
	u, err := url.Parse("http://x" + target)
	require.NoError(t, err)
	var problem fault.Problem
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&problem))
	require.Equal(t, http.StatusNotFound, resp.StatusCode, target)
	require.Contains(t, problem.Detail, fmt.Sprintf("no route for %q", u.Path), target)
}

// scenario: a-health-path-answers-404
func TestScenarioAHealthPathAnswers404(t *testing.T) {
	t.Parallel()
	worker := &workerPaths{}
	h, st, scaler := authDoorUpstream(t, warmAPI(), healthRoutes(), creds{}, worker)
	seedFunction(t, st, "api")
	seedFunction(t, st, "idle")
	off := false
	seedFnIngress(t, st, "default", "closed", &v1.Ingress{Enabled: &off})
	for _, tc := range []struct{ host, path string }{
		{"name.test", "/function/api/health/readiness"},
		{"name.test", "/function/api/health/liveness"},
		{"name.test", "/function/api/health/members"},
		{"name.test", "/function/api/health/other"},
		{"name.test", "/function/idle/health/liveness"},
		{"name.test", "/function/ghost/health/readiness"},
		{"name.test", "/function/closed/health/readiness"},
		{"prefix.test", "/api/health/readiness"},
		{"root.test", "/health/readiness"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			requireNoRoute(t, do(t, h, method, tc.host, tc.path, ""), tc.path)
		}
	}
	require.Empty(t, worker.received(), "no worker receives such a call")
	require.Zero(t, scaler.count(), "nothing wakes")
}

// A pooled Function's upstream keeps /function/<name> (ADR-0046): the check reads the remainder, so it answers the same.
func TestPooledFunctionHealthPathAnswers404(t *testing.T) {
	t.Parallel()
	worker := &workerPaths{}
	up := httptest.NewServer(worker)
	t.Cleanup(up.Close)
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: pooledEndpoints{worker: up.URL}, Scaler: &spyScaler{}})
	require.NoError(t, err)
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), healthRoutes(), nil))
	h := dataplane.Handler(st, act, rtr, "", nil, nil, nil, 0, nil)
	seedFunction(t, st, "api")
	requireNoRoute(t, do(t, h, http.MethodGet, "name.test", "/function/api/health/members", ""), "/function/api/health/members")
	requireNoRoute(t, do(t, h, http.MethodGet, "root.test", "/health/members", ""), "/health/members")
	require.Empty(t, worker.received())
	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "name.test", "/function/api/health", "").StatusCode)
	require.Equal(t, []string{"/function/api/health"}, worker.received())
}

// A refusal comes before the Function read of the Route form, and takes no rate token.
func TestHealthPathTakesNoTokenAndReadsNoFunction(t *testing.T) {
	t.Parallel()
	warm := map[v1.ObjectName]string{"fa": "u"}
	entries := []router.Entry{{Namespace: "default", Host: "edge.test", Rules: []router.CompiledRule{
		{Path: "/a", Function: "fa"}, {Path: "/gone", Function: "missing"},
	}}}
	h, st, scaler, _ := limitedFrontDoor(t, warm, entries, perTarget(1, 0))
	seedFunction(t, st, "fa")
	for range 3 {
		requireNoRoute(t, do(t, h, http.MethodGet, "edge.test", "/a/health/readiness", ""), "/a/health/readiness")
	}
	requireNoRoute(t, do(t, h, http.MethodGet, "edge.test", "/gone/health/readiness", ""), "/gone/health/readiness")
	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "edge.test", "/a/x", "").StatusCode, "the token is still there")
	require.Zero(t, scaler.count())
}

// A held namespace answers the same 404: the hold runs at Before.
func TestHealthPathInAHeldNamespaceAnswers404(t *testing.T) {
	t.Parallel()
	h, scaler, hits := heldDoor(t, heldOrders(t), nil, v1.AuthOpen)
	requireNoRoute(t, do(t, h, http.MethodGet, "name.test", "/function/api/health/readiness", "orders"), "/function/api/health/readiness")
	require.Equal(t, http.StatusServiceUnavailable, do(t, h, http.MethodGet, "name.test", "/function/api/x", "orders").StatusCode)
	require.Zero(t, hits.Load())
	require.Zero(t, scaler.count())
}

// scenario: a-health-path-is-read-resolved
func TestScenarioAHealthPathIsReadResolved(t *testing.T) {
	t.Parallel()
	worker := &workerPaths{}
	h, st, scaler := authDoorUpstream(t, warmAPI(), healthRoutes(), creds{}, worker)
	seedFunction(t, st, "api")
	for _, remainder := range []string{
		"/./health/readiness", "/x/../health/readiness", "/../health/readiness", "//health/readiness", "/health//readiness",
		"/health/./readiness", "/health/x/../readiness", "/%2e/health/readiness", "/%2e%2e/health/readiness",
		"/health%2Freadiness", "/%68ealth/readiness",
		"/health/", "/health/readiness/",
	} {
		requireNoRoute(t, do(t, h, http.MethodGet, "name.test", "/function/api"+remainder, ""), "/function/api"+remainder)
		requireNoRoute(t, do(t, h, http.MethodGet, "prefix.test", "/api"+remainder, ""), "/api"+remainder)
		requireNoRoute(t, do(t, h, http.MethodGet, "root.test", remainder, ""), remainder)
	}
	requireNoRoute(t, do(t, h, http.MethodGet, "name.test", "/function/api/health/readiness?x=1", ""), "/function/api/health/readiness?x=1")
	require.Empty(t, worker.received())
	require.Zero(t, scaler.count())
}

// scenario: the-stance-answers-first
func TestScenarioTheStanceAnswersFirst(t *testing.T) {
	t.Parallel()
	worker := &workerPaths{}
	tokens := creds{
		"tok":   {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team", "default"}},
		"other": {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"elsewhere"}},
	}
	entries := []router.Entry{{Namespace: "team", Auth: v1.AuthAuthenticated, Rules: []router.CompiledRule{{Path: "/api", Function: "api"}}}}
	h, st, scaler := authDoorUpstream(t, nil, entries, tokens, worker)
	seedNSAuth(t, st, "team", v1.ExposureImplicit, v1.AuthAuthenticated)
	seedFn(t, st, "team", "api")
	seedFnIngress(t, st, "default", "own", &v1.Ingress{Auth: &v1.RouteAuth{Mode: v1.AuthAuthenticated}})

	for _, tc := range []struct{ path, ns string }{
		{"/function/api/health/readiness", "team"},
		{"/function/missing/health/readiness", "team"},
		{"/api/health/readiness", "team"},
		{"/function/own/health/readiness", "default"},
	} {
		require.Equal(t, http.StatusUnauthorized, authReq(t, h, tc.path, tc.ns, "").StatusCode, "no credential on %s", tc.path)
		require.Equal(t, http.StatusForbidden, authReq(t, h, tc.path, tc.ns, "other").StatusCode, "another namespace's credential on %s", tc.path)
		requireNoRoute(t, authReq(t, h, tc.path, tc.ns, "tok"), tc.path)
	}
	require.Empty(t, worker.received())
	require.Zero(t, scaler.count())
}

// scenario: other-paths-reach-the-worker
func TestScenarioOtherPathsReachTheWorker(t *testing.T) {
	t.Parallel()
	for _, protocol := range []v1.FunctionProtocol{v1.ProtocolCloudEvent, v1.ProtocolHTTP} {
		worker := &workerPaths{}
		entries := []router.Entry{
			{Namespace: "default", Host: "prefix.test", Rules: []router.CompiledRule{{Path: "/health", Function: "api"}}},
			{Namespace: "default", Host: "exact.test", Rules: []router.CompiledRule{{Path: "/health/readiness", Exact: true, Function: "api"}}},
		}
		h, st, _ := authDoorUpstream(t, warmAPI(), entries, creds{}, worker)
		seedProtocol(t, st, "api", protocol)
		for _, tc := range []struct{ host, path, want string }{
			{"name.test", "/function/api/health", "/health"},
			{"name.test", "/function/api/health/.", "/health/."},
			{"name.test", "/function/api/healthz", "/healthz"},
			{"name.test", "/function/api/health-check", "/health-check"},
			{"name.test", "/function/api/healthz/readiness", "/healthz/readiness"},
			{"name.test", "/function/api/devices/health/readiness", "/devices/health/readiness"},
			{"name.test", "/function/api/HEALTH/readiness", "/HEALTH/readiness"},
			{"name.test", "/function/api", "/"},
			{"prefix.test", "/health/readiness", "/readiness"},
			{"exact.test", "/health/readiness", "/"},
		} {
			require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, tc.host, tc.path, "").StatusCode, tc.path)
			got := worker.received()
			require.Equal(t, tc.want, got[len(got)-1], "%s %s on a %s Function", tc.host, tc.path, protocol)
		}
	}
}

// scenario: another-function-keeps-the-path
func TestScenarioAnotherFunctionKeepsThePath(t *testing.T) {
	t.Parallel()
	worker := &workerPaths{}
	h, st, _ := authDoorUpstream(t, warmAPI(), nil, creds{}, worker)
	seedProtocol(t, st, "api", v1.ProtocolHTTP)
	req := httptest.NewRequest(http.MethodGet, "http://name.test/function/api/health/readiness", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(dataplane.WithInternal(req.Context())))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"/health/readiness"}, worker.received())
}
