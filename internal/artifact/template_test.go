package artifact_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/artifact"
)

// templateDir writes a small App template: app.yaml, app.lock and one resource file.
func templateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "resources"), 0o755))
	for name, body := range map[string]string{
		"app.yaml":           "name: todo\nversion: 1.2.0\nregistry: registry.example\nimages:\n  api: todo-api:1.0.0\n",
		"app.lock":           "images:\n  api:\n    digest: sha256:" + strings.Repeat("4f1c", 16) + "\n    requested: todo-api:1.0.0\n    version: 1.0.0\n",
		"resources/api.yaml": "functions:\n  - name: todo-api\n    image: ${{ images.api }}\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

// layoutRepo is an oci-layout:// repository in a new directory, without a tag.
func layoutRepo(t *testing.T) string {
	t.Helper()
	return "oci-layout://" + filepath.Join(t.TempDir(), "todo-app")
}

// tree reads every regular file under dir, keyed by its slash path.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p) //nolint:gosec // a file of the test's own tree
		files[filepath.ToSlash(rel)] = string(data)
		return err
	}))
	return files
}

// ADR-0218 Decisions 6 and 7: a template round-trips through a layout byte for byte, under its own artifact type, in
// one reproducible layer; ListTags and ResolveDigest read the layout.
func TestTemplateArtifactRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := templateDir(t)
	repo := layoutRepo(t)
	ref := repo + ":1.2.0"

	digest, err := artifact.PushTemplate(ctx, ref, dir, "1.2.0")
	require.NoError(t, err)
	m := fetchManifest(t, ref, digest)
	require.Equal(t, artifact.AppTemplateArtifactType, m.ArtifactType)
	require.Len(t, m.Layers, 1)
	require.Equal(t, artifact.BundleTarMediaType, m.Layers[0].MediaType)

	resolved, err := artifact.ResolveTemplate(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, digest, resolved)
	out := filepath.Join(t.TempDir(), "pulled")
	require.NoError(t, artifact.PullTemplate(ctx, ref, digest, out))
	require.Equal(t, tree(t, dir), tree(t, out), "the pulled files equal the source")

	again, err := artifact.PushTemplate(ctx, layoutRepo(t)+":1.2.0", templateDir(t), "1.2.0")
	require.NoError(t, err)
	require.Equal(t, digest, again, "the same directory gives one digest")

	tags, err := artifact.ListTags(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, []string{"1.2.0"}, tags)
	at, err := artifact.ResolveDigest(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, digest, at)
	_, err = artifact.ListTags(ctx, layoutRepo(t))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "no layout")
	_, err = artifact.ResolveDigest(ctx, repo+":9.9.9")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "no such tag")

	tl, err := artifact.TagResolver{}.Tags(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, tags, tl)
	td, err := artifact.TagResolver{}.Digest(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, digest, td)
}

// ADR-0218 Decision 6: a ref without a tag, with a digest or whose tag is not the version is refused before any write.
func TestPushTemplateRefusesTag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := templateDir(t)
	base := filepath.Join(t.TempDir(), "todo-app")
	digest := "sha256:" + strings.Repeat("a", 64)
	for ref, want := range map[string][]string{
		"oci-layout://" + base:                      {"no tag", "1.2.0"},
		"oci-layout://" + base + ":1.3.0":           {"1.3.0", "1.2.0"},
		"oci-layout://" + base + "@" + digest:       {"digest", "1.2.0"},
		"oci-layout://" + base + ":1.2.0@" + digest: {"digest", "1.2.0"},
		"registry.example/todo-app":                 {"no tag", "1.2.0"},
		"registry.example/todo-app:1.3.0":           {"1.3.0", "1.2.0"},
	} {
		_, err := artifact.PushTemplate(ctx, ref, dir, "1.2.0")
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s: %v", ref, err)
		for _, w := range want {
			require.ErrorContains(t, err, w, ref)
		}
	}
	_, err := artifact.PushTemplate(ctx, "oci-layout://"+base+":1.2.0+b1", dir, "1.2.0+b1")
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a version with build metadata is no tag")
	require.ErrorContains(t, err, "1.2.0+b1")
	require.NoDirExists(t, base, "no refusal creates the layout")
}

