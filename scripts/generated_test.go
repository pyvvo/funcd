package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// scenario: stale-generated-file-fails-ci
func TestScenarioStaleGeneratedFileFailsCI(t *testing.T) {
	generated := []string{"docs/guide/develop/services/kv/spec.md", "api/openapi/reference-paths.json",
		"docs/guide/api/meta.json", "docs/guide/api/services/kvstore/meta.json"}
	files := map[string]string{"api/openapi/funcd.v1alpha1.yaml": "spec\n", "docs/guide/api/index.md": "by hand\n"}
	for _, name := range generated {
		files[name] = "as generated\n"
	}
	dir := scratchRepo(t, files)
	out, err := runCheck(t, dir, "check-generated.sh")
	require.NoError(t, err, out)

	for _, name := range generated {
		putFile(t, dir, name, "as regenerated\n")
		out, err = runCheck(t, dir, "check-generated.sh")
		require.Error(t, err, name)
		require.Contains(t, out, name)
		putFile(t, dir, name, "as generated\n")
	}
	putFile(t, dir, "docs/guide/api/workloads/function/meta.json", "new\n")
	out, err = runCheck(t, dir, "check-generated.sh")
	require.Error(t, err, "a new folder of the reference is not committed")
	require.Contains(t, out, "docs/guide/api/workloads/")
}

// scratchRepo returns a repository with one commit that holds files.
func scratchRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		putFile(t, dir, name, body)
	}
	gitOut(t, dir, "init", "-q")
	gitOut(t, dir, "add", ".")
	gitOut(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "base")
	return dir
}

func putFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

// runCheck runs one of these scripts in dir and returns what it printed.
func runCheck(t *testing.T, dir, script string, args ...string) (string, error) {
	t.Helper()
	path, err := filepath.Abs(script)
	require.NoError(t, err)
	cmd := exec.Command("bash", append([]string{path}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
