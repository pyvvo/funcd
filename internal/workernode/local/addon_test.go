package local_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// ADR-0338: a Function calls an add-on object's engine through funcd, on its local API and through the add-on proxy.

// engineSeen is one request the engine got.
type engineSeen struct {
	method, path, query string
	header              http.Header
	body                string
}

// addonEngine records each request and answers with answer, or hangs while hang is set.
type addonEngine struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []engineSeen
}

func newAddonEngine(t *testing.T, answer http.HandlerFunc) *addonEngine {
	t.Helper()
	e := &addonEngine{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.seen = append(e.seen, engineSeen{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone(), body: string(b)})
		e.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *addonEngine) last(t *testing.T) engineSeen {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	require.NotEmpty(t, e.seen, "the engine got the call")
	return e.seen[len(e.seen)-1]
}

func (e *addonEngine) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.seen)
}

// fakeAddon is the port: alias idp of default/rauthy-settings reaches the engine, any other alias is refused, and err,
// when set, refuses every call.
type fakeAddon struct {
	engine string
	err    error
}

func (f fakeAddon) Target(_ context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, path string) (local.AddonTarget, error) {
	switch {
	case f.err != nil:
		return local.AddonTarget{}, f.err
	case ns != "default" || fn != "rauthy-settings" || alias != "idp":
		return local.AddonTarget{}, local.ErrAddonNoBinding.Errorf("test", "no binding %q", alias)
	}
	return local.AddonTarget{Address: strings.TrimPrefix(f.engine, "http://"), Path: path}, nil
}

func facetHandler(a local.Addon, limits local.AddonLimits) http.Handler {
	return local.NewHandler(local.Ref{Namespace: "default", Function: "rauthy-settings"}, nil, nil, nil, nil, nil,
		local.WithFacets(local.AddonFacet(a, limits)))
}

func proxyHandler(a local.Addon, limits local.AddonLimits) http.Handler {
	return local.NewAddonProxy(a, func(token string) (local.Ref, bool) {
		return local.Ref{Namespace: "default", Function: "rauthy-settings"}, token == "the-token"
	}, limits, nil)
}

func call(t *testing.T, h http.Handler, method, path string, header http.Header, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Result()
}

// detail is the problem's detail: the wire names a kind, and the detail says which refusal it is.
func detail(t *testing.T, res *http.Response) string {
	t.Helper()
	var p fault.Problem
	require.NoError(t, json.NewDecoder(res.Body).Decode(&p))
	return p.Detail
}

// scenario: addon-call — ctx.addon.fetch("idp", path, init) with a header of the handler's own: the engine gets it,
// and the handler gets the engine's status, headers and body, with the reply header.
func TestScenarioAddonCall(t *testing.T) {
	t.Parallel()
	e := newAddonEngine(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Policy", "strict")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"lengthMin":16}`)
	})
	res := call(t, facetHandler(fakeAddon{engine: e.srv.URL}, local.AddonLimits{}), http.MethodPut,
		"/addon/idp/auth/v1/password_policy?x=1", http.Header{"Api-Key": {"rauthy-api-key"}}, `{"lengthMin":16}`)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	require.Equal(t, "1", res.Header.Get(local.AddonReplyHeader))
	require.Equal(t, "strict", res.Header.Get("X-Policy"))
	b, _ := io.ReadAll(res.Body)
	require.Equal(t, `{"lengthMin":16}`, string(b))
	seen := e.last(t)
	require.Equal(t, http.MethodPut, seen.method)
	require.Equal(t, "/auth/v1/password_policy", seen.path)
	require.Equal(t, "x=1", seen.query)
	require.Equal(t, "rauthy-api-key", seen.header.Get("Api-Key"))
	require.Equal(t, `{"lengthMin":16}`, seen.body)
}

// scenario: addon-call-sdk — a plain HTTP client at the proxy with the token header and its own Authorization: the
// engine gets the Authorization and no token header, and the client gets the engine's answer.
func TestScenarioAddonCallSDK(t *testing.T) {
	t.Parallel()
	e := newAddonEngine(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	res := call(t, proxyHandler(fakeAddon{engine: e.srv.URL}, local.AddonLimits{}), http.MethodGet,
		"/addon/idp/auth/v1/users", http.Header{local.AddonTokenHeader: {"the-token"}, "Authorization": {"API-Key abc"}}, "")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "1", res.Header.Get(local.AddonReplyHeader))
	seen := e.last(t)
	require.Equal(t, "API-Key abc", seen.header.Get("Authorization"))
	require.Empty(t, seen.header.Values(local.AddonTokenHeader), "the token never reaches the engine")
}

