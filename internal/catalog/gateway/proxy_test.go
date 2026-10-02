package gateway

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
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
