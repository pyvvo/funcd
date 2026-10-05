package gateway

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// makeHandshake builds a minimal Quack handshake frame carrying token as its first TLV field (id 0x01),
// with a trailing opaque field — the shape swapHandshakeToken parses.
func makeHandshake(token string) []byte {
	nt := []byte(token)
	b := make([]byte, preambleLen)    // 8-byte zero preamble
	b = append(b, tokenFieldID, 0x00) // token field: id 0x01 + the constant 0x00
	var lenBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lenBuf[:], uint64(len(nt))) // length as an UNSIGNED LEB128 VARINT (2 bytes for >=128, like a JWT)
	b = append(b, lenBuf[:n]...)
	b = append(b, nt...)
	b = append(b, 0x02, 0x00, 0x03, 'e', 'n', 'd') // an opaque trailing field
	return b
}

// engineStub is an httptest engine that records whether it was hit and the last body it received.
type engineStub struct {
	hit  bool
	body []byte
}

func (e *engineStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		e.hit = true
		e.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}
}

const engineToken = "SHARED-ENGINE-TOKEN"

// TestCatalogProxy_AllowDeny exercises the full PEP proxy against an httptest engine stub: a granted
// Function token is forwarded with the caller token SWAPPED for the shared engine token; an ungranted
// token is 403'd with the upstream never called; an unresolvable token is 403'd.
func TestCatalogProxy_AllowDeny(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st) // Function "analytics" bound to "lake"; "reporting" unbound
	pdp := buildPDP(t, st)
	master := []byte("proxy-test-node-master")
	keys := NewCatalogKeys(master, st)

	target := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}

	newProxy := func(stub *engineStub) *httptest.Server {
		up := httptest.NewServer(stub.handler())
		t.Cleanup(up.Close)
		h := NewCatalogProxy(keys, pdp, EngineTarget{Catalog: target, Upstream: up.URL, EngineToken: engineToken})
		front := httptest.NewServer(h)
		t.Cleanup(front.Close)
		return front
	}

	post := func(front *httptest.Server, body []byte) *http.Response {
		resp, err := http.Post(front.URL, "application/octet-stream", strings.NewReader(string(body)))
		require.NoError(t, err)
		return resp
	}

	// granted: analytics is bound to lake ⇒ allow ⇒ forwarded with the engine token swapped in.
	stub := &engineStub{}
	front := newProxy(stub)
	granted, grantedErr := DeriveCatalogToken(master, "data", "analytics")
	require.NoError(t, grantedErr)
	resp := post(front, makeHandshake(granted))
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "a granted Function query is forwarded")
	require.True(t, stub.hit, "the engine was reached")
	_, forwardedToken, ok := swapHandshakeToken(stub.body, "x")
	require.True(t, ok, "the forwarded body is still a valid handshake")
	require.Equal(t, engineToken, forwardedToken, "the engine sees the SHARED engine token, not the caller token")
	require.NotContains(t, string(stub.body), granted, "the caller token never reaches the engine")

	// ungranted: reporting is unbound ⇒ deny ⇒ 403, upstream never called.
	stub2 := &engineStub{}
	front2 := newProxy(stub2)
	ungranted, ungrantedErr := DeriveCatalogToken(master, "data", "reporting")
	require.NoError(t, ungrantedErr)
	resp2 := post(front2, makeHandshake(ungranted))
	_ = resp2.Body.Close()
	require.Equal(t, http.StatusForbidden, resp2.StatusCode, "an unbound Function is 403'd")
	require.False(t, stub2.hit, "the engine is never called on a deny")

	// unknown: an unresolvable token ⇒ 403, upstream never called.
	stub3 := &engineStub{}
	front3 := newProxy(stub3)
	resp3 := post(front3, makeHandshake("garbage-unresolvable-token"))
	_ = resp3.Body.Close()
	require.Equal(t, http.StatusForbidden, resp3.StatusCode, "an unresolvable credential is 403'd")
	require.False(t, stub3.hit, "the engine is never reached by an unresolved caller")
}

