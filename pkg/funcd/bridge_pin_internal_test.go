package funcd

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/testkit/oidcprovider"
)

// testBridge is funcd's address on the container bridge in these tests.
func testBridge() netip.Addr { return netip.MustParseAddr("10.63.0.1") }

// The callers of the ADR-0353 scenarios, on the worker subnet: the engine of team's Tunnel public, team's worker api,
// default's worker web, an address no index holds, and one the two indexes hold with different principals.
const (
	teamEngine   = "10.63.0.3"
	teamWorker   = "10.63.0.4"
	defaultWork  = "10.63.0.5"
	nobody       = "10.63.0.9"
	twoPrincipal = "10.63.0.6"
)

// bridgePlatform is funcd with the egress gateway on and a bridge address, not running: its Route table is loaded as
// Run loads it, and its egress indexes hold the scenarios' callers, as the platform writes them.
// Each Route serves a static page that reads "<namespace>/<name>".
func bridgePlatform(t *testing.T, objs []v1.Object, opts ...Option) *Platform {
	t.Helper()
	p, err := New(append([]Option{InMemory(), WithLogger(slog.New(slog.DiscardHandler)), WithoutLogCompaction(),
		WithEgressGateway(15080, 15053, netip.MustParseAddrPort("192.0.2.53:53")), WithBridgeAddress(testBridge())}, opts...)...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	ctx := context.Background()
	buckets := map[v1.NamespaceName]bool{}
	for _, o := range objs {
		seedObjects(t, p.cfg.store, o)
		r, ok := o.(*v1.Route)
		if !ok {
			continue
		}
		require.NoError(t, p.cfg.blob.Put(ctx, "s3/"+string(r.Namespace)+"/web/"+string(r.Name)+"/index.html",
			[]byte(string(r.Namespace)+"/"+string(r.Name)), blob.PutOptions{}))
		if !buckets[r.Namespace] {
			buckets[r.Namespace] = true
			b := &v1.Bucket{TypeMeta: tmeta(v1.KindBucket)}
			b.Name, b.Namespace, b.ResourceGroup = "web", r.Namespace, "rg1"
			seedObjects(t, p.cfg.store, b)
		}
	}
	_, err = p.routeReconciler.Reconcile(ctx, controller.Request{GVK: v1.KindRoute.GVK()})
	require.NoError(t, err)
	p.exec.egressEngines.Add(netip.MustParseAddr(teamEngine), auth.EntityRef{Type: v1.KindAddonObject, Namespace: "team", Name: "public"})
	p.exec.egressWorkers.Add(netip.MustParseAddr(teamWorker), auth.EntityRef{Type: v1.KindFunction, Namespace: "team", Name: "api"})
	p.exec.egressWorkers.Add(netip.MustParseAddr(defaultWork), auth.EntityRef{Type: v1.KindFunction, Namespace: "default", Name: "web"})
	p.exec.egressWorkers.Add(netip.MustParseAddr(twoPrincipal), auth.EntityRef{Type: v1.KindFunction, Namespace: "team", Name: "api"})
	p.exec.egressEngines.Add(netip.MustParseAddr(twoPrincipal), auth.EntityRef{Type: v1.KindAddonObject, Namespace: "other", Name: "public"})
	return p
}

// page is the open static Route name of ns on host, at path.
func page(ns v1.NamespaceName, name, host, path string) *v1.Route {
	r := &v1.Route{TypeMeta: tmeta(v1.KindRoute)}
	r.Name, r.Namespace, r.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	r.Spec = v1.RouteSpec{Host: host, Auth: &v1.RouteAuth{Mode: v1.AuthOpen}, Rules: []v1.RouteRule{{Path: path,
		Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: "web", Prefix: name + "/", Index: "index.html", SPA: true}}}}}
	return r
}

// sites are team's page on its namespace host and its Route host api.example.com, which has no / rule, other's page on
// its namespace host, and default's host-less page at /home.
func sites() []v1.Object {
	return []v1.Object{page("team", "site", "", "/"), page("team", "api", "api.example.com", "/app"), page("other", "site", "", "/"),
		page("default", "home", "", "/home")}
}

