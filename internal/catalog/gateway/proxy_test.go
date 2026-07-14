package gateway

import (
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
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
