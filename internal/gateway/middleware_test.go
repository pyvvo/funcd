package gateway_test

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/gateway"
)

// scenario: middleware-chain-order — the chain runs middlewares in declared order
// (outermost first), and a rejecting middleware short-circuits before the handler.
func TestScenarioMiddlewareChainOrder(t *testing.T) {
	t.Parallel()

	var order []string
	mark := func(name string) gateway.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		order = append(order, "handler")
		w.WriteHeader(http.StatusOK)
	})

	gateway.Chain(handler, mark("a"), mark("b")).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, []string{"a", "b", "handler"}, order, "outermost middleware runs first")

	// A rejecting middleware short-circuits before inner middlewares and the handler.
	order = nil
	reject := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
	}
	rec := httptest.NewRecorder()
	gateway.Chain(handler, reject, mark("after")).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Empty(t, order, "a rejecting outer middleware short-circuits the chain")
}

// Recover turns a handler panic into a problem+json 500 instead of crashing.
func TestRecoverMiddleware(t *testing.T) {
	t.Parallel()
	panicker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		var nilMap map[string]int
		nilMap["boom"] = 1 //nolint:staticcheck // deliberate nil-map write to trigger a panic for Recover
	})
	rec := httptest.NewRecorder()
	gateway.Recover(panicker).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/problem+json")
}

// Issue #91: a mid-stream abort (httputil.ReverseProxy panics http.ErrAbortHandler when the
// upstream dies after the response committed) must reach the client as a truncation, not be
// rewritten into a clean 200 with a problem blob appended.
func TestIssue91_RecoverRepanicsErrAbortHandler(t *testing.T) {
	t.Parallel()
	streamer := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"rows":[1,2,3,`))
		_ = http.NewResponseController(w).Flush()
		panic(http.ErrAbortHandler)
	})
	srv := httptest.NewServer(gateway.Recover(streamer))
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF, "an aborted stream must surface as a truncated body")
	require.NotContains(t, string(body), "urn:funcd:problem", "no problem may be written into a committed stream")
}

// Issue #338: a plain panic after the response committed can no longer become a problem+json 500.
// Recover must abort the connection (the client sees a truncated body, not a clean 200 with a problem
// appended) and leave the panic to net/http, which logs it.
func TestIssue338_RecoverAbortsCommittedResponseOnPanic(t *testing.T) {
	t.Parallel()
	streamer := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial-"))
		_ = http.NewResponseController(w).Flush()
		panic("boom after commit")
	})
	srv := httptest.NewUnstartedServer(gateway.Recover(streamer))
	logs := &syncBuffer{}
	srv.Config.ErrorLog = log.New(logs, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF, "a panic in a committed response must surface as a truncated body")
	require.Equal(t, "partial-", string(body), "no problem may be appended to a committed response")
	require.Contains(t, logs.String(), "boom after commit", "the panic must still be reported")
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// RequestID mints/echoes an X-Request-Id and exposes it via the context.
func TestRequestIDMiddleware(t *testing.T) {
	t.Parallel()
	var seen string
	h := gateway.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = gateway.RequestIDFromContext(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.NotEmpty(t, seen, "request id available in context")
	require.Equal(t, seen, rec.Header().Get("X-Request-Id"), "same id echoed on the response")
}
