package artifact_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
)

// platformBundle pushes a bundle for platform into the layout at dir under tag; its handler names the platform, so a
// pull shows which one it got.
func platformBundle(t *testing.T, dir, tag string, platform v1.OCIPlatform, runtime string) string {
	t.Helper()
	src, entry := goodBundle(t)
	body, err := os.ReadFile(filepath.Join(src, entry)) //nolint:gosec // test-owned
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(src, entry), append(body, []byte("# built for "+string(platform)+"\n")...), 0o600))
	ref := "oci-layout://" + dir + ":" + tag
	_, err = artifact.PushBundle(context.Background(), ref, src, entry, runtime, platform)
	require.NoError(t, err)
	return ref
}

// multiArch pushes amd64 and arm64 bundles into one layout and indexes them; it returns the layout dir, the index ref
// and its digest.
func multiArch(t *testing.T) (dir, ref, digest string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "layout")
	amd := platformBundle(t, dir, "fn-amd64", v1.PlatformLinuxAMD64, "python314")
	arm := platformBundle(t, dir, "fn-arm64", v1.PlatformLinuxARM64, "python314")
	ref = "oci-layout://" + dir + ":fn"
	digest, err := artifact.PushIndex(context.Background(), ref, []string{amd, arm})
	require.NoError(t, err)
	return dir, ref, digest
}

// scenario: push-records-platform
func TestScenarioPushRecordsPlatform(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	digest, err := artifact.Push(context.Background(), ref, writeBundle(t, "export function handle() {}\n"), mustContract(t), "", v1.PlatformLinuxARM64)
	require.NoError(t, err)
	m := fetchManifest(t, ref, digest)
	require.Equal(t, "linux/arm64", m.Annotations[artifact.PlatformAnnotation])

	ps, err := artifact.Platforms(context.Background(), ref, digest)
	require.NoError(t, err)
	require.Equal(t, []v1.OCIPlatform{v1.PlatformLinuxARM64}, ps)
}

// scenario: index-combines-platforms
func TestScenarioIndexCombinesPlatforms(t *testing.T) {
	t.Parallel()
	_, ref, digest := multiArch(t)
	require.True(t, strings.HasPrefix(digest, "sha256:"))

	ps, err := artifact.Platforms(context.Background(), ref, digest)
	require.NoError(t, err)
	require.ElementsMatch(t, []v1.OCIPlatform{v1.PlatformLinuxAMD64, v1.PlatformLinuxARM64}, ps)

	resolved, err := artifact.NewOrasMaterializer(t.TempDir(), "").Resolve(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, digest, resolved, "the tag names the index")
}

