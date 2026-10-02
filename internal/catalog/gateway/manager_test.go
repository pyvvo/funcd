package gateway

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// dialTimeout bounds the post-Remove reachability probe (a closed listener refuses fast).
const dialTimeout = 500 * time.Millisecond

// TestManager_EnsureProxiesThroughToEngine drives a real HTTP request through a Manager-run
// node-private proxy: Ensure returns a reachable "http://127.0.0.1:<port>" URL; a granted
// per-function token reaches the engine stub with the caller token SWAPPED for the shared engine
// token; Remove stops the proxy (a subsequent dial fails).
func TestManager_EnsureProxiesThroughToEngine(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st) // Function "analytics" bound to "lake" in ns "data"
	pdp := buildPDP(t, st)
	master := []byte("manager-test-node-master")
	keys := NewCatalogKeys(master, st)

	stub := &engineStub{}
	up := httptest.NewServer(stub.handler())
	t.Cleanup(up.Close)

	mgr := NewManager("", "", keys, pdp, nil)
	t.Cleanup(mgr.Shutdown)

	catalog := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}
	url, err := mgr.Ensure(catalog, up.URL, engineToken)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(url, "127.0.0.1:"), "Ensure returns a bare node-private host:port, got %q", url)

	// a granted Function token, driven through the proxy addr, reaches the stub with the token swapped.
	// The injected value is bare host:port (the handler prepends quack://); the proxy speaks HTTP, so we
	// dial http://<addr> here.
	granted, grantedErr := DeriveCatalogToken(master, "data", "analytics")
	require.NoError(t, grantedErr)
	resp, err := http.Post("http://"+url, "application/octet-stream", strings.NewReader(string(makeHandshake(granted))))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "a granted query is forwarded")
	require.True(t, stub.hit, "the engine stub was reached through the managed proxy")
	_, forwarded, ok := swapHandshakeToken(stub.body, "x")
	require.True(t, ok)
	require.Equal(t, engineToken, forwarded, "the engine sees the SHARED engine token, not the caller token")

	// idempotent: Ensure with the same (upstream, engineToken) reuses the same listener (same URL).
	url2, err := mgr.Ensure(catalog, up.URL, engineToken)
	require.NoError(t, err)
	require.Equal(t, url, url2, "an unchanged Ensure reuses the running proxy (same URL)")

	// Remove stops the proxy — a subsequent dial to the addr fails.
	addr := url // already a bare host:port
	mgr.Remove("data", "lake")
	// retry: the server Close + OS port release is not instant, so a fast dial can still connect briefly.
	require.Eventually(t, func() bool {
		c, e := net.DialTimeout("tcp", addr, dialTimeout)
		if e == nil {
			_ = c.Close()
			return false
		}
		return true
	}, 2*time.Second, 20*time.Millisecond, "the listener is closed after Remove")
}

// TestManager_EnsureKeepsURLWhenEngineMoves confirms a changed upstream/engineToken retargets the running
// proxy instead of rebinding it: the URL consumers already hold stays the same, and the next query
// reaches the NEW engine with the NEW shared engine token swapped in.
func TestManager_EnsureKeepsURLWhenEngineMoves(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	pdp := buildPDP(t, st)
	master := []byte("retarget-master")
	keys := NewCatalogKeys(master, st)

	stub1, stub2 := &engineStub{}, &engineStub{}
	up1 := httptest.NewServer(stub1.handler())
	t.Cleanup(up1.Close)
	up2 := httptest.NewServer(stub2.handler())
	t.Cleanup(up2.Close)

	mgr := NewManager("", "", keys, pdp, nil)
	t.Cleanup(mgr.Shutdown)
	catalog := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}

	url1, err := mgr.Ensure(catalog, up1.URL, "token-a")
	require.NoError(t, err)
	url2, err := mgr.Ensure(catalog, up2.URL, "token-b")
	require.NoError(t, err)
	require.Equal(t, url1, url2, "the engine moved, but the proxy URL consumers hold is unchanged")

	granted, err := DeriveCatalogToken(master, "data", "analytics")
	require.NoError(t, err)
	resp, err := http.Post("http://"+url1, "application/octet-stream", strings.NewReader(string(makeHandshake(granted))))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "a granted query is forwarded")
	require.False(t, stub1.hit, "the old engine is no longer reached")
	require.True(t, stub2.hit, "the query reaches the new engine through the same URL")
	_, forwarded, ok := swapHandshakeToken(stub2.body, "x")
	require.True(t, ok)
	require.Equal(t, "token-b", forwarded, "the new shared engine token is swapped in")
}

// TestManager_PublishHostSplit confirms the containerd bind/publish split (ADR-0137): with a
// publishHost set, Ensure binds a netns-reachable interface but PUBLISHES the injected URL on the
// publishHost (the CNI bridge gateway IP), NOT the bind interface — the query-path analog of the
// s3gateway ListenAddr/Endpoint split. The published port still matches the actually-bound port.
func TestManager_PublishHostSplit(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	pdp := buildPDP(t, st)
	keys := NewCatalogKeys([]byte("publish-split-master"), st)

	up := httptest.NewServer((&engineStub{}).handler())
	t.Cleanup(up.Close)

	// bind loopback (reachable from the test), publish the "bridge gateway" host.
	const publishHost = "10.63.0.1"
	mgr := NewManager("127.0.0.1", publishHost, keys, pdp, nil)
	t.Cleanup(mgr.Shutdown)
	catalog := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}

	url, err := mgr.Ensure(catalog, up.URL, engineToken)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(url, publishHost+":"), "Ensure publishes on publishHost, got %q", url)

	// the published port is the port the listener actually bound on loopback (only the host differs).
	_, port, err := net.SplitHostPort(url)
	require.NoError(t, err)
	require.NotEmpty(t, port, "a real ephemeral port is published")
}

// TestIssue312_ProxyBoundsStalledAndIdleConns: a peer that connects to a catalog proxy and stalls before
// the PEP reads its token, or holds a keep-alive connection idle, pins a daemon goroutine without limit
// unless the proxy's http.Server bounds the header read, the body read and the idle wait. The idle bound
// must outlast the edge reverse proxy's client keep-alive (http.DefaultTransport), so that client closes first.
func TestIssue312_ProxyBoundsStalledAndIdleConns(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	mgr := NewManager("", "", NewCatalogKeys([]byte("i312-master"), st), buildPDP(t, st), nil)
	t.Cleanup(mgr.Shutdown)
	_, err := mgr.Ensure(auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}, "http://127.0.0.1:1", engineToken)
	require.NoError(t, err)

	mgr.mu.Lock()
	srv := mgr.servers[managerKey("data", "lake")].server
	mgr.mu.Unlock()
	require.Positive(t, srv.ReadHeaderTimeout, "a peer that never finishes its headers must be cut off")
	require.Positive(t, srv.ReadTimeout, "a peer that stalls its body before the token is read must be cut off")
	clientIdle := http.DefaultTransport.(*http.Transport).IdleConnTimeout
	require.Greater(t, srv.IdleTimeout, clientIdle, "an idle keep-alive connection must be closed, after the client's own idle timeout")
}
