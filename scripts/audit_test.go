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

// The bloat audit (scripts/agent/audit.py) on a throwaway repository: a clean change, whose comment only names
// time.Sleep and //nolint, passes; a production time.Sleep or bare <-time.After, a //nolint with no reason, a new
// direct dependency, and a function copied within its package (golangci-lint's dupl, in the root module and in a
// nested one) fail it; a copy into another package and a promoted indirect dependency are reported; audit-allow
// lines waive the flags.
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
	gomod := "module example.com/audit\n\ngo 1.26\n\nreplace example.com/dep => ./dep\n\nreplace example.com/ind => ./ind\n\n"
	commit("main", "base", map[string]string{
		"go.mod":       gomod + "require example.com/ind v0.0.0 // indirect\n",
		"dep/go.mod":   "module example.com/dep\n\ngo 1.26\n",
		"ind/go.mod":   "module example.com/ind\n\ngo 1.26\n",
		"a/a.go":       "package a\n" + histogram,
		"tools/go.mod": "module example.com/tools\n\ngo 1.26\n",
		"tools/a.go":   "package tools\n" + histogram,
	})
	commit("clean", "fix: add a helper", map[string]string{"a/sum.go": "package a\n\n" +
		"// Sum never needs time.Sleep(d) or a //nolint.\nfunc Sum(xs []int) (n int) {\n\tfor _, x := range xs {\n\t\tn += x\n\t}\n\treturn n\n}\n"})
	bad := map[string]string{
		"go.mod": gomod + "require (\n\texample.com/dep v0.0.0\n\texample.com/ind v0.0.0\n)\n",
		"b/b.go": "package b\n\nimport \"time\"\n\nfunc Wait() { time.Sleep(time.Second) }\n\n" +
			"func Pause(d time.Duration) {\n\t<-time.After(d)\n}\n\nvar Limit = 3 //nolint\n" + histogram,
		"a/copy.go": "package a\n" + strings.Replace(histogram, "Histogram", "Spread", 1),
	}
	commit("bad", "fix: copy and sleep", bad)
	commit("waived", "fix: copy and sleep\n\naudit-allow: dupl a fixture\naudit-allow: sleep:b/b.go a fixture\n"+
		"audit-allow: nolint:b/b.go a fixture\naudit-allow: dep:example.com/dep a fixture", bad)
	commit("nested", "fix: copy in a nested module", map[string]string{
		"tools/copy.go": "package tools\n" + strings.Replace(histogram, "Histogram", "Spread", 1),
	})
	audit := func(head string) (string, int) {
		return run("python3", script, "--base", "main", "--head", head, "--golangci-lint", strings.TrimSpace(string(linter)))
	}

	out, code := audit("clean")
	require.Zero(t, code, out)
	require.Contains(t, out, "**PASS**: 0 hard flags")
	require.Contains(t, out, "### Masking patterns (added lines)\nnone\n")

	out, code = audit("bad")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "`sleep:b/b.go` `b/b.go:5`")
	require.Contains(t, out, "`sleep:b/b.go` `b/b.go:8`")
	require.Contains(t, out, "`nolint:b/b.go` `b/b.go:11`")
	require.Contains(t, out, "`dep:example.com/dep` `go.mod:")
	require.Contains(t, out, "- promoted from indirect: `example.com/ind` `go.mod:")
	require.NotContains(t, out, "`dep:example.com/ind`")
	require.Contains(t, out, "`dupl:a/copy.go` `a/copy.go:")
	require.Regexp(t, "### Cross-package clones \\(production\\)\n(- .*\n)*- `b/b.go:\\d+-\\d+` ≈ `a/a.go:", out)

	out, code = audit("waived")
	require.Zero(t, code, out)
	require.Contains(t, out, "[waived: a fixture]")

	out, code = audit("nested")
	require.Equal(t, 1, code, out)
	require.Regexp(t, "`dupl:tools/copy.go` `tools/copy.go:\\d+-\\d+` duplicates tools/a.go:", out)
}
