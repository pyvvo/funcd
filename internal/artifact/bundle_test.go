package artifact_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"math/rand"
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

// goodBundle writes a minimal well-formed bundle: a handler carrying BOTH baked validators and a
// valid {input, output} contract (ADR-0089/0090), plus a vendored file, into a temp dir.
func goodBundle(t *testing.T) (dir, entry string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "vendored", "pkg"), 0o755))
	entry = "handler.py"
	handler := "" +
		"def __funcd_validate_input(d):\n    return []\n" +
		"def __funcd_validate_output(d):\n    return []\n" +
		"def handle(context, event):\n    return {}\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, entry), []byte(handler), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vendored", "pkg", "__init__.py"), []byte("X = 1\n"), 0o600))
	// An in-profile {input, output} contract (ADR-0058): a void input and a closed-record output.
	// VerifyBundleContract now runs contract.Check per side (ADR-0123), so the fixture must be
	// within the funcd profile (an empty-properties object reads as an open record — out of profile).
	contract := `{"input":{"type":"null"},"output":{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "__funcd_contract.json"), []byte(contract), 0o600))
	return dir, entry
}

// fetchManifest reads back a pushed manifest from a local layout ref by digest.
func fetchManifest(t *testing.T, ref, digest string) ocispec.Manifest {
	t.Helper()
	dir, _, ok := parseLayoutRef(ref)
	require.True(t, ok)
	store, err := oci.New(dir)
	require.NoError(t, err)
	_, data, err := oras.FetchBytes(context.Background(), store, digest, oras.DefaultFetchBytesOptions)
	require.NoError(t, err)
	var m ocispec.Manifest
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

// parseLayoutRef splits oci-layout://<dir>:<tag> for the test (mirrors the internal parser).
func parseLayoutRef(ref string) (dir, tag string, ok bool) {
	const scheme = "oci-layout://"
	if len(ref) < len(scheme) || ref[:len(scheme)] != scheme {
		return "", "", false
	}
	rest := ref[len(scheme):]
	for i := len(rest) - 1; i >= 0; i-- {
		if rest[i] == ':' {
			return rest[:i], rest[i+1:], true
		}
		if rest[i] == '/' || rest[i] == '\\' {
			break
		}
	}
	return rest, "", true
}

// scenario: push-directory-bundles-tar — PushBundle a dir → the artifact carries a
// BundleTarMediaType layer + the entry annotation.
func TestScenarioPushDirectoryBundlesTar(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	dir, entry := goodBundle(t)

	digest, err := artifact.PushBundle(context.Background(), ref, dir, entry, "", "")
	require.NoError(t, err)
	require.Contains(t, digest, "sha256:")

	m := fetchManifest(t, ref, digest)
	layer, ok := layerByMT(m.Layers, artifact.BundleTarMediaType)
	require.True(t, ok, "the artifact carries a tar+gzip bundle layer")
	require.Equal(t, entry, layer.Annotations[artifact.BundleEntryAnnotation], "the entry is recorded on the layer")
	require.Equal(t, entry, m.Annotations[artifact.BundleEntryAnnotation], "the entry is also on the manifest")
}

// scenario: push-single-file-unchanged — Push a lone .py → the unchanged single-blob layer (no tar).
func TestScenarioPushSingleFileUnchanged(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	p := filepath.Join(t.TempDir(), "handler.py")
	require.NoError(t, os.WriteFile(p, []byte("def handle():\n    return {}\n"), 0o600))
	contract := mustContract(t)

	digest, err := artifact.Push(context.Background(), ref, p, contract, "", "")
	require.NoError(t, err)

	m := fetchManifest(t, ref, digest)
	_, ok := layerByMT(m.Layers, artifact.BundleTarMediaType)
	require.False(t, ok, "a single file is NOT a tar bundle")
	_, ok = layerByMT(m.Layers, "application/vnd.funcd.function.bundle")
	require.True(t, ok, "a single file stays the ADR-0031 single-blob layer")
}

// scenario: packbundle-deterministic — the same tree packed twice → identical bytes.
func TestScenarioPackBundleDeterministic(t *testing.T) {
	t.Parallel()
	dir, entry := goodBundle(t)

	a, err := artifact.PackBundle(dir, entry)
	require.NoError(t, err)
	b, err := artifact.PackBundle(dir, entry)
	require.NoError(t, err)
	require.Equal(t, a, b, "identical trees yield identical tar bytes (sorted entries, zeroed mtimes)")
	require.NotEmpty(t, a)

	// A missing entry is refused.
	_, err = artifact.PackBundle(dir, "nope.py")
	require.Equal(t, fault.Invalid, fault.KindOf(err), "entry must be an existing regular file")
}

// scenario: push-gates-bundle-contract — a bundle missing the contract / a key / a baked validator
// is refused (fault.Invalid); a good bundle promotes the contract to the OCI contract layer.
func TestScenarioPushGatesBundleContract(t *testing.T) {
	t.Parallel()

	t.Run("missing contract file", func(t *testing.T) {
		t.Parallel()
		dir, entry := goodBundle(t)
		require.NoError(t, os.Remove(filepath.Join(dir, "__funcd_contract.json")))
		_, err := artifact.VerifyBundleContract(dir, entry)
		require.Equal(t, fault.Invalid, fault.KindOf(err))
	})

	t.Run("missing a key", func(t *testing.T) {
		t.Parallel()
		dir, entry := goodBundle(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "__funcd_contract.json"),
			[]byte(`{"input":{"type":"null"}}`), 0o600))
		_, err := artifact.VerifyBundleContract(dir, entry)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "output key is mandatory (ADR-0090)")
	})

	t.Run("no baked validator symbols still passes (ADR-0123 schema-only)", func(t *testing.T) {
		t.Parallel()
		dir, entry := goodBundle(t)
		// A schema-only handler carrying NO __funcd_validate_* symbols: the artifact no longer bakes
		// a validator (ADR-0123 supersedes ADR-0060's build-time bake — the shim compiles it at
		// warm-up), so the entry defining no validator symbol is now valid.
		require.NoError(t, os.WriteFile(filepath.Join(dir, entry),
			[]byte("def handle(context, event):\n    return {}\n"), 0o600))
		blob, err := artifact.VerifyBundleContract(dir, entry)
		require.NoError(t, err, "a schema-only bundle (no baked validator) passes the gate")
		require.Contains(t, string(blob), "\"input\"")
	})

	t.Run("out-of-profile schema fails contract.Check", func(t *testing.T) {
		t.Parallel()
		dir, entry := goodBundle(t)
		// An OPEN record (additionalProperties absent) is outside the funcd profile (ADR-0058). The
		// gate now runs contract.Check per side (ADR-0123), so this fails at push, not at worker
		// compile-time — a bundle can no longer ship a schema the shim then can't compile in-profile.
		require.NoError(t, os.WriteFile(filepath.Join(dir, "__funcd_contract.json"),
			[]byte(`{"input":{"type":"null"},"output":{"type":"object","properties":{"x":{"type":"string"}}}}`), 0o600))
		_, err := artifact.VerifyBundleContract(dir, entry)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "an out-of-profile schema is refused at push (contract.Check)")
	})

	t.Run("good bundle promotes the contract", func(t *testing.T) {
		t.Parallel()
		ref := layoutRef(t, "v1")
		dir, entry := goodBundle(t)
		blob, err := artifact.VerifyBundleContract(dir, entry)
		require.NoError(t, err)
		require.Contains(t, string(blob), "\"input\"")
		require.Contains(t, string(blob), "\"output\"")

		digest, err := artifact.PushBundle(context.Background(), ref, dir, entry, "", "")
		require.NoError(t, err)
		// The contract is inspectable straight from the manifest (never pulling the bundle).
		got, err := artifact.Inspect(context.Background(), ref, digest)
		require.NoError(t, err)
		require.Contains(t, string(got), "\"output\"", "the bundle contract is promoted to the OCI contract layer")
	})
}

// scenario: pull-untars-bundle — Pull a bundle → the tree is present, digest verified, and it
// returns dir/<entry>. Plus: a crafted "../" tar path is refused (path-traversal guard).
func TestScenarioPullUntarsBundle(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	dir, entry := goodBundle(t)

	digest, err := artifact.PushBundle(context.Background(), ref, dir, entry, "", "")
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "out")
	path, err := artifact.Pull(context.Background(), ref, digest, out, "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(out, entry), path, "Pull returns <dir>/<entry>")

	// The whole tree materialized, including the vendored subdir.
	require.FileExists(t, filepath.Join(out, "handler.py"))
	require.FileExists(t, filepath.Join(out, "vendored", "pkg", "__init__.py"))
	require.FileExists(t, filepath.Join(out, "__funcd_contract.json"))
}

// scenario: pull-untars-bundle (security) — a bundle layer whose tar contains a "../escape" entry is
// REFUSED by the traversal guard rather than writing outside the bundle dir. This forges a malicious
// layer directly (PackBundle can never emit such a path) and verifies Pull fails closed.
func TestScenarioPullRejectsPathTraversal(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	dir, _, ok := parseLayoutRef(ref)
	require.True(t, ok)
	store, err := oci.New(dir)
	require.NoError(t, err)

	// Craft a gzipped tar with a path that escapes the destination dir.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("pwned\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "../escape.txt", Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o644}))
	_, err = tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	ctx := context.Background()
	layer := content.NewDescriptorFromBytes(artifact.BundleTarMediaType, buf.Bytes())
	layer.Annotations = map[string]string{artifact.BundleEntryAnnotation: "escape.txt"}
	require.NoError(t, store.Push(ctx, layer, bytes.NewReader(buf.Bytes())))
	manifest, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1,
		"application/vnd.funcd.function.artifact.v1",
		oras.PackManifestOptions{
			Layers:              []ocispec.Descriptor{layer},
			ManifestAnnotations: map[string]string{artifact.BundleEntryAnnotation: "escape.txt"},
		})
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "out")
	_, perr := artifact.Pull(ctx, ref, manifest.Digest.String(), out, "")
	require.Error(t, perr, "a traversal path must be refused")
	require.Equal(t, fault.Invalid, fault.KindOf(perr), "traversal → fault.Invalid, fail closed")

	// Nothing was written outside the bundle dir.
	require.NoFileExists(t, filepath.Join(filepath.Dir(out), "escape.txt"))
}

// scenario: pull-single-file-unchanged — a single-blob Pull is unchanged (covered broadly by the
// existing roundtrip test; this pins the bundle path does not regress the single-file path).
func TestScenarioPullSingleFileUnchanged(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	p := filepath.Join(t.TempDir(), "handler.py")
	body := "def handle():\n    return {}\n"
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))

	digest, err := artifact.Push(context.Background(), ref, p, mustContract(t), "", "")
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "out")
	path, err := artifact.Pull(context.Background(), ref, digest, out, "")
	require.NoError(t, err)
	require.Equal(t, "handler.py", filepath.Base(path))
	got, err := os.ReadFile(path) //nolint:gosec // test-owned path
	require.NoError(t, err)
	require.Equal(t, body, string(got))
}

// layerByMT is the test's media-type layer lookup (the package's is unexported).
func layerByMT(layers []ocispec.Descriptor, mt string) (ocispec.Descriptor, bool) {
	for _, l := range layers {
		if l.MediaType == mt {
			return l, true
		}
	}
	return ocispec.Descriptor{}, false
}

// mustContract builds a valid mandatory {input, output} contract blob for the single-file tests.
func mustContract(t *testing.T) []byte {
	t.Helper()
	blob, err := artifact.ContractBlob([]byte(`{"type":"null"}`), []byte(`{"type":"null"}`))
	require.NoError(t, err)
	return blob
}

// TestScenarioPullLargeBundleExceedsFetchAllCap is a regression guard: a bundle whose (compressed)
// layer exceeds oras's 32 MiB content.FetchAll cap — vendored native deps run to ~100MB — must still
// pull. pullBundle STREAMS the layer (target.Fetch + a VerifyReader), it does not buffer it via
// FetchAll, so the cap never applies.
func TestScenarioPullLargeBundleExceedsFetchAllCap(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "big")
	dir := filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	const entry = "handler.py"
	handler := "def __funcd_validate_input(d):\n    return []\n" +
		"def __funcd_validate_output(d):\n    return []\n" +
		"def handle(context, event):\n    return {}\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, entry), []byte(handler), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "__funcd_contract.json"),
		[]byte(`{"input":{"type":"null"},"output":{"type":"null"}}`), 0o600))
	// ~34 MiB of INCOMPRESSIBLE bytes so the gzipped layer clears the 32 MiB FetchAll cap.
	big := make([]byte, 34<<20)
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test fixture, not security
	_, _ = rng.Read(big)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vendored.bin"), big, 0o600))

	digest, err := artifact.PushBundle(context.Background(), ref, dir, entry, "", "")
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "out")
	path, err := artifact.Pull(context.Background(), ref, digest, out, "")
	require.NoError(t, err, "a >32 MiB bundle layer must stream, not trip the FetchAll cap")
	require.Equal(t, filepath.Join(out, entry), path)
	got, err := os.ReadFile(filepath.Join(out, "vendored.bin"))
	require.NoError(t, err)
	require.Equal(t, big, got, "the large vendored file round-trips byte-for-byte")
}
