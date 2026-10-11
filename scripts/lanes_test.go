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

// The patterns of the lane-input guard (TestLaneInputsArePinned).
var (
	pinnedImage      = regexp.MustCompile(`^([^@\s]+)@sha256:[0-9a-f]{64}$`)
	digestForm       = regexp.MustCompile(`^sha(256|512):[0-9a-f]+$`)
	movingLocation   = regexp.MustCompile(`latest|/current/|/daily/`)
	snapshotArchive  = regexp.MustCompile(`snapshot\.debian\.org/archive/`)
	snapshotTime     = regexp.MustCompile(`\b\d{8}T\d{6}Z\b`)
	duckdbInstall    = regexp.MustCompile(`\bINSTALL\b`)
	hashedPin        = regexp.MustCompile(`==\S+.*--hash=sha256:[0-9a-f]{64}`)
	uvResolve        = regexp.MustCompile(`\buv\s+(run|sync)\b`)
	uvLocked         = regexp.MustCompile(`--(locked|frozen)\b`)
	checksumCheck    = regexp.MustCompile(`\b(sha256sum|sha512sum|shasum\s+-a\s+(256|512))\b[^|;&]*\s(-c|--check)\b`)
	aptInstall       = regexp.MustCompile(`\bapt(?:-get)?\s+install\s+([^;&|)}]*)`)
	pipInstall       = regexp.MustCompile(`\bpip3?\s+install\b`)
	npmInstall       = regexp.MustCompile(`\bnpm\s+(install|i)\b`)
	download         = regexp.MustCompile(`\b(?:curl|wget)\s`)
	hostURL          = regexp.MustCompile(`https?://(127\.0\.0\.1|localhost|\[::1\]|\$\{?\w+\}?)(:\d+)?([/\s"']|$)`)
	movingGoQuery    = regexp.MustCompile(`\bgo\s+(run|install)\b[^;&|]*@(latest|upgrade|patch|main|master|HEAD)\b`)
	shellSeparator   = regexp.MustCompile(`&&|\|\||[;|]`)
	dockerBoolFlag   = regexp.MustCompile(`^(-[dDiItTqP]+|--(rm|init|privileged|read-only|interactive|tty|detach|quiet|publish-all|disable-content-trust|oom-kill-disable|no-healthcheck|sig-proxy|all-tags))$`)
	imageInReference = regexp.MustCompile(`[:/]`)
)

