package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// Middleware wraps an http.Handler with one ingress concern (auth, rate-limit,
// recover, …). The composition root builds the gateway's served handler as
// Chain(gw.Handler(), Recover, RequestID, …) — see ADR-0013.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares around h in declared order: Chain(h, a, b) runs a
// (outermost), then b, then h. A middleware that responds without calling next
// short-circuits the rest of the chain.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// requestIDKey is the context key for the request id (struct key — no global).
type requestIDKey struct{}

// Recover is a middleware that turns a handler panic into an RFC 9457
// problem+json 500 instead of crashing the connection.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				fault.WriteProblem(w, fault.Internalf("gateway.Recover", "handler panic: %v", rec))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// RequestID is a middleware that ensures every request carries an X-Request-Id:
// it reuses an inbound id or mints one, echoes it on the response, and stores it
// in the request context (RequestIDFromContext reads it).
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the request id set by RequestID, or "" if none.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}
