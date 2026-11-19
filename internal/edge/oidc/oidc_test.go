package oidc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/testkit/oidcprovider"
)

const sessionName = "__Host-funcd_session.staging.sso"

// scenario: oidc-redirect
func TestScenarioOidcRedirect(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withClient(nil)

	resp, _ := h.get(http.MethodGet, h.appURL(appHost, "/"), navigate())
	require.Equal(t, http.StatusFound, resp.StatusCode)
	to, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, h.idp.Issuer()+"/authorize", to.Scheme+"://"+to.Host+to.Path)
	q := to.Query()
	require.Equal(t, appClient, q.Get("client_id"))
	require.Equal(t, "https://"+appHost+callbackPath, q.Get("redirect_uri"), "the host's callback")
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.NotEmpty(t, q.Get("code_challenge"))
	require.NotEmpty(t, q.Get("state"))
	require.NotEmpty(t, q.Get("nonce"))
	require.Equal(t, "openid profile email", q.Get("scope"))
	var state *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-funcd_auth.staging.sso" {
			state = c
		}
	}
	require.NotNil(t, state, "the state cookie is set")
	require.True(t, state.HttpOnly && state.Secure && state.Path == "/" && state.SameSite == http.SameSiteLaxMode)
	require.Equal(t, int(stateTTL/time.Second), state.MaxAge)

	resp, _ = h.get(http.MethodGet, h.appURL(appHost, "/"), map[string]string{"Sec-Fetch-Mode": "cors"})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a GET from fetch is no navigation")
	require.Zero(t, h.reached(), "nothing wakes")
}

// scenario: oidc-signin
func TestScenarioOidcSignin(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withClient(nil)

	resp := h.signIn(appHost, "/orders/7?tab=items")
	require.Equal(t, "/orders/7?tab=items", resp.Header.Get("Location"), "back to the first path")
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionName {
			session = c
		}
	}
	require.NotNil(t, session, "the callback sets %s", sessionName)
	require.True(t, session.HttpOnly && session.Secure && session.Path == "/" && session.SameSite == http.SameSiteLaxMode)
	require.Positive(t, session.MaxAge, "until the ID token's exp")

	req, err := http.NewRequestWithContext(h.ctx, http.MethodGet, h.appURL(appHost, "/orders/7?tab=items"), nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
	got, err := h.browser.Do(req)
	require.NoError(t, err)
	_ = got.Body.Close()
	require.Equal(t, http.StatusOK, got.StatusCode)
	hdr := h.last()
	require.Equal(t, "user-1", hdr.Get(HeaderPrincipalID))
	require.Equal(t, "Ada Lovelace", hdr.Get(HeaderPrincipalName))
	require.NotEmpty(t, hdr.Get(HeaderIDToken))
	provider, claims := principalOf(t, hdr)
	require.Equal(t, "sso", provider)
	require.JSONEq(t, `"ada@example.com"`, string(claims["email"]))
	require.Contains(t, hdr.Get("Cookie"), "theme=dark", "the app keeps its own cookies")
	require.NotContains(t, hdr.Get("Cookie"), "funcd_", "and gets none of funcd's")
}

