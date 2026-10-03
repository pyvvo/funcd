//go:build e2e

package funcd_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #544: without a Python that can load the shim, the Python shim tests skipped, so CI, which had none,
// never ran them. requirePython still skips locally but fails in CI. It runs in a child test process with no
// interpreter reachable, since its skip or failure ends the calling test.
func TestIssue544_PythonShimLaneFailsInCIWithoutInterpreter(t *testing.T) {
	if os.Getenv("FUNCD_ISSUE544_CHILD") != "" {
		requirePython(t, false)
		return
	}
	for _, tc := range []struct {
		name, ci, result string
		fails            bool
	}{
		{name: "local", ci: "", result: "--- SKIP"},
		{name: "ci", ci: "true", result: "--- FAIL", fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestIssue544_PythonShimLaneFailsInCIWithoutInterpreter$", "-test.v")
			cmd.Env = append(os.Environ(), "FUNCD_ISSUE544_CHILD=1", "CI="+tc.ci, "FUNCD_PYTHON=", "PATH="+t.TempDir())
			out, err := cmd.CombinedOutput()
			if tc.fails {
				require.Error(t, err, "%s", out)
			} else {
				require.NoError(t, err, "%s", out)
			}
			assert.Contains(t, string(out), tc.result)
			assert.Contains(t, string(out), "no Python 3.12 or later with fastjsonschema")
		})
	}
}
