// Package static is the edge static-asset handler (ADR-0120, F82): it serves a Bucket-prefix
// static site directly through the FEAT-0006 edge — index resolution, content-types, a weak
// (ModTime,Size) ETag + conditional GET (304), HTTP Range (206), an optional SPA fallback, and a
// pinned three-tier Cache-Control — with no function code and no activator hop. The bytes are the
// same per-namespace Bucket view the S3 frontend serves (ADR-0080), read over the blob.Bucket port.
//
// Security (ADR-0120 B1): the traversal defense is OWNED here, not inherited from the blob driver.
// It path.Cleans the (already percent-decoded) request remainder, rejects any `..`-containing path,
// and asserts the resolved object key has the declared Prefix BEFORE any blob Get — no read can
// escape the prefix / bucket / namespace.
package static

import (
	"bytes"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
)

const op = "edge.static"

// defaultIndex is the document served for "/" / a directory / (SPA) a miss when Index is unset.
const defaultIndex = "index.html"

// BucketResolver resolves a (namespace, bucket) to its per-namespace blob view (the same
// s3BucketFor the S3 frontend uses, ADR-0080). ok=false ⇒ the Bucket is missing / cross-namespace.
type BucketResolver func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool)

// Deps are the Handler's dependencies.
type Deps struct {
	Buckets BucketResolver
	Logger  *slog.Logger
}

// Handler serves a Bucket-prefix static site over the edge (F82). It resolves it, it does not proxy —
// there is no function to wake.
type Handler struct {
	buckets BucketResolver
	logger  *slog.Logger
}

// New builds the static Handler.
func New(d Deps) (*Handler, error) {
	if d.Buckets == nil {
		return nil, fault.Invalidf("static.New", "bucket resolver is required")
	}
	l := d.Logger
	if l == nil {
		l = slog.Default()
	}
	return &Handler{buckets: d.Buckets, logger: l.With("component", "edge.static")}, nil
}

// hashShaped matches a content-hash-shaped filename segment (Observable/Vite/webpack fingerprints):
// a hex run of ≥8 delimited by "." or "-" before the extension, e.g. app.a1b2c3d4.js / app-a1b2c3d4.js.
var hashShaped = regexp.MustCompile(`[.\-][0-9a-f]{8,}\.`)

// Serve writes the object at <back.Prefix><cleaned-remainder> in back.Bucket. remainder is the
// function-relative path (the matched Route prefix already stripped), always starting "/", and already
// percent-decoded (it is cut from r.URL.Path, which net/http decodes), so it is not decoded again. It owns
// the 405 (non-GET/HEAD), the traversal defense (B1), the weak (ModTime,Size) ETag validator (M1),
// the three-tier Cache-Control (M3), index resolution and the SPA fallback; the stdlib
// http.ServeContent handles Last-Modified / If-Modified-Since / If-Range / If-None-Match → 304 / Range → 206.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, ns v1.NamespaceName, back *v1.StaticBackend, remainder string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	bucket, ok := h.buckets(ns, string(back.Bucket))
	if !ok {
		// Tenancy guard: a missing / cross-namespace Bucket is a 404 (no existence oracle).
		fault.WriteProblem(w, fault.NotFoundf(op, "no static content"))
		return
	}
	index := back.Index
	if index == "" {
		index = defaultIndex
	}

	key, bad := resolveKey(back.Prefix, index, remainder)
	if bad {
		fault.WriteProblem(w, fault.NotFoundf(op, "no such object"))
		return
	}

	// Look up the object's attributes (ModTime, Size) for the weak validator — no full Get needed
	// yet (this also lets a matching If-None-Match short-circuit to 304 without reading the body).
	attrs, found, err := stat(r, bucket, key)
	if err != nil {
		fault.WriteProblem(w, fault.Wrapf(err, fault.KindOf(err), op, "stat %q", key))
		return
	}
	if !found {
		// SPA fallback: an un-matched path serves the Index (200) so a client-side router owns
		// routing; a non-SPA miss is 404. The index key is inside the prefix by construction.
		if !back.SPA {
			fault.WriteProblem(w, fault.NotFoundf(op, "no such object"))
			return
		}
		key = back.Prefix + index
		attrs, found, err = stat(r, bucket, key)
		if err != nil {
			fault.WriteProblem(w, fault.Wrapf(err, fault.KindOf(err), op, "stat %q", key))
			return
		}
		if !found {
			fault.WriteProblem(w, fault.NotFoundf(op, "no such object"))
			return
		}
	}

	etag := weakETag(attrs)
	setContentType(w, key)
	w.Header().Set("Cache-Control", cacheControl(key, back.Prefix, index))
	w.Header().Set("ETag", etag)

	// Conditional GET short-circuit: a matching If-None-Match returns 304 without reading the body
	// (the weak (ModTime,Size) validator needs no body hash — the whole point of M1).
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	data, err := bucket.Get(r.Context(), key)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			fault.WriteProblem(w, fault.NotFoundf(op, "no such object"))
			return
		}
		fault.WriteProblem(w, fault.Wrapf(err, fault.KindOf(err), op, "get %q", key))
		return
	}
	// The stdlib file-server: honors the ETag we set (If-None-Match → 304) plus the real ModTime
	// (Last-Modified / If-Modified-Since / If-Range) and Range (→ 206 Partial Content).
	http.ServeContent(w, r, path.Base(key), attrs.ModTime, bytes.NewReader(data))
}

