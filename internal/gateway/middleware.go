package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"

	"github.com/pyvvo/funcd/api/fault"
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

// Recover returns a middleware that turns a handler panic into an RFC 9457
// problem+json 500 instead of crashing the connection. As the operation's owner
// (ADR-0002 §3) it logs the panic once to logger (nil means slog.Default()), with
// its value, stack and request id; the client's problem omits the panic text. A
// panic is re-panicked when no problem can be written: http.ErrAbortHandler
// (net/http's signal to abort, e.g. a proxied stream whose upstream died
// mid-body), or any panic after the response committed (#338). net/http then
// aborts the connection, so the client sees the truncation, and logs any panic
// other than ErrAbortHandler.
func Recover(logger *slog.Logger) Middleware {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cw := &commitWriter{ResponseWriter: w}
			defer func() {
				if rec := recover(); rec != nil {
					if err, ok := rec.(error); cw.committed || ok && errors.Is(err, http.ErrAbortHandler) {
						panic(rec)
					}
					// Recover runs outside RequestID, so the id is read from the response header it set.
					logger.ErrorContext(r.Context(), "handler panic",
						"panic", fmt.Sprint(rec), "request_id", w.Header().Get("X-Request-Id"), "stack", string(debug.Stack()))
					fault.WriteProblem(w, fault.Internalf("gateway.Recover", "handler panic"))
				}
			}()
			next.ServeHTTP(cw, r)
		})
	}
}

// commitWriter records whether the response has committed (a final status, a
// body byte, a flush or a hijack). It implements http.Flusher and http.Hijacker
// directly, as inner wrappers type-assert them, and Unwrap for
// http.ResponseController.
type commitWriter struct {
	http.ResponseWriter
	committed bool
}

func (c *commitWriter) WriteHeader(code int) {
	c.ResponseWriter.WriteHeader(code)
	if code >= 200 || code == http.StatusSwitchingProtocols {
		c.committed = true
	}
}

func (c *commitWriter) Write(b []byte) (int, error) {
	c.committed = true
	return c.ResponseWriter.Write(b)
}

func (c *commitWriter) Flush() {
	c.committed = true
	_ = http.NewResponseController(c.ResponseWriter).Flush()
}

func (c *commitWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, brw, err := http.NewResponseController(c.ResponseWriter).Hijack()
	if err == nil {
		c.committed = true
	}
	return conn, brw, err
}

func (c *commitWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

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
