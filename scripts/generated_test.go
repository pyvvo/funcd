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
	page := "docs/guide/develop/services/kv/spec.md"
	dir := scratchRepo(t, map[string]string{page: "as generated\n", "api/openapi/funcd.v1alpha1.yaml": "spec\n"})
	out, err := runCheck(t, dir, "check-generated.sh")
	require.NoError(t, err, out)

	putFile(t, dir, page, "as regenerated\n")
	out, err = runCheck(t, dir, "check-generated.sh")
	require.Error(t, err)
	require.Contains(t, out, page)
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
