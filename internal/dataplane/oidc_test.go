package dataplane_test

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/oidc"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/oidcprovider"
)

// signInEdge is the data plane with the edge's sign-in over an in-test provider, the namespace staging and its
// OIDCProviders, and a warm Function app whose upstream records the headers of each call it gets.
type signInEdge struct {
	t       *testing.T
	st      store.Store
	idp     *oidcprovider.Provider
	agg     *router.Aggregator
	handler http.Handler
	scaler  *spyScaler

	mu   sync.Mutex
	seen []http.Header
}

func newSignInEdge(t *testing.T) *signInEdge {
	t.Helper()
	e := &signInEdge{t: t, st: store.New(memory.New()), idp: oidcprovider.New(t, oidcprovider.Options{}), scaler: &spyScaler{}}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.seen = append(e.seen, r.Header.Clone())
		e.mu.Unlock()
		_, _ = io.WriteString(w, "app")
	}))
	t.Cleanup(up.Close)
	act, err := activator.New(activator.Deps{Store: e.st, Endpoints: readyEndpoints{warm: map[v1.ObjectName]string{"app": "u"}, upstream: up.URL}, Scaler: e.scaler})
	require.NoError(t, err)
	rtr := router.New()
	e.agg = router.NewAggregator(rtr, nil, nil, "localhost", nil)
	gate := oidc.New(oidc.Deps{Store: e.st, CallbackURL: func(host string) string { return "https://" + host + "/.auth/callback" },
		HTTPS: true, BackChannel: e.idp.Transport()})
	e.handler = dataplane.Handler(e.st, act, rtr, "localhost", nil, nil, nil, 0, nil, dataplane.WithOIDC(gate))
	seedNS(t, e.st, "staging", v1.ExposureImplicit)
	seedFn(t, e.st, "staging", "app")
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	e.create(secret("sso-session", map[string][]byte{"SESSION_KEY": key}))
	return e
}

func (e *signInEdge) create(obj v1.Object) {
	e.t.Helper()
	_, err := e.st.Create(context.Background(), obj)
	require.NoError(e.t, err)
}

func secret(name v1.ObjectName, data map[string][]byte) *v1.Secret {
	s := &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret}}
	s.Name, s.Namespace, s.ResourceGroup = name, "staging", "rg1"
	s.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: data}
	return s
}

// provider seeds the OIDCProvider name with a client made by hand at the in-test provider, for the hosts' callbacks.
func (e *signInEdge) provider(name v1.ObjectName, audiences []string, hosts ...string) {
	e.t.Helper()
	creds := name + "-client"
	e.create(secret(creds, map[string][]byte{"CLIENT_ID": []byte(creds), "CLIENT_SECRET": []byte("s3cret")}))
	var callbacks []string
	for _, h := range hosts {
		callbacks = append(callbacks, "https://"+h+"/.auth/callback")
	}
	e.idp.AddClient(string(creds), "s3cret", callbacks...)
	p := &v1.OIDCProvider{TypeMeta: v1.TypeMeta{APIVersion: v1.KindOIDCProvider.GVK().APIVersion(), Kind: v1.KindOIDCProvider}}
	p.Name, p.Namespace, p.ResourceGroup = name, "staging", "rg1"
	p.Spec = v1.OIDCProviderSpec{Issuer: e.idp.Issuer(), Audiences: audiences, SessionSecret: "sso-session",
		Client: v1.OIDCProviderClient{CredentialsSecret: creds}}
	e.create(p)
}

// program sets the Route entries on the aggregator and returns their verdicts.
func (e *signInEdge) program(entries ...router.Entry) []router.Verdict {
	e.t.Helper()
	v, err := e.agg.Set(context.Background(), "routes", entries)
	require.NoError(e.t, err)
	return v
}

func entry(name v1.ObjectName, host string, rules ...router.CompiledRule) router.Entry {
	return router.Entry{Namespace: "staging", Host: host, Rules: rules, Owner: router.Owner{Kind: v1.KindRoute, Namespace: "staging", Name: name}}
}

func oidcRule(path string, provider v1.ObjectName, un v1.Unauthenticated) router.CompiledRule {
	return router.CompiledRule{Path: path, Function: "app", Auth: v1.AuthOIDC, OIDC: &router.OIDCAuth{Provider: provider, Unauthenticated: un}}
}

// call sends one request to the data plane; cookies are the Cookie header's pairs.
func (e *signInEdge) call(method, host, path string, header map[string]string, cookies ...*http.Cookie) *http.Response {
	e.t.Helper()
	req := httptest.NewRequest(method, "https://"+host+path, nil)
	req.Host = host
	for k, v := range header {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec.Result()
}

func (e *signInEdge) last() http.Header {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	require.NotEmpty(e.t, e.seen, "the Function was reached")
	return e.seen[len(e.seen)-1]
}

func (e *signInEdge) reached() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.seen) }

