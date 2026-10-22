package funcd

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/testkit/freeport"
)

// feedEndpoint applies the database name and returns the change feed URL its engine is given (ADR-0235 Decision 1).
func (e *sqlEnv) feedEndpoint(t *testing.T, name string) *url.URL {
	t.Helper()
	require.NoError(t, e.apply(t, sqlDatabase(name)))
	e.waitReady(t, name, 1)
	spec, _, _, _ := e.rt.spec(sqlRef(name))
	u, err := url.Parse(spec.Env["FUNCD_SQL_CDC_ENDPOINT"])
	require.NoError(t, err)
	return u
}

// delivered reports whether a change posted to endpoint, as the database's engine posts it, is taken: a feed that
// does not know the endpoint's token answers 404.
func delivered(endpoint *url.URL) bool {
	resp, err := http.Post(endpoint.String(), "application/json", strings.NewReader(delivery("e1", 1, insertMilk)))
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// requirePortHeld fails unless a plain bind of the loopback port fails.
func requirePortHeld(t *testing.T, port int) {
	t.Helper()
	if ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err == nil {
		_ = ln.Close()
		t.Fatalf("port %d is not held", port)
	}
}

// scenario: platforms-own-feeds — two platforms in one process, on loopback with sql.changeFeedPort unset: each feed
// listens on its own port from the start, neither logs that it cannot listen yet, and each platform's engines are
// given their own feed's URL.
func TestScenarioPlatformsOwnFeeds(t *testing.T) {
	t.Parallel()
	var logs []*syncBuffer
	ports := map[string]bool{}
	for _, name := range []string{"todo-a", "todo-b"} {
		buf := &syncBuffer{}
		logs = append(logs, buf)
		e := startSQL(t, newSQLRuntime(t), WithLogger(slog.New(slog.NewTextHandler(buf, nil))))
		port := e.p.sqlFeedListener.Addr().(*net.TCPAddr).Port
		requirePortHeld(t, port)
		u := e.feedEndpoint(t, name)
		require.Equal(t, "127.0.0.1:"+strconv.Itoa(port), u.Host, "the engines are given their own feed")
		require.Eventually(t, func() bool { return delivered(u) }, 5*time.Second, 10*time.Millisecond)
		ports[u.Port()] = true
	}
	require.Len(t, ports, 2)
	for _, buf := range logs {
		require.NotContains(t, buf.String(), "cannot listen yet")
	}
}

// Issue #1154: every platform on loopback served its change feed on the fixed port 7075, so while two ran, an engine's
// changes reached whichever platform held the port, which refuses a token it does not know. Each platform's engine
// now reaches its own feed.
func TestIssue1154_PlatformsOnLoopbackDoNotShareTheFeedPort(t *testing.T) {
	t.Parallel()
	a, b := startSQL(t, newSQLRuntime(t)), startSQL(t, newSQLRuntime(t))
	for _, c := range []struct {
		e    *sqlEnv
		name string
	}{{a, "todo-a"}, {b, "todo-b"}} {
		u := c.e.feedEndpoint(t, c.name)
		require.Eventually(t, func() bool { return delivered(u) }, 5*time.Second, 10*time.Millisecond,
			"%s's engine reaches the feed that knows its token", c.name)
	}
}

// scenario: set-port-kept — with sql.changeFeedPort set on loopback, the feed listens on 127.0.0.1 at that port and the
// engines' URL names it.
func TestScenarioSetPortKept(t *testing.T) {
	t.Parallel()
	port := freeport.Port(t)
	e := startSQL(t, newSQLRuntime(t), WithSQLChangeFeedPort(port))
	u := e.feedEndpoint(t, "todo-db")
	require.Equal(t, "127.0.0.1:"+strconv.Itoa(port), u.Host)
	require.Eventually(t, func() bool { return delivered(u) }, 5*time.Second, 10*time.Millisecond)
}

// scenario: bridge-keeps-7075 — with a bridge address and sql.changeFeedPort unset, nothing binds loopback and the
// engines' URL names the bridge address and 7075.
func TestScenarioBridgeKeeps7075(t *testing.T) {
	t.Parallel()
	e := startSQL(t, newSQLRuntime(t), WithBridgeAddress(netip.MustParseAddr("10.63.0.1")),
		WithLogger(slog.New(slog.DiscardHandler)))
	require.Nil(t, e.p.sqlFeedListener)
	require.Equal(t, "10.63.0.1:7075", e.feedEndpoint(t, "todo-db").Host)
}

// Shutdown closes the loopback feed listener New bound, whether or not Run served it (ADR-0260 Contracts).
func TestShutdownReleasesTheLoopbackFeedPort(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory(), WithSQLProviderRuntime(newSQLRuntime(t)), WithLogger(slog.New(slog.DiscardHandler)))
	require.NoError(t, err)
	port := p.sqlFeedListener.Addr().(*net.TCPAddr).Port
	requirePortHeld(t, port)
	require.NoError(t, p.Shutdown(context.Background()))
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	require.NoError(t, err, "the port is free once the platform shut down")
	_ = ln.Close()
}
