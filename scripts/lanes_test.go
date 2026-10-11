package scripts_test

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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

// A fresh clone builds the runtime images and runs the lanes the same way on any host on any day only when every
// external input is read at a pinned value: an image is named by its digest, pip installs hashed requirements, a
// download is checked against a recorded checksum, a VM image is a dated build with its digest, apt installs named
// versions from the Debian snapshot archive at one fixed time, and a VM does not take the host's time zone. One image
// tag names one digest everywhere.
func TestLaneInputsArePinned(t *testing.T) {
	pins := imagePins{}
	problems := dockerfileProblems(t, pins)
	problems = append(problems, vmTemplateProblems(t)...)
	problems = append(problems, laneBuildProblems(t, pins)...)
	problems = append(problems, pins.conflicts()...)
	sort.Strings(problems)
	require.Empty(t, problems, "unpinned lane inputs:\n%s", strings.Join(problems, "\n"))
}

// dockerfileProblems checks the runtime images' Dockerfiles and their pip requirements.
func dockerfileProblems(t *testing.T, pins imagePins) []string {
	t.Helper()
	dockerfiles, err := filepath.Glob("../images/runtime/*/Dockerfile")
	require.NoError(t, err)
	require.NotEmpty(t, dockerfiles)
	var problems []string
	for _, path := range dockerfiles {
		rel := strings.TrimPrefix(path, "../")
		lines := logicalLines(readText(t, path))
		problems = append(problems, shellProblems(rel, lines)...)
		stages := map[string]bool{}
		for i, l := range lines {
			where := fmt.Sprintf("%s:%d", rel, l.n)
			f := strings.Fields(l.text)
			switch strings.ToUpper(f[0]) {
			case "FROM":
				args := slices.DeleteFunc(f[1:], func(a string) bool { return strings.HasPrefix(a, "--") })
				if !stages[strings.ToLower(args[0])] {
					problems = append(problems, pins.record(where, args[0])...)
				}
				if len(args) == 3 && strings.EqualFold(args[1], "AS") {
					stages[strings.ToLower(args[2])] = true
				}
			case "ADD":
				if strings.Contains(l.text, "://") && !strings.Contains(l.text, "--checksum=sha256:") {
					problems = append(problems, where+": ADD downloads a URL without --checksum")
				}
			case "RUN":
				if regexp.MustCompile(`\bINSTALL\b`).MatchString(l.text) && !verified(lines[i:]) {
					problems = append(problems, where+": DuckDB INSTALL downloads extensions that no sha256sum -c checks")
				}
			}
		}
	}
	requirements, err := filepath.Glob("../images/runtime/*/requirements*.txt")
	require.NoError(t, err)
	hashed := regexp.MustCompile(`==\S+.*--hash=sha256:[0-9a-f]{64}`)
	for _, path := range requirements {
		for _, l := range logicalLines(readText(t, path)) {
			if !hashed.MatchString(l.text) {
				problems = append(problems, fmt.Sprintf("%s:%d: %s is not pinned to a version and a hash",
					strings.TrimPrefix(path, "../"), l.n, strings.Fields(l.text)[0]))
			}
		}
	}
	return problems
}

// vmTemplateProblems checks the Lima templates, the scripts they read in and the scripts the lane VMs run.
func vmTemplateProblems(t *testing.T) []string {
	t.Helper()
	type template struct {
		Base     string `yaml:"base"`
		Timezone string `yaml:"timezone"`
		Images   []struct {
			Location string `yaml:"location"`
			Digest   string `yaml:"digest"`
		} `yaml:"images"`
		Provision []struct {
			Mode   string `yaml:"mode"`
			Script string `yaml:"script"`
			File   string `yaml:"file"`
		} `yaml:"provision"`
		Probes []struct {
			Script string `yaml:"script"`
		} `yaml:"probes"`
	}
	templates := map[string]template{}
	for _, name := range []string{"lima.yaml", "lima-lane.yaml", "lima-linux-tests.yaml"} {
		var tmpl template
		require.NoError(t, yaml.Unmarshal([]byte(readText(t, name)), &tmpl), name)
		templates[name] = tmpl
	}
	// The scripts a lane VM runs, or a template reads in with file:, each checked once.
	scripts := map[string]bool{"lane-provision.sh": true, "lane-ready.sh": true, "lane-reset.sh": true,
		"lane-leakcheck.sh": true, "linux-exec.sh": true}
	dated := regexp.MustCompile(`^sha(256|512):[0-9a-f]+$`)
	moving := regexp.MustCompile(`latest|/current/|/daily/`)
	frozen := regexp.MustCompile(`snapshot\.debian\.org/archive/(?s:.*)\b\d{8}T\d{6}Z\b|\b\d{8}T\d{6}Z\b(?s:.*)snapshot\.debian\.org/archive/`)
	var problems []string
	for name, tmpl := range templates {
		for _, img := range tmpl.Images {
			if !dated.MatchString(img.Digest) || moving.MatchString(img.Location) {
				problems = append(problems, fmt.Sprintf("scripts/%s: the VM image %s is not a dated build pinned by digest", name, img.Location))
			}
		}
		frozenApt, timezone := false, false
		for k, tm := range []template{tmpl, templates[tmpl.Base]} {
			timezone = timezone || tm.Timezone != ""
			for _, p := range tm.Provision {
				script := p.Script
				if p.File != "" {
					script = readText(t, p.File)
					scripts[p.File] = true
				} else if k == 0 {
					problems = append(problems, shellProblems("scripts/"+name+" provision", logicalLines(script))...)
				}
				frozenApt = frozenApt || p.Mode == "dependency" && frozen.MatchString(script)
			}
		}
		if !frozenApt {
			problems = append(problems, fmt.Sprintf("scripts/%s: apt in the VM reads the live Debian archive, not a snapshot at a fixed time (a mode: dependency provision step)", name))
		}
		if !timezone {
			problems = append(problems, fmt.Sprintf("scripts/%s: the VM takes the host's time zone", name))
		}
		for _, p := range tmpl.Probes {
			problems = append(problems, shellProblems("scripts/"+name+" probe", logicalLines(p.Script))...)
		}
	}
	for name := range scripts {
		problems = append(problems, shellProblems("scripts/"+name, logicalLines(readText(t, name)))...)
	}
	return problems
}

