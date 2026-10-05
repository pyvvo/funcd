package local_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	iblob "github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

const testPool v1.ObjectName = "__pool__nodejs22__agents__0123456789abcdef"

// poolCalls records each call a local API port receives, with the caller it was made for.
type poolCalls struct {
	mu    sync.Mutex
	calls []string
}

func (p *poolCalls) add(call string) {
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()
}

func (p *poolCalls) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.calls
	p.calls = nil
	return out
}

// poolStore serves every caller a Function that links alias "x" to "t".
type poolStore struct{ *poolCalls }

func (p poolStore) Get(_ context.Context, _ v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	p.add("store " + string(ns) + "/" + string(name))
	f := &v1.Function{Spec: v1.FunctionSpec{Links: []v1.FunctionLink{{Alias: "x", Target: "t"}}}}
	f.Name, f.Namespace = name, ns
	return f, nil
}

type poolInvoker struct{ *poolCalls }

func (p poolInvoker) Invoke(_ context.Context, target local.Ref, _ []byte, _ time.Duration) ([]byte, error) {
	p.add("invoke " + target.String())
	return []byte(`{}`), nil
}

// poolPort is the KV port, and with SignedURL the blob port; port names which one it is in the record.
type poolPort struct {
	*poolCalls
	port string
}

func (p poolPort) record(ns v1.NamespaceName, fn v1.ObjectName) {
	p.add(p.port + " " + string(ns) + "/" + string(fn))
}

func (p poolPort) Get(_ context.Context, ns v1.NamespaceName, fn v1.ObjectName, _, _ string) ([]byte, bool, error) {
	p.record(ns, fn)
	return []byte("v"), true, nil
}

func (p poolPort) Put(_ context.Context, ns v1.NamespaceName, fn v1.ObjectName, _, _ string, _ []byte) error {
	p.record(ns, fn)
	return nil
}

func (p poolPort) Delete(_ context.Context, ns v1.NamespaceName, fn v1.ObjectName, _, _ string) error {
	p.record(ns, fn)
	return nil
}

func (p poolPort) List(_ context.Context, ns v1.NamespaceName, fn v1.ObjectName, _, _ string) ([]string, error) {
	p.record(ns, fn)
	return nil, nil
}

type poolBlob struct{ poolPort }

func (p poolBlob) SignedURL(_ context.Context, ns v1.NamespaceName, fn v1.ObjectName, _, _ string, _ iblob.SignOptions) (string, error) {
	p.record(ns, fn)
	return "https://signed.example/k", nil
}

func poolManager(t *testing.T) (*local.Manager, *poolCalls) {
	t.Helper()
	dir, err := os.MkdirTemp("", "fl") // not t.TempDir(): a unix socket path is capped near 104 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	rec := &poolCalls{}
	m := local.NewManager(dir, poolStore{rec}, poolInvoker{rec}, nil, poolPort{rec, "kv"},
		poolBlob{poolPort{rec, "blob"}}, slog.New(slog.DiscardHandler))
	t.Cleanup(m.Close)
	return m, rec
}

func unixClient(t *testing.T, sock string) *http.Client {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

// callAs sends one request carrying a MemberHeader per name and returns its status.
func callAs(t *testing.T, c *http.Client, method, path string, names ...string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, "http://local"+path, strings.NewReader(`{}`))
	require.NoError(t, err)
	for _, name := range names {
		req.Header.Add(local.MemberHeader, name)
	}
	resp, err := c.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// scenario: pool-refuses-outsider — a request on a pool's local API is served as the member it names; one
// naming a Function outside the pool, or no member, is refused 403 and reaches no port.
func TestScenarioPoolRefusesOutsider(t *testing.T) {
	m, rec := poolManager(t)
	_, err := m.PoolSocketFor("team-a", "__pool__nodejs22__other__0123456789abcdef", []v1.ObjectName{"c"})
	require.NoError(t, err)
	sock, err := m.PoolSocketFor("team-a", testPool, []v1.ObjectName{"a", "b"})
	require.NoError(t, err)
	c := unixClient(t, sock)

	for _, tc := range []struct {
		name, method, path string
		served             []string
	}{
		{"invoke", http.MethodPost, "/invoke/x", []string{"store team-a/b", "invoke team-a/t"}},
		{"kv", http.MethodGet, "/kv/k/key", []string{"kv team-a/b"}},
		{"blob", http.MethodGet, "/blob/k/key", []string{"blob team-a/b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, names := range [][]string{{"c"}, nil, {""}, {"b", "c"}} {
				require.Equal(t, http.StatusForbidden, callAs(t, c, tc.method, tc.path, names...), "member header %q", names)
				require.Empty(t, rec.take(), "a refused request reaches no port (member header %q)", names)
			}
			require.Equal(t, http.StatusOK, callAs(t, c, tc.method, tc.path, "b"))
			require.Equal(t, tc.served, rec.take(), "served as member b")
		})
	}
}

func TestPoolSocketForReplacesMembers(t *testing.T) {
	m, rec := poolManager(t)
	sock, err := m.PoolSocketFor("team-a", testPool, []v1.ObjectName{"a", "b"})
	require.NoError(t, err)
	c := unixClient(t, sock)
	require.Equal(t, http.StatusOK, callAs(t, c, http.MethodGet, "/kv/k/key", "b"))
	rec.take()

	again, err := m.PoolSocketFor("team-a", testPool, []v1.ObjectName{"a"})
	require.NoError(t, err)
	require.Equal(t, sock, again, "the pool keeps its socket path")
	require.Equal(t, http.StatusForbidden, callAs(t, c, http.MethodGet, "/kv/k/key", "b"), "a member that left is refused")
	require.Empty(t, rec.take())
	require.Equal(t, http.StatusOK, callAs(t, c, http.MethodGet, "/kv/k/key", "a"))
	require.Equal(t, []string{"kv team-a/a"}, rec.take())
}

func TestPoolSocketForRemove(t *testing.T) {
	m, _ := poolManager(t)
	sock, err := m.PoolSocketFor("team-a", testPool, []v1.ObjectName{"a"})
	require.NoError(t, err)
	conn, err := net.Dial("unix", sock)
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	m.Remove("team-a", testPool)
	require.NoFileExists(t, sock)
	if conn, err := net.Dial("unix", sock); err == nil {
		_ = conn.Close()
		t.Fatal("a removed pool's local API still accepts connections")
	}
}

func TestSoloSocketIgnoresMemberHeader(t *testing.T) {
	m, rec := poolManager(t)
	sock, err := m.SocketFor("team-a", "a")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, callAs(t, unixClient(t, sock), http.MethodGet, "/kv/k/key", "c"))
	require.Equal(t, []string{"kv team-a/a"}, rec.take(), "a function's own socket serves it, whatever the header names")
}
