package gateway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/internal/gateway"
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
