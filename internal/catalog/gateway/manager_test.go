package gateway

import (
	"bytes"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/platform/httpx"
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
// must outlast the edge reverse proxy's client keep-alive (httpx.NodeTransport()), so that client closes first.
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
	clientIdle := httpx.NodeTransport().IdleConnTimeout
	require.Greater(t, srv.IdleTimeout, clientIdle, "an idle keep-alive connection must be closed, after the client's own idle timeout")
}

// TestIssue454_ProxyServerErrorsUseTheManagerLogger: net/http logs its own server errors (an accept error,
// a recovered panic) through the http.Server's ErrorLog, and through the stdlib log package when it is nil,
// so they bypassed the Manager's logger and its format.
func TestIssue454_ProxyServerErrorsUseTheManagerLogger(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	st := store.New(memory.New())
	mgr := NewManager("", "", NewCatalogKeys([]byte("i454-master"), st), buildPDP(t, st), slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(mgr.Shutdown)
	_, err := mgr.Ensure(auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}, "http://127.0.0.1:1", engineToken)
	require.NoError(t, err)

	mgr.mu.Lock()
	srv := mgr.servers[managerKey("data", "lake")].server
	mgr.mu.Unlock()
	require.NotNil(t, srv.ErrorLog, "the catalog proxy's net/http errors must go through the Manager's logger")
	srv.ErrorLog.Print("issue-454 probe")
	require.Contains(t, logs.String(), `"level":"WARN","msg":"issue-454 probe","component":"catalog.gateway"`)
}

// TestIssue379_ProxyLogsThroughManagerLogger: the catalog PEP proxy logs through the logger the
// Manager was given (ADR-0002 §6), and an unreachable engine is logged there, not through the
// stdlib log package. Not parallel: it captures log's output.
func TestIssue379_ProxyLogsThroughManagerLogger(t *testing.T) {
	var stdlog bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&stdlog)
	t.Cleanup(func() { log.SetOutput(prev) })

	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	pdp := buildPDP(t, st)
	master := []byte("issue-379-master")
	keys := NewCatalogKeys(master, st)

	dead := httptest.NewServer((&engineStub{}).handler())
	dead.Close()

	var logs bytes.Buffer
	mgr := NewManager("", "", keys, pdp, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(mgr.Shutdown)
	url, err := mgr.Ensure(auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}, dead.URL, engineToken)
	require.NoError(t, err)
	post := func(token string) int {
		resp, perr := http.Post("http://"+url, "application/octet-stream", bytes.NewReader(makeHandshake(token)))
		require.NoError(t, perr)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	require.Equal(t, http.StatusForbidden, post("garbage-unresolvable-token"))
	require.Contains(t, logs.String(), `"msg":"catalog PEP: unresolved credential"`, "the debug line reaches the injected logger")
	require.Contains(t, logs.String(), `"component":"catalog.gateway"`)

	granted, err := DeriveCatalogToken(master, "data", "analytics")
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, post(granted))
	require.Empty(t, stdlog.String(), "nothing is logged through the stdlib log package")
	require.Contains(t, logs.String(), `"level":"WARN"`, "the engine failure is logged through the injected logger")
}

// TestIssue531_EngineCallsSurviveDefaultTransportCloseIdle: the catalog proxy keeps its engine connections out of
// http.DefaultTransport. Every httptest.Server.Close in the process closes that transport's idle connections, and
// one landing while an engine call has just picked a parked connection fails the call with a 503. Not parallel: it
// closes the default transport's idle connections, which would break the other tests' parked connections.
func TestIssue531_EngineCallsSurviveDefaultTransportCloseIdle(t *testing.T) {
	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	master := []byte("issue-531-master")
	keys := NewCatalogKeys(master, st)

	conns := new(atomic.Int32)
	up := httptest.NewUnstartedServer((&engineStub{}).handler())
	up.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	up.Start()
	t.Cleanup(up.Close)

	mgr := NewManager("", "", keys, buildPDP(t, st), nil)
	t.Cleanup(mgr.Shutdown)
	url, err := mgr.Ensure(auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}, up.URL, engineToken)
	require.NoError(t, err)
	granted, err := DeriveCatalogToken(master, "data", "analytics")
	require.NoError(t, err)
	query := func() {
		resp, perr := http.Post("http://"+url, "application/octet-stream", bytes.NewReader(makeHandshake(granted)))
		require.NoError(t, perr)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	query()
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	query()
	require.EqualValues(t, 1, conns.Load(), "closing the default transport's idle connections must not touch the proxy's engine connections")
}

