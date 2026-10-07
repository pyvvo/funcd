package local_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	iblob "github.com/pyvvo/funcd/internal/blob"
	blobsvc "github.com/pyvvo/funcd/internal/services/blob"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// blobMapBucket is a tiny in-memory blob.Bucket for the local-API blob route tests (supports SignedURL).
// It embeds the port only to satisfy ListAfter, which these routes never call.
type blobMapBucket struct {
	iblob.Bucket
	m map[string][]byte
}

func newBlobMapBucket() *blobMapBucket { return &blobMapBucket{m: map[string][]byte{}} }

func (b *blobMapBucket) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := b.m[key]
	if !ok {
		return nil, fault.NotFoundf("blobMapBucket.Get", "key %q", key)
	}
	return v, nil
}
func (b *blobMapBucket) Put(_ context.Context, key string, data []byte, _ iblob.PutOptions) error {
	b.m[key] = data
	return nil
}
func (b *blobMapBucket) Delete(_ context.Context, key string) error { delete(b.m, key); return nil }
func (b *blobMapBucket) Exists(_ context.Context, key string) (bool, error) {
	_, ok := b.m[key]
	return ok, nil
}
func (b *blobMapBucket) List(_ context.Context, prefix string) ([]iblob.Attributes, error) {
	var out []iblob.Attributes
	for k, v := range b.m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, iblob.Attributes{Key: k, Size: int64(len(v))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func (b *blobMapBucket) Attributes(_ context.Context, key string) (iblob.Attributes, error) {
	v, ok := b.m[key]
	if !ok {
		return iblob.Attributes{}, fault.NotFoundf("blobMapBucket.Attributes", "key %q", key)
	}
	return iblob.Attributes{Key: key, Size: int64(len(v))}, nil
}
func (b *blobMapBucket) SignedURL(_ context.Context, key string, opts iblob.SignOptions) (string, error) {
	return "https://signed.example/" + key + "?method=" + string(opts.Method), nil
}
func (b *blobMapBucket) Close() error { return nil }

// s3TestPDP is a test PDP: s3::read allowed iff readOK, s3::write iff writeOK.
type s3TestPDP struct{ readOK, writeOK bool }

func (p s3TestPDP) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	switch req.Action {
	case auth.ActionS3Read:
		return auth.Decision{Allowed: p.readOK}, nil
	case auth.ActionS3Write:
		return auth.Decision{Allowed: p.writeOK}, nil
	default:
		return auth.Decision{Allowed: false}, nil
	}
}

// fakeBlobResolver binds caller "fn" via alias "b" → bucket "bkt", prefix "p"; else default-deny.
type fakeBlobResolver struct{}

func (fakeBlobResolver) Resolve(_ context.Context, _ v1.NamespaceName, fn v1.ObjectName, alias string) (blobsvc.Binding, error) {
	if fn == "fn" && alias == "b" {
		return blobsvc.Binding{Bucket: "bkt", Prefix: "p"}, nil
	}
	return blobsvc.Binding{}, fault.Forbiddenf("fakeBlobResolver", "no blob binding for %s/%s", fn, alias)
}

// blobHandler builds a worker-node local API handler for caller ns/"fn", backed by the real blob Facade
// over a fresh map bucket + the given resolver + PDP.
func blobHandler(t *testing.T, ns v1.NamespaceName, pdp auth.Authorizer, bkt iblob.Bucket) http.Handler {
	t.Helper()
	f, err := blobsvc.NewFacade(blobsvc.FacadeDeps{
		Resolver: fakeBlobResolver{},
		BucketFor: func(_ v1.NamespaceName, name string) (iblob.Bucket, bool) {
			if name == "bkt" {
				return bkt, true
			}
			return nil, false
		},
		Authorizer: pdp,
	})
	require.NoError(t, err)
	return local.NewHandler(local.Ref{Namespace: ns, Function: "fn"}, nil, nil, nil, nil, f, nil)
}

// scenario: blob-read-write (local API) — a bound function put/gets its prefix over context.blob, no keypair.
func TestScenarioBlobReadWrite(t *testing.T) {
	h := blobHandler(t, "default", s3TestPDP{readOK: true, writeOK: true}, newBlobMapBucket())

	require.Equal(t, http.StatusNoContent, do(t, h, http.MethodPut, "/blob/b/report.txt", "hello").Code)
	rec := do(t, h, http.MethodGet, "/blob/b/report.txt", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hello", rec.Body.String())
	require.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))

	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/blob/b/absent", "").Code)
	require.Equal(t, http.StatusNoContent, do(t, h, http.MethodDelete, "/blob/b/report.txt", "").Code)
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/blob/b/report.txt", "").Code)
}