// countingReader counts the bytes its reader handed out.
type countingReader struct {
	r    io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

// gatedBody hands out first, then holds the body open until release is closed: a body still
// being sent.
type gatedBody struct {
	first   []byte
	release <-chan struct{}
}

func (g *gatedBody) Read(p []byte) (int, error) {
	if len(g.first) > 0 {
		n := copy(p, g.first)
		g.first = g.first[n:]
		return n, nil
	}
	select {
	case <-g.release:
		return 0, io.EOF
	case <-time.After(5 * time.Second):
		return 0, errors.New("the engine never saw the body while it was being sent")
	}
}

// TestIssue39_ProxyStreamsTheBody: the PEP needs only the handshake's token field, so the proxy
// reads no more than that before it denies, and streams the rest of a body to the engine instead
// of holding all of it in daemon memory.
func TestIssue39_ProxyStreamsTheBody(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	pdp := buildPDP(t, st)
	master := []byte("proxy-test-node-master")
	keys := NewCatalogKeys(master, st)
	target := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}
	proxyTo := func(engine http.Handler) http.Handler {
		up := httptest.NewServer(engine)
		t.Cleanup(up.Close)
		return NewCatalogProxy(keys, pdp, EngineTarget{Catalog: target, Upstream: up.URL, EngineToken: engineToken})
	}

	t.Run("a denied handshake is rejected before its body is read", func(t *testing.T) {
		stub := &engineStub{}
		h := proxyTo(stub.handler())
		body := &countingReader{r: io.MultiReader(
			bytes.NewReader(makeHandshake("garbage-unresolvable-token")),
			bytes.NewReader(make([]byte, 16<<20)),
		)}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/quack", body))
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.False(t, stub.hit, "the engine is never reached by an unresolved caller")
		require.Less(t, body.read, int64(1<<20), "the proxy read %d bytes of an unauthenticated body", body.read)
	})

	t.Run("a forwarded body streams to the engine while it is still being sent", func(t *testing.T) {
		started := make(chan struct{})
		var received int64
		front := httptest.NewServer(proxyTo(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var one [1]byte
			n, _ := io.ReadFull(r.Body, one[:])
			close(started)
			rest, _ := io.Copy(io.Discard, r.Body)
			received = int64(n) + rest
			w.WriteHeader(http.StatusOK)
		})))
		t.Cleanup(front.Close)

		const sent = 1 << 20
		resp, err := http.Post(front.URL, "application/octet-stream", &gatedBody{first: make([]byte, sent), release: started})
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, int64(sent), received, "the engine receives the whole body")
	})

	t.Run("an allowed handshake larger than its token field keeps its tail and length", func(t *testing.T) {
		var got []byte
		var gotLen int64
		front := httptest.NewServer(proxyTo(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotLen = r.ContentLength
			got, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		})))
		t.Cleanup(front.Close)

		granted, err := DeriveCatalogToken(master, "data", "analytics")
		require.NoError(t, err)
		tail := bytes.Repeat([]byte("q"), 1<<20)
		resp, err := http.Post(front.URL, "application/octet-stream", bytes.NewReader(append(makeHandshake(granted), tail...)))
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		_, forwarded, ok := swapHandshakeToken(got, "x")
		require.True(t, ok)
		require.Equal(t, engineToken, forwarded, "the engine sees the shared engine token")
		require.True(t, bytes.HasSuffix(got, tail), "the body after the token field is forwarded intact")
		require.Equal(t, int64(len(got)), gotLen, "the forwarded Content-Length matches the rewritten body")
	})
}

// pdpFailure is a PDP that cannot decide.
type pdpFailure struct{}

func (pdpFailure) Authorize(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{}, errors.New("policy store unavailable")
}

// failingBody fails on its first read.
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("client went away") }