// scenario: oidc-callback-refused
func TestScenarioOidcCallbackRefused(t *testing.T) {
	t.Parallel()
	refused := func(t *testing.T, h *harness, callback string, why string) {
		t.Helper()
		resp, body := h.get(http.MethodGet, callback, nil)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, why)
		require.Contains(t, body, `"detail"`, "%s: the answer names the cause", why)
		for _, c := range resp.Cookies() {
			require.NotEqual(t, sessionName, c.Name, "%s: no session", why)
		}
	}
	t.Run("no state cookie", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{})
		h.withClient(nil)
		callback := h.startSignIn(appHost, "/")
		h.browser.Jar, _ = cookiejar.New(nil)
		refused(t, h, callback, "no state cookie")
	})
	t.Run("an expired state", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{})
		h.withClient(nil)
		callback := h.startSignIn(appHost, "/")
		h.clock.Advance(stateTTL + time.Second)
		refused(t, h, callback, "an expired state")
	})
	t.Run("another state", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{})
		h.withClient(nil)
		first := h.startSignIn(appHost, "/")
		h.startSignIn(appHost, "/other")
		refused(t, h, first, "a second sign-in replaced the first's state")
	})
	t.Run("the provider's error", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{})
		h.withClient(nil)
		h.idp.DenyNext("access_denied")
		resp, body := h.get(http.MethodGet, h.startSignIn(appHost, "/"), nil)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		require.Contains(t, body, "access_denied")
	})
	t.Run("another nonce", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{})
		h.withClient(nil)
		h.idp.EditIDTokens(func(c *oidcprovider.Claims) { c.Nonce = "not-this-sign-in" })
		refused(t, h, h.startSignIn(appHost, "/"), "an ID token with another nonce")
	})
	t.Run("another azp", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{})
		h.withClient(nil)
		h.idp.EditIDTokens(func(c *oidcprovider.Claims) { c.AuthorizedParty = "someone-else" })
		refused(t, h, h.startSignIn(appHost, "/"), "an ID token for another party")
	})
	t.Run("a session cookie over 4096 bytes", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{})
		h.withClient(nil)
		h.idp.EditIDTokens(func(c *oidcprovider.Claims) { c.Padding = strings.Repeat("x", maxCookieBytes) })
		resp, body := h.get(http.MethodGet, h.startSignIn(appHost, "/"), nil)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		require.Contains(t, body, "4096")
	})
}

// scenario: oidc-bearer
func TestScenarioOidcBearer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withClient(func(p *v1.OIDCProvider) { p.Spec.Audiences = []string{"api"} })
	claims := h.idp.Claims("api", "")
	call := func(token string) (*http.Response, string) {
		return h.get(http.MethodPost, h.appURL(appHost, "/orders"), map[string]string{"Authorization": "Bearer " + token})
	}

	for _, alg := range []oidcprovider.Alg{oidcprovider.RS256, oidcprovider.EdDSA} {
		token := h.idp.Token(alg, claims)
		resp, _ := call(token)
		require.Equal(t, http.StatusOK, resp.StatusCode, "a token %s signed for api passes", alg)
		hdr := h.last()
		require.Equal(t, "user-1", hdr.Get(HeaderPrincipalID))
		require.Equal(t, "Bearer "+token, hdr.Get("Authorization"), "the provider's token is forwarded")
		require.Empty(t, hdr.Get(HeaderIDToken), "a Bearer call carries no ID token header")
	}

	expired := claims
	expired.Expiry = time.Now().Add(-time.Minute).Unix()
	other := claims
	other.Audience = []string{"billing"}
	noExp := claims
	noExp.Expiry = 0
	for why, token := range map[string]string{
		"forged":           h.idp.TokenWithForeignKey(claims),
		"expired":          h.idp.Token(oidcprovider.RS256, expired),
		"another audience": h.idp.Token(oidcprovider.RS256, other),
		"no exp":           h.idp.Token(oidcprovider.RS256, noExp),
		"not a JWT at all": "abc.def",
		"another issuer":   h.idp.Token(oidcprovider.RS256, withIssuer(claims, "https://elsewhere.example.com")),
	} {
		resp, _ := call(token)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, why)
		require.Equal(t, `Bearer error="invalid_token"`, resp.Header.Get("WWW-Authenticate"), why)
	}

	p := h.provider(sso)
	p.Spec.Audiences = nil
	h.update(p)
	resp, _ := call(h.idp.Token(oidcprovider.RS256, claims))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "with no audiences a Bearer token signs nobody in")
	require.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"))
}

func withIssuer(c oidcprovider.Claims, iss string) oidcprovider.Claims { c.Issuer = iss; return c }