// signIn signs a browser in at path on host and returns its session cookie.
func (e *signInEdge) signIn(host, path string) *http.Cookie {
	e.t.Helper()
	resp := e.call(http.MethodGet, host, path, map[string]string{"Sec-Fetch-Mode": "navigate"})
	require.Equal(e.t, http.StatusFound, resp.StatusCode)
	state := resp.Cookies()[0]
	authorize, err := http.NewRequestWithContext(context.Background(), http.MethodGet, resp.Header.Get("Location"), nil)
	require.NoError(e.t, err)
	back, err := (&http.Client{Transport: e.idp.Transport(), CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(authorize)
	require.NoError(e.t, err)
	_ = back.Body.Close()
	require.Equal(e.t, http.StatusFound, back.StatusCode)
	callback, err := url.Parse(back.Header.Get("Location"))
	require.NoError(e.t, err)
	require.Equal(e.t, host, callback.Host)
	resp = e.call(http.MethodGet, host, callback.RequestURI(), nil, state)
	require.Equal(e.t, http.StatusFound, resp.StatusCode, "the sign-in completes")
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "__Host-funcd_session.") {
			return c
		}
	}
	e.t.Fatal("the callback set no session")
	return nil
}

// scenario: oidc-forged-headers
func TestScenarioOidcForgedHeaders(t *testing.T) {
	t.Parallel()
	e := newSignInEdge(t)
	e.provider("sso", []string{"api"}, "app.example.com")
	e.program(entry("app", "app.example.com", oidcRule("/", "sso", v1.UnauthenticatedAllow)))
	forged := map[string]string{oidc.HeaderPrincipal: "Zm9yZ2Vk", oidc.HeaderPrincipalID: "admin", oidc.HeaderPrincipalName: "Admin",
		oidc.HeaderIDToken: "forged"}
	only := func(h http.Header) {
		t.Helper()
		for k := range forged {
			require.Empty(t, h.Get(k), "%s never reaches the Function as sent", k)
		}
	}

	resp := e.call(http.MethodPost, "app.example.com", "/", forged)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	only(e.last())

	token := e.idp.Token(oidcprovider.RS256, e.idp.Claims("api", ""))
	withToken := map[string]string{"Authorization": "Bearer " + token}
	for k, v := range forged {
		withToken[k] = v
	}
	resp = e.call(http.MethodPost, "app.example.com", "/", withToken)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "user-1", e.last().Get(oidc.HeaderPrincipalID), "the Function sees the edge's value")
	require.NotEqual(t, "forged", e.last().Get(oidc.HeaderIDToken))

	resp = e.call(http.MethodPost, "staging.localhost", "/function/app", forged)
	require.Equal(t, http.StatusOK, resp.StatusCode, "by name")
	only(e.last())

	req := httptest.NewRequest(http.MethodPost, "http://in/function/app", nil).WithContext(dataplane.WithInternal(context.Background()))
	req.Header.Set("X-Funcd-Namespace", "staging")
	for k, v := range forged {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "an internal call")
	only(e.last())
}

// scenario: oidc-other-provider-session
func TestScenarioOidcOtherProviderSession(t *testing.T) {
	t.Parallel()
	e := newSignInEdge(t)
	e.provider("sso", nil, "app.example.com")
	e.provider("public", nil, "b.example.com")
	verdicts := e.program(
		entry("app", "app.example.com", oidcRule("/", "sso", "")),
		entry("b", "b.example.com", oidcRule("/", "public", "")),
		entry("zz", "app.example.com", oidcRule("/b", "public", "")),
	)
	reasons := map[v1.ObjectName]string{}
	for _, v := range verdicts {
		reasons[v.Owner.Name] = v.Reason
	}
	require.Equal(t, map[v1.ObjectName]string{"app": "", "b": "", "zz": router.ReasonProviderConflict}, reasons)

	public := e.signIn("b.example.com", "/")
	resp := e.call(http.MethodGet, "b.example.com", "/", nil, public)
	require.Equal(t, http.StatusOK, resp.StatusCode, "public's session works on its own host")
	forged := &http.Cookie{Name: "__Host-funcd_session.staging.sso", Value: public.Value}
	resp = e.call(http.MethodGet, "app.example.com", "/", map[string]string{"Sec-Fetch-Mode": "navigate"}, forged)
	require.Equal(t, http.StatusFound, resp.StatusCode, "public's session under sso's name counts as none")

	before := e.reached()
	resp = e.call(http.MethodGet, "app.example.com", "/b/x", nil, e.signIn("app.example.com", "/"))
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "zz answers 503 at /b")
	require.Equal(t, before, e.reached(), "and reaches nothing")
}

