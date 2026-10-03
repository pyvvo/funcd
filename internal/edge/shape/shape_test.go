package shape_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/edge/shape"
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

// A 206 carries a Content-Range over the identity bytes and a 204/304 has no body, so none is gzipped.
func TestIssue162_CompressionSkipsPartialAndBodylessResponses(t *testing.T) {
	content := strings.Repeat("0123456789", 100)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nocontent" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "a.txt", time.Time{}, strings.NewReader(content))
	})
	get := func(path string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "http://x"+path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		return serve(shape.Chain(shape.Config{Compression: true}), next, req)
	}

	rec := get("/range", map[string]string{"Range": "bytes=100-199"})
	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Empty(t, rec.Header().Get("Content-Encoding"), "a 206 is never gzipped")
	require.Equal(t, "bytes 100-199/1000", rec.Header().Get("Content-Range"))
	require.Equal(t, "100", rec.Header().Get("Content-Length"))
	require.Equal(t, content[100:200], rec.Body.String(), "the body is the requested identity bytes")

	rec = get("/nocontent", nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Empty(t, rec.Header().Get("Content-Encoding"), "a 204 is never gzipped")

	rec = get("/notmod", map[string]string{"If-None-Match": `"v1"`})
	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Empty(t, rec.Header().Get("Content-Encoding"), "a 304 is never gzipped")
}

// httputil.ReverseProxy relays an upstream 1xx through the writer and then clears the headers, so the gzip
// choice must wait for the final status and headers.
func TestIssue305_CompressionDecidesAtFinalStatusAfter1xx(t *testing.T) {
	payload := strings.Repeat("compress me ", 500)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = io.Copy(io.Discard, r.Body) // the first body read answers Expect with 100 Continue
		} else {
			w.Header().Set("Link", "</a.css>; rel=preload")
			w.WriteHeader(http.StatusEarlyHints)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, payload)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	edge := httptest.NewServer(shape.Chain(shape.Config{Compression: true})(httputil.NewSingleHostReverseProxy(target)))
	defer edge.Close()
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	defer client.CloseIdleConnections()

	for _, tc := range []struct {
		name    string
		method  string
		body    io.Reader
		interim int
	}{
		{name: "early-hints", method: http.MethodGet, interim: http.StatusEarlyHints},
		{name: "expect-continue", method: http.MethodPost, body: bytes.NewReader(make([]byte, 4096)), interim: http.StatusContinue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var codes []int
			var encodings []string
			trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, h textproto.MIMEHeader) error {
				codes = append(codes, code)
				encodings = append(encodings, h.Get("Content-Encoding"))
				return nil
			}}
			req, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), tc.method, edge.URL, tc.body)
			require.NoError(t, err)
			req.Header.Set("Accept-Encoding", "gzip")
			if tc.body != nil {
				req.Header.Set("Expect", "100-continue")
			}
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Contains(t, codes, tc.interim, "the upstream 1xx is relayed")
			for _, enc := range encodings {
				require.Empty(t, enc, "a 1xx never carries the gzip choice")
			}
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"), "the final response declares its gzip body")
			gz, err := gzip.NewReader(resp.Body)
			require.NoError(t, err)
			got, err := io.ReadAll(gz)
			require.NoError(t, err)
			require.Equal(t, payload, string(got), "the body decodes to the upstream payload")
		})
	}
}

// Whether a compressible response is gzipped depends on Accept-Encoding, so both variants carry Vary.
func TestIssue336_CompressibleResponseVariesOnAcceptEncoding(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "body")
	})
	mw := shape.Chain(shape.Config{Compression: true})

	req := httptest.NewRequest("GET", "http://x/y", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(mw, next, req)
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	require.Contains(t, rec.Header().Values("Vary"), "Accept-Encoding", "the gzip variant varies on Accept-Encoding")

	rec = serve(mw, next, httptest.NewRequest("GET", "http://x/y", nil))
	require.Empty(t, rec.Header().Get("Content-Encoding"))
	require.Equal(t, "body", rec.Body.String())
	require.Contains(t, rec.Header().Values("Vary"), "Accept-Encoding", "the identity variant varies on Accept-Encoding")
}

