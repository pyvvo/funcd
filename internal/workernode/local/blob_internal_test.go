package local

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
)

// zeroReader yields an endless stream of zero bytes — used to stream an over-cap body without
// allocating it up front.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// capFakeBlob is a no-op Blob port; the size-cap check fires in the handler (http.MaxBytesReader) BEFORE
// Put is ever called, so the port body is irrelevant.
type capFakeBlob struct{}

func (capFakeBlob) Get(context.Context, v1.NamespaceName, v1.ObjectName, string, string) ([]byte, bool, error) {
	return nil, false, nil
}
func (capFakeBlob) Put(context.Context, v1.NamespaceName, v1.ObjectName, string, string, []byte) error {
	return nil
}
func (capFakeBlob) Delete(context.Context, v1.NamespaceName, v1.ObjectName, string, string) error {
	return nil
}
func (capFakeBlob) List(context.Context, v1.NamespaceName, v1.ObjectName, string, string) ([]string, error) {
	return nil, nil
}
func (capFakeBlob) SignedURL(context.Context, v1.NamespaceName, v1.ObjectName, string, string, blob.SignOptions) (string, error) {
	return "", nil
}

// scenario: blob-size-cap — a put whose body exceeds maxBlobBytes is 413 (the http.MaxBytesReader
// guard fires in the handler before Put is called). The over-cap body is STREAMED (not allocated).
func TestScenarioBlobSizeCap(t *testing.T) {
	mux := http.NewServeMux()
	registerBlob(mux, Ref{Namespace: "default", Function: "fn"}, capFakeBlob{}, slog.Default())

	over := httptest.NewRequest(http.MethodPut, "/blob/b/obj", io.LimitReader(zeroReader{}, maxBlobBytes+1))
	overRec := httptest.NewRecorder()
	mux.ServeHTTP(overRec, over)
	require.Equal(t, http.StatusRequestEntityTooLarge, overRec.Code, "an over-cap put is 413")

	within := httptest.NewRequest(http.MethodPut, "/blob/b/ok", strings.NewReader("ok"))
	withinRec := httptest.NewRecorder()
	mux.ServeHTTP(withinRec, within)
	require.Equal(t, http.StatusNoContent, withinRec.Code, "a within-cap put succeeds")
}

// The signOptsFromQuery contract (ADR-0198 Decisions 2–3): exact methods, the ADR-0194 grammar, whole seconds
// from 1s to 168h; only an absent parameter takes a default.
func TestADR0198_SignOptsFromQueryContract(t *testing.T) {
	ok := map[string]blob.SignOptions{
		"":                          {Method: blob.SignGet},
		"method=GET":                {Method: blob.SignGet},
		"method=PUT":                {Method: blob.SignPut},
		"method=DELETE":             {Method: blob.SignDelete},
		"expiry=1s":                 {Method: blob.SignGet, Expiry: time.Second},
		"expiry=168h":               {Method: blob.SignGet, Expiry: 168 * time.Hour},
		"expiry=2000ms":             {Method: blob.SignGet, Expiry: 2 * time.Second},
		"method=PUT&expiry=1h30m5s": {Method: blob.SignPut, Expiry: 90*time.Minute + 5*time.Second},
	}
	for raw, want := range ok {
		q, err := url.ParseQuery(raw)
		require.NoError(t, err)
		got, err := signOptsFromQuery(q)
		require.NoError(t, err, raw)
		require.Equal(t, want, got, raw)
	}
	bad := map[string]string{
		"method=":               `method "" is not GET, PUT or DELETE`,
		"method=get":            `method "get" is not GET, PUT or DELETE`,
		"expiry=":               `expiry "" is not a duration string such as 10m or 1h30m`,
		"expiry=10":             `expiry "10" is not a duration string such as 10m or 1h30m`,
		"expiry=999ms":          `expiry "999ms" must be a whole number of seconds from 1s to 168h`,
		"expiry=168h1s":         `expiry "168h1s" must be a whole number of seconds from 1s to 168h`,
		"expiry=1s1ms":          `expiry "1s1ms" must be a whole number of seconds from 1s to 168h`,
		"expiry=0s":             `expiry "0s" must be a whole number of seconds from 1s to 168h`,
		"method=HEAD&expiry=10": `method "HEAD" is not GET, PUT or DELETE`,
	}
	for raw, msg := range bad {
		q, err := url.ParseQuery(raw)
		require.NoError(t, err)
		got, err := signOptsFromQuery(q)
		require.Equal(t, fault.Invalid, fault.KindOf(err), raw)
		require.EqualError(t, err, "workernode.local.blob.sign: "+msg)
		require.Zero(t, got, raw)
	}
}
