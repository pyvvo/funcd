package dataplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/authn"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/edge/static"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const (
	siteFile     = "SITE-FILE"
	workerAnswer = "WORKER-ANSWER"
	readCause    = "disk read failed"
)

// namespaceReads is a store whose reads of a Namespace can answer (obj, err): the read numbered only, counted from 1
// once armed, or every read when only is 0.
type namespaceReads struct {
	store.Store
	obj   v1.Object
	err   error
	only  int
	reads int
}

func (s *namespaceReads) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if gvk.Kind == v1.KindNamespace && (s.err != nil || s.obj != nil) {
		s.reads++
		if s.only == 0 || s.only == s.reads {
			return s.obj, s.err
		}
	}
	return s.Store.Get(ctx, gvk, ns, name)
}

// namespaceReadDoor is a data plane with the PEP wired, over a namespaceReads store that holds the Functions below and
// no Namespace. It returns the count of calls that reached a worker.
func namespaceReadDoor(t *testing.T, logger *slog.Logger) (http.Handler, *namespaceReads, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, workerAnswer)
	}))
	t.Cleanup(up.Close)
	inner := store.New(memory.New())
	st := &namespaceReads{Store: inner}
	for name, ing := range map[string]*v1.Ingress{
		"api":      nil,
		"opened":   {Enabled: new(true)},
		"closed":   {Enabled: new(false)},
		"custom":   {Host: "api.example.com"},
		"own-open": {Auth: &v1.RouteAuth{Mode: v1.AuthOpen}},
	} {
		seedFnIngress(t, st, "team", name, ing)
	}
	warm := map[v1.ObjectName]string{"api": "u", "opened": "u", "closed": "u", "custom": "u", "own-open": "u"}
	act, err := activator.New(activator.Deps{Store: inner, Endpoints: readyEndpoints{warm: warm, upstream: up.URL}, Scaler: noScaler{}})
	require.NoError(t, err)

	site := &v1.StaticBackend{Bucket: "web", Prefix: "web/", Index: "index.html"}
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{Namespace: "team", Rules: []router.CompiledRule{
		{Path: "/api", Function: "api"},
		{Path: "/site", Static: site},
		{Path: "/open-api", Function: "api", Auth: v1.AuthOpen},
		{Path: "/open-site", Static: site, Auth: v1.AuthOpen},
		{Path: "/guarded-api", Function: "api", Auth: v1.AuthAuthenticated},
	}}}, nil))

	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bucket.Close() })
	require.NoError(t, bucket.Put(context.Background(), "web/index.html", []byte(siteFile), blob.PutOptions{}))
	sh, err := static.New(static.Deps{Buckets: func(v1.NamespaceName, string) (blob.Bucket, bool) { return bucket, true }})
	require.NoError(t, err)

	tokens := creds{"tok": {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team"}}}
	enf, err := authn.New(authn.Deps{Creds: tokens, Authz: rbac.New()})
	require.NoError(t, err)
	return dataplane.Handler(st, act, rtr, "", enf, nil, sh, 0, logger), st, hits
}

func callTeam(h http.Handler, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://team.localhost"+path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// A failed read of a Namespace refuses the call before anything is served, on every path that reads it: its stance and
// exposure are unknown, as ADR-0176 Decision 2 rules for the edge aggregator. The namespace here is explicit and
// authenticated, so whenRead is what the call answers while the read works.
func TestNamespaceRead_FailedReadRefusesTheCall(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, path, token string
		only, whenRead    int
	}{
		{name: "a Route to a Function, with no stance", path: "/api", whenRead: http.StatusUnauthorized},
		{name: "a Route to a Function, with a token", path: "/api", token: "tok", whenRead: http.StatusOK},
		{name: "a static Route, with no stance", path: "/site/", whenRead: http.StatusUnauthorized},
		{name: "a static Route, with a token", path: "/site/", token: "tok", whenRead: http.StatusOK},
		{name: "by name, a missing Function", path: "/function/missing", whenRead: http.StatusUnauthorized},
		{name: "by name, an address of its own", path: "/function/custom", whenRead: http.StatusUnauthorized},
		{name: "by name, on, the exposure read", path: "/function/opened", only: 1, whenRead: http.StatusUnauthorized},
		{name: "by name, on, the stance read", path: "/function/opened", only: 2, whenRead: http.StatusUnauthorized},
		{name: "by name, on, with a token", path: "/function/opened", token: "tok", whenRead: http.StatusOK},
		{name: "by name, off, the exposure read", path: "/function/closed", only: 1, whenRead: http.StatusUnauthorized},
		{name: "by name, off, the stance read", path: "/function/closed", only: 2, whenRead: http.StatusUnauthorized},
		{name: "by name, off by the namespace, with its own stance", path: "/function/own-open", whenRead: http.StatusUnauthorized},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, st, hits := namespaceReadDoor(t, nil)
			seedNSAuth(t, st, "team", v1.ExposureExplicit, v1.AuthAuthenticated)
			require.Equal(t, c.whenRead, callTeam(h, c.path, c.token).Code, "while the read works")

			hits.Store(0)
			st.err, st.only = errors.New(readCause), c.only
			rec := callTeam(h, c.path, c.token)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			require.Zero(t, hits.Load(), "a refused call reaches no worker")
			require.NotContains(t, rec.Body.String(), siteFile)
		})
	}
}

