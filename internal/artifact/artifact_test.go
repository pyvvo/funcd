package artifact_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/artifact"
)

// layoutRef returns an oci-layout://<dir>:<tag> ref for a fresh local layout.
func layoutRef(t *testing.T, tag string) string {
	t.Helper()
	return "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":" + tag
}

func writeBundle(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bundle.js")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

// scenario: cli-pushes-artifact.
func TestScenarioCLIPushesArtifact(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	bundle := writeBundle(t, "export function handle() {}\n")

	digest, err := artifact.Push(context.Background(), ref, bundle)
	require.NoError(t, err)
	require.Contains(t, digest, "sha256:", "Push prints a sha256 descriptor digest for spec.artifact.digest")
}

// scenario: push-pull-roundtrips — push to a LOCAL layout (no registry server), pull back,
// assert identical bytes + digest.
func TestScenarioPushPullRoundtrips(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	body := "export function handle(_, e) { return e; }\n"
	bundle := writeBundle(t, body)

	digest, err := artifact.Push(context.Background(), ref, bundle)
	require.NoError(t, err)

	out := filepath.Join(t.TempDir(), "out")
	path, err := artifact.Pull(context.Background(), ref, digest, out)
	require.NoError(t, err)

	got, err := os.ReadFile(path) //nolint:gosec // path is test-owned
	require.NoError(t, err)
	require.Equal(t, body, string(got), "pulled bytes are identical to what was pushed")
	require.Equal(t, "bundle.js", filepath.Base(path), "the original filename is restored")
}

// scenario: platform-pulls-artifact — the digest is the authority; mismatch/empty/missing → fault.
func TestScenarioPlatformPullsArtifact(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	bundle := writeBundle(t, "export function handle() {}\n")
	digest, err := artifact.Push(context.Background(), ref, bundle)
	require.NoError(t, err)

	// empty digest is rejected (the digest is the authority).
	_, err = artifact.Pull(context.Background(), ref, "", t.TempDir())
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "empty digest → fault.Invalid")

	// a wrong digest → NotFound (no such manifest in the layout).
	_, err = artifact.Pull(context.Background(), ref, "sha256:"+
		"0000000000000000000000000000000000000000000000000000000000000000", t.TempDir())
	require.Error(t, err)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "unknown digest → fault.NotFound")

	// the correct digest pulls.
	_, err = artifact.Pull(context.Background(), ref, digest, t.TempDir())
	require.NoError(t, err)
}

// scenario: materializer-satisfies-adr0030-seam — the OrasMaterializer (a driver of
// internal/function.Materializer) resolves + pulls a Function's artifact by digest.
func TestScenarioMaterializerSatisfiesADR0030Seam(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	body := "export function handle() {}\n"
	bundle := writeBundle(t, body)
	digest, err := artifact.Push(context.Background(), ref, bundle)
	require.NoError(t, err)

	m := artifact.NewOrasMaterializer(t.TempDir())

	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace = "echo", "default"
	fn.Spec.Artifact = v1.ArtifactRef{URI: ref, Digest: digest}

	path, err := m.Materialize(context.Background(), fn)
	require.NoError(t, err)
	got, err := os.ReadFile(path) //nolint:gosec // path is materializer-owned under a temp dir
	require.NoError(t, err)
	require.Equal(t, body, string(got), "the materialized artifact matches the pushed bundle")

	// container-readable perms (ADR-0052 footprint-lane fix): the artifact is bind-mounted into a
	// curated image that runs as a non-root user (USER node), so it must be world-readable (0644)
	// under a traversable (0755) cache dir — else the shim cannot load it (Cannot find module).
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), fi.Mode().Perm(), "artifact must be container-readable (0644)")
	di, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), di.Mode().Perm(), "artifact cache dir must be traversable (0755)")

	// second call is served from the per-digest cache (immutable) — same path.
	again, err := m.Materialize(context.Background(), fn)
	require.NoError(t, err)
	require.Equal(t, path, again, "the per-digest cache returns the same local path")

	// a Function without a digest is rejected (the digest is the authority).
	fn.Spec.Artifact.Digest = ""
	_, err = m.Materialize(context.Background(), fn)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "no digest → fault.Invalid")
}

// scenario: resolve-tag-to-digest (ADR-0035) — Resolve(uri) returns the manifest digest
// the push printed, so the reconciler can pin it without the user typing it.
func TestScenarioResolveTagToDigest(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	bundle := writeBundle(t, "export function handle() {}\n")
	pushed, err := artifact.Push(context.Background(), ref, bundle)
	require.NoError(t, err)

	m := artifact.NewOrasMaterializer(t.TempDir())
	resolved, err := m.Resolve(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, pushed, resolved, "Resolve returns the pushed manifest digest")

	_, err = m.Resolve(context.Background(), layoutRef(t, "absent"))
	require.Error(t, err, "an unknown tag fails to resolve")
}
