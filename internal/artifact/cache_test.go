package artifact_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/artifact"
)

// layerBlob returns the file that holds the layer of media type mt of the artifact ref@digest in its local layout,
// made writable so a test can corrupt or remove it.
func layerBlob(t *testing.T, ref, digest, mt string) string {
	t.Helper()
	layer, ok := layerByMT(fetchManifest(t, ref, digest).Layers, mt)
	require.True(t, ok)
	dir, _, _ := parseLayoutRef(ref)
	blob := filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(layer.Digest.String(), "sha256:"))
	require.NoError(t, os.Chmod(blob, 0o600))
	return blob
}

func tarGz(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o644}))
	_, err := tw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// A bundle layer whose bytes do not match the pinned digest is refused, and the refused bytes are never served by a
// later call: the digest is the authority (ADR-0031, ADR-0035).
func TestMaterializeRepullsBundleThatFailedItsDigestCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := layoutRef(t, "v1")
	dir, entry := goodBundle(t)
	digest, err := artifact.PushBundle(ctx, ref, dir, entry, "", "")
	require.NoError(t, err)
	blob := layerBlob(t, ref, digest, artifact.BundleTarMediaType)
	require.NoError(t, os.WriteFile(blob, tarGz(t, entry, "CORRUPT = True\n"), 0o600))

	m := artifact.NewOrasMaterializer(t.TempDir(), "")
	for i := range 2 {
		path, merr := m.Materialize(ctx, mkFunction(t, ref, digest))
		got, _ := os.ReadFile(path) //nolint:gosec // materializer-owned temp path
		require.Error(t, merr, "call %d served %q from a digest-mismatched layer", i+1, got)
	}
}

// A bundle pull cut off mid-stream leaves nothing a later call serves: once the source recovers, the next call pulls
// the whole bundle and resolves its entry.
func TestMaterializeRepullsBundleAfterInterruptedPull(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := layoutRef(t, "v1")
	dir, entry := goodBundle(t)
	digest, err := artifact.PushBundle(ctx, ref, dir, entry, "", "")
	require.NoError(t, err)
	blob := layerBlob(t, ref, digest, artifact.BundleTarMediaType)
	orig, err := os.ReadFile(blob) //nolint:gosec // test-owned layout
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(blob, orig[:len(orig)-30], 0o600))

	m := artifact.NewOrasMaterializer(t.TempDir(), "")
	_, err = m.Materialize(ctx, mkFunction(t, ref, digest))
	require.Error(t, err)
	require.NoError(t, os.WriteFile(blob, orig, 0o600))

	path, err := m.Materialize(ctx, mkFunction(t, ref, digest))
	require.NoError(t, err)
	require.Equal(t, entry, filepath.Base(path), "the bundle entry, not a file left by the cut-off pull")
	require.FileExists(t, filepath.Join(filepath.Dir(path), "vendored", "pkg", "__init__.py"))
}

// A single-file pull whose contract could not be delivered leaves nothing a later call serves: the handler is never
// cached without its contract, so a contracted function never reaches a worker un-validated (ADR-0123).
func TestMaterializeRepullsContractAfterFailedDelivery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := layoutRef(t, "v1")
	contract, err := artifact.ContractBlob([]byte(schemaIn), []byte(schemaOut))
	require.NoError(t, err)
	digest, err := artifact.Push(ctx, ref, writeBundle(t, "export function handle() {}\n"), contract, "", "")
	require.NoError(t, err)
	blob := layerBlob(t, ref, digest, "application/vnd.funcd.contract.v1+json")
	require.NoError(t, os.Rename(blob, blob+".away"))

	m := artifact.NewOrasMaterializer(t.TempDir(), "")
	_, err = m.Materialize(ctx, mkFunction(t, ref, digest))
	require.Error(t, err)
	require.NoError(t, os.Rename(blob+".away", blob))

	path, err := m.Materialize(ctx, mkFunction(t, ref, digest))
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(filepath.Dir(path), ".funcd-contract.json"), "the handler is served with its contract")
}

// A cache dir that no completed pull filled, such as one an earlier daemon left behind, is pulled again, not served.
func TestMaterializeRepullsCacheDirNoPullCompleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := layoutRef(t, "v1")
	dir, entry := goodBundle(t)
	digest, err := artifact.PushBundle(ctx, ref, dir, entry, "", "")
	require.NoError(t, err)
	filled, err := artifact.NewOrasMaterializer(t.TempDir(), "").Materialize(ctx, mkFunction(t, ref, digest))
	require.NoError(t, err)

	root := t.TempDir()
	left := filepath.Join(root, filepath.Base(filepath.Dir(filled)))
	require.NoError(t, os.MkdirAll(left, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(left, ".funcd-entry"), []byte(entry), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(left, entry), []byte("CORRUPT = True\n"), 0o600))

	path, err := artifact.NewOrasMaterializer(root, "").Materialize(ctx, mkFunction(t, ref, digest))
	require.NoError(t, err)
	got, err := os.ReadFile(path) //nolint:gosec // materializer-owned temp path
	require.NoError(t, err)
	require.NotContains(t, string(got), "CORRUPT")
	require.FileExists(t, filepath.Join(filepath.Dir(path), "vendored", "pkg", "__init__.py"))
}
