package process_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	shimpython "github.com/pyvvo/funcd-python/shim"
	shimnode "github.com/pyvvo/funcd-typescript/shim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
)

// A driver with a state dir appends --funcd-instance=<id> to every worker's argv and saves the worker; the Node shim,
// the Node pool host and the Python shim still start and serve with that trailing element (ADR-0167).
func TestOpenWorkersServeWithTheInstanceToken(t *testing.T) {
	dir := t.TempDir()
	nodeHandler := func(t *testing.T) (string, string) {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Skip("node not on PATH")
		}
		handler := filepath.Join(dir, "handler.mjs")
		require.NoError(t, os.WriteFile(handler, []byte("export function handle() { return { ok: true }; }\n"), 0o600))
		return node, handler
	}
	cases := map[string]func(t *testing.T) ([]string, map[string]string, string){
		"node": func(t *testing.T) ([]string, map[string]string, string) {
			node, handler := nodeHandler(t)
			shim := filepath.Join(dir, "shim.mjs")
			require.NoError(t, os.WriteFile(shim, shimnode.Shim, 0o600))
			return []string{node, shim}, map[string]string{"FUNCD_ARTIFACT": handler, "FUNCD_HANDLER": "handle"}, "/"
		},
		"pool": func(t *testing.T) ([]string, map[string]string, string) {
			node, handler := nodeHandler(t)
			pool := filepath.Join(dir, "pool.mjs")
			require.NoError(t, os.WriteFile(pool, shimnode.Pool, 0o600))
			manifest, err := json.Marshal([]map[string]string{{"name": "member", "artifact": handler, "handler": "handle"}})
			require.NoError(t, err)
			manifestPath := filepath.Join(dir, "manifest.json")
			require.NoError(t, os.WriteFile(manifestPath, manifest, 0o600))
			return []string{node, pool}, map[string]string{"FUNCD_POOL_MANIFEST": manifestPath}, "/function/member"
		},
		"python": func(t *testing.T) ([]string, map[string]string, string) {
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Skip("python3 not on PATH")
			}
			entry, _, err := shimpython.Extract(filepath.Join(dir, "shim-python"))
			require.NoError(t, err)
			if reason := process.PythonShimLoadError(context.Background(), python, filepath.Dir(entry)); reason != "" {
				t.Skip("python cannot load the shim: " + reason)
			}
			handler := filepath.Join(dir, "handler.py")
			require.NoError(t, os.WriteFile(handler, []byte("def handle(ctx, event):\n    return {'ok': True}\n"), 0o600))
			return []string{python, entry}, map[string]string{"FUNCD_ARTIFACT": handler, "FUNCD_HANDLER": "handle"}, "/"
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			command, env, path := setup(t)
			state := filepath.Join(t.TempDir(), "process")
			rt, err := process.Open(ctx, state, 0, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = rt.Close() })
			inst, err := rt.Create(ctx, runtime.WorkerSpec{
				Namespace: "default", Name: v1alpha1.ObjectName(name), OwnerKind: v1alpha1.KindFunction, Command: command, Env: env,
			})
			require.NoError(t, err)
			require.NoError(t, rt.Start(ctx, inst.ID))

			var port int
			require.Eventually(t, func() bool {
				got, serr := rt.Status(ctx, inst.ID)
				port = got.Port
				return serr == nil && port > 0
			}, 20*time.Second, 20*time.Millisecond, "the shim listens")
			// A pool host listens before its members finish loading, so the first calls may find the member loading.
			var status int
			var body []byte
			assert.Eventually(t, func() bool {
				resp, perr := http.Post("http://127.0.0.1:"+strconv.Itoa(port)+path, "application/json", strings.NewReader(`{}`))
				if perr != nil {
					return false
				}
				body, _ = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				status = resp.StatusCode
				return status == http.StatusOK
			}, 20*time.Second, 20*time.Millisecond, "the shim serves")
			require.Equal(t, http.StatusOK, status, "%s", body)

			b, err := os.ReadFile(filepath.Join(state, "workers.json"))
			require.NoError(t, err)
			var saved []procreg.Entry
			require.NoError(t, json.Unmarshal(b, &saved))
			require.Len(t, saved, 1)
			require.Equal(t, "--funcd-instance="+string(inst.ID), saved[0].Token)
			argv, err := exec.Command("ps", "-ww", "-o", "command=", "-p", strconv.Itoa(saved[0].PID)).Output()
			require.NoError(t, err)
			require.Contains(t, string(argv), saved[0].Token, "the worker's argv carries the token")
			require.True(t, procreg.Owned(saved[0]), "the saved entry names the worker")
		})
	}
}
