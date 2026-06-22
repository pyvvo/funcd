package funcd_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// buildKVExample runs the kv-counter example's esbuild (→ counter.mjs), reusing the shim's node_modules
// so it resolves esbuild offline (same pattern as buildFnToFnExample). Node-gated.
func buildKVExample(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	exDir := filepath.Join(root, "examples", "js", "kv-counter")
	if _, serr := os.Stat(filepath.Join(exDir, "node_modules")); serr != nil {
		shimNM := filepath.Join(root, "shim", "nodejs", "node_modules")
		if _, e := os.Stat(shimNM); e != nil {
			t.Skip("shim node_modules absent (run: just build-shim)")
		}
		require.NoError(t, os.Symlink(shimNM, filepath.Join(exDir, "node_modules")))
	}
	cmd := exec.Command(node, "--experimental-strip-types", "build.ts")
	cmd.Dir = exDir
	if b, berr := cmd.CombinedOutput(); berr != nil {
		t.Fatalf("kv-counter build: %v\n%s", berr, b)
	}
	return exDir
}

// scenario (e2e): kv-counter-via-context-kv (ADR-0069) — the REAL path: build the kv-counter handler,
// push it to an OCI layout, apply it, then POST twice. The handler reads+increments a per-name counter
// through context.kv (→ worker-node local API UDS → PDP Facade → durable driver), so the count goes 1
// then 2 across invocations — proving functions can use durable KV end-to-end.
func TestScenarioE2EKVCounterViaContextKV(t *testing.T) {
	c, dpURL := shimPlatformOCI(t)
	exDir := buildKVExample(t)
	layout := t.TempDir()
	// push WITH the I/O contract (counter-{input,output}.schema.json baked by build.ts) — the kv-counter
	// function is contract-validated exactly like fn-to-fn, so the artifact carries its schemas.
	ref, digest := pushExampleFn(t, layout, exDir, "counter")

	root, _ := filepath.Abs(filepath.Join("..", ".."))
	data, err := os.ReadFile(filepath.Join(root, "examples", "js", "kv-counter", "counter.yaml"))
	require.NoError(t, err)
	var fn v1.Function
	require.NoError(t, yaml.Unmarshal(data, &fn), "parse counter.yaml")
	fn.Spec.Artifact = v1.ArtifactRef{URI: ref, Digest: digest}
	applyFnObj(t, c, &fn)
	waitReady(t, c, "counter")

	call := func() int {
		resp, err := http.Post(dpURL+"/function/counter", "application/json", strings.NewReader(`{"data":{"name":"alice"}}`))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		require.Equal(t, http.StatusOK, resp.StatusCode, "kv-counter invoked: %s", body)
		var out struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		}
		require.NoError(t, json.Unmarshal(body, &out))
		return out.Count
	}

	require.Equal(t, 1, call(), "first invoke → count 1")
	require.Equal(t, 2, call(), "second invoke → count 2 (KV persisted across invocations)")
}