// scenario: addon-call-refused — an alias the caller does not bind, a Cedar denial, and a proxy request with no token
// or a token of no Function: 403, no reply header, and no engine call.
func TestScenarioAddonCallRefused(t *testing.T) {
	t.Parallel()
	e := newAddonEngine(t, func(http.ResponseWriter, *http.Request) {})
	for name, c := range map[string]struct {
		h      http.Handler
		path   string
		header http.Header
		want   string
	}{
		"an alias not bound": {facetHandler(fakeAddon{engine: e.srv.URL}, local.AddonLimits{}), "/addon/other/x", nil, `no binding "other"`},
		"a Cedar denial": {facetHandler(fakeAddon{err: local.ErrAddonForbidden.Errorf("test", "forbidden by a Policy")}, local.AddonLimits{}),
			"/addon/idp/x", nil, "forbidden by a Policy"},
		"no token":          {proxyHandler(fakeAddon{engine: e.srv.URL}, local.AddonLimits{}), "/addon/idp/x", nil, "no token of a Function"},
		"a token of no one": {proxyHandler(fakeAddon{engine: e.srv.URL}, local.AddonLimits{}), "/addon/idp/x", http.Header{local.AddonTokenHeader: {"forged"}}, "no token of a Function"},
	} {
		res := call(t, c.h, http.MethodGet, c.path, c.header, "")
		require.Equal(t, http.StatusForbidden, res.StatusCode, name)
		require.Empty(t, res.Header.Get(local.AddonReplyHeader), name)
		require.Contains(t, detail(t, res), c.want, name)
	}
	require.Zero(t, e.calls(), "no engine is called")
}

// scenario: addon-call-held — the caller's namespace held: 503 with Retry-After: 30.
// oracle: docs/design/addon/restore-start.fizz#NoAckedWriteRolledBack
func TestScenarioAddonCallHeld(t *testing.T) {
	t.Parallel()
	for _, h := range []http.Handler{
		facetHandler(fakeAddon{err: local.ErrAddonHeld.Errorf("test", "held")}, local.AddonLimits{}),
		proxyHandler(fakeAddon{err: local.ErrAddonHeld.Errorf("test", "held")}, local.AddonLimits{}),
	} {
		res := call(t, h, http.MethodPost, "/addon/idp/x", http.Header{local.AddonTokenHeader: {"the-token"}}, "{}")
		require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
		require.Equal(t, "30", res.Header.Get("Retry-After"))
		require.Empty(t, res.Header.Get(local.AddonReplyHeader))
	}
}

// scenario: addon-call-timeout — an engine that does not answer within addons.callTimeout: 503, local.addon-timeout.
func TestScenarioAddonCallTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	e := newAddonEngine(t, func(http.ResponseWriter, *http.Request) { <-release })
	t.Cleanup(func() { close(release) }) // before the engine's Close, which waits for the held call
	res := call(t, facetHandler(fakeAddon{engine: e.srv.URL}, local.AddonLimits{Timeout: 100 * time.Millisecond}),
		http.MethodGet, "/addon/idp/slow", nil, "")
	require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
	require.Contains(t, detail(t, res), "did not answer within 100ms")
}

// scenario: addon-call-too-large — a request body or an answer body over addons.callPayloadLimit: 413.
func TestScenarioAddonCallTooLarge(t *testing.T) {
	t.Parallel()
	e := newAddonEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			_, _ = io.WriteString(w, strings.Repeat("a", 2048))
		}
	})
	h := facetHandler(fakeAddon{engine: e.srv.URL}, local.AddonLimits{Payload: 1024})
	res := call(t, h, http.MethodPost, "/addon/idp/small", nil, strings.Repeat("b", 2048))
	require.Equal(t, http.StatusRequestEntityTooLarge, res.StatusCode)
	require.Contains(t, detail(t, res), "request body exceeds 1024 bytes")
	require.Zero(t, e.calls(), "a body over the limit is not sent")
	res = call(t, h, http.MethodGet, "/addon/idp/big", nil, "")
	require.Equal(t, http.StatusRequestEntityTooLarge, res.StatusCode)
	require.Contains(t, detail(t, res), "is larger than 1024 bytes")
}

