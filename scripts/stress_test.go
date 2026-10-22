package scripts_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const stressBase = `package a

import "testing"

// TestA checks Sum.
func TestA(t *testing.T) {
	if Sum(1, 2) != 3 {
		t.Fatal("sum")
	}
}

func TestB(t *testing.T) {
	x := 1
	x++
	if x != 2 {
		t.Fatal("x")
	}
}

func TestC(t *testing.T) {}

func TestSpan(t *testing.T) {
	const s = ` + "`" + `
}
` + "`" + `
	t.Log(s, "one")
}

func helper() int { return 1 }
`

// The stress run (scripts/agent/stress.py, #1144) on a throwaway repository: it lists the Test functions a branch
// adds or changes, a doc comment's line, a deletion-only hunk and an edit in a renamed file included, with the build
// tag their file needs, and not an unchanged test, a changed helper or a Test function in a raw string's Go source;
// a branch that changes no test lists none and runs nothing; a test whose timing races fails the run within its
// budget, and so does one that fails only after its first run; a test whose subtests run in parallel is timed by its
// longest subtest, which its own time leaves out.
func TestStressRun(t *testing.T) {
	t.Parallel()
	script, err := filepath.Abs(filepath.Join("agent", "stress.py"))
	require.NoError(t, err)
	run, commit := fixtureRepo(t)
	commit("main", "base", map[string]string{
		"go.mod":      "module example.com/stress\n\ngo 1.26\n",
		"a/a.go":      "package a\n\nfunc Sum(x, y int) int { return x + y }\n",
		"a/a_test.go": stressBase,
		"a/e2e_test.go": "//go:build e2e\n\npackage a\n\nimport \"testing\"\n\n" +
			"func TestE(t *testing.T) {\n\tt.Log(\"e2e\")\n}\n",
	})
	changed := strings.NewReplacer("// TestA checks Sum.", "// TestA checks Sum of two ints.", "\tx++\n", "",
		"func helper() int { return 1 }", "func helper() int { return 2 }", `"one"`, `"two"`).Replace(stressBase)
	commit("changed", "test: change tests", map[string]string{
		"a/a_test.go": changed,
		"a/e2e_test.go": "//go:build e2e\n\npackage a\n\nimport \"testing\"\n\n" +
			"func TestE(t *testing.T) {\n\tt.Log(\"e2e!\")\n}\n",
		"a/raw_test.go": "package a\n\nimport \"testing\"\n\nconst src = `\nfunc TestFake(t *testing.T) {\n}\n`\n\n" +
			"var tick = \"`\"\n\nfunc TestQuote(t *testing.T) {\n\tt.Log(tick)\n}\n\n// a ` in a comment\n\n" +
			"func TestRaw(t *testing.T) {\n\tif src == \"\" {\n\t\tt.Fatal(\"src\")\n\t}\n}\n",
	})
	for _, step := range [][]string{
		{"git", "checkout", "-q", "-b", "renamed", "main"},
		{"git", "mv", "a/e2e_test.go", "a/moved_e2e_test.go"},
		{"perl", "-pi", "-e", `s/"e2e"/"moved"/`, "a/moved_e2e_test.go"},
		{"git", "commit", "-q", "-am", "test: move a test file"},
	} {
		out, code := run(step[0], step[1:]...)
		require.Zero(t, code, out)
	}
	commit("notest", "fix: change code only", map[string]string{
		"a/a.go": "package a\n\n// Sum adds.\nfunc Sum(x, y int) int { return x + y }\n",
	})
	commit("racy", "test: a timing race", map[string]string{"a/racy_test.go": `package a

import (
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"
)

// TestRacy waits 2 ms for a goroutine that sleeps 0 or 4 ms, so it fails about half its runs.
func TestRacy(t *testing.T) {
	var done atomic.Bool
	go func() {
		time.Sleep(time.Duration(rand.IntN(2)*4) * time.Millisecond)
		done.Store(true)
	}()
	time.Sleep(2 * time.Millisecond)
	if !done.Load() {
		t.Fatal("the goroutine was late")
	}
}
`})
	commit("later", "test: fails on its third run", map[string]string{"a/later_test.go": `package a

import "testing"

var laterRuns int

// TestLater passes its first run and fails the third of each go test process: one failure the timing run misses.
func TestLater(t *testing.T) {
	if laterRuns++; laterRuns == 3 {
		t.Fatal("the third run failed")
	}
}
`})
	commit("parallel", "test: parallel subtests", map[string]string{"a/par_test.go": `package a

import (
	"testing"
	"time"
)

// TestPar's parallel subtests take 300 ms, which go test does not count in TestPar's own time.
func TestPar(t *testing.T) {
	for _, name := range []string{"one", "two"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			time.Sleep(300 * time.Millisecond)
		})
	}
}
`})
	stress := func(head string, args ...string) (string, int) {
		// the run tests the checkout, as the gate's does; --list reads the head's commits only
		out, code := run("git", "checkout", "-q", head)
		require.Zero(t, code, out)
		// the host check sees no process of this test as orphaned and counts no socket
		return run("env", append([]string{"FUNCD_ORPHAN_TEST_SUFFIX=.no-such-binary", "FUNCD_TIME_WAIT_COUNT=0", "GOWORK=off",
			"python3", script, "--base", "main", "--head", head}, args...)...)
	}

	out, code := stress("changed", "--list")
	require.Zero(t, code, out)
	require.Equal(t, "./a - TestA\n./a - TestB\n./a - TestSpan\n./a e2e TestE\n./a - TestQuote\n./a - TestRaw\n", out,
		"TestC and the helper are unchanged or no test, TestFake is a raw string's source, and a raw string's `}`, "+
			"a quoted or a commented backtick ends no test")

	out, code = stress("renamed", "--list")
	require.Zero(t, code, out)
	require.Equal(t, "./a e2e TestE\n", out, "a renamed file's edited test")

	out, code = stress("notest", "--list")
	require.Zero(t, code, out)
	require.Empty(t, out)
	out, code = stress("notest")
	require.Zero(t, code, out)
	require.Contains(t, out, "the branch adds or changes no test")

	start := time.Now()
	out, code = stress("racy", "--budget", "60")
	require.Equal(t, 1, code, out)
	require.Regexp(t, `TestRacy \./a: \d+ runs, [1-9]\d* failed, .*: FAIL`, out)
	require.Contains(t, out, "the goroutine was late")
	require.Less(t, time.Since(start), 2*time.Minute, "within its budget, compiles included")

	out, code = stress("later", "--budget", "60")
	require.Equal(t, 1, code, out)
	require.Regexp(t, `TestLater \./a: \d+ runs, 1 failed, .*: FAIL`, out)
	require.Contains(t, out, "the third run failed")

	out, code = stress("parallel", "--budget", "10")
	require.Zero(t, code, out)
	require.Regexp(t, `TestPar \./a: \d+ runs, 0 failed, one run 0\.[3-9]\d s, longest 0\.[3-9]\d s`, out,
		"timed by its subtests, not by its own 0 s")
}
