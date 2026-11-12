package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	undescribedList       = "internal/controlplane/kindref/testdata/undescribed.txt"
	undescribedConfigList = "internal/reference/testdata/undescribed-config.txt"
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

// scenario: undescribed-list-never-grows
func TestScenarioUndescribedListNeverGrows(t *testing.T) {
	dir := scratchRepo(t, map[string]string{undescribedList: "AppSpec.parts\nFunctionSpec.handler\n"})
	base := gitOut(t, dir, "rev-parse", "HEAD")

	putFile(t, dir, undescribedList, "FunctionSpec.handler\n")
	out, err := runCheck(t, dir, "check-undescribed.sh", base)
	require.NoError(t, err, "a list that shrinks passes: %s", out)

	putFile(t, dir, undescribedList, "AppSpec.parts\nFunctionSpec.handler\nFunctionSpec.newField\n")
	out, err = runCheck(t, dir, "check-undescribed.sh", base)
	require.Error(t, err)
	require.Contains(t, out, "FunctionSpec.newField")

	out, err = runCheck(t, dir, "check-undescribed.sh", strings.Repeat("0", 40))
	require.Error(t, err, "a base it cannot read fails")
	require.Contains(t, out, "cannot read the base commit")

	fresh := scratchRepo(t, map[string]string{"README.md": "no list yet\n"})
	before := gitOut(t, fresh, "rev-parse", "HEAD")
	putFile(t, fresh, undescribedList, "FunctionSpec.handler\n")
	out, err = runCheck(t, fresh, "check-undescribed.sh", before)
	require.NoError(t, err, "the pull request that creates the list passes: %s", out)

	keys := scratchRepo(t, map[string]string{undescribedConfigList: "server.listenAddr\n"})
	keysBase := gitOut(t, keys, "rev-parse", "HEAD")
	putFile(t, keys, undescribedConfigList, "server.listenAddr\nserver.newKey\n")
	out, err = runCheck(t, keys, "check-undescribed.sh", keysBase)
	require.Error(t, err, "the config keys' list may not grow either")
	require.Contains(t, out, "server.newKey")
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
