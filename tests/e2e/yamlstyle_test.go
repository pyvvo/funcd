package e2e_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v3"
)

// Issue 315: the house style (CLAUDE.md) is block-style YAML, so no tracked YAML file holds a
// non-empty flow mapping `{ k: v }` or flow sequence `[a, b]`. An empty `{}`/`[]` has no block form.
func TestIssue315_TrackedYAMLIsBlockStyle(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	cmd := exec.Command("git", "ls-files", "--", "*.yml", "*.yaml")
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err, "git ls-files")
	files := strings.Fields(string(out))
	require.NotEmpty(t, files)

	var flow []string
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		require.NoError(t, err)
		dec := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var doc yaml.Node
			err := dec.Decode(&doc)
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err, "parse %s", f)
			flow = append(flow, flowNodes(f, &doc)...)
		}
	}
	require.Empty(t, flow, "flow-style YAML; write these in block style")
}

func flowNodes(file string, n *yaml.Node) []string {
	var hits []string
	if (n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode) && n.Style&yaml.FlowStyle != 0 && len(n.Content) > 0 {
		return []string{fmt.Sprintf("%s:%d", file, n.Line)}
	}
	for _, c := range n.Content {
		hits = append(hits, flowNodes(file, c)...)
	}
	return hits
}
