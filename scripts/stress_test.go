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

func helper() int { return 1 }
`

// The stress run (scripts/agent/stress.py, #1144) on a throwaway repository: it lists the Test functions a branch
// adds or changes, a doc comment's line and a deletion-only hunk included, with the build tag their file needs, and
// not an unchanged test or a changed helper; a branch that changes no test lists none and runs nothing; and a test
// whose timing races fails the run within its budget.
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
		"func helper() int { return 1 }", "func helper() int { return 2 }").Replace(stressBase)
	commit("changed", "test: change tests", map[string]string{
		"a/a_test.go":   changed,
		"a/e2e_test.go": "//go:build e2e\n\npackage a\n\nimport \"testing\"\n\nfunc TestE(t *testing.T) {\n\tt.Log(\"e2e!\")\n}\n",
	})
	commit("notest", "fix: change code only", map[string]string{"a/a.go": "package a\n\n// Sum adds.\nfunc Sum(x, y int) int { return x + y }\n"})
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
	stress := func(head string, args ...string) (string, int) {
		// the host check sees no process of this test as orphaned and counts no socket
		return run("env", append([]string{"FUNCD_ORPHAN_TEST_SUFFIX=.no-such-binary", "FUNCD_TIME_WAIT_COUNT=0", "GOWORK=off",
			"python3", script, "--base", "main", "--head", head}, args...)...)
	}

	out, code := stress("changed", "--list")
	require.Zero(t, code, out)
	require.Equal(t, "./a - TestA\n./a - TestB\n./a e2e TestE\n", out, "TestC and the helper are unchanged or no test")

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
}