// scenario: index-rejects-inconsistent-sources
func TestScenarioIndexRejectsInconsistentSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "layout")
	amd := platformBundle(t, dir, "amd", v1.PlatformLinuxAMD64, "python314")
	arm := platformBundle(t, dir, "arm", v1.PlatformLinuxARM64, "python314")
	amd2 := platformBundle(t, dir, "amd2", v1.PlatformLinuxAMD64, "python314")
	armNode := platformBundle(t, dir, "arm-node", v1.PlatformLinuxARM64, "nodejs22")
	src, entry := goodBundle(t)
	_, err := artifact.PushBundle(ctx, "oci-layout://"+dir+":plain", src, entry, "python314", "")
	require.NoError(t, err)
	bundleContract, err := artifact.VerifyBundleContract(src, entry) // the bundles' own contract
	require.NoError(t, err)
	_, err = artifact.Push(ctx, "oci-layout://"+dir+":file-arm", writeBundle(t, "x"), bundleContract, "python314", v1.PlatformLinuxARM64)
	require.NoError(t, err)
	_, err = artifact.Push(ctx, "oci-layout://"+dir+":other-contract", writeBundle(t, "x"),
		[]byte(`{"dialect":"https://json-schema.org/draft/2020-12/schema","input":{"type":"null"},"output":{"type":"null"}}`),
		"python314", v1.PlatformLinuxARM64)
	require.NoError(t, err)
	elsewhere := platformBundle(t, filepath.Join(t.TempDir(), "other"), "arm", v1.PlatformLinuxARM64, "python314")

	for name, tc := range map[string]struct {
		sources []string
		want    string
	}{
		"repeated platform":   {[]string{amd, amd2}, "repeats platform linux/amd64"},
		"missing annotation":  {[]string{amd, "oci-layout://" + dir + ":plain"}, "carries no dev.funcd.platform"},
		"different contract":  {[]string{amd, "oci-layout://" + dir + ":other-contract"}, "different contract"},
		"different runtime":   {[]string{amd, armNode}, "runtime"},
		"different kind":      {[]string{amd, "oci-layout://" + dir + ":file-arm"}, "differ in kind"},
		"different layout":    {[]string{amd, elsewhere}, "not in the index's repository"},
		"source is not found": {[]string{amd, "oci-layout://" + dir + ":nope"}, "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := artifact.PushIndex(ctx, "oci-layout://"+dir+":idx-"+strings.ReplaceAll(name, " ", "-"), tc.sources)
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.Contains(t, err.Error(), tc.want)
		})
	}
	_, err = artifact.Platforms(ctx, "oci-layout://"+dir+":idx-repeated-platform", "")
	require.Error(t, err, "a refused index writes nothing")
	_, err = artifact.PushIndex(ctx, "oci-layout://"+dir+":ok", []string{amd, arm})
	require.NoError(t, err, "the consistent pair indexes")
}

// scenario: pull-selects-node-platform
func TestScenarioPullSelectsNodePlatform(t *testing.T) {
	t.Parallel()
	_, ref, digest := multiArch(t)
	for _, node := range []v1.OCIPlatform{v1.PlatformLinuxAMD64, v1.PlatformLinuxARM64} {
		cache := t.TempDir()
		m := artifact.NewOrasMaterializer(cache, node)
		path, err := m.Materialize(context.Background(), mkFunction(t, ref, digest))
		require.NoError(t, err)
		body, err := os.ReadFile(path) //nolint:gosec // test-owned
		require.NoError(t, err)
		require.Contains(t, string(body), "# built for "+string(node), "node %s gets its own bundle", node)
		_, err = os.Stat(filepath.Join(filepath.Dir(path), "__funcd_contract.json"))
		require.NoError(t, err, "the contract is delivered as for a single manifest")
		require.Contains(t, filepath.Dir(path), node.OS()+"-"+node.Arch(), "the cache key includes the node platform")
	}
}

// scenario: pull-no-matching-platform
func TestScenarioPullNoMatchingPlatform(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "layout")
	amd := platformBundle(t, dir, "amd", v1.PlatformLinuxAMD64, "python314")
	ref := "oci-layout://" + dir + ":fn"
	digest, err := artifact.PushIndex(context.Background(), ref, []string{amd})
	require.NoError(t, err)

	_, err = artifact.Pull(context.Background(), ref, digest, t.TempDir(), v1.PlatformLinuxARM64)
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.Contains(t, err.Error(), "no bundle for linux/arm64; it provides [linux/amd64]")
}

// scenario: single-manifest-unchanged
func TestScenarioSingleManifestUnchanged(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	digest, err := artifact.Push(context.Background(), ref, writeBundle(t, "export function handle() {}\n"), nil, "", "")
	require.NoError(t, err)
	_, ok := fetchManifest(t, ref, digest).Annotations[artifact.PlatformAnnotation]
	require.False(t, ok, "no --platform, no annotation")
	ps, err := artifact.Platforms(context.Background(), ref, digest)
	require.NoError(t, err)
	require.Nil(t, ps, "an unannotated manifest runs anywhere")
	for _, node := range []v1.OCIPlatform{v1.PlatformLinuxAMD64, "darwin/arm64"} {
		_, err := artifact.Pull(context.Background(), ref, digest, t.TempDir(), node)
		require.NoError(t, err, "every node pulls it")
	}
}

