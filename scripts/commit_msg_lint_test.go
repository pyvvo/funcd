package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// lintMsg runs scripts/commit-msg-lint.sh on a commit message and returns its output.
func lintMsg(t *testing.T, msg string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "msg")
	require.NoError(t, os.WriteFile(path, []byte(msg), 0o600))
	out, err := exec.Command("bash", "commit-msg-lint.sh", path).CombinedOutput()
	return string(out), err
}

// Issue 248: the commit-msg lint reads only the subject line, so a long body cannot fail it.
func TestIssue248_CommitMsgLintIgnoresBodyLength(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("| a table row that a PR body carries into the squash message |\n", 20000)
	lint := func(subject string) (string, error) { return lintMsg(t, "# a comment\n\n"+subject+"\n\n"+body) }
	for range 5 {
		out, err := lint("fix(ci): a valid subject")
		require.NoError(t, err, "a valid subject passes whatever the body: %s", out)
	}
	out, err := lint("not a conventional subject")
	require.Error(t, err)
	require.Contains(t, out, "is not a Conventional Commit")
	require.Contains(t, out, "\"not a conventional subject\"\n", "the error quotes the subject line alone")
}

// A squash merge makes the PR title the commit subject, and the merge queue checks it with the commit-msg lint. The
// lint accepts a scope list such as "funcdctl,funclog", as the PR-title action does; it stays stricter on empty or
// malformed scopes, which the PR-title workflow now catches on the PR by running the same lint on the title.
func TestCommitMsgLintAcceptsAScopeList(t *testing.T) {
	t.Parallel()
	lint := func(subject string) (string, error) { return lintMsg(t, subject+"\n") }
	for _, ok := range []string{
		"refactor(funcdctl,funclog): split the dev watcher and CompactOnce (#559) (#590)",
		"fix(cli, observability): a scope list with a space",
		"feat(api)!: a breaking change",
	} {
		out, err := lint(ok)
		require.NoError(t, err, "%q: %s", ok, out)
	}
	for _, bad := range []string{"fix(): an empty scope", "fix(,): no scope names", "fix(a,): a trailing comma"} {
		_, err := lint(bad)
		require.Error(t, err, "%q", bad)
	}
}

// The PR-title workflow runs the commit-msg lint on the title, so a title the merge queue would refuse fails on the PR.
// It runs on pull_request_target, which holds a token: the lint must use the default branch's script (a checkout with
// no ref or repository, credentials not persisted), no step may check out the PR's head, and the title must reach the
// shell from an env var, never from an expression expanded into the script.
func TestPRTitleWorkflowLintsTheTitleSafely(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "pr-title.yml"))
	require.NoError(t, err)
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				If   string            `yaml:"if"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &wf))
	var checkout, lint bool
	for _, step := range wf.Jobs["pr-title"].Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			for _, key := range []string{"ref", "repository"} {
				require.NotContains(t, step.With[key], "pull_request.head", "no step checks out the PR's code")
			}
		}
		if !strings.Contains(step.If, "pull_request_target") {
			continue
		}
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			checkout = true
			require.NotContains(t, step.With, "ref", "the title lint runs the default branch's script, not the PR's")
			require.NotContains(t, step.With, "repository", "the title lint runs this repository's script, not a fork's")
			require.Equal(t, "false", step.With["persist-credentials"], "the checkout keeps no token on disk")
		}
		if strings.Contains(step.Run, "commit-msg-lint.sh") {
			lint = true
			require.NotContains(t, step.Run, "${{", "the title reaches the shell as data, through an env var")
			require.Equal(t, "${{ github.event.pull_request.title }}", step.Env["TITLE"])
		}
	}
	require.True(t, checkout, "the PR job checks out the default branch for the lint")
	require.True(t, lint, "the PR job runs scripts/commit-msg-lint.sh on the title")
}