// scenario: oidc-unauthenticated
func TestScenarioOidcUnauthenticated(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withClient(nil)

	h.setStance(appHost, router.OIDCAuth{Provider: sso, Unauthenticated: v1.Unauthenticated401})
	resp, _ := h.get(http.MethodGet, h.appURL(appHost, "/"), navigate())
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "401 even for a navigation")
	require.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"), "no error code for a call that sent no token")
	require.Zero(t, h.reached(), "nothing wakes")

	h.setStance(appHost, router.OIDCAuth{Provider: sso, Unauthenticated: v1.UnauthenticatedAllow})
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		resp, _ = h.get(method, h.appURL(appHost, "/"), map[string]string{HeaderPrincipalID: "forged"})
		require.Equal(t, http.StatusOK, resp.StatusCode, "allow passes a %s", method)
		require.Empty(t, h.last().Get(HeaderPrincipal), "with no principal")
		require.Empty(t, h.last().Get(HeaderPrincipalID))
	}

	h.setStance(appHost, router.OIDCAuth{Provider: sso})
	resp, _ = h.get(http.MethodPost, h.appURL(appHost, "/"), map[string]string{"Accept": "text/html"})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "redirect answers a POST with 401")
}

// scenario: oidc-cross-origin
func TestScenarioOidcCrossOrigin(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withClient(nil)
	h.signIn(appHost, "/")
	post := func(header map[string]string) int {
		resp, _ := h.get(http.MethodPost, h.appURL(appHost, "/orders"), header)
		return resp.StatusCode
	}
	require.Equal(t, http.StatusForbidden, post(map[string]string{"Sec-Fetch-Site": "same-site"}))
	require.Equal(t, http.StatusForbidden, post(map[string]string{"Origin": "https://b.example.com"}))
	require.Equal(t, http.StatusForbidden, post(map[string]string{"Origin": "http://" + appHost}), "another scheme")
	require.Equal(t, http.StatusOK, post(map[string]string{"Sec-Fetch-Site": "same-origin"}))
	require.Equal(t, http.StatusOK, post(map[string]string{"Origin": "https://" + appHost}))
	require.Equal(t, http.StatusOK, post(nil), "no Origin and no Sec-Fetch-Site")
	resp, _ := h.get(http.MethodGet, h.appURL(appHost, "/"), map[string]string{"Sec-Fetch-Site": "cross-site"})
	require.Equal(t, http.StatusOK, resp.StatusCode, "a GET from another origin passes")
}

// scenario: oidc-logout
func TestScenarioOidcLogout(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withClient(nil)
	h.signIn(appHost, "/")
	req, err := http.NewRequestWithContext(h.ctx, http.MethodGet, h.appURL(appHost, logoutPath), nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "__Host-funcd_session.staging.public", Value: "x"})
	req.AddCookie(&http.Cookie{Name: "funcd_session.other.sso", Value: "y"})
	req.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
	resp, err := h.browser.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/", resp.Header.Get("Location"))
	cleared := map[string]bool{}
	for _, c := range resp.Cookies() {
		require.Equal(t, -1, c.MaxAge, "%s is cleared", c.Name)
		cleared[c.Name] = true
	}
	require.Equal(t, map[string]bool{sessionName: true, "__Host-funcd_session.staging.public": true, "funcd_session.other.sso": true}, cleared,
		"every funcd session cookie of the host, and nothing else")
	resp, _ = h.get(http.MethodGet, h.appURL(appHost, "/"), navigate())
	require.Equal(t, http.StatusFound, resp.StatusCode, "signed out")
	resp, _ = h.get(http.MethodGet, h.appURL(appHost, v1.AuthPath+"/elsewhere"), nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "any other path under /.auth/ is 404")
}

