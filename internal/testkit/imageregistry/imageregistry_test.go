package imageregistry_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/testkit/imageregistry"
)

const manifestType = "application/vnd.oci.image.manifest.v1+json"

func archive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}))
		_, err := tw.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return b.Bytes()
}

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	_, err := zw.Write(b)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return out.Bytes()
}

// The registry serves the image of a saved archive, gzipped or not: /v2/, its manifest by digest and as latest, its
// blobs by digest, a HEAD with no body; any other repository, blob or method is refused.
func TestServesTheSavedImage(t *testing.T) {
	t.Parallel()
	layer := []byte("a layer")
	manifest := []byte(`{"schemaVersion":2,"mediaType":"` + manifestType + `","layers":[{"digest":"` + digest.FromBytes(layer).String() + `"}]}`)
	index := []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"` + manifestType + `","digest":"` + digest.FromBytes(manifest).String() + `"}]}`)
	saved := archive(t, map[string][]byte{"index.json": index, "manifest.json": []byte("[]"),
		"blobs/sha256/" + digest.FromBytes(manifest).Encoded(): manifest, "blobs/sha256/" + digest.FromBytes(layer).Encoded(): layer})
	for name, b := range map[string][]byte{"plain": saved, "gzipped": gzipped(t, saved)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			img, err := imageregistry.Read(bytes.NewReader(b))
			require.NoError(t, err)
			require.Equal(t, digest.FromBytes(manifest).String(), img.Digest())
			srv := httptest.NewServer(img.Handler("funcd/img"))
			t.Cleanup(srv.Close)
			get := func(method, path string) (*http.Response, []byte) {
				req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
				require.NoError(t, err)
				resp, err := srv.Client().Do(req)
				require.NoError(t, err)
				defer func() { _ = resp.Body.Close() }()
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				return resp, body
			}
			resp, _ := get(http.MethodGet, "/v2/")
			require.Equal(t, http.StatusOK, resp.StatusCode)
			for _, ref := range []string{img.Digest(), "latest"} {
				resp, body := get(http.MethodGet, "/v2/funcd/img/manifests/"+ref)
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.Equal(t, manifest, body)
				require.Equal(t, manifestType, resp.Header.Get("Content-Type"))
				require.Equal(t, img.Digest(), resp.Header.Get("Docker-Content-Digest"))
			}
			resp, body := get(http.MethodHead, "/v2/funcd/img/blobs/"+digest.FromBytes(layer).String())
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Empty(t, body)
			_, body = get(http.MethodGet, "/v2/funcd/img/blobs/"+digest.FromBytes(layer).String())
			require.Equal(t, layer, body)
			for _, path := range []string{"/v2/other/manifests/latest", "/v2/funcd/img/blobs/" + digest.FromString("none").String()} {
				resp, _ = get(http.MethodGet, path)
				require.Equal(t, http.StatusNotFound, resp.StatusCode, path)
			}
			resp, _ = get(http.MethodPut, "/v2/funcd/img/manifests/latest")
			require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
		})
	}
	_, err := imageregistry.Read(bytes.NewReader(archive(t, map[string][]byte{"manifest.json": []byte("[]")})))
	require.ErrorContains(t, err, "no OCI index.json")
}
