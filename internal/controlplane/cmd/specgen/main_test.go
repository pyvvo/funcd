package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/kindref"
)

// writeGuide deletes a generated page that kindref no longer renders and keeps a page written by hand (ADR-0323
// Decision 1: a page that carries the marker belongs to specgen).
func TestWriteGuideRemovesOnlyStaleGeneratedPages(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "develop", "gone", "spec.md")
	usage := filepath.Join(dir, "develop", "services", "kv", "index.md")
	for path, body := range map[string]string{
		stale: "---\n" + kindref.Marker + "\ntitle: \"Gone spec\"\n---\n",
		usage: "---\ntitle: KV\n---\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}

	require.NoError(t, writeGuide(dir, controlplane.SpecAPI().OpenAPI()))

	require.NoFileExists(t, stale)
	require.FileExists(t, usage)
	require.FileExists(t, filepath.Join(dir, "develop", "services", "kv", "spec.md"))
	require.FileExists(t, filepath.Join(dir, filepath.FromSlash(kindref.CommonPage)))
}
