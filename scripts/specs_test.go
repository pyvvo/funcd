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

// The design-spec checker (ADR-0272) runs the pinned FizzBee of the dev shell on a copy of testdata/specs/gate.fizz:
// a gate that opens once, with a mutant that skips its guard.

// gateSpec copies the gate spec into a fresh directory, applying each old→new replacement, and returns its path.
func gateSpec(t *testing.T, replace ...string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "specs", "gate.fizz"))
	require.NoError(t, err)
	text := string(src)
	for i := 0; i+1 < len(replace); i += 2 {
		require.Contains(t, text, replace[i])
		text = strings.Replace(text, replace[i], replace[i+1], 1)
	}
	spec := filepath.Join(t.TempDir(), "gate.fizz")
	require.NoError(t, os.WriteFile(spec, []byte(text), 0o644))
	return spec
}

// specs runs the checker with its cache off, so every check runs FizzBee.
func specs(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("python3", append([]string{"specs.py"}, args...)...)
	cmd.Env = append(os.Environ(), "SPECS_CACHE=")
	return exitCode(t, cmd)
}

// cachedCheck checks spec with the cache at cache, through a fizz wrapper that logs each call and carries note, and
// returns the exit code, the output and how many times fizz ran so far.
func cachedCheck(t *testing.T, spec, cache, note string, env ...string) (int, string, int) {
	t.Helper()
	real, err := exec.LookPath("fizz")
	require.NoError(t, err)
	dir := filepath.Dir(cache)
	calls := filepath.Join(dir, "calls")
	fizz := filepath.Join(dir, "fizz")
	wrapper := "#!/bin/sh\n# " + note + "\necho run >> " + calls + "\nexec " + real + " \"$@\"\n"
	require.NoError(t, os.WriteFile(fizz, []byte(wrapper), 0o755))
	cmd := exec.Command("python3", "specs.py", "check", spec)
	cmd.Env = append(os.Environ(), append([]string{"FIZZ=" + fizz, "SPECS_CACHE=" + cache}, env...)...)
	code, out := exitCode(t, cmd)
	logged, err := os.ReadFile(calls)
	if err != nil && !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	return code, out, strings.Count(string(logged), "run\n")
}

// A check reuses a spec's pass while its text, the fizz command and the state budget stay the same, so CI runs no
// FizzBee for the specs a change leaves alone, and prints the result it reused.
func TestSpecCheckReusesAPass(t *testing.T) {
	spec := gateSpec(t)
	code, out := specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	cache := filepath.Join(t.TempDir(), "cache")
	code, out, runs := cachedCheck(t, spec, cache, "one")
	require.Equal(t, 0, code, out)
	require.NotContains(t, out, "reused")
	require.Positive(t, runs)

	code, out, again := cachedCheck(t, spec, cache, "one")
	require.Equal(t, 0, code, out)
	require.Equal(t, runs, again, "a reused pass runs no fizz")
	require.Contains(t, out, "gate.fizz: pass (")
	require.Contains(t, out, " states, reused)")
	require.Contains(t, out, "gate.fizz mutant skip-guard: fails AtMostOneOpen (reused)")
	require.Contains(t, out, "gate.md: current")
	require.Contains(t, out, "1 specs, 0 failing")
}

// A reused pass still compares the diagram: an edited diagram fails the check without a FizzBee run.
func TestSpecCheckReusedPassComparesTheDiagram(t *testing.T) {
	spec := gateSpec(t)
	code, out := specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	cache := filepath.Join(t.TempDir(), "cache")
	code, out, runs := cachedCheck(t, spec, cache, "one")
	require.Equal(t, 0, code, out)
	require.NoError(t, os.WriteFile(strings.TrimSuffix(spec, ".fizz")+".md", []byte("edited\n"), 0o644))
	code, out, again := cachedCheck(t, spec, cache, "one")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "gate.md: STALE, run `just spec-diagrams`")
	require.Equal(t, runs, again)
}

