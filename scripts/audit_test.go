package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const histogram = `
func Histogram(xs []int, width int) map[int]int {
	if width <= 0 {
		width = 1
	}
	out := make(map[int]int)
	for _, x := range xs {
		if x < 0 {
			x = -x
		}
		bucket := x / width
		if bucket > 10 {
			bucket = 10
		}
		out[bucket]++
	}
	for k, v := range out {
		if v == 0 {
			delete(out, k)
		}
	}
	return out
}
`

// The bloat audit (scripts/agent/audit.py) on a throwaway repository: a clean change passes; a production
// time.Sleep and a function copied within its package (golangci-lint's dupl) fail it, and a copy into another
// package is reported; audit-allow lines in a commit message waive the flags.
func TestBloatAudit(t *testing.T) {
	t.Parallel()
	linter, err := exec.Command("go", "tool", "-n", "golangci-lint").Output()
	require.NoError(t, err)
	script, err := filepath.Abs(filepath.Join("agent", "audit.py"))
	require.NoError(t, err)
	repo := t.TempDir()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=audit", "GIT_AUTHOR_EMAIL=", "GIT_COMMITTER_NAME=audit", "GIT_COMMITTER_EMAIL=")
	run := func(name string, args ...string) (string, int) {
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env = repo, env
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode()
		}
		require.NoError(t, err, string(out))
		return string(out), 0
	}
	git := func(args ...string) {
		out, code := run("git", args...)
		require.Zero(t, code, out)
	}
	commit := func(branch, msg string, files map[string]string) {
		if branch != "main" {
			git("checkout", "-q", "-b", branch, "main")
		}
		for path, body := range files {
			require.NoError(t, os.MkdirAll(filepath.Join(repo, filepath.Dir(path)), 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(repo, path), []byte(body), 0o600))
		}
		git("add", "-A")
		git("commit", "-q", "-m", msg)
	}
	git("init", "-q", "-b", "main")
	commit("main", "base", map[string]string{"go.mod": "module example.com/audit\n\ngo 1.26\n", "a/a.go": "package a\n" + histogram})
	commit("clean", "fix: add a helper", map[string]string{"a/sum.go": "package a\n\nfunc Sum(xs []int) (n int) {\n\tfor _, x := range xs {\n\t\tn += x\n\t}\n\treturn n\n}\n"})
	bad := map[string]string{
		"b/b.go":    "package b\n\nimport \"time\"\n\nfunc Wait() { time.Sleep(time.Second) }\n" + histogram,
		"a/copy.go": "package a\n" + strings.Replace(histogram, "Histogram", "Spread", 1),
	}
	commit("bad", "fix: copy and sleep", bad)
	commit("waived", "fix: copy and sleep\n\naudit-allow: dupl a fixture\naudit-allow: sleep:b/b.go a fixture", bad)
	audit := func(head string) (string, int) {
		return run("python3", script, "--base", "main", "--head", head, "--golangci-lint", strings.TrimSpace(string(linter)))
	}

	out, code := audit("clean")
	require.Zero(t, code, out)
	require.Contains(t, out, "**PASS**: 0 hard flags")

	out, code = audit("bad")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "`sleep:b/b.go` `b/b.go:5`")
	require.Contains(t, out, "`dupl:a/copy.go` `a/copy.go:")
	require.Regexp(t, "### Cross-package clones \\(production\\)\n(- .*\n)*- `b/b.go:\\d+-\\d+` ≈ `a/a.go:", out)

	out, code = audit("waived")
	require.Zero(t, code, out)
	require.Contains(t, out, "[waived: a fixture]")
}