// edgeCall is one request to p's data plane from src, as the listener on local delivers it: the bridge address, or
// loopback, which stands for any address outside the container network. It returns the status and the body, or the
// problem's detail.
type edgeCall struct {
	local          netip.Addr
	src, host, ns  string
	method, target string
	cookies        []*http.Cookie
	headers        map[string]string
}

func (c edgeCall) send(t *testing.T, p *Platform) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(cmp.Or(c.method, http.MethodGet), "http://"+c.host+c.target, nil)
	req.Host, req.RemoteAddr = c.host, c.src+":40000"
	if c.ns != "" {
		req.Header.Set("X-Funcd-Namespace", c.ns)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey,
		net.TCPAddrFromAddrPort(netip.AddrPortFrom(c.local, 8081))))
	rec := httptest.NewRecorder()
	p.dataPlaneServer.Handler.ServeHTTP(rec, req)
	res := rec.Result()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	var problem fault.Problem
	if json.Unmarshal(body, &problem) == nil && problem.Detail != "" {
		return res, problem.Detail
	}
	return res, string(body)
}

func bridgeCall(src, host, target, ns string) edgeCall {
	return edgeCall{local: testBridge(), src: src, host: host, target: target, ns: ns}
}

func publicCall(host, target, ns string) edgeCall {
	return edgeCall{local: netip.MustParseAddr("127.0.0.1"), src: "192.0.2.7", host: host, target: target, ns: ns}
}

// refused asserts a 403 that names neither namespace and serves nothing of either.
func refused(t *testing.T, res *http.Response, detail, what string) {
	t.Helper()
	require.Equal(t, http.StatusForbidden, res.StatusCode, "%s: %s", what, detail)
	require.NotContains(t, detail, "/site", what)
	require.NotContains(t, detail, "other", what)
	require.NotContains(t, detail, "team", what)
}

// scenario: tunnel-reaches-its-hosts — the engine of team's Tunnel public, sending a request for team's host to the
// edge on the bridge address, is served as any visitor is (ADR-0353 Decisions 3 and 4).
func TestScenarioTunnelReachesItsHosts(t *testing.T) {
	t.Parallel()
	p := bridgePlatform(t, sites())
	res, body := bridgeCall(teamEngine, "team.localhost", "/", "").send(t, p)
	require.Equal(t, http.StatusOK, res.StatusCode, body)
	require.Equal(t, "team/site", body)
	_, visitor := publicCall("team.localhost", "/", "").send(t, p)
	require.Equal(t, visitor, body, "as a visitor gets it")
	res, body = bridgeCall(teamEngine, "api.example.com", "/app", "").send(t, p)
	require.Equal(t, http.StatusOK, res.StatusCode, body)
	require.Equal(t, "team/api", body, "a Route host of its namespace too")
}

// scenario: other-namespace-host-refused — the same engine, naming a host of namespace other, gets 403 and nothing of
// other.
func TestScenarioOtherNamespaceHostRefused(t *testing.T) {
	t.Parallel()
	p := bridgePlatform(t, sites())
	res, detail := bridgeCall(teamEngine, "other.localhost", "/", "").send(t, p)
	refused(t, res, detail, "other's host")
	require.Contains(t, detail, "outside the caller's namespace")
}

// scenario: by-name-pinned — /function/payroll on team's Route host api.example.com, which has no / rule, is 403 with
// X-Funcd-Namespace: other, and without it, where the call resolves in default.
func TestScenarioByNamePinned(t *testing.T) {
	t.Parallel()
	p := bridgePlatform(t, sites())
	res, detail := bridgeCall(teamEngine, "api.example.com", "/function/payroll", "other").send(t, p)
	refused(t, res, detail, "the header naming other")
	res, detail = bridgeCall(teamEngine, "api.example.com", "/function/payroll", "").send(t, p)
	refused(t, res, detail, "no header, so default")
	res, detail = bridgeCall(teamEngine, "api.example.com", "/function/payroll", "team").send(t, p)
	require.Equal(t, http.StatusNotFound, res.StatusCode, detail)
	require.Contains(t, detail, "team/payroll", "the header naming its own namespace is served by name there")
}