// A change to the spec's text, to the fizz command or to the state budget runs FizzBee again.
func TestSpecCheckRunsAgainWhenAnInputChanges(t *testing.T) {
	for _, tc := range []struct {
		name, edit, note string
		env              []string
	}{
		{name: "the spec", edit: "\n# a new comment\n", note: "one"},
		{name: "the fizz command", note: "two"},
		{name: "the state budget", note: "one", env: []string{"SPECS_MAX_STATES=50000"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := gateSpec(t)
			code, out := specs(t, "diagrams", spec)
			require.Equal(t, 0, code, out)
			cache := filepath.Join(t.TempDir(), "cache")
			code, out, runs := cachedCheck(t, spec, cache, "one")
			require.Equal(t, 0, code, out)
			if tc.edit != "" {
				text, err := os.ReadFile(spec)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(spec, append(text, tc.edit...), 0o644))
			}
			code, out, again := cachedCheck(t, spec, cache, tc.note, tc.env...)
			require.Equal(t, 0, code, out)
			require.NotContains(t, out, "reused")
			require.Greater(t, again, runs)
		})
	}
}

// A failing check keeps nothing, also one whose model passes and whose mutant does not fail: the next check runs
// FizzBee again and fails again.
func TestSpecCheckKeepsNoFailure(t *testing.T) {
	spec := gateSpec(t, "# mutant skip-guard:", "# mutant skip-nothing:")
	code, out := specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	cache := filepath.Join(t.TempDir(), "cache")
	code, out, runs := cachedCheck(t, spec, cache, "one")
	require.Equal(t, 1, code, out)
	code, out, again := cachedCheck(t, spec, cache, "one")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "gate.fizz mutant skip-nothing: PASSES")
	require.Greater(t, again, runs)
}

// scenario: specs-checked
func TestScenarioSpecsChecked(t *testing.T) {
	spec := gateSpec(t)
	code, out := specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	code, out = specs(t, "check", spec)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "gate.fizz: pass (")
	require.Contains(t, out, "gate.fizz mutant skip-guard: fails AtMostOneOpen")
	require.Contains(t, out, "gate.md: current")
	require.Contains(t, out, "1 specs, 0 failing")
}

// scenario: broken-assertion
func TestScenarioBrokenAssertion(t *testing.T) {
	spec := gateSpec(t, `if MUTANT != "skip-guard":`, `if MUTANT == "never":`)
	code, out := specs(t, "check", spec)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "gate.fizz: FAILS AtMostOneOpen")
	require.Contains(t, out, "Close", "the failing trace is printed")
	require.Contains(t, out, "1 specs, 1 failing: gate.fizz")
}

// scenario: toothless-mutant
func TestScenarioToothlessMutant(t *testing.T) {
	spec := gateSpec(t, "# mutant skip-guard:", "# mutant skip-nothing:")
	code, out := specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	code, out = specs(t, "check", spec)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "gate.fizz mutant skip-nothing: PASSES, so the guard it skips protects nothing gate.fizz asserts")
}

// scenario: diagram-drift
func TestScenarioDiagramDrift(t *testing.T) {
	spec := gateSpec(t)
	code, out := specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	drifted, err := os.ReadFile(spec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(spec, []byte(strings.ReplaceAll(string(drifted), `"open"`, `"opened"`)), 0o644))
	code, out = specs(t, "check", spec)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "gate.md: STALE, run `just spec-diagrams`")
	code, out = specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	code, out = specs(t, "check", spec)
	require.Equal(t, 0, code, out)
}

// A diagram shows each state of the named variable, the transitions with the action that takes each, the initial
// state, the end where no action is enabled, and no end the max_actions bound only cuts.
func TestSpecDiagramStatesAndTransitions(t *testing.T) {
	spec := gateSpec(t)
	code, out := specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	md, err := os.ReadFile(strings.TrimSuffix(spec, ".fizz") + ".md")
	require.NoError(t, err)
	require.Equal(t, "<!-- Generated by scripts/specs.py from gate.fizz (ADR-0272); do not edit. -->\n\n# gate\n\n"+
		"```mermaid\n---\ntitle: gate (phase)\n---\nstateDiagram-v2\n"+
		"    state \"closed\" as s0\n    state \"open\" as s1\n"+
		"    [*] --> s0\n    s0 --> s1 : Open\n    s1 --> s0 : Close\n    s0 --> [*]\n```\n", string(md))
}

// An Init that chooses starts the diagram where it ends: its choices are the initial states, and its steps no edges.
func TestSpecDiagramStartsWhereInitEnds(t *testing.T) {
	spec := gateSpec(t, `    phase = "closed"`, `    phase = oneof ["closed", "open"]`)
	code, out := specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	md, err := os.ReadFile(strings.TrimSuffix(spec, ".fizz") + ".md")
	require.NoError(t, err)
	require.NotContains(t, string(md), "null")
	require.NotContains(t, string(md), ": Init")
	require.Contains(t, string(md), "    [*] --> s0\n    [*] --> s1\n")
}