// laneBuildProblems checks the build commands of every lane in scripts/lanes.yaml.
func laneBuildProblems(t *testing.T, pins imagePins) []string {
	t.Helper()
	var registry map[string]struct {
		Build []string `yaml:"build"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(readText(t, "lanes.yaml")), &registry))
	var problems []string
	for name, lane := range registry {
		for i, cmd := range lane.Build {
			where := fmt.Sprintf("scripts/lanes.yaml %s build[%d]", name, i)
			if regexp.MustCompile(`\buv\s+(run|sync)\b`).MatchString(cmd) && !regexp.MustCompile(`--(locked|frozen)\b`).MatchString(cmd) {
				problems = append(problems, where+": uv resolves without --locked or --frozen")
			}
			for _, m := range regexp.MustCompile(`\bdocker\s+pull\s+(?:-\S+\s+)*(\S+)`).FindAllStringSubmatch(cmd, -1) {
				problems = append(problems, pins.record(where, m[1])...)
			}
			problems = append(problems, shellProblems(where, logicalLines(cmd))...)
		}
	}
	return problems
}

// imagePins maps an image name and tag to the digest-pinned references that name it.
type imagePins map[string]map[string]bool

// record notes ref, or reports it when it names no digest.
func (p imagePins) record(where, ref string) []string {
	m := regexp.MustCompile(`^([^@\s]+)@sha256:[0-9a-f]{64}$`).FindStringSubmatch(ref)
	if m == nil {
		return []string{fmt.Sprintf("%s: the image %s names no sha256 digest", where, ref)}
	}
	if p[m[1]] == nil {
		p[m[1]] = map[string]bool{}
	}
	p[m[1]][ref] = true
	return nil
}

// conflicts reports each image pinned at more than one digest.
func (p imagePins) conflicts() []string {
	var out []string
	for name, refs := range p {
		if len(refs) > 1 {
			out = append(out, fmt.Sprintf("the image %s is pinned at %d digests: %v", name, len(refs), slices.Sorted(maps.Keys(refs))))
		}
	}
	return out
}

// readText returns a file's contents.
func readText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(raw)
}

// line is one logical line of a script: backslash continuations joined, with the number of its first line.
type line struct {
	n    int
	text string
}

// logicalLines joins backslash-continued lines and drops blank and comment lines, as a shell or a Dockerfile reads them.
func logicalLines(text string) []line {
	var out []line
	var cur line
	for i, l := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if cur.text == "" {
			cur.n = i + 1
		}
		if strings.HasSuffix(trimmed, `\`) {
			cur.text += strings.TrimSuffix(trimmed, `\`) + " "
			continue
		}
		cur.text += trimmed
		out = append(out, cur)
		cur = line{}
	}
	if cur.text != "" {
		out = append(out, cur)
	}
	return out
}

// verified reports whether a checksum check (sha256sum -c and its kin) runs on the first line or a later one.
func verified(lines []line) bool {
	check := regexp.MustCompile(`\b(sha256sum|sha512sum)\b[^|;&]*\s-c\b|\bshasum\s+-a\s+(256|512)\b[^|;&]*\s-c\b`)
	return slices.ContainsFunc(lines, func(l line) bool { return check.MatchString(l.text) })
}

// shellProblems reports the unpinned installs and downloads of a script's lines: an apt install of a package at no
// named version, pip reading an index without --require-hashes (with --no-index it installs a local file, which the
// download rule covers), npm install instead of npm ci, and a curl or wget of anything but this host with no checksum
// check before the next one.
func shellProblems(where string, lines []line) []string {
	fetch := regexp.MustCompile(`\b(?:curl|wget)\s`)
	local := regexp.MustCompile(`https?://(127\.0\.0\.1|localhost)\b`)
	var out []string
	for i, l := range lines {
		at := fmt.Sprintf("%s:%d", where, l.n)
		for _, m := range regexp.MustCompile(`\bapt(?:-get)?\s+install\s+([^;&|)}]*)`).FindAllStringSubmatch(l.text, -1) {
			for _, pkg := range strings.Fields(m[1]) {
				if !strings.HasPrefix(pkg, "-") && !strings.Contains(pkg, "=") {
					out = append(out, fmt.Sprintf("%s: apt installs %s at no named version", at, pkg))
				}
			}
		}
		if regexp.MustCompile(`\bpip3?\s+install\b`).MatchString(l.text) && !strings.Contains(l.text, "--require-hashes") &&
			!strings.Contains(l.text, "--no-index") {
			out = append(out, at+": pip installs from an index without --require-hashes")
		}
		if regexp.MustCompile(`\bnpm\s+(install|i)\b`).MatchString(l.text) {
			out = append(out, at+": npm install resolves; npm ci installs the lockfile")
		}
		if fetch.MatchString(l.text) && !local.MatchString(l.text) {
			next := i + 1
			for next < len(lines) && !fetch.MatchString(lines[next].text) {
				next++
			}
			if !verified(lines[i:next]) {
				out = append(out, at+": a download with no checksum check before the next one")
			}
		}
	}
	return out
}
