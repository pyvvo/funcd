package oidc

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/oidcprovider"
)

// The scenarios' namespace, provider and host (ADR-0335 Scenarios).
const (
	staging    v1.NamespaceName = "staging"
	sso        v1.ObjectName    = "sso"
	appHost                     = "app.example.com"
	appClient                   = "app-client"
	appSecret                   = "app-secret"
	appSession                  = "sso-session"
	appCreds                    = "sso-client"
)

// harness is an app behind the gate on *.example.com over https, a provider, a store, and a browser.
type harness struct {
	t       *testing.T
	ctx     context.Context
	st      store.Store
	idp     *oidcprovider.Provider
	gate    *Gate
	rec     *Reconciler
	app     *httptest.Server
	clock   *clock.Manual
	browser *http.Client
	addons  *fakeAddons
	https   bool

	mu      sync.Mutex
	seen    []http.Header
	stances map[string]router.OIDCAuth // by host; a host with none is sso's redirect
}

type harnessOptions struct {
	plainApp bool                 // the app over http, as funcdctl dev serves it
	idp      oidcprovider.Options // the provider's shape
	callback func(h *harness) func(host string) string
}

func newHarness(t *testing.T, o harnessOptions) *harness {
	t.Helper()
	h := &harness{t: t, ctx: context.Background(), st: store.New(memory.New()), idp: oidcprovider.New(t, o.idp),
		clock: clock.NewManual(time.Now()), addons: &fakeAddons{objects: map[v1.ObjectName]AddonObject{}},
		https: !o.plainApp, stances: map[string]router.OIDCAuth{}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Strip(r.Header)
		if v1.UnderAuthPath(r.URL.Path) {
			h.gate.Auth(w, r)
			return
		}
		h.mu.Lock()
		a, ok := h.stances[host(r)]
		h.mu.Unlock()
		if !ok {
			a = router.OIDCAuth{Provider: sso}
		}
		if !h.gate.Serve(w, r, staging, a) {
			return
		}
		h.mu.Lock()
		h.seen = append(h.seen, r.Header.Clone())
		h.mu.Unlock()
		_, _ = io.WriteString(w, "app:"+r.URL.RequestURI())
	})
	if o.plainApp {
		h.app = httptest.NewServer(handler)
	} else {
		h.app = httptest.NewTLSServer(handler)
	}
	t.Cleanup(h.app.Close)
	callback := func(host string) string { return "https://" + host + callbackPath }
	if o.callback != nil {
		callback = o.callback(h)
	}
	quiet := slog.New(slog.DiscardHandler)
	h.gate = New(Deps{Store: h.st, CallbackURL: callback, HTTPS: h.https, BackChannel: h.idp.Transport(), Addons: h.addons, Logger: quiet})
	h.gate.clock = h.clock
	h.rec = NewReconciler(ReconcilerDeps{Store: h.st, CallbackURL: callback, Client: &http.Client{Transport: h.idp.Transport()},
		SupervisionPeriod: 10 * time.Second, BaseDomain: "localhost", Addons: h.addons, Logger: quiet})
	h.rec.clock = h.clock
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	appAddr := h.app.Listener.Addr().String()
	dialer := &net.Dialer{}
	h.browser = &http.Client{Jar: jar, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the test's own servers
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if hostOf(addr) == "localhost" || strings.HasSuffix(hostOf(addr), ".example.com") && !strings.HasPrefix(addr, "login.") {
					addr = appAddr
				}
				return dialer.DialContext(ctx, network, addr)
			},
		}}
	h.seed(namespace(staging, nil))
	return h
}

func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// appURL is the app's URL of path on host, as a browser reaches it.
func (h *harness) appURL(host, path string) string {
	if !h.https {
		_, port, _ := net.SplitHostPort(h.app.Listener.Addr().String())
		return "http://" + net.JoinHostPort(host, port) + path
	}
	return "https://" + host + path
}

func (h *harness) seed(obj v1.Object) v1.Object {
	h.t.Helper()
	out, err := h.st.Create(h.ctx, obj)
	require.NoError(h.t, err)
	return out
}

func (h *harness) update(obj v1.Object) {
	h.t.Helper()
	_, err := h.st.Update(h.ctx, obj)
	require.NoError(h.t, err)
}

func namespace(name v1.NamespaceName, auth *v1.EdgeAuth) *v1.Namespace {
	n := &v1.Namespace{TypeMeta: v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace}}
	n.Name = v1.ObjectName(name)
	if auth != nil {
		n.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: auth}
	}
	return n
}

func secretOf(name v1.ObjectName, data map[string][]byte) *v1.Secret {
	s := &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret}}
	s.Name, s.Namespace, s.ResourceGroup = name, staging, "rg1"
	s.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: data}
	return s
}

func sessionKey() []byte {
	k := make([]byte, sessionKeyBytes)
	_, _ = rand.Read(k)
	return k
}

func providerOf(name v1.ObjectName, issuer string, client v1.OIDCProviderClient, session v1.ObjectName) *v1.OIDCProvider {
	p := &v1.OIDCProvider{TypeMeta: v1.TypeMeta{APIVersion: v1.KindOIDCProvider.GVK().APIVersion(), Kind: v1.KindOIDCProvider}}
	p.Name, p.Namespace, p.ResourceGroup = name, staging, "rg1"
	p.Spec = v1.OIDCProviderSpec{Issuer: issuer, Client: client, SessionSecret: session}
	return p
}

