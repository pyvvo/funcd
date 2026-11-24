package local_test

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// headerInvoker records the headers of each fetch it forwards, as the data plane would get them.
type headerInvoker struct {
	mu   sync.Mutex
	seen []http.Header
}

func (h *headerInvoker) Invoke(context.Context, local.Ref, []byte, time.Duration) ([]byte, error) {
	return []byte(`{}`), nil
}

func (h *headerInvoker) Fetch(_ context.Context, _ local.Ref, req local.FetchRequest, _ time.Duration) (local.FetchReply, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = append(h.seen, req.Header.Clone())
	return local.FetchReply{Status: http.StatusOK, Body: []byte("priced")}, nil
}

func (h *headerInvoker) got() []http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seen
}

// ADR-0346 Decision 6: a dev container worker's listener serves its caller only a request that carries its token,
// strips the token before the pass-through, answers 403 to another token or none, and goes with ReleaseListener.
func TestDevWorkerListenerChecksItsToken(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "lst")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	pricing := &v1.Function{Spec: v1.FunctionSpec{Protocol: v1.ProtocolHTTP}}
	pricing.Name, pricing.Namespace = "pricing", "team-a"
	inv := &headerInvoker{}
	m := local.NewManager(dir, nameStore{"a": callerFn(v1.FunctionLink{Alias: "pricing", Target: "pricing"}), "pricing": pricing},
		inv, nil, nil, nil)
	t.Cleanup(m.Close)

	url, token, err := m.ListenerFor(local.FunctionRef("team-a", "a"), "team-a/a/a-1/r0")
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+$`), url)
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), token)
	again, sameToken, err := m.ListenerFor(local.FunctionRef("team-a", "a"), "team-a/a/a-1/r0")
	require.NoError(t, err)
	require.Equal(t, url, again)
	require.Equal(t, token, sameToken)

	fetch := func(tokens ...string) int {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url+"/fetch/pricing/quote", nil)
		require.NoError(t, err)
		for _, tok := range tokens {
			req.Header.Add(local.TokenHeader, tok)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	require.Equal(t, http.StatusOK, fetch(token))
	require.Len(t, inv.got(), 1)
	require.Empty(t, inv.got()[0].Values(local.TokenHeader), "the token never reaches the target")
	require.Equal(t, http.StatusForbidden, fetch())
	require.Equal(t, http.StatusForbidden, fetch(strings.Repeat("0", 64)))
	require.Equal(t, http.StatusForbidden, fetch(token, token), "one token, once")
	require.Len(t, inv.got(), 1, "a refused request passes nothing through")

	m.ReleaseListener("team-a/a/a-1/r0")
	_, err = http.Get(url + "/fetch/pricing/quote") //nolint:noctx // a refused dial needs no context
	require.Error(t, err, "the listener goes with its worker")
}

// ADR-0346 Decision 6: the Function's own Remove stops its workers' listeners too.
func TestDevWorkerListenerGoesWithItsFunction(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "lst")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	m := local.NewManager(dir, fakeStore{}, fakeInvoker{}, nil, nil, nil)
	t.Cleanup(m.Close)
	url, _, err := m.ListenerFor(local.FunctionRef("team-a", "a"), "team-a/a/a-1/r0")
	require.NoError(t, err)
	other, _, err := m.ListenerFor(local.FunctionRef("team-a", "b"), "team-a/b/b-1/r0")
	require.NoError(t, err)

	m.Remove(local.FunctionRef("team-a", "a"))
	_, err = http.Get(url) //nolint:noctx // a refused dial needs no context
	require.Error(t, err)
	resp, err := http.Get(other) //nolint:noctx // b's listener still answers
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}
