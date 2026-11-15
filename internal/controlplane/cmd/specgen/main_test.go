package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

// writeReference names the reference's sections with the guide's titles and icons in the guide's order, deletes a
// meta.json that kindref no longer returns and the folders it leaves empty, and keeps the Overview, which is written by
// hand (ADR-0331 Decision 3.2).
func TestWriteReferenceRemovesOnlyStaleMetaFiles(t *testing.T) {
	repo := filepath.Join("..", "..", "..", "..")
	sections, err := filepath.Glob(filepath.Join(repo, "docs", "guide", "develop", "*", "meta.json"))
	require.NoError(t, err)
	require.NotEmpty(t, sections)
	root := t.TempDir()
	guide := filepath.Join(root, "docs", "guide")
	for _, meta := range sections {
		body, err := os.ReadFile(meta)
		require.NoError(t, err)
		put(t, filepath.Join(guide, "develop", filepath.Base(filepath.Dir(meta)), "meta.json"), string(body))
	}
	api := filepath.Join(guide, "api")
	stale := filepath.Join(api, "gone", "thing", "meta.json")
	overview := filepath.Join(api, "index.md")
	put(t, stale, `{"title": "Thing", "pages": ["list"]}`)
	put(t, overview, "---\ntitle: Overview\n---\n")

	require.NoError(t, writeReference(guide, controlplane.SpecAPI().OpenAPI()))

	require.NoFileExists(t, stale)
	require.NoDirExists(t, filepath.Join(api, "gone"))
	require.FileExists(t, overview)
	require.FileExists(t, filepath.Join(root, "api", "openapi", "reference-paths.json"))
	services := readMeta(t, filepath.Join(api, "services", "meta.json"))
	require.Equal(t, "Services", services.Title, "the guide's title of the section")
	require.Equal(t, "Database", services.Icon, "the guide's icon of the section")
	develop := readMeta(t, filepath.Join(repo, "docs", "guide", "develop", "meta.json"))
	reference := readMeta(t, filepath.Join(api, "meta.json"))
	require.Equal(t, "index", reference.Pages[0])
	for i, section := range reference.Pages[1:] {
		at := slices.Index(develop.Pages, section)
		require.GreaterOrEqual(t, at, 0, "%s is a section of the guide", section)
		if i > 0 {
			require.Greater(t, at, slices.Index(develop.Pages, reference.Pages[i]), "%s keeps the guide's order", section)
		}
	}

	require.ErrorContains(t, writeReference(t.TempDir(), controlplane.SpecAPI().OpenAPI()), "is not a repository's docs/guide")
}

func put(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// folderMeta is what the tests read of a meta.json.
type folderMeta struct {
	Title, Icon string
	Pages       []string
}

func readMeta(t *testing.T, path string) folderMeta {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	var m folderMeta
	require.NoError(t, json.Unmarshal(body, &m))
	return m
}