// scenario: addon-call-headers — the forwarding headers and every X-Funcd-* header reach no engine, either way, and a
// 3xx reaches the caller unfollowed.
func TestScenarioAddonCallHeaders(t *testing.T) {
	t.Parallel()
	e := newAddonEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Funcd-Fake", "from the engine")
		w.Header().Set("Location", "/elsewhere")
		w.WriteHeader(http.StatusFound)
	})
	res := call(t, facetHandler(fakeAddon{engine: e.srv.URL}, local.AddonLimits{}), http.MethodGet, "/addon/idp/login",
		http.Header{"X-Forwarded-For": {"203.0.113.7"}, "Forwarded": {"for=203.0.113.7"}, "X-Real-Ip": {"203.0.113.7"},
			"X-Forwarded-Host": {"evil.example"}, "X-Forwarded-Proto": {"https"}, "X-Funcd-Member": {"m"}, "Keep": {"yes"}}, "")
	require.Equal(t, http.StatusFound, res.StatusCode, "no redirect is followed")
	require.Equal(t, "/elsewhere", res.Header.Get("Location"))
	require.Empty(t, res.Header.Get("X-Funcd-Fake"), "an engine's X-Funcd-* header is dropped too")
	require.Equal(t, "1", res.Header.Get(local.AddonReplyHeader))
	seen := e.last(t)
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Funcd-Member"} {
		require.Empty(t, seen.header.Values(h), h)
	}
	require.Equal(t, "yes", seen.header.Get("Keep"))
	require.Equal(t, 1, e.calls())
}

// The proxy answers a path that names no alias with 404, local.addon-path-refused.
func TestAddonProxyPathWithNoAlias(t *testing.T) {
	t.Parallel()
	res := call(t, proxyHandler(fakeAddon{}, local.AddonLimits{}), http.MethodGet, "/other", http.Header{local.AddonTokenHeader: {"the-token"}}, "")
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	require.Contains(t, detail(t, res), "names no alias")
}

// Review checklist of ADR-0338: no answer or log of funcd names the engine's address, when the engine refuses the
// connection (local.addon-engine-unavailable) or does not answer in time (local.addon-timeout), on either path.
func TestAddonCallNamesNoEngineAddress(t *testing.T) {
	t.Parallel()
	closed := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(closed.URL, "http://")
	closed.Close()
	release := make(chan struct{})
	slow := newAddonEngine(t, func(http.ResponseWriter, *http.Request) { <-release })
	t.Cleanup(func() { close(release) })
	for name, c := range map[string]struct {
		engine string
		status int
	}{
		"a refused connection": {closed.URL, http.StatusServiceUnavailable},
		"no answer in time":    {slow.srv.URL, http.StatusServiceUnavailable},
	} {
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		limits := local.AddonLimits{Timeout: 100 * time.Millisecond}
		facet := local.NewHandler(local.Ref{Namespace: "default", Function: "rauthy-settings"}, nil, nil, nil, nil, logger,
			local.WithFacets(local.AddonFacet(fakeAddon{engine: c.engine}, limits)))
		proxy := local.NewAddonProxy(fakeAddon{engine: c.engine}, func(string) (local.Ref, bool) {
			return local.Ref{Namespace: "default", Function: "rauthy-settings"}, true
		}, limits, logger)
		for _, h := range []http.Handler{facet, proxy} {
			res := call(t, h, http.MethodGet, "/addon/idp/x", http.Header{local.AddonTokenHeader: {"t"}}, "")
			require.Equal(t, c.status, res.StatusCode, name)
			b, _ := io.ReadAll(res.Body)
			require.NotContains(t, string(b), strings.TrimPrefix(slow.srv.URL, "http://"), name)
			require.NotContains(t, string(b), addr, name)
		}
		require.NotContains(t, logs.String(), addr, name)
		require.NotContains(t, logs.String(), strings.TrimPrefix(slow.srv.URL, "http://"), name)
	}
}

// ADR-0338 Decision 6: an answer the engine cuts off is 503 local.addon-reply-cut-off, and a request body funcd cannot
// read is 400 local.body-unreadable, with no reply header either way.
func TestAddonCallCutOffAndUnreadable(t *testing.T) {
	t.Parallel()
	cut := newAddonEngine(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
		_ = buf.Flush()
		_ = conn.Close()
	})
	h := facetHandler(fakeAddon{engine: cut.srv.URL}, local.AddonLimits{})
	res := call(t, h, http.MethodGet, "/addon/idp/x", nil, "")
	require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
	require.Empty(t, res.Header.Get(local.AddonReplyHeader))
	require.Contains(t, detail(t, res), "cut off")

	req := httptest.NewRequest(http.MethodPost, "/addon/idp/x", io.MultiReader(strings.NewReader("{"), failingReader{}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Empty(t, w.Header().Get(local.AddonReplyHeader))
}

// failingReader fails every read, as a client that drops its upload does.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the client went away") }