// A spec names its bounds: FizzBee's default of two concurrent actions hid #1217.
func TestSpecWithoutBoundsIsRefused(t *testing.T) {
	spec := gateSpec(t, "  max_concurrent_actions: 2\n", "")
	code, out := specs(t, "check", spec)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "gate.fizz: sets no max_concurrent_actions; a spec names its bounds")
}

// A run over the state budget fails: the budget counts states, so the check does not depend on the host's speed.
func TestSpecOverTheStateBudgetFails(t *testing.T) {
	spec := gateSpec(t)
	code, out := specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	cmd := exec.Command("python3", "specs.py", "check", spec)
	cmd.Env = append(os.Environ(), "SPECS_MAX_STATES=1", "SPECS_CACHE=")
	code, out = exitCode(t, cmd)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "gate.fizz: over the 1-state budget")
}

// A mutant that fails another assertion than the one it names is a miss: it does not protect what it claims.
func TestSpecMutantFailingAnotherAssertion(t *testing.T) {
	spec := gateSpec(t, "# mutant skip-guard: AtMostOneOpen", "# mutant skip-guard: NeverOpen")
	code, out := specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	code, out = specs(t, "check", spec)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "gate.fizz mutant skip-guard: fails AtMostOneOpen, not NeverOpen")
}

// A mutant that breaks a transition assertion is named by it, as one that breaks an always assertion is.
func TestSpecMutantFailingATransitionAssertion(t *testing.T) {
	spec := gateSpec(t, "always assertion AtMostOneOpen:\n    return opened <= 1",
		"transition assertion AtMostOneOpen(before, after):\n    return after.opened <= 1")
	code, out := specs(t, "diagrams", spec)
	require.Equal(t, 0, code, out)
	code, out = specs(t, "check", spec)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "gate.fizz mutant skip-guard: fails AtMostOneOpen")
}

// A marker may name a transition assertion, whose declaration carries its (before, after) parameters.
func TestSpecOracleMarkerNamesATransitionAssertion(t *testing.T) {
	root := t.TempDir()
	design := filepath.Join(root, "docs", "design", "demo")
	require.NoError(t, os.MkdirAll(design, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o755))
	script, err := os.ReadFile("specs.py")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "specs.py"), script, 0o644))
	spec := gateSpec(t, "always assertion AtMostOneOpen:\n    return opened <= 1",
		"transition assertion AtMostOneOpen(before, after):\n    return after.opened <= 1")
	body, err := os.ReadFile(spec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(design, "gate.fizz"), body, 0o644))
	marker := "// oracle" + ": docs/design/demo/gate.fizz#"
	require.NoError(t, os.WriteFile(filepath.Join(root, "gate_test.go"), []byte("package demo\n\n"+marker+"AtMostOneOpen\n"), 0o644))
	code, out := exitCode(t, exec.Command("git", "init", "-q", root))
	require.Equal(t, 0, code, out)
	run := func(args ...string) (int, string) {
		return exitCode(t, exec.Command("python3", append([]string{filepath.Join(root, "scripts", "specs.py")}, args...)...))
	}
	code, out = run("diagrams", filepath.Join(design, "gate.fizz"))
	require.Equal(t, 0, code, out)
	code, out = run("check")
	require.Equal(t, 0, code, out)
	require.NotContains(t, out, "oracle marker names no assertion")
}

// A full check finds every oracle marker in the code: one that names no assertion of an existing spec fails it, and an
// assertion with no marker is reported.
func TestSpecOracleMarkers(t *testing.T) {
	root := t.TempDir()
	design := filepath.Join(root, "docs", "design", "demo")
	require.NoError(t, os.MkdirAll(design, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o755))
	script, err := os.ReadFile("specs.py")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "specs.py"), script, 0o644))
	spec, err := os.ReadFile(filepath.Join("testdata", "specs", "gate.fizz"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(design, "gate.fizz"), spec, 0o644))
	// Split, so the repository's own check does not read these test markers as markers of this file.
	marker := "// oracle" + ": docs/design/demo/gate.fizz#"
	require.NoError(t, os.WriteFile(filepath.Join(root, "gate_test.go"), []byte("package demo\n\n"+
		marker+"AtMostOneOpen\n"+marker+"Missing\n"), 0o644))
	code, out := exitCode(t, exec.Command("git", "init", "-q", root))
	require.Equal(t, 0, code, out)
	run := func(args ...string) (int, string) {
		return exitCode(t, exec.Command("python3", append([]string{filepath.Join(root, "scripts", "specs.py")}, args...)...))
	}
	code, out = run("diagrams", filepath.Join(design, "gate.fizz"))
	require.Equal(t, 0, code, out)
	code, out = run("check")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "gate_test.go:4: oracle marker names no assertion docs/design/demo/gate.fizz#Missing")
	require.NotContains(t, out, "gate.fizz#AtMostOneOpen: no oracle test yet")
	require.Contains(t, out, "1 specs, 1 failing: oracle markers")
}

