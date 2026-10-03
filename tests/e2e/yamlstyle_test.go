package e2e_test

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	var flow []string
	for _, f := range trackedFiles(t, root, "*.yml", "*.yaml") {
		data, err := os.ReadFile(filepath.Join(root, f))
		require.NoError(t, err)
		hits, err := flowInYAML(f, data)
		require.NoError(t, err, "parse %s", f)
		flow = append(flow, hits...)
	}
	require.Empty(t, flow, "flow-style YAML; write these in block style")
}

// Issue 500: the same rule holds for YAML a Go test writes from a literal — a map entry keyed by a
// *.yaml/*.yml file name, the way the funcdctl dev tests lay out a project. A literal that is not
// valid YAML (an error-path fixture) is not style-checked.
func TestIssue500_GoTestYAMLLiteralsAreBlockStyle(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var flow []string
	for _, f := range trackedFiles(t, root, "*_test.go") {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(root, f), nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			name, ok := stringConst(kv.Key)
			if !ok || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
				return true
			}
			body, ok := stringConst(kv.Value)
			if !ok {
				return true
			}
			label := fmt.Sprintf("%s:%d %s", f, fset.Position(kv.Pos()).Line, name)
			if hits, err := flowInYAML(label, []byte(body)); err == nil {
				flow = append(flow, hits...)
			}
			return true
		})
	}
	require.Empty(t, flow, "flow-style YAML in a Go test literal; write these in block style")
}

func trackedFiles(t *testing.T, root string, patterns ...string) []string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"ls-files", "--"}, patterns...)...)
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err, "git ls-files")
	files := strings.Fields(string(out))
	require.NotEmpty(t, files)
	return files
}

// flowInYAML lists the non-empty flow nodes of every document in data, labelled label:<line>.
func flowInYAML(label string, data []byte) ([]string, error) {
	var hits []string
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return hits, nil
		}
		if err != nil {
			return nil, err
		}
		hits = append(hits, flowNodes(label, &doc)...)
	}
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

// stringConst is the value of a string literal or a + chain of them; any other expression is not one.
func stringConst(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, ok := stringConst(e.X)
		if !ok {
			return "", false
		}
		r, ok := stringConst(e.Y)
		return l + r, ok
	}
	return "", false
}