// ADR-0218 Decision 6: the version tag at another digest is a conflict; at the same digest the push writes nothing.
func TestPushTemplateExistingTag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := layoutRepo(t) + ":1.2.0"
	dir := templateDir(t)
	digest, err := artifact.PushTemplate(ctx, ref, dir, "1.2.0")
	require.NoError(t, err)
	layout, _, _ := parseLayoutRef(ref)
	before := tree(t, layout)

	again, err := artifact.PushTemplate(ctx, ref, dir, "1.2.0")
	require.NoError(t, err)
	require.Equal(t, digest, again)
	require.Equal(t, before, tree(t, layout), "the same digest writes nothing")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "resources", "api.yaml"), []byte("functions: []\n"), 0o600))
	_, err = artifact.PushTemplate(ctx, ref, dir, "1.2.0")
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "1.2.0")
	require.ErrorContains(t, err, digest)
	require.Equal(t, before, tree(t, layout), "a conflict writes nothing")
}

// ADR-0218 Decision 7: ResolveTemplate and PullTemplate refuse a site and a function artifact, naming the types.
func TestResolveTemplateRefusesOtherTypes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	siteRef := layoutRef(t, "web")
	siteDigest, err := artifact.PushSite(ctx, siteRef, siteDir(t))
	require.NoError(t, err)
	bundle, entry := goodBundle(t)
	fnRef := layoutRef(t, "fn")
	_, err = artifact.PushBundle(ctx, fnRef, bundle, entry, "", "")
	require.NoError(t, err)

	for _, ref := range []string{siteRef, fnRef} {
		_, err := artifact.ResolveTemplate(ctx, ref)
		require.Equal(t, fault.Invalid, fault.KindOf(err), ref)
		require.ErrorContains(t, err, artifact.AppTemplateArtifactType)
		require.ErrorContains(t, err, "not a template artifact")
	}
	err = artifact.PullTemplate(ctx, siteRef, siteDigest, filepath.Join(t.TempDir(), "out"))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, artifact.SiteArtifactType)
}

// ADR-0218 Decision 7: PullTemplate requires a digest, fetches by it and verifies the layer against it, and unpacks
// traversal-safely.
func TestPullTemplateVerifies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := layoutRepo(t) + ":1.2.0"
	digest, err := artifact.PushTemplate(ctx, ref, templateDir(t), "1.2.0")
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "out")
	require.Equal(t, fault.Invalid, fault.KindOf(artifact.PullTemplate(ctx, ref, "", out)), "a digest is required")
	err = artifact.PullTemplate(ctx, ref, "sha256:"+strings.Repeat("0", 64), out)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "fetched by digest, not by tag: %v", err)

	layout, _, _ := parseLayoutRef(ref)
	layer := fetchManifest(t, ref, digest).Layers[0]
	other, err := artifact.PackBundle(siteDir(t), "index.html")
	require.NoError(t, err)
	blob := filepath.Join(layout, "blobs", "sha256", layer.Digest.Encoded())
	require.NoError(t, os.Chmod(blob, 0o600))
	require.NoError(t, os.WriteFile(blob, other, 0o600))
	err = artifact.PullTemplate(ctx, ref, digest, filepath.Join(t.TempDir(), "tampered"))
	require.ErrorContains(t, err, "template layer", "a layer that is not the digest's is refused")

	evilRef := layoutRepo(t) + ":evil"
	dir, _, ok := parseLayoutRef(evilRef)
	require.True(t, ok)
	store, err := oci.New(dir)
	require.NoError(t, err)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("pwned\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "../escape.yaml", Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o644}))
	_, err = tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	evil := content.NewDescriptorFromBytes(artifact.BundleTarMediaType, buf.Bytes())
	require.NoError(t, store.Push(ctx, evil, bytes.NewReader(buf.Bytes())))
	manifest, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, artifact.AppTemplateArtifactType,
		oras.PackManifestOptions{Layers: []ocispec.Descriptor{evil}})
	require.NoError(t, err)
	out = filepath.Join(t.TempDir(), "out")
	err = artifact.PullTemplate(ctx, evilRef, manifest.Digest.String(), out)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a traversal entry is refused: %v", err)
	require.NoFileExists(t, filepath.Join(filepath.Dir(out), "escape.yaml"))
}
