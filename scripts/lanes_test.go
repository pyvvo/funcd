package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The lane tools read scripts/lanes.yaml with PyYAML through the dev shell's python3. When the dev shell pinned a
// Python without PyYAML, `just lima-example-all` listed no lane, ran only the metastore lane and still reported every
// lane passed. lane.py --venom-lanes lists the lanes the registry runs under Venom, from the dev shell's interpreter,
// and leaves out an optional lane, which only `just lima-example <name>` runs.
func TestLaneRegistryListsItsVenomLanes(t *testing.T) {
	raw, err := os.ReadFile("lanes.yaml")
	require.NoError(t, err)
	var registry map[string]struct {
		Venom    string `yaml:"venom"`
		Optional bool   `yaml:"optional"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &registry))
	var want []string
	for name, spec := range registry {
		if spec.Venom != "" && !spec.Optional {
			want = append(want, name)
		}
	}
	require.NotEmpty(t, want, "scripts/lanes.yaml runs lanes under Venom")

	out, err := exec.Command("python3", "lane.py", "--venom-lanes").CombinedOutput()
	require.NoError(t, err, "the dev shell's python3 must run the lane tools: %s", out)
	got := strings.Fields(string(out))
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got)
}

// A lane with static credentials (tokens) runs a daemon that refuses the built-in dev token (ADR-0171), so its Venom
// suite must call funcdctl with a lane token. Two PRs merged in parallel once left one env-echo case on the dev token:
// each PR's own lane run had passed, and the lane failed on main.
func TestCredentialLanesNeverUseTheDevToken(t *testing.T) {
	raw, err := os.ReadFile("lanes.yaml")
	require.NoError(t, err)
	var registry map[string]struct {
		Venom  string            `yaml:"venom"`
		Tokens map[string]string `yaml:"tokens"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &registry))
	checked := 0
	for name, spec := range registry {
		if spec.Venom == "" || len(spec.Tokens) == 0 {
			continue
		}
		suite, err := os.ReadFile(filepath.Join("..", spec.Venom))
		require.NoError(t, err, "lane %s", name)
		for i, line := range strings.Split(string(suite), "\n") {
			require.NotContains(t, line, "funcd-dev-token", "lane %s: %s:%d uses the built-in dev token", name, spec.Venom, i+1)
		}
		checked++
	}
	require.NotZero(t, checked, "a lane runs with static credentials")
}

// lanes.sh checked out every spec at .claude/worktrees/lane-<lane> and first removed whatever was there, so a second
// run for another branch and the same lane deleted the first run's checkout under its running lane ("failed to get
// current directory"). Here the first run's lane starts the second run, then checks that its own checkout survived.
func TestLanesGivesEachRunItsOwnCheckout(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	root := filepath.Join(home, "funcd")
	env := append(os.Environ(), "HOME="+home, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=lanes", "GIT_AUTHOR_EMAIL=", "GIT_COMMITTER_NAME=lanes", "GIT_COMMITTER_EMAIL=")
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}
	write := func(path, body string, mode os.FileMode) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), mode))
	}
	require.NoError(t, os.MkdirAll(root, 0o755))
	git("init", "-q", "-b", "main")
	write(filepath.Join(root, "scripts", "agent", "d"), "#!/bin/sh\nexec \"$@\"\n", 0o755)
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("branch", "fix/one")
	git("branch", "fix/two")
	git("remote", "add", "origin", root)
	src, err := os.ReadFile(filepath.Join("agent", "lanes.sh"))
	require.NoError(t, err)
	lanes := filepath.Join(root, "scripts", "agent", "lanes.sh")
	write(lanes, string(src), 0o755)

	bin := t.TempDir()
	write(filepath.Join(bin, "just"), `#!/bin/sh
if [ -n "${LANES_SECOND:-}" ]; then
  second=$LANES_SECOND
  unset LANES_SECOND
  "$second" fix/two:all
fi
test -f scripts/agent/d || { echo "the checkout of this run is gone"; exit 1; }
`, 0o755)
	cmd := exec.Command(lanes, "fix/one:all")
	cmd.Env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "LANES_SECOND="+lanes)
	code, out := exitCode(t, cmd)
	first, err := os.ReadFile(filepath.Join(root, ".cache", "lanes", "fix_one-all.log"))
	require.NoError(t, err)
	require.Equal(t, 0, code, "%s\nlog of fix/one:\n%s", out, first)
	require.Contains(t, out, "fix/one:all @")
	require.Contains(t, out, "PASS")
	require.Contains(t, string(first), "fix/two:all @", "the second run ran inside the first one")
	require.Contains(t, string(first), "PASS")

	require.Equal(t, 1, strings.Count(git("worktree", "list"), "\n"), "each run removes its own checkout")
	left, err := os.ReadDir(filepath.Join(root, ".claude", "worktrees"))
	require.NoError(t, err)
	require.Empty(t, left, "no checkout directory is left behind")
}