// TestIssue378_ProxyErrorsAreProblemJSONViaSlog: a failed engine call is a problem+json 503 logged once
// through slog, not the ReverseProxy default (a bare 502 logged through the stdlib log package), and the
// proxy's own rejections are problem+json too (ADR-0002). Not parallel: it swaps the global loggers.
func TestIssue378_ProxyErrorsAreProblemJSONViaSlog(t *testing.T) {
	var stdlog, logs bytes.Buffer
	prevSlog, prevOut, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	log.SetOutput(&stdlog) // after SetDefault, which points the stdlib logger at the slog handler
	t.Cleanup(func() {
		slog.SetDefault(prevSlog)
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	pdp := buildPDP(t, st)
	master := []byte("proxy-test-node-master")
	keys := NewCatalogKeys(master, st)
	target := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}
	granted, err := DeriveCatalogToken(master, "data", "analytics")
	require.NoError(t, err)

	stopped := httptest.NewServer(http.NotFoundHandler())
	stopped.Close()
	crashing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if conn, _, herr := w.(http.Hijacker).Hijack(); herr == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(crashing.Close)

	cases := []struct {
		name     string
		pdp      auth.Authorizer
		upstream string
		body     func() io.Reader
		status   int
		problem  string
	}{
		{
			name:     "an unreachable engine",
			pdp:      pdp,
			upstream: stopped.URL,
			body:     func() io.Reader { return bytes.NewReader(makeHandshake(granted)) },
			status:   http.StatusServiceUnavailable,
			problem:  "urn:funcd:problem:unavailable",
		},
		{
			name:     "an engine that fails mid-request",
			pdp:      pdp,
			upstream: crashing.URL,
			body:     func() io.Reader { return bytes.NewReader(makeHandshake(granted)) },
			status:   http.StatusServiceUnavailable,
			problem:  "urn:funcd:problem:unavailable",
		},
		{
			name:     "a denied caller",
			pdp:      pdp,
			upstream: crashing.URL,
			body:     func() io.Reader { return bytes.NewReader(makeHandshake("garbage-unresolvable-token")) },
			status:   http.StatusForbidden,
			problem:  "urn:funcd:problem:forbidden",
		},
		{
			name:     "an unreadable body",
			pdp:      pdp,
			upstream: crashing.URL,
			body:     func() io.Reader { return failingBody{} },
			status:   http.StatusBadRequest,
			problem:  "urn:funcd:problem:invalid",
		},
		{
			name:     "a PDP failure",
			pdp:      pdpFailure{},
			upstream: crashing.URL,
			body:     func() io.Reader { return bytes.NewReader(makeHandshake(granted)) },
			status:   http.StatusInternalServerError,
			problem:  "urn:funcd:problem:internal",
		},
		{
			name:     "a malformed upstream",
			pdp:      pdp,
			upstream: "::bad",
			body:     func() io.Reader { return bytes.NewReader(makeHandshake(granted)) },
			status:   http.StatusInternalServerError,
			problem:  "urn:funcd:problem:internal",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewCatalogProxy(keys, tc.pdp, EngineTarget{Catalog: target, Upstream: tc.upstream, EngineToken: engineToken})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/quack", tc.body()))
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
			require.Contains(t, rec.Body.String(), tc.problem)
		})
	}
	require.Empty(t, stdlog.String(), "nothing is logged through the stdlib log package")
	require.Equal(t, 2, strings.Count(logs.String(), "level=WARN"), "each engine failure is logged once through slog")
}

