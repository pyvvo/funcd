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

// apply runs the rules on every header block up to the final one: httputil.ReverseProxy clears the
// headers after it relays a 1xx (#417).
func (h *headerWriter) apply(code int) {
	if h.wrote {
		return
	}
	h.wrote = !interim(code)
	for k, v := range h.cfg.Set {
		h.Header().Set(k, v)
	}
	for _, k := range h.cfg.Remove {
		h.Header().Del(k)
	}
}
func (h *headerWriter) WriteHeader(code int) { h.apply(code); h.ResponseWriter.WriteHeader(code) }
func (h *headerWriter) Write(b []byte) (int, error) {
	h.apply(http.StatusOK)
	return h.ResponseWriter.Write(b)
}
func (h *headerWriter) Flush()                                       { flush(h.ResponseWriter) }
func (h *headerWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return hijack(h.ResponseWriter) }

func gzipMW() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gw := &gzipWriter{ResponseWriter: w, accept: acceptsGzip(r.Header.Values("Accept-Encoding"))}
			defer gw.close()
			next.ServeHTTP(gw, r)
		})
	}
}

// acceptsGzip reports whether Accept-Encoding names gzip (or its alias x-gzip) with a q-value above zero:
// q=0 means "not acceptable" (RFC 9110 §12.5.3). A malformed q-value counts as zero, since identity is
// always safe.
func acceptsGzip(values []string) bool {
	accept := false
	for _, v := range values {
		for member := range strings.SplitSeq(v, ",") {
			coding, params, _ := strings.Cut(member, ";")
			if c := strings.ToLower(strings.TrimSpace(coding)); c != "gzip" && c != "x-gzip" {
				continue
			}
			q := 1.0
			for p := range strings.SplitSeq(params, ";") {
				k, val, ok := strings.Cut(p, "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
					continue
				}
				var err error
				if q, err = strconv.ParseFloat(strings.TrimSpace(val), 64); err != nil {
					q = 0
				}
			}
			accept = q > 0
		}
	}
	return accept
}

// gzipWriter gzips the response, but only for non-streaming, non-upgrade responses; it decides at the
// final WriteHeader or the first Write (once the Content-Type/Connection headers are set) and forwards
// Flusher/Hijacker.
type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	accept  bool
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
	// A 206's Content-Range indexes the identity bytes, and a 204/304 has no body to encode.
	unencodable := code == http.StatusPartialContent || code == http.StatusNoContent ||
		code == http.StatusNotModified || h.Get("Content-Range") != ""
	if code == http.StatusNotModified && g.accept && !streaming && h.Get("Content-Encoding") == "" {
		weakenETag(h) // RFC 9110 §15.4.5: a 304 carries the ETag that its 200, the gzip variant, would carry
	}
	if streaming || unencodable || h.Get("Content-Encoding") != "" {
		return // passthrough: never gzip a stream/upgrade, a range/bodyless response, or an already-encoded body
	}
	h.Add("Vary", "Accept-Encoding") // RFC 9110 §12.5.5: both the gzip and the identity variant depend on it
	if !g.accept {
		return
	}
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length") // gzipped length is unknown
	weakenETag(h)
	g.gz = gzip.NewWriter(g.ResponseWriter)
}

// weakenETag marks a strong ETag weak (RFC 9110 §8.8.3): a strong ETag names the identity bytes, so an
// If-Range on the gzip variant must not match it; a weak one still revalidates via If-None-Match.
func weakenETag(h http.Header) {
	if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		h.Set("ETag", "W/"+et)
	}
}

func (g *gzipWriter) WriteHeader(code int) {
	// A 1xx is interim: httputil.ReverseProxy relays it and then clears the headers (#305).
	if !interim(code) {
		g.decide(code)
	}
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

// interim reports whether code is a 1xx that a final status follows (101 ends the exchange).
func interim(code int) bool {
	return code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols
}

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
