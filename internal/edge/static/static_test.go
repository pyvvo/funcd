package static_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/edge/static"
)

// site builds a memory Bucket seeded with a small static site under prefix "bi/" plus a "secret"
// object at the bucket ROOT (outside the prefix) to prove traversal never reaches it. It returns a
// handler whose resolver serves that bucket for (ns=analytics, bucket=reports).
func site(t *testing.T, extra map[string][]byte) *static.Handler {
	t.Helper()
	b, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	put := func(k string, v []byte) { require.NoError(t, b.Put(context.Background(), k, v)) }
	put("bi/index.html", []byte("<!doctype html><title>bi</title>"))
	put("bi/img/logo.png", []byte("\x89PNG\r\n\x1a\nlogo-bytes"))
	put("secret", []byte("TOP-SECRET"))
	for k, v := range extra {
		put(k, v)
	}
	h, err := static.New(static.Deps{Buckets: func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool) {
		if ns == "analytics" && bucket == "reports" {
			return b, true
		}
		return nil, false
	}})
	require.NoError(t, err)
	return h
}

func backend(prefix string, spa bool) *v1.StaticBackend {
	return &v1.StaticBackend{Bucket: "reports", Prefix: prefix, Index: "index.html", SPA: spa}
}

// serve drives the handler once with the given method + remainder + headers. Like the data-plane, it
// passes the decoded r.URL.Path as the remainder.
func serve(h *static.Handler, method, remainder string, back *v1.StaticBackend, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://bi.example.com"+remainder, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.Serve(w, r, "analytics", back, r.URL.Path)
	return w
}

// scenario: serves-index-and-asset
func TestScenarioServesIndexAndAsset(t *testing.T) {
	h := site(t, nil)
	w := serve(h, http.MethodGet, "/", backend("bi/", false), nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "<title>bi</title>")
	require.True(t, strings.HasPrefix(w.Header().Get("Content-Type"), "text/html"), w.Header().Get("Content-Type"))

	w = serve(h, http.MethodGet, "/img/logo.png", backend("bi/", false), nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "image/png", w.Header().Get("Content-Type"))
	require.Contains(t, w.Body.String(), "logo-bytes")
	require.True(t, strings.HasPrefix(w.Header().Get("ETag"), `W/"`), "weak ETag, got %q", w.Header().Get("ETag"))
}

// scenario: etag-conditional-304
func TestScenarioETagConditional304(t *testing.T) {
	h := site(t, nil)
	first := serve(h, http.MethodGet, "/img/logo.png", backend("bi/", false), nil)
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	second := serve(h, http.MethodGet, "/img/logo.png", backend("bi/", false), map[string]string{"If-None-Match": etag})
	require.Equal(t, http.StatusNotModified, second.Code)
	require.Empty(t, second.Body.String(), "304 has an empty body")
}

// scenario: range-request
func TestScenarioRangeRequest(t *testing.T) {
	big := make([]byte, 1000)
	for i := range big {
		big[i] = byte('a' + i%26)
	}
	h := site(t, map[string][]byte{"bi/big.bin": big})
	w := serve(h, http.MethodGet, "/big.bin", backend("bi/", false), map[string]string{"Range": "bytes=0-99"})
	require.Equal(t, http.StatusPartialContent, w.Code)
	require.Equal(t, "bytes 0-99/1000", w.Header().Get("Content-Range"))
	require.Len(t, w.Body.Bytes(), 100)
	require.Equal(t, big[:100], w.Body.Bytes())
}

// scenario: spa-fallback
func TestScenarioSPAFallback(t *testing.T) {
	h := site(t, nil)
	// SPA=true: an unknown client-route path serves the index (200).
	w := serve(h, http.MethodGet, "/dashboard/orders", backend("bi/", true), nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "<title>bi</title>")
	require.Equal(t, "no-cache", w.Header().Get("Cache-Control"), "the SPA fallback serves the index (no-cache)")

	// SPA=false: the same miss is a 404.
	w = serve(h, http.MethodGet, "/dashboard/orders", backend("bi/", false), nil)
	require.Equal(t, http.StatusNotFound, w.Code)
}

