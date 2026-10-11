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

	"github.com/pyvvo/funcd/internal/testkit/langmod"
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

// The funcd-linux-tests VM boots the lanes' image, so the cache lima-cache.py primes serves it too, and its one port
// rule ignores every guest port, so a test listening in the VM never takes a host port, such as a lane VM's 8080 or
// 8081. The rule must set guestIPMustBeZero to false: with guestIP 0.0.0.0 Lima defaults it to true, the rule then
// matches only a bind on 0.0.0.0 or ::, and Lima's built-in rule forwards a bind on 127.0.0.1 to the host.
func TestLinuxTestsVMBootsTheLaneImageAndIgnoresEveryGuestPort(t *testing.T) {
	type rule struct {
		GuestIP           string `yaml:"guestIP"`
		GuestIPMustBeZero *bool  `yaml:"guestIPMustBeZero"`
		GuestPortRange    []int  `yaml:"guestPortRange"`
		Proto             string `yaml:"proto"`
		Ignore            bool   `yaml:"ignore"`
	}
	type vm struct {
		Images       []map[string]string `yaml:"images"`
		PortForwards []rule              `yaml:"portForwards"`
	}
	read := func(name string) vm {
		raw, err := os.ReadFile(name)
		require.NoError(t, err)
		var v vm
		require.NoError(t, yaml.Unmarshal(raw, &v))
		return v
	}
	lane, tests := read("lima.yaml"), read("lima-linux-tests.yaml")
	require.NotEmpty(t, lane.Images)
	require.Equal(t, lane.Images, tests.Images)
	anyIP := false
	require.Equal(t, []rule{{GuestIP: "0.0.0.0", GuestIPMustBeZero: &anyIP, GuestPortRange: []int{1, 65535}, Proto: "any",
		Ignore: true}}, tests.PortForwards)
}

// ADR-0230: the lane template and `just lima-example-all`'s shared VM run the same provision, readiness, reset and
// leak-check scripts; each must at least parse.
func TestLaneVMScriptsParse(t *testing.T) {
	for _, script := range []string{"lane-provision.sh", "lane-ready.sh", "lane-reset.sh", "lane-leakcheck.sh"} {
		out, err := exec.Command("bash", "-n", script).CombinedOutput()
		require.NoError(t, err, "bash -n %s: %s", script, out)
	}
}

// ADR-0230: the lane template keeps no provision or readiness code of its own; it reads the shared scripts in, so a
// single-lane VM and the shared VM deploy and probe a lane the same way. Lima 2.1 reads no `file:` of a probe, so the
// readiness script is a data file that the probe runs.
func TestLaneTemplateRunsTheSharedScripts(t *testing.T) {
	raw, err := os.ReadFile("lima-lane.yaml")
	require.NoError(t, err)
	var tmpl struct {
		Provision []struct {
			Mode   string `yaml:"mode"`
			File   string `yaml:"file"`
			Path   string `yaml:"path"`
			Script string `yaml:"script"`
		} `yaml:"provision"`
		Probes []struct {
			Script string `yaml:"script"`
		} `yaml:"probes"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &tmpl))
	files := map[string]string{}
	for _, p := range tmpl.Provision {
		require.Empty(t, p.Script, "a provision step of the template reads a shared script with file:")
		require.FileExists(t, p.File, "the template reads %s from beside it", p.File)
		files[p.Mode+" "+p.File] = p.Path
	}
	require.Contains(t, files, "system lane-provision.sh")
	ready, ok := files["data lane-ready.sh"]
	require.True(t, ok, "the template writes lane-ready.sh into the guest: %v", files)
	require.Len(t, tmpl.Probes, 1)
	require.Contains(t, tmpl.Probes[0].Script, "exec "+ready)
}

// The reset of the shared lane VM (scripts/lane-reset.sh) removes /var/lib/funcd/data whole, which is a lane's own
// data only because every lane config sets that dataDir (ADR-0230); funcd's default is /var/lib/funcd.
func TestEveryLaneConfigSetsTheResetDataDir(t *testing.T) {
	raw, err := os.ReadFile("lanes.yaml")
	require.NoError(t, err)
	var registry map[string]struct {
		Module string      `yaml:"module"`
		Dir    string      `yaml:"dir"`
		Stage  []yaml.Node `yaml:"stage"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &registry))
	root := func(module string) string {
		if module == "" {
			return ".."
		}
		return langmod.Dir(t, module)
	}
	require.NotEmpty(t, registry)
	for name, lane := range registry {
		config := ""
		for _, node := range lane.Stage {
			if node.Kind == yaml.ScalarNode {
				if node.Value == "funcdconfig.yaml" {
					config = filepath.Join(root(lane.Module), lane.Dir, node.Value)
				}
				continue
			}
			var entry struct{ From, Module, To string }
			require.NoError(t, node.Decode(&entry), "lane %s", name)
			if entry.To == "funcdconfig.yaml" {
				config = filepath.Join(root(entry.Module), entry.From)
			}
		}
		require.NotEmpty(t, config, "lane %s stages a funcdconfig.yaml", name)
		raw, err := os.ReadFile(config)
		require.NoError(t, err, "lane %s", name)
		var cfg struct {
			Storage struct {
				DataDir string `yaml:"dataDir"`
			} `yaml:"storage"`
		}
		require.NoError(t, yaml.Unmarshal(raw, &cfg), "lane %s", name)
		require.Equal(t, "/var/lib/funcd/data", cfg.Storage.DataDir, "lane %s: %s", name, config)
	}
}
