package funcd

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/testkit/oidcprovider"
)

// backendSeen is a worker or an engine that records the headers of each request it gets.
type backendSeen struct {
	srv  *httptest.Server
	mu   sync.Mutex
	last http.Header
}

func newBackend(t *testing.T) *backendSeen {
	t.Helper()
	b := &backendSeen{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.last = r.Header.Clone()
		b.mu.Unlock()
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *backendSeen) seen() http.Header {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

func (b *backendSeen) port(t *testing.T) int {
	t.Helper()
	u, err := url.Parse(b.srv.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return port
}

// proxiedPlatform is bridgePlatform with proxies trusted, a rate limit of one call a minute per client address, and team's
// Function api Ready on a worker that backend serves.
func proxiedPlatform(t *testing.T, proxies []edge.TrustedProxy, objs []v1.Object, opts ...Option) (*Platform, *backendSeen) {
	t.Helper()
	backend := newBackend(t)
	rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
	rt.insts["team/api"] = runtime.Instance{ID: "team/api", Namespace: "team", Name: "api", OwnerKind: v1.KindFunction, Revision: "api-r1",
		State: runtime.StateRunning, IP: "127.0.0.1", Port: backend.port(t), CreatedAt: time.Now()}
	api := lakeFunction("team")
	api.Name, api.Spec.Handler, api.Spec.Image = "api", "handle", "oci://x/api:1"
	api.Status.Phase, api.Status.CurrentRevision = v1.PhaseReady, "api-r1"
	p := bridgePlatform(t, append([]v1.Object{api, storedAddonObject("team", "public", "Tunnel")}, objs...),
		append([]Option{WithRuntime(rt), WithLimits(limit.Config{RatePerMin: 1, Burst: 1, Key: limit.KeyClientIP}),
			WithTrustedProxies(edge.Proxies{Entries: proxies, Workers: netip.MustParsePrefix("10.63.0.0/16")})}, opts...)...)
	return p, backend
}

// serveEngine makes the edge serve team's Tunnel public at tunnel.team.localhost through backend, the entry the add-on
// reconciler programs for an engine served at its host; it is the edge's only entry.
func serveEngine(t *testing.T, p *Platform, backend *backendSeen) {
	t.Helper()
	require.NoError(t, p.edgeRouter.Program(context.Background(), []router.Entry{{Namespace: "team", Host: "tunnel.team.localhost",
		Auth: v1.AuthOpen, Rules: []router.CompiledRule{{Path: "/", Upstream: backend.srv.URL}}}}, nil))
}

func tunnelHeader() edge.TrustedProxy {
	return edge.TrustedProxy{AddonKinds: []string{"Tunnel"}, Header: "X-Real-IP"}
}

func withHeaders(c edgeCall, h map[string]string) edgeCall { c.headers = h; return c }

// scenario: tunnel-client-from-header — behind an entry for the add-on kind Tunnel and its header X-Real-IP, a request
// from the Tunnel's engine on the bridge address is from 198.51.100.7: the rate limit keys on it, and a Function worker,
// like an add-on engine served at its host, gets X-Forwarded-For: 198.51.100.7 alone (ADR-0354 Decisions 2 to 4).
func TestScenarioTunnelClientFromHeader(t *testing.T) {
	t.Parallel()
	p, backend := proxiedPlatform(t, []edge.TrustedProxy{tunnelHeader()}, nil)
	serveEngine(t, p, backend)
	visitor := func(ip string) edgeCall {
		return withHeaders(bridgeCall(teamEngine, "team.localhost", "/function/api", ""), map[string]string{"X-Real-IP": ip})
	}
	res, detail := visitor("198.51.100.7").send(t, p)
	require.Equal(t, http.StatusOK, res.StatusCode, detail)
	require.Equal(t, []string{"198.51.100.7"}, backend.seen().Values("X-Forwarded-For"), "the worker gets the visitor alone")
	require.Empty(t, backend.seen().Get("X-Real-Ip"))

	res, _ = visitor("198.51.100.7").send(t, p)
	require.Equal(t, http.StatusTooManyRequests, res.StatusCode, "the rate limit keys on the visitor")
	res, detail = withHeaders(bridgeCall(teamEngine, "tunnel.team.localhost", "/ping", ""), map[string]string{"X-Real-IP": "198.51.100.8"}).send(t, p)
	require.Equal(t, http.StatusOK, res.StatusCode, "another visitor behind the same engine has a bucket of its own: %s", detail)
	require.Equal(t, []string{"198.51.100.8"}, backend.seen().Values("X-Forwarded-For"), "an engine served at its host too")
}

// scenario: forwarded-for-walk — behind an entry whose cidrs holds 203.0.113.0/24, a request from 203.0.113.9 with
// X-Forwarded-For: 198.51.100.7, 203.0.113.4 is from 198.51.100.7.
func TestScenarioForwardedForWalk(t *testing.T) {
	t.Parallel()
	cdn := edge.TrustedProxy{CIDRs: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}}
	p, backend := proxiedPlatform(t, []edge.TrustedProxy{cdn}, nil)
	call := publicCall("team.localhost", "/function/api", "")
	call.src = "203.0.113.9"
	res, detail := withHeaders(call, map[string]string{"X-Forwarded-For": "198.51.100.7, 203.0.113.4"}).send(t, p)
	require.Equal(t, http.StatusOK, res.StatusCode, detail)
	require.Equal(t, []string{"198.51.100.7"}, backend.seen().Values("X-Forwarded-For"))
}

// scenario: untrusted-peer-headers-dropped — a peer that matches no entry is the client: the worker gets X-Forwarded-For
// holding the peer alone and no X-Real-IP.
func TestScenarioUntrustedPeerHeadersDropped(t *testing.T) {
	t.Parallel()
	cdn := edge.TrustedProxy{CIDRs: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}}
	p, backend := proxiedPlatform(t, []edge.TrustedProxy{cdn}, nil)
	res, detail := withHeaders(publicCall("team.localhost", "/function/api", ""),
		map[string]string{"X-Forwarded-For": "198.51.100.7", "X-Real-IP": "198.51.100.8", "Forwarded": "for=198.51.100.9"}).send(t, p)
	require.Equal(t, http.StatusOK, res.StatusCode, detail)
	require.Equal(t, []string{"192.0.2.7"}, backend.seen().Values("X-Forwarded-For"), "the peer alone")
	require.Empty(t, backend.seen().Get("X-Real-Ip"))
	require.Empty(t, backend.seen().Get("Forwarded"))
}

// scenario: scheme-never-from-header — a trusted request with X-Forwarded-Proto: http, or another X-Forwarded-Host,
// leaves the edge's public scheme server.edgePublicScheme and the host the request's own: the sign-in's callback keeps
// both.
func TestScenarioSchemeNeverFromHeader(t *testing.T) {
	t.Parallel()
	idp := oidcprovider.New(t, oidcprovider.Options{})
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	session := &v1.Secret{TypeMeta: tmeta(v1.KindSecret)}
	session.Name, session.Namespace, session.ResourceGroup = "sso-session", "team", "rg1"
	session.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{"SESSION_KEY": key}}
	creds := &v1.Secret{TypeMeta: tmeta(v1.KindSecret)}
	creds.Name, creds.Namespace, creds.ResourceGroup = "sso-client", "team", "rg1"
	creds.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{"CLIENT_ID": []byte("app"), "CLIENT_SECRET": []byte("s3cret")}}
	sso := &v1.OIDCProvider{TypeMeta: tmeta(v1.KindOIDCProvider)}
	sso.Name, sso.Namespace, sso.ResourceGroup = "sso", "team", "rg1"
	sso.Spec = v1.OIDCProviderSpec{Issuer: idp.Issuer(), SessionSecret: "sso-session", Client: v1.OIDCProviderClient{CredentialsSecret: "sso-client"}}
	app := page("team", "app", "app.example.com", "/")
	app.Spec.Auth = &v1.RouteAuth{Mode: v1.AuthOIDC, Provider: "sso"}
	p, _ := proxiedPlatform(t, []edge.TrustedProxy{tunnelHeader()}, []v1.Object{session, creds, sso, app},
		WithOIDCBackChannel(idp.Transport()), WithEdgePublicScheme("https"))
	res, detail := withHeaders(bridgeCall(teamEngine, "app.example.com", "/", ""), map[string]string{"X-Real-IP": "198.51.100.7",
		"X-Forwarded-Proto": "http", "X-Forwarded-Host": "evil.example", "Sec-Fetch-Mode": "navigate", "Accept": "text/html"}).send(t, p)
	require.Equal(t, http.StatusFound, res.StatusCode, detail)
	to, err := url.Parse(res.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "https://app.example.com/.auth/callback", to.Query().Get("redirect_uri"))
}