// scenario: traversal-rejected
func TestScenarioTraversalRejected(t *testing.T) {
	h := site(t, nil)
	for _, rem := range []string{"/..%2f..%2fsecret", "/../../secret", "/../../x", "/img/../../secret"} {
		w := serve(h, http.MethodGet, rem, backend("bi/", false), nil)
		require.Contains(t, []int{http.StatusNotFound, http.StatusBadRequest}, w.Code, "remainder %q must be rejected", rem)
		require.NotContains(t, w.Body.String(), "TOP-SECRET", "remainder %q escaped the prefix", rem)
	}
	// Even an SPA route must not leak the out-of-prefix secret on a traversal (it 404s, not index).
	w := serve(h, http.MethodGet, "/../../secret", backend("bi/", true), nil)
	require.NotContains(t, w.Body.String(), "TOP-SECRET")
}

// scenario: method-not-allowed
func TestScenarioMethodNotAllowed(t *testing.T) {
	h := site(t, nil)
	w := serve(h, http.MethodPost, "/", backend("bi/", false), nil)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Contains(t, w.Header().Get("Allow"), "GET")

	// HEAD is allowed (headers, no body).
	w = serve(h, http.MethodHead, "/img/logo.png", backend("bi/", false), nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "image/png", w.Header().Get("Content-Type"))
	require.Empty(t, w.Body.String())
}

// cache-control three tiers (M3): no blanket immutable.
func TestCacheControlThreeTiers(t *testing.T) {
	h := site(t, map[string][]byte{
		"bi/favicon.ico":     []byte("ico"),
		"bi/sw.js":           []byte("self.addEventListener"),
		"bi/app.a1b2c3d4.js": []byte("hashed"),
		"bi/data.json":       []byte("{}"),
		"bi/.well-known/x":   []byte("wk"),
	})
	cases := map[string]string{
		"/":                "no-cache",                             // the index doc
		"/favicon.ico":     "no-cache",                             // stable name
		"/sw.js":           "no-cache",                             // service worker
		"/.well-known/x":   "no-cache",                             // .well-known/*
		"/app.a1b2c3d4.js": "public, max-age=31536000, immutable",  // content-hash-shaped
		"/data.json":       "public, max-age=300, must-revalidate", // conservative default
	}
	for rem, want := range cases {
		w := serve(h, http.MethodGet, rem, backend("bi/", false), nil)
		require.Equal(t, http.StatusOK, w.Code, rem)
		require.Equal(t, want, w.Header().Get("Cache-Control"), "Cache-Control for %q", rem)
	}
}

// a missing / cross-namespace bucket is a 404 (tenancy guard, no existence oracle).
func TestUnknownBucketNotFound(t *testing.T) {
	h := site(t, nil)
	r := httptest.NewRequest(http.MethodGet, "http://x/", nil)
	w := httptest.NewRecorder()
	h.Serve(w, r, "analytics", &v1.StaticBackend{Bucket: "nope", Index: "index.html"}, "/")
	require.Equal(t, http.StatusNotFound, w.Code)
}

// pinnedModTime reports a test-chosen ModTime from List, so two writes land deterministically in the
// same wall-clock second.
type pinnedModTime struct {
	blob.Bucket
	modTime time.Time
}

func (p *pinnedModTime) List(ctx context.Context, prefix string) ([]blob.Attributes, error) {
	items, err := p.Bucket.List(ctx, prefix)
	for i := range items {
		items[i].ModTime = p.modTime
	}
	return items, err
}

// A same-length rewrite within the same second must not revalidate as unchanged (a stale 304).
func TestIssue161_SameSecondRedeployIsNotStale304(t *testing.T) {
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	b := &pinnedModTime{Bucket: mem, modTime: time.Unix(1790905216, 100_000_000)}
	h, err := static.New(static.Deps{Buckets: func(v1.NamespaceName, string) (blob.Bucket, bool) { return b, true }})
	require.NoError(t, err)

	require.NoError(t, b.Put(context.Background(), "bi/index.html", []byte("<title>build v1</title>")))
	first := serve(h, http.MethodGet, "/", backend("bi/", false), nil)
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")

	require.NoError(t, b.Put(context.Background(), "bi/index.html", []byte("<title>build v2</title>")))
	b.modTime = b.modTime.Add(300 * time.Millisecond)
	revalidate := serve(h, http.MethodGet, "/", backend("bi/", false), map[string]string{"If-None-Match": etag})
	require.Equal(t, http.StatusOK, revalidate.Code, "changed content revalidated as unchanged (ETag %s)", etag)
	require.Equal(t, "<title>build v2</title>", revalidate.Body.String())
	require.NotEqual(t, etag, revalidate.Header().Get("ETag"))
}