// scenario: oidc-dev
func TestScenarioOidcDev(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{plainApp: true, idp: oidcprovider.Options{Plain: true, Path: "/auth/v1"},
		callback: func(h *harness) func(string) string {
			return Callbacks{Bound: func() int { return h.app.Listener.Addr().(*net.TCPAddr).Port }}.URL
		}})
	h.addons.set(AddonObject{Name: "login", Address: h.idp.Addr()})
	h.seed(secretOf(appSession, map[string][]byte{keySessionKey: sessionKey()}))
	h.seed(secretOf(appCreds, map[string][]byte{keyClientID: []byte(appClient), keyClientSecret: []byte(appSecret)}))
	port := strconv.Itoa(h.app.Listener.Addr().(*net.TCPAddr).Port)
	h.idp.AddClient(appClient, appSecret, "http://"+net.JoinHostPort("localhost", port)+callbackPath,
		"http://"+net.JoinHostPort("127.0.0.1", port)+callbackPath)
	p := providerOf(sso, h.idp.Issuer(), v1.OIDCProviderClient{CredentialsSecret: appCreds}, appSession)
	p.Namespace, p.Spec.Addon = staging, "login"
	h.seed(p)

	for _, host := range []string{"localhost", "127.0.0.1"} {
		resp, _ := h.get(http.MethodGet, h.appURL(host, "/"), navigate())
		require.Equal(t, http.StatusFound, resp.StatusCode, host)
		to, err := url.Parse(resp.Header.Get("Location"))
		require.NoError(t, err)
		require.Equal(t, "http://"+net.JoinHostPort(host, port)+callbackPath, to.Query().Get("redirect_uri"), host)
		for _, c := range resp.Cookies() {
			require.False(t, c.Secure, "%s: no Secure cookie over http", host)
			require.False(t, strings.HasPrefix(c.Name, hostPrefix), "%s: no __Host- name over http", host)
		}
		resp, body := h.get(http.MethodGet, h.toProvider(resp.Header.Get("Location")), nil)
		require.Equal(t, http.StatusFound, resp.StatusCode, body)
		resp, body = h.get(http.MethodGet, resp.Header.Get("Location"), nil)
		require.Equal(t, http.StatusFound, resp.StatusCode, "%s signs in: %s", host, body)
		names := []string{}
		for _, c := range resp.Cookies() {
			names = append(names, c.Name)
			require.False(t, c.Secure, host)
		}
		require.Contains(t, names, "funcd_session.staging.sso", host)
		resp, _ = h.get(http.MethodGet, h.appURL(host, "/"), navigate())
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s is signed in", host)
	}
}

// scenario: oidc-provider-not-ready
func TestScenarioOidcProviderNotReady(t *testing.T) {
	t.Parallel()
	notReady := func(t *testing.T, h *harness, reason string) {
		t.Helper()
		p := h.pass(sso)
		c := condition(p, condReady)
		require.Equal(t, v1.ConditionFalse, c.Status)
		require.Equal(t, reason, c.Reason, c.Message)
		resp, _ := h.get(http.MethodGet, h.appURL(appHost, "/"), navigate())
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "calls on app get 503")
		require.Zero(t, h.reached())
	}
	t.Run("a 16-byte SESSION_KEY", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{})
		h.withClient(nil)
		s := h.secret(appSession)
		s.Spec.Data[keySessionKey] = sessionKey()[:16]
		h.update(s)
		notReady(t, h, ReasonSessionKeyInvalid)
	})
	t.Run("a discovery naming another issuer", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{idp: oidcprovider.Options{Public: "https://elsewhere.example.com"}})
		h.withClient(func(p *v1.OIDCProvider) { p.Spec.Issuer = h.idp.URL() })
		notReady(t, h, ReasonIssuerMismatch)
	})
	t.Run("an http issuer without addon", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{idp: oidcprovider.Options{Plain: true}})
		h.withClient(nil)
		h.gate.back = newBackChannel(nil)
		h.rec.back = newBackChannel(nil)
		notReady(t, h, ReasonDiscoveryFailed)
	})
	t.Run("an issuer at 10.0.0.5", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{})
		h.withClient(func(p *v1.OIDCProvider) { p.Spec.Issuer = "https://10.0.0.5/auth/v1" })
		h.gate.back = newBackChannel(nil)
		h.rec.back = newBackChannel(nil)
		notReady(t, h, ReasonDiscoveryFailed)
	})
	t.Run("ready", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, harnessOptions{})
		h.withClient(nil)
		p := h.pass(sso)
		require.Equal(t, ReasonClientReady, condition(p, condReady).Reason)
		require.Equal(t, v1.PhaseReady, p.Status.Phase)
		require.Equal(t, appClient, p.Status.ClientID)
	})
}