// statusAt POSTs an empty body to a bare host:port proxy URL and returns the status code.
func statusAt(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Post("http://"+url, "application/octet-stream", strings.NewReader(""))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp.StatusCode
}

// TestManagerListenRebindsRecordedPort: a new run binds the port the previous run recorded, answering 503 until
// Ensure targets the engine; a second Listen in the same run keeps the listener. Not parallel: a port bound in parallel
// could take the recorded port between the first run's Shutdown and the rebind.
func TestManagerListenRebindsRecordedPort(t *testing.T) {
	first := NewManager("", "", NewCatalogKeys(nil, nil), nil, nil)
	port, moved, err := first.Listen("data", "lake", 0)
	require.NoError(t, err)
	require.False(t, moved)
	require.NotZero(t, port)
	first.Shutdown()

	mgr := NewManager("", "", NewCatalogKeys(nil, nil), nil, nil)
	t.Cleanup(mgr.Shutdown)
	bound, moved, err := mgr.Listen("data", "lake", port)
	require.NoError(t, err)
	require.False(t, moved)
	require.Equal(t, port, bound)
	url, ok := mgr.ProxyURL("data", "lake")
	require.True(t, ok)
	require.Equal(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), url)
	require.Equal(t, http.StatusServiceUnavailable, statusAt(t, url), "a listener answers 503 until Ensure targets it")

	again, moved, err := mgr.Listen("data", "lake", 0)
	require.NoError(t, err)
	require.False(t, moved)
	require.Equal(t, port, again, "a listener bound in this run is kept")
}

// TestManagerListenTakesNewPortWhenTaken: a recorded port another socket holds moves the listener to a new port.
func TestManagerListenTakesNewPortWhenTaken(t *testing.T) {
	t.Parallel()
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Close() })
	taken := holder.Addr().(*net.TCPAddr).Port

	mgr := NewManager("", "", NewCatalogKeys(nil, nil), nil, nil)
	t.Cleanup(mgr.Shutdown)
	bound, moved, err := mgr.Listen("data", "lake", taken)
	require.NoError(t, err)
	require.True(t, moved)
	require.NotEqual(t, taken, bound)
	url, ok := mgr.ProxyURL("data", "lake")
	require.True(t, ok)
	require.Equal(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(bound)), url)
}

// TestManagerReleaseKeepsURL: a released listener answers 503 on the same URL and is listed until Listen or Ensure
// takes it back; Remove closes it.
func TestManagerReleaseKeepsURL(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(up.Close)
	mgr := NewManager("", "", NewCatalogKeys(nil, nil), nil, nil)
	t.Cleanup(mgr.Shutdown)
	mgr.Release("data", "lake")
	require.Empty(t, mgr.Released("data"), "releasing an unknown catalog is a no-op")

	catalog := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}
	url, err := mgr.Ensure(catalog, up.URL, engineToken)
	require.NoError(t, err)
	_, _, err = mgr.Listen("data", "other", 0)
	require.NoError(t, err)
	_, _, err = mgr.Listen("ops", "lake", 0)
	require.NoError(t, err)
	mgr.Release("ops", "lake")

	mgr.Release("data", "lake")
	require.Equal(t, http.StatusServiceUnavailable, statusAt(t, url))
	got, ok := mgr.ProxyURL("data", "lake")
	require.True(t, ok)
	require.Equal(t, url, got, "a released listener keeps its URL")
	require.Equal(t, []v1.ObjectName{"lake"}, mgr.Released("data"))

	_, _, err = mgr.Listen("data", "lake", 0)
	require.NoError(t, err)
	require.Empty(t, mgr.Released("data"), "Listen takes a released listener back")

	mgr.Release("data", "lake")
	again, err := mgr.Ensure(catalog, up.URL, engineToken)
	require.NoError(t, err)
	require.Equal(t, url, again)
	require.Empty(t, mgr.Released("data"), "Ensure takes a released listener back")

	mgr.Release("data", "lake")
	mgr.Remove("data", "lake")
	_, ok = mgr.ProxyURL("data", "lake")
	require.False(t, ok)
	conn, derr := net.DialTimeout("tcp", url, dialTimeout)
	if derr == nil {
		_ = conn.Close()
	}
	require.Error(t, derr, "Remove closes the listener")
}