// scenario: blob-list (local API) — list returns exactly the binding's keys under the prefix, stripped.
func TestScenarioBlobList(t *testing.T) {
	h := blobHandler(t, "default", s3TestPDP{readOK: true, writeOK: true}, newBlobMapBucket())
	for _, k := range []string{"bronze/a", "bronze/b", "silver/x"} {
		require.Equal(t, http.StatusNoContent, do(t, h, http.MethodPut, "/blob/b/"+k, "v").Code)
	}
	require.JSONEq(t, `["bronze/a","bronze/b"]`, do(t, h, http.MethodGet, "/blob/b?prefix=bronze/", "").Body.String())
}

// scenario: blob-signed-url (local API) — ?sign=1 returns a presigned URL; ?method=PUT signs a write.
func TestScenarioBlobSignedURL(t *testing.T) {
	h := blobHandler(t, "default", s3TestPDP{readOK: true, writeOK: true}, newBlobMapBucket())

	rec := do(t, h, http.MethodGet, "/blob/b/report.txt?sign=1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/plain")
	require.Contains(t, rec.Body.String(), "p/report.txt", "signed URL keyed at the shared keyspace")

	recPut := do(t, h, http.MethodGet, "/blob/b/report.txt?sign=1&method=PUT", "")
	require.Equal(t, http.StatusOK, recPut.Code)
	require.Contains(t, recPut.Body.String(), "method=PUT")
}

// scenario: blob-unbound-forbidden (local API) — an alias with no binding returns 403 (default-deny).
func TestScenarioBlobUnboundForbidden(t *testing.T) {
	h := blobHandler(t, "default", s3TestPDP{readOK: true, writeOK: true}, newBlobMapBucket())
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodGet, "/blob/nope/k", "").Code)
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodPut, "/blob/nope/k", "v").Code)
}

// scenario: blob-authz-denied (local API) — a bound caller with no read Policy is 403 on get; a put or a
// PUT-sign needs s3::write (no read→write escalation), a GET-sign needs only s3::read.
func TestScenarioBlobAuthzDenied(t *testing.T) {
	hDeny := blobHandler(t, "default", s3TestPDP{readOK: false, writeOK: false}, newBlobMapBucket())
	require.Equal(t, http.StatusForbidden, do(t, hDeny, http.MethodGet, "/blob/b/k", "").Code)

	hRead := blobHandler(t, "default", s3TestPDP{readOK: true, writeOK: false}, newBlobMapBucket())
	require.Equal(t, http.StatusForbidden, do(t, hRead, http.MethodPut, "/blob/b/k", "v").Code, "put needs s3::write")
	require.Equal(t, http.StatusForbidden, do(t, hRead, http.MethodGet, "/blob/b/k?sign=1&method=PUT", "").Code, "a PUT-sign needs s3::write")
	require.Equal(t, http.StatusOK, do(t, hRead, http.MethodGet, "/blob/b/k?sign=1", "").Code, "a GET-sign needs only s3::read")
}

// scenario: blob-over-bucket-object-cap — a context.blob.put of 17 bytes into a Bucket with maxObjectBytes 16
// answers 413 naming the cap and stores nothing (ADR-0148).
func TestScenarioBlobOverBucketObjectCap(t *testing.T) {
	inner := newBlobMapBucket()
	h := blobHandler(t, "default", s3TestPDP{readOK: true, writeOK: true}, iblob.Capped(inner, 16))

	requireProblem(t, do(t, h, http.MethodPut, "/blob/b/big.bin", strings.Repeat("x", 17)), http.StatusRequestEntityTooLarge, payloadTooLarge, "maxObjectBytes (16)")
	require.Empty(t, inner.m, "nothing is stored")
	require.Equal(t, http.StatusNoContent, do(t, h, http.MethodPut, "/blob/b/ok.bin", strings.Repeat("x", 16)).Code)
}

const problemInvalid = "urn:funcd:problem:invalid"

// signRecBucket records the SignOptions of every SignedURL call it gets (ADR-0198).
type signRecBucket struct {
	*blobMapBucket
	calls []iblob.SignOptions
}

func (b *signRecBucket) SignedURL(ctx context.Context, key string, opts iblob.SignOptions) (string, error) {
	b.calls = append(b.calls, opts)
	return b.blobMapBucket.SignedURL(ctx, key, opts)
}

func signHandler(t *testing.T) (http.Handler, *signRecBucket) {
	t.Helper()
	bkt := &signRecBucket{blobMapBucket: newBlobMapBucket()}
	return blobHandler(t, "default", s3TestPDP{readOK: true, writeOK: true}, bkt), bkt
}

// scenario: valid-expiry-honoured — ?sign=1&method=PUT&expiry=10m (or 1h30m) is 200 and signs a PUT for exactly
// that lifetime.
func TestScenarioValidExpiryHonoured(t *testing.T) {
	for q, want := range map[string]time.Duration{"10m": 10 * time.Minute, "1h30m": 90 * time.Minute} {
		h, bkt := signHandler(t)
		rec := do(t, h, http.MethodGet, "/blob/b/k?sign=1&method=PUT&expiry="+q, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, []iblob.SignOptions{{Method: iblob.SignPut, Expiry: want}}, bkt.calls, q)
	}
}