// ADR-0335 Consequences: a session ends with the provider's ID token, with no refresh: a navigation signs in again and
// a POST gets 401.
func TestASessionPastItsExpirySignsInAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withClient(nil)
	h.signIn(appHost, "/")
	resp, _ := h.get(http.MethodPost, h.appURL(appHost, "/orders"), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	h.clock.Advance(31 * time.Minute)
	resp, _ = h.get(http.MethodGet, h.appURL(appHost, "/"), navigate())
	require.Equal(t, http.StatusFound, resp.StatusCode, "the session is past the ID token's exp")
	resp, _ = h.get(http.MethodPost, h.appURL(appHost, "/orders"), nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// ADR-0335 Decision 4: a state cookie opens only under the session key and the client of a provider the store holds,
// so a made-up one gets the 401 of a cookie that does not open, whether its provider exists or not, and the gate keeps
// nothing of it.
func TestCallbackMadeUpStateCookies(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withClient(nil)
	h.browser.Jar, _ = cookiejar.New(nil)
	for _, name := range []string{"__Host-funcd_auth.staging.sso", "__Host-funcd_auth.staging.nope",
		"__Host-funcd_auth.nope.sso", "__Host-funcd_auth." + strings.Repeat("x", 4096) + ".y"} {
		resp, _ := h.get(http.MethodGet, h.appURL(appHost, callbackPath+"?state=s&code=c"), map[string]string{"Cookie": name + "=AAAA"})
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, name[:min(len(name), 40)])
	}
	h.gate.mu.Lock()
	defer h.gate.mu.Unlock()
	require.Empty(t, h.gate.logged, "no made-up cookie leaves a record")
}

// readCounter is a store that counts its reads of OIDCProviders.
type readCounter struct {
	store.Store
	reads atomic.Int64
}

func (s *readCounter) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if gvk == v1.KindOIDCProvider.GVK() {
		s.reads.Add(1)
	}
	return s.Store.Get(ctx, gvk, ns, name)
}

// A callback reads its first maxStateCookies state cookies, each provider once: many copies of one provider's name
// cost one read, and a state cookie past the first few is not read (ADR-0335 Decision 4).
func TestCallbackReadsAFewStateCookies(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withClient(nil)
	counter := &readCounter{Store: h.st}
	g := New(Deps{Store: counter, CallbackURL: func(host string) string { return "https://" + host + callbackPath },
		HTTPS: true, BackChannel: h.idp.Transport(), Addons: h.addons, Logger: slog.New(slog.DiscardHandler)})
	callback := func(cookies string) int {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+appHost+callbackPath+"?state=s&code=c", nil)
		r.Header.Set("Cookie", cookies)
		w := httptest.NewRecorder()
		g.Auth(w, r)
		return w.Code
	}
	many := strings.TrimSuffix(strings.Repeat("__Host-funcd_auth.staging.sso=AAAA; ", 100), "; ")
	require.Equal(t, http.StatusUnauthorized, callback(many))
	require.Equal(t, int64(1), counter.reads.Load(), "one read for one provider")

	counter.reads.Store(0)
	var names []string
	for i := range maxStateCookies + 3 {
		names = append(names, fmt.Sprintf("__Host-funcd_auth.ns%d.p=AAAA", i))
	}
	require.Equal(t, http.StatusUnauthorized, callback(strings.Join(names, "; ")))
	require.Equal(t, int64(maxStateCookies), counter.reads.Load(), "only the first few state cookies are read")
}