// scenario: inspect-index
func TestScenarioInspectIndex(t *testing.T) {
	t.Parallel()
	_, ref, digest := multiArch(t)
	contract, resolved, err := artifact.InspectContract(context.Background(), ref, digest)
	require.NoError(t, err)
	require.Contains(t, string(contract), "\"output\"")
	require.Equal(t, digest, resolved, "the pinned authority is the index digest")
	rt, err := artifact.InspectRuntime(context.Background(), ref, digest)
	require.NoError(t, err)
	require.Equal(t, "python314", rt)
}

// The index carries one descriptor per source, with its platform, and the funcd artifact type.
func TestIndexShape(t *testing.T) {
	t.Parallel()
	dir, _, digest := multiArch(t)
	data, err := os.ReadFile(filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))) //nolint:gosec // test-owned
	require.NoError(t, err)
	var index ocispec.Index
	require.NoError(t, json.Unmarshal(data, &index))
	require.Equal(t, ocispec.MediaTypeImageIndex, index.MediaType)
	require.Equal(t, "application/vnd.funcd.function.artifact.v1", index.ArtifactType)
	require.Len(t, index.Manifests, 2)
	for _, d := range index.Manifests {
		require.NotNil(t, d.Platform)
		require.Equal(t, "linux", d.Platform.OS)
		require.Equal(t, ocispec.MediaTypeImageManifest, d.MediaType)
	}
}

// Issue 95: a restarted daemon gates a Function's cached artifact on its platforms, so a digest this node already
// materialized must not need the artifact's source after a restart.
func TestIssue95_CachedPlatformsNeedNoSourceAfterRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	single := filepath.Join(t.TempDir(), "layout")
	singleRef := "oci-layout://" + single + ":v1"
	singleDigest, err := artifact.Push(ctx, singleRef, writeBundle(t, "export function handle() {}\n"), nil, "", "")
	require.NoError(t, err)
	index, indexRef, indexDigest := multiArch(t)

	for name, a := range map[string]struct{ layout, ref, digest string }{
		"unannotated manifest": {single, singleRef, singleDigest},
		"index":                {index, indexRef, indexDigest},
	} {
		cache := t.TempDir()
		before := artifact.NewOrasMaterializer(cache, v1.PlatformLinuxAMD64)
		want, err := before.Platforms(ctx, a.ref, a.digest)
		require.NoError(t, err, name)
		_, err = before.Materialize(ctx, mkFunction(t, a.ref, a.digest))
		require.NoError(t, err, name)

		require.NoError(t, os.RemoveAll(a.layout))
		after := artifact.NewOrasMaterializer(cache, v1.PlatformLinuxAMD64)
		got, err := after.Platforms(ctx, a.ref, a.digest)
		require.NoError(t, err, "%s: the platforms of a cached digest need no source", name)
		require.Equal(t, want, got, name)
		_, err = after.Materialize(ctx, mkFunction(t, a.ref, a.digest))
		require.NoError(t, err, name)
	}
}

// A digest-form source is refused with "needs a tag" on both an OCI layout and a registry (ADR-0145 Decision 2).
func TestIssue156_IndexRefusesDigestSourceConsistently(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "layout")
	arm := platformBundle(t, dir, "arm", v1.PlatformLinuxARM64, "python314")
	armDigest, err := artifact.NewOrasMaterializer(t.TempDir(), "").Resolve(ctx, arm)
	require.NoError(t, err)
	const registryDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	for name, tc := range map[string]struct {
		ref    string
		source string
	}{
		"layout digest":         {"oci-layout://" + dir + ":idx", "oci-layout://" + dir + "@" + armDigest},
		"layout tag and digest": {"oci-layout://" + dir + ":idx", arm + "@" + armDigest},
		"registry digest":       {"127.0.0.1:1/fn:idx", "127.0.0.1:1/fn@" + registryDigest},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := artifact.PushIndex(ctx, tc.ref, []string{tc.source})
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.Contains(t, err.Error(), "source "+tc.source+" needs a tag")
		})
	}
}