// TestCatalogProxy_DeniesCrossNamespaceCaller pins the PEP to the endpoint's own CatalogService: a
// principal from another namespace that holds a grant on a same-named catalog in ITS namespace is 403'd
// and never reaches this endpoint's engine; the owner namespace's bound Function is still forwarded.
func TestCatalogProxy_DeniesCrossNamespaceCaller(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	fn := func(ns v1.NamespaceName, name v1.ObjectName) *v1.Function {
		return &v1.Function{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
			ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns, ResourceGroup: "rg1"},
			Spec:       v1.FunctionSpec{Catalogs: []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}}},
		}
	}
	createObj(t, st, fn("victim", "reader"))
	createObj(t, st, fn("attacker", "thief"))
	const minted = "MINTED-ATTACKER-IDENTITY-CATALOG-TOKEN"
	createObj(t, st, &v1.Identity{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindIdentity.GVK().APIVersion(), Kind: v1.KindIdentity},
		ObjectMeta: v1.ObjectMeta{Name: "mallory", Namespace: "attacker", ResourceGroup: "rg1"},
		Spec:       v1.IdentitySpec{Type: v1.IdentityTypeExternal, CredentialSecretName: "mallory-cred"},
	})
	createObj(t, st, &v1.Secret{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret},
		ObjectMeta: v1.ObjectMeta{Name: "mallory-cred", Namespace: "attacker", ResourceGroup: "rg1"},
		Spec:       v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{catalogTokenSecretKey: []byte(minted)}},
	})
	createObj(t, st, &v1.RolesAssignment{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindRolesAssignment.GVK().APIVersion(), Kind: v1.KindRolesAssignment},
		ObjectMeta: v1.ObjectMeta{Name: "mallory-can-query-lake", Namespace: "attacker", ResourceGroup: "rg1"},
		Spec: v1.RolesAssignmentSpec{
			Principal:   &v1.PrincipalRef{Kind: v1.PrincipalKindIdentity, Name: "mallory"},
			Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "Catalog Query Reader"}, Scope: &v1.ScopeRef{Kind: v1.ScopeKindCatalog, Name: "lake"}}},
		},
	})
	master := []byte("proxy-test-node-master")
	keys := NewCatalogKeys(master, st)
	pdp := buildPDP(t, st)
	target := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "victim", Name: "lake"}

	query := func(token string) (int, *engineStub) {
		stub := &engineStub{}
		up := httptest.NewServer(stub.handler())
		defer up.Close()
		front := httptest.NewServer(NewCatalogProxy(keys, pdp, EngineTarget{Catalog: target, Upstream: up.URL, EngineToken: engineToken}))
		defer front.Close()
		resp, err := http.Post(front.URL, "application/octet-stream", bytes.NewReader(makeHandshake(token)))
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode, stub
	}

	owner, err := DeriveCatalogToken(master, "victim", "reader")
	require.NoError(t, err)
	code, stub := query(owner)
	require.Equal(t, http.StatusOK, code, "the owner namespace's bound Function is forwarded")
	require.True(t, stub.hit)

	thief, err := DeriveCatalogToken(master, "attacker", "thief")
	require.NoError(t, err)
	for name, token := range map[string]string{"Function bound to attacker/lake": thief, "Identity granted on attacker/lake": minted} {
		code, stub := query(token)
		require.Equal(t, http.StatusForbidden, code, "%s must not query victim/lake", name)
		require.False(t, stub.hit, "%s must never reach victim/lake's engine", name)
	}
}

// deadOnWriteConn is an engine connection whose peer is gone. Once dead, a write fails with a reset before a byte
// goes out while a read still waits: the transport has not yet read the reset of its parked connection.
type deadOnWriteConn struct {
	net.Conn
	dead atomic.Bool
}

func (c *deadOnWriteConn) Write(p []byte) (int, error) {
	if c.dead.Load() {
		return 0, &net.OpError{Op: "write", Net: "tcp", Err: syscall.ECONNRESET}
	}
	return c.Conn.Write(p)
}

// TestIssue634_ProxyResendsUnsentQueryOnFreshConn: an engine restarted on the address of the one that crashed
// leaves the proxy a parked connection to the dead engine, and the next query's write on it fails before a byte
// goes out. net/http sends such a request again on a fresh connection only when it can rewind the body; the proxy
// holds the whole handshake in memory, so the query must reach the engine instead of failing with a 503.
func TestIssue634_ProxyResendsUnsentQueryOnFreshConn(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedCatalogWorld(t, st)
	master := []byte("issue-634-master")
	keys := NewCatalogKeys(master, st)
	granted, err := DeriveCatalogToken(master, "data", "analytics")
	require.NoError(t, err)

	bodies := make(chan []byte, 2)
	up := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- b
	}))
	t.Cleanup(up.Close)

	conns := make(chan *deadOnWriteConn, 4)
	transport := httpx.Transport()
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, derr := dial(ctx, network, addr)
		if derr != nil {
			return nil, derr
		}
		dc := &deadOnWriteConn{Conn: c}
		conns <- dc
		return dc, nil
	}
	t.Cleanup(transport.CloseIdleConnections)
	target := auth.EntityRef{Type: v1.KindCatalogService, Namespace: "data", Name: "lake"}
	proxy := newCatalogProxy(keys, buildPDP(t, st), EngineTarget{Catalog: target, Upstream: up.URL, EngineToken: engineToken},
		transport, slog.New(slog.DiscardHandler))
	query := func() int {
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/quack", bytes.NewReader(makeHandshake(granted))))
		return rec.Code
	}

	require.Equal(t, http.StatusOK, query())
	<-bodies
	(<-conns).dead.Store(true)

	require.Equal(t, http.StatusOK, query(), "a query never written to the dead engine is sent again on a fresh connection")
	require.Len(t, conns, 1, "the query went out on a fresh connection")
	_, forwarded, ok := swapHandshakeToken(<-bodies, "x")
	require.True(t, ok)
	require.Equal(t, engineToken, forwarded, "the resent query carries the swapped handshake")
}
