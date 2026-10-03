package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Issue 248: the commit-msg lint reads only the subject line, so a long body cannot fail it.
func TestIssue248_CommitMsgLintIgnoresBodyLength(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("| a table row that a PR body carries into the squash message |\n", 20000)
	lint := func(subject string) (string, error) {
		msg := filepath.Join(t.TempDir(), "msg")
		require.NoError(t, os.WriteFile(msg, []byte("# a comment\n\n"+subject+"\n\n"+body), 0o600))
		out, err := exec.Command("bash", "commit-msg-lint.sh", msg).CombinedOutput()
		return string(out), err
	}
	for range 5 {
		out, err := lint("fix(ci): a valid subject")
		require.NoError(t, err, "a valid subject passes whatever the body: %s", out)
	}
	out, err := lint("not a conventional subject")
	require.Error(t, err)
	require.Contains(t, out, "is not a Conventional Commit")
	require.Contains(t, out, "\"not a conventional subject\"\n", "the error quotes the subject line alone")
}

// The pull-request title check accepts a scope list such as "funcdctl,funclog", and a squash merge makes that title
// the commit subject. The commit-msg lint, which checks the merge group, must accept the same subjects, or a PR that
// passed every check leaves the merge queue on its own title.
func TestCommitMsgLintAcceptsAScopeList(t *testing.T) {
	t.Parallel()
	lint := func(subject string) (string, error) {
		msg := filepath.Join(t.TempDir(), "msg")
		require.NoError(t, os.WriteFile(msg, []byte(subject+"\n"), 0o600))
		out, err := exec.Command("bash", "commit-msg-lint.sh", msg).CombinedOutput()
		return string(out), err
	}
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
