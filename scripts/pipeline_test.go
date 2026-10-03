package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The committed fix pipeline (issue #566): every agent shell script parses, and each workflow script parses the way
// the workflow runner reads it, as the body of an async function, and names no machine path.
func TestIssue566_PipelineScriptsParse(t *testing.T) {
	scripts, err := filepath.Glob(filepath.Join("agent", "*.sh"))
	require.NoError(t, err)
	scripts = append(scripts, "lane-lock.sh")
	for _, s := range scripts {
		out, err := exec.Command("bash", "-n", s).CombinedOutput()
		require.NoError(t, err, "%s: %s", s, out)
	}

	t.Run("workflows", func(t *testing.T) {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Skip("node not on PATH")
		}
		flows, err := filepath.Glob(filepath.Join("..", ".claude", "workflows", "*.js"))
		require.NoError(t, err)
		require.NotEmpty(t, flows)
		for _, f := range flows {
			src, err := os.ReadFile(f)
			require.NoError(t, err)
			require.NotRegexp(t, `/(Users|home)/[a-z]`, string(src), "%s names a machine path; take it from args", f)
			body := strings.Replace(string(src), "export const meta", "const meta", 1)
			wrapped := filepath.Join(t.TempDir(), "workflow.mjs")
			require.NoError(t, os.WriteFile(wrapped, []byte("async function workflow(args, agent, parallel, pipeline, phase, log) {\n"+body+"\n}\n"), 0o644))
			out, err := exec.Command(node, "--check", wrapped).CombinedOutput()
			require.NoError(t, err, "%s: %s", f, out)
		}
	})
}