// scenario: bad-expiry-refused — an expiry outside the grammar or the whole-second 1s–168h bounds is 400 invalid
// naming the value; nothing is signed.
func TestScenarioBadExpiryRefused(t *testing.T) {
	for _, e := range []string{"10", "abc", "1e-05s", "1.5s", "500us", "-5m", "", "0s", "500ms", "1s500ms", "168h1s"} {
		h, bkt := signHandler(t)
		rec := do(t, h, http.MethodGet, "/blob/b/k?sign=1&expiry="+url.QueryEscape(e), "")
		requireProblem(t, rec, http.StatusBadRequest, problemInvalid, fmt.Sprintf("workernode.local.blob.sign: expiry %q ", e))
		require.Empty(t, bkt.calls, "expiry %q signs nothing", e)
	}
}

// scenario: unknown-method-refused — a method other than exactly GET, PUT or DELETE is 400 naming it; nothing is
// signed.
func TestScenarioUnknownMethodRefused(t *testing.T) {
	for _, m := range []string{"post", "POST", "put", "HEAD", ""} {
		h, bkt := signHandler(t)
		rec := do(t, h, http.MethodGet, "/blob/b/k?sign=1&method="+url.QueryEscape(m), "")
		requireProblem(t, rec, http.StatusBadRequest, problemInvalid,
			fmt.Sprintf("workernode.local.blob.sign: method %q is not GET, PUT or DELETE", m))
		require.Empty(t, bkt.calls, "method %q signs nothing", m)
	}
}

// scenario: absent-expiry-defaults — neither expiry nor method signs a GET with a zero Expiry (the driver default).
func TestScenarioAbsentExpiryDefaults(t *testing.T) {
	h, bkt := signHandler(t)
	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "/blob/b/k?sign=1", "").Code)
	require.Equal(t, []iblob.SignOptions{{Method: iblob.SignGet}}, bkt.calls)
}

// The checks run before the PDP and the driver (ADR-0198 Decision 1): an unbound alias with a good expiry is 403,
// with a bad one 400; the method is checked before the expiry.
func TestADR0198_SignCheckOrder(t *testing.T) {
	h, bkt := signHandler(t)
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodGet, "/blob/nope/k?sign=1&expiry=10m", "").Code)
	requireProblem(t, do(t, h, http.MethodGet, "/blob/nope/k?sign=1&expiry=10", ""), http.StatusBadRequest, problemInvalid, `expiry "10"`)
	requireProblem(t, do(t, h, http.MethodGet, "/blob/b/k?sign=1&method=post&expiry=10", ""), http.StatusBadRequest, problemInvalid, `method "post"`)
	require.Empty(t, bkt.calls)
}

// scenario: typescript-shim-takes-duration-string — the queries funcd-typescript's signedUrl builds: no expiry
// takes the default, "10m" signs 10 minutes, "1.5s" and "" (sent since expiry != null) are 400.
func TestScenarioTypescriptShimTakesDurationString(t *testing.T) {
	h, bkt := signHandler(t)
	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "/blob/b/k?sign=1&method=GET", "").Code)
	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "/blob/b/k?sign=1&method=GET&expiry=10m", "").Code)
	require.Equal(t, []iblob.SignOptions{{Method: iblob.SignGet}, {Method: iblob.SignGet, Expiry: 10 * time.Minute}}, bkt.calls)
	for _, e := range []string{"1.5s", ""} {
		requireProblem(t, do(t, h, http.MethodGet, "/blob/b/k?sign=1&method=GET&expiry="+e, ""), http.StatusBadRequest, problemInvalid, fmt.Sprintf("expiry %q", e))
	}
	require.Len(t, bkt.calls, 2)
}

// scenario: python-shim-takes-duration-string — the queries funcd-python's signed_url builds (its own suite covers
// the TypeError): "10m" signs, "" is 400, and the old shim's float seconds ("60.0s", "0.5s") are 400.
func TestScenarioPythonShimTakesDurationString(t *testing.T) {
	h, bkt := signHandler(t)
	rec := do(t, h, http.MethodGet, "/blob/b/k?sign=1&method=PUT&expiry=10m", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "method=PUT")
	for _, e := range []string{"", "60.0s", "0.5s"} {
		requireProblem(t, do(t, h, http.MethodGet, "/blob/b/k?sign=1&method=GET&expiry="+e, ""), http.StatusBadRequest, problemInvalid, fmt.Sprintf("expiry %q", e))
	}
	require.Equal(t, []iblob.SignOptions{{Method: iblob.SignPut, Expiry: 10 * time.Minute}}, bkt.calls)
}