// scenario: pushed-spec-checked
func TestScenarioPushedSpecChecked(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "lefthook.yml"))
	require.NoError(t, err)
	var hooks struct {
		PrePush struct {
			Commands map[string]struct {
				Glob     string `yaml:"glob"`
				UseStdin bool   `yaml:"use_stdin"`
				Run      string `yaml:"run"`
			} `yaml:"commands"`
		} `yaml:"pre-push"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &hooks))
	hook := hooks.PrePush.Commands["specs"]
	require.Equal(t, "docs/design/**/*.fizz", hook.Glob, "the hook runs only when a push carries a spec")
	require.True(t, hook.UseStdin, "the hook reads the pushed refs that git gives a pre-push hook")

	root, origin := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@funcd.invalid",
			"-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	write := func(f, body string) {
		require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(f)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, f), []byte(body), 0o644))
	}
	git("init", "-q", "-b", "main")
	git("init", "-q", "--bare", origin)
	git("remote", "add", "origin", origin)
	// The stand-in for scripts/agent/d and its log stay out of every commit.
	write(".git/info/exclude", "scripts/\nd.log\n")
	write("docs/design/demo/base.fizz", "base")
	write("docs/design/demo/old.fizz", "old")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("push", "-q", "origin", "main")

	// push runs the hook's command as lefthook does, with git's pre-push line for the branch on its stdin and a
	// stand-in for scripts/agent/d that logs its arguments and exits with code; it returns the exit code and the call.
	log := filepath.Join(root, "d.log")
	push := func(local, remote, code string) (int, string) {
		write("scripts/agent/d", "#!/bin/sh\necho \"$*\" >> '"+log+"'\nexit "+code+"\n")
		require.NoError(t, os.Chmod(filepath.Join(root, "scripts", "agent", "d"), 0o755))
		_ = os.Remove(log)
		cmd := exec.Command("sh", "-c", hook.Run)
		cmd.Dir = root
		cmd.Stdin = strings.NewReader("refs/heads/feature " + local + " refs/heads/feature " + remote + "\n")
		status, _ := exitCode(t, cmd)
		called, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return status, ""
		}
		require.NoError(t, err)
		return status, strings.TrimSpace(string(called))
	}
	zero := strings.Repeat("0", 40)

	git("switch", "-q", "-c", "feature")
	write("docs/design/demo/gate.fizz", "gate")
	require.NoError(t, os.Remove(filepath.Join(root, "docs/design/demo/old.fizz")))
	write("docs/notes.md", "notes")
	git("add", "-A")
	git("commit", "-q", "-m", "feature")
	first := git("rev-parse", "HEAD")
	status, called := push(first, zero, "0")
	require.Equal(t, 0, status)
	require.Equal(t, "python3 scripts/specs.py check docs/design/demo/gate.fizz", called,
		"a branch's first push checks the spec its commits add, not every spec the branch holds, and not the deleted one")

	status, called = push(first, zero, "1")
	require.NotEqual(t, 0, status, "a failing spec stops the push")
	require.Equal(t, "python3 scripts/specs.py check docs/design/demo/gate.fizz", called)

	git("push", "-q", "origin", "feature")
	write("docs/design/demo/base.fizz", "base, changed")
	git("commit", "-q", "-am", "change base")
	second := git("rev-parse", "HEAD")
	status, called = push(second, first, "0")
	require.Equal(t, 0, status)
	require.Equal(t, "python3 scripts/specs.py check docs/design/demo/base.fizz", called,
		"a later push checks only the specs of the commits the remote does not have")

	git("rm", "-q", "docs/design/demo/base.fizz")
	git("commit", "-q", "-m", "drop base")
	status, called = push(git("rev-parse", "HEAD"), second, "0")
	require.Equal(t, 0, status)
	require.Empty(t, called, "a push that only deletes a spec checks none")
}
