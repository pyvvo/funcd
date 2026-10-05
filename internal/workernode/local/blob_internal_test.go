package local

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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