// withClient seeds sso with a client made by hand at the provider, and its Secrets.
func (h *harness) withClient(mutate func(*v1.OIDCProvider)) *v1.OIDCProvider {
	h.t.Helper()
	h.seed(secretOf(appSession, map[string][]byte{keySessionKey: sessionKey()}))
	h.seed(secretOf(appCreds, map[string][]byte{keyClientID: []byte(appClient), keyClientSecret: []byte(appSecret)}))
	h.idp.AddClient(appClient, appSecret, "https://"+appHost+callbackPath, "https://b.example.com"+callbackPath)
	p := providerOf(sso, h.idp.Issuer(), v1.OIDCProviderClient{CredentialsSecret: appCreds}, appSession)
	if mutate != nil {
		mutate(p)
	}
	return h.seed(p).(*v1.OIDCProvider)
}

func (h *harness) provider(name v1.ObjectName) *v1.OIDCProvider {
	h.t.Helper()
	obj, err := h.st.Get(h.ctx, v1.KindOIDCProvider.GVK(), staging, name)
	require.NoError(h.t, err)
	return obj.(*v1.OIDCProvider)
}

func (h *harness) secret(name v1.ObjectName) *v1.Secret {
	h.t.Helper()
	obj, err := h.st.Get(h.ctx, v1.KindSecret.GVK(), staging, name)
	require.NoError(h.t, err)
	return obj.(*v1.Secret)
}

// pass runs one reconciler pass over the provider.
func (h *harness) pass(name v1.ObjectName) *v1.OIDCProvider {
	h.t.Helper()
	_, err := h.rec.Reconcile(h.ctx, controller.Request{GVK: v1.KindOIDCProvider.GVK(), Namespace: staging, Name: name})
	require.NoError(h.t, err)
	return h.provider(name)
}

func condition(p *v1.OIDCProvider, t v1.ConditionType) v1.Condition {
	c, _ := p.Status.Conditions.Get(t)
	return c
}

// get sends a browser request and returns the answer with its body read.
func (h *harness) get(method, target string, header map[string]string) (*http.Response, string) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.ctx, method, target, nil)
	require.NoError(h.t, err)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := h.browser.Do(req)
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	return resp, string(body)
}

func navigate() map[string]string {
	return map[string]string{"Sec-Fetch-Mode": "navigate", "Accept": "text/html"}
}

// provider follows a redirect to the provider's server, which an add-on engine's public URL names elsewhere.
func (h *harness) toProvider(location string) string {
	u, err := url.Parse(location)
	require.NoError(h.t, err)
	own, err := url.Parse(h.idp.URL())
	require.NoError(h.t, err)
	u.Scheme, u.Host = own.Scheme, own.Host
	return u.String()
}

// startSignIn opens path on host and follows the provider's redirect back: it returns the callback URL, not opened.
func (h *harness) startSignIn(host, path string) string {
	h.t.Helper()
	resp, _ := h.get(http.MethodGet, h.appURL(host, path), navigate())
	require.Equal(h.t, http.StatusFound, resp.StatusCode, "a navigation with no session is sent to the provider")
	resp, body := h.get(http.MethodGet, h.toProvider(resp.Header.Get("Location")), nil)
	require.Equal(h.t, http.StatusFound, resp.StatusCode, "the provider signs the user in: %s", body)
	return resp.Header.Get("Location")
}

// signIn signs the browser in at path on host and returns the callback's answer.
func (h *harness) signIn(host, path string) *http.Response {
	h.t.Helper()
	resp, body := h.get(http.MethodGet, h.startSignIn(host, path), nil)
	require.Equal(h.t, http.StatusFound, resp.StatusCode, "the callback signs the browser in: %s", body)
	return resp
}

func (h *harness) reached() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.seen) }

func (h *harness) last() http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	require.NotEmpty(h.t, h.seen, "the app was reached")
	return h.seen[len(h.seen)-1]
}

func (h *harness) setStance(host string, a router.OIDCAuth) {
	h.mu.Lock()
	h.stances[host] = a
	h.mu.Unlock()
}

// principalOf decodes X-Funcd-Principal.
func principalOf(t *testing.T, hdr http.Header) (string, map[string]json.RawMessage) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(hdr.Get(HeaderPrincipal))
	require.NoError(t, err)
	var p struct {
		Provider string                     `json:"provider"`
		Claims   map[string]json.RawMessage `json:"claims"`
	}
	require.NoError(t, json.Unmarshal(raw, &p))
	return p.Provider, p.Claims
}

// fakeAddons is the add-on objects a test declares.
type fakeAddons struct {
	mu      sync.Mutex
	objects map[v1.ObjectName]AddonObject
}

func (f *fakeAddons) set(a AddonObject) { f.mu.Lock(); f.objects[a.Name] = a; f.mu.Unlock() }

func (f *fakeAddons) Get(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (AddonObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.objects[name]; ok {
		return a, nil
	}
	return noAddons{}.Get(context.Background(), ns, name)
}

func (f *fakeAddons) List(context.Context, v1.NamespaceName) ([]AddonObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]AddonObject, 0, len(f.objects))
	for _, a := range f.objects {
		out = append(out, a)
	}
	return out, nil
}

// routeOf is a Route of staging on host, its rules at paths, with stance.
func routeOf(name v1.ObjectName, host string, auth *v1.RouteAuth, paths ...string) *v1.Route {
	r := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	r.Name, r.Namespace, r.ResourceGroup = name, staging, "rg1"
	r.Spec = v1.RouteSpec{Host: host, Auth: auth}
	for _, p := range paths {
		r.Spec.Rules = append(r.Spec.Rules, v1.RouteRule{Path: p, Backend: v1.RouteBackend{Function: "app"}})
	}
	return r
}

func oidcStance(provider v1.ObjectName) *v1.RouteAuth {
	return &v1.RouteAuth{Mode: v1.AuthOIDC, Provider: provider}
}

func newCookieJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return jar
}