// The shaping wrappers forward http.Flusher + http.Hijacker (SSE/WS must not break).
func TestShapingForwardsFlusherAndHijacker(t *testing.T) {
	// Headers + compression both wrap the writer — both must forward the streaming interfaces.
	cfg := shape.Config{Headers: &shape.Headers{Set: map[string]string{"X-A": "b"}}, Compression: true}
	var flushed, hijackable bool
	done := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(done) // happens-before the test's reads — f.Flush() unblocks the client before the
		// handler finishes, so the flags must be published via `done`, not read racily after http.Get.
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
	<-done // wait for the handler to finish writing the flags before reading them
	require.True(t, flushed, "the shaping wrapper forwards http.Flusher")
	require.True(t, hijackable, "the shaping wrapper forwards http.Hijacker")
}

// RFC 9110 §12.5.3: a coding with q=0 is not acceptable, so the client gets the identity body.
func TestIssue335_CompressionHonorsQZero(t *testing.T) {
	payload := strings.Repeat("compress me ", 500)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, payload)
	})
	for ae, gzipped := range map[string]bool{
		"gzip;q=0, identity":       false,
		"GZIP; Q=0.000":            false,
		"x-gzip;q=0, *;q=0.1":      false,
		"identity;q=1, gzip;q=0.5": true,
		"deflate, x-gzip":          true,
	} {
		req := httptest.NewRequest("GET", "http://x/y", nil)
		req.Header.Set("Accept-Encoding", ae)
		rec := serve(shape.Chain(shape.Config{Compression: true}), next, req)
		if !gzipped {
			require.Empty(t, rec.Header().Get("Content-Encoding"), "Accept-Encoding %q refuses gzip", ae)
			require.Equal(t, payload, rec.Body.String(), "Accept-Encoding %q gets the identity body", ae)
			continue
		}
		require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), "Accept-Encoding %q accepts gzip", ae)
		gz, err := gzip.NewReader(rec.Body)
		require.NoError(t, err)
		got, err := io.ReadAll(gz)
		require.NoError(t, err)
		require.Equal(t, payload, string(got))
	}
}

// RFC 9110 §8.8: the gzip and identity variants must not share a strong ETag, or an If-Range resume of a
// gzip download matches the upstream and gets identity bytes spliced into the partial gzip body.
func TestIssue439_GzipVariantWeakensStrongETag(t *testing.T) {
	content := strings.Repeat("0123456789", 100)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", r.URL.Query().Get("etag"))
		http.ServeContent(w, r, "a.txt", time.Time{}, strings.NewReader(content))
	})
	get := func(etag string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "http://x/a?etag="+url.QueryEscape(etag), nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		return serve(shape.Chain(shape.Config{Compression: true}), next, req)
	}
	gunzip := func(rec *httptest.ResponseRecorder) string {
		gz, err := gzip.NewReader(rec.Body)
		require.NoError(t, err)
		got, err := io.ReadAll(gz)
		require.NoError(t, err)
		return string(got)
	}

	rec := get(`"v1"`, map[string]string{"Accept-Encoding": "gzip"})
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	gzipETag := rec.Header().Get("ETag")
	require.Equal(t, `W/"v1"`, gzipETag, "the gzip variant does not reuse the identity's strong ETag")

	rec = get(`"v1"`, map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=20-", "If-Range": gzipETag})
	require.Equal(t, http.StatusOK, rec.Code, "an If-Range resume of the gzip variant never gets identity bytes")
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	require.Equal(t, content, gunzip(rec), "the full representation is resent")

	rec = get(`"v1"`, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": gzipETag})
	require.Equal(t, http.StatusNotModified, rec.Code, "the gzip variant still revalidates")
	require.Equal(t, gzipETag, rec.Header().Get("ETag"), "the 304 carries the ETag of the gzip 200 it revalidates")

	rec = get(`"v1"`, map[string]string{"If-None-Match": `"v1"`})
	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Equal(t, `"v1"`, rec.Header().Get("ETag"), "the identity variant's 304 keeps its strong ETag")

	rec = get(`"v1"`, nil)
	require.Equal(t, `"v1"`, rec.Header().Get("ETag"), "the identity variant keeps its strong ETag")
	require.Equal(t, content, rec.Body.String())

	rec = get(`W/"v1"`, map[string]string{"Accept-Encoding": "gzip"})
	require.Equal(t, `W/"v1"`, rec.Header().Get("ETag"), "an already weak ETag is kept")
	require.Equal(t, content, gunzip(rec))
}
