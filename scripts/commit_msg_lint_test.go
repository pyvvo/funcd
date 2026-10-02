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
