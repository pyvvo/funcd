package function

import (
	"bytes"
	"encoding/json"
	"io"
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

// scenario: the-shims-read-no-contract — the Node and Python shims funcd pins, solo and pooled, run a handler whose
// module still exports validators that refuse everything, with a delivered contract that the call and the result
// break, named the way funcd named one before ADR-0304: they read neither, and answer 200 with the handler's result.
func TestScenarioTheShimsReadNoContract(t *testing.T) {
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
		"node": {"handler.mjs", "export const __funcdValidateInput = () => [{ message: 'rejected' }];\n" +
			"export const __funcdValidateOutput = () => [{ message: 'rejected' }];\n" +
			"export function handle() { return { ok: 'yes' }; }\n", []string{node, langmod.NodeShim(t)}, []string{node, langmod.PoolShim(t)}},
		"python": {"handler.py", "def __funcd_validate_input(d):\n    return ['rejected']\n\n\n" +
			"def __funcd_validate_output(d):\n    return ['rejected']\n\n\n" +
			"def handle(context, event):\n    return {'ok': 'yes'}\n", []string{python, pyShim}, []string{python, pyPool}},
	}
	blob := `{"dialect": "funcd-contract-test", "input": {"type": "object", "properties": {"email": {"type": "string"}},
		"required": ["email"]}, "output": {"type": "object", "properties": {"ok": {"type": "boolean"}}, "required": ["ok"]}}`
	for name, rt := range runtimes {
		for _, pooled := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "-solo", true: "-pooled"}[pooled], func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, rt.file), []byte(rt.source), 0o600))
				contract := filepath.Join(dir, "__funcd_contract.json")
				require.NoError(t, os.WriteFile(contract, []byte(blob), 0o600))
				cmd, path := rt.solo, "/"
				if pooled {
					cmd, path = rt.pool, "/function/fn"
				}
				port := startShim(t, dir, cmd, rt.file, contract, pooled)
				// A pool host answers 503 until its member has loaded, as funcd's readiness probe sees it.
				var got int
				var body []byte
				require.Eventually(t, func() bool {
					resp, err := http.Post("http://127.0.0.1:"+port+path, "application/json",
						strings.NewReader(`{"specversion": "1.0", "id": "1", "source": "test", "type": "t", "data": {"email": 5}}`))
					if err != nil {
						return false
					}
					body, _ = io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					got = resp.StatusCode
					return got != http.StatusServiceUnavailable
				}, 30*time.Second, 50*time.Millisecond)
				require.Equal(t, http.StatusOK, got, "%s: %s", name, body)
				require.JSONEq(t, `{"ok": "yes"}`, string(body))
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