// scenario: a-function-cannot-forge — with an entry for the kind Tunnel and one whose cidrs holds 10.0.0.0/8, which holds
// the worker subnet, a Function worker on the bridge address that sends X-Real-IP and X-Forwarded-For matches neither,
// and its own address is the client.
func TestScenarioAFunctionCannotForge(t *testing.T) {
	t.Parallel()
	wide := edge.TrustedProxy{CIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	p, backend := proxiedPlatform(t, []edge.TrustedProxy{tunnelHeader(), wide}, nil)
	res, detail := withHeaders(bridgeCall(teamWorker, "team.localhost", "/function/api", ""),
		map[string]string{"X-Real-IP": "198.51.100.7", "X-Forwarded-For": "198.51.100.8"}).send(t, p)
	require.Equal(t, http.StatusOK, res.StatusCode, detail)
	require.Equal(t, []string{teamWorker}, backend.seen().Values("X-Forwarded-For"), "the worker's own address")
	res, _ = withHeaders(bridgeCall(teamWorker, "team.localhost", "/function/api", ""), map[string]string{"X-Real-IP": "198.51.100.9"}).send(t, p)
	require.Equal(t, http.StatusTooManyRequests, res.StatusCode, "a forged header gives the worker no second bucket")
}

// ADR-0354 Decision 3: an addonKinds entry matches nothing where no egress gateway keeps the engine index, and funcd says
// so at start; with the gateway, or with cidrs entries only, it says nothing.
func TestAnAddonKindsEntryWithNoEngineIndexWarns(t *testing.T) {
	t.Parallel()
	warned := func(opts ...Option) bool {
		var out bytes.Buffer
		p, err := New(append([]Option{InMemory(), WithLogger(slog.New(slog.NewTextHandler(&out, nil))), WithoutLogCompaction()}, opts...)...)
		require.NoError(t, err)
		t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
		p.warnUnmatchedKinds()
		return strings.Contains(out.String(), "an addonKinds entry matches nothing here")
	}
	kinds := WithTrustedProxies(edge.Proxies{Entries: []edge.TrustedProxy{tunnelHeader()}})
	require.True(t, warned(kinds), "no egress gateway")
	require.False(t, warned(kinds, WithEgressGateway(15080, 15053, netip.MustParseAddrPort("192.0.2.53:53")), WithBridgeAddress(testBridge())))
	require.False(t, warned(WithTrustedProxies(edge.Proxies{Entries: []edge.TrustedProxy{{CIDRs: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}}}})))
}
