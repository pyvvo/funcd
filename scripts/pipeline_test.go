package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
			capBlock := regexp.MustCompile(`(?s)const MAX_AGENTS .*?\n}\n`).FindString(string(src))
			require.NotEmpty(t, capBlock, "%s defines the run() cap", f)
			probe := filepath.Join(t.TempDir(), "cap.mjs")
			require.NoError(t, os.WriteFile(probe, []byte(capProbe(capBlock)), 0o644))
			out, err := exec.Command(node, probe).CombinedOutput()
			require.NoError(t, err, "%s: %s", f, out)
			require.Equal(t, "peak 5 done 40", strings.TrimSpace(string(out)),
				"%s runs at most five agents at once, even when args ask for more, and frees a slot when an agent throws", f)
			require.Len(t, regexp.MustCompile(`(^|[^.\w])agent\(`).FindAllString(string(src), -1), 1,
				"%s calls agent() only inside run(), so every agent counts against the cap", f)
			body := strings.Replace(string(src), "export const meta", "const meta", 1)
			wrapped := filepath.Join(t.TempDir(), "workflow.mjs")
			require.NoError(t, os.WriteFile(wrapped, []byte("async function workflow(args, agent, parallel, pipeline, phase, log) {\n"+body+"\n}\n"), 0o644))
			out, err = exec.Command(node, "--check", wrapped).CombinedOutput()
			require.NoError(t, err, "%s: %s", f, out)
		}
	})
}

// capProbe runs a workflow's run() wrapper against a fake agent: two rounds of 20 calls, every fourth one throwing,
// with args asking for 50 at once. The second round only finishes if the first gave its slots back. It prints the
// peak number of agents running together and how many calls settled.
func capProbe(capBlock string) string {
	return `const args = { maxAgents: 50 }
let running = 0, peak = 0, calls = 0
const agent = async () => {
  running++
  peak = Math.max(peak, running)
  await new Promise(resolve => setTimeout(resolve, 5))
  running--
  if (++calls % 4 === 0) throw new Error("agent failed")
  return "ok"
}
` + capBlock + `
const round = () => Promise.allSettled(Array.from({ length: 20 }, () => run("p", {})))
const done = (await round()).length + (await round()).length
console.log("peak " + peak + " done " + done)
`
}
