package function

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/testkit/langmod"
)

// scenario: the-shims-stop-checking — the pinned Node and Python shims, solo and pooled, refuse #1339's ops@localhost
// with the delivered contract (their engines want a dot in the domain) and run the handler with the open contract
// funcd gives them (ADR-0303 Decision 7). funcd's RFC 5321 rule accepts the value (ADR-0301).
func TestScenarioTheShimsStopChecking(t *testing.T) {
	t.Parallel()
	node, nerr := exec.LookPath("node")
	python, perr := exec.LookPath("python3.14")
	if perr != nil {
		python, perr = exec.LookPath("python3")
	}
	if nerr != nil || perr != nil {
		t.Skip("node and python3 run the pinned shims")
	}
	pyShim, pyPool := langmod.PythonShim(t)
	runtimes := map[string]struct {
		file, source string
		solo, pool   []string
	}{
		"node":   {"handler.mjs", "export function handle() { return { ok: true }; }\n", []string{node, langmod.NodeShim(t)}, []string{node, langmod.PoolShim(t)}},
		"python": {"handler.py", "def handle(context, event):\n    return {\"ok\": True}\n", []string{python, pyShim}, []string{python, pyPool}},
	}
	for name, rt := range runtimes {
		for _, pooled := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "-solo", true: "-pooled"}[pooled], func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, rt.file), []byte(rt.source), 0o600))
				blob := `{"dialect": "funcd-contract-test", "input": {"type": "object", "properties": {"email": {"type": "string",
					"format": "email"}}, "required": ["email"]}, "output": {}}`
				require.NoError(t, os.WriteFile(filepath.Join(dir, "__funcd_contract.json"), []byte(blob), 0o600))
				open, err := writeOpenContract(dir)
				require.NoError(t, err)
				for contract, want := range map[string]int{"__funcd_contract.json": http.StatusUnprocessableEntity, open: http.StatusOK} {
					cmd, path := rt.solo, "/"
					if pooled {
						cmd, path = rt.pool, "/function/fn"
					}
					port := startShim(t, dir, cmd, rt.file, filepath.Join(dir, contract), pooled)
					// A pool host answers 503 until its member has loaded, as funcd's readiness probe sees it.
					var got int
					require.Eventually(t, func() bool {
						resp, err := http.Post("http://127.0.0.1:"+port+path, "application/json",
							strings.NewReader(`{"specversion": "1.0", "id": "1", "source": "test", "type": "t", "data": {"email": "ops@localhost"}}`))
						if err != nil {
							return false
						}
						_ = resp.Body.Close()
						got = resp.StatusCode
						return got != http.StatusServiceUnavailable
					}, 30*time.Second, 50*time.Millisecond)
					require.Equal(t, want, got, "%s with %s", name, contract)
				}
			})
		}
	}
}

// startShim starts a shim or pool host on artifact in dir, with contract as its contract, and returns its port.
func startShim(t *testing.T, dir string, command []string, artifact, contract string, pooled bool) string {
	t.Helper()
	run := t.TempDir()
	portFile := filepath.Join(run, "port")
	env := append(os.Environ(), "FUNCD_PORTFILE="+portFile, "FUNCD_FUNCTION=fn")
	if pooled {
		manifest, err := json.Marshal([]map[string]string{{"name": "fn", "artifact": filepath.Join(dir, artifact),
			"handler": "handle", "contract": contract}})
		require.NoError(t, err)
		path := filepath.Join(run, "manifest.json")
		require.NoError(t, os.WriteFile(path, manifest, 0o600))
		env = append(env, "FUNCD_POOL_MANIFEST="+path)
	} else {
		env = append(env, "FUNCD_ARTIFACT="+filepath.Join(dir, artifact), "FUNCD_HANDLER=handle", "FUNCD_CONTRACT_PATH="+contract)
	}
	var logs bytes.Buffer
	cmd := exec.Command(command[0], command[1:]...) //nolint:gosec // the pinned shims, from the test's own module
	cmd.Env, cmd.Stdout, cmd.Stderr = env, &logs, &logs
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	var port string
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(portFile)
		port = strings.TrimSpace(string(b))
		return err == nil && port != ""
	}, 30*time.Second, 50*time.Millisecond, "the shim never wrote its port: %s", &logs)
	return port
}
