// Package shape is the edge shaping middleware (ADR-0114, F78): CORS (preflight + ACAO), response
// header inject/strip, and gzip compression that SKIPS streaming (text/event-stream) and upgrades
// (Connection: Upgrade / 101) and FORWARDS http.Flusher + http.Hijacker so SSE/WebSocket pass through
// unbroken. Additive (never rejects) and pass-through when unconfigured. It is the innermost edge
// middleware so it wraps the real upstream response.
package shape

import (
	"bufio"
	"compress/gzip"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// CORS configures cross-origin response headers + preflight handling.
type CORS struct {
	AllowOrigins  []string
	AllowMethods  []string
	AllowHeaders  []string
	MaxAgeSeconds int
}

// Headers configures response header injection/removal.
type Headers struct {
	Set    map[string]string
	Remove []string
}

// Config toggles the shaping middlewares. A zero Config is a pass-through.
type Config struct {
	CORS        *CORS
	Headers     *Headers
	Compression bool
}

// Chain composes the configured shaping middlewares (CORS → headers → compression, innermost). A zero
// Config is a pass-through.
func Chain(cfg Config) func(http.Handler) http.Handler {
	var mws []func(http.Handler) http.Handler
	if cfg.CORS != nil {
		mws = append(mws, corsMW(*cfg.CORS))
	}
	if cfg.Headers != nil {
		mws = append(mws, headersMW(*cfg.Headers))
	}
	if cfg.Compression {
		mws = append(mws, gzipMW())
	}
	return func(next http.Handler) http.Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			next = mws[i](next)
		}
		return next
	}
}

func corsMW(cfg CORS) func(http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range cfg.AllowOrigins {
		allowed[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowed["*"] || allowed[origin]) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
				if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
					if len(cfg.AllowMethods) > 0 {
						w.Header().Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowMethods, ", "))
					}
					if len(cfg.AllowHeaders) > 0 {
						w.Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowHeaders, ", "))
					}
					if cfg.MaxAgeSeconds > 0 {
						w.Header().Set("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAgeSeconds))
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func headersMW(cfg Headers) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(&headerWriter{ResponseWriter: w, cfg: cfg}, r)
		})
	}
}

type headerWriter struct {
	http.ResponseWriter
	cfg   Headers
	wrote bool
}

func (h *headerWriter) apply() {
	if h.wrote {
		return
	}
	h.wrote = true
	for k, v := range h.cfg.Set {
		h.Header().Set(k, v)
	}
	for _, k := range h.cfg.Remove {
		h.Header().Del(k)
	}
}
func (h *headerWriter) WriteHeader(code int) { h.apply(); h.ResponseWriter.WriteHeader(code) }
func (h *headerWriter) Write(b []byte) (int, error) {
	h.apply()
	return h.ResponseWriter.Write(b)
}
func (h *headerWriter) Flush()                                       { flush(h.ResponseWriter) }
func (h *headerWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return hijack(h.ResponseWriter) }

func gzipMW() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				next.ServeHTTP(w, r)
				return
			}
			gw := &gzipWriter{ResponseWriter: w}
			defer gw.close()
			next.ServeHTTP(gw, r)
		})
	}
}

// gzipWriter gzips the response, but only for non-streaming, non-upgrade responses; it decides at the
// first WriteHeader/Write (once the Content-Type/Connection headers are set) and forwards Flusher/Hijacker.
type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func (g *gzipWriter) decide(code int) {
	if g.decided {
		return
	}
	g.decided = true
	h := g.Header()
	ct := h.Get("Content-Type")
	streaming := code == http.StatusSwitchingProtocols ||
		strings.EqualFold(h.Get("Connection"), "Upgrade") ||
		strings.HasPrefix(ct, "text/event-stream")
	if streaming || h.Get("Content-Encoding") != "" {
		return // passthrough: never gzip a stream/upgrade or an already-encoded body
	}
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length") // gzipped length is unknown
	g.gz = gzip.NewWriter(g.ResponseWriter)
}

func (g *gzipWriter) WriteHeader(code int) {
	g.decide(code)
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		g.decide(http.StatusOK)
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) close() {
	if g.gz != nil {
		_ = g.gz.Close()
	}
}

func (g *gzipWriter) Flush() {
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	flush(g.ResponseWriter)
}
func (g *gzipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return hijack(g.ResponseWriter) }

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func hijack(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("shape: underlying ResponseWriter does not support hijacking")
}