// scenario: oidc-auth-paths
func TestScenarioOidcAuthPaths(t *testing.T) {
	t.Parallel()
	e := newSignInEdge(t)
	e.provider("sso", nil, "app.example.com")
	e.program(entry("app", "app.example.com", oidcRule("/api", "sso", ""),
		router.CompiledRule{Path: "/", Function: "app", Auth: v1.AuthOpen}))

	session := e.signIn("app.example.com", "/api/orders")
	resp := e.call(http.MethodGet, "app.example.com", "/api/orders", nil, session)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the sign-in at /api completed at /.auth/callback")
	require.Equal(t, "user-1", e.last().Get(oidc.HeaderPrincipalID))
	resp = e.call(http.MethodGet, "app.example.com", "/.auth/elsewhere", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "/.auth/ is the edge's, not the / rule's")

	route := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	route.Name, route.Namespace, route.ResourceGroup = "x", "staging", "rg1"
	route.Spec = v1.RouteSpec{Host: "app.example.com", Rules: []v1.RouteRule{{Path: "/.auth/x", Backend: v1.RouteBackend{Function: "app"}}}}
	require.Equal(t, fault.Invalid, fault.KindOf(route.Validate()), "a rule at /.auth/x")
	route.Namespace = "default"
	route.Spec = v1.RouteSpec{Auth: &v1.RouteAuth{Mode: v1.AuthOIDC, Provider: "sso"}, Rules: []v1.RouteRule{{Path: "/", Backend: v1.RouteBackend{Function: "app"}}}}
	require.Equal(t, fault.Invalid, fault.KindOf(route.Validate()), "a Route of default with oidc and no host")
	route.Spec.Host = "app.example.com"
	require.NoError(t, route.Validate())
}

// ADR-0335 Decision 10: an entry refused for its provider answers 503 on its paths and wakes nothing; another rule
// keeps its own.
func TestRefusedOIDCEntryAnswers503(t *testing.T) {
	t.Parallel()
	e := newSignInEdge(t)
	www := entry("www", "app.example.com", router.CompiledRule{Path: "/", Function: "app", Auth: v1.AuthOpen})
	admin := entry("admin", "app.example.com", oidcRule("/admin", "missing", ""))
	admin.Refused = router.ReasonProviderNotFound
	e.program(www, admin)
	resp := e.call(http.MethodGet, "app.example.com", "/admin/x", nil)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Zero(t, e.reached())
	resp = e.call(http.MethodGet, "app.example.com", "/elsewhere", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Zero(t, e.scaler.count())
}

// ADR-0335 Decision 2: a namespace's edgeDefaults.auth reaches a by-name call, and a missing provider answers 503.
func TestNamespaceOIDCStanceReachesTheByNamePath(t *testing.T) {
	t.Parallel()
	e := newSignInEdge(t)
	e.provider("sso", nil, "staging.localhost")
	obj, err := e.st.Get(context.Background(), v1.KindNamespace.GVK(), "", "staging")
	require.NoError(t, err)
	n := obj.(*v1.Namespace)
	n.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthOIDC, Provider: "sso", Unauthenticated: v1.Unauthenticated401}}
	_, err = e.st.Update(context.Background(), n)
	require.NoError(t, err)
	resp := e.call(http.MethodPost, "staging.localhost", "/function/app", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Zero(t, e.reached(), "nothing wakes")

	updated, err := e.st.Get(context.Background(), v1.KindNamespace.GVK(), "", "staging")
	require.NoError(t, err)
	n = updated.(*v1.Namespace)
	n.Spec.EdgeDefaults.Auth.Provider = "gone"
	_, err = e.st.Update(context.Background(), n)
	require.NoError(t, err)
	resp = e.call(http.MethodPost, "staging.localhost", "/function/app", nil)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "any other call whose provider is missing gets 503")
}

// ADR-0335 Decision 8: funcd's sign-in cookies reach no backend, whatever the stance of the rule that serves the call:
// an open rule beside an oidc one on the host gets the call's other cookies as sent.
func TestFuncdCookiesReachNoBackend(t *testing.T) {
	t.Parallel()
	e := newSignInEdge(t)
	e.provider("sso", nil, "app.example.com")
	e.program(entry("app", "app.example.com", oidcRule("/", "sso", v1.UnauthenticatedRedirect),
		router.CompiledRule{Path: "/www", Function: "app", Auth: v1.AuthOpen}))
	session := e.signIn("app.example.com", "/")
	resp := e.call(http.MethodGet, "app.example.com", "/www", map[string]string{"Cookie": "theme=dark"}, session,
		&http.Cookie{Name: "__Host-funcd_auth.staging.sso", Value: "x"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "theme=dark", e.last().Get("Cookie"), "only the call's other cookies reach the open rule's Function")
}

// scenario: oidc-by-name-other-host
// ADR-0335 Decision 2 (amended 2026-11-21): a navigation by name whose namespace X-Funcd-Namespace chose gets 401, not
// a redirect whose callback the host it used may not have; on the namespace host it is redirected to sign in.
func TestScenarioOidcByNameOtherHost(t *testing.T) {
	t.Parallel()
	e := newSignInEdge(t)
	e.provider("sso", nil, "staging.localhost")
	seedFnIngress(t, e.st, "staging", "page", &v1.Ingress{Auth: &v1.RouteAuth{Mode: v1.AuthOIDC, Provider: "sso"}})

	resp := e.call(http.MethodGet, "localhost", "/function/page", map[string]string{"Accept": "text/html", "X-Funcd-Namespace": "staging"})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"))

	resp = e.call(http.MethodGet, "staging.localhost", "/function/page", map[string]string{"Accept": "text/html"})
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Zero(t, e.reached(), "nothing wakes")
}
