package artifact

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
)

// layoutTags lists the tags in the index.json of the layout at dir.
func layoutTags(t *testing.T, dir string) []string {
	t.Helper()
	store, err := oci.New(dir)
	require.NoError(t, err)
	var tags []string
	require.NoError(t, store.Tags(context.Background(), "", func(page []string) error {
		tags = append(tags, page...)
		return nil
	}))
	return tags
}

// TestIssue97_ParallelPushesKeepTheirTags: pushes into one layout that all succeed all keep their tags. The first part
// replays the losing interleaving of two funcdctl processes: one push opens the layout, the other runs to completion,
// then the first tags. The second part runs the pushes in parallel.
func TestIssue97_ParallelPushesKeepTheirTags(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "layout")
	bundle := filepath.Join(t.TempDir(), "bundle.js")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle() {}\n"), 0o600))

	first, tag, err := resolveTarget(ctx, ociLayoutScheme+dir+":fn-amd64")
	require.NoError(t, err)
	_, err = Push(ctx, ociLayoutScheme+dir+":fn-arm64", bundle, nil, "", "")
	require.NoError(t, err)
	manifest, err := oras.PackManifest(ctx, first, oras.PackManifestVersion1_1, artifactType, oras.PackManifestOptions{})
	require.NoError(t, err)
	require.NoError(t, first.Tag(ctx, manifest, tag))
	want := []string{"fn-amd64", "fn-arm64"}
	require.ElementsMatch(t, want, layoutTags(t, dir), "a push that succeeded lost its tag")

	var wg sync.WaitGroup
	for i := range 8 {
		tag := fmt.Sprintf("fn-%d", i)
		want = append(want, tag)
		wg.Go(func() {
			_, err := Push(ctx, ociLayoutScheme+dir+":"+tag, bundle, nil, "", "")
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	require.ElementsMatch(t, want, layoutTags(t, dir), "a parallel push that succeeded lost its tag")
}