// A fresh clone builds the runtime images, fetches the catalog engine and runs the lanes the same way on any host on
// any day only when every external input is read at a pinned value: an image is named by its digest, pip installs
// hashed requirements, a download is checked against a recorded checksum, a VM image is a dated build with its digest,
// apt installs named versions from the Debian snapshot archive at one fixed time, a VM does not take the host's time
// zone, and go run or go install names a version. One image tag names one digest everywhere.
func TestLaneInputsArePinned(t *testing.T) {
	g := newGuard()
	dockerfiles, err := filepath.Glob("../images/runtime/*/Dockerfile")
	require.NoError(t, err)
	require.NotEmpty(t, dockerfiles)
	for _, path := range dockerfiles {
		g.dockerfile(strings.TrimPrefix(path, "../"), readText(t, path))
	}
	requirements, err := filepath.Glob("../images/runtime/*/requirements*.txt")
	require.NoError(t, err)
	for _, path := range requirements {
		g.requirements(strings.TrimPrefix(path, "../"), readText(t, path))
	}
	g.vmTemplates(t)
	var registry map[string]struct {
		Build []string `yaml:"build"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(readText(t, "lanes.yaml")), &registry))
	for name, lane := range registry {
		for i, cmd := range lane.Build {
			g.laneBuild(fmt.Sprintf("scripts/lanes.yaml %s build[%d]", name, i), cmd)
		}
	}
	g.conflicts()
	sort.Strings(g.problems)
	require.Empty(t, g.problems, "unpinned lane inputs:\n%s", strings.Join(g.problems, "\n"))
}

// The guard flags each kind of unpinned input and leaves the pinned and the local ones alone.
func TestLaneInputGuardRules(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("0", 64)
	for _, c := range []struct {
		name, kind, text string
		want             string // a part of the one problem, or "" for none
	}{
		{"FROM by tag", "dockerfile", "FROM python:3.14", "names no sha256 digest"},
		{"FROM scratch", "dockerfile", "FROM scratch", ""},
		{"FROM an earlier stage", "dockerfile", "FROM python:3.14" + digest + " AS py\nFROM py", ""},
		{"COPY from an image by tag", "dockerfile", "COPY --from=busybox:1.36 /bin/sh /sh", "names no sha256 digest"},
		{"COPY from a named build context", "dockerfile", "COPY --from=shim shim.mjs /opt/funcd/", ""},
		{"a download no check follows", "script", "curl -fsSLo f https://example.org/f", "no checksum check"},
		{"sha256sum -c", "script", "curl -fsSLo f https://example.org/f\necho \"$sum  f\" | sha256sum -c -", ""},
		{"sha256sum --check", "script", "curl -fsSLo f https://example.org/f\necho \"$sum  f\" | sha256sum --check -", ""},
		{"a check of the next download only", "script",
			"curl -fsSLo a https://example.org/a\ncurl -fsSLo b https://example.org/b\nsha256sum -c sums", "no checksum check"},
		{"this host", "script", "curl -s -o /dev/null http://127.0.0.1:8080/healthz", ""},
		{"the VM's own service by a variable host", "script", "curl -fsS http://$ip:8080/healthz", ""},
		{"a URL in a variable", "script", "curl -fsSLo f \"$url/f\"", "no checksum check"},
		{"docker run by tag", "build", "docker run --rm -v a:/b -e X=1 -w /src python:3.14 true", "names no sha256 digest"},
		{"docker run by digest", "build", "docker run --rm -it -v a:/b python:3.14" + digest + " true", ""},
		{"docker create by tag", "build", "docker create --platform linux/arm64 node:22 sh", "names no sha256 digest"},
		{"docker pull by tag", "build", "docker pull -q node:22", "names no sha256 digest"},
		{"docker build from an image context by tag", "build",
			"docker build --build-context base=docker-image://alpine:3.20 -t out:1 .", "names no sha256 digest"},
		{"docker build names its output", "build", "docker build -t out:1 -f Dockerfile .", ""},
		{"go run @latest", "build", "go run golang.org/x/tools/cmd/stringer@latest -type=T", "moving version"},
		{"go install @latest", "script", "go install example.org/tool@latest", "moving version"},
		{"go run at a version", "build", "go run golang.org/x/tools/cmd/stringer@v0.30.0", ""},
		{"apt at no version", "script", "apt-get install -y git", "at no named version"},
		{"uv without the lockfile", "build", "uv run funcd-bundle", "without --locked"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newGuard()
			switch c.kind {
			case "dockerfile":
				g.dockerfile("Dockerfile", c.text)
			case "script":
				g.shell("script", logicalLines(c.text))
			case "build":
				g.laneBuild("build", c.text)
			}
			if c.want == "" {
				require.Empty(t, g.problems)
				return
			}
			require.Len(t, g.problems, 1, "%v", g.problems)
			require.Contains(t, g.problems[0], c.want)
		})
	}
}

// guard collects the unpinned inputs it finds, and each digest-pinned image reference by its image name and tag.
type guard struct {
	problems []string
	images   map[string]map[string]bool
}

func newGuard() *guard {
	return &guard{images: map[string]map[string]bool{}}
}

func (g *guard) report(problem string) {
	g.problems = append(g.problems, problem)
}

// image notes a reference pinned by digest, or reports one that names no digest.
func (g *guard) image(where, ref string) {
	m := pinnedImage.FindStringSubmatch(ref)
	if m == nil {
		g.report(fmt.Sprintf("%s: the image %s names no sha256 digest", where, ref))
		return
	}
	if g.images[m[1]] == nil {
		g.images[m[1]] = map[string]bool{}
	}
	g.images[m[1]][ref] = true
}

// conflicts reports each image pinned at more than one digest.
func (g *guard) conflicts() {
	for name, refs := range g.images {
		if len(refs) > 1 {
			g.report(fmt.Sprintf("the image %s is pinned at %d digests: %v", name, len(refs), slices.Sorted(maps.Keys(refs))))
		}
	}
}

// dockerfile checks a Dockerfile: the images its FROM and COPY --from lines name, ADD of a URL, the DuckDB INSTALL
// the duckdb image runs, and the installs and downloads of its RUN lines.
func (g *guard) dockerfile(rel, text string) {
	lines := logicalLines(text)
	g.shell(rel, lines)
	stages := map[string]bool{"scratch": true}
	for i, l := range lines {
		where := fmt.Sprintf("%s:%d", rel, l.n)
		f := strings.Fields(l.text)
		switch strings.ToUpper(f[0]) {
		case "FROM":
			args := slices.DeleteFunc(f[1:], func(a string) bool { return strings.HasPrefix(a, "--") })
			if !stages[strings.ToLower(args[0])] {
				g.image(where, args[0])
			}
			if len(args) == 3 && strings.EqualFold(args[1], "AS") {
				stages[strings.ToLower(args[2])] = true
			}
		case "COPY":
			for _, a := range f[1:] {
				// A name with no tag and no path is a stage or a named build context (--build-context).
				if from, ok := strings.CutPrefix(a, "--from="); ok && !stages[strings.ToLower(from)] &&
					imageInReference.MatchString(from) {
					g.image(where, from)
				}
			}
		case "ADD":
			if strings.Contains(l.text, "://") && !strings.Contains(l.text, "--checksum=sha256:") {
				g.report(where + ": ADD downloads a URL without --checksum")
			}
		case "RUN":
			if duckdbInstall.MatchString(l.text) && !verified(lines[i:]) {
				g.report(where + ": DuckDB INSTALL downloads extensions that no sha256sum -c checks")
			}
		}
	}
}

// requirements checks that a pip requirements file pins each requirement to a version and a hash.
func (g *guard) requirements(rel, text string) {
	for _, l := range logicalLines(text) {
		if !hashedPin.MatchString(l.text) {
			g.report(fmt.Sprintf("%s:%d: %s is not pinned to a version and a hash", rel, l.n, strings.Fields(l.text)[0]))
		}
	}
}

// vmTemplates checks the Lima templates, the scripts they read in, the scripts the lane VMs run and the scripts that
// download for the code base.
func (g *guard) vmTemplates(t *testing.T) {
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
	// Each checked once: the scripts a lane VM runs or a template reads in with file:, and the scripts that download.
	scripts := map[string]bool{"lane-provision.sh": true, "lane-ready.sh": true, "lane-reset.sh": true,
		"lane-leakcheck.sh": true, "linux-exec.sh": true, "fetch-catalog-engine.sh": true}
	for name, tmpl := range templates {
		for _, img := range tmpl.Images {
			if !digestForm.MatchString(img.Digest) || movingLocation.MatchString(img.Location) {
				g.report(fmt.Sprintf("scripts/%s: the VM image %s is not a dated build pinned by digest", name, img.Location))
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
					g.shell("scripts/"+name+" provision", logicalLines(script))
				}
				frozenApt = frozenApt || p.Mode == "dependency" && snapshotArchive.MatchString(script) && snapshotTime.MatchString(script)
			}
		}
		if !frozenApt {
			g.report(fmt.Sprintf("scripts/%s: apt in the VM reads the live Debian archive, not a snapshot at a fixed time (a mode: dependency provision step)", name))
		}
		if !timezone {
			g.report(fmt.Sprintf("scripts/%s: the VM takes the host's time zone", name))
		}
		for _, p := range tmpl.Probes {
			g.shell("scripts/"+name+" probe", logicalLines(p.Script))
		}
	}
	for name := range scripts {
		g.shell("scripts/"+name, logicalLines(readText(t, name)))
	}
}

// laneBuild checks one build command of a lane: uv reads the lockfile, and the command's installs, downloads and
// images are pinned.
func (g *guard) laneBuild(where, cmd string) {
	if uvResolve.MatchString(cmd) && !uvLocked.MatchString(cmd) {
		g.report(where + ": uv resolves without --locked or --frozen")
	}
	g.shell(where, logicalLines(cmd))
}

// shell checks a script's lines: an apt install of a package at no named version, pip reading an index without
// --require-hashes (with --no-index it installs a local file, which the download rule covers), npm install instead of
// npm ci, go run or go install at a moving version, an image a docker command reads by tag only, and a curl or wget
// with no checksum check before the next one, unless it reaches this host or a host in a variable (the VM's or a
// sandbox's own address).
func (g *guard) shell(where string, lines []line) {
	for i, l := range lines {
		at := fmt.Sprintf("%s:%d", where, l.n)
		for _, m := range aptInstall.FindAllStringSubmatch(l.text, -1) {
			for _, pkg := range strings.Fields(m[1]) {
				if !strings.HasPrefix(pkg, "-") && !strings.Contains(pkg, "=") {
					g.report(fmt.Sprintf("%s: apt installs %s at no named version", at, pkg))
				}
			}
		}
		if pipInstall.MatchString(l.text) && !strings.Contains(l.text, "--require-hashes") && !strings.Contains(l.text, "--no-index") {
			g.report(at + ": pip installs from an index without --require-hashes")
		}
		if npmInstall.MatchString(l.text) {
			g.report(at + ": npm install resolves; npm ci installs the lockfile")
		}
		if movingGoQuery.MatchString(l.text) {
			g.report(at + ": go run or go install resolves a moving version")
		}
		for _, ref := range dockerImages(l.text) {
			g.image(at, ref)
		}
		if download.MatchString(l.text) && !hostURL.MatchString(l.text) {
			next := i + 1
			for next < len(lines) && !download.MatchString(lines[next].text) {
				next++
			}
			if !verified(lines[i:next]) {
				g.report(at + ": a download with no checksum check before the next one")
			}
		}
	}
}

// dockerImages returns the images the docker commands of a line read: the image of pull, run and create, and each
// docker-image:// build context of build. An image a build names with -t is its output, not an input.
func dockerImages(text string) []string {
	var refs []string
	for _, cmd := range shellSeparator.Split(text, -1) {
		f := strings.Fields(cmd)
		i := slices.Index(f, "docker")
		if i < 0 || i+1 >= len(f) {
			continue
		}
		args := f[i+2:]
		switch f[i+1] {
		case "pull", "run", "create":
			for j := 0; j < len(args); j++ {
				a := args[j]
				if !strings.HasPrefix(a, "-") {
					refs = append(refs, a)
					break
				}
				if !strings.Contains(a, "=") && !dockerBoolFlag.MatchString(a) {
					j++ // the flag's value
				}
			}
		case "build", "buildx":
			for _, a := range args {
				if _, ref, ok := strings.Cut(a, "docker-image://"); ok {
					refs = append(refs, ref)
				}
			}
		}
	}
	return refs
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
	return slices.ContainsFunc(lines, func(l line) bool { return checksumCheck.MatchString(l.text) })
}