// The refusal is the same 503 whatever the read answered. It comes before the PEP, so its detail names neither the
// cause nor the namespace; both stay in the log.
func TestNamespaceRead_RefusalNamesOnlyAnUnavailableEdge(t *testing.T) {
	t.Parallel()
	for name, read := range map[string]namespaceReads{
		"an error with no kind": {err: errors.New(readCause)},
		"an unavailable store":  {err: fault.Unavailablef("store.Get", readCause)},
		"an internal fault":     {err: fault.Internalf("store.Get", readCause)},
		"a forbidden fault":     {err: fault.Forbiddenf("store.Get", readCause)},
		"an object of the wrong kind": {
			obj: &v1.Function{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			h, st, _ := namespaceReadDoor(t, slog.New(slog.NewTextHandler(&logs, nil)))
			st.obj, st.err = read.obj, read.err
			rec := callTeam(h, "/site/", "")

			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
			var p struct{ Type, Detail string }
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
			require.Equal(t, "urn:funcd:problem:unavailable", p.Type)
			require.NotContains(t, p.Detail, readCause)
			require.NotContains(t, p.Detail, "team")

			require.Contains(t, logs.String(), "namespace=team")
			if read.err != nil {
				require.Contains(t, logs.String(), readCause)
			}
		})
	}
}

// The reads a call makes before its PEP take a context that cannot end, so every call here answers the PEP's 401,
// whatever the request's context has become. The namespace is explicit and authenticated.
func TestNamespaceRead_ContextThatEndedChangesNoAnswer(t *testing.T) {
	t.Parallel()
	h, st, hits := namespaceReadDoor(t, nil)
	seedNSAuth(t, st, "team", v1.ExposureExplicit, v1.AuthAuthenticated)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, path := range []string{
		"/api", "/site/", "/function/missing", "/function/custom", "/function/opened", "/function/closed", "/function/own-open",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://team.localhost"+path, nil).WithContext(ctx))
		require.Equal(t, http.StatusUnauthorized, rec.Code, "%s: %s", path, rec.Body.String())
	}
	require.Zero(t, hits.Load(), "no call reaches a worker")
}

// A call whose answer does not depend on the Namespace is not refused for it: a Route rule with its own stance reads
// no Namespace, and neither does an internal call.
func TestNamespaceRead_OwnStanceNeedsNoRead(t *testing.T) {
	t.Parallel()
	h, st, _ := namespaceReadDoor(t, nil)
	seedNSAuth(t, st, "team", v1.ExposureExplicit, v1.AuthAuthenticated)
	st.err = errors.New(readCause)

	rec := callTeam(h, "/open-api", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, workerAnswer, rec.Body.String())
	rec = callTeam(h, "/open-site/", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, siteFile, rec.Body.String())
	require.Equal(t, http.StatusUnauthorized, callTeam(h, "/guarded-api", "").Code)
	require.Equal(t, http.StatusOK, callTeam(h, "/guarded-api", "tok").Code)

	r := httptest.NewRequest(http.MethodPost, "http://any/function/closed", nil)
	r.Header.Set("X-Funcd-Namespace", "team")
	r = r.WithContext(dataplane.WithInternal(r.Context()))
	internal := httptest.NewRecorder()
	h.ServeHTTP(internal, r)
	require.Equal(t, http.StatusOK, internal.Code, internal.Body.String())
}

// ADR-0110 Decision 6.3, ADR-0113 Decision 3 and ADR-0241 Decision 8: only an absent Namespace reads as unset, implicit
// and open, on every path.
func TestNamespaceRead_AbsentNamespaceIsUnset(t *testing.T) {
	t.Parallel()
	h, _, _ := namespaceReadDoor(t, nil)

	for _, path := range []string{"/api", "/function/api", "/function/opened", "/function/own-open"} {
		rec := callTeam(h, path, "")
		require.Equal(t, http.StatusOK, rec.Code, path)
		require.Equal(t, workerAnswer, rec.Body.String(), path)
	}
	rec := callTeam(h, "/site/", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, siteFile, rec.Body.String())
	for _, path := range []string{"/function/missing", "/function/closed", "/function/custom"} {
		require.Equal(t, http.StatusNotFound, callTeam(h, path, "").Code, path)
	}
}
