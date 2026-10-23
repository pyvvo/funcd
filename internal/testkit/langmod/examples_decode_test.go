package langmod

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/pyvvo/funcd/pkg/sdk"
)

// backupPlaceholder is the token a lane replaces with a Backup's name before it applies a Restore (funcd-functions
// apps/todo/dr, apps/todo/sql).
var backupPlaceholder = regexp.MustCompile(`\bBACKUP\b`)

// Every funcd resource of the pinned example modules decodes strictly and passes Validate, so an example that no lane
// or test applies still fails here when the API moves on (#1078). An App template's files are fragments its App
// renders, so they are left to the template tests.
func TestExampleResourcesDecode(t *testing.T) {
	t.Parallel()
	// The fixtures a lane applies to see them refused, with the error each must fail with: they still decode, and their
	// Validate keeps failing for that reason.
	refusedByDesign := map[string]string{
		"apps/volumes/fixtures/two-replicas.yaml": "a local volume allows at most 1 replica",
	}
	for _, mod := range []string{TypeScript, Python, Functions} {
		root := Dir(t, mod)
		decoded := 0
		require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != root && (d.Name()[0] == '.' || d.Name() == "node_modules" || isTemplate(path)) {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".yaml" {
				return nil
			}
			data, err := os.ReadFile(path) //nolint:gosec // a file of a pinned module
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			for _, doc := range resourceDocs(t, rel, data) {
				obj, derr := sdk.DecodeManifest(backupPlaceholder.ReplaceAll(doc, []byte("backup-0")))
				require.NoError(t, derr, "%s %s", mod, rel)
				if want, ok := refusedByDesign[filepath.ToSlash(rel)]; ok && mod == Functions {
					require.ErrorContains(t, obj.Validate(), want, "%s %s is refused by design", mod, rel)
				} else {
					require.NoError(t, obj.Validate(), "%s %s", mod, rel)
				}
				decoded++
			}
			return nil
		}))
		require.NotZero(t, decoded, "%s holds no funcd resource", mod)
	}
}

// isTemplate reports whether dir is an App template, which holds app.yaml beside its resources (ADR-0217).
func isTemplate(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "app.yaml"))
	return err == nil
}

// resourceDocs returns the YAML documents of data that are funcd resources: apiVersion funcd.io/v1alpha1, except a
// daemon's FuncdConfig.
func resourceDocs(t *testing.T, rel string, data []byte) [][]byte {
	t.Helper()
	dec := yamlv3.NewDecoder(bytes.NewReader(data))
	var docs [][]byte
	for {
		var node yamlv3.Node
		if err := dec.Decode(&node); errors.Is(err, io.EOF) {
			return docs
		} else if err != nil {
			require.NoError(t, err, "parse %s", rel)
		}
		var head struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
		}
		if node.Decode(&head) != nil || head.APIVersion != "funcd.io/v1alpha1" || head.Kind == "FuncdConfig" {
			continue
		}
		out, err := yamlv3.Marshal(&node)
		require.NoError(t, err, "re-encode %s", rel)
		docs = append(docs, out)
	}
}
