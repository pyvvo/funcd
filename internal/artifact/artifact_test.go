package artifact_test

import (
	"context"
	"encoding/json"
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

	digest, err := artifact.Push(context.Background(), ref, bundle, nil, "")
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

	digest, err := artifact.Push(context.Background(), ref, bundle, nil, "")
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
	digest, err := artifact.Push(context.Background(), ref, bundle, nil, "")
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
	digest, err := artifact.Push(context.Background(), ref, bundle, nil, "")
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
	pushed, err := artifact.Push(context.Background(), ref, bundle, nil, "")
	require.NoError(t, err)

	m := artifact.NewOrasMaterializer(t.TempDir())
	resolved, err := m.Resolve(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, pushed, resolved, "Resolve returns the pushed manifest digest")

	_, err = m.Resolve(context.Background(), layoutRef(t, "absent"))
	require.Error(t, err, "an unknown tag fails to resolve")
}

const (
	schemaIn  = `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`
	schemaOut = `{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`
)

// scenario: contract-embedded-on-push — a contracted push carries the I/O schemas as OCI metadata;
// Inspect reads them back as the {input?, output?, dialect} payload (ADR-0059).
func TestScenarioContractEmbeddedOnPush(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	bundle := writeBundle(t, "export function handle() {}\n")
	blob, err := artifact.ContractBlob([]byte(schemaIn), []byte(schemaOut))
	require.NoError(t, err)
	digest, err := artifact.Push(context.Background(), ref, bundle, blob, "")
	require.NoError(t, err)

	got, err := artifact.Inspect(context.Background(), ref, digest)
	require.NoError(t, err)
	var payload struct {
		Input   json.RawMessage `json:"input"`
		Output  json.RawMessage `json:"output"`
		Dialect string          `json:"dialect"`
	}
	require.NoError(t, json.Unmarshal(got, &payload))
	require.JSONEq(t, schemaIn, string(payload.Input))
	require.JSONEq(t, schemaOut, string(payload.Output))
	require.Equal(t, "https://json-schema.org/draft/2020-12/schema", payload.Dialect)
}

// scenario: runtime-annotation-roundtrips (ADR-0094) — a push carrying a runtime class records it as
// a manifest annotation; InspectRuntime reads it back from the manifest alone. An artifact pushed
// without a runtime → fault.NotFound (self-describing artifacts are opt-in).
func TestScenarioRuntimeAnnotationRoundtrips(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	bundle := writeBundle(t, "export function handle() {}\n")
	digest, err := artifact.Push(context.Background(), ref, bundle, nil, "nodejs22")
	require.NoError(t, err)

	rt, err := artifact.InspectRuntime(context.Background(), ref, digest)
	require.NoError(t, err)
	require.Equal(t, "nodejs22", rt)

	// a runtime-less push asserts no runtime.
	plain := layoutRef(t, "v1")
	pd, err := artifact.Push(context.Background(), plain, bundle, nil, "")
	require.NoError(t, err)
	_, err = artifact.InspectRuntime(context.Background(), plain, pd)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "a runtime-less artifact → fault.NotFound")
}

// scenario: no-contract-no-metadata — a nil-contract push is the unchanged ADR-0031 artifact;
// Inspect returns fault.NotFound (no contract surface added).
func TestScenarioNoContractNoMetadata(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	bundle := writeBundle(t, "export function handle() {}\n")
	digest, err := artifact.Push(context.Background(), ref, bundle, nil, "")
	require.NoError(t, err)

	_, err = artifact.Inspect(context.Background(), ref, digest)
	require.Error(t, err)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "a contract-less artifact → fault.NotFound on inspect")
}

// scenario: contract-mandatory-push-gate (ADR-0090) — ContractBlob refuses a missing side; there is
// no contract-less artifact. Both-empty and each-single-side-empty → fault.Invalid.
func TestScenario_contract_mandatory_push_gate(t *testing.T) {
	t.Parallel()
	_, err := artifact.ContractBlob(nil, nil)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "no contract at all → fault.Invalid")
}