// scenario: default-caller — a worker of default reaches default's host-less entries and its Functions by name, and gets
// 403 on a host that team reserves.
func TestScenarioDefaultCaller(t *testing.T) {
	t.Parallel()
	p := bridgePlatform(t, sites())
	res, body := bridgeCall(defaultWork, "example.com", "/home", "").send(t, p)
	require.Equal(t, http.StatusOK, res.StatusCode, body)
	require.Equal(t, "default/home", body)
	res, detail := bridgeCall(defaultWork, "example.com", "/function/x", "").send(t, p)
	require.Equal(t, http.StatusNotFound, res.StatusCode, detail)
	require.Contains(t, detail, "default/x", "served by name in default")
	res, detail = bridgeCall(defaultWork, "team.localhost", "/", "").send(t, p)
	refused(t, res, detail, "team's reserved host")
	res, detail = bridgeCall(defaultWork, "team.localhost", "/function/x", "").send(t, p)
	refused(t, res, detail, "by name on team's host")
}

// scenario: sign-in-pinned — the engine of team forwarding /.auth/callback whose state names an OIDCProvider of other
// gets 403, and no code exchange runs; the same callback from a visitor is exchanged.
func TestScenarioSignInPinned(t *testing.T) {
	t.Parallel()
	idp := oidcprovider.New(t, oidcprovider.Options{})
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	session := &v1.Secret{TypeMeta: tmeta(v1.KindSecret)}
	session.Name, session.Namespace, session.ResourceGroup = "sso-session", "other", "rg1"
	session.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{"SESSION_KEY": key}}
	creds := &v1.Secret{TypeMeta: tmeta(v1.KindSecret)}
	creds.Name, creds.Namespace, creds.ResourceGroup = "sso-client", "other", "rg1"
	creds.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{"CLIENT_ID": []byte("app"), "CLIENT_SECRET": []byte("s3cret")}}
	sso := &v1.OIDCProvider{TypeMeta: tmeta(v1.KindOIDCProvider)}
	sso.Name, sso.Namespace, sso.ResourceGroup = "sso", "other", "rg1"
	sso.Spec = v1.OIDCProviderSpec{Issuer: idp.Issuer(), SessionSecret: "sso-session", Client: v1.OIDCProviderClient{CredentialsSecret: "sso-client"}}
	app := page("other", "app", "app.example.com", "/")
	app.Spec.Auth = &v1.RouteAuth{Mode: v1.AuthOIDC, Provider: "sso"}
	p := bridgePlatform(t, append(sites(), session, creds, sso, app), WithOIDCBackChannel(idp.Transport()))

	start := publicCall("app.example.com", "/", "")
	start.headers = map[string]string{"Sec-Fetch-Mode": "navigate", "Accept": "text/html"}
	res, detail := start.send(t, p)
	require.Equal(t, http.StatusFound, res.StatusCode, detail)
	to, err := url.Parse(res.Header.Get("Location"))
	require.NoError(t, err)
	idp.AddClient("app", "s3cret", to.Query().Get("redirect_uri"))
	authorize, err := (&http.Client{Transport: idp.Transport(), CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Get(to.String()) //nolint:noctx // the test provider
	require.NoError(t, err)
	_ = authorize.Body.Close()
	require.Equal(t, http.StatusFound, authorize.StatusCode)
	callback, err := url.Parse(authorize.Header.Get("Location"))
	require.NoError(t, err)

	pinned := bridgeCall(teamEngine, "app.example.com", callback.RequestURI(), "")
	pinned.cookies = res.Cookies()
	res2, detail := pinned.send(t, p)
	refused(t, res2, detail, "a callback whose state names other's provider")
	require.Zero(t, idp.Calls("token"), "no code exchange runs")

	visitor := publicCall("app.example.com", callback.RequestURI(), "")
	visitor.cookies = res.Cookies()
	res2, detail = visitor.send(t, p)
	require.Equal(t, http.StatusFound, res2.StatusCode, detail)
	require.Equal(t, 1, idp.Calls("token"), "a visitor's callback is exchanged")
}

// scenario: unknown-caller-refused — a caller on the bridge address that neither egress index holds, or that the two
// hold with different principals, gets 403 on any path.
func TestScenarioUnknownCallerRefused(t *testing.T) {
	t.Parallel()
	p := bridgePlatform(t, sites())
	for _, src := range []string{nobody, twoPrincipal} {
		for _, target := range []string{"/", "/function/x", "/.auth/logout"} {
			res, detail := bridgeCall(src, "team.localhost", target, "").send(t, p)
			require.Equal(t, http.StatusForbidden, res.StatusCode, "%s %s: %s", src, target, detail)
			require.NotContains(t, detail, src, "the refusal names no address")
		}
	}
}

// scenario: a-worker-is-pinned-too — a Function worker of team that calls the edge on the bridge address reaches team's
// hosts and no other namespace's.
func TestScenarioAWorkerIsPinnedToo(t *testing.T) {
	t.Parallel()
	p := bridgePlatform(t, sites())
	res, body := bridgeCall(teamWorker, "team.localhost", "/", "").send(t, p)
	require.Equal(t, http.StatusOK, res.StatusCode, body)
	require.Equal(t, "team/site", body)
	res, detail := bridgeCall(teamWorker, "other.localhost", "/", "").send(t, p)
	refused(t, res, detail, "other's host")
	res, detail = bridgeCall(teamWorker, "team.localhost", "/", "other").send(t, p)
	refused(t, res, detail, "the header naming other, on team's own host")
}

// scenario: the-public-listener-is-unchanged — a request from outside the container network gets every namespace's
// hosts, and its X-Funcd-Namespace picks the namespace off a namespace host, as before.
func TestScenarioThePublicListenerIsUnchanged(t *testing.T) {
	t.Parallel()
	p := bridgePlatform(t, sites())
	for host, want := range map[string]string{"team.localhost": "team/site", "other.localhost": "other/site"} {
		res, body := publicCall(host, "/", "").send(t, p)
		require.Equal(t, http.StatusOK, res.StatusCode, body)
		require.Equal(t, want, body)
	}
	res, detail := publicCall("api.example.com", "/function/payroll", "other").send(t, p)
	require.Equal(t, http.StatusNotFound, res.StatusCode, detail)
	require.Contains(t, detail, "other/payroll", "served by name in the namespace the header names")
	res, detail = edgeCall{local: netip.MustParseAddr("127.0.0.1"), src: teamEngine, host: "other.localhost", target: "/"}.send(t, p)
	require.Equal(t, http.StatusOK, res.StatusCode, "a known source on another address is no bridge caller: %s", detail)
}

// The bridge listener exists only where the egress gateway runs, at the data plane's bound port: a data plane on every
// address accepts there itself; the workers' allow list gains the bridge address at that port, and the gateway holds every
// address and port the data plane listens on (ADR-0353 Decisions 1 and 2).
func TestTheEdgeListensOnTheBridgeWhereTheGatewayRuns(t *testing.T) {
	t.Parallel()
	loop := bridgePlatform(t, nil)
	bound := netip.MustParseAddrPort(loop.DataPlaneAddr())
	edge := netip.AddrPortFrom(testBridge(), bound.Port())
	require.Equal(t, edge, loop.bridgeListen, "a listener of its own on the bridge address, at the bound port")
	require.Contains(t, loop.cfg.netPolicy.InternalAllow, edge)
	require.Equal(t, []netip.AddrPort{edge, bound}, loop.exec.edge, "the gateway's Edge")

	wild := bridgePlatform(t, nil, WithDataPlaneAddr("0.0.0.0:0"))
	bound = netip.MustParseAddrPort(wild.DataPlaneAddr())
	require.True(t, bound.Addr().IsUnspecified())
	require.False(t, wild.bridgeListen.IsValid(), "the wildcard listener accepts on the bridge address")
	require.Equal(t, []netip.AddrPort{netip.AddrPortFrom(testBridge(), bound.Port()), bound}, wild.exec.edge)

	plain, err := New(InMemory(), WithLogger(slog.New(slog.DiscardHandler)), WithoutLogCompaction(), WithBridgeAddress(testBridge()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = plain.Shutdown(context.Background()) })
	require.False(t, plain.bridgeListen.IsValid(), "no egress gateway, no listener on the bridge")
	res, body := bridgeCall(nobody, "localhost", "/function/x", "").send(t, plain)
	require.Equal(t, http.StatusNotFound, res.StatusCode, "and no pin: %s", body)
}
