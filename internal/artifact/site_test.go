package artifact_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/artifact"
)

// siteDir writes a minimal prebuilt web app (index + a nested asset) into a temp dir.
func siteDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "site")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "img"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>bi</title>"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "img", "logo.png"), []byte("\x89PNG\r\n\x1a\nlogo"), 0o600))
	return dir
}

// scenario: site-artifact-roundtrip (ADR-0139) — PushSite a dir → the manifest is SiteArtifactType with
// exactly one BundleTarMediaType layer, no contract layer, no runtime/contract annotation; ResolveSite
// resolves the tag to that digest; PullSite untars the same tree; and an identical tree yields an
// identical digest (the ADR-0089 determinism PushSite reuses).
func TestScenarioSiteArtifactRoundtrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := siteDir(t)
	ref := layoutRef(t, "bi")

	digest, err := artifact.PushSite(ctx, ref, dir)
	require.NoError(t, err)
	m := fetchManifest(t, ref, digest)
	require.Equal(t, artifact.SiteArtifactType, m.ArtifactType)
	require.Len(t, m.Layers, 1)
	require.Equal(t, artifact.BundleTarMediaType, m.Layers[0].MediaType)
	require.Empty(t, m.Annotations["dev.funcd.contract.v1"], "a site carries no contract annotation")
	require.Empty(t, m.Annotations["dev.funcd.runtime.v1"], "a site carries no runtime annotation")

	resolved, err := artifact.ResolveSite(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, digest, resolved)

	out := filepath.Join(t.TempDir(), "out")
	require.NoError(t, artifact.PullSite(ctx, ref, digest, out))
	index, err := os.ReadFile(filepath.Join(out, "index.html"))
	require.NoError(t, err)
	require.Equal(t, "<!doctype html><title>bi</title>", string(index))
	logo, err := os.ReadFile(filepath.Join(out, "img", "logo.png"))
	require.NoError(t, err)
	require.Equal(t, "\x89PNG\r\n\x1a\nlogo", string(logo))

	again, err := artifact.PushSite(ctx, layoutRef(t, "bi2"), siteDir(t))
	require.NoError(t, err)
	require.Equal(t, digest, again, "an identical tree yields an identical digest")

	require.Equal(t, fault.Invalid, fault.KindOf(artifact.PullSite(ctx, ref, "", out)), "an empty digest is rejected")
	_, rerr := artifact.ResolveSite(ctx, layoutRef(t, "absent"))
	require.Equal(t, fault.NotFound, fault.KindOf(rerr), "an absent tag is NotFound")
	_, perr := artifact.PushSite(ctx, layoutRef(t, "empty"), t.TempDir())
	require.Equal(t, fault.Invalid, fault.KindOf(perr), "an empty site dir is Invalid")
}

// scenario: resolve-rejects-function-artifact (ADR-0139 §10) — a FUNCTION artifact (PushBundle) under the
// same layout is refused by ResolveSite and PullSite with fault.Invalid: the distinct artifactType keeps a
// Site from ever materializing a function bundle.
func TestScenarioResolveSiteRejectsFunctionArtifact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir, entry := goodBundle(t)
	ref := layoutRef(t, "fn")
	digest, err := artifact.PushBundle(ctx, ref, dir, entry, "", "")
	require.NoError(t, err)

	_, rerr := artifact.ResolveSite(ctx, ref)
	require.Equal(t, fault.Invalid, fault.KindOf(rerr))
	require.Contains(t, rerr.Error(), "not a site artifact")
	perr := artifact.PullSite(ctx, ref, digest, filepath.Join(t.TempDir(), "out"))
	require.Equal(t, fault.Invalid, fault.KindOf(perr))
}

// scenario: traversal-safe-unpack (ADR-0139) — a site layer whose tar carries a "../" entry is refused
// by PullSite (fault.Invalid) and nothing is written outside the site dir; an absolute path likewise.
func TestScenarioSiteTraversalSafeUnpack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := layoutRef(t, "evil")
	dir, _, ok := parseLayoutRef(ref)
	require.True(t, ok)
	store, err := oci.New(dir)
	require.NoError(t, err)

	for _, name := range []string{"../escape.txt", "/abs.txt"} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		body := []byte("pwned\n")
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o644}))
		_, err = tw.Write(body)
		require.NoError(t, err)
		require.NoError(t, tw.Close())
		require.NoError(t, gz.Close())
		layer := content.NewDescriptorFromBytes(artifact.BundleTarMediaType, buf.Bytes())
		require.NoError(t, store.Push(ctx, layer, bytes.NewReader(buf.Bytes())))
		manifest, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, artifact.SiteArtifactType,
			oras.PackManifestOptions{Layers: []ocispec.Descriptor{layer}})
		require.NoError(t, err)

		out := filepath.Join(t.TempDir(), "out")
		perr := artifact.PullSite(ctx, ref, manifest.Digest.String(), out)
		require.Equal(t, fault.Invalid, fault.KindOf(perr), "traversal entry %q → fault.Invalid, fail closed", name)
		require.NoFileExists(t, filepath.Join(filepath.Dir(out), "escape.txt"))
		require.NoFileExists(t, "/abs.txt")
	}
}

// scenario: pack-bundle-still-gates-entry (ADR-0139 m2) — the packer refactor is behaviour-preserving:
// PackBundle still rejects a missing handler entry while PushSite needs none.
func TestScenarioPackBundleStillGatesEntry(t *testing.T) {
	t.Parallel()
	_, err := artifact.PackBundle(siteDir(t), "handler.py")
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a function bundle without its entry is Invalid")
}

// Issue 362: the packer's walk does not descend into a symlinked root, so a site pushed from a symlink
// to its directory shipped an empty layer and a bundle was refused with a misleading entry error. The
// root link is followed: both pack the directory it points to, byte-identical to packing it directly.
func TestIssue362_SymlinkedRootPacksTarget(t *testing.T) {
	t.Parallel()

	t.Run("site", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		real := siteDir(t)
		link := filepath.Join(t.TempDir(), "dist")
		require.NoError(t, os.Symlink(real, link))

		ref := layoutRef(t, "site")
		digest, err := artifact.PushSite(ctx, ref, link)
		require.NoError(t, err)
		out := filepath.Join(t.TempDir(), "out")
		require.NoError(t, artifact.PullSite(ctx, ref, digest, out))
		index, err := os.ReadFile(filepath.Join(out, "index.html"))
		require.NoError(t, err, "the pulled site holds the files the link points to")
		require.Equal(t, "<!doctype html><title>bi</title>", string(index))
		require.FileExists(t, filepath.Join(out, "img", "logo.png"))

		direct, err := artifact.PushSite(ctx, layoutRef(t, "direct"), real)
		require.NoError(t, err)
		require.Equal(t, direct, digest, "a symlinked root packs the same tree as its target")
	})

	t.Run("bundle", func(t *testing.T) {
		t.Parallel()
		real, entry := goodBundle(t)
		link := filepath.Join(t.TempDir(), "bundle")
		require.NoError(t, os.Symlink(real, link))

		viaLink, err := artifact.PackBundle(link, entry)
		require.NoError(t, err)
		direct, err := artifact.PackBundle(real, entry)
		require.NoError(t, err)
		require.Equal(t, direct, viaLink, "a symlinked bundle root packs the same tree as its target")
	})
}