// scenario: both-keys-required (ADR-0090) — a document with only one side is rejected; both input and
// output must be present (a void side is VoidSchema, never absent).
func TestScenario_both_keys_required(t *testing.T) {
	t.Parallel()
	_, ierr := artifact.ContractBlob([]byte(schemaIn), nil)
	require.Error(t, ierr, "output side missing → fault.Invalid")
	require.Equal(t, fault.Invalid, fault.KindOf(ierr))
	_, oerr := artifact.ContractBlob(nil, []byte(schemaOut))
	require.Error(t, oerr, "input side missing → fault.Invalid")
	require.Equal(t, fault.Invalid, fault.KindOf(oerr))
}

// scenario: void-side-serialized (ADR-0090) — a void side is the explicit {"type":"null"} schema and
// it is always SERIALIZED into the blob (no omitempty); both keys are present.
func TestScenario_void_side_serialized(t *testing.T) {
	t.Parallel()
	blob, err := artifact.ContractBlob([]byte(schemaIn), []byte(artifact.VoidSchema))
	require.NoError(t, err)
	var payload struct {
		Input   json.RawMessage `json:"input"`
		Output  json.RawMessage `json:"output"`
		Dialect string          `json:"dialect"`
	}
	require.NoError(t, json.Unmarshal(blob, &payload))
	require.JSONEq(t, schemaIn, string(payload.Input))
	require.JSONEq(t, artifact.VoidSchema, string(payload.Output), "the void side is serialized, not omitted")
	require.NotEmpty(t, payload.Dialect)
	require.Contains(t, string(blob), `"output"`, "the output key is always present in the marshaled blob")
}

// scenario: bundle-selected-by-mediatype — with a contract layer present, Pull still materializes
// the BUNDLE (selected by media type, not by index).
func TestScenarioBundleSelectedByMediaType(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	body := "export function handle(_, e) { return e; }\n"
	bundle := writeBundle(t, body)
	blob, err := artifact.ContractBlob([]byte(schemaIn), []byte(artifact.VoidSchema))
	require.NoError(t, err)
	digest, err := artifact.Push(context.Background(), ref, bundle, blob, "")
	require.NoError(t, err)

	path, err := artifact.Pull(context.Background(), ref, digest, filepath.Join(t.TempDir(), "out"))
	require.NoError(t, err)
	got, err := os.ReadFile(path) //nolint:gosec // path is test-owned
	require.NoError(t, err)
	require.Equal(t, body, string(got), "Pull returns the bundle bytes, never the contract layer")
}

// scenario: contract-digest-pinned — inspecting by the ORIGINAL digest returns the originally
// deployed contract even after the tag is moved to a different artifact (tamper-evident).
func TestScenarioContractDigestPinned(t *testing.T) {
	t.Parallel()
	ref := layoutRef(t, "v1")
	blobA, err := artifact.ContractBlob([]byte(schemaIn), []byte(artifact.VoidSchema)) // has "name"
	require.NoError(t, err)
	digestA, err := artifact.Push(context.Background(), ref, writeBundle(t, "export function handle() {}\n"), blobA, "")
	require.NoError(t, err)

	// move tag v1 to a DIFFERENT artifact (different bundle ⇒ different manifest digest).
	blobB, err := artifact.ContractBlob([]byte(schemaOut), []byte(artifact.VoidSchema)) // has "ok"
	require.NoError(t, err)
	digestB, err := artifact.Push(context.Background(), ref, writeBundle(t, "export function handle(_, e) { return e; }\n"), blobB, "")
	require.NoError(t, err)
	require.NotEqual(t, digestA, digestB, "the moved tag points at a new manifest")

	got, err := artifact.Inspect(context.Background(), ref, digestA)
	require.NoError(t, err)
	require.Contains(t, string(got), `"name"`, "digest-pinned inspect returns the originally deployed contract")
	require.NotContains(t, string(got), `"ok"`, "not the contract the tag now points at")
}
