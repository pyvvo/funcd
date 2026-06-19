package artifact

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
)

// countingTarget wraps a read-only target and counts Fetch calls by layer media type — the seam
// that lets the inspect-without-pull invariant be asserted (the bundle layer is never fetched).
type countingTarget struct {
	oras.ReadOnlyTarget
	fetched map[string]int
}

func (c *countingTarget) Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	c.fetched[desc.MediaType]++
	return c.ReadOnlyTarget.Fetch(ctx, desc)
}

// scenario: inspect-without-pull — Inspect fetches the manifest + contract blob ONLY; it NEVER
// fetches the bundle layer (ADR-0059's static-inspection invariant). Also asserts the manifest
// carries the dedicated contract layer + the dev.funcd.contract.v1 annotation = the blob digest.
func TestInspectWithoutPullNeverFetchesBundle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "layout")
	ref := ociLayoutScheme + dir + ":v1"
	bundlePath := filepath.Join(t.TempDir(), "bundle.js")
	require.NoError(t, os.WriteFile(bundlePath, []byte("export function handle() {}\n"), 0o600))
	blob, err := ContractBlob([]byte(`{"type":"object","additionalProperties":false}`), nil)
	require.NoError(t, err)
	digest, err := Push(context.Background(), ref, bundlePath, blob)
	require.NoError(t, err)

	store, err := oci.New(dir)
	require.NoError(t, err)
	ct := &countingTarget{ReadOnlyTarget: store, fetched: map[string]int{}}

	got, err := inspectFrom(context.Background(), ct, digest, digest)
	require.NoError(t, err)
	require.JSONEq(t, string(blob), string(got), "Inspect returns the contract blob")
	require.Zero(t, ct.fetched[bundleMediaType], "Inspect must NEVER fetch the bundle layer")
	require.Equal(t, 1, ct.fetched[contractMediaType], "Inspect fetches the contract blob exactly once")

	// the manifest carries the dedicated contract layer + the annotation pinned to its digest.
	_, manifestData, err := oras.FetchBytes(context.Background(), store, digest, oras.DefaultFetchBytesOptions)
	require.NoError(t, err)
	var manifest ocispec.Manifest
	require.NoError(t, json.Unmarshal(manifestData, &manifest))
	contractLayer, ok := layerByMediaType(manifest.Layers, contractMediaType)
	require.True(t, ok, "a dedicated contract layer is present")
	require.Equal(t, contractLayer.Digest.String(), manifest.Annotations[contractAnnotation],
		"the dev.funcd.contract.v1 annotation pins the contract blob digest")
}
