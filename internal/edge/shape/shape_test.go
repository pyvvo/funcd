package shape_test

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/internal/edge/shape"
)

func serve(mw func(http.Handler) http.Handler, next http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, r)
	return rec
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("body")) })
}

// scenario: disabled-passthrough
func TestScenarioDisabledPassthrough(t *testing.T) {
	rec := serve(shape.Chain(shape.Config{}), okHandler(), httptest.NewRequest("GET", "http://x/y", nil))
	require.Equal(t, "body", rec.Body.String())
	require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
}

// scenario: cors-preflight
func TestScenarioCorsPreflight(t *testing.T) {
	mw := shape.Chain(shape.Config{CORS: &shape.CORS{
		AllowOrigins: []string{"https://app.example"}, AllowMethods: []string{"GET", "POST"},
		AllowHeaders: []string{"Authorization"}, MaxAgeSeconds: 600,
	}})
	req := httptest.NewRequest(http.MethodOptions, "http://x/y", nil)
	req.Header.Set("Origin", "https://app.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	reached := false
	rec := serve(mw, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }), req)
	require.Equal(t, http.StatusNoContent, rec.Code, "preflight is answered at the edge")
	require.False(t, reached, "preflight short-circuits — the upstream is never woken")
	require.Equal(t, "https://app.example", rec.Header().Get("Access-Control-Allow-Origin"))
	require.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), "POST")
	require.Equal(t, "Authorization", rec.Header().Get("Access-Control-Allow-Headers"))
	require.Equal(t, "600", rec.Header().Get("Access-Control-Max-Age"))
}

// scenario: cors-actual-request
func TestScenarioCorsActualRequest(t *testing.T) {
	mw := shape.Chain(shape.Config{CORS: &shape.CORS{AllowOrigins: []string{"*"}}})
	req := httptest.NewRequest("GET", "http://x/y", nil)
	req.Header.Set("Origin", "https://any.example")
	rec := serve(mw, okHandler(), req)
	require.Equal(t, "https://any.example", rec.Header().Get("Access-Control-Allow-Origin"), "wildcard echoes the origin")
	require.Contains(t, rec.Header().Values("Vary"), "Origin")
	require.Equal(t, "body", rec.Body.String(), "a real request still reaches the upstream")
}

// scenario: response-headers-set-strip
func TestScenarioResponseHeadersSetStrip(t *testing.T) {
	mw := shape.Chain(shape.Config{Headers: &shape.Headers{
		Set: map[string]string{"X-Frame-Options": "DENY"}, Remove: []string{"Server"},
	}})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "leaky/1.0")
		w.WriteHeader(http.StatusOK)
	})
	rec := serve(mw, next, httptest.NewRequest("GET", "http://x/y", nil))
	require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"), "configured header is set")
	require.Empty(t, rec.Header().Get("Server"), "configured header is stripped")
}

// scenario: compression-gzip
func TestScenarioCompressionGzip(t *testing.T) {
	payload := strings.Repeat("compress me ", 500)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, payload)
	})
	req := httptest.NewRequest("GET", "http://x/y", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(shape.Chain(shape.Config{Compression: true}), next, req)
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	require.Empty(t, rec.Header().Get("Content-Length"), "gzipped length is unknown")
	gz, err := gzip.NewReader(rec.Body)
	require.NoError(t, err)
	got, err := io.ReadAll(gz)
	require.NoError(t, err)
	require.Equal(t, payload, string(got), "the gzipped body decompresses to the original")
}

// scenario: compression-skips-streaming
func TestScenarioCompressionSkipsStreaming(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: hello\n\n")
	})
	req := httptest.NewRequest("GET", "http://x/y", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(shape.Chain(shape.Config{Compression: true}), next, req)
	require.Empty(t, rec.Header().Get("Content-Encoding"), "an SSE stream is never gzipped")
	require.Equal(t, "data: hello\n\n", rec.Body.String(), "the stream bytes pass through untouched")
}

// The shaping wrappers forward http.Flusher + http.Hijacker (SSE/WS must not break).
func TestShapingForwardsFlusherAndHijacker(t *testing.T) {
	// Headers + compression both wrap the writer — both must forward the streaming interfaces.
	cfg := shape.Config{Headers: &shape.Headers{Set: map[string]string{"X-A": "b"}}, Compression: true}
	var flushed, hijackable bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
			flushed = true
		}
		_, hijackable = w.(http.Hijacker)
	})
	srv := httptest.NewServer(shape.Chain(cfg)(next))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.True(t, flushed, "the shaping wrapper forwards http.Flusher")
	require.True(t, hijackable, "the shaping wrapper forwards http.Hijacker")
}
