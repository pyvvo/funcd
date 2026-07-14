package gateway

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
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

// TestManager_EnsureRebindsOnEngineChange confirms a changed upstream/engineToken rebinds the proxy
// (a fresh listener, so the old addr stops answering).
func TestManager_EnsureRebindsOnEngineChange(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	pdp := buildPDP(t, st)
	keys := NewCatalogKeys([]byte("rebind-master"), st)

	up1 := httptest.NewServer((&engineStub{}).handler())
	t.Cleanup(up1.Close)
	up2 := httptest.NewServer((&engineStub{}).handler())
	t.Cleanup(up2.Close)

	mgr := NewManager("", "", keys, pdp, nil)
	t.Cleanup(mgr.Shutdown)
	catalog := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}

	url1, err := mgr.Ensure(catalog, up1.URL, "token-a")
	require.NoError(t, err)
	url2, err := mgr.Ensure(catalog, up2.URL, "token-b")
	require.NoError(t, err)
	require.NotEqual(t, url1, url2, "a changed engine rebinds a fresh listener")

	// the old addr no longer answers (its server was closed) — retry, the close/port-release is not instant.
	require.Eventually(t, func() bool {
		c, e := net.DialTimeout("tcp", url1, dialTimeout)
		if e == nil {
			_ = c.Close()
			return false
		}
		return true
	}, 2*time.Second, 20*time.Millisecond, "the superseded proxy listener is closed")
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