// resolveKey cleans the decoded remainder, rejects traversal, and computes the object key. bad=true
// on a `..` segment or a key that would escape the prefix (B1). "/" or a directory ("…/") resolves
// to <prefix><dir>index.
func resolveKey(prefix, index, dec string) (key string, bad bool) {
	if dec == "" {
		dec = "/"
	}
	// Reject a `..` segment BEFORE path.Clean collapses it — a stated rejection, not a silent fix.
	if containsDotDot(dec) {
		return "", true
	}
	isDir := strings.HasSuffix(dec, "/")
	// Root the path at "/" so path.Clean can never ascend above it; then take it relative to the prefix.
	cleaned := path.Clean("/" + strings.TrimPrefix(dec, "/"))
	rel := strings.TrimPrefix(cleaned, "/")
	if rel == "" || isDir {
		if rel != "" {
			rel += "/"
		}
		rel += index
	}
	key = prefix + rel
	// Containment is THIS handler's contract, not the blob driver's — assert it before any read.
	if !strings.HasPrefix(key, prefix) {
		return "", true
	}
	return key, false
}

// stat returns the object's attributes via a List keyed by the exact object path (the blob.Bucket
// port is Get/List-only, no per-key Stat — ADR-0007). found=false ⇒ no object at that exact key.
func stat(r *http.Request, b blob.Bucket, key string) (blob.Attributes, bool, error) {
	items, err := b.List(r.Context(), key)
	if err != nil {
		return blob.Attributes{}, false, err
	}
	for i := range items {
		if items[i].Key == key {
			return items[i], true, nil
		}
	}
	return blob.Attributes{}, false, nil
}

// weakETag derives the (ModTime,Size) weak validator (M1) — no per-request sha256 of the body. ModTime
// keeps its full precision (as ADR-0119's fingerprint does): whole seconds would let a same-length
// rewrite within one second keep its ETag and answer a stale 304 (issue #161).
func weakETag(a blob.Attributes) string {
	return `W/"` + strconv.FormatInt(a.Size, 10) + "-" + strconv.FormatInt(a.ModTime.UnixNano(), 10) + `"`
}

// setContentType sets Content-Type from the key extension (mime), leaving it unset for an unknown
// extension so http.ServeContent falls back to http.DetectContentType.
func setContentType(w http.ResponseWriter, key string) {
	if ct := mime.TypeByExtension(path.Ext(key)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
}

// cacheControl is the pinned three-tier policy (M3): no blanket immutable.
//   - Index + a known-stable set (sw.js/service-worker, manifest*, favicon*, robots.txt, .well-known/*)
//     ⇒ no-cache (always revalidated; a 304 keeps it cheap, a deploy is picked up at once).
//   - a content-hash-shaped filename ⇒ public, max-age=31536000, immutable (safe forever — a change mints a new name).
//   - everything else ⇒ public, max-age=300, must-revalidate.
func cacheControl(key, prefix, index string) string {
	base := path.Base(key)
	if base == index || isStableName(key, prefix) {
		return "no-cache"
	}
	if hashShaped.MatchString(base) {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=300, must-revalidate"
}

// isStableName reports the known-stable filenames that keep their name across deploys.
func isStableName(key, prefix string) bool {
	rel := strings.TrimPrefix(key, prefix)
	if strings.HasPrefix(rel, ".well-known/") || strings.Contains(rel, "/.well-known/") {
		return true
	}
	base := path.Base(key)
	switch {
	case base == "sw.js" || strings.Contains(base, "service-worker"):
		return true
	case strings.HasPrefix(base, "manifest"):
		return true
	case strings.HasPrefix(base, "favicon"):
		return true
	case base == "robots.txt":
		return true
	}
	return false
}

// etagMatches implements a weak If-None-Match comparison (RFC 9110 §13.1.2): "*" matches any, else
// any comma-listed tag equals ours after stripping the weak "W/" marker.
func etagMatches(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(ifNoneMatch, ",") {
		if strings.TrimPrefix(strings.TrimSpace(part), "W/") == want {
			return true
		}
	}
	return false
}

// containsDotDot reports whether any path segment is exactly "..", the traversal defense (B1),
// mirroring net/http's own containsDotDot.
func containsDotDot(p string) bool {
	if !strings.Contains(p, "..") {
		return false
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}
